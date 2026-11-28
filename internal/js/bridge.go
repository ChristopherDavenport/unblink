package js

import (
	"bytes"
	"context"
	"net/http"
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
	// bound is set once bind() has attached a page; preludesRan once prepare() has
	// run the preludes on this runtime (so bind knows what to patch up).
	bound       bool
	preludesRan bool
	transport   Transport // nil when JS networking is disabled
	cookies     CookieJar // nil when document.cookie is unavailable
	storage     Storage   // backs window.localStorage; nil → per-render prelude fallback
	sessStorage Storage   // backs window.sessionStorage; nil → per-render prelude fallback
	ctx         context.Context
	reqTimeout  time.Duration

	// allowCrossOrigin disables the same-origin/CORS enforcement applied to
	// page-JS fetch/XHR (escape hatch; default false = enforce). The extension
	// background worker sets it true, since extensions legitimately fetch
	// cross-origin. See cors.go and ADR 0011.
	allowCrossOrigin bool
	// sriEnabled turns on Subresource Integrity verification of the subresources
	// the engine executes (default true for page renders). See sri.go and ADR 0012.
	sriEnabled bool
	// respHeaders is the main document's HTTP response headers, the source of the
	// document's Content-Security-Policy / Cross-Origin-Opener-Policy /
	// Cross-Origin-Embedder-Policy. nil when unavailable. See csp.go, isolation.go.
	respHeaders http.Header
	// cspEnabled turns on Content-Security-Policy enforcement (default true for page
	// renders). csp is the parsed policy set (header + <meta>), nil when the document
	// declared none or enforcement is off. See csp.go and ADR 0014.
	cspEnabled bool
	csp        *cspContext

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
	protoShadowRoot       *goja.Object
	protoDocumentType     *goja.Object
	textSelections        map[*html.Node]*textSelection // input/textarea selection state (installTextSelection)
	animatedStrings       map[*goja.Object]*html.Node   // SVGAnimatedString → its element
	animatedStringOf      map[*html.Node]*goja.Object
	scriptOrdinal         map[*html.Node]int // classic script → its document position (runDocumentScripts)
	protoNodeList         *goja.Object       // see installCollections
	protoHTMLCollection   *goja.Object
	protoRadioNodeList    *goja.Object // see installFormNamedAccess
	protoFormControls     *goja.Object
	namedScopeFn          goja.Callable           // prelude helper behind namedScope
	objectProto           *goja.Object            // Object.prototype, cached for adoptEventInterface
	eventProtos           map[string]*goja.Object // interface name → prototype (adoptEventInterface)
	protoXPathResult      *goja.Object
	protoXPathExpression  *goja.Object
	protoSVGElement       *goja.Object
	protoUnknownElement   *goja.Object
	ifaceProtos           map[string]*goja.Object // interface name → prototype
	tagProtos             map[string]*goja.Object // HTML tag → interface prototype
	protoDocument         *goja.Object

	// Per-node companion objects, cached for identity (el.classList === el.classList).
	tokenListCache map[tokenListKey]*goja.Object // classList / relList, keyed by (node, attribute)
	styleCache     map[*html.Node]*goja.Object
	dialogReturns  map[*html.Node]string // HTMLDialogElement.returnValue (dialog.go)
	// Reflected IDL attributes (reflect.go): the shared accessor per rule, and the
	// interface prototypes whose attributes are not installed yet.
	reflectAccessors map[string]goja.Value
	reflectPending   map[*goja.Object]string
	styleOf          map[*goja.Object]*cssStyle // style object → its declaration block (cssstyle.go)
	styleMethods     map[string]goja.Value      // shared CSSStyleDeclaration methods
	// styleVersion counts inline-style writes made through el.style. They make a
	// live snapshot stale but are not settle activity (see setStyleAttr).
	styleVersion uint64
	datasetCache map[*html.Node]*goja.Object

	// domVersion bumps on every DOM mutation made through the bridge. The live
	// Context polls it for a "DOM quiet" settle signal; MutationObserver flushing
	// keys off the same sink (onMutate).
	domVersion      uint64
	observers       []*mutationObserver
	flushScheduled  bool
	resolvedPromise *goja.Object  // a pre-resolved Promise, reused to schedule microtask flushes
	promiseThen     goja.Callable // resolvedPromise.then

	documentObj *goja.Object
	windowObj   *goja.Object

	// handlerSlots holds the on<type> event handlers (eventhandlers.go), one slot
	// per (target, type); reportingError guards "report an exception" against an
	// error handler that itself throws.
	handlerSlots   map[handlerKey]*handlerSlot
	reportingError bool

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

	// Custom elements + encapsulating Shadow DOM (Phase C / Phase 23). The registry
	// maps a lowercase tag to its constructor; upgrades reuse the existing node as
	// `this`. A shadow root has its own detached backing node (a DocumentNode), kept
	// out of the light tree so page-JS queries respect the boundary; the compose pass
	// (slots.go) flattens shadow content into the light tree for extraction.
	customElements       map[string]*goja.Object
	customObserved       map[string]map[string]bool
	upgraded             map[*html.Node]bool
	connectedNotified    map[*html.Node]bool
	loadedScripts        map[*html.Node]bool         // <script src> nodes already fetched+run once
	nonces               map[*html.Node]string       // CSP nonce captured pre-JS, then blanked in the DOM (hideNonces)
	shadowRoots          map[*html.Node]*goja.Object // host → ShadowRoot object (whose backing node is a detached DocumentNode)
	shadowHostOf         map[*html.Node]*html.Node   // shadow-root backing node → host (reverse, for connectivity/retargeting)
	templateContentCache map[*html.Node]*goja.Object
	// readyState is document.readyState: "loading" while the parser-inserted
	// classic scripts run, "interactive" from then until DOMContentLoaded has been
	// dispatched, "complete" before load. Empty (a bridge that hosts no page
	// load: an extension background) reads as "complete". See setReadyState.
	readyState     string
	attrMapCache   map[*html.Node]*goja.Object            // el.attributes (live NamedNodeMap)
	attrNodeCache  map[*html.Node]map[string]*goja.Object // stable Attr objects
	indexedIterFn  goja.Value                             // shared @@iterator for index/length collections
	upgradingStack []*html.Node
	// Form-associated custom elements (forminternals.go): the definitions that
	// declared `static formAssociated` / disabled internals, and per-element state.
	formAssoc         map[string]bool
	internalsDisabled map[string]bool
	faces             map[*html.Node]*faceState
	faceOrder         []*html.Node // the keys of faces in creation order (deterministic iteration)
	// Form-associated custom elements carrying a form attribute, by the id it names,
	// so an id change re-resolves only the elements that name it (syncFormRefs).
	faceFormRefs map[string][]*html.Node
	formRefEpoch uint64 // bumped by every mutation that can change what an id names
	// ElementInternals state every custom element can hold (internalsstate.go): its
	// default ARIA semantics and its CustomStateSet. Both maps are created on first use.
	ariaDefaults         map[*html.Node]*ariaDefaults
	customStates         map[*html.Node][]string
	upgradeCount         int // total custom-element upgrades; bounds re-entrant upgrade blowup
	whenDefinedResolvers map[string][]func(interface{}) error
	reflectConstructFn   goja.Callable

	// diagErrors collects uncaught script/upgrade exceptions for render diagnostics.
	diagErrors []string
	rejections map[*goja.Promise]struct{} // promises rejected without a handler (yet)
	// rejectQueue holds promises rejected since the last unhandledrejection pass;
	// rejectCheck is set while that pass is scheduled (see queueUnhandledRejection).
	rejectQueue []*goja.Promise
	rejectCheck bool
	// consoleLog captures page console.* output for the console tool (capped);
	// consoleDropped counts messages past the cap. Written on the loop goroutine.
	consoleLog     []ConsoleMsg
	consoleDropped int

	winListeners  map[string][]listenerEntry
	docListeners  map[string][]listenerEntry
	nodeListeners map[*html.Node]map[string][]listenerEntry

	jsSetTimeout   goja.Callable
	jsClearTimeout goja.Callable
	noopVal        goja.Value

	// timerAudit, when the prelude registered it, returns {c, t}: clamped
	// one-shot timers still scheduled (read on the settle-close tick for
	// timers_pending) and all live wrapped timers (the provable-idle signal).
	timerAudit goja.Callable

	// pending counts in-flight off-loop network requests (brackets the keepalive
	// window). A persistent live Context polls it to detect "network idle" since a
	// Start()ed loop's jobCount never reaches zero. Written/read on the loop goroutine.
	pending atomic.Int32

	// netCount is the countingTransport installed over the caller's Transport (nil
	// when the render has no network); render diagnostics read its totals.
	netCount *countingTransport

	// prefetch holds the in-flight concurrent fetches of the initial external
	// <script src> bodies (nil unless startPrefetch armed it). Written once on
	// the loop goroutine before runScripts; entries are filled by off-loop
	// workers and read via prefetched(), which blocks on the entry's done.
	prefetch map[string]*prefetchEntry

	// assets is the Engine's shared cross-render script/module/transform cache (nil
	// when disabled). Set by the engine right after newBridge. Asset-cache hits
	// bypass the transport, so they never count toward NetRequests or the budget.
	assets *assetCache

	// extHost is the engine-lifetime WebExtension state (declarativeNetRequest network
	// rules in Phase 1), shared read-only across renders. nil when no extension is
	// loaded. The subrequest network gate (blockingTransport) is wired from it.
	extHost *ExtensionHost

	// cosmeticSelectors are element-hiding selectors gathered from loaded extensions'
	// content-script CSS (and injected styles); seenCosmetic de-dupes them. Applied as
	// physical node detachment once the DOM is frozen (no CSSOM). nil without an extension.
	cosmeticSelectors []string
	seenCosmetic      map[string]bool

	// bgMode marks the background-service-worker bridge (Phase 3): chrome.runtime.onMessage
	// registers real listeners here (in msgListeners) instead of the page's stub.
	bgMode          bool
	msgListeners    []goja.Callable            // background onMessage listeners
	webReqListeners []webReqListener           // background MV2 webRequest.onBeforeRequest listeners
	hostEvents      map[string][]goja.Callable // background webNavigation/tabs listeners the host fires
	extActiveID     string                     // active extension id, for message sender identity
	reqSeq          int
	tabID           int        // synthetic tab id for this page render/session (0 = none)
	inert           goja.Value // cached self-propagating inert stub for unimplemented chrome.* APIs

	// dynSem bounds how many modules fetch+transform off-loop at once, so a page
	// with a wide module graph or hundreds of concurrent import()s can't spawn an
	// unbounded number of esbuild transforms. Buffered to dynImportConcurrency.
	dynSem chan struct{}

	// reg is the per-render module-instance registry backing dynamic import(): it
	// shares one instance of each module URL across all importers (module-map
	// semantics), so a dependency shared by several import()s isn't duplicated.
	reg *moduleRegistry

	// importMap is the document's parsed <script type="importmap"> (nil when it has
	// none). acquireImportMap sets it once, on the loop, when module loading begins
	// (importMapAcquired); it is immutable after, so the off-loop module instantiate,
	// which only starts later, may read it.
	importMap         *importMap
	importMapAcquired bool
}

// resetTransportBudget clears the per-dispatch download budget on the counting
// transport (which forwards to the guarded transport underneath). No-op when JS
// networking is disabled.
func (b *bridge) resetTransportBudget() {
	if b.netCount != nil {
		b.netCount.ResetBudget()
	}
}

func newBridge(vm *goja.Runtime, loop *eventloop.EventLoop, doc *html.Node, base *url.URL, transport Transport, cookies CookieJar, storage, sessStorage Storage, ctx context.Context, reqTimeout time.Duration, extHost *ExtensionHost) *bridge {
	b := newBridgeShell(vm, loop, extHost)
	b.bind(doc, base, transport, cookies, storage, sessStorage, ctx, reqTimeout)
	return b
}

// newBridgeShell returns a bridge that is not yet bound to a document. Everything
// install() and the preludes build — prototypes, interface objects, the window
// and document objects — closes over the bridge and reads the document, base URL,
// transport and stores only when called, so that whole setup can run on a shell
// ahead of time (the pre-warm pool, see Engine.prepare) and be bound to a page by
// bind() when a render takes it.
func newBridgeShell(vm *goja.Runtime, loop *eventloop.EventLoop, extHost *ExtensionHost) *bridge {
	tabID := 0
	if extHost != nil {
		tabID = extHost.nextTabID()
	}
	return &bridge{
		vm:                   vm,
		loop:                 loop,
		extHost:              extHost,
		tabID:                tabID,
		dynSem:               make(chan struct{}, dynImportConcurrency),
		reg:                  newModuleRegistry(),
		cache:                make(map[*html.Node]*goja.Object),
		objNode:              make(map[*goja.Object]*html.Node),
		tokenListCache:       make(map[tokenListKey]*goja.Object),
		styleCache:           make(map[*html.Node]*goja.Object),
		datasetCache:         make(map[*html.Node]*goja.Object),
		customElements:       make(map[string]*goja.Object),
		customObserved:       make(map[string]map[string]bool),
		upgraded:             make(map[*html.Node]bool),
		connectedNotified:    make(map[*html.Node]bool),
		loadedScripts:        make(map[*html.Node]bool),
		shadowRoots:          make(map[*html.Node]*goja.Object),
		shadowHostOf:         make(map[*html.Node]*html.Node),
		templateContentCache: make(map[*html.Node]*goja.Object),
		attrMapCache:         make(map[*html.Node]*goja.Object),
		attrNodeCache:        make(map[*html.Node]map[string]*goja.Object),
		formAssoc:            make(map[string]bool),
		internalsDisabled:    make(map[string]bool),
		faces:                make(map[*html.Node]*faceState),
		whenDefinedResolvers: make(map[string][]func(interface{}) error),
		winListeners:         make(map[string][]listenerEntry),
		docListeners:         make(map[string][]listenerEntry),
		nodeListeners:        make(map[*html.Node]map[string][]listenerEntry),
		webdriver:            true,
	}
}

// bind attaches the bridge to one page: its document, base URL, network
// transport, cookie jar and storage areas. Called once, either before install()
// (the background worker) or after install() and the preludes have run on a shell
// (renders and live contexts) — in which case it also completes the two pieces of
// the installed world that are per page: the document object's identity with the
// document node, and the storage globals.
func (b *bridge) bind(doc *html.Node, base *url.URL, transport Transport, cookies CookieJar, storage, sessStorage Storage, ctx context.Context, reqTimeout time.Duration) {
	var nc *countingTransport
	if transport != nil {
		if b.extHost != nil {
			// declarativeNetRequest gates every subrequest (blockingTransport); a
			// chrome-extension:// URL is served from the bundle before the gate
			// (extResourceTransport) so extension resources aren't DNR-blocked. Both sit
			// inside the counting transport so they still show in the requests diagnostics.
			transport = &blockingTransport{inner: transport, host: b.extHost, initiator: initiatorHost(base), pageURL: baseURLString(base), tabID: b.tabID}
			transport = &extResourceTransport{inner: transport, host: b.extHost, page: base}
		}
		// Attach truthful Sec-Fetch-* to every subrequest, just inside the counting
		// transport so the request log is unaffected. See secfetch.go / ADR 0014.
		transport = &secFetchTransport{inner: transport, base: base}
		nc = &countingTransport{inner: transport}
		transport = nc
	}
	b.doc = doc
	b.base = base
	b.explicitBase = findExplicitBase(doc, base)
	b.transport = transport
	b.netCount = nc
	b.cookies = cookies
	b.storage = storage
	b.sessStorage = sessStorage
	b.ctx = ctx
	b.reqTimeout = reqTimeout
	b.bound = true
	if b.documentObj != nil {
		b.bindInstalled()
	}
}

// bindInstalled finishes install() for a bridge whose document arrived afterwards.
func (b *bridge) bindInstalled() {
	// Register the document node so wrap(b.doc) (e.g. html.parentNode) returns the
	// document object.
	b.cache[b.doc] = b.documentObj
	b.objNode[b.documentObj] = b.doc
	b.sealTemplates(b.doc) // template contents are inert before any script runs
	b.installStorage()
	if b.locationObj != nil {
		b.currentURL = b.base
		b.updateLocation(b.base)
	}
	if b.base != nil && len(b.historyStack) == 0 {
		b.historyStack = []historyEntry{{url: b.base}}
	}
	if b.transport == nil && b.preludesRan {
		// The preludes built fetch/XHR over __unblinkFetch before the page was known;
		// without a transport they are the rejecting stubs instead.
		_ = b.vm.GlobalObject().Delete("__unblinkFetch")
		_, _ = b.vm.RunProgram(noNetProgram)
	}
}

// finishBind runs the preludes' bind hooks (see preludeJS): the page-dependent
// state they could not read while the runtime was being prepared — the page URL,
// the navigation start, the timer deadline, the isolation flags, the stores. It
// must run once the bridge is bound, the preludes have run, and every per-render
// field the hooks read (respHeaders, the timer-deadline globals) is set; the hook
// entry points are then removed so page code never sees them.
func (b *bridge) finishBind() {
	b.installIsolationGlobals()
	g := b.vm.GlobalObject()
	if fn, ok := goja.AssertFunction(g.Get("__unblinkBind")); ok {
		_, _ = fn(goja.Undefined())
	}
	_ = g.Delete("__unblinkBind")
	_ = g.Delete("__unblinkOnBind")
}

// noNetProgram is what the API prelude installs for fetch/XHR when the runtime has
// no transport. A prepared shell ran that prelude before its page was bound, so
// bind() applies this afterwards; it must stay identical to the prelude's branch.
var noNetProgram = goja.MustCompile("nonet.js", noNetJS, false)

const noNetJS = `(function () {
  var window = globalThis;
` + noNetStubsJS + `
})();`

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
	// The event loop's runtime comes with a Node-style `require` global (neutered:
	// see rejectRequire). A browser has none, and page code tests for it —
	// `typeof require` is how a bundle decides it is running under CommonJS. The
	// engine's own modules are wired from Go, so the binding is simply removed
	// from the page world; a page (an AMD loader) may then define its own.
	_ = vm.GlobalObject().Delete("require")

	b.installPrototypes()

	doc := b.documentObject()
	b.documentObj = doc
	// Give it the Document prototype for instanceof. Its identity with the document
	// node, and the storage globals, are per page: done here when the bridge is
	// already bound, otherwise by bind().
	_ = doc.SetPrototype(b.protoDocument)
	_ = vm.Set("document", doc)
	if b.doc != nil {
		b.bindInstalled()
	}
	_ = win.Set("document", doc)

	b.installGlobals(win)
	b.installEventHandlers()
	b.installReflection()
	b.installDialog()
	b.installNodeFilter()
	b.installMutationObserver()
	b.installCustomElements(win)
	b.installFormInternals()
	b.installParsers()
	b.installAsync()
	b.installDynamicImport()
	b.installSubtle()
	b.installURL()
	b.installEncoding()
	b.installIsolationGlobals()
	if b.extHost != nil {
		b.installExtensionAPI(win)
	}
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
			b.sealTemplates(docNode)
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
			b.queueUnhandledRejection(p)
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
	// The viewport scrolls the root element (standards mode).
	b.defineGetter(d, "scrollingElement", func() goja.Value { return b.wrap(findTag(b.doc, "html")) })
	b.defineGetter(d, "readyState", func() goja.Value { return vm.ToValue(b.docReadyState()) })
	b.defineGetter(d, "hidden", func() goja.Value { return vm.ToValue(false) })
	b.defineGetter(d, "visibilityState", func() goja.Value { return vm.ToValue("visible") })
	// The one document is always the focused, visible one (visibilityState above),
	// so hasFocus is true. Routers and data libraries gate refetch/prefetch on it.
	_ = d.Set("hasFocus", func(goja.FunctionCall) goja.Value { return vm.ToValue(true) })
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
	// document.domain reads the document's host. Assigning it is the deprecated
	// same-site origin relaxation: the write is accepted and ignored, so no
	// origin is ever relaxed (ADR 0015 — there is no second document to relax
	// towards). The getter matters on its own: cookie helpers derive the cookie
	// Domain from it and throw when it is missing.
	b.defineProp(d, "domain",
		func() goja.Value {
			if b.currentURL != nil {
				return vm.ToValue(b.currentURL.Hostname())
			}
			return vm.ToValue("")
		},
		func(goja.Value) {})
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
		return b.wrap(b.pageQuery(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(b.pageQueryAll(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("getElementsByTagName", func(call goja.FunctionCall) goja.Value {
		return b.htmlCollection(queryAll(b.doc, call.Argument(0).String()))
	})
	_ = d.Set("getElementsByClassName", func(call goja.FunctionCall) goja.Value {
		return b.htmlCollection(queryAll(b.doc, "."+call.Argument(0).String()))
	})
	b.defineGetter(d, "forms", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "form")) })
	// The other document-wide element collections. scripts is how a loader finds
	// its own tag (and its src-relative base) when document.currentScript is null.
	b.defineGetter(d, "scripts", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "script")) })
	b.defineGetter(d, "images", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "img")) })
	b.defineGetter(d, "links", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "a[href], area[href]")) })
	b.defineGetter(d, "embeds", func() goja.Value { return b.htmlCollection(queryAll(b.doc, "embed")) })
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
		return b.wrap(b.cloneTree(src, call.Argument(1).ToBoolean()))
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
	_ = d.Set("createNodeIterator", func(call goja.FunctionCall) goja.Value {
		root := b.unwrap(call.Argument(0))
		if root == nil {
			root = b.doc
		}
		whatToShow := int64(0)
		if w := call.Argument(1); w != nil && !goja.IsUndefined(w) {
			whatToShow = w.ToInteger()
		}
		return b.createNodeIterator(root, whatToShow, call.Argument(2))
	})
	_ = d.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		b.addListener(b.docListeners, call, true)
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
	_ = impl.Set("createDocumentType", func(call goja.FunctionCall) goja.Value {
		dt := &html.Node{Type: html.DoctypeNode, Data: call.Argument(0).String()}
		if v := call.Argument(1).String(); v != "" && !goja.IsUndefined(call.Argument(1)) {
			dt.Attr = append(dt.Attr, html.Attribute{Key: "public", Val: v})
		}
		if v := call.Argument(2).String(); v != "" && !goja.IsUndefined(call.Argument(2)) {
			dt.Attr = append(dt.Attr, html.Attribute{Key: "system", Val: v})
		}
		return b.wrap(dt)
	})
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
	_ = d.SetPrototype(b.protoDocument) // so `doc instanceof Document/Node` holds; own methods below shadow the node-backed ones
	// Register the facade as docNode's wrapper: the node-backed Node/ParentNode
	// members on Document.prototype (childNodes, firstChild, children, append,
	// getElementsByTagName, contains…) then work on it, and html.parentNode is
	// this same object.
	b.cache[docNode] = d
	b.objNode[d] = docNode
	b.defineGetter(d, "URL", func() goja.Value { return vm.ToValue("about:blank") })
	b.defineGetter(d, "documentURI", func() goja.Value { return vm.ToValue("about:blank") })
	b.defineGetter(d, "characterSet", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "charset", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "inputEncoding", func() goja.Value { return vm.ToValue("UTF-8") })
	b.defineGetter(d, "contentType", func() goja.Value { return vm.ToValue("text/html") })
	b.defineGetter(d, "compatMode", func() goja.Value { return vm.ToValue("CSS1Compat") })
	b.defineGetter(d, "location", func() goja.Value { return goja.Null() })
	b.defineGetter(d, "readyState", func() goja.Value { return vm.ToValue("complete") }) // parsed in one step, never loading
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
		return b.wrap(b.pageQuery(docNode, call.Argument(0).String()))
	})
	_ = d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.nodeList(b.pageQueryAll(docNode, call.Argument(0).String()))
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
	o := b.vm.NewArray(arr...) // a real array, so page code can hang properties on it
	if b.protoNodeList != nil {
		_ = o.SetPrototype(b.protoNodeList)
	}
	return o
}

// classListFor returns the cached DOMTokenList for n.classList.
func (b *bridge) classListFor(n *html.Node) *goja.Object {
	return b.tokenListFor(n, "class", nil)
}

// relSupported are the link-relation keywords this engine acts on, which is
// what relList.supports() answers: the preload family is fetched ahead of use
// (startModulePreload). Nothing else — no stylesheet is loaded, no prefetch or
// preconnect is made — so those report false and feature-detecting code (Vite's
// and Qwik's preload helpers) takes the branch that matches what will happen.
var relSupported = map[string]bool{"modulepreload": true, "preload": true}

// tokenListFor returns the cached, live DOMTokenList reflecting n's `attr`
// attribute (classList, relList, …): tokens are read from the attribute on every
// access. supported is the set supports() answers from; nil means the attribute
// defines no supported tokens, where supports() throws a TypeError as specified.
func (b *bridge) tokenListFor(n *html.Node, attr string, supported map[string]bool) *goja.Object {
	key := tokenListKey{n: n, attr: attr}
	if o, ok := b.tokenListCache[key]; ok {
		return o
	}
	vm := b.vm
	cl := vm.NewObject()
	// Brand it so Object.prototype.toString.call(classList) is "[object DOMTokenList]".
	_ = cl.SetSymbol(goja.SymToStringTag, vm.ToValue("DOMTokenList"))
	// The token set: ASCII-whitespace separated, duplicates dropped, order kept.
	tokens := func() []string {
		fields := strings.Fields(getAttr(n, attr))
		out := fields[:0]
		seen := make(map[string]bool, len(fields))
		for _, f := range fields {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
		return out
	}
	write := func(ts []string) { b.setAttrMut(n, attr, strings.Join(ts, " ")) }
	has := func(c string) bool {
		for _, t := range tokens() {
			if t == c {
				return true
			}
		}
		return false
	}
	// check validates a token argument: empty is a SyntaxError, white space an
	// InvalidCharacterError.
	check := func(c string) {
		if c == "" {
			b.throwDOMException("SyntaxError", "The token provided must not be empty.")
		}
		if strings.ContainsAny(c, " \t\n\r\f") {
			b.throwDOMException("InvalidCharacterError", "The token provided ('"+c+"') contains HTML space characters, which are not valid in tokens.")
		}
	}
	_ = cl.Set("contains", func(call goja.FunctionCall) goja.Value { return vm.ToValue(has(call.Argument(0).String())) })
	_ = cl.Set("item", func(call goja.FunctionCall) goja.Value {
		ts := tokens()
		if i := call.Argument(0).ToInteger(); i >= 0 && i < int64(len(ts)) {
			return vm.ToValue(ts[i])
		}
		return goja.Null()
	})
	_ = cl.Set("add", func(call goja.FunctionCall) goja.Value {
		ts := tokens()
		for _, a := range call.Arguments {
			check(a.String())
		}
		for _, a := range call.Arguments {
			c := a.String()
			dup := false
			for _, t := range ts {
				dup = dup || t == c
			}
			if !dup {
				ts = append(ts, c)
			}
		}
		write(ts)
		return goja.Undefined()
	})
	_ = cl.Set("remove", func(call goja.FunctionCall) goja.Value {
		drop := map[string]bool{}
		for _, a := range call.Arguments {
			check(a.String())
			drop[a.String()] = true
		}
		var ts []string
		for _, t := range tokens() {
			if !drop[t] {
				ts = append(ts, t)
			}
		}
		if hasAttr(n, attr) || len(ts) > 0 {
			write(ts)
		}
		return goja.Undefined()
	})
	_ = cl.Set("toggle", func(call goja.FunctionCall) goja.Value {
		c := call.Argument(0).String()
		check(c)
		forced := len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1))
		force := forced && call.Argument(1).ToBoolean()
		if has(c) {
			if forced && force {
				return vm.ToValue(true)
			}
			var ts []string
			for _, t := range tokens() {
				if t != c {
					ts = append(ts, t)
				}
			}
			write(ts)
			return vm.ToValue(false)
		}
		if forced && !force {
			return vm.ToValue(false)
		}
		write(append(tokens(), c))
		return vm.ToValue(true)
	})
	_ = cl.Set("replace", func(call goja.FunctionCall) goja.Value {
		oldTok, newTok := call.Argument(0).String(), call.Argument(1).String()
		check(oldTok)
		check(newTok)
		ts := tokens()
		found := false
		var out []string
		for _, t := range ts {
			switch {
			case t == oldTok && !found:
				found = true
				if !has(newTok) || newTok == oldTok {
					out = append(out, newTok)
				}
			case t == newTok && found:
				// already emitted in oldTok's place
			default:
				out = append(out, t)
			}
		}
		if !found {
			return vm.ToValue(false)
		}
		write(out)
		return vm.ToValue(true)
	})
	_ = cl.Set("supports", func(call goja.FunctionCall) goja.Value {
		if supported == nil {
			panic(vm.NewTypeError("DOMTokenList has no supported tokens."))
		}
		return vm.ToValue(supported[strings.ToLower(call.Argument(0).String())])
	})
	_ = cl.Set("toString", func(goja.FunctionCall) goja.Value { return vm.ToValue(getAttr(n, attr)) })
	_ = cl.Set("forEach", func(call goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			panic(vm.NewTypeError("forEach callback is not a function"))
		}
		for i, t := range tokens() {
			if _, err := fn(call.Argument(1), vm.ToValue(t), vm.ToValue(i), call.This); err != nil {
				panic(err)
			}
		}
		return goja.Undefined()
	})
	if it := b.indexedIter(); it != nil {
		_ = cl.SetSymbol(goja.SymIterator, it)
		_ = cl.Set("values", it)
	}
	handler := &goja.ProxyTrapConfig{
		GetIdx: func(_ *goja.Object, idx int, _ goja.Value) goja.Value {
			if ts := tokens(); idx >= 0 && idx < len(ts) {
				return vm.ToValue(ts[idx])
			}
			return goja.Undefined()
		},
		Get: func(t *goja.Object, key string, _ goja.Value) goja.Value {
			switch key {
			case "length":
				return vm.ToValue(len(tokens()))
			case "value":
				return vm.ToValue(getAttr(n, attr))
			}
			return t.Get(key)
		},
		Set: func(t *goja.Object, key string, value goja.Value, _ goja.Value) bool {
			if key == "value" {
				b.setAttrMut(n, attr, value.String())
				return true
			}
			return t.Set(key, value) == nil
		},
		HasIdx: func(_ *goja.Object, idx int) bool { return idx >= 0 && idx < len(tokens()) },
		Has: func(t *goja.Object, key string) bool {
			if key == "length" || key == "value" {
				return true
			}
			v := t.Get(key)
			return v != nil && !goja.IsUndefined(v)
		},
	}
	o := vm.ToValue(vm.NewProxy(cl, handler)).ToObject(vm)
	b.tokenListCache[key] = o
	return o
}

// tokenListKey identifies one reflected DOMTokenList.
type tokenListKey struct {
	n    *html.Node
	attr string
}

// indexedIter returns the shared @@iterator for index/length collections
// (NamedNodeMap, DOMTokenList): it walks this[0..length).
func (b *bridge) indexedIter() goja.Value {
	if b.indexedIterFn == nil {
		if v, err := b.vm.RunString(`(function(){var i=0,s=this;var it={next:function(){return i<s.length?{value:s[i++],done:false}:{value:undefined,done:true};}};it[Symbol.iterator]=function(){return it;};return it;})`); err == nil {
			b.indexedIterFn = v
		}
	}
	return b.indexedIterFn
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
	return htmlVoidStyle(buf.String())
}

func outerHTML(n *html.Node) string {
	var buf bytes.Buffer
	_ = html.Render(&buf, n)
	return htmlVoidStyle(buf.String())
}

// rawTextTags are the elements whose text children x/net/html writes verbatim.
var rawTextTags = map[string]bool{
	"iframe": true, "noembed": true, "noframes": true, "noscript": true,
	"plaintext": true, "script": true, "style": true, "xmp": true,
}

// htmlVoidStyle rewrites x/net/html's XML-style void elements (<br/>, <img …/>)
// to the HTML serialization a browser's innerHTML/outerHTML produces (<br>,
// <img …>). Page code compares and re-parses these strings, and "<br/>" is not
// what the fragment serialization algorithm emits.
//
// The renderer escapes < and > in text and attribute values, so outside
// comments and raw-text elements every "<" opens a tag and the next ">" closes
// it; those two regions are copied through untouched.
func htmlVoidStyle(s string) string {
	if !strings.Contains(s, "/>") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			sb.WriteString(s[i:])
			break
		}
		lt += i
		sb.WriteString(s[i:lt])
		if strings.HasPrefix(s[lt:], "<!--") {
			end := strings.Index(s[lt+4:], "-->")
			if end < 0 {
				sb.WriteString(s[lt:])
				break
			}
			end += lt + 4 + 3
			sb.WriteString(s[lt:end])
			i = end
			continue
		}
		gt := strings.IndexByte(s[lt:], '>')
		if gt < 0 {
			sb.WriteString(s[lt:])
			break
		}
		gt += lt
		tag := s[lt : gt+1]
		i = gt + 1
		if strings.HasSuffix(tag, "/>") {
			sb.WriteString(tag[:len(tag)-2])
			sb.WriteByte('>')
			continue
		}
		sb.WriteString(tag)
		if len(tag) > 2 && tag[1] != '/' {
			name := tag[1 : len(tag)-1]
			if sp := strings.IndexAny(name, " \t\n"); sp >= 0 {
				name = name[:sp]
			}
			if rawTextTags[name] {
				end := strings.Index(s[i:], "</"+name+">")
				if end < 0 {
					sb.WriteString(s[i:])
					break
				}
				sb.WriteString(s[i : i+end])
				i += end
			}
		}
	}
	return sb.String()
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
