package js

import (
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

	nav := vm.NewObject()
	_ = nav.Set("userAgent", userAgent)
	_ = nav.Set("language", "en-US")
	_ = nav.Set("languages", []string{"en-US", "en"})
	_ = nav.Set("platform", "Linux x86_64")
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

// resolveNav resolves a (possibly relative) URL string against the current
// location. Returns the current URL unchanged on empty input or a parse failure.
func (b *bridge) resolveNav(raw string) *url.URL {
	if raw == "" || b.currentURL == nil {
		return b.currentURL
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return b.currentURL
	}
	return b.currentURL.ResolveReference(ref)
}

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
// interact result). Same-document changes (fragment only) update location silently.
func (b *bridge) navigate(raw string) {
	u := b.resolveNav(raw)
	if !sameDocument(b.currentURL, u) {
		b.pendingNav = u
	}
	b.updateLocation(u)
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
  // The engine's built-in console is disabled (its printer writes to stdout, which
  // is reserved for MCP). Real browsers always expose console, so provide a no-op
  // one here: page console.* calls stay silent instead of throwing ReferenceError
  // or leaking untrusted page text into the host logs.
  var noop = function () {};
  window.console = { log: noop, info: noop, warn: noop, error: noop, debug: noop,
    trace: noop, dir: noop, assert: noop, group: noop, groupCollapsed: noop,
    groupEnd: noop, table: noop, count: noop, time: noop, timeEnd: noop };

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
  window.localStorage = makeStorage();
  window.sessionStorage = makeStorage();

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

  window.matchMedia = function (q) {
    return { matches: false, media: q, onchange: null,
      addListener: function () {}, removeListener: function () {},
      addEventListener: function () {}, removeEventListener: function () {},
      dispatchEvent: function () { return false; } };
  };

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

  var hasNet = (typeof __unblinkFetch === 'function');

  if (hasNet) {
    function buildResponse(r) {
      return {
        ok: r.ok, status: r.status, statusText: '', url: r.url, headers: r.headers,
        text: function () { return Promise.resolve(r.body); },
        json: function () { return Promise.resolve(JSON.parse(r.body)); },
        clone: function () { return this; }
      };
    }
    window.fetch = function (url, opts) {
      opts = opts || {};
      var method = opts.method || 'GET';
      var headers = opts.headers || {};
      var body = opts.body != null ? String(opts.body) : '';
      var signal = opts.signal;
      if (signal && signal.aborted) return Promise.reject(signal.reason || new Error('AbortError'));
      var core = __unblinkFetch(method, String(url), headers, body).then(buildResponse);
      if (signal) {
        return new Promise(function (resolve, reject) {
          signal.addEventListener('abort', function () { reject(signal.reason || new Error('AbortError')); });
          core.then(resolve, reject);
        });
      }
      return core;
    };

    window.XMLHttpRequest = function () {
      var self = this;
      this.readyState = 0; this.status = 0; this.responseText = ''; this.response = '';
      this._method = 'GET'; this._url = ''; this._headers = {};
      this.onreadystatechange = null; this.onload = null; this.onerror = null;
      this.open = function (m, u) { self._method = m; self._url = u; self.readyState = 1; };
      this.setRequestHeader = function (k, v) { self._headers[k] = v; };
      this.getResponseHeader = function () { return null; };
      this.addEventListener = function (t, fn) { if (t === 'load') self.onload = fn; else if (t === 'error') self.onerror = fn; };
      this.removeEventListener = function () {};
      this.abort = function () {};
      this.send = function (body) {
        __unblinkFetch(self._method, self._url, self._headers, body != null ? String(body) : '')
          .then(function (r) {
            self.status = r.status; self.responseText = r.body; self.response = r.body; self.readyState = 4;
            if (self.onreadystatechange) self.onreadystatechange();
            if (self.onload) self.onload();
          }, function (e) {
            self.status = 0; self.readyState = 4;
            if (self.onreadystatechange) self.onreadystatechange();
            if (self.onerror) self.onerror(e);
          });
      };
    };
  } else {
    window.fetch = function () { return Promise.reject(new Error('unblink: network is disabled in render')); };
    window.XMLHttpRequest = function () {
      this.open = function () {}; this.send = function () {}; this.abort = function () {};
      this.setRequestHeader = function () {}; this.getResponseHeader = function () { return null; };
      this.addEventListener = function () {}; this.removeEventListener = function () {};
      this.readyState = 0; this.status = 0; this.responseText = ''; this.response = null;
    };
  }

  // Minimal crypto: getRandomValues + randomUUID, which uuid/nanoid/react-aria's
  // useId need at import/registration time (their absence throws and breaks
  // hydration). Backed by Math.random — NOT cryptographic, but these consumers only
  // need collision-resistant ids and unblink is a read-only content extractor, not a
  // security context. Assigning to a typed-array element auto-masks to its byte
  // width, so one loop covers Uint8/16/32. crypto.subtle is left undefined so
  // libraries feature-detect and fall back rather than hit a half-working stub.
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
})();
`
