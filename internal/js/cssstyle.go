package js

import (
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// el.style: a CSSStyleDeclaration over the element's `style` content attribute.
//
// There is still no CSSOM and no cascade — values are not validated, shorthands
// are not expanded and nothing is computed. What this gives is the one thing
// the node tree needs: an inline-style write (el.style.display = 'none',
// style.setProperty, style.cssText = …) lands in the style attribute, where
// innerText's display check and extraction's hidden-element check already
// look. Before, the object was a property bag the tree never saw.
//
// The attribute is the only state. Declarations are parsed from it on access
// (cached against the attribute text) and written back on every change.

type styleDecl struct {
	prop, val string
	important bool
}

// cssStyle is the DynamicObject behind one element's style.
type cssStyle struct {
	b      *bridge
	n      *html.Node
	src    string // the attribute text decls was parsed from
	parsed bool
	decls  []styleDecl
}

// parseStyleAttr splits a style attribute into declarations. Later duplicates
// replace earlier ones in place, as a browser's declaration block does.
func parseStyleAttr(text string) []styleDecl {
	var out []styleDecl
	for _, part := range splitStyleDecls(text) {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if !strings.HasPrefix(k, "--") {
			k = strings.ToLower(k)
		}
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		d := styleDecl{prop: k, val: v}
		if i := strings.LastIndexByte(v, '!'); i >= 0 && strings.EqualFold(strings.TrimSpace(v[i+1:]), "important") {
			d.val, d.important = strings.TrimSpace(v[:i]), true
		}
		replaced := false
		for i := range out {
			if out[i].prop == d.prop {
				out[i], replaced = d, true
				break
			}
		}
		if !replaced {
			out = append(out, d)
		}
	}
	return out
}

// splitStyleDecls splits on the semicolons that separate declarations, leaving
// alone those inside quotes or parentheses (url(data:…;base64,…), content:";").
func splitStyleDecls(text string) []string {
	var (
		out   []string
		depth int
		quote byte
		start int
	)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}

func serializeStyle(decls []styleDecl) string {
	var sb strings.Builder
	for i, d := range decls {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(d.prop)
		sb.WriteString(": ")
		sb.WriteString(d.val)
		if d.important {
			sb.WriteString(" !important")
		}
		sb.WriteByte(';')
	}
	return sb.String()
}

// cssPropName maps an IDL style name to its CSS property: backgroundColor →
// background-color, cssFloat → float, webkitTransform → -webkit-transform. A
// name that is already dashed (or a custom property) passes through.
func cssPropName(key string) string {
	if strings.HasPrefix(key, "--") || strings.IndexByte(key, '-') >= 0 {
		return strings.ToLower(key)
	}
	if key == "cssFloat" {
		return "float"
	}
	var sb strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'A' && c <= 'Z' {
			sb.WriteByte('-')
			sb.WriteByte(c + 32)
			continue
		}
		sb.WriteByte(c)
	}
	out := sb.String()
	for _, p := range []string{"webkit-", "moz-", "ms-", "o-"} {
		if strings.HasPrefix(out, p) {
			return "-" + out
		}
	}
	return out
}

// cssIDLName is the inverse, for enumeration.
func cssIDLName(prop string) string {
	if strings.HasPrefix(prop, "--") {
		return prop
	}
	if prop == "float" {
		return "cssFloat"
	}
	p := strings.TrimPrefix(prop, "-")
	var sb strings.Builder
	up := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '-' {
			up = true
			continue
		}
		if up && c >= 'a' && c <= 'z' {
			c -= 32
		}
		up = false
		sb.WriteByte(c)
	}
	return sb.String()
}

func (s *cssStyle) load() {
	text := getAttr(s.n, "style")
	if s.parsed && text == s.src {
		return
	}
	s.src, s.parsed, s.decls = text, true, parseStyleAttr(text)
}

// store writes the declarations back to the style attribute.
func (s *cssStyle) store() {
	text := serializeStyle(s.decls)
	s.src = text
	s.b.setStyleAttr(s.n, text)
}

func (s *cssStyle) get(prop string) (styleDecl, bool) {
	s.load()
	for _, d := range s.decls {
		if d.prop == prop {
			return d, true
		}
	}
	return styleDecl{}, false
}

func (s *cssStyle) set(prop, val string, important bool) {
	s.load()
	val = strings.TrimSpace(val)
	if val == "" {
		s.remove(prop)
		return
	}
	for i := range s.decls {
		if s.decls[i].prop == prop {
			if s.decls[i].val == val && s.decls[i].important == important {
				return
			}
			s.decls[i].val, s.decls[i].important = val, important
			s.store()
			return
		}
	}
	s.decls = append(s.decls, styleDecl{prop: prop, val: val, important: important})
	s.store()
}

func (s *cssStyle) remove(prop string) string {
	s.load()
	for i, d := range s.decls {
		if d.prop == prop {
			s.decls = append(s.decls[:i:i], s.decls[i+1:]...)
			s.store()
			return d.val
		}
	}
	return ""
}

// styleMethods are the CSSStyleDeclaration members that are not properties.
var styleMethods = map[string]bool{
	"setProperty": true, "removeProperty": true, "getPropertyValue": true,
	"getPropertyPriority": true, "item": true,
}

func (s *cssStyle) Get(key string) goja.Value {
	vm := s.b.vm
	switch key {
	case "cssText":
		return vm.ToValue(getAttr(s.n, "style"))
	case "length":
		s.load()
		return vm.ToValue(len(s.decls))
	case "parentRule":
		return goja.Null()
	}
	if styleMethods[key] {
		return s.b.styleMethod(key)
	}
	if i, err := strconv.Atoi(key); err == nil {
		s.load()
		if i >= 0 && i < len(s.decls) {
			return vm.ToValue(s.decls[i].prop)
		}
		return goja.Undefined()
	}
	if d, ok := s.get(cssPropName(key)); ok {
		return vm.ToValue(d.val)
	}
	return vm.ToValue("")
}

func (s *cssStyle) Set(key string, val goja.Value) bool {
	switch {
	case key == "cssText":
		s.b.setStyleAttr(s.n, styleValue(val))
		return true
	case key == "length" || key == "parentRule" || styleMethods[key]:
		return true // read-only: ignored
	}
	if _, err := strconv.Atoi(key); err == nil {
		return true
	}
	s.set(cssPropName(key), styleValue(val), false)
	return true
}

// styleValue stringifies a value assigned to a style property; null clears it.
func styleValue(v goja.Value) string {
	if v == nil || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func (s *cssStyle) Has(key string) bool {
	// Every style property "exists" on a real CSSStyleDeclaration whether or not
	// it is set; with no property list to consult, any name does.
	return true
}

func (s *cssStyle) Delete(key string) bool {
	s.remove(cssPropName(key))
	return true
}

func (s *cssStyle) Keys() []string {
	s.load()
	keys := make([]string, 0, len(s.decls))
	for _, d := range s.decls {
		keys = append(keys, cssIDLName(d.prop))
	}
	return keys
}

// styleFor returns the cached style object for n.
func (b *bridge) styleFor(n *html.Node) *goja.Object {
	if o, ok := b.styleCache[n]; ok {
		return o
	}
	s := &cssStyle{b: b, n: n}
	o := b.vm.NewDynamicObject(s)
	b.styleCache[n] = o
	if b.styleOf == nil {
		b.styleOf = make(map[*goja.Object]*cssStyle)
	}
	b.styleOf[o] = s
	return o
}

// styleMethod returns the shared function for a CSSStyleDeclaration method. One
// function per runtime serves every element's style: it finds its declaration
// block from `this`.
func (b *bridge) styleMethod(name string) goja.Value {
	if v, ok := b.styleMethods[name]; ok {
		return v
	}
	if b.styleMethods == nil {
		b.styleMethods = make(map[string]goja.Value)
	}
	vm := b.vm
	self := func(call goja.FunctionCall) *cssStyle {
		o, _ := call.This.(*goja.Object)
		return b.styleOf[o]
	}
	var fn func(goja.FunctionCall) goja.Value
	switch name {
	case "setProperty":
		fn = func(call goja.FunctionCall) goja.Value {
			if s := self(call); s != nil {
				s.set(cssPropName(call.Argument(0).String()), styleValue(call.Argument(1)),
					strings.EqualFold(strings.TrimSpace(optString(call.Argument(2))), "important"))
			}
			return goja.Undefined()
		}
	case "removeProperty":
		fn = func(call goja.FunctionCall) goja.Value {
			if s := self(call); s != nil {
				return vm.ToValue(s.remove(cssPropName(call.Argument(0).String())))
			}
			return vm.ToValue("")
		}
	case "getPropertyValue":
		fn = func(call goja.FunctionCall) goja.Value {
			if s := self(call); s != nil {
				if d, ok := s.get(cssPropName(call.Argument(0).String())); ok {
					return vm.ToValue(d.val)
				}
			}
			return vm.ToValue("")
		}
	case "getPropertyPriority":
		fn = func(call goja.FunctionCall) goja.Value {
			if s := self(call); s != nil {
				if d, ok := s.get(cssPropName(call.Argument(0).String())); ok && d.important {
					return vm.ToValue("important")
				}
			}
			return vm.ToValue("")
		}
	default: // item
		fn = func(call goja.FunctionCall) goja.Value {
			if s := self(call); s != nil {
				s.load()
				if i := int(call.Argument(0).ToInteger()); i >= 0 && i < len(s.decls) {
					return vm.ToValue(s.decls[i].prop)
				}
			}
			return vm.ToValue("")
		}
	}
	v := vm.ToValue(fn)
	b.styleMethods[name] = v
	return v
}

func optString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

// setStyleAttr writes the style attribute on behalf of the style object.
//
// It is a mutation — observers see it and a live session's snapshot goes stale
// (styleVersion) — but it is deliberately NOT settle activity (domVersion). A
// page animating from requestAnimationFrame or setInterval writes inline styles
// every frame; counting those would hold the render's quiet window open until
// the budget ran out. Content-changing work still shows up as real DOM
// mutations, timers and network.
func (b *bridge) setStyleAttr(n *html.Node, text string) {
	old, had := getAttr(n, "style"), hasAttr(n, "style")
	if had && old == text || !had && text == "" {
		return
	}
	setAttr(n, "style", text)
	b.styleVersion++
	rec := mutationRecord{typ: "attributes", target: n, attr: "style", oldValue: old}
	queued := false
	for _, obs := range b.observers {
		if obs.matches(rec) {
			obs.queue = append(obs.queue, rec)
			queued = true
		}
	}
	if queued {
		b.scheduleFlush()
	}
	b.afterAttr(n, "style", old, text)
}
