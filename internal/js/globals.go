package js

import (
	"fmt"
	"net/url"

	"github.com/dop251/goja"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 unblink"

// installGlobals adds navigator, location, history, and window-level no-ops. The
// remaining browser stubs (storage, observers, rAF, fetch/XHR) are installed by
// preludeJS, which is simpler to express in JS.
func (b *bridge) installGlobals(win *goja.Object) {
	vm := b.vm

	// console.error sink: frameworks catch fatal boot errors and console.error them
	// (Angular CLI's main.ts, React error boundaries), which a no-op console would
	// swallow. The prelude consumes and deletes this hook, routing console.error
	// into the render diagnostics next to uncaught exceptions.
	_ = vm.Set("__unblinkConsoleError", func(msg string) {
		b.recordError(fmt.Errorf("console.error: %s", msg))
	})

	// console.* capture sink for the console tool: the prelude formats each call's
	// arguments and reports (level, text) here. Consumed and deleted by the prelude.
	_ = vm.Set("__unblinkConsole", func(level, msg string) {
		b.recordConsole(level, msg)
	})

	// The prelude's timer wrapper registers its live-timer audit here (consumed
	// and deleted like __unblinkConsoleError); settlePoll reads it on close.
	_ = vm.Set("__unblinkRegisterTimerAudit", func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
			b.timerAudit = fn
		}
		return goja.Undefined()
	})

	nav := vm.NewObject()
	_ = nav.Set("userAgent", userAgent)
	_ = nav.Set("language", "en-US")
	_ = nav.Set("languages", []string{"en-US", "en"})
	_ = nav.Set("platform", "Linux x86_64")
	_ = nav.Set("cookieEnabled", b.cookies != nil)
	_ = nav.Set("onLine", true)
	_ = nav.Set("hardwareConcurrency", 4)
	_ = nav.Set("maxTouchPoints", 0)
	_ = nav.Set("vendor", "")
	_ = nav.Set("doNotTrack", goja.Null())
	// webdriver is true by default: unblink is automation and says so (the UA
	// string already carries "unblink"). Under --tls-mimic the caller opts into
	// fingerprint parity and the browser flips this to false via WithWebdriver.
	_ = nav.Set("webdriver", b.webdriver)
	// sendBeacon: accepted-and-dropped. Analytics beacons return true so queued
	// flush loops don't retry forever; nothing is transmitted.
	_ = nav.Set("sendBeacon", func(goja.FunctionCall) goja.Value { return vm.ToValue(true) })
	uaData := vm.NewObject()
	_ = uaData.Set("brands", []map[string]interface{}{
		{"brand": "Chromium", "version": "124"},
		{"brand": "unblink", "version": "1"},
	})
	_ = uaData.Set("mobile", false)
	_ = uaData.Set("platform", "Linux")
	_ = uaData.Set("getHighEntropyValues", func(goja.FunctionCall) goja.Value {
		promise, resolve, _ := vm.NewPromise()
		_ = resolve(uaData)
		return vm.ToValue(promise)
	})
	_ = nav.Set("userAgentData", uaData)
	_ = vm.Set("navigator", nav)
	_ = win.Set("navigator", nav)

	loc := vm.NewObject()
	b.locationObj = loc
	b.currentURL = b.base
	b.updateLocation(b.base)
	// href is an accessor (not a data property) so `location.href = "..."` routes
	// through navigate() and is recorded as a pending cross-document navigation, the
	// same as assign/replace. The getter mirrors the current location string.
	b.defineProp(loc, "href",
		func() goja.Value {
			if b.currentURL == nil {
				return vm.ToValue("")
			}
			return vm.ToValue(b.currentURL.String())
		},
		func(v goja.Value) { b.navigate(v.String()) })
	_ = loc.Set("assign", func(call goja.FunctionCall) goja.Value {
		b.navigate(call.Argument(0).String())
		return goja.Undefined()
	})
	_ = loc.Set("replace", func(call goja.FunctionCall) goja.Value {
		b.navigate(call.Argument(0).String())
		return goja.Undefined()
	})
	_ = loc.Set("reload", noop)
	_ = vm.Set("location", loc)
	_ = win.Set("location", loc)

	if b.base != nil {
		b.historyStack = []historyEntry{{url: b.base}}
	}
	hist := vm.NewObject()
	_ = hist.Set("pushState", func(call goja.FunctionCall) goja.Value {
		b.historyPush(call.Argument(0), call.Argument(2), false)
		return goja.Undefined()
	})
	_ = hist.Set("replaceState", func(call goja.FunctionCall) goja.Value {
		b.historyPush(call.Argument(0), call.Argument(2), true)
		return goja.Undefined()
	})
	_ = hist.Set("back", func(goja.FunctionCall) goja.Value { b.historyGo(-1); return goja.Undefined() })
	_ = hist.Set("forward", func(goja.FunctionCall) goja.Value { b.historyGo(1); return goja.Undefined() })
	_ = hist.Set("go", func(call goja.FunctionCall) goja.Value {
		b.historyGo(int(call.Argument(0).ToInteger()))
		return goja.Undefined()
	})
	b.historyObj = hist
	_ = hist.Set("length", 1)
	_ = vm.Set("history", hist)
	_ = win.Set("history", hist)

	_ = win.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		b.addListener(b.winListeners, call)
		return goja.Undefined()
	})
	_ = win.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		b.removeListener(b.winListeners, call)
		return goja.Undefined()
	})
	_ = win.Set("dispatchEvent", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(b.dispatchUserEvent(call.Argument(0), win, b.dispatchOnWindow))
	})
	_ = win.Set("scrollTo", noop)
	_ = win.Set("scroll", noop)
	_ = win.Set("scrollBy", noop)
	// getComputedStyle is installed by the prelude (empty-string CSSStyleDeclaration).
}

// historyEntry is one client-side navigation state (History API).
type historyEntry struct {
	state goja.Value
	url   *url.URL
}

// updateLocation rewrites the location object's fields to reflect u. Client-side
// routers read location.pathname/search after pushState to pick the active view.
func (b *bridge) updateLocation(u *url.URL) {
	if b.locationObj == nil || u == nil {
		return
	}
	loc := b.locationObj
	// href is an accessor backed by currentURL (see installGlobals); do not Set it
	// here or the setter would recurse back into navigate.
	_ = loc.Set("protocol", u.Scheme+":")
	_ = loc.Set("host", u.Host)
	_ = loc.Set("hostname", u.Hostname())
	_ = loc.Set("port", u.Port())
	_ = loc.Set("pathname", u.Path)
	_ = loc.Set("search", rawQuery(u.RawQuery))
	_ = loc.Set("hash", rawFragment(u.Fragment))
	_ = loc.Set("origin", u.Scheme+"://"+u.Host)
	b.currentURL = u
}

// resolveNav resolves a (possibly relative) URL string against the document base
// (<base href> when present, else the current location). Returns the current URL
// unchanged on empty input or a parse failure.
func (b *bridge) resolveNav(raw string) *url.URL {
	if raw == "" || b.currentURL == nil {
		return b.currentURL
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return b.currentURL
	}
	return b.docBaseNow().ResolveReference(ref)
}

// maxHistoryStack bounds the JS history stack: a router that pushState-loops
// must not grow it for the life of a live session (real browsers cap history too).
const maxHistoryStack = 100

// historyPush implements history.pushState/replaceState: it updates location and
// records the entry. There is no popstate (pushState/replaceState never fire it).
func (b *bridge) historyPush(state, urlArg goja.Value, replace bool) {
	u := b.currentURL
	if urlArg != nil && !goja.IsUndefined(urlArg) && !goja.IsNull(urlArg) {
		u = b.resolveNav(urlArg.String())
	}
	b.updateLocation(u)
	entry := historyEntry{state: state, url: u}
	if replace && len(b.historyStack) > 0 {
		b.historyStack[b.historyPos] = entry
	} else {
		b.historyStack = append(b.historyStack[:b.historyPos+1], entry)
		if len(b.historyStack) > maxHistoryStack {
			b.historyStack = append([]historyEntry(nil), b.historyStack[len(b.historyStack)-maxHistoryStack:]...)
		}
		b.historyPos = len(b.historyStack) - 1
	}
	b.syncHistoryLength()
}

// historyGo moves the history pointer by delta, updates location, and fires a
// popstate event (with the target entry's state) on window — the signal SPA
// routers listen for to re-render.
func (b *bridge) historyGo(delta int) {
	pos := b.historyPos + delta
	if pos < 0 || pos >= len(b.historyStack) {
		return
	}
	b.historyPos = pos
	entry := b.historyStack[pos]
	b.updateLocation(entry.url)

	ev := b.newEvent("popstate", false, false, b.windowObj)
	if entry.state != nil {
		_ = ev.js.Set("state", entry.state)
	} else {
		_ = ev.js.Set("state", goja.Null())
	}
	b.dispatchOnWindow(ev)
	if h := b.windowObj.Get("onpopstate"); h != nil {
		if fn, ok := goja.AssertFunction(h); ok {
			b.callSafe(fn, b.windowObj, ev.js)
		}
	}
}

// navigate handles location.href/assign/replace. A render never performs the real
// cross-document fetch, but when the target is a different document it records the
// requested URL in pendingNav so the caller can follow it (surfaced on the render /
// interact result). A fragment-only change stays on the same document and fires
// hashchange — the signal hash-based routers listen for.
func (b *bridge) navigate(raw string) {
	u := b.resolveNav(raw)
	if !sameDocument(b.currentURL, u) {
		b.pendingNav = u
		b.updateLocation(u)
		return
	}
	old := b.currentURL
	b.updateLocation(u)
	if old != nil && u != nil && old.Fragment != u.Fragment {
		ev := b.newEvent("hashchange", false, false, b.windowObj)
		_ = ev.js.Set("oldURL", old.String())
		_ = ev.js.Set("newURL", u.String())
		b.dispatchOnWindow(ev)
		if h := b.windowObj.Get("onhashchange"); h != nil {
			if fn, ok := goja.AssertFunction(h); ok {
				b.callSafe(fn, b.windowObj, ev.js)
			}
		}
	}
}

// sameDocument reports whether a and c differ only in their fragment (a hash change
// stays on the same document and is not a navigation).
func sameDocument(a, c *url.URL) bool {
	if a == nil || c == nil {
		return false
	}
	ax, cx := *a, *c
	ax.Fragment, cx.Fragment = "", ""
	return ax.String() == cx.String()
}

func (b *bridge) syncHistoryLength() {
	if b.historyObj != nil {
		_ = b.historyObj.Set("length", len(b.historyStack))
	}
}

func rawQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

func rawFragment(f string) string {
	if f == "" {
		return ""
	}
	return "#" + f
}

// preludeJS installs the benign stubs that are simplest in JS. It runs after the
// Go-side globals and before page scripts. setTimeout/Promise come from the
// event loop / goja; this just fills the gaps so registration-time code doesn't
// throw ReferenceError.
const preludeJS = `
(function () {
  // Browsers hand out numeric timer ids; goja's event loop returns a host Timer
  // object. zone.js stamps __zone_symbol__zoneTask onto non-numeric handles
  // (host objects reject expando properties) and other code compares ids with
  // ===, so wrap the natives to return plain numbers backed by a handle table.
  // (The Go-side keepalive captured the natives before this prelude runs.)
  (function () {
    var nativeSet = window.setTimeout, nativeClear = window.clearTimeout;
    var nativeSetI = window.setInterval, nativeClearI = window.clearInterval;
    var seq = 1, live = {};
    // A one-shot timer whose delay would fire it after the render budget never
    // runs before the snapshot, so content behind it (deferred hydration, splash
    // timeouts, polling first ticks) silently vanishes. __unblinkTimerDeadlineMs
    // (set by the one-shot engine, absent for live sessions) is the wall-clock
    // instant timers must fire by; a delay past it is pulled in to just inside
    // the budget. Only timers that otherwise never fire change behavior. Intervals
    // are never clamped (they'd busy-loop against the deadline).
    var deadline = (typeof window.__unblinkTimerDeadlineMs === 'number') ? window.__unblinkTimerDeadlineMs : 0;
    // clamped tracks one-shot timers whose delay was pulled in because it
    // exceeded the budget. The settle poll holds open while any remain unfired,
    // so a clamped timer's deferred content lands before the snapshot (without
    // it the poll would declare the page settled ~60ms in, long before the timer
    // fires). Cleared when the timer runs or is cleared.
    var clamped = {};
    function toFn(fn) { return typeof fn === 'function' ? fn : new Function(String(fn)); }
    window.setTimeout = function (fn, ms) {
      var args = Array.prototype.slice.call(arguments, 2), cb = toFn(fn), id = seq++;
      ms = +ms || 0;
      var wasClamped = false;
      if (deadline > 0) {
        var remaining = deadline - Date.now();
        if (remaining < 0) remaining = 0;
        if (ms > remaining) { ms = remaining; wasClamped = true; clamped[id] = true; }
      }
      live[id] = nativeSet(function () { delete live[id]; delete clamped[id]; cb.apply(undefined, args); }, ms);
      return id;
    };
    window.clearTimeout = function (id) {
      var h = live[id]; if (h !== undefined) { delete live[id]; delete clamped[id]; nativeClear(h); }
    };
    if (nativeSetI) {
      window.setInterval = function (fn, ms) {
        var args = Array.prototype.slice.call(arguments, 2), cb = toFn(fn), id = seq++;
        live[id] = nativeSetI(function () { cb.apply(undefined, args); }, ms);
        return id;
      };
      window.clearInterval = function (id) {
        var h = live[id]; if (h !== undefined) { delete live[id]; nativeClearI(h); }
      };
    }
    // goja's event loop exposes a native setImmediate (a 0-delay macrotask
    // here). Route it through the wrapped setTimeout so immediates land in the
    // live table: the settle audit must see every scheduled macrotask, and
    // React's scheduler prefers setImmediate over MessageChannel when it
    // exists — an unwrapped one would hide work-loop continuations from the
    // provable-idle check.
    window.setImmediate = function (fn) {
      var args = Array.prototype.slice.call(arguments, 1);
      return window.setTimeout.apply(undefined, [fn, 0].concat(args));
    };
    window.clearImmediate = window.clearTimeout;
    // Audit hook: {c: clamped one-shot timers still unfired, t: all live wrapped
    // timers (one-shots, intervals, immediates)}. The settle poll reads this
    // every tick: non-zero c holds the poll open (deferred content pending) and
    // is reported at close as timers_pending; t==0 combined with zero in-flight
    // network is the provable-idle signal — no mechanism left to run more JS —
    // that lets the settle close without waiting out the quiet window.
    if (typeof window.__unblinkRegisterTimerAudit === 'function') {
      window.__unblinkRegisterTimerAudit(function () {
        var c = 0, t = 0, k;
        for (k in clamped) c++;
        for (k in live) t++;
        return { c: c, t: t };
      });
      delete window.__unblinkRegisterTimerAudit;
    }
  })();
  // The engine's built-in console is disabled (its printer writes to stdout, which
  // is reserved for MCP). Real browsers always expose console, so provide a no-op
  // one here: page console.* calls stay silent instead of throwing ReferenceError
  // or leaking untrusted page text into the host logs.
  var noop = function () {};
  // console.error feeds the render diagnostics (see __unblinkConsoleError); every
  // level is also captured into a buffer for the console tool (see __unblinkConsole).
  // Neither writes to the host's stdout/stderr, which stay reserved for MCP.
  var reportError = window.__unblinkConsoleError || noop;
  delete window.__unblinkConsoleError;
  var capture = window.__unblinkConsole || noop;
  delete window.__unblinkConsole;
  var fmtArgs = function (args) {
    var parts = [];
    for (var i = 0; i < args.length; i++) {
      var a = args[i];
      try {
        if (a && a.stack) parts.push(String(a.stack));
        else if (a !== null && typeof a === 'object') parts.push(JSON.stringify(a));
        else parts.push(String(a));
      } catch (e) { parts.push(String(a)); }
    }
    return parts.join(' ');
  };
  var mk = function (level) {
    return function () {
      try {
        var text = fmtArgs(arguments);
        capture(level, text);
        if (level === 'error') reportError(text);
      } catch (e) { /* diagnostics must never throw into page code */ }
    };
  };
  window.console = { log: mk('log'), info: mk('info'), warn: mk('warn'), error: mk('error'),
    debug: mk('debug'), trace: mk('trace'), dir: mk('log'), table: mk('log'),
    assert: noop, group: noop, groupCollapsed: noop, groupEnd: noop,
    count: noop, time: noop, timeEnd: noop };

  function makeStorage() {
    var m = Object.create(null);
    return {
      getItem: function (k) { return k in m ? m[k] : null; },
      setItem: function (k, v) { m[k] = String(v); },
      removeItem: function (k) { delete m[k]; },
      clear: function () { m = Object.create(null); },
      key: function (i) { return Object.keys(m)[i] || null; },
      get length() { return Object.keys(m).length; }
    };
  }
  // Go-installed persistent stores (session-scoped: a session is a tab, and both
  // areas survive navigations within it) win; the in-memory fallbacks cover
  // one-shot stateless renders.
  if (!window.localStorage) window.localStorage = makeStorage();
  if (!window.sessionStorage) window.sessionStorage = makeStorage();

  // structuredClone: a real recursive clone for the object graphs apps actually
  // clone (JSON-ish + Date/RegExp/Map/Set/typed arrays, cycles included).
  // Mainstream bundles call it without feature detection; absence threw at
  // hydration. Functions/symbols throw, matching the spec's DataCloneError.
  if (typeof window.structuredClone !== 'function') {
    window.structuredClone = function (value) {
      var seen = new Map();
      function clone(v) {
        if (v === null || typeof v !== 'object') {
          if (typeof v === 'function' || typeof v === 'symbol') {
            throw new Error('DataCloneError: ' + typeof v + ' could not be cloned');
          }
          return v;
        }
        if (seen.has(v)) return seen.get(v);
        if (v instanceof Date) return new Date(v.getTime());
        if (v instanceof RegExp) return new RegExp(v.source, v.flags);
        if (v instanceof Map) { var m = new Map(); seen.set(v, m); v.forEach(function (val, k) { m.set(clone(k), clone(val)); }); return m; }
        if (v instanceof Set) { var st = new Set(); seen.set(v, st); v.forEach(function (val) { st.add(clone(val)); }); return st; }
        if (Array.isArray(v)) { var a = []; seen.set(v, a); for (var i = 0; i < v.length; i++) a[i] = clone(v[i]); return a; }
        if (typeof ArrayBuffer === 'function') {
          if (v instanceof ArrayBuffer) return v.slice(0);
          if (ArrayBuffer.isView(v)) return new v.constructor(v);
        }
        var o = {}; seen.set(v, o);
        for (var k in v) if (Object.prototype.hasOwnProperty.call(v, k)) o[k] = clone(v[k]);
        return o;
      }
      return clone(value);
    };
  }

  // WebSocket: connection-less stub (real sockets are a permanent non-goal).
  // Constructing one no longer throws; it reports failure through the standard
  // error -> close(1006) event sequence so reconnect/offline logic degrades
  // gracefully instead of crashing hydration.
  if (typeof window.WebSocket === 'undefined') {
    var WS = function (url) {
      var self = this;
      this.url = String(url || '');
      this.readyState = WS.CONNECTING;
      this.bufferedAmount = 0; this.protocol = ''; this.extensions = '';
      this.binaryType = 'blob';
      this.onopen = null; this.onmessage = null; this.onerror = null; this.onclose = null;
      this.__l = {};
      setTimeout(function () {
        self.readyState = WS.CLOSED;
        var err = { type: 'error', target: self };
        var close = { type: 'close', target: self, code: 1006, reason: 'WebSocket is not supported in this environment', wasClean: false };
        if (typeof self.onerror === 'function') { try { self.onerror(err); } catch (e) {} }
        (self.__l.error || []).slice().forEach(function (f) { try { f.call(self, err); } catch (e) {} });
        if (typeof self.onclose === 'function') { try { self.onclose(close); } catch (e) {} }
        (self.__l.close || []).slice().forEach(function (f) { try { f.call(self, close); } catch (e) {} });
      }, 0);
    };
    WS.CONNECTING = 0; WS.OPEN = 1; WS.CLOSING = 2; WS.CLOSED = 3;
    WS.prototype.send = function () {};
    WS.prototype.close = function () { this.readyState = WS.CLOSED; };
    WS.prototype.addEventListener = function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); };
    WS.prototype.removeEventListener = function (t, f) { var a = this.__l[t]; if (a) { var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); } };
    WS.prototype.dispatchEvent = function () { return true; };
    window.WebSocket = WS;
  }

  // Image: HTMLImageElement constructor stub. Construction and src assignment never
  // throw; nothing is fetched or rendered (no load/error fires). Enough for code that
  // does new Image() for a beacon/probe without blocking on it.
  if (typeof window.Image === 'undefined') {
    var Img = function (w, h) {
      this.width = w || 0; this.height = h || 0;
      this.naturalWidth = 0; this.naturalHeight = 0; this.complete = false;
      this.src = ''; this.srcset = ''; this.alt = ''; this.crossOrigin = null;
      this.onload = null; this.onerror = null;
      this.addEventListener = function () {}; this.removeEventListener = function () {};
      this.setAttribute = function () {}; this.getAttribute = function () { return null; };
    };
    window.Image = Img;
  }

  // Worker/SharedWorker: inert stubs — construction succeeds, messages go
  // nowhere. Apps that offload work keep running on their main-thread fallback
  // path (or simply never receive results) instead of throwing at load.
  if (typeof window.Worker === 'undefined') {
    var Wk = function () { this.onmessage = null; this.onmessageerror = null; this.onerror = null; };
    Wk.prototype.postMessage = noop; Wk.prototype.terminate = noop;
    Wk.prototype.addEventListener = noop; Wk.prototype.removeEventListener = noop;
    Wk.prototype.dispatchEvent = function () { return true; };
    window.Worker = Wk;
    window.SharedWorker = function () {
      this.port = { postMessage: noop, start: noop, close: noop, addEventListener: noop, removeEventListener: noop, onmessage: null };
    };
  }

  window.requestAnimationFrame = function (cb) {
    return setTimeout(function () { cb(typeof Date.now === 'function' ? Date.now() : 0); }, 0);
  };
  window.cancelAnimationFrame = function (id) { clearTimeout(id); };

  // Real microtask scheduling: goja drains the promise-job queue between macrotasks.
  window.queueMicrotask = function (cb) { Promise.resolve().then(cb); };
  window.requestIdleCallback = function (cb) {
    return setTimeout(function () { cb({ didTimeout: false, timeRemaining: function () { return 50; } }); }, 1);
  };
  window.cancelIdleCallback = function (id) { clearTimeout(id); };

  // matchMedia (a real evaluator against the constant viewport) and the
  // window.innerWidth/screen/devicePixelRatio constants it reads are installed
  // by preludeAPIJS (prelude_api.go).

  // A frozen zero-rect (no layout engine); shared by the geometry observers below.
  function zeroRect() {
    return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: function () { return this; } };
  }

  // getComputedStyle returns empty-string values for every property, so guards
  // like getComputedStyle(el).display !== 'none' treat content as visible.
  window.getComputedStyle = function () {
    var base = { getPropertyValue: function () { return ''; }, getPropertyPriority: function () { return ''; }, setProperty: function () {}, removeProperty: function () { return ''; }, length: 0, item: function () { return ''; } };
    if (typeof Proxy === 'function') {
      return new Proxy(base, { get: function (t, k) { return (k in t) ? t[k] : ''; } });
    }
    return base;
  };

  // MutationObserver is installed Go-side (see installMutationObserver) so it can
  // observe real tree mutations; do not stub it here.
  //
  // IntersectionObserver/ResizeObserver have no layout, so they report a single
  // synthetic "visible / zero-size" entry on a microtask. This makes lazy-loaded,
  // infinite-scroll, and visibility-gated content render instead of staying blank.
  window.IntersectionObserver = function (cb) {
    var self = this; this._t = [];
    this.observe = function (t) { self._t.push(t); queueMicrotask(function () {
      cb([{ isIntersecting: true, intersectionRatio: 1, target: t, boundingClientRect: zeroRect(), intersectionRect: zeroRect(), rootBounds: zeroRect(), time: 0 }], self);
    }); };
    this.unobserve = function () {}; this.disconnect = function () {}; this.takeRecords = function () { return []; };
  };
  window.ResizeObserver = function (cb) {
    var self = this;
    this.observe = function (t) { queueMicrotask(function () {
      cb([{ target: t, contentRect: zeroRect(), borderBoxSize: [{ inlineSize: 0, blockSize: 0 }], contentBoxSize: [{ inlineSize: 0, blockSize: 0 }] }], self);
    }); };
    this.unobserve = function () {}; this.disconnect = function () {};
  };

  function defEvent(name, init) {
    window[name] = function (type, opts) {
      opts = opts || {};
      this.type = type;
      this.bubbles = !!opts.bubbles;
      this.cancelable = !!opts.cancelable;
      this.composed = !!opts.composed;
      this.defaultPrevented = false;
      if (init) init(this, opts);
    };
  }
  defEvent('Event');
  defEvent('CustomEvent', function (e, o) { e.detail = o.detail; });
  defEvent('MouseEvent', function (e, o) { e.clientX = o.clientX || 0; e.clientY = o.clientY || 0; e.button = o.button || 0; e.detail = o.detail || 0; });
  defEvent('PointerEvent', function (e, o) { e.clientX = o.clientX || 0; e.clientY = o.clientY || 0; e.button = o.button || 0; e.buttons = o.buttons || 0; e.detail = o.detail || 0; e.pointerType = o.pointerType || ''; e.pointerId = o.pointerId || 0; e.isPrimary = !!o.isPrimary; e.width = o.width || 0; e.height = o.height || 0; e.pressure = o.pressure || 0; });
  defEvent('KeyboardEvent', function (e, o) { e.key = o.key || ''; e.code = o.code || ''; e.keyCode = o.keyCode || 0; });
  defEvent('InputEvent', function (e, o) { e.data = (o.data != null) ? o.data : null; });

  // Minimal EventTarget + AbortController/AbortSignal (self-contained JS).
  function ETShim() { this.__l = {}; }
  ETShim.prototype.addEventListener = function (t, fn) { (this.__l[t] = this.__l[t] || []).push(fn); };
  ETShim.prototype.removeEventListener = function (t, fn) { var a = this.__l[t]; if (a) { var i = a.indexOf(fn); if (i >= 0) a.splice(i, 1); } };
  ETShim.prototype.dispatchEvent = function (ev) { var a = (this.__l[ev.type] || []).slice(); for (var i = 0; i < a.length; i++) { try { a[i].call(this, ev); } catch (e) {} } return true; };
  // window.EventTarget is the Go-installed DOM EventTarget (base of the node
  // prototype chain) so a node is instanceof EventTarget; ETShim only backs the
  // non-DOM AbortSignal below. Give the real EventTarget.prototype the same
  // generic listener methods so a plain new EventTarget() is usable too.
  if (window.EventTarget && window.EventTarget.prototype && !window.EventTarget.prototype.addEventListener) {
    var ep = window.EventTarget.prototype;
    ep.addEventListener = function (t, fn) { if (!this.__l) this.__l = {}; (this.__l[t] = this.__l[t] || []).push(fn); };
    ep.removeEventListener = function (t, fn) { if (!this.__l) return; var a = this.__l[t]; if (a) { var i = a.indexOf(fn); if (i >= 0) a.splice(i, 1); } };
    ep.dispatchEvent = function (ev) { if (!this.__l) return true; var a = (this.__l[ev.type] || []).slice(); for (var i = 0; i < a.length; i++) { try { a[i].call(this, ev); } catch (e) {} } return true; };
  }
  window.AbortSignal = function () { ETShim.call(this); this.aborted = false; this.reason = undefined; this.onabort = null; };
  window.AbortSignal.prototype = Object.create(ETShim.prototype);
  window.AbortSignal.prototype.throwIfAborted = function () { if (this.aborted) throw this.reason; };
  window.AbortSignal.abort = function (reason) { var c = new AbortController(); c.abort(reason); return c.signal; };
  window.AbortSignal.timeout = function (ms) { var c = new AbortController(); setTimeout(function () { c.abort(new Error('TimeoutError')); }, ms); return c.signal; };
  window.AbortController = function () { this.signal = new AbortSignal(); };
  window.AbortController.prototype.abort = function (reason) {
    var s = this.signal;
    if (s.aborted) return;
    s.aborted = true;
    s.reason = (reason !== undefined) ? reason : new Error('AbortError');
    var ev = { type: 'abort', target: s };
    if (typeof s.onabort === 'function') s.onabort(ev);
    s.dispatchEvent(ev);
  };

  // fetch / XMLHttpRequest (and the Headers/Request/Response/Blob/FormData
  // classes they use) live in preludeAPIJS (prelude_api.go), which runs right
  // after this prelude — both the network-backed implementations and the
  // network-disabled rejecting stubs.

  // Minimal crypto: getRandomValues + randomUUID, which uuid/nanoid/react-aria's
  // useId need at import/registration time (their absence throws and breaks
  // hydration). This Math.random baseline is REPLACED at prelude_api time by a
  // crypto/rand-backed getRandomValues (and crypto.subtle is installed there,
  // Go-backed — ADR 0006) whenever the __unblinkRandomBytes native is present; it
  // stays as the fallback for the rare no-native path. Assigning to a typed-array
  // element auto-masks to its byte width, so one loop covers Uint8/16/32.
  if (!window.crypto) window.crypto = {};
  if (!window.crypto.getRandomValues) {
    window.crypto.getRandomValues = function (arr) {
      for (var i = 0; i < arr.length; i++) arr[i] = Math.floor(Math.random() * 4294967296);
      return arr;
    };
  }
  if (!window.crypto.randomUUID) {
    window.crypto.randomUUID = function () {
      var b = new Uint8Array(16); window.crypto.getRandomValues(b);
      b[6] = (b[6] & 0x0f) | 0x40; b[8] = (b[8] & 0x3f) | 0x80;
      var h = []; for (var i = 0; i < 16; i++) h.push((b[i] + 0x100).toString(16).slice(1));
      return h.slice(0, 4).join('') + '-' + h.slice(4, 6).join('') + '-' +
             h.slice(6, 8).join('') + '-' + h.slice(8, 10).join('') + '-' + h.slice(10, 16).join('');
    };
  }

  // URLSearchParams + URL: SPA routers (react-router et al.) construct these at
  // hydration time; their absence throws ReferenceError and aborts routing (mobalytics
  // hit exactly this). Pairs keep insertion order and duplicates; encoding follows
  // application/x-www-form-urlencoded ('+' <-> space). URL is a pragmatic parser (not a
  // full WHATWG state machine) that resolves relative refs against a base/location and
  // exposes the components routers read, including .searchParams.
  function uspDec(s) { try { return decodeURIComponent(String(s).replace(/\+/g, ' ')); } catch (e) { return String(s); } }
  function uspEnc(s) { return encodeURIComponent(String(s)).replace(/%20/g, '+'); }
  function uspParse(init) {
    var pairs = [];
    if (init == null || init === '') return pairs;
    if (typeof init === 'string') {
      var s = init.charAt(0) === '?' ? init.slice(1) : init;
      if (s === '') return pairs;
      var parts = s.split('&');
      for (var i = 0; i < parts.length; i++) {
        if (parts[i] === '') continue;
        var eq = parts[i].indexOf('=');
        if (eq < 0) pairs.push([uspDec(parts[i]), '']);
        else pairs.push([uspDec(parts[i].slice(0, eq)), uspDec(parts[i].slice(eq + 1))]);
      }
    } else if (Array.isArray(init)) {
      for (var j = 0; j < init.length; j++) pairs.push([String(init[j][0]), String(init[j][1])]);
    } else if (typeof init === 'object') {
      for (var key in init) if (Object.prototype.hasOwnProperty.call(init, key)) pairs.push([String(key), String(init[key])]);
    }
    return pairs;
  }
  window.URLSearchParams = function (init) {
    if (init instanceof window.URLSearchParams) this.__p = init.__p.map(function (p) { return [p[0], p[1]]; });
    else this.__p = uspParse(init);
  };
  var USP = window.URLSearchParams.prototype;
  USP.append = function (k, v) { this.__p.push([String(k), String(v)]); };
  USP['delete'] = function (k) { k = String(k); this.__p = this.__p.filter(function (p) { return p[0] !== k; }); };
  USP.get = function (k) { k = String(k); for (var i = 0; i < this.__p.length; i++) if (this.__p[i][0] === k) return this.__p[i][1]; return null; };
  USP.getAll = function (k) { k = String(k); var r = []; for (var i = 0; i < this.__p.length; i++) if (this.__p[i][0] === k) r.push(this.__p[i][1]); return r; };
  USP.has = function (k) { return this.get(String(k)) !== null; };
  USP.set = function (k, v) { k = String(k); v = String(v); var set = false; var out = []; for (var i = 0; i < this.__p.length; i++) { if (this.__p[i][0] === k) { if (!set) { out.push([k, v]); set = true; } } else out.push(this.__p[i]); } if (!set) out.push([k, v]); this.__p = out; };
  USP.sort = function () { this.__p.sort(function (a, b) { return a[0] < b[0] ? -1 : (a[0] > b[0] ? 1 : 0); }); };
  USP.forEach = function (cb, thisArg) { for (var i = 0; i < this.__p.length; i++) cb.call(thisArg, this.__p[i][1], this.__p[i][0], this); };
  USP.keys = function () { return this.__p.map(function (p) { return p[0]; })[Symbol.iterator](); };
  USP.values = function () { return this.__p.map(function (p) { return p[1]; })[Symbol.iterator](); };
  USP.entries = function () { return this.__p.map(function (p) { return [p[0], p[1]]; })[Symbol.iterator](); };
  USP.toString = function () { return this.__p.map(function (p) { return uspEnc(p[0]) + '=' + uspEnc(p[1]); }).join('&'); };
  if (typeof Symbol === 'function' && Symbol.iterator) USP[Symbol.iterator] = USP.entries;

  function urlResolve(base, rel) {
    if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(rel)) return rel;
    var bm = /^([^:\/?#]+:)?(?:\/\/([^\/?#]*))?([^?#]*)(\?[^#]*)?(#.*)?$/.exec(base) || [];
    var scheme = bm[1] || '', authority = bm[2] || '', path = bm[3] || '';
    if (rel.indexOf('//') === 0) return scheme + rel;
    if (rel.charAt(0) === '/') return scheme + '//' + authority + rel;
    if (rel.charAt(0) === '?') return scheme + '//' + authority + path + rel;
    if (rel.charAt(0) === '#') return scheme + '//' + authority + path + (bm[4] || '') + rel;
    if (rel === '') return scheme + '//' + authority + path + (bm[4] || '');
    var dir = path.slice(0, path.lastIndexOf('/') + 1);
    var segs = (dir + rel).split('/'), out = [];
    for (var i = 0; i < segs.length; i++) {
      if (segs[i] === '.') continue;
      if (segs[i] === '..') { if (out.length && out[out.length - 1] !== '') out.pop(); continue; }
      out.push(segs[i]);
    }
    return scheme + '//' + authority + out.join('/');
  }
  window.URL = function (url, base) {
    url = String(url);
    if (base !== undefined && base !== null) url = urlResolve(String(base), url);
    else if (url.indexOf('://') < 0 && typeof location !== 'undefined' && location && location.href) url = urlResolve(location.href, url);
    var m = /^([^:\/?#]+:)?(?:\/\/([^\/?#]*))?([^?#]*)(\?[^#]*)?(#.*)?$/.exec(url) || [];
    this.href = url;
    this.protocol = m[1] || '';
    this.host = m[2] || '';
    var hp = this.host.split(':');
    this.hostname = hp[0] || '';
    this.port = hp[1] || '';
    this.pathname = m[3] || '';
    this.search = m[4] || '';
    this.hash = m[5] || '';
    this.origin = (this.protocol && this.host) ? this.protocol + '//' + this.host : '';
    this.username = ''; this.password = '';
    this.searchParams = new window.URLSearchParams(this.search);
  };
  window.URL.prototype.toString = function () { return this.href; };
  window.URL.prototype.toJSON = function () { return this.href; };
  window.URL.createObjectURL = function () { return 'blob:unblink'; };
  window.URL.revokeObjectURL = function () {};

  // document.write / writeln: append-mode. Post-parse write() must not blow the
  // document away (the destructive spec behavior); appending the parsed markup
  // to <body> keeps it visible to extraction — the dominant real-world use is
  // ad/analytics snippets injecting markup at load.
  if (typeof document !== 'undefined' && typeof document.write !== 'function') {
    var docWrite = function (html) {
      try {
        var host = document.createElement('div');
        host.innerHTML = String(html);
        var body = document.body || document.documentElement;
        if (!body) return;
        while (host.firstChild) body.appendChild(host.firstChild);
      } catch (e) {}
    };
    document.write = docWrite;
    document.writeln = function (html) { docWrite(String(html) + '\n'); };
  }
})();
`

// preludeProgram is the prelude compiled once at init and shared across every
// render and live context: a goja.Program is immutable and safe to run in
// multiple runtimes concurrently, and re-parsing the ~27KB source per render
// was the single largest fixed setup cost. MustCompile panics at init on a
// broken prelude, which any test run catches immediately.
var preludeProgram = goja.MustCompile("prelude.js", preludeJS, false)
