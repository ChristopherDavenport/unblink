package js

import "github.com/dop251/goja"

// preludeAPIJS is the web-platform API layer: encoding (TextEncoder/atob),
// timing (performance), the constant viewport + a real matchMedia evaluator,
// the fetch ecosystem classes (Headers/Request/Response/Blob/File/FormData) and
// the fetch/XHR implementations built on __unblinkFetch, window messaging
// (postMessage/MessageChannel), parsing/selection shims (XMLSerializer/Range/
// Selection), constructable stylesheets, and an Intl crash-avoidance shim.
//
// It runs immediately after preludeJS (globals.go) in both the one-shot Render
// and the persistent Context, and depends on it for structuredClone, crypto,
// URL/URLSearchParams, and the Event constructors. Everything here follows the
// same rule as the rest of the engine: no layout, no pixels — the viewport is a
// truthful constant (1280×720@1x), element geometry stays zero.
const preludeAPIJS = `
(function () {
  var noop = function () {};

  // ---- encoding primitives (used by Blob/FormData/fetch below) ----

  function utf8Encode(s) {
    s = String(s);
    var out = [];
    for (var i = 0; i < s.length; i++) {
      var cp = s.codePointAt(i);
      if (cp > 0xFFFF) i++; // surrogate pair consumed two units
      if (cp < 0x80) out.push(cp);
      else if (cp < 0x800) out.push(0xC0 | (cp >> 6), 0x80 | (cp & 63));
      else if (cp < 0x10000) out.push(0xE0 | (cp >> 12), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
      else out.push(0xF0 | (cp >> 18), 0x80 | ((cp >> 12) & 63), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
    }
    return new Uint8Array(out);
  }

  function utf8Decode(bytes) {
    var out = '', i = 0, n = bytes.length;
    function cont(j) { return j < n && (bytes[j] & 0xC0) === 0x80; }
    while (i < n) {
      var b0 = bytes[i], cp;
      if (b0 < 0x80) { out += String.fromCharCode(b0); i++; continue; }
      if (b0 < 0xC2) { out += '�'; i++; continue; } // continuation or overlong lead
      if (b0 < 0xE0) {
        if (!cont(i + 1)) { out += '�'; i++; continue; }
        cp = ((b0 & 31) << 6) | (bytes[i + 1] & 63); i += 2;
      } else if (b0 < 0xF0) {
        if (!cont(i + 1) || !cont(i + 2)) { out += '�'; i++; continue; }
        cp = ((b0 & 15) << 12) | ((bytes[i + 1] & 63) << 6) | (bytes[i + 2] & 63); i += 3;
        if (cp < 0x800 || (cp >= 0xD800 && cp <= 0xDFFF)) { out += '�'; continue; }
      } else if (b0 < 0xF5) {
        if (!cont(i + 1) || !cont(i + 2) || !cont(i + 3)) { out += '�'; i++; continue; }
        cp = ((b0 & 7) << 18) | ((bytes[i + 1] & 63) << 12) | ((bytes[i + 2] & 63) << 6) | (bytes[i + 3] & 63); i += 4;
        if (cp < 0x10000 || cp > 0x10FFFF) { out += '�'; continue; }
      } else { out += '�'; i++; continue; }
      out += String.fromCodePoint(cp);
    }
    return out;
  }

  function toU8(input) {
    if (input == null) return new Uint8Array(0);
    if (input instanceof Uint8Array) return input;
    if (input instanceof ArrayBuffer) return new Uint8Array(input);
    if (ArrayBuffer.isView(input)) return new Uint8Array(input.buffer, input.byteOffset, input.byteLength);
    return new Uint8Array(0);
  }

  function concatBytes(chunks) {
    var total = 0, i;
    for (i = 0; i < chunks.length; i++) total += chunks[i].length;
    var out = new Uint8Array(total), off = 0;
    for (i = 0; i < chunks.length; i++) { out.set(chunks[i], off); off += chunks[i].length; }
    return out;
  }

  function bytesToArrayBuffer(u8) {
    return u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength);
  }

  if (typeof window.TextEncoder === 'undefined') {
    window.TextEncoder = function () { this.encoding = 'utf-8'; };
    window.TextEncoder.prototype.encode = function (s) { return utf8Encode(s === undefined ? '' : s); };
    window.TextEncoder.prototype.encodeInto = function (s, dest) {
      var bytes = utf8Encode(s === undefined ? '' : s);
      var written = Math.min(bytes.length, dest.length);
      dest.set(bytes.subarray(0, written));
      return { read: String(s).length, written: written };
    };
  }
  if (typeof window.TextDecoder === 'undefined') {
    // UTF-8 only: unblink normalizes page bytes to UTF-8 before JS ever runs.
    window.TextDecoder = function (label) { this.encoding = String(label || 'utf-8').toLowerCase(); this.fatal = false; this.ignoreBOM = false; };
    window.TextDecoder.prototype.decode = function (input) { return utf8Decode(toU8(input)); };
  }

  if (typeof window.btoa === 'undefined') {
    var B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
    window.btoa = function (s) {
      s = String(s);
      var out = '';
      for (var i = 0; i < s.length; i += 3) {
        var c1 = s.charCodeAt(i), c2 = s.charCodeAt(i + 1), c3 = s.charCodeAt(i + 2);
        if (c1 > 255 || c2 > 255 || c3 > 255) throw new Error('InvalidCharacterError: btoa: character out of latin1 range');
        var trip = (c1 << 16) | ((c2 || 0) << 8) | (c3 || 0);
        out += B64.charAt((trip >> 18) & 63) + B64.charAt((trip >> 12) & 63);
        out += (i + 1 < s.length) ? B64.charAt((trip >> 6) & 63) : '=';
        out += (i + 2 < s.length) ? B64.charAt(trip & 63) : '=';
      }
      return out;
    };
    window.atob = function (s) {
      s = String(s).replace(/[\t\n\f\r ]+/g, '');
      if (!/^[A-Za-z0-9+\/]*={0,2}$/.test(s) || s.length % 4 === 1) {
        throw new Error('InvalidCharacterError: atob: invalid base64');
      }
      s = s.replace(/=+$/, '');
      var out = '', buf = 0, bits = 0;
      for (var i = 0; i < s.length; i++) {
        buf = (buf << 6) | B64.indexOf(s.charAt(i));
        bits += 6;
        if (bits >= 8) { bits -= 8; out += String.fromCharCode((buf >> bits) & 255); }
      }
      return out;
    };
  }

  // ---- performance ----

  if (typeof window.performance === 'undefined' || typeof window.performance.now !== 'function') {
    var perfT0 = Date.now();
    var perfEntries = [];
    var perf = {
      timeOrigin: perfT0,
      now: function () { return Date.now() - perfT0; },
      mark: function (name) {
        var e = { name: String(name), entryType: 'mark', startTime: perf.now(), duration: 0, toJSON: function () { return this; } };
        perfEntries.push(e);
        return e;
      },
      measure: function (name, startMark, endMark) {
        var start = 0, end = perf.now();
        function lookup(n) {
          for (var i = perfEntries.length - 1; i >= 0; i--) {
            if (perfEntries[i].entryType === 'mark' && perfEntries[i].name === n) return perfEntries[i].startTime;
          }
          return null;
        }
        if (typeof startMark === 'string') { var s = lookup(startMark); if (s !== null) start = s; }
        if (typeof endMark === 'string') { var en = lookup(endMark); if (en !== null) end = en; }
        var e = { name: String(name), entryType: 'measure', startTime: start, duration: end - start, toJSON: function () { return this; } };
        perfEntries.push(e);
        return e;
      },
      getEntries: function () { return perfEntries.slice(); },
      getEntriesByType: function (t) { return perfEntries.filter(function (e) { return e.entryType === t; }); },
      getEntriesByName: function (n, t) { return perfEntries.filter(function (e) { return e.name === n && (t === undefined || e.entryType === t); }); },
      clearMarks: function (n) { perfEntries = perfEntries.filter(function (e) { return e.entryType !== 'mark' || (n !== undefined && e.name !== n); }); },
      clearMeasures: function (n) { perfEntries = perfEntries.filter(function (e) { return e.entryType !== 'measure' || (n !== undefined && e.name !== n); }); },
      clearResourceTimings: noop,
      setResourceTimingBufferSize: noop
    };
    window.performance = perf;
  }
  if (typeof window.PerformanceObserver === 'undefined') {
    window.PerformanceObserver = function () {};
    window.PerformanceObserver.prototype.observe = noop;
    window.PerformanceObserver.prototype.disconnect = noop;
    window.PerformanceObserver.prototype.takeRecords = function () { return []; };
    window.PerformanceObserver.supportedEntryTypes = [];
  }

  // ---- constant viewport + real matchMedia ----
  //
  // The environment is a truthful constant: a 1280x720@1x desktop viewport.
  // Element geometry stays zero (no layout engine — see proto.go), but the
  // *viewport* numbers responsive code branches on are real values, and
  // matchMedia genuinely evaluates queries against them instead of returning a
  // blanket false (which sent every width-branching page down its narrowest,
  // most stripped-down code path).

  var VP_W = 1280, VP_H = 720, VP_DPPX = 1;
  window.innerWidth = VP_W; window.innerHeight = VP_H;
  window.outerWidth = VP_W; window.outerHeight = VP_H;
  window.devicePixelRatio = VP_DPPX;
  window.scrollX = 0; window.scrollY = 0;
  window.pageXOffset = 0; window.pageYOffset = 0;
  window.screen = {
    width: VP_W, height: VP_H, availWidth: VP_W, availHeight: VP_H,
    colorDepth: 24, pixelDepth: 24,
    orientation: { type: 'landscape-primary', angle: 0, onchange: null, addEventListener: noop, removeEventListener: noop }
  };
  window.visualViewport = {
    width: VP_W, height: VP_H, scale: 1,
    offsetLeft: 0, offsetTop: 0, pageLeft: 0, pageTop: 0,
    onresize: null, onscroll: null,
    addEventListener: noop, removeEventListener: noop,
    dispatchEvent: function () { return true; }
  };

  function mmLength(v) {
    var m = /^([\d.]+)\s*(px|em|rem)?$/.exec(String(v).trim());
    if (!m) return null;
    var n = parseFloat(m[1]);
    if (m[2] === 'em' || m[2] === 'rem') n *= 16;
    return n;
  }
  function mmRatio(v) {
    var m = /^(\d+)\s*\/\s*(\d+)$/.exec(String(v).trim());
    if (m) return parseInt(m[1], 10) / parseInt(m[2], 10);
    var f = parseFloat(v);
    return isNaN(f) ? null : f;
  }
  function mmResolution(v) {
    var m = /^([\d.]+)\s*(dppx|dpi|dpcm|x)?$/.exec(String(v).trim());
    if (!m) return null;
    var n = parseFloat(m[1]);
    if (m[2] === 'dpi') n /= 96;
    if (m[2] === 'dpcm') n /= 37.8;
    return n;
  }
  // evalFeature evaluates one (feature) / (feature: value) term against the
  // constant environment. Returns true/false, or null for an unknown feature
  // (which makes the whole query 'not all', per spec).
  function mmFeature(name, value) {
    name = String(name).toLowerCase();
    var pre = /^(min|max)-(.+)$/.exec(name);
    var cmp = pre ? pre[1] : null;
    var base = pre ? pre[2] : name;
    function range(actual, parsed) {
      if (parsed === null) return false;
      if (cmp === 'min') return actual >= parsed - 1e-9;
      if (cmp === 'max') return actual <= parsed + 1e-9;
      return Math.abs(actual - parsed) < 1e-9;
    }
    switch (base) {
      case 'width': case 'device-width':
        return value == null ? VP_W > 0 : range(VP_W, mmLength(value));
      case 'height': case 'device-height':
        return value == null ? VP_H > 0 : range(VP_H, mmLength(value));
      case 'aspect-ratio': case 'device-aspect-ratio':
        return value == null ? true : range(VP_W / VP_H, mmRatio(value));
      case 'resolution':
        return value == null ? true : range(VP_DPPX, mmResolution(value));
    }
    var val = value == null ? null : String(value).toLowerCase().trim();
    switch (name) {
      case 'orientation': return val === null || val === 'landscape';
      case 'prefers-color-scheme': return val === null || val === 'light';
      case 'prefers-reduced-motion':
      case 'prefers-reduced-transparency':
      case 'prefers-reduced-data':
        return val === 'no-preference'; // bare form asks "is it reduced?" — no
      case 'prefers-contrast': return val === 'no-preference';
      case 'forced-colors': case 'inverted-colors': return val === 'none';
      case 'hover': case 'any-hover': return val === null || val === 'hover';
      case 'pointer': case 'any-pointer': return val === null || val === 'fine';
      case 'display-mode': return val === null || val === 'browser';
      case 'update': return val === null || val === 'fast';
      case 'scripting': return val === null || val === 'enabled';
      case 'color': return val === null || parseFloat(val) <= 24;
      case 'monochrome': return val !== null && parseFloat(val) === 0;
      case 'grid': return val !== null && parseFloat(val) === 0;
    }
    return null;
  }
  // mmQuery evaluates one comma-separated alternative: [not|only] [type] terms
  // joined by 'and'. Range syntax ((400px <= width)) is unsupported -> not all.
  function mmQuery(q) {
    q = String(q).trim();
    if (q === '') return true;
    var negate = false;
    var mod = /^(not|only)\s+/i.exec(q);
    if (mod) {
      if (mod[1].toLowerCase() === 'not') negate = true;
      q = q.slice(mod[0].length);
    }
    var result = true;
    var terms = q.split(/\s+and\s+/i);
    for (var i = 0; i < terms.length; i++) {
      var term = terms[i].trim();
      if (term === '') continue;
      if (term.charAt(0) === '(') {
        var fm = /^\(\s*([A-Za-z-]+)\s*(?::\s*([^)]+))?\s*\)$/.exec(term);
        var ok = fm ? mmFeature(fm[1], fm[2] === undefined ? null : fm[2]) : null;
        if (ok !== true) { result = false; break; }
      } else {
        var t = term.toLowerCase();
        if (t !== 'all' && t !== 'screen') { result = false; break; }
      }
    }
    return negate ? !result : result;
  }
  window.matchMedia = function (query) {
    var qs = String(query == null ? '' : query);
    var matches = false;
    var parts = qs.split(',');
    for (var i = 0; i < parts.length; i++) {
      if (mmQuery(parts[i])) { matches = true; break; }
    }
    // Listeners are accepted but never fire: the viewport is a constant, so a
    // change event cannot happen. matches itself is truthful.
    return {
      matches: matches, media: qs, onchange: null,
      addListener: noop, removeListener: noop,
      addEventListener: noop, removeEventListener: noop,
      dispatchEvent: function () { return false; }
    };
  };

  // ---- WeakRef / FinalizationRegistry ----
  //
  // goja has neither. A strong-reference WeakRef is spec-legal (deref may
  // always return the target; GC is never observable), and FinalizationRegistry
  // callbacks are never guaranteed to run. The leak a strong ref creates is
  // bounded by runtime teardown (one-shot renders) / session eviction.

  if (typeof window.WeakRef === 'undefined') {
    window.WeakRef = function (target) {
      if (target === null || (typeof target !== 'object' && typeof target !== 'function')) {
        throw new TypeError('WeakRef: target must be an object');
      }
      this.__target = target;
    };
    window.WeakRef.prototype.deref = function () { return this.__target; };
  }
  if (typeof window.FinalizationRegistry === 'undefined') {
    window.FinalizationRegistry = function (cb) {
      if (typeof cb !== 'function') throw new TypeError('FinalizationRegistry: callback must be a function');
    };
    window.FinalizationRegistry.prototype.register = noop;
    window.FinalizationRegistry.prototype.unregister = function () { return false; };
    window.FinalizationRegistry.prototype.cleanupSome = noop;
  }

  // ---- Blob / File / FormData ----

  function partToBytes(part) {
    if (part instanceof window.Blob) return part.__bytes;
    if (part instanceof ArrayBuffer || ArrayBuffer.isView(part)) return toU8(part).slice();
    return utf8Encode(String(part));
  }

  window.Blob = function (parts, opts) {
    opts = opts || {};
    var chunks = [];
    if (parts != null) {
      for (var i = 0; i < parts.length; i++) chunks.push(partToBytes(parts[i]));
    }
    this.__bytes = concatBytes(chunks);
    this.size = this.__bytes.length;
    this.type = String(opts.type || '').toLowerCase();
  };
  window.Blob.prototype.text = function () { return Promise.resolve(utf8Decode(this.__bytes)); };
  window.Blob.prototype.arrayBuffer = function () { return Promise.resolve(bytesToArrayBuffer(this.__bytes)); };
  window.Blob.prototype.bytes = function () { return Promise.resolve(this.__bytes.slice()); };
  window.Blob.prototype.slice = function (start, end, type) {
    var len = this.__bytes.length;
    start = start === undefined ? 0 : (start < 0 ? Math.max(len + start, 0) : Math.min(start, len));
    end = end === undefined ? len : (end < 0 ? Math.max(len + end, 0) : Math.min(end, len));
    var b = Object.create(window.Blob.prototype);
    b.__bytes = this.__bytes.slice(start, Math.max(start, end));
    b.size = b.__bytes.length;
    b.type = String(type || '').toLowerCase();
    return b;
  };

  window.File = function (parts, name, opts) {
    opts = opts || {};
    window.Blob.call(this, parts, opts);
    this.name = String(name);
    this.lastModified = opts.lastModified !== undefined ? opts.lastModified : Date.now();
  };
  window.File.prototype = Object.create(window.Blob.prototype);
  window.File.prototype.constructor = window.File;

  window.FormData = function (form) {
    this.__e = []; // ordered [name, string|File] pairs
    if (form && form.tagName === 'FORM' && form.elements) {
      // Successful-controls subset: named, enabled text-ish controls plus
      // checked checkboxes/radios. File inputs have no picker here — skipped.
      var els = form.elements;
      for (var i = 0; i < els.length; i++) {
        var el = els.item ? els.item(i) : els[i];
        if (!el || !el.getAttribute) continue;
        var name = el.getAttribute('name');
        if (!name || el.disabled) continue;
        var tag = String(el.tagName || '').toLowerCase();
        var type = String(el.getAttribute('type') || '').toLowerCase();
        if (tag === 'button' || type === 'submit' || type === 'button' || type === 'reset' || type === 'file') continue;
        if (type === 'checkbox' || type === 'radio') {
          if (!el.checked) continue;
          this.__e.push([name, el.value ? String(el.value) : 'on']);
          continue;
        }
        this.__e.push([name, el.value != null ? String(el.value) : '']);
      }
    }
  };
  var FD = window.FormData.prototype;
  function fdValue(value, filename) {
    if (value instanceof window.Blob && !(value instanceof window.File)) {
      var f = Object.create(window.File.prototype);
      f.__bytes = value.__bytes; f.size = value.size; f.type = value.type;
      f.name = filename !== undefined ? String(filename) : 'blob';
      f.lastModified = Date.now();
      return f;
    }
    if (value instanceof window.File && filename !== undefined) {
      var g = Object.create(window.File.prototype);
      g.__bytes = value.__bytes; g.size = value.size; g.type = value.type;
      g.name = String(filename); g.lastModified = value.lastModified;
      return g;
    }
    return value instanceof window.File ? value : String(value);
  }
  FD.append = function (name, value, filename) { this.__e.push([String(name), fdValue(value, filename)]); };
  FD.set = function (name, value, filename) {
    name = String(name);
    var v = fdValue(value, filename), placed = false, out = [];
    for (var i = 0; i < this.__e.length; i++) {
      if (this.__e[i][0] === name) {
        if (!placed) { out.push([name, v]); placed = true; }
      } else out.push(this.__e[i]);
    }
    if (!placed) out.push([name, v]);
    this.__e = out;
  };
  FD['delete'] = function (name) { name = String(name); this.__e = this.__e.filter(function (p) { return p[0] !== name; }); };
  FD.get = function (name) { name = String(name); for (var i = 0; i < this.__e.length; i++) if (this.__e[i][0] === name) return this.__e[i][1]; return null; };
  FD.getAll = function (name) { name = String(name); return this.__e.filter(function (p) { return p[0] === name; }).map(function (p) { return p[1]; }); };
  FD.has = function (name) { return this.get(name) !== null; };
  FD.forEach = function (cb, thisArg) { for (var i = 0; i < this.__e.length; i++) cb.call(thisArg, this.__e[i][1], this.__e[i][0], this); };
  FD.entries = function () { return this.__e.map(function (p) { return [p[0], p[1]]; })[Symbol.iterator](); };
  FD.keys = function () { return this.__e.map(function (p) { return p[0]; })[Symbol.iterator](); };
  FD.values = function () { return this.__e.map(function (p) { return p[1]; })[Symbol.iterator](); };
  if (typeof Symbol === 'function' && Symbol.iterator) FD[Symbol.iterator] = FD.entries;

  // ---- Headers / Request / Response ----

  window.Headers = function (init) {
    this.__h = []; // [lowercased-name, value], insertion order
    if (init instanceof window.Headers) {
      this.__h = init.__h.map(function (p) { return [p[0], p[1]]; });
    } else if (Array.isArray(init)) {
      for (var i = 0; i < init.length; i++) this.append(init[i][0], init[i][1]);
    } else if (init != null && typeof init === 'object') {
      for (var k in init) if (Object.prototype.hasOwnProperty.call(init, k)) this.append(k, init[k]);
    }
  };
  var H = window.Headers.prototype;
  H.append = function (name, value) { this.__h.push([String(name).toLowerCase(), String(value)]); };
  H.set = function (name, value) { this['delete'](name); this.append(name, value); };
  H['delete'] = function (name) { name = String(name).toLowerCase(); this.__h = this.__h.filter(function (p) { return p[0] !== name; }); };
  H.get = function (name) {
    name = String(name).toLowerCase();
    var vals = this.__h.filter(function (p) { return p[0] === name; }).map(function (p) { return p[1]; });
    return vals.length ? vals.join(', ') : null;
  };
  H.has = function (name) { return this.get(name) !== null; };
  H.forEach = function (cb, thisArg) { for (var i = 0; i < this.__h.length; i++) cb.call(thisArg, this.__h[i][1], this.__h[i][0], this); };
  H.entries = function () { return this.__h.map(function (p) { return [p[0], p[1]]; })[Symbol.iterator](); };
  H.keys = function () { return this.__h.map(function (p) { return p[0]; })[Symbol.iterator](); };
  H.values = function () { return this.__h.map(function (p) { return p[1]; })[Symbol.iterator](); };
  if (typeof Symbol === 'function' && Symbol.iterator) H[Symbol.iterator] = H.entries;

  function headersToPlain(h) {
    var out = {};
    if (!h) return out;
    if (!(h instanceof window.Headers)) h = new window.Headers(h);
    for (var i = 0; i < h.__h.length; i++) {
      var k = h.__h[i][0];
      out[k] = (k in out) ? out[k] + ', ' + h.__h[i][1] : h.__h[i][1];
    }
    return out;
  }

  window.Request = function (input, init) {
    init = init || {};
    if (input instanceof window.Request) {
      this.url = input.url;
      this.method = init.method !== undefined ? init.method : input.method;
      this.headers = new window.Headers(init.headers !== undefined ? init.headers : input.headers);
      this.__body = init.body !== undefined ? init.body : input.__body;
      this.signal = init.signal !== undefined ? init.signal : input.signal;
      this.credentials = init.credentials !== undefined ? init.credentials : input.credentials;
      this.mode = init.mode !== undefined ? init.mode : input.mode;
    } else {
      this.url = String(input);
      this.method = init.method !== undefined ? init.method : 'GET';
      this.headers = new window.Headers(init.headers);
      this.__body = init.body;
      this.signal = init.signal;
      this.credentials = init.credentials || 'same-origin';
      this.mode = init.mode || 'cors';
    }
    this.method = String(this.method).toUpperCase();
    this.redirect = init.redirect || 'follow';
    this.bodyUsed = false;
  };
  window.Request.prototype.clone = function () { return new window.Request(this); };
  window.Request.prototype.text = function () {
    var b = this.__body;
    if (b == null) return Promise.resolve('');
    if (b instanceof window.Blob) return b.text();
    if (b instanceof ArrayBuffer || ArrayBuffer.isView(b)) return Promise.resolve(utf8Decode(toU8(b)));
    return Promise.resolve(String(b));
  };
  window.Request.prototype.json = function () { return this.text().then(JSON.parse); };

  window.Response = function (body, init) {
    init = init || {};
    this.status = init.status !== undefined ? Number(init.status) : 200;
    this.statusText = init.statusText !== undefined ? String(init.statusText) : '';
    this.ok = this.status >= 200 && this.status < 300;
    this.headers = new window.Headers(init.headers);
    this.url = init.url !== undefined ? String(init.url) : '';
    this.redirected = false;
    this.type = 'basic';
    this.bodyUsed = false;
    this.__text = null;  // string body when the source was text
    this.__bytes = null; // Uint8Array body when the source was binary
    if (body == null) {
      this.__text = '';
    } else if (body instanceof window.Blob) {
      this.__bytes = body.__bytes;
      if (body.type && !this.headers.has('content-type')) this.headers.set('content-type', body.type);
    } else if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) {
      this.__bytes = toU8(body).slice();
    } else if (typeof window.URLSearchParams === 'function' && body instanceof window.URLSearchParams) {
      this.__text = body.toString();
      if (!this.headers.has('content-type')) this.headers.set('content-type', 'application/x-www-form-urlencoded;charset=UTF-8');
    } else if (body instanceof window.FormData) {
      var ser = serializeBody(body);
      this.__bytes = toU8(ser.data);
      if (!this.headers.has('content-type')) this.headers.set('content-type', ser.type);
    } else {
      this.__text = String(body);
    }
  };
  var RES = window.Response.prototype;
  function resText(r) { return r.__text !== null ? r.__text : utf8Decode(r.__bytes || new Uint8Array(0)); }
  function resBytes(r) { return r.__bytes !== null ? r.__bytes : utf8Encode(r.__text || ''); }
  RES.text = function () { this.bodyUsed = true; return Promise.resolve(resText(this)); };
  RES.json = function () { this.bodyUsed = true; return Promise.resolve(resText(this)).then(JSON.parse); };
  RES.arrayBuffer = function () { this.bodyUsed = true; return Promise.resolve(bytesToArrayBuffer(resBytes(this))); };
  RES.bytes = function () { this.bodyUsed = true; return Promise.resolve(resBytes(this).slice()); };
  RES.blob = function () {
    this.bodyUsed = true;
    var b = Object.create(window.Blob.prototype);
    b.__bytes = resBytes(this).slice();
    b.size = b.__bytes.length;
    b.type = (this.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
    return Promise.resolve(b);
  };
  RES.clone = function () {
    var r = Object.create(RES);
    r.status = this.status; r.statusText = this.statusText; r.ok = this.ok;
    r.headers = new window.Headers(this.headers);
    r.url = this.url; r.redirected = this.redirected; r.type = this.type;
    r.bodyUsed = false;
    r.__text = this.__text; r.__bytes = this.__bytes;
    return r;
  };
  window.Response.json = function (data, init) {
    var r = new window.Response(JSON.stringify(data), init);
    if (!r.headers.has('content-type')) r.headers.set('content-type', 'application/json');
    return r;
  };
  window.Response.error = function () {
    var r = new window.Response(null, { status: 0 });
    r.type = 'error'; r.ok = false;
    return r;
  };
  window.Response.redirect = function (url, status) {
    return new window.Response(null, { status: status || 302, headers: { location: String(url) } });
  };

  // serializeBody flattens a fetch/XHR body into what __unblinkFetch accepts
  // (string or ArrayBuffer) plus the implied Content-Type. FormData becomes
  // multipart right here in JS so the Go transport signature stays untouched.
  function serializeBody(body) {
    if (body == null) return { data: '', type: null };
    if (typeof body === 'string') return { data: body, type: null };
    if (typeof window.URLSearchParams === 'function' && body instanceof window.URLSearchParams) {
      return { data: body.toString(), type: 'application/x-www-form-urlencoded;charset=UTF-8' };
    }
    if (body instanceof window.Blob) {
      return { data: bytesToArrayBuffer(body.__bytes), type: body.type || null };
    }
    if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) {
      return { data: bytesToArrayBuffer(toU8(body)), type: null };
    }
    if (body instanceof window.FormData) {
      var boundary = '----unblink' + (window.crypto && window.crypto.randomUUID ? window.crypto.randomUUID().replace(/-/g, '') : String(Math.random()).slice(2));
      var esc = function (s) { return String(s).replace(/"/g, '%22').replace(/\r/g, '%0D').replace(/\n/g, '%0A'); };
      var chunks = [];
      for (var i = 0; i < body.__e.length; i++) {
        var name = body.__e[i][0], val = body.__e[i][1];
        var head = '--' + boundary + '\r\nContent-Disposition: form-data; name="' + esc(name) + '"';
        if (val instanceof window.Blob) {
          head += '; filename="' + esc(val.name || 'blob') + '"\r\nContent-Type: ' + (val.type || 'application/octet-stream');
          chunks.push(utf8Encode(head + '\r\n\r\n'), val.__bytes, utf8Encode('\r\n'));
        } else {
          chunks.push(utf8Encode(head + '\r\n\r\n' + String(val) + '\r\n'));
        }
      }
      chunks.push(utf8Encode('--' + boundary + '--\r\n'));
      return { data: bytesToArrayBuffer(concatBytes(chunks)), type: 'multipart/form-data; boundary=' + boundary };
    }
    return { data: String(body), type: null };
  }

  // ---- fetch / XMLHttpRequest ----

  var hasNet = (typeof __unblinkFetch === 'function');

  if (hasNet) {
    window.fetch = function (input, opts) {
      var req;
      try {
        req = (input instanceof window.Request && opts === undefined) ? input : new window.Request(input, opts);
      } catch (e) { return Promise.reject(e); }
      var headers = new window.Headers(req.headers);
      var ser = serializeBody(req.__body);
      if (ser.type && !headers.has('content-type')) headers.set('content-type', ser.type);
      var signal = req.signal;
      if (signal && signal.aborted) return Promise.reject(signal.reason || new Error('AbortError'));
      var reqURL = req.url;
      var core = __unblinkFetch(req.method, String(reqURL), headersToPlain(headers), ser.data).then(function (r) {
        var resp = new window.Response(null, { status: r.status, headers: r.headers, url: r.url });
        resp.__text = r.body;
        resp.__bytes = r.bodyBytes ? new Uint8Array(r.bodyBytes) : null;
        try {
          var abs = new window.URL(reqURL, (typeof location !== 'undefined' && location && location.href) || undefined).href;
          resp.redirected = !!(r.url && r.url !== abs);
        } catch (e) { resp.redirected = false; }
        return resp;
      });
      if (signal) {
        return new Promise(function (resolve, reject) {
          signal.addEventListener('abort', function () { reject(signal.reason || new Error('AbortError')); });
          core.then(resolve, reject);
        });
      }
      return core;
    };

    // XHR carries the surface Angular's HttpXhrBackend reads inside its load
    // handler (getAllResponseHeaders/statusText/responseURL) — their absence
    // makes the response silently undeliverable. responseType='json' parses like
    // a real browser (null on bad JSON, never a throw); 'arraybuffer'/'blob'
    // read the binary body.
    window.XMLHttpRequest = function () {
      var self = this;
      this.readyState = 0; this.status = 0; this.statusText = '';
      this.responseText = ''; this.response = ''; this.responseType = '';
      this.responseURL = ''; this.withCredentials = false; this.timeout = 0;
      this._method = 'GET'; this._url = ''; this._headers = {}; this._resHeaders = null;
      this._listeners = {};
      this.onreadystatechange = null; this.onload = null; this.onerror = null;
      this.upload = { addEventListener: noop, removeEventListener: noop };
      this.open = function (m, u) { self._method = m; self._url = u; self.readyState = 1; };
      this.setRequestHeader = function (k, v) { self._headers[k] = v; };
      this.getResponseHeader = function (k) {
        if (!self._resHeaders) return null;
        var v = self._resHeaders[String(k).toLowerCase()];
        return v == null ? null : v;
      };
      this.getAllResponseHeaders = function () {
        if (!self._resHeaders) return '';
        var out = '';
        for (var k in self._resHeaders) out += k + ': ' + self._resHeaders[k] + '\r\n';
        return out;
      };
      this.overrideMimeType = noop;
      this.addEventListener = function (t, fn) { (self._listeners[t] || (self._listeners[t] = [])).push(fn); };
      this.removeEventListener = function (t, fn) {
        var a = self._listeners[t]; if (!a) return;
        var i = a.indexOf(fn); if (i >= 0) a.splice(i, 1);
      };
      this.abort = noop;
      // Handler exceptions propagate (they surface as unhandled rejections in the
      // render diagnostics rather than vanishing).
      function fire(type, evt) {
        evt = evt || { type: type, target: self };
        var h = self['on' + type];
        if (h) h.call(self, evt);
        var a = (self._listeners[type] || []).slice();
        for (var i = 0; i < a.length; i++) a[i].call(self, evt);
      }
      this.send = function (body) {
        var ser = serializeBody(body);
        if (ser.type) {
          var hasCT = false;
          for (var k in self._headers) if (String(k).toLowerCase() === 'content-type') { hasCT = true; break; }
          if (!hasCT) self._headers['content-type'] = ser.type;
        }
        __unblinkFetch(self._method, self._url, self._headers, ser.data)
          .then(function (r) {
            self.status = r.status; self.statusText = 'OK';
            self.responseURL = r.url || self._url; self._resHeaders = r.headers || {};
            self.responseText = r.body;
            if (self.responseType === 'json') {
              try { self.response = r.body === '' ? null : JSON.parse(r.body); } catch (e) { self.response = null; }
            } else if (self.responseType === 'arraybuffer') {
              self.response = r.bodyBytes || new ArrayBuffer(0);
            } else if (self.responseType === 'blob') {
              self.response = new window.Blob([r.bodyBytes ? new Uint8Array(r.bodyBytes) : new Uint8Array(0)]);
            } else {
              self.response = r.body;
            }
            self.readyState = 4;
            if (self.onreadystatechange) self.onreadystatechange();
            fire('load'); fire('loadend');
          }, function (e) {
            self.status = 0; self.readyState = 4;
            if (self.onreadystatechange) self.onreadystatechange();
            fire('error', { type: 'error', target: self, message: String(e) });
            fire('loadend');
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

  // ---- dialogs / window shell ----
  //
  // alert is swallowed; confirm auto-accepts (unblink drives read flows, and a
  // blocked confirm dead-ends them); prompt is dismissed (never fabricate user
  // input). window.open is popup-blocked semantics: the page keeps running.

  window.alert = noop;
  window.confirm = function () { return true; };
  window.prompt = function () { return null; };
  window.print = noop;
  window.open = function () { return null; };
  window.stop = noop;
  window.opener = null;
  window.closed = false;
  window.top = window;
  window.parent = window;
  window.frames = window;
  window.frameElement = null;
  if (window.name === undefined) window.name = '';
  window.status = '';

  // ---- messaging: postMessage / MessageChannel / BroadcastChannel ----

  if (typeof window.MessageEvent === 'undefined') {
    window.MessageEvent = function (type, opts) {
      opts = opts || {};
      this.type = type;
      this.bubbles = !!opts.bubbles;
      this.cancelable = !!opts.cancelable;
      this.defaultPrevented = false;
      this.data = opts.data !== undefined ? opts.data : null;
      this.origin = opts.origin || '';
      this.source = opts.source !== undefined ? opts.source : null;
      this.ports = opts.ports || [];
      this.lastEventId = '';
    };
  }

  // Same-window postMessage: one window, so the message loops back to it (the
  // pattern pages actually use in a single-context world). Clone errors throw
  // synchronously per spec; delivery is async.
  window.postMessage = function (data) {
    var cloned = structuredClone(data);
    setTimeout(function () {
      var ev = new window.MessageEvent('message', {
        data: cloned,
        origin: (typeof location !== 'undefined' && location && location.origin) || '',
        source: window
      });
      window.dispatchEvent(ev);
      if (typeof window.onmessage === 'function') {
        try { window.onmessage(ev); } catch (e) {}
      }
    }, 0);
  };

  var MessagePort = function () {
    this.__l = [];
    this.__q = []; // FIFO delivery queue; holds messages until start()
    this.__started = false;
    this.__flushing = false;
    this.__closed = false;
    this.__peer = null;
    this.__onmessage = null;
  };
  MessagePort.prototype.addEventListener = function (t, fn) { if (t === 'message') this.__l.push(fn); };
  MessagePort.prototype.removeEventListener = function (t, fn) { var i = this.__l.indexOf(fn); if (i >= 0) this.__l.splice(i, 1); };
  MessagePort.prototype.dispatchEvent = function (ev) { this.__deliver(ev); return true; };
  MessagePort.prototype.__deliver = function (ev) {
    if (typeof this.__onmessage === 'function') { try { this.__onmessage(ev); } catch (e) {} }
    var a = this.__l.slice();
    for (var i = 0; i < a.length; i++) { try { a[i].call(this, ev); } catch (e) {} }
  };
  // One async flush drains the whole queue in order — per-message timers are
  // not ordering-safe (goja's same-deadline timers may fire out of scheduling
  // order), a single FIFO drain is.
  MessagePort.prototype.__flush = function () {
    var self = this;
    if (!self.__started || self.__flushing) return;
    self.__flushing = true;
    setTimeout(function () {
      self.__flushing = false;
      while (self.__started && self.__q.length) {
        self.__deliver(new window.MessageEvent('message', { data: self.__q.shift() }));
      }
    }, 0);
  };
  MessagePort.prototype.__receive = function (data) { this.__q.push(data); this.__flush(); };
  MessagePort.prototype.start = function () {
    if (this.__started) return;
    this.__started = true;
    this.__flush();
  };
  MessagePort.prototype.postMessage = function (data) {
    if (this.__closed || !this.__peer) return;
    this.__peer.__receive(structuredClone(data));
  };
  MessagePort.prototype.close = function () { this.__closed = true; };
  // Assigning onmessage implicitly starts the port, per spec.
  Object.defineProperty(MessagePort.prototype, 'onmessage', {
    get: function () { return this.__onmessage; },
    set: function (fn) { this.__onmessage = fn; this.start(); }
  });
  window.MessagePort = MessagePort;
  window.MessageChannel = function () {
    var p1 = new MessagePort(), p2 = new MessagePort();
    p1.__peer = p2; p2.__peer = p1;
    this.port1 = p1; this.port2 = p2;
  };

  // BroadcastChannel: constructible but inert — there is only ever one context,
  // and a channel never delivers to its own sender, so silence is spec-shaped.
  window.BroadcastChannel = function (name) {
    this.name = String(name);
    this.onmessage = null; this.onmessageerror = null;
  };
  window.BroadcastChannel.prototype.postMessage = noop;
  window.BroadcastChannel.prototype.close = noop;
  window.BroadcastChannel.prototype.addEventListener = noop;
  window.BroadcastChannel.prototype.removeEventListener = noop;
  window.BroadcastChannel.prototype.dispatchEvent = function () { return true; };

  // ---- XMLSerializer / Range / Selection ----

  window.XMLSerializer = function () {};
  window.XMLSerializer.prototype.serializeToString = function (node) {
    if (!node) return '';
    var el = node.documentElement || node; // document facades expose documentElement
    if (el && el.outerHTML != null) return el.outerHTML;
    return String((el && el.textContent) || '');
  };

  function zeroRect() {
    return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: function () { return this; } };
  }

  // Range: enough shape for libraries that create one defensively. No editing
  // model — mutation methods are inert — but createContextualFragment really
  // parses (it's what several template engines use).
  var Range = function () {
    var d = (typeof document !== 'undefined') ? document : null;
    this.collapsed = true;
    this.startContainer = d; this.endContainer = d;
    this.startOffset = 0; this.endOffset = 0;
    this.commonAncestorContainer = d;
  };
  Range.prototype.setStart = function (n, o) { this.startContainer = n; this.startOffset = o || 0; this.commonAncestorContainer = n; };
  Range.prototype.setEnd = function (n, o) { this.endContainer = n; this.endOffset = o || 0; };
  Range.prototype.setStartBefore = function (n) { this.setStart(n.parentNode || n, 0); };
  Range.prototype.setStartAfter = Range.prototype.setStartBefore;
  Range.prototype.setEndBefore = function (n) { this.setEnd(n.parentNode || n, 0); };
  Range.prototype.setEndAfter = Range.prototype.setEndBefore;
  Range.prototype.collapse = function () { this.collapsed = true; };
  Range.prototype.selectNode = function (n) { this.setStart(n.parentNode || n, 0); this.setEnd(n.parentNode || n, 0); };
  Range.prototype.selectNodeContents = function (n) { this.setStart(n, 0); this.setEnd(n, 0); };
  Range.prototype.deleteContents = noop;
  Range.prototype.extractContents = function () { return document.createDocumentFragment(); };
  Range.prototype.cloneContents = function () { return document.createDocumentFragment(); };
  Range.prototype.insertNode = noop;
  Range.prototype.surroundContents = noop;
  Range.prototype.cloneRange = function () { var r = new Range(); r.startContainer = this.startContainer; r.endContainer = this.endContainer; r.startOffset = this.startOffset; r.endOffset = this.endOffset; return r; };
  Range.prototype.detach = noop;
  Range.prototype.toString = function () { return ''; };
  Range.prototype.getBoundingClientRect = zeroRect;
  Range.prototype.getClientRects = function () { return []; };
  Range.prototype.createContextualFragment = function (markup) {
    var frag = document.createDocumentFragment();
    var host = document.createElement('div');
    host.innerHTML = String(markup);
    while (host.firstChild) frag.appendChild(host.firstChild);
    return frag;
  };
  window.Range = Range;
  if (typeof document !== 'undefined') {
    document.createRange = function () { return new Range(); };
  }

  var emptySelection = {
    rangeCount: 0, isCollapsed: true, type: 'None',
    anchorNode: null, anchorOffset: 0, focusNode: null, focusOffset: 0,
    toString: function () { return ''; },
    getRangeAt: function () { return null; },
    addRange: noop, removeRange: noop, removeAllRanges: noop, empty: noop,
    collapse: noop, collapseToStart: noop, collapseToEnd: noop,
    selectAllChildren: noop, setBaseAndExtent: noop, extend: noop, modify: noop,
    containsNode: function () { return false; },
    deleteFromDocument: noop
  };
  window.getSelection = function () { return emptySelection; };
  window.Selection = function () {};
  if (typeof document !== 'undefined') {
    document.getSelection = window.getSelection;
  }

  // ---- constructable stylesheets (accepted-and-ignored: no CSSOM) ----

  window.CSSStyleSheet = function () {
    this.cssRules = [];
    this.rules = this.cssRules;
    this.disabled = false;
    this.media = { mediaText: '' };
    this.__text = '';
  };
  window.CSSStyleSheet.prototype.replaceSync = function (text) { this.__text = String(text); };
  window.CSSStyleSheet.prototype.replace = function (text) { this.__text = String(text); return Promise.resolve(this); };
  window.CSSStyleSheet.prototype.insertRule = function () { return 0; };
  window.CSSStyleSheet.prototype.deleteRule = noop;
  window.CSSStyleSheet.prototype.addRule = function () { return -1; };
  window.CSSStyleSheet.prototype.removeRule = noop;
  // Lit feature-detects adoptedStyleSheets on Document.prototype (and ShadowRoot
  // wrappers accept expandos already). Sheets assigned here are simply never
  // rendered — there is no CSS cascade.
  if (window.Document && window.Document.prototype && !('adoptedStyleSheets' in window.Document.prototype)) {
    window.Document.prototype.adoptedStyleSheets = [];
  }
  if (typeof document !== 'undefined' && document.styleSheets === undefined) {
    document.styleSheets = { length: 0, item: function () { return null; } };
  }

  // ---- Intl (crash-avoidance shim; goja has no native Intl) ----
  //
  // en-US-flavored output, deliberately NOT spec-compliant: the goal is that
  // i18n-heavy bundles constructing formatters at module scope keep running and
  // produce readable text, not locale fidelity.

  if (typeof Intl === 'undefined') {
    var MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
    var DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

    var DateTimeFormat = function (locales, opts) { this.__o = opts || {}; };
    DateTimeFormat.prototype.format = function (d) {
      d = d === undefined ? new Date() : new Date(d);
      if (isNaN(d.getTime())) return 'Invalid Date';
      var o = this.__o;
      var wantsDate = o.year !== undefined || o.month !== undefined || o.day !== undefined || o.weekday !== undefined || o.dateStyle !== undefined;
      var wantsTime = o.hour !== undefined || o.minute !== undefined || o.second !== undefined || o.timeStyle !== undefined;
      if (!wantsDate && !wantsTime) wantsDate = true;
      var parts = [];
      if (wantsDate) {
        var long = o.dateStyle === 'full' || o.dateStyle === 'long' || o.month === 'long';
        var med = o.dateStyle === 'medium' || o.month === 'short';
        var ds;
        if (long) ds = MONTHS[d.getMonth()] + ' ' + d.getDate() + ', ' + d.getFullYear();
        else if (med) ds = MONTHS[d.getMonth()].slice(0, 3) + ' ' + d.getDate() + ', ' + d.getFullYear();
        else ds = (d.getMonth() + 1) + '/' + d.getDate() + '/' + d.getFullYear();
        if (o.weekday !== undefined || o.dateStyle === 'full') {
          var wd = DAYS[d.getDay()];
          if (o.weekday === 'short') wd = wd.slice(0, 3);
          ds = wd + ', ' + ds;
        }
        parts.push(ds);
      }
      if (wantsTime) {
        var h = d.getHours(), ampm = h < 12 ? 'AM' : 'PM';
        var h12 = h % 12 === 0 ? 12 : h % 12;
        var pad = function (n) { return n < 10 ? '0' + n : String(n); };
        var ts = h12 + ':' + pad(d.getMinutes());
        if (o.second !== undefined || o.timeStyle === 'medium' || o.timeStyle === 'long' || o.timeStyle === 'full') ts += ':' + pad(d.getSeconds());
        parts.push(ts + ' ' + ampm);
      }
      return parts.join(', ');
    };
    DateTimeFormat.prototype.formatToParts = function (d) { return [{ type: 'literal', value: this.format(d) }]; };
    DateTimeFormat.prototype.resolvedOptions = function () {
      var out = { locale: 'en-US', calendar: 'gregory', numberingSystem: 'latn', timeZone: 'UTC' };
      for (var k in this.__o) if (Object.prototype.hasOwnProperty.call(this.__o, k)) out[k] = this.__o[k];
      return out;
    };
    DateTimeFormat.prototype.formatRange = function (a, b) { return this.format(a) + ' – ' + this.format(b); };
    DateTimeFormat.supportedLocalesOf = function () { return ['en-US']; };

    var NumberFormat = function (locales, opts) { this.__o = opts || {}; };
    NumberFormat.prototype.format = function (n) {
      n = Number(n);
      if (!isFinite(n)) return String(n);
      var o = this.__o;
      var isPct = o.style === 'percent';
      var isCur = o.style === 'currency';
      if (isPct) n *= 100;
      var min = o.minimumFractionDigits != null ? Number(o.minimumFractionDigits) : (isCur ? 2 : 0);
      var max = o.maximumFractionDigits != null ? Number(o.maximumFractionDigits) : Math.max(min, isCur ? 2 : (isPct ? 0 : 3));
      var neg = n < 0;
      var fixed = Math.abs(n).toFixed(max);
      // trim trailing zeros down to min fraction digits
      if (max > min) {
        var dotAt = fixed.indexOf('.');
        if (dotAt >= 0) {
          var need = dotAt + (min > 0 ? min + 1 : 0);
          var endAt = fixed.length;
          while (endAt > need && (fixed.charAt(endAt - 1) === '0')) endAt--;
          if (fixed.charAt(endAt - 1) === '.') endAt--;
          fixed = fixed.slice(0, endAt);
        }
      }
      var pieces = fixed.split('.');
      if (o.useGrouping !== false && pieces[0].length > 3) {
        // manual grouping: goja's global replace mishandles zero-width
        // lookahead matches (doubles the separator)
        var grouped = '', count = 0;
        for (var gi = pieces[0].length - 1; gi >= 0; gi--) {
          grouped = pieces[0].charAt(gi) + grouped;
          count++;
          if (count % 3 === 0 && gi > 0) grouped = ',' + grouped;
        }
        pieces[0] = grouped;
      }
      var s = pieces.join('.');
      if (isPct) return (neg ? '-' : '') + s + '%';
      if (isCur) {
        var cur = String(o.currency || 'USD').toUpperCase();
        var sym = cur === 'USD' ? '$' : (cur === 'EUR' ? '€' : (cur === 'GBP' ? '£' : (cur === 'JPY' ? '¥' : cur + ' ')));
        return (neg ? '-' : '') + sym + s;
      }
      return (neg ? '-' : '') + s;
    };
    NumberFormat.prototype.formatToParts = function (n) { return [{ type: 'literal', value: this.format(n) }]; };
    NumberFormat.prototype.resolvedOptions = function () {
      var out = { locale: 'en-US', numberingSystem: 'latn', style: this.__o.style || 'decimal' };
      for (var k in this.__o) if (Object.prototype.hasOwnProperty.call(this.__o, k)) out[k] = this.__o[k];
      return out;
    };
    NumberFormat.supportedLocalesOf = function () { return ['en-US']; };

    var Collator = function (locales, opts) { this.__o = opts || {}; };
    Collator.prototype.compare = function (a, b) { a = String(a); b = String(b); return a < b ? -1 : (a > b ? 1 : 0); };
    Collator.prototype.resolvedOptions = function () { return { locale: 'en-US', usage: this.__o.usage || 'sort', sensitivity: 'variant' }; };
    Collator.supportedLocalesOf = function () { return ['en-US']; };

    var PluralRules = function (locales, opts) { this.__o = opts || {}; };
    PluralRules.prototype.select = function (n) {
      if (this.__o.type === 'ordinal') {
        n = Math.abs(Number(n));
        var mod10 = n % 10, mod100 = n % 100;
        if (mod10 === 1 && mod100 !== 11) return 'one';
        if (mod10 === 2 && mod100 !== 12) return 'two';
        if (mod10 === 3 && mod100 !== 13) return 'few';
        return 'other';
      }
      return Number(n) === 1 ? 'one' : 'other';
    };
    PluralRules.prototype.resolvedOptions = function () { return { locale: 'en-US', type: this.__o.type || 'cardinal', pluralCategories: ['one', 'other'] }; };
    PluralRules.supportedLocalesOf = function () { return ['en-US']; };

    var RelativeTimeFormat = function (locales, opts) { this.__o = opts || {}; };
    RelativeTimeFormat.prototype.format = function (value, unit) {
      var v = Number(value);
      unit = String(unit).replace(/s$/, '');
      var abs = Math.abs(v);
      var u = abs === 1 ? unit : unit + 's';
      if (v < 0) return abs + ' ' + u + ' ago';
      if (v > 0) return 'in ' + abs + ' ' + u;
      return 'this ' + unit;
    };
    RelativeTimeFormat.prototype.formatToParts = function (value, unit) { return [{ type: 'literal', value: this.format(value, unit) }]; };
    RelativeTimeFormat.prototype.resolvedOptions = function () { return { locale: 'en-US', numeric: this.__o.numeric || 'always', style: this.__o.style || 'long' }; };
    RelativeTimeFormat.supportedLocalesOf = function () { return ['en-US']; };

    window.Intl = {
      DateTimeFormat: DateTimeFormat,
      NumberFormat: NumberFormat,
      Collator: Collator,
      PluralRules: PluralRules,
      RelativeTimeFormat: RelativeTimeFormat,
      getCanonicalLocales: function (l) {
        if (l == null) return [];
        if (typeof l === 'string') return [l];
        return Array.prototype.slice.call(l).map(String);
      }
    };
  }

  // ---- legacy document.createEvent ----

  if (typeof document !== 'undefined' && typeof document.createEvent !== 'function') {
    document.createEvent = function (iface) {
      iface = String(iface || 'Event');
      var ev;
      if (/mouse/i.test(iface)) ev = new MouseEvent('');
      else if (/custom/i.test(iface)) ev = new CustomEvent('');
      else ev = new Event('');
      ev.initEvent = function (type, bubbles, cancelable) {
        ev.type = type; ev.bubbles = !!bubbles; ev.cancelable = !!cancelable;
      };
      ev.initCustomEvent = function (type, bubbles, cancelable, detail) {
        ev.initEvent(type, bubbles, cancelable); ev.detail = detail;
      };
      ev.initMouseEvent = function (type, bubbles, cancelable) {
        ev.initEvent(type, bubbles, cancelable);
      };
      return ev;
    };
  }
})();
`

// preludeAPIProgram is preludeAPIJS compiled once at init, shared across
// runtimes exactly like preludeProgram (a goja.Program is immutable).
var preludeAPIProgram = goja.MustCompile("prelude_api.js", preludeAPIJS, false)
