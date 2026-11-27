package js

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

// ElementInternals state that is not form participation, and so applies to every
// custom element: its default ARIA semantics (the ARIAMixin on internals) and its
// CustomStateSet (internals.states). The objects themselves are plain JS in
// prelude_api.go; they push each change here because both end up as questions about
// the tree — what extraction reports as the element's role, name and state, and what
// a :state() selector matches.

// ariaDefaults is one custom element's internals-provided default semantics.
type ariaDefaults struct {
	attrs      map[string]string // "role" / "aria-*" attribute name → value
	labelledBy []*html.Node      // internals.ariaLabelledByElements
}

// reflectedARIA is the subset of the ARIAMixin that extraction reads (internal/dom:
// accessible name, control role, and the interactive states). Anything else stays
// settable/readable state in JS and is not reflected.
var reflectedARIA = map[string]bool{
	"role": true, "aria-label": true,
	"aria-checked": true, "aria-expanded": true, "aria-pressed": true, "aria-selected": true,
	"aria-required": true, "aria-invalid": true, "aria-disabled": true,
}

func (b *bridge) ariaDefaultsOf(n *html.Node) *ariaDefaults {
	if b.ariaDefaults == nil {
		b.ariaDefaults = make(map[*html.Node]*ariaDefaults)
	}
	d := b.ariaDefaults[n]
	if d == nil {
		d = &ariaDefaults{attrs: map[string]string{}}
		b.ariaDefaults[n] = d
	}
	return d
}

// installInternalsState adds the ARIA and custom-state setters to the
// __unblinkFormInternals hook object.
func (b *bridge) installInternalsState(h *goja.Object) {
	vm := b.vm
	el := func(v goja.Value) *html.Node {
		if n := b.unwrap(v); n != nil && n.Type == html.ElementNode {
			return n
		}
		return nil
	}
	// setAria(el, attr, value|null): one default ARIA attribute.
	_ = h.Set("setAria", func(call goja.FunctionCall) goja.Value {
		n, key := el(call.Argument(0)), call.Argument(1).String()
		if n == nil || !reflectedARIA[key] {
			return goja.Undefined()
		}
		if v := call.Argument(2); goja.IsNull(v) || goja.IsUndefined(v) {
			delete(b.ariaDefaultsOf(n).attrs, key)
		} else {
			b.ariaDefaultsOf(n).attrs[key] = v.String()
		}
		b.domVersion++ // part of the snapshot (reflectARIADefaults)
		return goja.Undefined()
	})
	// setLabelledBy(el, elements): internals.ariaLabelledByElements.
	_ = h.Set("setLabelledBy", func(call goja.FunctionCall) goja.Value {
		n := el(call.Argument(0))
		if n == nil {
			return goja.Undefined()
		}
		var refs []*html.Node
		if arr, ok := call.Argument(1).(*goja.Object); ok {
			_ = vm.Try(func() {
				vm.ForOf(arr, func(v goja.Value) bool {
					if r := el(v); r != nil {
						refs = append(refs, r)
					}
					return true
				})
			})
		}
		b.ariaDefaultsOf(n).labelledBy = refs
		b.domVersion++
		return goja.Undefined()
	})
	// setStates(el, names): the element's whole CustomStateSet after a change.
	_ = h.Set("setStates", func(call goja.FunctionCall) goja.Value {
		n := el(call.Argument(0))
		if n == nil {
			return goja.Undefined()
		}
		var names []string
		_ = vm.ExportTo(call.Argument(1), &names)
		if len(names) == 0 {
			delete(b.customStates, n)
			return goja.Undefined()
		}
		if b.customStates == nil {
			b.customStates = make(map[*html.Node][]string)
		}
		b.customStates[n] = names
		return goja.Undefined()
	})
}

// reflectInternals stamps everything extraction needs from ElementInternals onto the
// tree: form values (reflectFormValues) and default ARIA semantics. The returned
// func puts the tree back exactly as it was; a live session calls it once the
// snapshot is serialized, so page JS never observes the reflection.
func (b *bridge) reflectInternals() (undo func()) {
	var s treeStamp
	b.reflectFormValues(&s)
	b.reflectARIADefaults(&s)
	return s.undo
}

// treeStamp is the one way the reflection writes to the tree. The writes bypass the
// mutation sink (no MutationObserver record, no attributeChangedCallback), and undo
// restores each attribute's prior state — its previous value where the page had set
// one of these names itself, absence otherwise — so a snapshot leaves nothing a page
// could detect.
type treeStamp struct {
	prior []stampedAttr
}

type stampedAttr struct {
	n        *html.Node
	key, old string
	had      bool
}

func (s *treeStamp) set(n *html.Node, key, val string) {
	s.prior = append(s.prior, stampedAttr{n: n, key: key, old: getAttr(n, key), had: hasAttr(n, key)})
	setAttr(n, key, val)
}

// undo unwinds newest-first. An attribute the page already had was overwritten in
// place and one it lacked was appended, so attribute order comes back too.
func (s *treeStamp) undo() {
	for i := len(s.prior) - 1; i >= 0; i-- {
		if p := s.prior[i]; p.had {
			setAttr(p.n, p.key, p.old)
		} else {
			removeAttr(p.n, p.key)
		}
	}
	s.prior = nil
}

// reflectARIADefaults writes each custom element's internals-provided default
// semantics onto the tree as page.AriaDefaultPrefix attributes (and the text of its
// ariaLabelledByElements as page.AriaDefaultLabelledByAttr), so extraction — which
// never reads the JS heap — can name and describe the control. Every default is
// written; whether an explicit attribute overrides it is extraction's call, so the
// two cannot disagree.
func (b *bridge) reflectARIADefaults(s *treeStamp) {
	for n, d := range b.ariaDefaults {
		for key, val := range d.attrs {
			s.set(n, page.AriaDefaultPrefix+key, val)
		}
		var parts []string
		for _, ref := range d.labelledBy {
			if ref == n {
				continue
			}
			text := strings.TrimSpace(getAttr(ref, "aria-label"))
			if text == "" {
				text = strings.Join(strings.Fields(textContent(ref)), " ")
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			s.set(n, page.AriaDefaultLabelledByAttr, strings.Join(parts, " "))
		}
	}
}

// ---- :state() ----
//
// Selector matching is cascadia over the node tree, which knows nothing of a
// CustomStateSet. A page-JS selector that uses :state(x) is rewritten to an attribute
// selector on stateAttr, and for the duration of that one synchronous match every
// element with custom states carries the attribute. Page JS cannot run in between, the
// writes bypass the mutation sink, and the attribute is gone before the call returns.
// A selector without :state( takes none of this path.

// stateAttr is the carrier attribute. The name is random per process so a page cannot
// write it into its own markup and forge a :state() match.
var stateAttr = func() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "data-unblink-state-" + hex.EncodeToString(buf[:])
}()

var errBadStateSelector = errors.New("invalid :state() selector")

// compilePageSelector compiles a selector handed over by page JS (querySelector,
// querySelectorAll, matches, closest). When it uses :state(), the elements with
// custom states are marked for the match and done must be called to unmark them.
func (b *bridge) compilePageSelector(selector string) (sel cascadia.Selector, done func(), err error) {
	done = func() {}
	if !strings.Contains(strings.ToLower(selector), ":state(") {
		sel, err = compileSelector(selector)
		return sel, done, err
	}
	rewritten, err := rewriteStateSelector(selector)
	if err != nil {
		return nil, done, err
	}
	if sel, err = compileSelector(rewritten); err != nil || rewritten == selector {
		return sel, done, err
	}
	marked := make([]*html.Node, 0, len(b.customStates))
	for n, names := range b.customStates {
		tokens := make([]string, len(names))
		for i, name := range names {
			tokens[i] = hex.EncodeToString([]byte(name))
		}
		setAttr(n, stateAttr, strings.Join(tokens, " "))
		marked = append(marked, n)
	}
	return sel, func() {
		for _, n := range marked {
			removeAttr(n, stateAttr)
		}
	}, nil
}

func (b *bridge) pageQuery(root *html.Node, selector string) *html.Node {
	sel, done, err := b.compilePageSelector(selector)
	if err != nil {
		return nil
	}
	defer done()
	return cascadia.Query(root, sel)
}

func (b *bridge) pageQueryAll(root *html.Node, selector string) []*html.Node {
	sel, done, err := b.compilePageSelector(selector)
	if err != nil {
		return nil
	}
	defer done()
	return cascadia.QueryAll(root, sel)
}

// pageClosest is Element.closest: n or its nearest ancestor matching selector. With
// self set it tests n alone (Element.matches).
func (b *bridge) pageClosest(n *html.Node, selector string, self bool) *html.Node {
	sel, done, err := b.compilePageSelector(selector)
	if err != nil {
		return nil
	}
	defer done()
	for a := n; a != nil; a = a.Parent {
		if a.Type == html.ElementNode && sel.Match(a) {
			return a
		}
		if self {
			break
		}
	}
	return nil
}

// rewriteStateSelector replaces every :state(ident) pseudo-class in a selector with
// an attribute selector on stateAttr (the state name hex-encoded, so no name needs
// escaping). Text inside strings and attribute brackets is left alone. A :state(
// that is not followed by exactly one identifier is an error, i.e. an invalid
// selector.
func rewriteStateSelector(sel string) (string, error) {
	var sb strings.Builder
	brackets := 0
	// afterColon: the previous character was a real ':' (so this one would make the
	// "::" of a pseudo-element). An escaped colon — .a\: — is part of a name instead.
	afterColon := false
	for i := 0; i < len(sel); {
		c := sel[i]
		wasColon := afterColon
		afterColon = false
		switch {
		case c == '\\' && i+1 < len(sel):
			sb.WriteString(sel[i : i+2])
			i += 2
			continue
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(sel) && sel[j] != c {
				if sel[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(sel))
			sb.WriteString(sel[i:j])
			i = j
			continue
		case c == '[':
			brackets++
		case c == ']' && brackets > 0:
			brackets--
		case c == ':' && brackets == 0 && len(sel)-i >= 7 && strings.EqualFold(sel[i:i+7], ":state(") && !wasColon:
			j := skipCSSSpace(sel, i+7)
			name, end, ok := parseCSSIdent(sel, j)
			if !ok {
				return "", errBadStateSelector
			}
			if end = skipCSSSpace(sel, end); end >= len(sel) || sel[end] != ')' {
				return "", errBadStateSelector
			}
			sb.WriteString(`[` + stateAttr + `~="` + hex.EncodeToString([]byte(name)) + `"]`)
			i = end + 1
			continue
		}
		afterColon = c == ':'
		sb.WriteByte(c)
		i++
	}
	return sb.String(), nil
}

func skipCSSSpace(s string, i int) int {
	for i < len(s) && strings.IndexByte(" \t\n\r\f", s[i]) >= 0 {
		i++
	}
	return i
}

// parseCSSIdent reads a CSS identifier at s[i:], resolving escapes, and returns it
// with the index just past it.
func parseCSSIdent(s string, i int) (name string, end int, ok bool) {
	isStart := func(c byte) bool {
		return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
	}
	isHex := func(c byte) bool {
		return (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'f')
	}
	var sb strings.Builder
	start := i
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			i++
			if i >= len(s) || s[i] == '\n' {
				return "", 0, false
			}
			if !isHex(s[i]) {
				sb.WriteByte(s[i]) // a multi-byte rune's remaining bytes follow as name bytes
				i++
				continue
			}
			var r rune
			for n := 0; n < 6 && i < len(s) && isHex(s[i]); n, i = n+1, i+1 {
				d := s[i]
				if d > '9' {
					d = (d | 0x20) - 'a' + 10
				} else {
					d -= '0'
				}
				r = r<<4 | rune(d)
			}
			if i < len(s) && strings.IndexByte(" \t\n\r\f", s[i]) >= 0 {
				i++
			}
			if r == 0 || r > 0x10FFFF {
				r = 0xFFFD
			}
			sb.WriteRune(r)
			continue
		case isStart(c):
		case c == '-':
			// "-" alone and "-<digit>" do not start an identifier.
			if i == start && (i+1 >= len(s) || (!isStart(s[i+1]) && s[i+1] != '-' && s[i+1] != '\\')) {
				return "", 0, false
			}
		case c >= '0' && c <= '9':
			if i == start {
				return "", 0, false
			}
		default:
			return sb.String(), i, sb.Len() > 0
		}
		sb.WriteByte(c)
		i++
	}
	return sb.String(), i, sb.Len() > 0
}
