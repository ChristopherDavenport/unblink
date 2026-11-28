package js

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Event handlers (HTML Standard, "Event handlers"): the on<type> IDL properties
// (el.onclick = fn, window.onload = fn) and content attributes (onclick="…").
//
// A handler is modelled the way the standard does it: the first time a target
// gets a handler for a type, ONE listener entry is appended to that target's
// ordinary listener list and stays there for good. Later assignments only swap
// the value the entry points at, so a handler keeps its original position among
// the addEventListener listeners, and setting null leaves a dead entry rather
// than moving anything. dispatch needs no separate pass.
//
// Content attributes are compiled lazily: an element carrying onclick="…" costs
// nothing until an event of that type actually reaches it or its property is
// read. The attribute text is page-supplied inline script, so it is gated by
// the document's Content-Security-Policy (cspAllowInlineHandler) before it is
// compiled.

// handlerOwner says which listener registry a handler belongs to.
type handlerOwner uint8

const (
	ownerNode handlerOwner = iota
	ownerDocument
	ownerWindow
)

type handlerKey struct {
	owner handlerOwner
	n     *html.Node // set for ownerNode only
	typ   string
}

// handlerSlot is the current value of one event handler.
type handlerSlot struct {
	key handlerKey
	// val is the handler: a callable, a non-callable object (stored, never
	// invoked), or nil for null.
	val goja.Value
	// fromAttr marks a handler supplied by a content attribute on attrOwner.
	// attrSrc/attrDone record what val was compiled from, so a changed or removed
	// attribute is noticed without hooking every attribute-mutation path.
	fromAttr  bool
	attrOwner *html.Node
	attrSrc   string
	attrDone  bool
}

// globalHandlerNames is GlobalEventHandlers plus DocumentAndElementEventHandlers:
// the on<type> names every element, the document and the window expose.
//
// The touch handlers are deliberately absent. A desktop browser without a touch
// screen does not expose ontouchstart and friends, and `'ontouchstart' in window`
// is the classic "is this a touch device" test; the engine reports
// navigator.maxTouchPoints 0, so it must answer false. focusin/focusout have no
// handler attribute in the standard either (`'onfocusin' in window` is false).
// Listeners for all of these still work through addEventListener.
var globalHandlerNames = []string{
	"abort", "animationcancel", "animationend", "animationiteration", "animationstart",
	"auxclick", "beforeinput", "beforematch", "beforetoggle", "blur", "cancel", "canplay",
	"canplaythrough", "change", "click", "close", "command", "contextlost", "contextmenu",
	"contextrestored", "copy", "cuechange", "cut", "dblclick", "drag", "dragend",
	"dragenter", "dragleave", "dragover", "dragstart", "drop", "durationchange", "emptied",
	"ended", "error", "focus", "formdata", "fullscreenchange",
	"fullscreenerror", "gotpointercapture", "input", "invalid", "keydown", "keypress",
	"keyup", "load", "loadeddata", "loadedmetadata", "loadstart", "lostpointercapture",
	"mousedown", "mouseenter", "mouseleave", "mousemove", "mouseout", "mouseover",
	"mouseup", "paste", "pause", "play", "playing", "pointercancel", "pointerdown",
	"pointerenter", "pointerleave", "pointermove", "pointerout", "pointerover",
	"pointerup", "progress", "ratechange", "reset", "resize", "scroll", "scrollend",
	"securitypolicyviolation", "seeked", "seeking", "select", "selectionchange",
	"selectstart", "slotchange", "stalled", "submit", "suspend", "timeupdate", "toggle",
	"transitioncancel",
	"transitionend", "transitionrun", "transitionstart", "volumechange", "waiting",
	"webkitanimationend", "webkitanimationiteration", "webkitanimationstart",
	"webkittransitionend", "wheel",
}

// windowHandlerNames is WindowEventHandlers: exposed on the window, and on
// <body>/<frameset> where they are forwarded to the window.
var windowHandlerNames = []string{
	"afterprint", "beforeprint", "beforeunload", "hashchange", "languagechange",
	"message", "messageerror", "offline", "online", "pagehide", "pagereveal",
	"pageshow", "pageswap", "popstate", "rejectionhandled", "storage",
	"unhandledrejection", "unload",
}

// documentHandlerNames are the Document-only handlers.
var documentHandlerNames = []string{"readystatechange", "visibilitychange", "pointerlockchange", "pointerlockerror"}

// bodyForwarded are the handlers that, set on <body>/<frameset> (as a property or
// a content attribute), belong to the window instead: WindowEventHandlers plus
// the six GlobalEventHandlers the standard singles out.
var bodyForwarded = func() map[string]bool {
	m := map[string]bool{"blur": true, "error": true, "focus": true, "load": true, "resize": true, "scroll": true}
	for _, n := range windowHandlerNames {
		m[n] = true
	}
	return m
}()

// handlerNames is every name a content attribute may define a handler for.
var handlerNames = func() map[string]bool {
	m := make(map[string]bool, len(globalHandlerNames)+len(windowHandlerNames)+len(documentHandlerNames))
	for _, l := range [][]string{globalHandlerNames, windowHandlerNames, documentHandlerNames} {
		for _, n := range l {
			m[n] = true
		}
	}
	return m
}()

// installEventHandlers defines the on<type> accessors.
//
// This runs for every render, so it is built to be cheap: each name gets ONE
// native function that serves as both the getter and the setter (told apart by
// whether it was passed a value) and is shared by every object exposing the
// name; it resolves its target from `this`. That is half the function objects
// of a get/set pair per name, which is where the cost of an accessor lies.
func (b *bridge) installEventHandlers() {
	define := func(o *goja.Object, name string, acc goja.Value) {
		_ = o.DefineAccessorProperty("on"+name, acc, acc, goja.FLAG_TRUE, goja.FLAG_TRUE)
	}
	accessor := func(name string) goja.Value {
		return b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			key, ok := b.handlerKeyFor(call.This, name)
			if len(call.Arguments) == 0 { // get
				if !ok {
					return goja.Null()
				}
				return b.handlerGet(key)
			}
			if ok {
				b.handlerSet(key, call.Arguments[0])
			}
			return goja.Undefined()
		})
	}
	for _, name := range globalHandlerNames {
		acc := accessor(name)
		define(b.protoHTMLElement, name, acc) // SVG and MathML elements chain to it
		define(b.protoDocument, name, acc)
		define(b.windowObj, name, acc)
	}
	for _, name := range windowHandlerNames {
		acc := accessor(name)
		define(b.windowObj, name, acc)
		// <body>/<frameset> expose them too; handlerKeyFor forwards to the window.
		for _, iface := range []string{"HTMLBodyElement", "HTMLFrameSetElement"} {
			if p := b.ifaceProtos[iface]; p != nil {
				define(p, name, acc)
			}
		}
	}
	for _, name := range documentHandlerNames {
		define(b.protoDocument, name, accessor(name))
	}
}

// isBodyLike reports whether n is the document's <body> or a <frameset>: the
// elements whose window-forwarded handlers belong to the window.
func (b *bridge) isBodyLike(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode || (n.Data != "body" && n.Data != "frameset") {
		return false
	}
	// Only the document's own body: a <body> in a detached or parsed-elsewhere
	// tree has no window to forward to.
	return n.Parent != nil && n.Parent.Data == "html" && n.Parent.Parent == b.doc
}

// handlerKeyFor resolves the target a handler accessor was invoked on.
func (b *bridge) handlerKeyFor(this goja.Value, typ string) (handlerKey, bool) {
	o, _ := this.(*goja.Object)
	if o == nil || o == b.windowObj {
		return handlerKey{owner: ownerWindow, typ: typ}, true
	}
	n := b.objNode[o]
	if n == nil {
		return handlerKey{}, false
	}
	return b.nodeHandlerKey(n, typ)
}

func (b *bridge) nodeHandlerKey(n *html.Node, typ string) (handlerKey, bool) {
	switch {
	case n == b.doc:
		return handlerKey{owner: ownerDocument, typ: typ}, true
	case n.Type != html.ElementNode:
		return handlerKey{}, false
	case bodyForwarded[typ]:
		if b.isBodyLike(n) {
			return handlerKey{owner: ownerWindow, typ: typ}, true
		}
		if !handlerOnElements[typ] {
			return handlerKey{}, false // a WindowEventHandlers name on an ordinary element
		}
	}
	return handlerKey{owner: ownerNode, n: n, typ: typ}, true
}

// handlerOnElements are the names ordinary elements expose (GlobalEventHandlers).
var handlerOnElements = func() map[string]bool {
	m := make(map[string]bool, len(globalHandlerNames))
	for _, n := range globalHandlerNames {
		m[n] = true
	}
	return m
}()

// handlerRegistry returns the listener list a handler's entry lives in.
func (b *bridge) handlerRegistry(key handlerKey) map[string][]listenerEntry {
	switch key.owner {
	case ownerWindow:
		return b.winListeners
	case ownerDocument:
		return b.docListeners
	}
	m := b.nodeListeners[key.n]
	if m == nil {
		m = make(map[string][]listenerEntry)
		b.nodeListeners[key.n] = m
	}
	return m
}

// attrOwnerFor returns the element whose content attribute can supply key's
// handler: the element itself, or the document's <body> for a window handler.
func (b *bridge) attrOwnerFor(key handlerKey) *html.Node {
	switch key.owner {
	case ownerNode:
		return key.n
	case ownerWindow:
		if bodyForwarded[key.typ] {
			if body := b.bodyNode(); body != nil {
				return body
			}
		}
	}
	return nil
}

// bodyNode returns the document's <body> (or <frameset>), nil if it has none.
func (b *bridge) bodyNode() *html.Node {
	for c := b.doc.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "html" {
			continue
		}
		for g := c.FirstChild; g != nil; g = g.NextSibling {
			if g.Type == html.ElementNode && (g.Data == "body" || g.Data == "frameset") {
				return g
			}
		}
	}
	return nil
}

// ensureSlot returns key's slot, creating it — and its one listener entry — on
// first use. front places the entry ahead of the existing listeners: a handler
// that comes from markup was registered when the element was parsed, before any
// script could call addEventListener.
func (b *bridge) ensureSlot(key handlerKey, front bool) *handlerSlot {
	if s := b.handlerSlots[key]; s != nil {
		return s
	}
	if b.handlerSlots == nil {
		b.handlerSlots = make(map[handlerKey]*handlerSlot)
	}
	s := &handlerSlot{key: key}
	b.handlerSlots[key] = s
	reg := b.handlerRegistry(key)
	entry := listenerEntry{handler: s}
	if front && len(reg[key.typ]) > 0 {
		reg[key.typ] = append([]listenerEntry{entry}, reg[key.typ]...)
	} else {
		reg[key.typ] = append(reg[key.typ], entry)
	}
	return s
}

// slotFromMarkup returns key's slot, materialising it from a content attribute
// that has not been looked at yet. nil when there is neither a slot nor an
// attribute.
func (b *bridge) slotFromMarkup(key handlerKey) *handlerSlot {
	if s := b.handlerSlots[key]; s != nil {
		return s
	}
	owner := b.attrOwnerFor(key)
	if owner == nil || !hasAttr(owner, "on"+key.typ) {
		return nil
	}
	s := b.ensureSlot(key, true)
	s.fromAttr, s.attrOwner = true, owner
	return s
}

// primeNodeHandler makes sure an element's on<typ> content attribute has its
// listener entry before an event of that type is dispatched through it.
// attrName is "on"+typ, built once per dispatch.
func (b *bridge) primeNodeHandler(n *html.Node, typ, attrName string) {
	if len(n.Attr) == 0 || !hasAttr(n, attrName) || !handlerNames[typ] {
		return
	}
	if key, ok := b.nodeHandlerKey(n, typ); ok {
		b.slotFromMarkup(key)
	}
}

// primeWindowHandler does the same for <body onload="…"> and friends.
func (b *bridge) primeWindowHandler(typ string) {
	if bodyForwarded[typ] {
		b.slotFromMarkup(handlerKey{owner: ownerWindow, typ: typ})
	}
}

// handlerValue returns the slot's current handler, compiling a content
// attribute on first use and recompiling when the attribute text changed.
func (b *bridge) handlerValue(s *handlerSlot) goja.Value {
	if !s.fromAttr {
		return s.val
	}
	name := "on" + s.key.typ
	if !hasAttr(s.attrOwner, name) {
		return nil // the attribute was removed: the handler is null
	}
	src := getAttr(s.attrOwner, name)
	if s.attrDone && src == s.attrSrc {
		return s.val
	}
	s.attrDone, s.attrSrc = true, src
	s.val = b.compileAttrHandler(s, src)
	return s.val
}

// compileAttrHandler turns a content attribute's text into a function, per the
// standard's "getting the current value of the event handler": the text is a
// function body whose scope chain is the element, its form owner and the
// document, and whose parameter is `event` (or onerror's five on the window).
// Returns nil when the policy blocks it or the text does not parse.
func (b *bridge) compileAttrHandler(s *handlerSlot, body string) goja.Value {
	typ := s.key.typ
	if !b.cspAllowInlineHandler(body) {
		return nil
	}
	params := "event"
	if s.key.owner == ownerWindow && typ == "error" {
		params = "event, source, lineno, colno, error"
	}
	// The three scope objects arrive as arguments and `this`, so no helper names
	// leak into the handler's scope.
	src := "(function(){with(arguments[0]){with(arguments[1]){with(this){return function on" + typ +
		"(" + params + "){\n" + body + "\n}}}}})"
	prog, err := compileCached("handler.js", src)
	if err != nil {
		b.recordError(fmt.Errorf("on%s attribute: %w", typ, err))
		return nil
	}
	factoryV, err := b.vm.RunProgram(prog)
	if err != nil {
		b.recordError(err)
		return nil
	}
	factory, ok := goja.AssertFunction(factoryV)
	if !ok {
		return nil
	}
	// The document and the form owner resolve their named elements too
	// (`onclick="q.value = ''"`), which takes a scope object that answers `has`.
	var form goja.Value = b.vm.NewObject()
	if f := b.formOwner(s.attrOwner); f != nil {
		form = b.namedScope(b.wrap(f))
	}
	fn, err := factory(b.wrap(s.attrOwner), b.namedScope(b.documentObj), form)
	if err != nil {
		b.recordError(err)
		return nil
	}
	return fn
}

func (b *bridge) handlerGet(key handlerKey) goja.Value {
	s := b.slotFromMarkup(key)
	if s == nil {
		return goja.Null()
	}
	if v := b.handlerValue(s); v != nil {
		return v
	}
	return goja.Null()
}

// handlerSet is the IDL setter: an object is stored (and invoked if callable),
// anything else means null ([LegacyTreatNonObjectAsNull]).
func (b *bridge) handlerSet(key handlerKey, v goja.Value) {
	if _, isObj := v.(*goja.Object); !isObj {
		v = nil
	}
	s := b.slotFromMarkup(key)
	if s == nil {
		if v == nil {
			return // nothing to deactivate
		}
		s = b.ensureSlot(key, false)
	}
	s.fromAttr, s.val = false, v
}

// handlerAttrSet is called when script sets an on<typ> content attribute: the
// attribute becomes the handler, replacing an IDL-assigned one.
func (b *bridge) handlerAttrSet(n *html.Node, typ string) {
	if !handlerNames[typ] {
		return
	}
	key, ok := b.nodeHandlerKey(n, typ)
	if !ok {
		return
	}
	s := b.ensureSlot(key, false)
	s.fromAttr, s.attrOwner, s.attrDone, s.val = true, n, false, nil
}

// handlerAttrRemoved is called when script removes an on<typ> content
// attribute: the handler becomes null even if it had been assigned as a property.
func (b *bridge) handlerAttrRemoved(n *html.Node, typ string) {
	if !handlerNames[typ] {
		return
	}
	if key, ok := b.nodeHandlerKey(n, typ); ok {
		if s := b.handlerSlots[key]; s != nil {
			s.fromAttr, s.val = false, nil
		}
	}
}

// invokeHandler runs a handler entry for ev and applies the standard's
// return-value rule: false cancels the event — except for window.onerror, where
// true does and which is called with (message, source, lineno, colno, error).
func (b *bridge) invokeHandler(s *handlerSlot, this goja.Value, ev *domEvent) {
	fn, ok := goja.AssertFunction(b.handlerValue(s))
	if !ok {
		return
	}
	special := s.key.owner == ownerWindow && ev.typ == "error" && isErrorEventObject(ev.js)
	var (
		ret goja.Value
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = panicError(r)
			}
		}()
		if special {
			ret, err = fn(this, ev.js.Get("message"), ev.js.Get("filename"), ev.js.Get("lineno"), ev.js.Get("colno"), ev.js.Get("error"))
		} else {
			ret, err = fn(this, ev.js)
		}
	}()
	if err != nil {
		b.reportException(err)
		return
	}
	if ret == nil || ev.typ == "beforeunload" {
		return
	}
	bv, isBool := ret.Export().(bool)
	if !isBool {
		return
	}
	if bv == special && ev.cancelable && !ev.passiveActive {
		ev.defaultPrevented = true
	}
}

// isErrorEventObject reports whether an event object is an ErrorEvent — it
// carries a message — as opposed to a plain Event named "error" (the kind a
// failed <script>/<img> load fires).
func isErrorEventObject(o *goja.Object) bool {
	v := o.Get("message")
	return v != nil && !goja.IsUndefined(v)
}

// panicError turns a recovered panic into an error, keeping a goja exception
// (a throw that crossed a native frame) intact.
func panicError(r interface{}) error {
	if e, ok := r.(error); ok {
		return e
	}
	return fmt.Errorf("%v", r)
}

// callReporting calls a page callback (an event listener) and reports an
// exception it throws, where callSafe would swallow it.
func (b *bridge) callReporting(fn goja.Callable, this goja.Value, args ...goja.Value) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = panicError(r)
			}
		}()
		_, err = fn(this, args...)
	}()
	if err != nil {
		b.reportException(err)
	}
}

// reportException is the standard's "report an exception": an uncaught error
// from a script, an event listener or a timer fires an ErrorEvent at the window
// (window.onerror and `error` listeners), and — unless a handler cancels it —
// lands in the render diagnostics, the engine's stand-in for the console.
//
// Only a JavaScript throw is reported to the page. An interrupt (the render
// budget firing) or an engine fault goes to the diagnostics alone.
func (b *bridge) reportException(err error) {
	if err == nil {
		return
	}
	var ex *goja.Exception
	if !errors.As(err, &ex) {
		b.recordError(err)
		return
	}
	file, line, col := "", 0, 0
	if frames := ex.Stack(); len(frames) > 0 {
		pos := frames[0].Position()
		file, line, col = pos.Filename, pos.Line, pos.Column
	}
	if !b.fireErrorEvent(ex.Value(), file, line, col) {
		return // a handler took responsibility for it
	}
	b.recordError(err)
}

// reportThrown is reportException for a value caught in JS (the timer wrapper's
// try/catch), where there is no goja exception to read the position from.
func (b *bridge) reportThrown(v goja.Value) {
	file, line, col := "", 0, 0
	text := "Uncaught " + safeString(v)
	if o, ok := v.(*goja.Object); ok {
		if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
			stack := st.String()
			file, line, col = stackTop(stack)
			if stack != "" {
				text = "Uncaught " + stack
			}
		}
	}
	if !b.fireErrorEvent(v, file, line, col) {
		return
	}
	b.recordError(errors.New(text))
}

// fireErrorEvent dispatches the ErrorEvent for an uncaught exception and reports
// whether it was left unhandled. An exception thrown while one is already being
// reported is not dispatched again — an onerror handler that throws must not
// recurse.
func (b *bridge) fireErrorEvent(thrown goja.Value, file string, line, col int) (unhandled bool) {
	if b.reportingError || len(b.winListeners["error"]) == 0 && !b.hasBodyAttr("onerror") {
		return true
	}
	b.reportingError = true
	defer func() { b.reportingError = false }()
	if thrown == nil {
		thrown = goja.Undefined()
	}
	ev := b.newEvent("error", false, true, b.windowObj)
	_ = ev.js.Set("message", "Uncaught "+safeString(thrown))
	_ = ev.js.Set("filename", file)
	_ = ev.js.Set("lineno", line)
	_ = ev.js.Set("colno", col)
	_ = ev.js.Set("error", thrown)
	if ctor, ok := b.windowObj.Get("ErrorEvent").(*goja.Object); ok {
		if proto, ok := ctor.Get("prototype").(*goja.Object); ok {
			_ = ev.js.SetPrototype(proto)
		}
	}
	return b.dispatchOnWindow(ev)
}

func (b *bridge) hasBodyAttr(name string) bool {
	body := b.bodyNode()
	return body != nil && hasAttr(body, name)
}

// safeString stringifies a thrown value without letting a hostile toString
// throw back into the engine.
func safeString(v goja.Value) (s string) {
	defer func() {
		if recover() != nil {
			s = "[object]"
		}
	}()
	if v == nil {
		return "undefined"
	}
	return v.String()
}

// stackTop reads file, line and column from the first frame of a goja stack
// string ("\tat name (file:line:col(pc))"). Zeroes when it has no such frame.
func stackTop(stack string) (file string, line, col int) {
	for _, l := range strings.Split(stack, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "at ") {
			continue
		}
		loc := strings.TrimPrefix(l, "at ")
		if i := strings.LastIndex(loc, " ("); i >= 0 && strings.HasSuffix(loc, ")") {
			loc = loc[i+2 : len(loc)-1]
		}
		if i := strings.LastIndexByte(loc, '('); i >= 0 { // trailing "(pc)"
			loc = loc[:i]
		}
		c := strings.LastIndexByte(loc, ':')
		if c < 0 {
			return "", 0, 0
		}
		l2 := strings.LastIndexByte(loc[:c], ':')
		if l2 < 0 {
			return "", 0, 0
		}
		ln, err1 := strconv.Atoi(loc[l2+1 : c])
		cn, err2 := strconv.Atoi(loc[c+1:])
		if err1 != nil || err2 != nil {
			return "", 0, 0
		}
		return loc[:l2], ln, cn
	}
	return "", 0, 0
}
