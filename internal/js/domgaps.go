package js

import (
	"net/url"
	"strings"
	"unicode/utf16"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Members real pages reached for and found missing (each traced to a script
// error on a corpus page). They are grouped here because none is large enough
// to earn a file; each is real behaviour computed from the node tree.

func (b *bridge) installDOMGaps() {
	vm := b.vm

	// NonDocumentTypeChildNode on CharacterData. Lit's SSR hydration walks from
	// a marker comment to the element it annotates with
	// comment.nextElementSibling; without it every hydrating component threw.
	b.protoGetter(b.protoCharacterData, "nextElementSibling", func(n *html.Node) goja.Value { return b.wrap(nextElement(n)) })
	b.protoGetter(b.protoCharacterData, "previousElementSibling", func(n *html.Node) goja.Value { return b.wrap(prevElement(n)) })

	// Node.normalize(): drop empty Text nodes and merge adjacent ones, through
	// the whole subtree.
	b.protoMethod(b.protoNode, "normalize", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		b.normalize(n)
		return goja.Undefined()
	})

	// DocumentType. The doctype node already exists in the parsed tree; this
	// gives it its interface and the document its accessor.
	b.protoDocumentType = vm.CreateObject(b.protoNode)
	b.protoGetter(b.protoDocumentType, "name", func(n *html.Node) goja.Value { return vm.ToValue(n.Data) })
	b.protoGetter(b.protoDocumentType, "publicId", func(n *html.Node) goja.Value { return vm.ToValue(doctypeID(n, "public")) })
	b.protoGetter(b.protoDocumentType, "systemId", func(n *html.Node) goja.Value { return vm.ToValue(doctypeID(n, "system")) })
	b.installChildNode(b.protoDocumentType)
	b.defineCtor("DocumentType", b.protoDocumentType)
	b.setToStringTag(b.protoDocumentType, "DocumentType")
	b.protoGetter(b.protoDocument, "doctype", func(n *html.Node) goja.Value {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.DoctypeNode {
				return b.wrap(c)
			}
		}
		return goja.Null()
	})

	// document.createAttribute: a detached Attr, attached with setAttributeNode.
	createAttr := func(name string) goja.Value {
		if name == "" || strings.ContainsAny(name, " \t\n\f\r/>=") {
			b.throwDOMException("InvalidCharacterError", "The string contains invalid characters.")
		}
		return b.attrNode(nil, name)
	}
	b.protoMethod(b.protoDocument, "createAttribute", func(_ *html.Node, call goja.FunctionCall) goja.Value {
		return createAttr(strings.ToLower(call.Argument(0).String()))
	})
	b.protoMethod(b.protoDocument, "createAttributeNS", func(_ *html.Node, call goja.FunctionCall) goja.Value {
		return createAttr(call.Argument(1).String())
	})

	// Hit testing without layout. The engine computes no geometry: every element
	// has an empty box except the root and the body, which carry the constant
	// viewport rect (see viewportRect). A hit test answers from exactly that
	// model — inside the viewport the innermost box containing the point is the
	// body's, outside it nothing is hit — so the answer is consistent with what
	// getBoundingClientRect reports rather than a guess about what is drawn.
	hit := func(n *html.Node, call goja.FunctionCall) []*html.Node {
		x, y := call.Argument(0).ToFloat(), call.Argument(1).ToFloat()
		if !(x >= 0 && x <= viewportWidth && y >= 0 && y <= viewportHeight) { // also rejects NaN
			return nil
		}
		var out []*html.Node
		if body := findTag(n, "body"); body != nil {
			out = append(out, body)
		}
		if root := findTag(n, "html"); root != nil {
			out = append(out, root)
		}
		return out
	}
	b.protoMethod(b.protoDocument, "elementFromPoint", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if els := hit(n, call); len(els) > 0 {
			return b.wrap(els[0])
		}
		return goja.Null()
	})
	b.protoMethod(b.protoDocument, "elementsFromPoint", func(n *html.Node, call goja.FunctionCall) goja.Value {
		els := hit(n, call)
		arr := make([]interface{}, len(els))
		for i, e := range els {
			arr[i] = b.wrap(e)
		}
		return vm.NewArray(arr...)
	})

	b.installTextSelection()
	b.installSVGAnimatedString()
}

// normalize implements Node.normalize() on n's subtree.
func (b *bridge) normalize(n *html.Node) {
	changed := false
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch c.Type {
		case html.TextNode:
			for next != nil && next.Type == html.TextNode {
				c.Data += next.Data
				after := next.NextSibling
				n.RemoveChild(next)
				next = after
				changed = true
			}
			if c.Data == "" {
				n.RemoveChild(c)
				changed = true
			}
		case html.ElementNode:
			b.normalize(c)
		}
		c = next
	}
	if changed {
		b.onMutate(mutationRecord{typ: "childList", target: n})
	}
}

// doctypeID reads a doctype's public or system identifier, which x/net/html
// keeps as attributes named "public" and "system".
func doctypeID(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textSelection is one text control's selection, in UTF-16 code units.
type textSelection struct {
	start, end int
	dir        string
}

// installTextSelection adds the text-control selection API to <input> and
// <textarea>: selectionStart/End/Direction, setSelectionRange(), select() and
// setRangeText(). There is no caret to draw, but the state is real — a handler
// that selects on focus and one that reads the selection back agree — and
// setRangeText edits the value. Until something sets it, the selection is
// collapsed at the end of the value, as in a browser after a script assigns it.
func (b *bridge) installTextSelection() {
	vm := b.vm
	value := func(n *html.Node) string {
		if n.Data == "textarea" {
			return textContent(n)
		}
		return getAttr(n, "value")
	}
	utf16Units := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
	units := func(s string) int { return len(utf16Units(s)) }
	sel := func(n *html.Node) *textSelection {
		l := units(value(n))
		s, ok := b.textSelections[n]
		if !ok {
			return &textSelection{start: l, end: l, dir: "none"}
		}
		if s.start > l {
			s.start = l
		}
		if s.end > l {
			s.end = l
		}
		return s
	}
	set := func(n *html.Node, start, end int, dir string) {
		l := units(value(n))
		if start > l {
			start = l
		}
		if end > l {
			end = l
		}
		if start < 0 {
			start = 0
		}
		if end < start {
			end = start
		}
		if dir != "forward" && dir != "backward" {
			dir = "none"
		}
		if b.textSelections == nil {
			b.textSelections = make(map[*html.Node]*textSelection)
		}
		b.textSelections[n] = &textSelection{start: start, end: end, dir: dir}
	}
	// Selection applies to text-entry input types only; elsewhere the offsets
	// are null and setSelectionRange throws InvalidStateError.
	applies := func(n *html.Node) bool {
		if n.Data == "textarea" {
			return true
		}
		switch strings.ToLower(getAttr(n, "type")) {
		case "", "text", "search", "url", "tel", "password":
			return true
		}
		return false
	}
	intArg := func(v goja.Value, def int) int {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return def
		}
		return int(v.ToInteger())
	}
	for _, name := range []string{"HTMLInputElement", "HTMLTextAreaElement"} {
		p := b.ifaceProtos[name]
		if p == nil {
			continue
		}
		b.protoProp(p, "selectionStart",
			func(n *html.Node) goja.Value {
				if !applies(n) {
					return goja.Null()
				}
				return vm.ToValue(sel(n).start)
			},
			func(n *html.Node, v goja.Value) {
				if applies(n) {
					s := sel(n)
					end := s.end
					if st := intArg(v, 0); end < st {
						end = st
					}
					set(n, intArg(v, 0), end, s.dir)
				}
			})
		b.protoProp(p, "selectionEnd",
			func(n *html.Node) goja.Value {
				if !applies(n) {
					return goja.Null()
				}
				return vm.ToValue(sel(n).end)
			},
			func(n *html.Node, v goja.Value) {
				if applies(n) {
					s := sel(n)
					set(n, s.start, intArg(v, 0), s.dir)
				}
			})
		b.protoProp(p, "selectionDirection",
			func(n *html.Node) goja.Value {
				if !applies(n) {
					return goja.Null()
				}
				return vm.ToValue(sel(n).dir)
			},
			func(n *html.Node, v goja.Value) {
				if applies(n) {
					s := sel(n)
					set(n, s.start, s.end, v.String())
				}
			})
		b.protoMethod(p, "setSelectionRange", func(n *html.Node, call goja.FunctionCall) goja.Value {
			if !applies(n) {
				b.throwDOMException("InvalidStateError", "The input element's type does not support selection.")
			}
			dir := "none"
			if d := call.Argument(2); !goja.IsUndefined(d) {
				dir = d.String()
			}
			set(n, intArg(call.Argument(0), 0), intArg(call.Argument(1), 0), dir)
			return goja.Undefined()
		})
		b.protoMethod(p, "select", func(n *html.Node, _ goja.FunctionCall) goja.Value {
			if applies(n) {
				set(n, 0, units(value(n)), "none")
			}
			return goja.Undefined()
		})
		b.protoMethod(p, "setRangeText", func(n *html.Node, call goja.FunctionCall) goja.Value {
			if !applies(n) {
				b.throwDOMException("InvalidStateError", "The input element's type does not support selection.")
			}
			s := sel(n)
			start, end := s.start, s.end
			if len(call.Arguments) > 1 {
				start, end = intArg(call.Argument(1), 0), intArg(call.Argument(2), 0)
			}
			u := utf16Units(value(n))
			if start > len(u) {
				start = len(u)
			}
			if end > len(u) {
				end = len(u)
			}
			if start > end {
				b.throwDOMException("IndexSizeError", "The index is not in the allowed range.")
			}
			repl := utf16Units(call.Argument(0).String())
			out := append(append(append([]uint16{}, u[:start]...), repl...), u[end:]...)
			b.setControlValue(n, string(utf16.Decode(out)))
			// "preserve" (the default) keeps the selection, shifted past the edit.
			mode := "preserve"
			if m := call.Argument(3); !goja.IsUndefined(m) {
				mode = m.String()
			}
			newEnd := start + len(repl)
			switch mode {
			case "select":
				set(n, start, newEnd, "none")
			case "start":
				set(n, start, start, "none")
			case "end":
				set(n, newEnd, newEnd, "none")
			default:
				delta := len(repl) - (end - start)
				ss, se := s.start, s.end
				if ss > end {
					ss += delta
				} else if ss > start {
					ss = start
				}
				if se > end {
					se += delta
				} else if se > start {
					se = newEnd
				}
				set(n, ss, se, s.dir)
			}
			return goja.Undefined()
		})
	}
}

// installSVGAnimatedString gives SVG elements the className a browser has: an
// SVGAnimatedString whose baseVal reflects the class attribute, not a string.
// Code written for the DOM branches on it (`className instanceof
// SVGAnimatedString`, `className.baseVal`); with a plain string the interface
// name itself was a ReferenceError.
func (b *bridge) installSVGAnimatedString() {
	vm := b.vm
	proto := vm.NewObject()
	b.defineCtor("SVGAnimatedString", proto)
	b.setToStringTag(proto, "SVGAnimatedString")
	owner := func(this goja.Value) *html.Node {
		o, ok := this.(*goja.Object)
		if !ok {
			return nil
		}
		return b.animatedStrings[o]
	}
	get := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		if n := owner(call.This); n != nil {
			return vm.ToValue(getAttr(n, "class"))
		}
		return vm.ToValue("")
	})
	set := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		if n := owner(call.This); n != nil {
			b.setAttrMut(n, "class", call.Argument(0).String())
		}
		return goja.Undefined()
	})
	_ = proto.DefineAccessorProperty("baseVal", get, set, goja.FLAG_TRUE, goja.FLAG_TRUE)
	_ = proto.DefineAccessorProperty("animVal", get, nil, goja.FLAG_TRUE, goja.FLAG_TRUE)
	b.protoProp(b.protoSVGElement, "className",
		func(n *html.Node) goja.Value {
			if o, ok := b.animatedStringOf[n]; ok {
				return o
			}
			o := vm.CreateObject(proto)
			if b.animatedStrings == nil {
				b.animatedStrings = make(map[*goja.Object]*html.Node)
				b.animatedStringOf = make(map[*html.Node]*goja.Object)
			}
			b.animatedStrings[o] = n
			b.animatedStringOf[n] = o
			return o
		},
		// Assigning a string to an SVG element's className is a no-op in a
		// browser (the attribute is read-only there); keep the attribute.
		func(*html.Node, goja.Value) {})
}

// installURLMembers puts the URL-bearing members on the interfaces that have
// them. They used to sit on HTMLElement.prototype, i.e. on every element, where
// the read-only ones broke page code: a component class that keeps its own
// `this.search`, `this.host`, `this.hash` or `this.origin` — ordinary names for
// a custom element's fields — threw "Cannot assign to read only property" in its
// constructor (class bodies are strict), and `'href' in el` was true of a <div>.
//
//   - href: <a>, <area>, <link>, <base>; src: the embedding and media elements.
//     The getter resolves against the document base; the setter writes the
//     attribute, so `a.href = "/x"` on a created element is a link extraction sees.
//   - The HTMLHyperlinkElementUtils components on <a>/<area> (the
//     createElement('a') URL-parser trick Angular's getBaseHref and many
//     libraries rely on), readable and writable; origin is read-only.
//   - relList on <a>, <area>, <link>, <form>; its supports() is how loaders pick
//     modulepreload over preload.
func (b *bridge) installURLMembers() {
	vm := b.vm
	protos := func(names ...string) []*goja.Object {
		var out []*goja.Object
		for _, n := range names {
			if p := b.ifaceProtos[n]; p != nil {
				out = append(out, p)
			}
		}
		return out
	}
	urlAttr := func(p *goja.Object, name string) {
		b.protoProp(p, name,
			func(n *html.Node) goja.Value {
				v := getAttr(n, name)
				if v == "" {
					return vm.ToValue("")
				}
				if u := b.resolveNav(v); u != nil {
					return vm.ToValue(u.String())
				}
				return vm.ToValue(v)
			},
			func(n *html.Node, v goja.Value) { b.setAttrMut(n, name, v.String()) })
	}
	for _, p := range protos("HTMLAnchorElement", "HTMLAreaElement", "HTMLLinkElement", "HTMLBaseElement") {
		urlAttr(p, "href")
	}
	for _, p := range protos("HTMLImageElement", "HTMLScriptElement", "HTMLIFrameElement", "HTMLEmbedElement",
		"HTMLSourceElement", "HTMLTrackElement", "HTMLInputElement", "HTMLMediaElement") {
		urlAttr(p, "src")
	}

	type component struct {
		get func(*url.URL) string
		set func(*url.URL, string) // nil: read-only
	}
	comps := map[string]component{
		"protocol": {func(u *url.URL) string { return u.Scheme + ":" }, func(u *url.URL, v string) {
			if s := strings.TrimSuffix(strings.ToLower(v), ":"); s != "" {
				u.Scheme = s
			}
		}},
		"host": {func(u *url.URL) string { return u.Host }, func(u *url.URL, v string) { u.Host = v }},
		"hostname": {func(u *url.URL) string { return u.Hostname() }, func(u *url.URL, v string) {
			if port := u.Port(); port != "" {
				u.Host = v + ":" + port
			} else {
				u.Host = v
			}
		}},
		"port": {func(u *url.URL) string { return u.Port() }, func(u *url.URL, v string) {
			if v == "" {
				u.Host = u.Hostname()
			} else {
				u.Host = u.Hostname() + ":" + v
			}
		}},
		"pathname": {func(u *url.URL) string {
			if u.Path == "" && (u.Scheme == "http" || u.Scheme == "https") {
				return "/"
			}
			return u.EscapedPath()
		}, func(u *url.URL, v string) {
			if !strings.HasPrefix(v, "/") {
				v = "/" + v
			}
			if p, err := url.Parse(v); err == nil {
				u.Path, u.RawPath = p.Path, p.RawPath
			}
		}},
		"search": {func(u *url.URL) string { return rawQuery(u.RawQuery) }, func(u *url.URL, v string) {
			u.RawQuery = strings.TrimPrefix(v, "?")
			u.ForceQuery = false
		}},
		"hash": {func(u *url.URL) string { return rawFragment(u.Fragment) }, func(u *url.URL, v string) {
			u.Fragment, u.RawFragment = strings.TrimPrefix(v, "#"), ""
		}},
		"origin": {func(u *url.URL) string { return u.Scheme + "://" + u.Host }, nil},
	}
	for _, p := range protos("HTMLAnchorElement", "HTMLAreaElement") {
		for name, c := range comps {
			c := c
			get := func(n *html.Node) goja.Value {
				if !hasAttr(n, "href") {
					return vm.ToValue("")
				}
				if u := b.resolveNav(getAttr(n, "href")); u != nil {
					return vm.ToValue(c.get(u))
				}
				return vm.ToValue("")
			}
			if c.set == nil {
				b.protoGetter(p, name, get)
				continue
			}
			b.protoProp(p, name, get, func(n *html.Node, v goja.Value) {
				u := b.resolveNav(getAttr(n, "href"))
				if u == nil || !hasAttr(n, "href") {
					return // no URL to edit: the setter does nothing, as specified
				}
				cp := *u
				c.set(&cp, v.String())
				b.setAttrMut(n, "href", cp.String())
			})
		}
	}

	relTokens := map[string]map[string]bool{
		"HTMLLinkElement":   relSupported,
		"HTMLAnchorElement": {"noreferrer": true, "noopener": true, "opener": true},
		"HTMLAreaElement":   {"noreferrer": true, "noopener": true, "opener": true},
		"HTMLFormElement":   {"noreferrer": true, "noopener": true, "opener": true},
	}
	for name, supported := range relTokens {
		supported := supported
		if p := b.ifaceProtos[name]; p != nil {
			b.protoGetter(p, "relList", func(n *html.Node) goja.Value { return b.tokenListFor(n, "rel", supported) })
		}
	}
}
