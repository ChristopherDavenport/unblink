package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// maxCustomElementUpgrades bounds the total custom-element upgrades per runtime, so a
// component whose constructor inserts more distinct custom elements can't expand the
// tree without limit. High enough not to affect legitimate component-heavy pages.
const maxCustomElementUpgrades = 100000

// Custom elements + flat Shadow DOM (Phase C) — the Lit / web-components path.
//
// Two ideas make this work without a real Shadow DOM:
//  1. Upgrade reuses the existing node as `this`. The HTMLElement constructor,
//     during an upgrade, returns the element's wrapper (whose prototype we have
//     set to the component class), so `super()` yields the live element.
//  2. attachShadow is flat: the ShadowRoot's backing node IS the host element, so
//     everything a component renders into its shadow root lands in the light tree
//     and stays visible to cascadia / reduce / emit. Encapsulation is ignored.

// newElement builds an element node for createElement/createElementNS, upgrading
// it in place when the tag is a defined custom element.
func (b *bridge) newElement(tag, namespace string) *html.Node {
	n := &html.Node{
		Type:      html.ElementNode,
		Data:      tag,
		DataAtom:  atom.Lookup([]byte(tag)),
		Namespace: namespace,
	}
	if ctor := b.customElements[tag]; ctor != nil {
		b.upgradeElement(n, ctor)
	}
	return n
}

// defineHTMLElementCtor registers HTMLElement as an upgrade-aware native
// constructor: during an upgrade it returns the element being upgraded (popped off
// the construction stack); otherwise it allocates a fresh detached element.
func (b *bridge) defineHTMLElementCtor() {
	vm := b.vm
	ctor := vm.ToValue(func(call goja.ConstructorCall) *goja.Object {
		if n := b.popUpgrading(); n != nil {
			if o, ok := b.wrap(n).(*goja.Object); ok {
				return o
			}
		}
		// `new SomeComponent()` with no element to upgrade: a detached element whose
		// tag is the class's registered name (if any), else a generic <div>.
		tag := "div"
		if call.NewTarget != nil {
			if name := b.tagForCtor(call.NewTarget); name != "" {
				tag = name
			}
		}
		n := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
		o, _ := b.wrap(n).(*goja.Object)
		return o
	}).(*goja.Object)
	_ = ctor.Set("prototype", b.protoHTMLElement)
	_ = b.protoHTMLElement.Set("constructor", ctor)
	_ = vm.Set("HTMLElement", ctor)
}

func (b *bridge) popUpgrading() *html.Node {
	if len(b.upgradingStack) == 0 {
		return nil
	}
	n := b.upgradingStack[len(b.upgradingStack)-1]
	b.upgradingStack = b.upgradingStack[:len(b.upgradingStack)-1]
	return n
}

// tagForCtor finds the registered tag whose constructor's prototype matches t's
// prototype (so `new MyEl()` produces a <my-el>).
func (b *bridge) tagForCtor(t *goja.Object) string {
	tp := t.Get("prototype")
	if tp == nil {
		return ""
	}
	for name, c := range b.customElements {
		if c.Get("prototype").SameAs(tp) {
			return name
		}
	}
	return ""
}

// installCustomElements registers window.customElements (define/get/whenDefined/
// upgrade) and the upgrade-aware HTMLElement constructor.
func (b *bridge) installCustomElements(win *goja.Object) {
	vm := b.vm
	b.defineHTMLElementCtor()

	ce := vm.NewObject()
	_ = ce.Set("define", func(call goja.FunctionCall) goja.Value {
		name := strings.ToLower(call.Argument(0).String())
		ctor := call.Argument(1).ToObject(vm)
		if name == "" || ctor == nil {
			return goja.Undefined()
		}
		b.define(name, ctor)
		return goja.Undefined()
	})
	_ = ce.Set("get", func(call goja.FunctionCall) goja.Value {
		if c := b.customElements[strings.ToLower(call.Argument(0).String())]; c != nil {
			return c
		}
		return goja.Undefined()
	})
	_ = ce.Set("whenDefined", func(call goja.FunctionCall) goja.Value {
		name := strings.ToLower(call.Argument(0).String())
		promise, resolve, _ := vm.NewPromise()
		if b.customElements[name] != nil {
			_ = resolve(b.customElements[name])
		} else {
			b.whenDefinedResolvers[name] = append(b.whenDefinedResolvers[name], resolve)
		}
		return vm.ToValue(promise)
	})
	_ = ce.Set("upgrade", func(call goja.FunctionCall) goja.Value {
		if root := b.unwrap(call.Argument(0)); root != nil {
			b.scan(root)
		}
		return goja.Undefined()
	})
	_ = vm.Set("customElements", ce)
	_ = win.Set("customElements", ce)
}

// define registers a custom element, upgrades existing matches in the document,
// and resolves any pending whenDefined promises.
func (b *bridge) define(name string, ctor *goja.Object) {
	b.customElements[name] = ctor
	delete(b.customObserved, name)
	b.scan(b.doc)
	for _, resolve := range b.whenDefinedResolvers[name] {
		_ = resolve(ctor)
	}
	delete(b.whenDefinedResolvers, name)
}

// upgradeTree is the insert-time hook (see bridge.connect): upgrade and connect any
// defined custom elements at or under n.
func (b *bridge) upgradeTree(n *html.Node) {
	if len(b.customElements) == 0 {
		return
	}
	b.scan(n)
}

// scan walks root and, for each defined custom element, upgrades it (constructing
// the instance over the existing node) and fires connectedCallback once it is
// connected to the document.
func (b *bridge) scan(root *html.Node) {
	if root == nil || len(b.customElements) == 0 {
		return
	}
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.ElementNode {
			if ctor := b.customElements[nd.Data]; ctor != nil {
				if !b.upgraded[nd] {
					b.upgradeElement(nd, ctor)
				}
				if b.upgraded[nd] && !b.connectedNotified[nd] && b.isConnected(nd) {
					b.connectedNotified[nd] = true
					b.invokeLifecycle(nd, "connectedCallback")
				}
			}
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
}

// upgradeElement constructs the component class over the existing node, reusing the
// node's wrapper as `this`, then fires attributeChangedCallback for present
// observed attributes. connectedCallback is fired separately (see scan).
func (b *bridge) upgradeElement(n *html.Node, ctor *goja.Object) {
	// Bound total upgrades: a constructor that creates and inserts new distinct custom
	// elements re-enters insert→connect→upgradeTree→scan, so a malicious component can
	// expand the tree exponentially. Stop upgrading past the cap (the wall-clock
	// watchdog remains the backstop for other unbounded growth).
	if b.upgradeCount >= maxCustomElementUpgrades {
		return
	}
	b.upgradeCount++
	o, ok := b.wrap(n).(*goja.Object)
	if !ok {
		return
	}
	if proto := ctor.Get("prototype"); proto != nil {
		if po := proto.ToObject(b.vm); po != nil {
			_ = o.SetPrototype(po)
		}
	}
	depth := len(b.upgradingStack)
	b.upgradingStack = append(b.upgradingStack, n)
	b.reflectConstruct(ctor)
	if len(b.upgradingStack) > depth {
		b.upgradingStack = b.upgradingStack[:depth] // defensive: class didn't call super()
	}
	b.upgraded[n] = true

	if observed := b.observedAttrs(n.Data, ctor); len(observed) > 0 {
		for _, a := range n.Attr {
			if observed[a.Key] {
				b.invokeAttrChanged(o, a.Key, goja.Null(), b.vm.ToValue(a.Val))
			}
		}
	}
}

// reflectConstruct runs `Reflect.construct(ctor, [], ctor)`; the construction-stack
// trick makes super() return the element being upgraded.
func (b *bridge) reflectConstruct(ctor *goja.Object) {
	if b.reflectConstructFn == nil {
		if r := b.vm.Get("Reflect"); r != nil {
			if ro := r.ToObject(b.vm); ro != nil {
				b.reflectConstructFn, _ = goja.AssertFunction(ro.Get("construct"))
			}
		}
	}
	if b.reflectConstructFn == nil {
		return
	}
	if _, err := b.reflectConstructFn(goja.Undefined(), ctor, b.vm.NewArray(), ctor); err != nil {
		b.recordError(err)
	}
}

// observedAttrs returns (and caches) the component's observedAttributes set.
func (b *bridge) observedAttrs(tag string, ctor *goja.Object) map[string]bool {
	if set, ok := b.customObserved[tag]; ok {
		return set
	}
	set := map[string]bool{}
	if oa := ctor.Get("observedAttributes"); oa != nil && !goja.IsUndefined(oa) && !goja.IsNull(oa) {
		b.vm.ForOf(oa, func(v goja.Value) bool { set[v.String()] = true; return true })
	}
	b.customObserved[tag] = set
	return set
}

func (b *bridge) invokeAttrChanged(o *goja.Object, name string, oldVal, newVal goja.Value) {
	if fn, ok := goja.AssertFunction(o.Get("attributeChangedCallback")); ok {
		b.callSafe(fn, o, b.vm.ToValue(name), oldVal, newVal, goja.Null())
	}
}

func (b *bridge) invokeLifecycle(n *html.Node, name string) {
	o, ok := b.wrap(n).(*goja.Object)
	if !ok {
		return
	}
	if fn, ok := goja.AssertFunction(o.Get(name)); ok {
		b.callSafe(fn, o)
	}
}

// afterAttr fires attributeChangedCallback when an observed attribute of an
// upgraded custom element changes through setAttribute/className/etc.
func (b *bridge) afterAttr(n *html.Node, key, oldVal, newVal string) {
	if !b.upgraded[n] {
		return
	}
	ctor := b.customElements[n.Data]
	if ctor == nil {
		return
	}
	if b.observedAttrs(n.Data, ctor)[key] {
		if o, ok := b.wrap(n).(*goja.Object); ok {
			b.invokeAttrChanged(o, key, b.vm.ToValue(oldVal), b.vm.ToValue(newVal))
		}
	}
}

// isConnected reports whether n is attached to the document (not a detached or
// fragment subtree).
func (b *bridge) isConnected(n *html.Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p == b.doc {
			return true
		}
	}
	return false
}

// attachShadow returns a flat ShadowRoot whose backing node is the host element, so
// shadow content is written into the light tree (visible to extraction). The mode
// and slot/encapsulation semantics are intentionally ignored.
func (b *bridge) attachShadow(host *html.Node, opts goja.Value) *goja.Object {
	if sr, ok := b.shadowRoots[host]; ok {
		return sr
	}
	vm := b.vm
	// Element prototype gives the root querySelector/innerHTML/appendChild that
	// operate on the host's children (objNode maps it to the host node).
	sr := vm.CreateObject(b.protoElement)
	b.objNode[sr] = host
	mode := "open"
	if opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
		if oo := opts.ToObject(vm); oo != nil {
			if m := oo.Get("mode"); m != nil && !goja.IsUndefined(m) {
				mode = m.String()
			}
		}
	}
	_ = sr.Set("mode", mode)
	_ = sr.Set("host", b.wrap(host))
	b.shadowRoots[host] = sr
	return sr
}

// templateContent returns the cached DocumentFragment for a <template>'s content,
// moving the template's children into it on first access.
func (b *bridge) templateContent(n *html.Node) *goja.Object {
	if o, ok := b.templateContentCache[n]; ok {
		return o
	}
	frag := &html.Node{Type: html.DocumentNode}
	moved := n.FirstChild != nil
	for n.FirstChild != nil {
		c := n.FirstChild
		n.RemoveChild(c)
		frag.AppendChild(c)
	}
	if moved {
		// The template element in the live tree just lost its children — bump the
		// mutation signal so settle and live-snapshot staleness checks see it.
		b.domVersion++
	}
	o, _ := b.wrap(frag).(*goja.Object)
	b.templateContentCache[n] = o
	return o
}

// maxDiagErrors bounds the per-context error buffer: a long-lived live session
// with a crash-looping timer must not grow it forever. New errors past the cap
// are dropped (keeping the earliest — indexes into the slice stay stable for
// Dispatch's per-window capture).
const maxDiagErrors = 64

// recordError appends a script/upgrade exception for render diagnostics (Phase E).
func (b *bridge) recordError(err error) {
	if err != nil && len(b.diagErrors) < maxDiagErrors {
		b.diagErrors = append(b.diagErrors, err.Error())
	}
}
