package js

import (
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Event phases (DOM).
const (
	phaseNone      = 0
	phaseCapturing = 1
	phaseAtTarget  = 2
	phaseBubbling  = 3
)

// listenerEntry is one registered event listener. jsFn is the original function
// value, kept so removeEventListener can match by identity.
type listenerEntry struct {
	fn      goja.Callable
	jsFn    goja.Value
	capture bool
	once    bool
	passive bool
}

// listenerOpts is the parsed addEventListener 3rd argument.
type listenerOpts struct {
	capture bool
	once    bool
	passive bool
	signal  goja.Value // nil if absent
}

// domEvent holds the Go-side state for an in-flight event dispatch.
type domEvent struct {
	typ              string
	bubbles          bool
	cancelable       bool
	composed         bool // crosses shadow boundaries (composed path); orthogonal to bubbles
	defaultPrevented bool
	stopped          bool // stopPropagation
	stopImmediate    bool // stopImmediatePropagation
	passiveActive    bool // a passive listener is currently running
	js               *goja.Object
	path             []goja.Value // composed path (target → outermost), set at dispatch for composedPath()
}

// pathEntry is one node on the event propagation path. target is the retargeted
// event.target a listener at this node must observe (the host, not the shadow-internal
// node, for listeners outside the shadow tree the event crossed).
type pathEntry struct {
	listeners map[string][]listenerEntry
	jsValue   goja.Value
	target    goja.Value
}

type phaseFilter int

const (
	captureOnly phaseFilter = iota
	bubbleOnly
	allListeners
)

// --- registration ---

func (b *bridge) addListener(reg map[string][]listenerEntry, call goja.FunctionCall) {
	fnVal := call.Argument(1)
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return
	}
	t := call.Argument(0).String()
	opts := b.parseListenerOpts(call.Argument(2))
	if opts.signal != nil && b.signalAborted(opts.signal) {
		return // an already-aborted signal means never add the listener
	}
	reg[t] = append(reg[t], listenerEntry{fn: fn, jsFn: fnVal, capture: opts.capture, once: opts.once, passive: opts.passive})
	if opts.signal != nil {
		b.onAbort(opts.signal, func() { removeEntryByFn(reg, t, fnVal, opts.capture) })
	}
}

func (b *bridge) removeListener(reg map[string][]listenerEntry, call goja.FunctionCall) {
	removeEntryByFn(reg, call.Argument(0).String(), call.Argument(1), b.parseListenerOpts(call.Argument(2)).capture)
}

// removeEntryByFn drops the listener for type t matching jsFn (by identity) and
// capture phase.
func removeEntryByFn(reg map[string][]listenerEntry, t string, jsFn goja.Value, capture bool) {
	entries := reg[t]
	if len(entries) == 0 {
		return
	}
	out := entries[:0]
	for _, e := range entries {
		if e.capture == capture && jsFn != nil && e.jsFn != nil && e.jsFn.SameAs(jsFn) {
			continue
		}
		out = append(out, e)
	}
	reg[t] = out
}

func (b *bridge) nodeAddListener(n *html.Node, call goja.FunctionCall) {
	m := b.nodeListeners[n]
	if m == nil {
		m = make(map[string][]listenerEntry)
		b.nodeListeners[n] = m
	}
	b.addListener(m, call)
}

func (b *bridge) nodeRemoveListener(n *html.Node, call goja.FunctionCall) {
	if m := b.nodeListeners[n]; m != nil {
		b.removeListener(m, call)
	}
}

// parseListenerOpts reads the addEventListener 3rd argument: a boolean (legacy
// capture), or an options object {capture, once, passive, signal}.
func (b *bridge) parseListenerOpts(arg goja.Value) listenerOpts {
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		return listenerOpts{}
	}
	if v, ok := arg.Export().(bool); ok {
		return listenerOpts{capture: v}
	}
	o := arg.ToObject(b.vm)
	if o == nil {
		return listenerOpts{}
	}
	opts := listenerOpts{
		capture: boolProp(o, "capture"),
		once:    boolProp(o, "once"),
		passive: boolProp(o, "passive"),
	}
	if s := o.Get("signal"); s != nil && !goja.IsUndefined(s) && !goja.IsNull(s) {
		opts.signal = s
	}
	return opts
}

func boolProp(o *goja.Object, name string) bool {
	if v := o.Get(name); v != nil && !goja.IsUndefined(v) {
		return v.ToBoolean()
	}
	return false
}

// signalAborted reports whether an AbortSignal's `aborted` is true.
func (b *bridge) signalAborted(signal goja.Value) bool {
	o := signal.ToObject(b.vm)
	if o == nil {
		return false
	}
	return boolProp(o, "aborted")
}

// onAbort registers a Go callback to run when an AbortSignal fires "abort".
func (b *bridge) onAbort(signal goja.Value, cb func()) {
	o := signal.ToObject(b.vm)
	if o == nil {
		return
	}
	add, ok := goja.AssertFunction(o.Get("addEventListener"))
	if !ok {
		return
	}
	_, _ = add(o, b.vm.ToValue("abort"), b.vm.ToValue(func(goja.FunctionCall) goja.Value {
		cb()
		return goja.Undefined()
	}))
}

// --- dispatch ---

// newEvent builds a fresh synthetic event (used by element.click() and lifecycle
// events).
func (b *bridge) newEvent(typ string, bubbles, cancelable bool, target goja.Value) *domEvent {
	o := b.vm.NewObject()
	_ = o.Set("type", typ)
	_ = o.Set("bubbles", bubbles)
	_ = o.Set("cancelable", cancelable)
	composed := composedByDefault(typ)
	_ = o.Set("composed", composed)
	ev := &domEvent{typ: typ, bubbles: bubbles, cancelable: cancelable, composed: composed, js: o}
	b.bindEvent(o, ev, target)
	return ev
}

// composedByDefault reports whether an event type crosses shadow boundaries by
// default — the UIEvent families (mouse/pointer/keyboard/input/composition/focus/
// drag) plus click. Matches the browser default so element.click() and synthesized
// gestures reach delegated handlers on the host/document across the boundary.
func composedByDefault(typ string) bool {
	switch typ {
	case "click", "dblclick", "auxclick", "contextmenu", "wheel",
		"mousedown", "mouseup", "mousemove", "mouseover", "mouseout", "mouseenter", "mouseleave",
		"pointerdown", "pointerup", "pointermove", "pointerover", "pointerout", "pointerenter",
		"pointerleave", "pointercancel", "gotpointercapture", "lostpointercapture",
		"keydown", "keyup", "keypress", "input", "beforeinput",
		"focus", "blur", "focusin", "focusout",
		"compositionstart", "compositionupdate", "compositionend",
		"dragstart", "drag", "dragend", "dragenter", "dragover", "dragleave", "drop":
		return true
	}
	return false
}

// newUIEvent builds a synthetic mouse/pointer event carrying realistic,
// non-"virtual" fields so press/pointer-based widget libraries (react-aria,
// Radix, …) treat an interact-driven gesture as a real primary-button press.
// Unlike newEvent (which stays bare for lifecycle events and element.click()),
// it is used only for the engine's synthesized interact gestures.
func (b *bridge) newUIEvent(typ string, bubbles, cancelable bool, target goja.Value) *domEvent {
	o := b.vm.NewObject()
	_ = o.Set("type", typ)
	_ = o.Set("bubbles", bubbles)
	_ = o.Set("cancelable", cancelable)
	// A real user gesture: primary button, trusted, composed, on-screen.
	_ = o.Set("isTrusted", true)
	_ = o.Set("composed", true)
	_ = o.Set("clientX", 1)
	_ = o.Set("clientY", 1)
	_ = o.Set("screenX", 1)
	_ = o.Set("screenY", 1)
	_ = o.Set("button", 0)
	if typ == "pointerdown" || typ == "mousedown" {
		_ = o.Set("buttons", 1) // primary button held during the down phase
	} else {
		_ = o.Set("buttons", 0)
	}
	switch typ {
	case "pointerdown", "pointerup", "pointermove", "pointerover", "pointerout", "pointerenter", "pointerleave":
		_ = o.Set("pointerId", 1)
		_ = o.Set("pointerType", "mouse")
		_ = o.Set("isPrimary", true)
		// Non-zero geometry + pressure so react-aria's isVirtualPointerEvent()
		// heuristic never classifies this synthetic press as a virtual/AT event.
		_ = o.Set("width", 1)
		_ = o.Set("height", 1)
		_ = o.Set("pressure", 0.5)
		_ = o.Set("detail", 0)
	default: // mouse/click family: a non-zero detail marks a real (non-virtual) click
		_ = o.Set("detail", 1)
	}
	ev := &domEvent{typ: typ, bubbles: bubbles, cancelable: cancelable, composed: true, js: o}
	b.bindEvent(o, ev, target)
	return ev
}

// newKeyEvent builds a synthetic KeyboardEvent carrying the key identity fields
// handlers read (key/code/keyCode/which/charCode) plus the trusted/composed
// markers, modeled on newUIEvent. keypress carries charCode (printable keys
// only); keydown/keyup report charCode 0, matching browsers.
func (b *bridge) newKeyEvent(typ string, info keyInfo, bubbles, cancelable bool, target goja.Value) *domEvent {
	o := b.vm.NewObject()
	_ = o.Set("type", typ)
	_ = o.Set("bubbles", bubbles)
	_ = o.Set("cancelable", cancelable)
	_ = o.Set("isTrusted", true)
	_ = o.Set("composed", true)
	_ = o.Set("key", info.key)
	_ = o.Set("code", info.code)
	_ = o.Set("keyCode", info.keyCode)
	_ = o.Set("which", info.keyCode)
	if typ == "keypress" && info.printable {
		_ = o.Set("charCode", []rune(info.char)[0])
	} else {
		_ = o.Set("charCode", 0)
	}
	_ = o.Set("repeat", false)
	_ = o.Set("location", 0)
	_ = o.Set("ctrlKey", false)
	_ = o.Set("shiftKey", false)
	_ = o.Set("altKey", false)
	_ = o.Set("metaKey", false)
	_ = o.Set("getModifierState", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	ev := &domEvent{typ: typ, bubbles: bubbles, cancelable: cancelable, composed: true, js: o}
	b.bindEvent(o, ev, target)
	return ev
}

// wrapEvent augments a user-constructed event object (from dispatchEvent) with the
// dispatch machinery, preserving its own fields (e.g. clientX on a MouseEvent).
func (b *bridge) wrapEvent(o *goja.Object, target goja.Value) *domEvent {
	typ := ""
	if t := o.Get("type"); t != nil && !goja.IsUndefined(t) {
		typ = t.String()
	}
	ev := &domEvent{typ: typ, bubbles: boolProp(o, "bubbles"), cancelable: boolProp(o, "cancelable"), composed: boolProp(o, "composed"), js: o}
	b.bindEvent(o, ev, target)
	return ev
}

// bindEvent installs target/currentTarget/phase and the Go-backed control methods
// onto an event object.
func (b *bridge) bindEvent(o *goja.Object, ev *domEvent, target goja.Value) {
	vm := b.vm
	_ = o.Set("target", target)
	_ = o.Set("currentTarget", goja.Null())
	_ = o.Set("eventPhase", phaseNone)
	b.defineGetter(o, "defaultPrevented", func() goja.Value { return vm.ToValue(ev.defaultPrevented) })
	_ = o.Set("preventDefault", func(goja.FunctionCall) goja.Value {
		if ev.cancelable && !ev.passiveActive {
			ev.defaultPrevented = true
		}
		return goja.Undefined()
	})
	_ = o.Set("stopPropagation", func(goja.FunctionCall) goja.Value { ev.stopped = true; return goja.Undefined() })
	_ = o.Set("stopImmediatePropagation", func(goja.FunctionCall) goja.Value {
		ev.stopped, ev.stopImmediate = true, true
		return goja.Undefined()
	})
	// composedPath() returns the full propagation path (target → window), including
	// shadow-internal nodes and the crossed hosts. Empty until the event is dispatched.
	_ = o.Set("composedPath", func(goja.FunctionCall) goja.Value {
		arr := make([]interface{}, len(ev.path))
		for i, v := range ev.path {
			arr[i] = v
		}
		return vm.NewArray(arr...)
	})
}

// dispatch runs an event through capture → target → bubble over path (ordered
// outermost → target). Returns false if the default was prevented.
func (b *bridge) dispatch(path []pathEntry, ev *domEvent) bool {
	target := len(path) - 1

	// Record the composed path (target → outermost) for composedPath(). Independent
	// of bubbles: the full path is exposed even for a non-bubbling event.
	cp := make([]goja.Value, len(path))
	for i, e := range path {
		cp[len(path)-1-i] = e.jsValue
	}
	ev.path = cp

	_ = ev.js.Set("eventPhase", phaseCapturing)
	for i := 0; i < target; i++ {
		b.invokePhase(path[i], ev, captureOnly)
		if ev.stopped {
			return !ev.defaultPrevented
		}
	}

	_ = ev.js.Set("eventPhase", phaseAtTarget)
	b.invokePhase(path[target], ev, allListeners)
	if ev.stopped || !ev.bubbles {
		return !ev.defaultPrevented
	}

	_ = ev.js.Set("eventPhase", phaseBubbling)
	for i := target - 1; i >= 0; i-- {
		b.invokePhase(path[i], ev, bubbleOnly)
		if ev.stopped {
			return !ev.defaultPrevented
		}
	}
	return !ev.defaultPrevented
}

func (b *bridge) invokePhase(entry pathEntry, ev *domEvent, filter phaseFilter) {
	live := entry.listeners[ev.typ]
	if len(live) == 0 {
		return
	}
	// Snapshot: listeners added during dispatch must not fire (DOM semantics);
	// once-listeners are removed from the live registry after firing.
	listeners := append([]listenerEntry(nil), live...)
	// Retarget event.target for this node's tree (host for out-of-shadow listeners,
	// the real node inside). Applied in EVERY phase — capture, at-target, bubble.
	if entry.target != nil {
		_ = ev.js.Set("target", entry.target)
	}
	_ = ev.js.Set("currentTarget", entry.jsValue)
	ev.stopImmediate = false
	for _, le := range listeners {
		switch filter {
		case captureOnly:
			if !le.capture {
				continue
			}
		case bubbleOnly:
			if le.capture {
				continue
			}
		}
		ev.passiveActive = le.passive
		b.callSafe(le.fn, entry.jsValue, ev.js)
		ev.passiveActive = false
		if le.once {
			removeEntryByFn(entry.listeners, ev.typ, le.jsFn, le.capture)
		}
		if ev.stopImmediate {
			return
		}
	}
}

// elementTargetPath builds the event propagation path, ordered outermost → target, as
// the shadow-including composed path: [window, document, ...light ancestors..., host,
// shadowRoot, ...shadow-internal ancestors..., target]. Each entry carries the
// retargeted event.target listeners at that node must observe.
//
// Crossing a shadow boundary (the detached shadow-root backing node → its host) happens
// ONLY for composed events; a non-composed event stops at the shadow root containing the
// target. bubbles plays no part here — it gates only the bubble phase in dispatch(),
// never path construction or the anchors. For a node in the light DOM this yields exactly
// the old [window, document, ...ancestors..., target] with target = the dispatched node.
func (b *bridge) elementTargetPath(target *html.Node, composed bool) []pathEntry {
	type hop struct {
		listeners map[string][]listenerEntry
		jsValue   goja.Value
		retarget  goja.Value
	}
	var hops []hop // target-first
	curTarget := b.wrap(target)
	reachedDoc := false // did the path reach the light/document tree?
	for cur := target; cur != nil; {
		var next *html.Node
		for n := cur; n != nil; n = n.Parent {
			if n.Type == html.ElementNode {
				hops = append(hops, hop{b.nodeListeners[n], b.wrap(n), curTarget})
			}
			if n.Parent != nil {
				continue
			}
			// Top of this tree: a shadow-root backing node crosses to its host (only
			// when composed); otherwise it's the light tree root and we're done.
			host, ok := b.shadowHostOf[n]
			if !ok {
				reachedDoc = true
				break
			}
			if sr := b.shadowRoots[host]; sr != nil {
				hops = append(hops, hop{b.nodeListeners[n], sr, curTarget})
			}
			if composed {
				curTarget = b.wrap(host)
				next = host
			}
			break
		}
		cur = next
	}
	// Assemble outermost → target. window/document belong to the path only when it
	// reaches the document tree — a non-composed event confined to a shadow tree stops
	// at the shadow root and never sees document/window. They observe the outermost
	// retargeted target.
	var path []pathEntry
	if reachedDoc {
		path = append(path,
			pathEntry{listeners: b.winListeners, jsValue: b.windowObj, target: curTarget},
			pathEntry{listeners: b.docListeners, jsValue: b.documentObj, target: curTarget})
	}
	for i := len(hops) - 1; i >= 0; i-- {
		path = append(path, pathEntry{listeners: hops[i].listeners, jsValue: hops[i].jsValue, target: hops[i].retarget})
	}
	return path
}

func (b *bridge) dispatchOnNode(target *html.Node, ev *domEvent) bool {
	return b.dispatch(b.elementTargetPath(target, ev.composed), ev)
}

func (b *bridge) dispatchOnDocument(ev *domEvent) bool {
	return b.dispatch([]pathEntry{
		{listeners: b.winListeners, jsValue: b.windowObj},
		{listeners: b.docListeners, jsValue: b.documentObj},
	}, ev)
}

func (b *bridge) dispatchOnWindow(ev *domEvent) bool {
	return b.dispatch([]pathEntry{{listeners: b.winListeners, jsValue: b.windowObj}}, ev)
}

// dispatchFocusEvent fires a focus-family event (focus/blur non-bubbling;
// focusin/focusout bubbling) at n, with relatedTarget set to the other element.
func (b *bridge) dispatchFocusEvent(typ string, bubbles bool, n, related *html.Node) {
	ev := b.newEvent(typ, bubbles, false, b.wrap(n))
	if related != nil {
		_ = ev.js.Set("relatedTarget", b.wrap(related))
	} else {
		_ = ev.js.Set("relatedTarget", goja.Null())
	}
	b.dispatchOnNode(n, ev)
}

// focusNode moves focus to n, updating document.activeElement and firing the
// browser-order focus events: blur+focusout on the previously focused element,
// then focus+focusin on n. No-op if n is nil or already focused.
func (b *bridge) focusNode(n *html.Node) {
	if n == nil || n == b.activeEl {
		return
	}
	prev := b.activeEl
	if prev != nil {
		b.activeEl = nil // transitional: activeElement is <body> during blur
		b.dispatchFocusEvent("blur", false, prev, n)
		b.dispatchFocusEvent("focusout", true, prev, n)
	}
	b.activeEl = n
	b.dispatchFocusEvent("focus", false, n, prev)
	b.dispatchFocusEvent("focusin", true, n, prev)
}

// blurNode clears focus from n (if it is the active element), firing blur then
// focusout. document.activeElement falls back to <body>.
func (b *bridge) blurNode(n *html.Node) {
	if n == nil || n != b.activeEl {
		return
	}
	b.activeEl = nil
	b.dispatchFocusEvent("blur", false, n, nil)
	b.dispatchFocusEvent("focusout", true, n, nil)
}

// dispatchUserEvent dispatches a user-constructed event object at an element/
// document/window target, returning false if the default was prevented.
func (b *bridge) dispatchUserEvent(arg goja.Value, target goja.Value, run func(*domEvent) bool) bool {
	o := arg.ToObject(b.vm)
	if o == nil {
		return false
	}
	// A createEvent()'d event dispatched before initEvent() must throw InvalidStateError.
	if v := o.Get("__uninitialized"); v != nil && v.ToBoolean() {
		b.throwDOMException("InvalidStateError", "The event has not been initialized.")
	}
	return run(b.wrapEvent(o, target))
}

func (b *bridge) callSafe(fn goja.Callable, this goja.Value, args ...goja.Value) {
	defer func() { _ = recover() }()
	_, _ = fn(this, args...)
}

// fireLifecycle dispatches DOMContentLoaded (document, bubbling to window) and
// load (window) after the page's scripts have run.
func (b *bridge) fireLifecycle() {
	b.dispatchOnDocument(b.newEvent("DOMContentLoaded", true, false, b.documentObj))
	b.dispatchOnWindow(b.newEvent("load", false, false, b.windowObj))
}
