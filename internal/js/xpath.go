package js

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// An XPath 1.0 evaluator over the *html.Node tree, behind document.evaluate /
// XPathEvaluator (xpathapi.go). It is the language of the XPath 1.0
// Recommendation minus what an HTML document cannot exercise:
//
//   - Supported: the full expression grammar (or/and, comparisons with the
//     node-set rules, arithmetic, unions, filter expressions, paths), every
//     axis except `namespace` (which is always empty), name / `*` / node-type
//     tests, predicates with proximity positions, and the whole core function
//     library except id()-by-DTD: id() matches the `id` attribute.
//   - Approximated: names are matched case-insensitively against HTML elements
//     and attributes, and a namespace prefix on a name test is ignored (the
//     local part is matched), so `//x:div` with an XHTML resolver and `//div`
//     select the same nodes. Variables ($x) are a syntax error: the DOM API
//     has no way to bind one.
//
// The expression and the document are both untrusted. Parsing is bounded by
// expression length and nesting depth; evaluation by a step budget (every node
// visited and every predicate evaluated costs one), so a quadratic expression
// such as //*[//*[//*]] fails with an error instead of holding the event loop.

const (
	maxXPathLen   = 16 << 10 // bytes of expression source
	maxXPathDepth = 64       // parser recursion (parentheses, predicates, nesting)
	maxXPathSteps = 2_000_000
)

var (
	errXPathSyntax = errors.New("xpath: syntax error")
	errXPathBudget = errors.New("xpath: evaluation budget exceeded")
	errXPathType   = errors.New("xpath: value cannot be converted to a node-set")
)

// xnode is an XPath node: a tree node, or — when attr >= 0 — the attr'th
// attribute of n.
type xnode struct {
	n    *html.Node
	attr int
}

func treeNode(n *html.Node) xnode { return xnode{n: n, attr: -1} }

// xvalue is one of []xnode, bool, float64, string.
type xvalue interface{}

// --- lexer ---

type xtokKind int

const (
	xtEOF xtokKind = iota
	xtName
	xtNumber
	xtLiteral
	xtOp // punctuation and operators, text in val
)

type xtok struct {
	kind xtokKind
	val  string
	num  float64
}

func isXNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isXNameChar(c byte) bool {
	return isXNameStart(c) || c == '-' || c == '.' || (c >= '0' && c <= '9')
}

func xpathLex(src string) ([]xtok, error) {
	var toks []xtok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"' || c == '\'':
			end := strings.IndexByte(src[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated string", errXPathSyntax)
			}
			toks = append(toks, xtok{kind: xtLiteral, val: src[i+1 : i+1+end]})
			i += end + 2
		case (c >= '0' && c <= '9') || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			if j < len(src) && src[j] == '.' {
				j++
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			f, err := strconv.ParseFloat(src[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("%w: bad number", errXPathSyntax)
			}
			toks = append(toks, xtok{kind: xtNumber, num: f})
			i = j
		case isXNameStart(c):
			j := i + 1
			for j < len(src) && isXNameChar(src[j]) {
				j++
			}
			// QName: prefix:local or prefix:* (but not the axis separator "::").
			if j+1 < len(src) && src[j] == ':' && src[j+1] != ':' {
				if src[j+1] == '*' {
					j += 2
				} else if isXNameStart(src[j+1]) {
					j += 2
					for j < len(src) && isXNameChar(src[j]) {
						j++
					}
				}
			}
			toks = append(toks, xtok{kind: xtName, val: src[i:j]})
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch two {
			case "//", "::", "..", "!=", "<=", ">=":
				toks = append(toks, xtok{kind: xtOp, val: two})
				i += 2
				continue
			}
			if strings.IndexByte("/()[]@,.|+-=<>*", c) < 0 {
				return nil, fmt.Errorf("%w: unexpected %q", errXPathSyntax, string(c))
			}
			toks = append(toks, xtok{kind: xtOp, val: string(c)})
			i++
		}
	}
	return append(toks, xtok{kind: xtEOF}), nil
}

// --- AST ---

type xexpr interface {
	eval(c *xctx) (xvalue, error)
}

type xbinary struct {
	op   string
	l, r xexpr
}

type xneg struct{ e xexpr }

type xliteral struct{ v xvalue }

type xcall struct {
	name string
	args []xexpr
}

// xstep is one location step.
type xstep struct {
	axis  string
	test  string // "*", a lowercased local name, or a node type: "node()" "text()" "comment()" "processing-instruction()"
	preds []xexpr
}

// xpath is a location path, optionally rooted at a filter expression.
type xpath struct {
	abs    bool  // starts at the root
	filter xexpr // non-nil: the primary expression the steps apply to
	fpreds []xexpr
	steps  []xstep
}

// --- parser ---

type xparser struct {
	toks  []xtok
	pos   int
	depth int
}

func (p *xparser) peek() xtok { return p.toks[p.pos] }

func (p *xparser) isOp(v string) bool {
	t := p.toks[p.pos]
	return t.kind == xtOp && t.val == v
}

func (p *xparser) accept(v string) bool {
	if p.isOp(v) {
		p.pos++
		return true
	}
	return false
}

// operatorName reports whether the current name token is one of the operator
// names (and/or/div/mod) in operator position. Per the XPath lexical rules a
// name is an operator only when a preceding token exists and is not one that
// expects an operand.
func (p *xparser) operatorName(name string) bool {
	t := p.toks[p.pos]
	return t.kind == xtName && t.val == name && p.afterOperand()
}

// afterOperand reports whether the previous token ends an operand, which is what
// makes a following `*` a multiplication and a following and/or/div/mod an
// operator.
func (p *xparser) afterOperand() bool {
	if p.pos == 0 {
		return false
	}
	prev := p.toks[p.pos-1]
	switch prev.kind {
	case xtName, xtNumber, xtLiteral:
		return true
	case xtOp:
		switch prev.val {
		case ")", "]", ".", "..", "*":
			// A `*` that was itself a name test ends an operand; one that was a
			// multiplication is followed by an operand, never by an operator, so
			// treating it as an operand end is safe for well-formed input.
			return true
		}
	}
	return false
}

func (p *xparser) enter() error {
	p.depth++
	if p.depth > maxXPathDepth {
		return fmt.Errorf("%w: expression nested too deeply", errXPathSyntax)
	}
	return nil
}

func (p *xparser) leave() { p.depth-- }

func xpathCompile(src string) (xexpr, error) {
	if len(src) > maxXPathLen {
		return nil, fmt.Errorf("%w: expression too long", errXPathSyntax)
	}
	toks, err := xpathLex(src)
	if err != nil {
		return nil, err
	}
	p := &xparser{toks: toks}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != xtEOF {
		return nil, fmt.Errorf("%w: unexpected trailing input", errXPathSyntax)
	}
	return e, nil
}

func (p *xparser) parseExpr() (xexpr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()
	return p.parseBinary(0)
}

// binary operator precedence levels, loosest first.
var xpathLevels = [][]string{
	{"or"}, {"and"}, {"=", "!="}, {"<", "<=", ">", ">="}, {"+", "-"}, {"*", "div", "mod"},
}

func (p *xparser) matchLevelOp(level int) string {
	t := p.peek()
	for _, op := range xpathLevels[level] {
		switch op {
		case "or", "and", "div", "mod":
			if p.operatorName(op) {
				return op
			}
		case "*":
			if t.kind == xtOp && t.val == "*" && p.afterOperand() {
				return op
			}
		default:
			if t.kind == xtOp && t.val == op {
				return op
			}
		}
	}
	return ""
}

func (p *xparser) parseBinary(level int) (xexpr, error) {
	if level == len(xpathLevels) {
		return p.parseUnary()
	}
	left, err := p.parseBinary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op := p.matchLevelOp(level)
		if op == "" {
			return left, nil
		}
		p.pos++
		right, err := p.parseBinary(level + 1)
		if err != nil {
			return nil, err
		}
		left = &xbinary{op: op, l: left, r: right}
	}
}

func (p *xparser) parseUnary() (xexpr, error) {
	if p.accept("-") {
		if err := p.enter(); err != nil {
			return nil, err
		}
		defer p.leave()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &xneg{e: e}, nil
	}
	return p.parseUnion()
}

func (p *xparser) parseUnion() (xexpr, error) {
	left, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	for p.accept("|") {
		right, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		left = &xbinary{op: "|", l: left, r: right}
	}
	return left, nil
}

var xpathNodeTypes = map[string]bool{"node": true, "text": true, "comment": true, "processing-instruction": true}

// startsPrimary reports whether the next tokens begin a primary expression
// (so the path is a filter expression) rather than a location step.
func (p *xparser) startsPrimary() bool {
	t := p.peek()
	switch t.kind {
	case xtNumber, xtLiteral:
		return true
	case xtOp:
		return t.val == "("
	case xtName:
		next := p.toks[p.pos+1]
		return next.kind == xtOp && next.val == "(" && !xpathNodeTypes[t.val]
	}
	return false
}

func (p *xparser) parsePath() (xexpr, error) {
	path := &xpath{}
	if p.startsPrimary() {
		prim, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		preds, err := p.parsePredicates()
		if err != nil {
			return nil, err
		}
		if !p.isOp("/") && !p.isOp("//") {
			if len(preds) == 0 {
				return prim, nil
			}
			return &xpath{filter: prim, fpreds: preds}, nil
		}
		path.filter, path.fpreds = prim, preds
	} else if p.isOp("/") {
		p.pos++
		path.abs = true
		if !p.startsStep() {
			return path, nil // "/" alone: the root
		}
		return p.parseSteps(path)
	} else if p.isOp("//") {
		path.abs = true
	} else {
		return p.parseSteps(path)
	}
	// Here the next token is "/" or "//" continuing a filter or absolute path.
	for p.isOp("/") || p.isOp("//") {
		if p.accept("//") {
			path.steps = append(path.steps, xstep{axis: "descendant-or-self", test: "node()"})
		} else {
			p.pos++
		}
		st, err := p.parseStep()
		if err != nil {
			return nil, err
		}
		path.steps = append(path.steps, st)
	}
	return path, nil
}

func (p *xparser) startsStep() bool {
	t := p.peek()
	switch t.kind {
	case xtName:
		return true
	case xtOp:
		switch t.val {
		case ".", "..", "@", "*":
			return true
		}
	}
	return false
}

func (p *xparser) parseSteps(path *xpath) (xexpr, error) {
	st, err := p.parseStep()
	if err != nil {
		return nil, err
	}
	path.steps = append(path.steps, st)
	for p.isOp("/") || p.isOp("//") {
		if p.accept("//") {
			path.steps = append(path.steps, xstep{axis: "descendant-or-self", test: "node()"})
		} else {
			p.pos++
		}
		st, err := p.parseStep()
		if err != nil {
			return nil, err
		}
		path.steps = append(path.steps, st)
	}
	return path, nil
}

var xpathAxes = map[string]bool{
	"ancestor": true, "ancestor-or-self": true, "attribute": true, "child": true,
	"descendant": true, "descendant-or-self": true, "following": true,
	"following-sibling": true, "namespace": true, "parent": true, "preceding": true,
	"preceding-sibling": true, "self": true,
}

func (p *xparser) parseStep() (xstep, error) {
	if p.accept(".") {
		return xstep{axis: "self", test: "node()"}, nil
	}
	if p.accept("..") {
		return xstep{axis: "parent", test: "node()"}, nil
	}
	st := xstep{axis: "child"}
	if p.accept("@") {
		st.axis = "attribute"
	} else if t := p.peek(); t.kind == xtName {
		if next := p.toks[p.pos+1]; next.kind == xtOp && next.val == "::" {
			if !xpathAxes[t.val] {
				return st, fmt.Errorf("%w: unknown axis %q", errXPathSyntax, t.val)
			}
			st.axis = t.val
			p.pos += 2
		}
	}
	t := p.peek()
	switch {
	case t.kind == xtOp && t.val == "*":
		p.pos++
		st.test = "*"
	case t.kind == xtName:
		p.pos++
		if next := p.peek(); next.kind == xtOp && next.val == "(" && xpathNodeTypes[t.val] {
			p.pos++
			if t.val == "processing-instruction" && p.peek().kind == xtLiteral {
				p.pos++ // a target literal: no PI ever matches in an HTML tree anyway
			}
			if !p.accept(")") {
				return st, fmt.Errorf("%w: expected ) after node type", errXPathSyntax)
			}
			st.test = t.val + "()"
		} else {
			name := t.val
			if i := strings.IndexByte(name, ':'); i >= 0 {
				name = name[i+1:] // the prefix is ignored (see file comment)
			}
			st.test = strings.ToLower(name)
		}
	default:
		return st, fmt.Errorf("%w: expected a node test", errXPathSyntax)
	}
	preds, err := p.parsePredicates()
	if err != nil {
		return st, err
	}
	st.preds = preds
	return st, nil
}

func (p *xparser) parsePredicates() ([]xexpr, error) {
	var preds []xexpr
	for p.accept("[") {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.accept("]") {
			return nil, fmt.Errorf("%w: expected ]", errXPathSyntax)
		}
		preds = append(preds, e)
	}
	return preds, nil
}

func (p *xparser) parsePrimary() (xexpr, error) {
	t := p.peek()
	switch t.kind {
	case xtNumber:
		p.pos++
		return &xliteral{v: t.num}, nil
	case xtLiteral:
		p.pos++
		return &xliteral{v: t.val}, nil
	case xtOp:
		if t.val == "(" {
			p.pos++
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.accept(")") {
				return nil, fmt.Errorf("%w: expected )", errXPathSyntax)
			}
			return e, nil
		}
	case xtName:
		p.pos += 2 // name and "("
		call := &xcall{name: t.val}
		if !p.accept(")") {
			for {
				a, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				call.args = append(call.args, a)
				if p.accept(")") {
					break
				}
				if !p.accept(",") {
					return nil, fmt.Errorf("%w: expected , or ) in arguments", errXPathSyntax)
				}
			}
		}
		def, ok := xpathFuncs[call.name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown function %s()", errXPathSyntax, call.name)
		}
		if len(call.args) < def.min || (def.max >= 0 && len(call.args) > def.max) {
			return nil, fmt.Errorf("%w: wrong number of arguments to %s()", errXPathSyntax, call.name)
		}
		return call, nil
	}
	return nil, fmt.Errorf("%w: expected an expression", errXPathSyntax)
}

// --- evaluation ---

// xeval is the state shared by one evaluation: the step budget and the lazily
// built document-order index.
type xeval struct {
	steps int
	order map[*html.Node]int
	root  *html.Node
}

func (e *xeval) spend(n int) error {
	e.steps += n
	if e.steps > maxXPathSteps {
		return errXPathBudget
	}
	return nil
}

// xctx is the evaluation context: node, proximity position and size.
type xctx struct {
	ev   *xeval
	node xnode
	pos  int
	size int
}

func (e *xeval) index() map[*html.Node]int {
	if e.order != nil {
		return e.order
	}
	e.order = make(map[*html.Node]int)
	i := 0
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		e.order[n] = i
		i++
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(e.root)
	return e.order
}

// sortDocOrder sorts nodes into document order and removes duplicates. An
// attribute sorts after its element and before the element's children.
func (e *xeval) sortDocOrder(nodes []xnode) []xnode {
	if len(nodes) < 2 {
		return nodes
	}
	idx := e.index()
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.n != b.n {
			return idx[a.n] < idx[b.n]
		}
		return a.attr < b.attr
	})
	out := nodes[:1]
	for _, n := range nodes[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}

func xstringValue(x xnode) string {
	if x.attr >= 0 {
		return x.n.Attr[x.attr].Val
	}
	switch x.n.Type {
	case html.TextNode, html.CommentNode:
		return x.n.Data
	}
	return textContent(x.n)
}

func xtoString(v xvalue) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		switch {
		case math.IsNaN(t):
			return "NaN"
		case math.IsInf(t, 1):
			return "Infinity"
		case math.IsInf(t, -1):
			return "-Infinity"
		case t == 0:
			return "0"
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []xnode:
		if len(t) == 0 {
			return ""
		}
		return xstringValue(t[0])
	}
	return ""
}

func xstringToNumber(s string) float64 {
	s = strings.Trim(s, " \t\r\n")
	if s == "" {
		return math.NaN()
	}
	// XPath's Number: optional '-', digits with an optional fraction. No
	// exponent, no '+', no hex, no "Infinity".
	body := strings.TrimPrefix(s, "-")
	dot := false
	digits := 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return math.NaN()
		}
	}
	if digits == 0 {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

func xtoNumber(v xvalue) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		return xstringToNumber(t)
	case []xnode:
		return xstringToNumber(xtoString(t))
	}
	return math.NaN()
}

func xtoBool(v xvalue) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case string:
		return t != ""
	case []xnode:
		return len(t) > 0
	}
	return false
}

func (l *xliteral) eval(*xctx) (xvalue, error) { return l.v, nil }

func (n *xneg) eval(c *xctx) (xvalue, error) {
	v, err := n.e.eval(c)
	if err != nil {
		return nil, err
	}
	return -xtoNumber(v), nil
}

func xcompareAtoms(op string, a, b xvalue) bool {
	switch op {
	case "=", "!=":
		var eq bool
		_, ab := a.(bool)
		_, bb := b.(bool)
		_, an := a.(float64)
		_, bn := b.(float64)
		switch {
		case ab || bb:
			eq = xtoBool(a) == xtoBool(b)
		case an || bn:
			eq = xtoNumber(a) == xtoNumber(b)
		default:
			eq = xtoString(a) == xtoString(b)
		}
		return eq == (op == "=")
	}
	x, y := xtoNumber(a), xtoNumber(b)
	switch op {
	case "<":
		return x < y
	case "<=":
		return x <= y
	case ">":
		return x > y
	default:
		return x >= y
	}
}

// xcompare implements the comparison rules, including the existential ones for
// node-sets (true if SOME node satisfies the comparison).
func xcompare(ev *xeval, op string, a, b xvalue) (bool, error) {
	an, aIsSet := a.([]xnode)
	bn, bIsSet := b.([]xnode)
	switch {
	case aIsSet && bIsSet:
		if err := ev.spend(len(an) + len(bn)); err != nil {
			return false, err
		}
		if len(an) == 0 || len(bn) == 0 {
			return false, nil
		}
		if op == "=" || op == "!=" {
			seen := make(map[string]bool, len(an))
			for _, x := range an {
				seen[xstringValue(x)] = true
			}
			for _, y := range bn {
				s := xstringValue(y)
				if op == "=" && seen[s] {
					return true, nil
				}
				if op == "!=" && (len(seen) > 1 || !seen[s]) {
					return true, nil
				}
			}
			return false, nil
		}
		for _, x := range an {
			for _, y := range bn {
				if err := ev.spend(1); err != nil {
					return false, err
				}
				if xcompareAtoms(op, xstringValue(x), xstringValue(y)) {
					return true, nil
				}
			}
		}
		return false, nil
	case aIsSet:
		if _, isBool := b.(bool); isBool {
			return xcompareAtoms(op, len(an) > 0, b), nil
		}
		if err := ev.spend(len(an)); err != nil {
			return false, err
		}
		for _, x := range an {
			if xcompareAtoms(op, xstringValue(x), b) {
				return true, nil
			}
		}
		return false, nil
	case bIsSet:
		if _, isBool := a.(bool); isBool {
			return xcompareAtoms(op, a, len(bn) > 0), nil
		}
		if err := ev.spend(len(bn)); err != nil {
			return false, err
		}
		for _, y := range bn {
			if xcompareAtoms(op, a, xstringValue(y)) {
				return true, nil
			}
		}
		return false, nil
	}
	return xcompareAtoms(op, a, b), nil
}

func (b *xbinary) eval(c *xctx) (xvalue, error) {
	if err := c.ev.spend(1); err != nil {
		return nil, err
	}
	l, err := b.l.eval(c)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case "or":
		if xtoBool(l) {
			return true, nil
		}
		r, err := b.r.eval(c)
		if err != nil {
			return nil, err
		}
		return xtoBool(r), nil
	case "and":
		if !xtoBool(l) {
			return false, nil
		}
		r, err := b.r.eval(c)
		if err != nil {
			return nil, err
		}
		return xtoBool(r), nil
	}
	r, err := b.r.eval(c)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case "|":
		ln, ok1 := l.([]xnode)
		rn, ok2 := r.([]xnode)
		if !ok1 || !ok2 {
			return nil, errXPathType
		}
		merged := make([]xnode, 0, len(ln)+len(rn))
		merged = append(append(merged, ln...), rn...)
		if err := c.ev.spend(len(merged)); err != nil {
			return nil, err
		}
		return c.ev.sortDocOrder(merged), nil
	case "=", "!=", "<", "<=", ">", ">=":
		return xcompare(c.ev, b.op, l, r)
	}
	x, y := xtoNumber(l), xtoNumber(r)
	switch b.op {
	case "+":
		return x + y, nil
	case "-":
		return x - y, nil
	case "*":
		return x * y, nil
	case "div":
		return x / y, nil
	default: // mod: truncating remainder, sign of the dividend
		return math.Mod(x, y), nil
	}
}

// xnameOf is the name a name test is compared against, lowercased: HTML
// element and attribute names are case-insensitive, and foreign (SVG/MathML)
// names are matched the same way, which is what an author writing //svg or
// //foreignobject expects.
func xnameOf(x xnode) string {
	if x.attr >= 0 {
		return strings.ToLower(x.n.Attr[x.attr].Key)
	}
	if x.n.Type == html.ElementNode {
		return strings.ToLower(x.n.Data)
	}
	return ""
}

func (st *xstep) matches(x xnode) bool {
	switch st.test {
	case "node()":
		return true
	case "text()":
		return x.attr < 0 && x.n.Type == html.TextNode
	case "comment()":
		return x.attr < 0 && x.n.Type == html.CommentNode
	case "processing-instruction()":
		return false
	}
	// A name test selects the axis's principal node type: attributes on the
	// attribute axis, elements everywhere else.
	if st.axis == "attribute" {
		if x.attr < 0 {
			return false
		}
	} else if x.attr >= 0 || x.n.Type != html.ElementNode {
		return false
	}
	return st.test == "*" || xnameOf(x) == st.test
}

// axisNodes returns the nodes on st.axis from x, in axis order (reverse
// document order for the reverse axes), spending one step per node.
func (c *xctx) axisNodes(axis string, x xnode) ([]xnode, error) {
	ev := c.ev
	var out []xnode
	add := func(n *html.Node) error {
		out = append(out, treeNode(n))
		return ev.spend(1)
	}
	var descend func(n *html.Node) error
	descend = func(n *html.Node) error {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if err := add(ch); err != nil {
				return err
			}
			if err := descend(ch); err != nil {
				return err
			}
		}
		return nil
	}
	// An attribute's parent is its element; it has no children or siblings.
	if x.attr >= 0 {
		switch axis {
		case "self", "descendant-or-self":
			return []xnode{x}, nil
		case "parent":
			return []xnode{treeNode(x.n)}, nil
		case "ancestor", "ancestor-or-self":
			if axis == "ancestor-or-self" {
				out = append(out, x)
			}
			for a := x.n; a != nil; a = a.Parent {
				if err := add(a); err != nil {
					return nil, err
				}
			}
			return out, nil
		case "following":
			// Everything after the element's start tag except its attributes.
			if err := descend(x.n); err != nil {
				return nil, err
			}
			x = treeNode(x.n)
		case "preceding":
			x = treeNode(x.n)
		default:
			return nil, nil
		}
	}
	n := x.n
	switch axis {
	case "self":
		return []xnode{x}, nil
	case "child":
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if err := add(ch); err != nil {
				return nil, err
			}
		}
	case "descendant":
		if err := descend(n); err != nil {
			return nil, err
		}
	case "descendant-or-self":
		if err := add(n); err != nil {
			return nil, err
		}
		if err := descend(n); err != nil {
			return nil, err
		}
	case "parent":
		if n.Parent != nil {
			out = append(out, treeNode(n.Parent))
		}
	case "ancestor", "ancestor-or-self":
		if axis == "ancestor-or-self" {
			out = append(out, x)
		}
		for a := n.Parent; a != nil; a = a.Parent {
			if err := add(a); err != nil {
				return nil, err
			}
		}
	case "following-sibling":
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if err := add(s); err != nil {
				return nil, err
			}
		}
	case "preceding-sibling":
		for s := n.PrevSibling; s != nil; s = s.PrevSibling {
			if err := add(s); err != nil {
				return nil, err
			}
		}
	case "following":
		for a := n; a != nil; a = a.Parent {
			for s := a.NextSibling; s != nil; s = s.NextSibling {
				if err := add(s); err != nil {
					return nil, err
				}
				if err := descend(s); err != nil {
					return nil, err
				}
			}
		}
	case "preceding":
		// Reverse document order, excluding ancestors: for each ancestor-or-self,
		// its preceding siblings (nearest first), each subtree visited last-to-first.
		var rdescend func(m *html.Node) error
		rdescend = func(m *html.Node) error {
			for ch := m.LastChild; ch != nil; ch = ch.PrevSibling {
				if err := rdescend(ch); err != nil {
					return err
				}
			}
			return add(m)
		}
		for a := n; a != nil; a = a.Parent {
			for s := a.PrevSibling; s != nil; s = s.PrevSibling {
				if err := rdescend(s); err != nil {
					return nil, err
				}
			}
		}
	case "attribute":
		if n.Type == html.ElementNode {
			for i := range n.Attr {
				out = append(out, xnode{n: n, attr: i})
			}
			if err := ev.spend(len(n.Attr)); err != nil {
				return nil, err
			}
		}
	case "namespace":
		// HTML documents expose no namespace nodes.
	}
	return out, nil
}

// applyPredicates filters nodes (in axis order) by each predicate in turn,
// renumbering proximity positions between predicates.
func (c *xctx) applyPredicates(nodes []xnode, preds []xexpr) ([]xnode, error) {
	for _, pred := range preds {
		kept := nodes[:0:0]
		for i, x := range nodes {
			if err := c.ev.spend(1); err != nil {
				return nil, err
			}
			v, err := pred.eval(&xctx{ev: c.ev, node: x, pos: i + 1, size: len(nodes)})
			if err != nil {
				return nil, err
			}
			ok := false
			if f, isNum := v.(float64); isNum {
				ok = f == float64(i+1)
			} else {
				ok = xtoBool(v)
			}
			if ok {
				kept = append(kept, x)
			}
		}
		nodes = kept
	}
	return nodes, nil
}

func (p *xpath) eval(c *xctx) (xvalue, error) {
	var cur []xnode
	switch {
	case p.filter != nil:
		v, err := p.filter.eval(c)
		if err != nil {
			return nil, err
		}
		set, ok := v.([]xnode)
		if !ok {
			return nil, errXPathType
		}
		// Filter-expression predicates number the set in document order.
		if cur, err = c.applyPredicates(set, p.fpreds); err != nil {
			return nil, err
		}
	case p.abs:
		cur = []xnode{treeNode(c.ev.root)}
	default:
		cur = []xnode{c.node}
	}
	for i := range p.steps {
		st := &p.steps[i]
		var next []xnode
		for _, x := range cur {
			cand, err := c.axisNodes(st.axis, x)
			if err != nil {
				return nil, err
			}
			matched := cand[:0]
			for _, y := range cand {
				if st.matches(y) {
					matched = append(matched, y)
				}
			}
			if matched, err = c.applyPredicates(matched, st.preds); err != nil {
				return nil, err
			}
			next = append(next, matched...)
		}
		if len(cur) > 1 || xpathReverseAxis[st.axis] {
			next = c.ev.sortDocOrder(next)
		}
		cur = next
		if len(cur) == 0 {
			break
		}
	}
	return cur, nil
}

var xpathReverseAxis = map[string]bool{
	"ancestor": true, "ancestor-or-self": true, "preceding": true, "preceding-sibling": true,
}

// --- core function library ---

type xfunc struct {
	min, max int // argument count bounds; max < 0 = variadic
	fn       func(c *xctx, args []xvalue) (xvalue, error)
}

func (f *xcall) eval(c *xctx) (xvalue, error) {
	def := xpathFuncs[f.name] // existence and arity were checked by the parser
	if err := c.ev.spend(1); err != nil {
		return nil, err
	}
	args := make([]xvalue, len(f.args))
	for i, a := range f.args {
		v, err := a.eval(c)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return def.fn(c, args)
}

// argNode returns the node a name()/local-name()/string()-style function
// applies to: the first node of its node-set argument, or the context node.
func argNode(c *xctx, args []xvalue) (xnode, bool, error) {
	if len(args) == 0 {
		return c.node, true, nil
	}
	set, ok := args[0].([]xnode)
	if !ok {
		return xnode{}, false, errXPathType
	}
	if len(set) == 0 {
		return xnode{}, false, nil
	}
	return set[0], true, nil
}

func xround(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	return math.Floor(f + 0.5) // round half towards +infinity, as XPath specifies
}

// xsubstr implements substring() with XPath's rounding and NaN/infinity rules,
// over characters (not bytes).
func xsubstr(s string, start float64, length float64, hasLen bool) string {
	r := []rune(s)
	first := xround(start)
	if math.IsNaN(first) {
		return ""
	}
	last := math.Inf(1)
	if hasLen {
		l := xround(length)
		if math.IsNaN(l) {
			return ""
		}
		last = first + l
	}
	var b strings.Builder
	for i, ch := range r {
		p := float64(i + 1)
		if p >= first && p < last {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func xnameFn(local bool) func(c *xctx, args []xvalue) (xvalue, error) {
	return func(c *xctx, args []xvalue) (xvalue, error) {
		x, ok, err := argNode(c, args)
		if err != nil || !ok {
			return "", err
		}
		if x.attr >= 0 {
			name := x.n.Attr[x.attr].Key
			if local {
				if i := strings.IndexByte(name, ':'); i >= 0 {
					name = name[i+1:]
				}
			}
			return name, nil
		}
		if x.n.Type == html.ElementNode {
			return x.n.Data, nil
		}
		return "", nil
	}
}

var xpathFuncs map[string]xfunc

func init() {
	xpathFuncs = map[string]xfunc{
		"last":     {0, 0, func(c *xctx, _ []xvalue) (xvalue, error) { return float64(c.size), nil }},
		"position": {0, 0, func(c *xctx, _ []xvalue) (xvalue, error) { return float64(c.pos), nil }},
		"count": {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) {
			set, ok := a[0].([]xnode)
			if !ok {
				return nil, errXPathType
			}
			return float64(len(set)), nil
		}},
		"id": {1, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			var ids []string
			if set, ok := a[0].([]xnode); ok {
				for _, x := range set {
					ids = append(ids, strings.Fields(xstringValue(x))...)
				}
			} else {
				ids = strings.Fields(xtoString(a[0]))
			}
			var out []xnode
			for _, id := range ids {
				if err := c.ev.spend(1); err != nil {
					return nil, err
				}
				if n := findByID(c.ev.root, id); n != nil {
					out = append(out, treeNode(n))
				}
			}
			return c.ev.sortDocOrder(out), nil
		}},
		"local-name": {0, 1, xnameFn(true)},
		"name":       {0, 1, xnameFn(false)},
		"namespace-uri": {0, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			x, ok, err := argNode(c, a)
			if err != nil || !ok || x.attr >= 0 || x.n.Type != html.ElementNode {
				return "", err
			}
			return namespaceURIFor(x.n.Namespace), nil
		}},
		"string": {0, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			if len(a) == 0 {
				return xstringValue(c.node), nil
			}
			return xtoString(a[0]), nil
		}},
		"concat": {2, -1, func(_ *xctx, a []xvalue) (xvalue, error) {
			var b strings.Builder
			for _, v := range a {
				b.WriteString(xtoString(v))
			}
			return b.String(), nil
		}},
		"starts-with": {2, 2, func(_ *xctx, a []xvalue) (xvalue, error) {
			return strings.HasPrefix(xtoString(a[0]), xtoString(a[1])), nil
		}},
		"contains": {2, 2, func(_ *xctx, a []xvalue) (xvalue, error) {
			return strings.Contains(xtoString(a[0]), xtoString(a[1])), nil
		}},
		"substring-before": {2, 2, func(_ *xctx, a []xvalue) (xvalue, error) {
			s, sep := xtoString(a[0]), xtoString(a[1])
			if i := strings.Index(s, sep); i >= 0 {
				return s[:i], nil
			}
			return "", nil
		}},
		"substring-after": {2, 2, func(_ *xctx, a []xvalue) (xvalue, error) {
			s, sep := xtoString(a[0]), xtoString(a[1])
			if i := strings.Index(s, sep); i >= 0 {
				return s[i+len(sep):], nil
			}
			return "", nil
		}},
		"substring": {2, 3, func(_ *xctx, a []xvalue) (xvalue, error) {
			if len(a) == 3 {
				return xsubstr(xtoString(a[0]), xtoNumber(a[1]), xtoNumber(a[2]), true), nil
			}
			return xsubstr(xtoString(a[0]), xtoNumber(a[1]), 0, false), nil
		}},
		"string-length": {0, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			s := ""
			if len(a) == 0 {
				s = xstringValue(c.node)
			} else {
				s = xtoString(a[0])
			}
			return float64(len([]rune(s))), nil
		}},
		"normalize-space": {0, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			s := ""
			if len(a) == 0 {
				s = xstringValue(c.node)
			} else {
				s = xtoString(a[0])
			}
			// XPath white space is exactly #x20 #x9 #xD #xA.
			return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
				return r == ' ' || r == '\t' || r == '\r' || r == '\n'
			}), " "), nil
		}},
		"translate": {3, 3, func(_ *xctx, a []xvalue) (xvalue, error) {
			from, to := []rune(xtoString(a[1])), []rune(xtoString(a[2]))
			m := make(map[rune]int, len(from))
			for i, r := range from {
				if _, dup := m[r]; !dup {
					m[r] = i
				}
			}
			var b strings.Builder
			for _, r := range xtoString(a[0]) {
				i, ok := m[r]
				switch {
				case !ok:
					b.WriteRune(r)
				case i < len(to):
					b.WriteRune(to[i])
				}
			}
			return b.String(), nil
		}},
		"boolean": {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) { return xtoBool(a[0]), nil }},
		"not":     {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) { return !xtoBool(a[0]), nil }},
		"true":    {0, 0, func(*xctx, []xvalue) (xvalue, error) { return true, nil }},
		"false":   {0, 0, func(*xctx, []xvalue) (xvalue, error) { return false, nil }},
		"lang": {1, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			want := strings.ToLower(xtoString(a[0]))
			for n := c.node.n; n != nil; n = n.Parent {
				if n.Type != html.ElementNode {
					continue
				}
				for _, key := range []string{"xml:lang", "lang"} {
					if hasAttr(n, key) {
						got := strings.ToLower(getAttr(n, key))
						return got == want || strings.HasPrefix(got, want+"-"), nil
					}
				}
			}
			return false, nil
		}},
		"number": {0, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			if len(a) == 0 {
				return xstringToNumber(xstringValue(c.node)), nil
			}
			return xtoNumber(a[0]), nil
		}},
		"sum": {1, 1, func(c *xctx, a []xvalue) (xvalue, error) {
			set, ok := a[0].([]xnode)
			if !ok {
				return nil, errXPathType
			}
			if err := c.ev.spend(len(set)); err != nil {
				return nil, err
			}
			total := 0.0
			for _, x := range set {
				total += xstringToNumber(xstringValue(x))
			}
			return total, nil
		}},
		"floor":   {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) { return math.Floor(xtoNumber(a[0])), nil }},
		"ceiling": {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) { return math.Ceil(xtoNumber(a[0])), nil }},
		"round":   {1, 1, func(_ *xctx, a []xvalue) (xvalue, error) { return xround(xtoNumber(a[0])), nil }},
	}
}

// xpathEvaluate runs a compiled expression with ctxNode as the context node.
// Node-set results are in document order.
func xpathEvaluate(e xexpr, ctxNode xnode) (xvalue, error) {
	root := ctxNode.n
	for root.Parent != nil {
		root = root.Parent
	}
	ev := &xeval{root: root}
	v, err := e.eval(&xctx{ev: ev, node: ctxNode, pos: 1, size: 1})
	if err != nil {
		return nil, err
	}
	if set, ok := v.([]xnode); ok {
		v = ev.sortDocOrder(set)
	}
	return v, nil
}
