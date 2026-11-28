package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Interface objects for things the engine builds itself.
//
// Page code asks what an object *is* far more often than it constructs one:
// `e instanceof MouseEvent`, `list instanceof NodeList`, `self.constructor ===
// Window` (Ember's "is this a browser?" test — failing it, an Ember app boots
// non-interactive with no URL and renders nothing). An object the engine hands
// out must therefore sit on the prototype of the interface a browser would
// report, and that interface must exist as a global. This file covers the
// Go-built collections and events; the host singletons (window, location,
// navigator, …) are adopted by the prelude (see "host interface objects" in
// prelude_api.go).

// installCollections builds NodeList and HTMLCollection. Both prototypes inherit
// Array.prototype: the engine's lists are arrays (static snapshots, as before),
// and plenty of page code already leans on that, so the standard members
// (item, namedItem, forEach, entries, …) are added in front of the array ones
// rather than replacing them. `Array.isArray(list)` stays true — a known
// deviation.
func (b *bridge) installCollections() {
	vm := b.vm
	arrayProto := vm.Get("Array").ToObject(vm).Get("prototype").ToObject(vm)
	item := func(call goja.FunctionCall) goja.Value {
		o := call.This.ToObject(vm)
		i := call.Argument(0).ToInteger()
		if i < 0 || i >= o.Get("length").ToInteger() {
			return goja.Null()
		}
		return o.Get(call.Argument(0).ToString().String())
	}
	b.protoNodeList = vm.CreateObject(arrayProto)
	_ = b.protoNodeList.Set("item", item)
	b.defineCtor("NodeList", b.protoNodeList)
	b.setToStringTag(b.protoNodeList, "NodeList")

	b.protoHTMLCollection = vm.CreateObject(arrayProto)
	_ = b.protoHTMLCollection.Set("item", item)
	_ = b.protoHTMLCollection.Set("namedItem", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if name == "" {
			return goja.Null()
		}
		o := call.This.ToObject(vm)
		n := o.Get("length").ToInteger()
		for i := int64(0); i < n; i++ {
			v := o.Get(vm.ToValue(i).String())
			if el := b.nodeOf(v); el != nil && (getAttr(el, "id") == name || getAttr(el, "name") == name) {
				return v
			}
		}
		return goja.Null()
	})
	b.defineCtor("HTMLCollection", b.protoHTMLCollection)
	b.setToStringTag(b.protoHTMLCollection, "HTMLCollection")
}

// htmlCollection is nodeList for the members the standard types HTMLCollection
// (children, getElementsByTagName/ClassName, document.forms, …).
func (b *bridge) htmlCollection(nodes []*html.Node) *goja.Object {
	arr := make([]interface{}, len(nodes))
	for i, n := range nodes {
		arr[i] = b.wrap(n)
	}
	o := b.vm.NewArray(arr...) // a real array, so page code can hang properties on it
	if b.protoHTMLCollection != nil {
		_ = o.SetPrototype(b.protoHTMLCollection)
	}
	return o
}

// eventInterface names the interface a browser dispatches typ as. Engine-built
// events (lifecycle, element.click(), interact gestures) are put on it by
// bindEvent; an event page code constructed keeps the class it was built with.
func eventInterface(typ string) string {
	switch typ {
	case "click", "auxclick", "contextmenu":
		return "PointerEvent"
	case "dblclick", "mousedown", "mouseup", "mousemove", "mouseover", "mouseout", "mouseenter", "mouseleave":
		return "MouseEvent"
	case "wheel":
		return "WheelEvent"
	case "keydown", "keyup", "keypress":
		return "KeyboardEvent"
	case "input", "beforeinput":
		return "InputEvent"
	case "focus", "blur", "focusin", "focusout":
		return "FocusEvent"
	case "compositionstart", "compositionupdate", "compositionend":
		return "CompositionEvent"
	case "dragstart", "drag", "dragend", "dragenter", "dragover", "dragleave", "drop":
		return "DragEvent"
	case "submit":
		return "SubmitEvent"
	case "popstate":
		return "PopStateEvent"
	case "hashchange":
		return "HashChangeEvent"
	case "pageshow", "pagehide":
		return "PageTransitionEvent"
	case "beforeunload":
		return "BeforeUnloadEvent"
	case "storage":
		return "StorageEvent"
	case "message", "messageerror":
		return "MessageEvent"
	case "unhandledrejection", "rejectionhandled":
		return "PromiseRejectionEvent"
	case "gotpointercapture", "lostpointercapture":
		return "PointerEvent"
	}
	switch {
	case strings.HasPrefix(typ, "pointer"):
		return "PointerEvent"
	case strings.HasPrefix(typ, "transition"):
		return "TransitionEvent"
	case strings.HasPrefix(typ, "animation"):
		return "AnimationEvent"
	}
	return "Event"
}

// adoptEventInterface puts a plain engine-built event object on the prototype of
// its interface. An object that already has a prototype of its own — one page
// code built with `new MouseEvent(…)`, a subclass instance — is left alone.
func (b *bridge) adoptEventInterface(o *goja.Object, typ string) {
	if b.objectProto == nil {
		b.objectProto = b.vm.Get("Object").ToObject(b.vm).Get("prototype").ToObject(b.vm)
	}
	if p := o.Prototype(); p != nil && p != b.objectProto {
		return
	}
	name := eventInterface(typ)
	proto, ok := b.eventProtos[name]
	if !ok {
		if b.eventProtos == nil {
			b.eventProtos = make(map[string]*goja.Object)
		}
		if c, isObj := b.vm.Get(name).(*goja.Object); isObj {
			proto, _ = c.Get("prototype").(*goja.Object)
		}
		b.eventProtos[name] = proto
	}
	if proto != nil {
		_ = o.SetPrototype(proto)
	}
}
