package js

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const xpathDoc = `<html lang="en-GB"><head><title>T</title></head><body>
<div id="a" class="box" hx-on:click="x()"><p id="p1">one <b>bold</b></p><p id="p2" data-n="5">two</p><!--note--><span>3</span></div>
<ul id="l"><li class="i">a</li><li class="i sel">b</li><li>c</li></ul>
<table><tr><td>1</td><td>2</td></tr><tr><td>10</td><td>20</td></tr></table>
<svg id="s"><foreignObject><circle r="4"/></foreignObject></svg>
</body></html>`

func xpathTestDoc(t testing.TB) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(xpathDoc))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// describe renders a result for comparison: node-sets as a space-joined list
// of ids / names / @attr / #text.
func xpathDescribe(v xvalue) string {
	set, ok := v.([]xnode)
	if !ok {
		return fmt.Sprintf("%T:%s", v, xtoString(v))
	}
	var parts []string
	for _, x := range set {
		switch {
		case x.attr >= 0:
			parts = append(parts, "@"+x.n.Attr[x.attr].Key)
		case x.n.Type == html.TextNode:
			parts = append(parts, "#"+strings.TrimSpace(x.n.Data))
		case x.n.Type == html.CommentNode:
			parts = append(parts, "!"+x.n.Data)
		case x.n.Type == html.DocumentNode:
			parts = append(parts, "/")
		case getAttr(x.n, "id") != "":
			parts = append(parts, getAttr(x.n, "id"))
		default:
			parts = append(parts, x.n.Data)
		}
	}
	return strings.Join(parts, " ")
}

func TestXPathEvaluate(t *testing.T) {
	doc := xpathTestDoc(t)
	body := findTag(doc, "body")
	cases := []struct{ expr, want string }{
		// paths and axes
		{"/", "/"},
		{"/html/body/div", "a"},
		{"//p", "p1 p2"},
		{"//P", "p1 p2"}, // HTML names are case-insensitive
		{"//div/p[2]", "p2"},
		{"//div/*", "p1 p2 span"},
		{"//div/node()", "p1 p2 !note span"},
		{"//div/comment()", "!note"},
		{"//p/text()", "#one #two"},
		{"//b/..", "p1"},
		{"//b/parent::*", "p1"},
		{"//b/ancestor::*", "html body a p1"},
		{"//b/ancestor-or-self::*[1]", "b"},
		{"//b/ancestor::*[1]", "p1"}, // proximity position on a reverse axis
		{"//p[1]/following-sibling::*", "p2 span"},
		{"//span/preceding-sibling::*", "p1 p2"},
		{"//span/preceding-sibling::*[1]", "p2"},
		{"//ul/following::td[1]", "td"},
		{"count(//li/preceding::p)", "float64:2"},
		{"//li[last()]", "li"},
		{"//li[position() < 3]/@class", "@class @class"},
		{"//@id", "@id @id @id @id @id"},
		{"//div/@*", "@id @class @hx-on:click"},
		{"//div/attribute::class/..", "a"},
		{"descendant::li[2]", "li"},
		{"./ul | ./div", "a l"}, // union is in document order
		{"(//li)[2]", "li"},
		{"(//li | //p)[last()]", "li"},
		{"//svg//circle", "circle"},
		{"//foreignobject", "foreignObject"},
		{"//x:div", "a"}, // a prefix is ignored
		{"self::body", "body"},
		{"//namespace::*", ""},
		// predicates and functions
		{"//*[@id='p2']", "p2"},
		{"//li[contains(@class, 'sel')]", "li"},
		{"//li[not(@class)]", "li"},
		{"//*[starts-with(@id, 'p')]", "p1 p2"},
		{"//*[@*[starts-with(name(), 'hx-on:')]]", "a"},
		{`.//*[@*[ starts-with(name(), "hx-on:") or starts-with(name(), "data-hx-on:") or starts-with(name(), "hx-on-") or starts-with(name(), "data-hx-on-") ]]`, "a"}, // htmx's expression
		{"//p[b]", "p1"},
		{"//p[normalize-space() = 'two']", "p2"},
		{"//td[. > 5]", "td td"},
		{"//tr[td = 10]", "tr"},
		{"//p[@data-n + 1 = 6]", "p2"},
		{"id('l p1')", "p1 l"},
		{"//li[text() = 'b' or text() = 'c']", "li li"},
		{"//*[lang('en')]/@lang", "@lang"},
		{"//*[local-name() = 'circle']/@r", "@r"},
		// values
		{"count(//li)", "float64:3"},
		{"sum(//td)", "float64:33"},
		{"string(//p)", "string:one bold"},
		{"string(//p[2]/@data-n)", "string:5"},
		{"name(//*[@id='a'])", "string:div"},
		{"concat('a', 1, true())", "string:a1true"},
		{"substring('12345', 1.5, 2.6)", "string:234"},
		{"substring('12345', 0, 3)", "string:12"},
		{"substring('12345', 2)", "string:2345"},
		{"substring-before('1999/04/01', '/')", "string:1999"},
		{"substring-after('1999/04/01', '/')", "string:04/01"},
		{"translate('bar', 'abc', 'ABC')", "string:BAr"},
		{"translate('--aaa--', 'abc-', 'ABC')", "string:AAA"},
		{"string-length('héllo')", "float64:5"},
		{"normalize-space('  a   b ')", "string:a b"},
		{"1 + 2 * 3 - 4 div 2", "float64:5"},
		{"7 mod 3", "float64:1"},
		{"-7 mod 3", "float64:-1"},
		{"- - 2", "float64:2"},
		{"round(2.5)", "float64:3"},
		{"round(-2.5)", "float64:-2"},
		{"floor(-1.5)", "float64:-2"},
		{"ceiling(1.2)", "float64:2"},
		{"number('  12.5 ')", "float64:12.5"},
		{"string(number('1e3'))", "string:NaN"},
		{"string(1 div 0)", "string:Infinity"},
		{"string(0.1 + 0.2 = 0.3)", "string:false"},
		{"boolean(//nope)", "bool:false"},
		{"//nope = //p", "bool:false"},
		{"//li != //li", "bool:true"},
		{"//li[1] != //li[1]", "bool:false"},
		{"2 > 1 and 1 >= 1 and not(1 > 2)", "bool:true"},
		{"true() = 'x'", "bool:true"},
		{"'a' = 'a' or 1 div 0", "bool:true"},
		// operator-name disambiguation: these are element names, not operators
		{"count(//div | //and | //or | //mod)", "float64:1"},
		{"count(//*) * 0 + count(//li)*2", "float64:6"},
		{"string(//td[1] div //td[2])", "string:0.5"},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			e, err := xpathCompile(c.expr)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			v, err := xpathEvaluate(e, treeNode(body))
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := xpathDescribe(v); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestXPathSyntaxErrors(t *testing.T) {
	for _, expr := range []string{
		"", "//", "//p[", "//p]", "foo(", "nosuch()", "count()", "count(1, 2)", "1 +", "$var", "//p[@]",
		"child::", "bogus::p", "'unterminated", "//p/", "a b", "1 = = 2", "#", "..5x",
		strings.Repeat("(", 200) + "1" + strings.Repeat(")", 200), // nesting bound
		strings.Repeat("-", 200) + "1",
		"//" + strings.Repeat("a", maxXPathLen), // length bound
	} {
		if _, err := xpathCompile(expr); !errors.Is(err, errXPathSyntax) {
			name := expr
			if len(name) > 30 {
				name = name[:30] + "…"
			}
			t.Errorf("%q: want a syntax error, got %v", name, err)
		}
	}
}

func TestXPathTypeErrors(t *testing.T) {
	doc := xpathTestDoc(t)
	for _, expr := range []string{"count(1)", "sum('a')", "1 | 2", "(1)/p", "name(1)"} {
		e, err := xpathCompile(expr)
		if err != nil {
			t.Fatalf("%q: compile: %v", expr, err)
		}
		if _, err := xpathEvaluate(e, treeNode(doc)); !errors.Is(err, errXPathType) {
			t.Errorf("%q: want a type error, got %v", expr, err)
		}
	}
}

// A quadratic-or-worse expression over a large tree stops at the step budget
// instead of holding the event loop.
func TestXPathStepBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for i := 0; i < 3000; i++ {
		sb.WriteString("<div><span>x</span></div>")
	}
	sb.WriteString("</body></html>")
	doc, err := html.Parse(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"//*[//*[//*]]", "//*[count(//*) > 0]", "//*/following::*/preceding::*"} {
		e, err := xpathCompile(expr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := xpathEvaluate(e, treeNode(doc)); !errors.Is(err, errXPathBudget) {
			t.Errorf("%q: want the budget error, got %v", expr, err)
		}
	}
	// A linear expression over the same tree is well inside the budget.
	e, _ := xpathCompile("count(//span)")
	v, err := xpathEvaluate(e, treeNode(doc))
	if err != nil || xtoNumber(v) != 3000 {
		t.Errorf("count(//span) = %v, %v", v, err)
	}
}

// FuzzXPath: the expression is untrusted. Compiling and evaluating any input
// must terminate without panicking, and every failure must be one of the three
// declared errors.
func FuzzXPath(f *testing.F) {
	for _, seed := range []string{
		"//p", "/html/body/div[@id='a']/p[2]", "count(//li) + 1", "//*[@*[starts-with(name(),'hx-on:')]]",
		"//td[. > 5] | //li[last()]", "substring('12345', 1.5, 2.6)", "//b/ancestor::*[1]", "id('l')/li[position() mod 2 = 1]",
		"- - 2 * 3 div 4 mod 5", "(//li)[2]/following::*", "//p[", "a::b", "''", "1e3", "..", "//*[//*[//*]]",
		"translate(., 'abc', 'A')", "string(//@*)", "//x:y", "@*", "*[*]", ".//.", "processing-instruction('x')",
	} {
		f.Add(seed)
	}
	doc, err := html.Parse(strings.NewReader(xpathDoc))
	if err != nil {
		f.Fatal(err)
	}
	ctx := treeNode(findTag(doc, "div"))
	f.Fuzz(func(t *testing.T, expr string) {
		e, err := xpathCompile(expr)
		if err != nil {
			if !errors.Is(err, errXPathSyntax) {
				t.Fatalf("compile(%q): undeclared error %v", expr, err)
			}
			return
		}
		v, err := xpathEvaluate(e, ctx)
		if err != nil {
			if !errors.Is(err, errXPathSyntax) && !errors.Is(err, errXPathType) && !errors.Is(err, errXPathBudget) {
				t.Fatalf("evaluate(%q): undeclared error %v", expr, err)
			}
			return
		}
		switch v.(type) {
		case []xnode, bool, float64, string:
		default:
			t.Fatalf("evaluate(%q): unexpected value type %T", expr, v)
		}
		_ = xtoString(v)
	})
}
