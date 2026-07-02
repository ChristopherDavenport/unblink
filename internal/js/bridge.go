package js

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bridge exposes a minimal, live DOM over an *html.Node tree to a goja runtime.
// Object identity (el === el) is preserved via a node→wrapper cache. It also owns
// the event-listener registry and the async fetch machinery for a single render.
type bridge struct {
	vm   *goja.Runtime
	loop *eventloop.EventLoop
	doc  *html.Node
	// base is the page URL (the document's address): it seeds location/history and
	// scopes cookies. It is NOT the resolution base for relative URLs — that is
	// docBaseNow(), which honors an explicit <base href> element. Conflating the
	// two strips the deep-link path from location and breaks SPA routing.
	base *url.URL
	// explicitBase is the resolved <base href> element, or nil when the page has
	// none (then relative URLs resolve against the current document URL).
	explicitBase *url.URL
	transport    Transport // nil when JS networking is disabled
	cookies      CookieJar // nil when document.cookie is unavailable
	storage      Storage   // backs window.localStorage; nil → per-render prelude fallback
	sessStorage  Storage   // backs window.sessionStorage; nil → per-render prelude fallback
	ctx          context.Context
	reqTimeout   time.Duration

	cache   map[*html.Node]*goja.Object
	objNode map[*goja.Object]*html.Node

	// Shared DOM prototypes (built once in installPrototypes). Every wrapper is
	// created on one of these so instanceof / prototype patching work.
	protoEventTarget      *goja.Object
	protoNode             *goja.Object
	protoElement          *goja.Object
	protoHTMLElement      *goja.Object
	protoCharacterData    *goja.Object
	protoText             *goja.Object
	protoComment          *goja.Object
	protoDocumentFragment *goja.Object
	protoDocument         *goja.Object

	// Per-node companion objects, cached for identity (el.classList === el.classList).
	classListCache map[*html.Node]*goja.Object
	styleCache     map[*html.Node]*goja.Object
	datasetCache   map[*html.Node]*goja.Object

	// domVersion bumps on every DOM mutation made through the bridge. The live
	// Context polls it for a "DOM quiet" settle signal; MutationObserver flushing
	// keys off the same sink (onMutate).
	domVersion      uint64
	observers       []*mutationObserver
	pendingRecords  []mutationRecord
	flushScheduled  bool
	resolvedPromise *goja.Object  // a pre-resolved Promise, reused to schedule microtask flushes
	promiseThen     goja.Callable // resolvedPromise.then

	documentObj *goja.Object
	windowObj   *goja.Object

	// activeEl is the currently focused element (nil ⇒ <body>), tracked by
	// focus()/blur() and the interact press gesture so focus-driven reveals work.
	activeEl *html.Node

	// currentScript backs document.currentScript: the classic <script> element
	// whose source is executing right now (nil between scripts and for modules,
	// per spec). Loop-goroutine owned.
	currentScript *html.Node

	// webdriver backs navigator.webdriver. True by default (unblink is
	// automation); the engine flips it to false when the operator opted into
	// fingerprint parity (--tls-mimic).
	webdriver bool

	// SPA history/location. locationObj's fields are rewritten on pushState/
	// replaceState/back/forward so client-side routers see the active path.
	locationObj  *goja.Object
	historyObj   *goja.Object
	currentURL   *url.URL
	historyStack []historyEntry
	historyPos   int

	// pendingNav records a cross-document navigation the page's JS requested via
	// location.href/assign/replace but that a render does not follow. It is a signal
	// for the caller (surfaced on RenderResult/InteractResult), not an action. Set
	// only by navigate(), never by SPA pushState/popstate. Loop-goroutine owned.
	pendingNav *url.URL

	// Custom elements + flat Shadow DOM (Phase C). The registry maps a lowercase
	// tag to its constructor; upgrades reuse the existing node as `this`. A
	// ShadowRoot's backing node is its host, so shadow content lands in the light
	// tree and stays visible to extraction.
	customElements       map[string]*goja.Object
	customObserved       map[string]map[string]bool
	upgraded             map[*html.Node]bool
	connectedNotified    map[*html.Node]bool
	loadedScripts        map[*html.Node]bool // <script src> nodes already fetched+run once
	shadowRoots          map[*html.Node]*goja.Object
	templateContentCache map[*html.Node]*goja.Object
	upgradingStack       []*html.Node
	upgradeCount         int // total custom-element upgrades; bounds re-entrant upgrade blowup
	whenDefinedResolvers map[string][]func(interface{}) error
	reflectConstructFn   goja.Callable

	// diagErrors collects uncaught script/upgrade exceptions for render diagnostics.
	diagErrors []string
	rejections map[*goja.Promise]struct{} // promises rejected without a handler (yet)

	winListeners  map[string][]listenerEntry
	docListeners  map[string][]listenerEntry
	nodeListeners map[*html.Node]map[string][]listenerEntry

	jsSetTimeout   goja.Callable
	jsClearTimeout goja.Callable
	noopVal        goja.Value

	// timerAudit, when the prelude registered it, returns the count of one-shot
	// timers still scheduled — read on the settle-close tick for timers_pending.
	timerAudit goja.Callable

	// pending counts in-flight off-loop network requests (brackets the keepalive
	// window). A persistent live Context polls it to detect "network idle" since a
	// Start()ed loop's jobCount never reaches zero. Written/read on the loop goroutine.
	pending atomic.Int32

	// netCount is the countingTransport installed over the caller's Transport (nil
	// when the render has no network); render diagnostics read its totals.
	netCount *countingTransport

	// assets is the Engine's shared cross-render script/module/bundle cache (nil
	// when disabled). Set by the engine right after newBridge. Asset-cache hits
	// bypass the transport, so they never count toward NetRequests or the budget.
	assets *assetCache
}

func newBridge(vm *goja.Runtime, loop *eventloop.EventLoop, doc *html.Node, base *url.URL, transport Transport, cookies CookieJar, storage, sessStorage Storage, ctx context.Context, reqTimeout time.Duration) *bridge {
	var nc *countingTransport
	if transport != nil {
		nc = &countingTransport{inner: transport}
		transport = nc
	}
	return &bridge{
		vm:                   vm,
		loop:                 loop,
		doc:                  doc,
		base:                 base,
		explicitBase:         findExplicitBase(doc, base),
		transport:            transport,
		netCount:             nc,
		cookies:              cookies,
		storage:              storage,
		sessStorage:          sessStorage,
		ctx:                  ctx,
		reqTimeout:           reqTimeout,
		cache:                make(map[*html.Node]*goja.Object),
		objNode:              make(map[*goja.Object]*html.Node),
		classListCache:       make(map[*html.Node]*goja.Object),
		styleCache:           make(map[*html.Node]*goja.Object),
		datasetCache:         make(map[*html.Node]*goja.Object),
		customElements:       make(map[string]*goja.Object),
		customObserved:       make(map[string]map[string]bool),
		upgraded:             make(map[*html.Node]bool),
		connectedNotified:    make(map[*html.Node]bool),
		loadedScripts:        make(map[*html.Node]bool),
		shadowRoots:          make(map[*html.Node]*goja.Object),
		templateContentCache: make(map[*html.Node]*goja.Object),
		whenDefinedResolvers: make(map[string][]func(interface{}) error),
		winListeners:         make(map[string][]listenerEntry),
		docListeners:         make(map[string][]listenerEntry),
		nodeListeners:        make(map[*html.Node]map[string][]listenerEntry),
		webdriver:            true,
	}
}

// maxCallStackDepth bounds JS call-stack depth. goja's default is math.MaxInt32
// (effectively unbounded, relying on Go stack exhaustion), so a runaway recursion in
// untrusted page JS could consume large memory before failing. This browser-comparable
// cap makes it fail cleanly with a RangeError while leaving ample room for legitimate
// framework recursion.
const maxCallStackDepth = 10000

// install wires window/document/navigator/location/history, the event registry,
// and the async fetch primitive onto the runtime.
func (b *bridge) install() {
	vm := b.vm
	vm.SetMaxCallStackSize(maxCallStackDepth)
	win := vm.GlobalObject()
	b.windowObj = win
	b.noopVal = vm.ToValue(noop)
	if fn, ok := goja.AssertFunction(vm.Get("setTimeout")); ok {
		b.jsSetTimeout = fn
	}
	if fn, ok := goja.AssertFunction(vm.Get("clearTimeout")); ok {
		b.jsClearTimeout = fn
	}

	_ = vm.Set("window", win)
	_ = vm.Set("self", win)

	b.installPrototypes()

	doc := b.documentObject()
	b.documentObj = doc
	// Register the document node so wrap(b.doc) (e.g. html.parentNode) returns this
	// same object, and give it the Document prototype for instanceof.
	b.cache[b.doc] = doc
	b.objNode[doc] = b.doc
	_ = doc.SetPrototype(b.protoDocument)
	_ = vm.Set("document", doc)
	b.installStorage()
	_ = win.Set("document", doc)

	b.installGlobals(win)
	b.installNodeFilter()
	b.installMutationObserver()
	b.installCustomElements(win)
	b.installParsers()
	b.installAsync()
	b.installDynamicImport()
	b.trackRejections()
}

// installParsers exposes DOMParser. parseFromString returns a detached document
// facade over a freshly parsed tree (the same facade createHTMLDocument uses).
// XML input is parsed with the HTML parser — best-effort; unblink has no XML DOM.
func (b *bridge) installParsers() {
	vm := b.vm
	_ = vm.Set("DOMParser", func(call goja.ConstructorCall) *goja.Object {
		_ = call.This.Set("parseFromString", func(c goja.FunctionCall) goja.Value {
			docNode, err := html.Parse(strings.NewReader(c.Argument(0).String()))
			if err != nil {
				docNode = &html.Node{Type: html.DocumentNode}
			}
			return b.documentFacade(docNode)
		})
		return nil
	})
}

// trackRejections records promise rejections that never get a handler, so async
// framework errors (which never reach RunProgram) surface in diagnostics. A
// rejection that is later handled is removed.
func (b *bridge) trackRejections() {
	b.rejections = make(map[*goja.Promise]struct{})
	b.vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		switch op {
		case goja.PromiseRejectionReject:
			b.rejections[p] = struct{}{}
		case goja.PromiseRejectionHandle:
			delete(b.rejections, p)
		}
	})
}

// wrap returns the stable JS object for a node (creating and caching it once).
func (b *bridge) wrap(n *html.Node) goja.Value {
	if n == nil {
		return goja.Null()
	}
	if o, ok := b.cache[n]; ok {
		return o
	}
	o := b.vm.CreateObject(b.protoFor(n))
	b.cache[n] = o
	b.objNode[o] = n
	return o
}

// unwrap recovers the node behind a JS value, or nil if it isn't one of ours.
func (b *bridge) unwrap(v goja.Value) *html.Node {
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return nil
	}
	o := v.ToObject(b.vm)
	if o == nil {
		return nil
	}
	return b.objNode[o]
}

// --- accessor-property helpers ---

func (b *bridge) getterValue(fn func() goja.Value) goja.Value {
	return b.vm.ToValue(func(goja.FunctionCall) goja.Value { return fn() })
}

func (b *bridge) defineGetter(o *goja.Object, name string, get func() goja.Value) {
	_ = o.DefineAccessorProperty(name, b.getterValue(get), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
}

func (b *bridge) defineProp(o *goja.Object, name string, get func() goja.Value, set func(goja.Value)) {
	var setter goja.Value
	if set != nil {
		setter = b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			set(call.Argument(0))
			return goja.Undefined()
		})
	}
	_ = o.DefineAccessorProperty(name, b.getterValue(get), setter, goja.FLAG_FALSE, goja.FLAG_TRUE)
}

// --- document ---

func (b *bridge) documentObject() *goja.Object {
	vm := b.vm
	d := vm.NewObject()

	b.defineGetter(d, "documentElement", func() goja.Value { return b.wrap(findTag(b.doc, "html")) })
	b.defineGetter(d, "body", func() goja.Value { return b.wrap(findTag(b.doc, "body")) })
	b.defineGetter(d, "head", func() goja.Value { return b.wrap(findTag(b.doc, "head")) })
	b.defineGetter(d, "nodeType", func() goja.Value { return vm.ToValue(9) })
	b.defineGetter(d, "nodeName", func() goja.Value { return vm.ToValue("#document") })
	b.defineGetter(d, "activeElement", func() goja.Value {
		if b.activeEl != nil {
			return b.wrap(b.activeEl)
		}
		return b.wrap(findTag(b.doc, "body"))
	})
	b.defineGetter(d, "defaultView", func() goja.Value { return b.windowObj })
	b.defineGetter(d, "readyState", func() goja.Value { return vm.ToValue("complete") })
	b.defineGetter(d, "hidden", func() goja.Value { return vm.ToValue(false) })
	b.defineGetter(d, "visibilityState", func() goja.Value { return vm.ToValue("visible") })
	// title reads/writes the live <title> element so a SPA's document.title
	// assignment lands in the tree extraction sees. The write goes through the
	// mutation sink: domVersion is the settle/staleness signal.
	b.defineProp(d, "title",
		func() goja.Value {
			if t := findTag(b.doc, "title"); t != nil {
				return vm.ToValue(textContent(t))
			}
			return vm.ToValue("")
		},
		func(v goja.Value) {
			t := findTag(b.doc, "title")
			if t == nil {
				parent := findTag(b.doc, "head")
				if parent == nil {
					parent = findTag(b.doc, "html")
				}
				if parent == nil {
					return
				}
				t = &html.Node{Type: html.ElementNode, Data: "title", DataAtom: atom.Title}
				parent.AppendChild(t)
			}
			setTextContent(t, v.String())
			b.onMutate(mutationRecord{typ: "characterData", target: t})
		})
	docURL := func() goja.Value {
		if b.currentURL == nil {
			return vm.ToValue("")
		}
		return vm.ToValue(b.currentURL.String())
	}
	b.defineGetter(d, "URL", docURL)
	b.defineGetter(d, "documentURI", docURL)
	// document.location aliases window.location; assignment navigates, like the
	// location.href accessor.
	b.defineProp(d, "location",
		func() goja.Value { return b.locationObjValue() },
		func(v goja.Value) { b.navigate(v.String()) })
	b.defineGetter(d, "referrer", func() goja.Value { return vm.ToValue("") })
	b.defineGetter(d, "characterSet", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "charset", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "inputEncoding", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "compatMode", func() goja.Value { return vm.ToValue("CSS1Compat") })
	b.defineGetter(d, "contentType", func() goja.Value { return vm.ToValue("text/html") })
	b.defineGetter(d, "currentScript", func() goja.Value { return b.wrap(b.currentScript) })
	// getElementsByName matches the name attribute with a manual walk (not a
	// cascadia selector, so arbitrary name values need no escaping).
	_ = d.Set("getElementsByName", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		var out []*html.Node
		var walk func(*html.Node)
		walk = func(nd *html.Node) {
			if nd.Type == html.ElementNode && getAttr(nd, "name") == name {
				out = append(out, nd)
			}
			for c := nd.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(b.doc)
		return b.nodeList(out)
	})

	_ = d.Set("getElementById", func(call goja.FunctionCall) goja.Value {
		return b.wrap(findByID(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("querySelector", func(call goja.FunctionCall) goja.Value {
		return b.wrap(query(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("getElementsByTagName", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("getElementsByClassName", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(b.doc, "."+call.Argument(0).String()))
	})
	b.defineGetter(d, "forms", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "form")) })
	_ = d.Set("createElement", func(call goja.FunctionCall) goja.Value {
		tag := strings.ToLower(call.Argument(0).String())
		return b.wrap(b.newElement(tag, ""))
	})
	_ = d.Set("createElementNS", func(call goja.FunctionCall) goja.Value {
		ns := call.Argument(0).String()
		tag := strings.ToLower(call.Argument(1).String())
		return b.wrap(b.newElement(tag, namespaceFor(ns)))
	})
	_ = d.Set("createTextNode", func(call goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.TextNode, Data: call.Argument(0).String()})
	})
	_ = d.Set("createComment", func(call goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.CommentNode, Data: call.Argument(0).String()})
	})
	_ = d.Set("importNode", func(call goja.FunctionCall) goja.Value {
		src := b.unwrap(call.Argument(0))
		if src == nil {
			return goja.Null()
		}
		return b.wrap(cloneNode(src, call.Argument(1).ToBoolean()))
	})
	_ = d.Set("adoptNode", func(call goja.FunctionCall) goja.Value { return call.Argument(0) })
	_ = d.Set("createDocumentFragment", func(goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.DocumentNode})
	})
	_ = d.Set("createTreeWalker", func(call goja.FunctionCall) goja.Value {
		root := b.unwrap(call.Argument(0))
		if root == nil {
			root = b.doc
		}
		whatToShow := int64(0)
		if w := call.Argument(1); w != nil && !goja.IsUndefined(w) {
			whatToShow = w.ToInteger()
		}
		return b.createTreeWalker(root, whatToShow, call.Argument(2))
	})
	_ = d.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		b.addListener(b.docListeners, call)
		return goja.Undefined()
	})
	_ = d.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		b.removeListener(b.docListeners, call)
		return goja.Undefined()
	})
	_ = d.Set("dispatchEvent", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(b.dispatchUserEvent(call.Argument(0), d, b.dispatchOnDocument))
	})

	b.defineProp(d, "cookie",
		func() goja.Value {
			if b.cookies == nil || b.base == nil {
				return vm.ToValue("")
			}
			return vm.ToValue(b.cookies.Cookies(b.base.String()))
		},
		func(v goja.Value) {
			if b.cookies == nil || b.base == nil {
				return
			}
			b.cookies.SetCookie(b.base.String(), v.String())
		})

	impl := vm.NewObject()
	_ = impl.Set("createHTMLDocument", func(call goja.FunctionCall) goja.Value {
		title := ""
		if a := call.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
			title = a.String()
		}
		return b.detachedHTMLDocument(title)
	})
	_ = impl.Set("hasFeature", func(goja.FunctionCall) goja.Value { return vm.ToValue(true) })
	b.defineGetter(d, "implementation", func() goja.Value { return impl })

	return d
}

// locationObjValue returns the live location object (document.location aliases
// window.location), or undefined before installGlobals has run.
func (b *bridge) locationObjValue() goja.Value {
	if b.locationObj == nil {
		return goja.Undefined()
	}
	return b.locationObj
}

// detachedHTMLDocument backs document.implementation.createHTMLDocument(): a
// fresh, disconnected <html><head><title/></head><body/></html> tree behind a
// minimal document facade. Angular's DomSanitizer (behind every [innerHTML]
// binding) and jQuery's parseHTML build one to parse untrusted markup inertly.
func (b *bridge) detachedHTMLDocument(title string) goja.Value {
	docNode := &html.Node{Type: html.DocumentNode}
	htmlEl := &html.Node{Type: html.ElementNode, Data: "html", DataAtom: atom.Html}
	headEl := &html.Node{Type: html.ElementNode, Data: "head", DataAtom: atom.Head}
	titleEl := &html.Node{Type: html.ElementNode, Data: "title", DataAtom: atom.Title}
	titleEl.AppendChild(&html.Node{Type: html.TextNode, Data: title})
	bodyEl := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	docNode.AppendChild(htmlEl)
	htmlEl.AppendChild(headEl)
	headEl.AppendChild(titleEl)
	htmlEl.AppendChild(bodyEl)
	return b.documentFacade(docNode)
}

// documentFacade wraps a detached document tree (createHTMLDocument, DOMParser)
// in a minimal document object. The returned nodes share this bridge's
// prototypes, so innerHTML/querySelector/removeChild work on the inert tree,
// but nothing connects it to the live page.
func (b *bridge) documentFacade(docNode *html.Node) goja.Value {
	vm := b.vm

	d := vm.NewObject()
	b.defineGetter(d, "documentElement", func() goja.Value { return b.wrap(findTag(docNode, "html")) })
	b.defineGetter(d, "body", func() goja.Value { return b.wrap(findTag(docNode, "body")) })
	b.defineGetter(d, "head", func() goja.Value { return b.wrap(findTag(docNode, "head")) })
	b.defineGetter(d, "title", func() goja.Value {
		if t := findTag(docNode, "title"); t != nil {
			return vm.ToValue(textContent(t))
		}
		return vm.ToValue("")
	})
	b.defineGetter(d, "nodeType", func() goja.Value { return vm.ToValue(9) })
	b.defineGetter(d, "nodeName", func() goja.Value { return vm.ToValue("#document") })
	_ = d.Set("createElement", func(call goja.FunctionCall) goja.Value {
		return b.wrap(b.newElement(strings.ToLower(call.Argument(0).String()), ""))
	})
	_ = d.Set("createElementNS", func(call goja.FunctionCall) goja.Value {
		ns := call.Argument(0).String()
		return b.wrap(b.newElement(strings.ToLower(call.Argument(1).String()), namespaceFor(ns)))
	})
	_ = d.Set("createTextNode", func(call goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.TextNode, Data: call.Argument(0).String()})
	})
	_ = d.Set("createComment", func(call goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.CommentNode, Data: call.Argument(0).String()})
	})
	_ = d.Set("createDocumentFragment", func(goja.FunctionCall) goja.Value {
		return b.wrap(&html.Node{Type: html.DocumentNode})
	})
	_ = d.Set("getElementById", func(call goja.FunctionCall) goja.Value {
		return b.wrap(findByID(docNode, call.Argument(0).String()))
	})
	_ = d.Set("querySelector", func(call goja.FunctionCall) goja.Value {
		return b.wrap(query(docNode, call.Argument(0).String()))
	})
	_ = d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(docNode, call.Argument(0).String()))
	})
	_ = d.Set("appendChild", func(call goja.FunctionCall) goja.Value {
		if n := b.unwrap(call.Argument(0)); n != nil && n.Parent == nil {
			docNode.AppendChild(n)
		}
		return call.Argument(0)
	})
	return d
}

// --- node wrappers ---

func (b *bridge) nodeList(nodes []*html.Node) goja.Value {
	arr := make([]interface{}, len(nodes))
	for i, n := range nodes {
		arr[i] = b.wrap(n)
	}
	return b.vm.ToValue(arr)
}

// classListFor returns the cached DOMTokenList for n, backing its class attribute
// with add/remove/toggle/contains. Cached so el.classList === el.classList.
func (b *bridge) classListFor(n *html.Node) *goja.Object {
	if o, ok := b.classListCache[n]; ok {
		return o
	}
	vm := b.vm
	cl := vm.NewObject()
	tokens := func() []string { return strings.Fields(getAttr(n, "class")) }
	write := func(ts []string) { b.setAttrMut(n, "class", strings.Join(ts, " ")) }
	has := func(c string) bool {
		for _, t := range tokens() {
			if t == c {
				return true
			}
		}
		return false
	}
	_ = cl.Set("contains", func(call goja.FunctionCall) goja.Value { return vm.ToValue(has(call.Argument(0).String())) })
	_ = cl.Set("add", func(call goja.FunctionCall) goja.Value {
		ts := tokens()
		for _, a := range call.Arguments {
			c := a.String()
			if c != "" && !has(c) {
				ts = append(ts, c)
			}
		}
		write(ts)
		return goja.Undefined()
	})
	_ = cl.Set("remove", func(call goja.FunctionCall) goja.Value {
		drop := map[string]bool{}
		for _, a := range call.Arguments {
			drop[a.String()] = true
		}
		var ts []string
		for _, t := range tokens() {
			if !drop[t] {
				ts = append(ts, t)
			}
		}
		write(ts)
		return goja.Undefined()
	})
	_ = cl.Set("toggle", func(call goja.FunctionCall) goja.Value {
		c := call.Argument(0).String()
		if has(c) {
			var ts []string
			for _, t := range tokens() {
				if t != c {
					ts = append(ts, t)
				}
			}
			write(ts)
			return vm.ToValue(false)
		}
		write(append(tokens(), c))
		return vm.ToValue(true)
	})
	b.classListCache[n] = cl
	return cl
}

// styleFor returns the cached, writable style object for n. There is no CSSOM:
// it is a plain property bag (so `el.style.display = 'none'` doesn't throw), not
// reflected back to the class/serialized output. Cached for identity.
func (b *bridge) styleFor(n *html.Node) *goja.Object {
	if o, ok := b.styleCache[n]; ok {
		return o
	}
	o := b.vm.NewObject()
	_ = o.Set("setProperty", noop)
	_ = o.Set("removeProperty", func(goja.FunctionCall) goja.Value { return b.vm.ToValue("") })
	_ = o.Set("getPropertyValue", func(goja.FunctionCall) goja.Value { return b.vm.ToValue("") })
	b.styleCache[n] = o
	return o
}

func noop(goja.FunctionCall) goja.Value { return goja.Undefined() }

var selBaseHref = cascadia.MustCompile("base[href]")

// findExplicitBase resolves the document's <base href> element against the page
// URL, or returns nil when the page declares none.
func findExplicitBase(doc *html.Node, pageURL *url.URL) *url.URL {
	if doc == nil {
		return nil
	}
	bn := selBaseHref.MatchFirst(doc)
	if bn == nil {
		return nil
	}
	href := strings.TrimSpace(getAttr(bn, "href"))
	if href == "" {
		return nil
	}
	u, err := url.Parse(href)
	if err != nil {
		return nil
	}
	if u.IsAbs() {
		return u
	}
	if pageURL == nil {
		return nil
	}
	return pageURL.ResolveReference(u)
}

// docBaseNow is the base for resolving relative URLs (fetch/XHR, script src,
// module specifiers, link targets): the explicit <base href> when the page has
// one, otherwise the current document URL (which pushState updates, as in a real
// browser). location/history never use this — they track the page URL itself.
func (b *bridge) docBaseNow() *url.URL {
	if b.explicitBase != nil {
		return b.explicitBase
	}
	if b.currentURL != nil {
		return b.currentURL
	}
	return b.base
}

// --- *html.Node helpers ---

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func detach(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func findTag(root *html.Node, tag string) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if found != nil {
			return
		}
		if nd.Type == html.ElementNode && nd.Data == tag {
			found = nd
			return
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

func findByID(root *html.Node, id string) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if found != nil {
			return
		}
		if nd.Type == html.ElementNode && getAttr(nd, "id") == id {
			found = nd
			return
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

func query(root *html.Node, selector string) *html.Node {
	sel, err := compileSelector(selector)
	if err != nil {
		return nil
	}
	return cascadia.Query(root, sel)
}

func queryAll(root *html.Node, selector string) []*html.Node {
	sel, err := compileSelector(selector)
	if err != nil {
		return nil
	}
	return cascadia.QueryAll(root, sel)
}

func childNodes(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, c)
	}
	return out
}

func childElements(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

func nextElement(n *html.Node) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			sb.WriteString(nd.Data)
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func setTextContent(n *html.Node, text string) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
}

func innerHTML(n *html.Node) string {
	var buf bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	return buf.String()
}

func outerHTML(n *html.Node) string {
	var buf bytes.Buffer
	_ = html.Render(&buf, n)
	return buf.String()
}

func setInnerHTML(n *html.Node, markup string) {
	context := n
	if context.Type != html.ElementNode {
		context = &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	}
	frag, err := html.ParseFragment(strings.NewReader(markup), context)
	if err != nil {
		return
	}
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	for _, c := range frag {
		n.AppendChild(c)
	}
}
