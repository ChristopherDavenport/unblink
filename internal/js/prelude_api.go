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
      if (cp >= 0xD800 && cp <= 0xDFFF) { out.push(0xEF, 0xBF, 0xBD); continue; } // lone surrogate -> U+FFFD (not WTF-8)
      if (cp > 0xFFFF) i++; // surrogate pair consumed two units
      if (cp < 0x80) out.push(cp);
      else if (cp < 0x800) out.push(0xC0 | (cp >> 6), 0x80 | (cp & 63));
      else if (cp < 0x10000) out.push(0xE0 | (cp >> 12), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
      else out.push(0xF0 | (cp >> 18), 0x80 | ((cp >> 12) & 63), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
    }
    return new Uint8Array(out);
  }

  // utf8Decode substitutes U+FFFD on malformed input; when fatal is true it instead
  // throws a TypeError at the first malformed sequence (WHATWG fatal decoder).
  function utf8Decode(bytes, fatal) {
    var out = '', i = 0, n = bytes.length;
    function cont(j) { return j < n && (bytes[j] & 0xC0) === 0x80; }
    function bad() { if (fatal) throw new TypeError('The encoded data was not valid for encoding utf-8.'); out += '�'; }
    while (i < n) {
      var b0 = bytes[i], cp;
      if (b0 < 0x80) { out += String.fromCharCode(b0); i++; continue; }
      if (b0 < 0xC2) { bad(); i++; continue; } // continuation or overlong lead
      if (b0 < 0xE0) {
        if (!cont(i + 1)) { bad(); i++; continue; }
        cp = ((b0 & 31) << 6) | (bytes[i + 1] & 63); i += 2;
      } else if (b0 < 0xF0) {
        if (!cont(i + 1) || !cont(i + 2)) { bad(); i++; continue; }
        cp = ((b0 & 15) << 12) | ((bytes[i + 1] & 63) << 6) | (bytes[i + 2] & 63); i += 3;
        if (cp < 0x800 || (cp >= 0xD800 && cp <= 0xDFFF)) { bad(); continue; }
      } else if (b0 < 0xF5) {
        if (!cont(i + 1) || !cont(i + 2) || !cont(i + 3)) { bad(); i++; continue; }
        cp = ((b0 & 7) << 18) | ((bytes[i + 1] & 63) << 12) | ((bytes[i + 2] & 63) << 6) | (bytes[i + 3] & 63); i += 4;
        if (cp < 0x10000 || cp > 0x10FFFF) { bad(); continue; }
      } else { bad(); i++; continue; }
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
  // decoderInit validates+canonicalizes the label (RangeError on an invalid one)
  // and reads the {fatal, ignoreBOM} options — shared by TextDecoder and
  // TextDecoderStream. Note bytes are still decoded as UTF-8 (legacy codecs are a
  // non-goal); only .encoding, validation, fatal, and ignoreBOM are honored.
  function decoderInit(target, label, options) {
    var name = __unblinkEncodingName(label === undefined ? 'utf-8' : String(label));
    if (name === '') throw new RangeError("Failed to construct 'TextDecoder': The encoding label provided ('" + label + "') is invalid.");
    target.encoding = name;
    options = options || {};
    target.fatal = Boolean(options.fatal);
    target.ignoreBOM = Boolean(options.ignoreBOM);
  }
  // stripBOM removes a single leading U+FEFF from decoded output unless ignoreBOM.
  function decodeUTF8(bytes, fatal, ignoreBOM) {
    var s = utf8Decode(bytes, fatal);
    if (!ignoreBOM && s.charCodeAt(0) === 0xFEFF) s = s.slice(1);
    return s;
  }
  if (typeof window.TextDecoder === 'undefined') {
    // UTF-8 only: unblink normalizes page bytes to UTF-8 before JS ever runs.
    window.TextDecoder = function (label, options) { decoderInit(this, label, options); };
    window.TextDecoder.prototype.decode = function (input) { return decodeUTF8(toU8(input), this.fatal, this.ignoreBOM); };
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

  // ---- FileReader (reads the Blob/File __bytes plumbing above) ----
  //
  // Async completions fire through the WRAPPED setTimeout so the settle audit
  // sees the pending work — a native timer would let the render close early.
  if (typeof window.FileReader === 'undefined') {
    var FR = function () {
      this.readyState = 0; this.result = null; this.error = null;
      this.onload = null; this.onloadend = null; this.onloadstart = null;
      this.onprogress = null; this.onerror = null; this.onabort = null;
      this.__l = {}; this.__gen = 0;
    };
    FR.EMPTY = 0; FR.LOADING = 1; FR.DONE = 2;
    FR.prototype.EMPTY = 0; FR.prototype.LOADING = 1; FR.prototype.DONE = 2;
    FR.prototype.addEventListener = function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); };
    FR.prototype.removeEventListener = function (t, f) { var a = this.__l[t]; if (a) { var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); } };
    FR.prototype.dispatchEvent = function () { return true; };
    FR.prototype.__fire = function (type) {
      var ev = { type: type, target: this, currentTarget: this };
      var h = this['on' + type];
      if (typeof h === 'function') { try { h.call(this, ev); } catch (e) {} }
      (this.__l[type] || []).slice().forEach(function (f) { try { f.call(this, ev); } catch (e) {} }, this);
    };
    FR.prototype.__read = function (blob, produce) {
      var self = this;
      self.readyState = 1;
      self.__gen++;
      var gen = self.__gen;
      setTimeout(function () {
        if (self.__gen !== gen) return; // aborted or superseded
        self.__fire('loadstart');
        try {
          var bytes = (blob && blob.__bytes) ? blob.__bytes : new Uint8Array(0);
          self.result = produce(bytes, blob);
          self.readyState = 2;
          self.__fire('progress');
          self.__fire('load');
        } catch (e) {
          self.error = e; self.result = null; self.readyState = 2; self.__fire('error');
        }
        self.__fire('loadend');
      }, 0);
    };
    FR.prototype.readAsText = function (blob) { this.__read(blob, function (b) { return utf8Decode(b); }); };
    FR.prototype.readAsArrayBuffer = function (blob) { this.__read(blob, function (b) { return bytesToArrayBuffer(b); }); };
    FR.prototype.readAsBinaryString = function (blob) { this.__read(blob, function (b) { var s = ''; for (var i = 0; i < b.length; i++) s += String.fromCharCode(b[i]); return s; }); };
    FR.prototype.readAsDataURL = function (blob) {
      this.__read(blob, function (b, bl) {
        var s = ''; for (var i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
        return 'data:' + ((bl && bl.type) || 'application/octet-stream') + ';base64,' + window.btoa(s);
      });
    };
    FR.prototype.abort = function () {
      this.__gen++;
      if (this.readyState === 1) { this.readyState = 2; this.__fire('abort'); this.__fire('loadend'); }
    };
    window.FileReader = FR;
  }

  // ---- Streams (buffer-backed, eager; no lazy backpressure) ----
  //
  // read() resolves from an in-memory queue via a microtask (Promise), which the
  // settle poll drains before it can observe idle — no timer/keepalive needed.
  // A future *lazy* (network-backed) pull would have to route through the wrapped
  // timer instead, or it would break settle correctness.
  if (typeof window.ReadableStream === 'undefined') {
    var RS = function (source) {
      var self = this;
      this.locked = false;
      this.__q = [];
      this.__closed = false;
      this.__err = null;
      var controller = {
        enqueue: function (chunk) { if (!self.__closed) self.__q.push(chunk); },
        close: function () { self.__closed = true; },
        error: function (e) { self.__err = e; self.__closed = true; },
        get desiredSize() { return 1; }
      };
      source = source || {};
      this.__source = source;
      this.__controller = controller;
      try { if (typeof source.start === 'function') source.start(controller); } catch (e) { this.__err = e; this.__closed = true; }
    };
    RS.prototype.getReader = function () {
      var self = this;
      if (self.locked) throw new TypeError('ReadableStream is locked');
      self.locked = true;
      var pulled = false;
      function maybePull() {
        if (!pulled && self.__q.length === 0 && !self.__closed && typeof self.__source.pull === 'function') {
          pulled = true;
          try { self.__source.pull(self.__controller); } catch (e) { self.__err = e; self.__closed = true; }
        }
      }
      return {
        read: function () {
          maybePull();
          if (self.__err) return Promise.reject(self.__err);
          if (self.__q.length > 0) return Promise.resolve({ value: self.__q.shift(), done: false });
          return Promise.resolve({ value: undefined, done: true });
        },
        cancel: function () { self.__closed = true; self.__q = []; return Promise.resolve(); },
        releaseLock: function () { self.locked = false; },
        closed: Promise.resolve()
      };
    };
    RS.prototype.cancel = function () { this.__closed = true; this.__q = []; return Promise.resolve(); };
    RS.prototype.tee = function () {
      var mk = function (arr) { return new window.ReadableStream({ start: function (c) { for (var i = 0; i < arr.length; i++) c.enqueue(arr[i]); c.close(); } }); };
      return [mk(this.__q.slice()), mk(this.__q.slice())];
    };
    RS.prototype.pipeTo = function (dest) {
      var reader = this.getReader();
      var writer = dest && dest.getWriter ? dest.getWriter() : null;
      function pump() {
        return reader.read().then(function (r) {
          if (r.done) { if (writer) writer.close(); return; }
          if (writer) writer.write(r.value);
          return pump();
        });
      }
      return pump();
    };
    RS.prototype.pipeThrough = function (pair) { this.pipeTo(pair.writable); return pair.readable; };
    if (typeof Symbol === 'function' && Symbol.asyncIterator) {
      RS.prototype[Symbol.asyncIterator] = function () { var reader = this.getReader(); return { next: function () { return reader.read(); }, 'return': function () { reader.releaseLock(); return Promise.resolve({ done: true }); } }; };
    }
    window.ReadableStream = RS;
  }
  if (typeof window.WritableStream === 'undefined') {
    var WSt = function (sink) {
      this.locked = false;
      sink = sink || {};
      var controller = { error: noop };
      try { if (typeof sink.start === 'function') sink.start(controller); } catch (e) {}
      this.__sink = sink; this.__controller = controller;
    };
    WSt.prototype.getWriter = function () {
      var self = this;
      if (self.locked) throw new TypeError('WritableStream is locked');
      self.locked = true;
      return {
        write: function (chunk) { try { if (typeof self.__sink.write === 'function') return Promise.resolve(self.__sink.write(chunk, self.__controller)); } catch (e) { return Promise.reject(e); } return Promise.resolve(); },
        close: function () { try { if (typeof self.__sink.close === 'function') self.__sink.close(); } catch (e) {} return Promise.resolve(); },
        abort: function () { try { if (typeof self.__sink.abort === 'function') self.__sink.abort(); } catch (e) {} return Promise.resolve(); },
        releaseLock: function () { self.locked = false; },
        closed: Promise.resolve(), ready: Promise.resolve(), desiredSize: 1
      };
    };
    WSt.prototype.abort = function () { return Promise.resolve(); };
    WSt.prototype.close = function () { return Promise.resolve(); };
    window.WritableStream = WSt;
  }
  if (typeof window.TransformStream === 'undefined') {
    var TS = function (transformer) {
      transformer = transformer || {};
      var readable = new window.ReadableStream({ start: noop });
      var controller = {
        enqueue: function (c) { readable.__q.push(c); },
        terminate: function () { readable.__closed = true; },
        error: function (e) { readable.__err = e; readable.__closed = true; }
      };
      var writable = new window.WritableStream({
        write: function (chunk) {
          if (typeof transformer.transform === 'function') { try { transformer.transform(chunk, controller); } catch (e) { controller.error(e); } }
          else readable.__q.push(chunk);
        },
        close: function () {
          if (typeof transformer.flush === 'function') { try { transformer.flush(controller); } catch (e) {} }
          readable.__closed = true;
        }
      });
      try { if (typeof transformer.start === 'function') transformer.start(controller); } catch (e) {}
      this.readable = readable;
      this.writable = writable;
    };
    window.TransformStream = TS;
  }
  if (typeof window.CountQueuingStrategy === 'undefined') {
    window.CountQueuingStrategy = function (opts) { this.highWaterMark = opts ? opts.highWaterMark : 1; };
    window.CountQueuingStrategy.prototype.size = function () { return 1; };
  }
  if (typeof window.ByteLengthQueuingStrategy === 'undefined') {
    window.ByteLengthQueuingStrategy = function (opts) { this.highWaterMark = opts ? opts.highWaterMark : 1; };
    window.ByteLengthQueuingStrategy.prototype.size = function (chunk) { return chunk && chunk.byteLength ? chunk.byteLength : 0; };
  }

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
      var core = __unblinkFetch(req.method, String(reqURL), headersToPlain(headers), ser.data, req.mode, req.credentials).then(function (r) {
        var resp = new window.Response(null, { status: r.status, headers: r.headers, url: r.url });
        resp.type = r.type || 'basic';
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
        __unblinkFetch(self._method, self._url, self._headers, ser.data, 'cors', self.withCredentials ? 'include' : 'same-origin')
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

  // Cross-origin isolation fidelity (window === self === globalThis, so one
  // assignment covers all aliases). Resolved Go-side from the document URL + its
  // COOP/COEP response headers; SharedArrayBuffer stays absent regardless.
  window.isSecureContext = !!(typeof __unblinkSecureContext !== 'undefined' && __unblinkSecureContext);
  window.crossOriginIsolated = !!(typeof __unblinkCrossOriginIsolated !== 'undefined' && __unblinkCrossOriginIsolated);

  // ---- navigator device / permission stubs ----
  //
  // Feature-detected surfaces whose ABSENCE throws when an app reads them at
  // boot; the content never depends on them working. Promises reject/deny (or
  // resolve empty) so hydration proceeds. serviceWorker.ready RESOLVES and is
  // never left pending -- "await navigator.serviceWorker.ready" would otherwise
  // hang the render until the settle timeout.

  if (typeof navigator !== 'undefined') {
    var swOrigin = (typeof location !== 'undefined' && location.origin) ? location.origin : '';
    if (!navigator.clipboard) {
      navigator.clipboard = {
        readText: function () { return Promise.resolve(''); },
        read: function () { return Promise.resolve([]); },
        writeText: function () { return Promise.resolve(); },
        write: function () { return Promise.resolve(); },
        addEventListener: noop, removeEventListener: noop
      };
    }
    if (!navigator.permissions) {
      navigator.permissions = {
        query: function () {
          return Promise.resolve({ state: 'denied', name: '', onchange: null, addEventListener: noop, removeEventListener: noop });
        }
      };
    }
    if (!navigator.geolocation) {
      navigator.geolocation = {
        getCurrentPosition: function (ok, err) {
          // POSITION_UNAVAILABLE, delivered async through the wrapped setTimeout.
          if (typeof err === 'function') setTimeout(function () {
            err({ code: 2, message: 'position unavailable', PERMISSION_DENIED: 1, POSITION_UNAVAILABLE: 2, TIMEOUT: 3 });
          }, 0);
        },
        watchPosition: function () { return 0; },
        clearWatch: noop
      };
    }
    if (!navigator.mediaDevices) {
      var notAllowed = function () { var e = new Error('Permission denied'); e.name = 'NotAllowedError'; return Promise.reject(e); };
      navigator.mediaDevices = {
        getUserMedia: notAllowed,
        getDisplayMedia: notAllowed,
        enumerateDevices: function () { return Promise.resolve([]); },
        getSupportedConstraints: function () { return {}; },
        addEventListener: noop, removeEventListener: noop
      };
    }
    if (!navigator.serviceWorker) {
      var swReg = {
        scope: swOrigin + '/', active: null, installing: null, waiting: null, updateViaCache: 'imports',
        update: function () { return Promise.resolve(); },
        unregister: function () { return Promise.resolve(true); },
        // Background Sync / Periodic Sync / Push / Notifications — all inert.
        sync: { register: function () { return Promise.resolve(); }, getTags: function () { return Promise.resolve([]); } },
        periodicSync: { register: function () { return Promise.resolve(); }, unregister: function () { return Promise.resolve(); }, getTags: function () { return Promise.resolve([]); } },
        pushManager: { subscribe: function () { return Promise.resolve(null); }, getSubscription: function () { return Promise.resolve(null); }, permissionState: function () { return Promise.resolve('denied'); } },
        navigationPreload: { enable: function () { return Promise.resolve(); }, disable: function () { return Promise.resolve(); }, setHeaderValue: function () { return Promise.resolve(); }, getState: function () { return Promise.resolve({ enabled: false, headerValue: '' }); } },
        showNotification: function () { return Promise.resolve(); },
        getNotifications: function () { return Promise.resolve([]); },
        addEventListener: noop, removeEventListener: noop
      };
      navigator.serviceWorker = {
        controller: null,
        ready: Promise.resolve(swReg),
        register: function () { return Promise.resolve(swReg); },
        getRegistration: function () { return Promise.resolve(swReg); },
        getRegistrations: function () { return Promise.resolve([swReg]); },
        startMessages: noop,
        addEventListener: noop, removeEventListener: noop
      };
    }
  }

  // ---- EventSource (SSE): connection-less stub ----
  //
  // The transport is request/response, not streaming, so a live event stream is
  // out of scope here. Constructing one must not throw — SSR-first pages that
  // layer live updates on top keep rendering. It reports a single error -> CLOSED
  // (via the wrapped setTimeout, so the settle audit still sees the work) and
  // does not auto-reconnect, mirroring the WebSocket stub in globals.go.

  if (typeof window.EventSource === 'undefined') {
    var ES = function (url, opts) {
      var self = this;
      this.url = String(url || '');
      this.withCredentials = !!(opts && opts.withCredentials);
      this.readyState = ES.CONNECTING;
      this.onopen = null; this.onmessage = null; this.onerror = null;
      this.__l = {};
      setTimeout(function () {
        self.readyState = ES.CLOSED;
        var err = { type: 'error', target: self };
        if (typeof self.onerror === 'function') { try { self.onerror(err); } catch (e) {} }
        (self.__l.error || []).slice().forEach(function (f) { try { f.call(self, err); } catch (e) {} });
      }, 0);
    };
    ES.CONNECTING = 0; ES.OPEN = 1; ES.CLOSED = 2;
    ES.prototype.CONNECTING = 0; ES.prototype.OPEN = 1; ES.prototype.CLOSED = 2;
    ES.prototype.close = function () { this.readyState = ES.CLOSED; };
    ES.prototype.addEventListener = function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); };
    ES.prototype.removeEventListener = function (t, f) { var a = this.__l[t]; if (a) { var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); } };
    ES.prototype.dispatchEvent = function () { return true; };
    window.EventSource = ES;
  }

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

  // ---- CSS namespace ----
  //
  // CSS.escape is a pure function (the WHATWG serialize-an-identifier algorithm),
  // so it is implemented for real — selector and CSS-in-JS libraries call it at
  // import time and would ReferenceError without it. CSS.supports is optimistic
  // (true for well-formed input): there is no CSSOM to consult, and returning true
  // keeps feature-gated code on its main path instead of loading a visual polyfill.
  if (typeof window.CSS === 'undefined') {
    window.CSS = {
      escape: function (value) {
        var str = String(value);
        var out = '';
        var len = str.length;
        var first = str.charCodeAt(0);
        for (var i = 0; i < len; i++) {
          var c = str.charCodeAt(i);
          if (c === 0) { out += '�'; continue; }
          if ((c >= 0x1 && c <= 0x1f) || c === 0x7f ||
              (i === 0 && c >= 0x30 && c <= 0x39) ||
              (i === 1 && c >= 0x30 && c <= 0x39 && first === 0x2d)) {
            out += '\\' + c.toString(16) + ' ';
            continue;
          }
          if (i === 0 && c === 0x2d && len === 1) { out += '\\' + str.charAt(i); continue; }
          if (c >= 0x80 || c === 0x2d || c === 0x5f ||
              (c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a)) {
            out += str.charAt(i);
            continue;
          }
          out += '\\' + str.charAt(i);
        }
        return out;
      },
      supports: function (prop, value) {
        // supports(property, value) or supports(conditionText); optimistic.
        return arguments.length >= 1 && String(prop).length > 0;
      }
    };
  }

  // ---- canvas getContext stub (no pixels; keeps canvas libs from throwing) ----
  //
  // Wrapped elements share one HTMLElement prototype (per protoFor), so the
  // per-interface HTMLCanvasElement.prototype is NOT in an instance's chain --
  // getContext must land on the shared prototype (reached via getPrototypeOf of
  // a throwaway element), guarded by tagName. There is no rasterizer: the 2D
  // context accepts every call and draws nothing, so a canvas charting or
  // fingerprinting library runs to completion and the surrounding DOM content
  // still renders. A truthy getContext also satisfies the common
  // !!canvas.getContext feature test. webgl/webgpu stay null (permanent non-goal).
  (function () {
    var htmlProto = document.createElement ? Object.getPrototypeOf(document.createElement('span')) : null;
    if (htmlProto && !('getContext' in htmlProto)) {
      var inert2d = function (canvas) {
        return {
          canvas: canvas,
          save: noop, restore: noop, scale: noop, rotate: noop, translate: noop, transform: noop, setTransform: noop, resetTransform: noop,
          getTransform: function () { return { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 }; },
          beginPath: noop, closePath: noop, moveTo: noop, lineTo: noop, bezierCurveTo: noop, quadraticCurveTo: noop, arc: noop, arcTo: noop, ellipse: noop, rect: noop, roundRect: noop,
          fill: noop, stroke: noop, clip: noop, isPointInPath: function () { return false; }, isPointInStroke: function () { return false; },
          fillRect: noop, strokeRect: noop, clearRect: noop, fillText: noop, strokeText: noop,
          measureText: function () { return { width: 0, actualBoundingBoxAscent: 0, actualBoundingBoxDescent: 0, actualBoundingBoxLeft: 0, actualBoundingBoxRight: 0, fontBoundingBoxAscent: 0, fontBoundingBoxDescent: 0 }; },
          drawImage: noop, putImageData: noop,
          createImageData: function () { return { data: new Uint8ClampedArray(0), width: 0, height: 0 }; },
          getImageData: function () { return { data: new Uint8ClampedArray(0), width: 0, height: 0 }; },
          createLinearGradient: function () { return { addColorStop: noop }; },
          createRadialGradient: function () { return { addColorStop: noop }; },
          createConicGradient: function () { return { addColorStop: noop }; },
          createPattern: function () { return null; },
          setLineDash: noop, getLineDash: function () { return []; }, drawFocusIfNeeded: noop, scrollPathIntoView: noop,
          fillStyle: '#000000', strokeStyle: '#000000', lineWidth: 1, lineCap: 'butt', lineJoin: 'miter', miterLimit: 10, lineDashOffset: 0,
          font: '10px sans-serif', textAlign: 'start', textBaseline: 'alphabetic', direction: 'ltr',
          globalAlpha: 1, globalCompositeOperation: 'source-over', imageSmoothingEnabled: true,
          shadowBlur: 0, shadowColor: 'rgba(0, 0, 0, 0)', shadowOffsetX: 0, shadowOffsetY: 0
        };
      };
      htmlProto.getContext = function (kind) {
        if (String(this.tagName).toUpperCase() !== 'CANVAS') return null;
        kind = String(kind || '2d').toLowerCase();
        return (kind === '2d' || kind === 'bitmaprenderer') ? inert2d(this) : null;
      };
      htmlProto.toDataURL = function () { return 'data:,'; };
      htmlProto.toBlob = function (cb) { if (typeof cb === 'function') setTimeout(function () { cb(new window.Blob([])); }, 0); };
      htmlProto.transferControlToOffscreen = function () { return null; };
    }
  })();

  // ---- geometry: DOMRect / DOMPoint / DOMMatrix / DOMQuad / Path2D ----
  //
  // Real math (no layout dependency): transform libraries do "new DOMMatrix()"
  // and compose/transform points during setup. Path2D records its ops and is
  // consumed as a no-op by the canvas 2D stub above.
  if (typeof window.DOMRectReadOnly === 'undefined') {
    var DRRO = function (x, y, w, h) { this.x = +x || 0; this.y = +y || 0; this.width = +w || 0; this.height = +h || 0; };
    Object.defineProperties(DRRO.prototype, {
      top: { get: function () { return Math.min(this.y, this.y + this.height); }, configurable: true },
      bottom: { get: function () { return Math.max(this.y, this.y + this.height); }, configurable: true },
      left: { get: function () { return Math.min(this.x, this.x + this.width); }, configurable: true },
      right: { get: function () { return Math.max(this.x, this.x + this.width); }, configurable: true }
    });
    DRRO.prototype.toJSON = function () { return { x: this.x, y: this.y, width: this.width, height: this.height, top: this.top, right: this.right, bottom: this.bottom, left: this.left }; };
    DRRO.fromRect = function (o) { o = o || {}; return new DRRO(o.x, o.y, o.width, o.height); };
    window.DOMRectReadOnly = DRRO;
    var DR = function (x, y, w, h) { DRRO.call(this, x, y, w, h); };
    DR.prototype = Object.create(DRRO.prototype); DR.prototype.constructor = DR;
    DR.fromRect = function (o) { o = o || {}; return new DR(o.x, o.y, o.width, o.height); };
    window.DOMRect = DR;
  }
  if (typeof window.DOMPointReadOnly === 'undefined') {
    var DPRO = function (x, y, z, w) { this.x = +x || 0; this.y = +y || 0; this.z = +z || 0; this.w = (w === undefined) ? 1 : (+w || 0); };
    DPRO.prototype.toJSON = function () { return { x: this.x, y: this.y, z: this.z, w: this.w }; };
    DPRO.prototype.matrixTransform = function (m) { return (m && m.transformPoint) ? m.transformPoint(this) : new window.DOMPoint(this.x, this.y, this.z, this.w); };
    DPRO.fromPoint = function (o) { o = o || {}; return new DPRO(o.x, o.y, o.z, o.w); };
    window.DOMPointReadOnly = DPRO;
    var DP = function (x, y, z, w) { DPRO.call(this, x, y, z, w); };
    DP.prototype = Object.create(DPRO.prototype); DP.prototype.constructor = DP;
    DP.fromPoint = function (o) { o = o || {}; return new DP(o.x, o.y, o.z, o.w); };
    window.DOMPoint = DP;
  }
  if (typeof window.DOMMatrixReadOnly === 'undefined') {
    // 2D affine matrix [a c e / b d f / 0 0 1]; 3D is reported as 2D identity extras.
    var matMul = function (m, n) {
      return [m.a * n.a + m.c * n.b, m.b * n.a + m.d * n.b, m.a * n.c + m.c * n.d,
              m.b * n.c + m.d * n.d, m.a * n.e + m.c * n.f + m.e, m.b * n.e + m.d * n.f + m.f];
    };
    var DMRO = function (init) {
      this.a = 1; this.b = 0; this.c = 0; this.d = 1; this.e = 0; this.f = 0;
      if (init && init.length >= 6) { this.a = init[0]; this.b = init[1]; this.c = init[2]; this.d = init[3]; this.e = init[4]; this.f = init[5]; }
      else if (init && typeof init === 'object' && 'a' in init) { this.a = init.a; this.b = init.b; this.c = init.c; this.d = init.d; this.e = init.e; this.f = init.f; }
    };
    Object.defineProperties(DMRO.prototype, {
      m11: { get: function () { return this.a; }, configurable: true }, m12: { get: function () { return this.b; }, configurable: true },
      m21: { get: function () { return this.c; }, configurable: true }, m22: { get: function () { return this.d; }, configurable: true },
      m41: { get: function () { return this.e; }, configurable: true }, m42: { get: function () { return this.f; }, configurable: true },
      m13: { get: function () { return 0; }, configurable: true }, m14: { get: function () { return 0; }, configurable: true },
      m23: { get: function () { return 0; }, configurable: true }, m24: { get: function () { return 0; }, configurable: true },
      m31: { get: function () { return 0; }, configurable: true }, m32: { get: function () { return 0; }, configurable: true },
      m33: { get: function () { return 1; }, configurable: true }, m34: { get: function () { return 0; }, configurable: true },
      m43: { get: function () { return 0; }, configurable: true }, m44: { get: function () { return 1; }, configurable: true },
      is2D: { get: function () { return true; }, configurable: true },
      isIdentity: { get: function () { return this.a === 1 && this.b === 0 && this.c === 0 && this.d === 1 && this.e === 0 && this.f === 0; }, configurable: true }
    });
    var newDM = function (r) { var m = new window.DOMMatrix(); m.a = r[0]; m.b = r[1]; m.c = r[2]; m.d = r[3]; m.e = r[4]; m.f = r[5]; return m; };
    DMRO.prototype.multiply = function (o) { return newDM(matMul(this, o)); };
    DMRO.prototype.translate = function (tx, ty) { return newDM(matMul(this, { a: 1, b: 0, c: 0, d: 1, e: +tx || 0, f: +ty || 0 })); };
    DMRO.prototype.scale = function (sx, sy) { sy = (sy === undefined) ? sx : sy; return newDM(matMul(this, { a: +sx || 0, b: 0, c: 0, d: +sy || 0, e: 0, f: 0 })); };
    DMRO.prototype.rotate = function (deg) { var r = (+deg || 0) * Math.PI / 180, cos = Math.cos(r), sin = Math.sin(r); return newDM(matMul(this, { a: cos, b: sin, c: -sin, d: cos, e: 0, f: 0 })); };
    DMRO.prototype.flipX = function () { return newDM(matMul(this, { a: -1, b: 0, c: 0, d: 1, e: 0, f: 0 })); };
    DMRO.prototype.flipY = function () { return newDM(matMul(this, { a: 1, b: 0, c: 0, d: -1, e: 0, f: 0 })); };
    DMRO.prototype.inverse = function () {
      var det = this.a * this.d - this.b * this.c;
      if (!det) return newDM([1, 0, 0, 1, 0, 0]);
      var ia = this.d / det, ib = -this.b / det, ic = -this.c / det, id = this.a / det;
      return newDM([ia, ib, ic, id, -(ia * this.e + ic * this.f), -(ib * this.e + id * this.f)]);
    };
    DMRO.prototype.transformPoint = function (p) {
      p = p || {}; var x = +p.x || 0, y = +p.y || 0;
      return new window.DOMPoint(this.a * x + this.c * y + this.e, this.b * x + this.d * y + this.f, (p.z !== undefined ? p.z : 0), (p.w !== undefined ? p.w : 1));
    };
    DMRO.prototype.toFloat32Array = function () { return new Float32Array([this.a, this.b, 0, 0, this.c, this.d, 0, 0, 0, 0, 1, 0, this.e, this.f, 0, 1]); };
    DMRO.prototype.toFloat64Array = function () { return new Float64Array([this.a, this.b, 0, 0, this.c, this.d, 0, 0, 0, 0, 1, 0, this.e, this.f, 0, 1]); };
    DMRO.prototype.toJSON = function () { return { a: this.a, b: this.b, c: this.c, d: this.d, e: this.e, f: this.f, is2D: true, isIdentity: this.isIdentity }; };
    DMRO.prototype.toString = function () { return 'matrix(' + [this.a, this.b, this.c, this.d, this.e, this.f].join(', ') + ')'; };
    DMRO.fromMatrix = function (o) { return new DMRO(o); };
    DMRO.fromFloat32Array = function (a) { return new DMRO(a); };
    DMRO.fromFloat64Array = function (a) { return new DMRO(a); };
    window.DOMMatrixReadOnly = DMRO;

    var DM = function (init) { DMRO.call(this, init); };
    DM.prototype = Object.create(DMRO.prototype); DM.prototype.constructor = DM;
    var applySelf = function (self, r) { self.a = r[0]; self.b = r[1]; self.c = r[2]; self.d = r[3]; self.e = r[4]; self.f = r[5]; return self; };
    DM.prototype.multiplySelf = function (o) { return applySelf(this, matMul(this, o)); };
    DM.prototype.preMultiplySelf = function (o) { return applySelf(this, matMul(o, this)); };
    DM.prototype.translateSelf = function (tx, ty) { return applySelf(this, matMul(this, { a: 1, b: 0, c: 0, d: 1, e: +tx || 0, f: +ty || 0 })); };
    DM.prototype.scaleSelf = function (sx, sy) { sy = (sy === undefined) ? sx : sy; return applySelf(this, matMul(this, { a: +sx || 0, b: 0, c: 0, d: +sy || 0, e: 0, f: 0 })); };
    DM.prototype.rotateSelf = function (deg) { var r = (+deg || 0) * Math.PI / 180, cos = Math.cos(r), sin = Math.sin(r); return applySelf(this, matMul(this, { a: cos, b: sin, c: -sin, d: cos, e: 0, f: 0 })); };
    DM.prototype.invertSelf = function () { var i = this.inverse(); return applySelf(this, [i.a, i.b, i.c, i.d, i.e, i.f]); };
    DM.prototype.setMatrixValue = function () { return this; };
    DM.fromMatrix = function (o) { return new DM(o); };
    DM.fromFloat32Array = function (a) { return new DM(a); };
    DM.fromFloat64Array = function (a) { return new DM(a); };
    window.DOMMatrix = DM;
    window.WebKitCSSMatrix = DM;
  }
  if (typeof window.DOMQuad === 'undefined') {
    window.DOMQuad = function (p1, p2, p3, p4) {
      this.p1 = p1 || new window.DOMPoint(); this.p2 = p2 || new window.DOMPoint();
      this.p3 = p3 || new window.DOMPoint(); this.p4 = p4 || new window.DOMPoint();
    };
    window.DOMQuad.prototype.getBounds = function () {
      var xs = [this.p1.x, this.p2.x, this.p3.x, this.p4.x], ys = [this.p1.y, this.p2.y, this.p3.y, this.p4.y];
      var minx = Math.min.apply(null, xs), miny = Math.min.apply(null, ys);
      return new window.DOMRect(minx, miny, Math.max.apply(null, xs) - minx, Math.max.apply(null, ys) - miny);
    };
    window.DOMQuad.prototype.toJSON = function () { return { p1: this.p1, p2: this.p2, p3: this.p3, p4: this.p4 }; };
    window.DOMQuad.fromRect = function (r) { r = r || {}; var x = +r.x || 0, y = +r.y || 0, w = +r.width || 0, h = +r.height || 0; return new window.DOMQuad(new window.DOMPoint(x, y), new window.DOMPoint(x + w, y), new window.DOMPoint(x + w, y + h), new window.DOMPoint(x, y + h)); };
    window.DOMQuad.fromQuad = function (q) { q = q || {}; return new window.DOMQuad(q.p1, q.p2, q.p3, q.p4); };
  }
  if (typeof window.Path2D === 'undefined') {
    window.Path2D = function (path) { this.__ops = (path && path.__ops) ? path.__ops.slice() : []; };
    ['moveTo', 'lineTo', 'bezierCurveTo', 'quadraticCurveTo', 'arc', 'arcTo', 'ellipse', 'rect', 'roundRect', 'closePath'].forEach(function (m) {
      window.Path2D.prototype[m] = function () { this.__ops.push(m); };
    });
    window.Path2D.prototype.addPath = function (p) { if (p && p.__ops) this.__ops = this.__ops.concat(p.__ops); };
  }

  // ---- document.fonts / FontFace (no real font loading) ----
  //
  // fonts.ready RESOLVES immediately (apps gate first paint on it) and check()
  // is optimistic, so no font-loading await can stall the render.
  if (typeof window.FontFace === 'undefined') {
    window.FontFace = function (family, source, descriptors) {
      descriptors = descriptors || {};
      this.family = String(family || '');
      this.style = descriptors.style || 'normal'; this.weight = descriptors.weight || 'normal';
      this.stretch = descriptors.stretch || 'normal'; this.unicodeRange = descriptors.unicodeRange || 'U+0-10FFFF';
      this.variant = 'normal'; this.featureSettings = 'normal'; this.display = descriptors.display || 'auto';
      this.status = 'unloaded';
      var self = this;
      this.loaded = Promise.resolve(this);
      this.load = function () { self.status = 'loaded'; return Promise.resolve(self); };
    };
  }
  if (typeof document !== 'undefined' && document.fonts === undefined) {
    var fontSet = [];
    document.fonts = {
      status: 'loaded', size: 0,
      onloading: null, onloadingdone: null, onloadingerror: null,
      add: function (f) { fontSet.push(f); this.size = fontSet.length; return this; },
      'delete': function (f) { var i = fontSet.indexOf(f); if (i >= 0) { fontSet.splice(i, 1); this.size = fontSet.length; } return i >= 0; },
      clear: function () { fontSet = []; this.size = 0; },
      has: function (f) { return fontSet.indexOf(f) >= 0; },
      check: function () { return true; },
      load: function () { return Promise.resolve([]); },
      forEach: function (cb, thisArg) { fontSet.forEach(function (f) { cb.call(thisArg, f, f, this); }, this); },
      values: function () { return fontSet.slice()[Symbol.iterator](); },
      keys: function () { return fontSet.slice()[Symbol.iterator](); },
      addEventListener: noop, removeEventListener: noop, dispatchEvent: function () { return true; }
    };
    document.fonts.ready = Promise.resolve(document.fonts);
  }

  // ---- Web Animations: Element.animate / getAnimations (no timeline) ----
  //
  // The animation is already finished; finished/ready resolve and onfinish fires
  // once via the wrapped timer, so code that animates then reads the end state
  // (or awaits finished) proceeds. No geometry is ever interpolated.
  if (typeof window.Animation === 'undefined') {
    window.Animation = function (effect, timeline) {
      this.effect = effect || null; this.timeline = timeline || null;
      this.playState = 'finished'; this.playbackRate = 1; this.startTime = 0; this.currentTime = 0;
      this.id = ''; this.pending = false;
      this.onfinish = null; this.oncancel = null; this.onremove = null;
      this.finished = Promise.resolve(this); this.ready = Promise.resolve(this);
      var self = this;
      this.play = function () { self.playState = 'finished'; }; this.pause = function () { self.playState = 'paused'; };
      this.cancel = function () { self.playState = 'idle'; }; this.finish = function () { self.playState = 'finished'; };
      this.reverse = noop; this.updatePlaybackRate = noop; this.persist = noop; this.commitStyles = noop;
      this.addEventListener = noop; this.removeEventListener = noop; this.dispatchEvent = function () { return true; };
    };
  }
  if (typeof window.KeyframeEffect === 'undefined') {
    window.KeyframeEffect = function (target, keyframes, options) {
      this.target = target || null;
      var opts = (typeof options === 'object' && options) ? options : { duration: +options || 0 };
      this.getComputedTiming = function () { return { duration: opts.duration || 0, delay: opts.delay || 0, endTime: 0, activeDuration: 0, progress: 1, currentIteration: 0, iterations: opts.iterations || 1 }; };
      this.getTiming = function () { return opts; };
      this.getKeyframes = function () { return (keyframes && keyframes.length) ? keyframes.slice() : []; };
      this.updateTiming = noop; this.setKeyframes = noop;
    };
  }
  (function () {
    var ep = document.createElement ? Object.getPrototypeOf(document.createElement('span')) : null;
    if (ep && typeof ep.animate !== 'function') {
      ep.animate = function (keyframes, options) {
        var anim = new window.Animation(new window.KeyframeEffect(this, keyframes, options), null);
        if (!this.__animations) this.__animations = [];
        this.__animations.push(anim);
        // Fire onfinish deferred so a handler assigned right after animate() is seen.
        setTimeout(function () { if (typeof anim.onfinish === 'function') { try { anim.onfinish({ type: 'finish', target: anim }); } catch (e) {} } }, 0);
        return anim;
      };
      ep.getAnimations = function () { return (this.__animations || []).slice(); };
    }
  })();

  // ---- attachInternals / ElementInternals (form-associated custom elements) ----
  //
  // A cached inert ElementInternals so form-associated components that call
  // attachInternals() in their constructor don't throw. No validation or form
  // participation is modelled; setFormValue/setValidity are accepted-and-ignored.
  (function () {
    var ep = document.createElement ? Object.getPrototypeOf(document.createElement('span')) : null;
    if (ep && typeof ep.attachInternals !== 'function') {
      ep.attachInternals = function () {
        if (this.__internals) return this.__internals;
        var host = this;
        var states = (typeof Set === 'function') ? new Set() : { add: noop, 'delete': noop, has: function () { return false; }, clear: noop };
        this.__internals = {
          form: null, labels: [], willValidate: true,
          validity: { valid: true, valueMissing: false, typeMismatch: false, patternMismatch: false, tooLong: false, tooShort: false, rangeUnderflow: false, rangeOverflow: false, stepMismatch: false, badInput: false, customError: false },
          validationMessage: '',
          get shadowRoot() { return host.shadowRoot || null; },
          states: states, role: null,
          setFormValue: noop, setValidity: noop,
          checkValidity: function () { return true; }, reportValidity: function () { return true; }
        };
        return this.__internals;
      };
    }
  })();

  // ---- reportError + cookieStore ----
  if (typeof window.reportError === 'undefined') {
    window.reportError = function (err) { try { console.error(err); } catch (e) {} };
  }
  if (typeof window.cookieStore === 'undefined') {
    var parseCookies = function () {
      var out = [], raw = (typeof document !== 'undefined' && document.cookie) ? document.cookie : '';
      raw.split(';').forEach(function (pair) {
        var i = pair.indexOf('=');
        if (i < 0) return;
        var name = pair.slice(0, i).trim();
        if (name) out.push({ name: name, value: decodeURIComponent(pair.slice(i + 1).trim()) });
      });
      return out;
    };
    window.cookieStore = {
      get: function (name) {
        if (name && typeof name === 'object') name = name.name;
        var all = parseCookies();
        for (var i = 0; i < all.length; i++) if (all[i].name === name) return Promise.resolve(all[i]);
        return Promise.resolve(null);
      },
      getAll: function () { return Promise.resolve(parseCookies()); },
      set: function (name, value) {
        try {
          if (name && typeof name === 'object') document.cookie = name.name + '=' + encodeURIComponent(name.value) + (name.path ? '; path=' + name.path : '');
          else document.cookie = name + '=' + encodeURIComponent(value);
        } catch (e) {}
        return Promise.resolve();
      },
      'delete': function (name) {
        if (name && typeof name === 'object') name = name.name;
        try { document.cookie = name + '=; expires=Thu, 01 Jan 1970 00:00:00 GMT'; } catch (e) {}
        return Promise.resolve();
      },
      addEventListener: noop, removeEventListener: noop, dispatchEvent: function () { return true; }
    };
  }

  // ---- TextEncoderStream / TextDecoderStream (over the Streams + utf8 helpers) ----
  //
  // CompressionStream/DecompressionStream are intentionally NOT provided: real
  // gzip/deflate needs a Go-side codec and rarely gates textual content — add it
  // only if a target page demonstrably needs it (see docs/web-api-priorities.md).
  if (typeof window.TextEncoderStream === 'undefined') {
    window.TextEncoderStream = function () {
      var ts = new window.TransformStream({ transform: function (chunk, c) { c.enqueue(utf8Encode(String(chunk))); } });
      this.readable = ts.readable; this.writable = ts.writable; this.encoding = 'utf-8';
    };
  }
  if (typeof window.TextDecoderStream === 'undefined') {
    window.TextDecoderStream = function (label, options) {
      decoderInit(this, label, options);
      var self = this, first = true;
      var ts = new window.TransformStream({ transform: function (chunk, c) {
        var s = utf8Decode(toU8(chunk), self.fatal);
        if (first) { first = false; if (!self.ignoreBOM && s.charCodeAt(0) === 0xFEFF) s = s.slice(1); }
        c.enqueue(s);
      } });
      this.readable = ts.readable; this.writable = ts.writable;
    };
  }

  // ---- Navigation API (partial; same-document routing) ----
  //
  // Newer routers prefer window.navigation over history. navigate() fires a
  // 'navigate' event whose intercept({handler}) runs the router's view update, so
  // client-side routing materializes content. Like history.pushState, it never
  // performs a real cross-document load and does not move window.location.
  if (typeof window.navigation === 'undefined') {
    var navListeners = {};
    var mkEntry = function (url, index) {
      var state;
      return { url: url, key: 'k' + index, id: 'e' + index, index: index, sameDocument: true,
               getState: function () { return state; }, __setState: function (s) { state = s; },
               addEventListener: noop, removeEventListener: noop };
    };
    var navEntries = [mkEntry((typeof location !== 'undefined' ? location.href : ''), 0)];
    var curIndex = 0;
    var navResult = function () { return { committed: Promise.resolve(navEntries[curIndex]), finished: Promise.resolve(navEntries[curIndex]) }; };
    var nav = {
      get currentEntry() { return navEntries[curIndex]; },
      get canGoBack() { return curIndex > 0; },
      get canGoForward() { return curIndex < navEntries.length - 1; },
      transition: null, activation: null,
      onnavigate: null, oncurrententrychange: null, onnavigatesuccess: null, onnavigateerror: null,
      entries: function () { return navEntries.slice(); },
      updateCurrentEntry: function (opts) { if (opts && 'state' in opts) navEntries[curIndex].__setState(opts.state); },
      addEventListener: function (t, f) { (navListeners[t] = navListeners[t] || []).push(f); },
      removeEventListener: function (t, f) { var a = navListeners[t]; if (a) { var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); } },
      dispatchEvent: function () { return true; },
      navigate: function (url, options) {
        options = options || {};
        var resolved;
        try { resolved = new window.URL(url, (typeof location !== 'undefined' ? location.href : undefined)).href; } catch (e) { resolved = String(url); }
        var interceptors = [];
        var ev = {
          type: 'navigate', navigationType: options.history === 'replace' ? 'replace' : 'push',
          canIntercept: true, hashChange: false, userInitiated: false, downloadRequest: null, formData: null, info: options.info,
          destination: { url: resolved, key: '', id: '', index: -1, sameDocument: true, getState: function () { return options.state; } },
          signal: (typeof AbortController === 'function' ? new AbortController().signal : null),
          intercept: function (opts) { if (opts && typeof opts.handler === 'function') interceptors.push(opts.handler); },
          scroll: noop, preventDefault: noop, stopPropagation: noop, stopImmediatePropagation: noop
        };
        if (typeof nav.onnavigate === 'function') { try { nav.onnavigate(ev); } catch (e) {} }
        (navListeners.navigate || []).slice().forEach(function (f) { try { f.call(nav, ev); } catch (e) {} });
        if (options.history === 'replace') { navEntries[curIndex] = mkEntry(resolved, curIndex); }
        else { navEntries = navEntries.slice(0, curIndex + 1); navEntries.push(mkEntry(resolved, navEntries.length)); curIndex = navEntries.length - 1; }
        if (options.state !== undefined) navEntries[curIndex].__setState(options.state);
        var finished = Promise.all(interceptors.map(function (h) { try { return Promise.resolve(h()); } catch (e) { return Promise.resolve(); } }));
        if (typeof nav.onnavigatesuccess === 'function') { try { nav.onnavigatesuccess({ type: 'navigatesuccess' }); } catch (e) {} }
        return { committed: Promise.resolve(navEntries[curIndex]), finished: finished.then(function () { return navEntries[curIndex]; }) };
      },
      reload: navResult,
      back: function () { if (curIndex > 0) curIndex--; return navResult(); },
      forward: function () { if (curIndex < navEntries.length - 1) curIndex++; return navResult(); },
      traverseTo: function () { return navResult(); }
    };
    window.navigation = nav;
  }

  // ---- Tier 3 crash-avoidance stubs: media & Web Audio (no playback) ----
  //
  // Inert by design: a page that calls video.play(), new Audio(), or
  // new AudioContext() at boot keeps running (and its surrounding DOM renders)
  // instead of throwing. Nothing is ever decoded or played.
  (function () {
    var ep = document.createElement ? Object.getPrototypeOf(document.createElement('span')) : null;
    if (ep && typeof ep.play !== 'function') {
      ep.play = function () { return Promise.resolve(); };
      ep.pause = noop;
      ep.load = noop;
      ep.canPlayType = function () { return ''; };
      ep.fastSeek = noop;
      ep.addTextTrack = function () { return { cues: [], activeCues: [], addCue: noop, removeCue: noop, mode: 'disabled', addEventListener: noop, removeEventListener: noop }; };
      ep.setSinkId = function () { return Promise.resolve(); };
      ep.setMediaKeys = function () { return Promise.resolve(); };
      ep.captureStream = function () { return { getTracks: function () { return []; }, getAudioTracks: function () { return []; }, getVideoTracks: function () { return []; }, addEventListener: noop, removeEventListener: noop }; };
    }
  })();
  if (typeof window.Audio === 'undefined') {
    window.Audio = function (src) { var a = document.createElement('audio'); if (src) a.src = src; return a; };
  }
  if (typeof window.MediaSource === 'undefined') {
    var MS = function () { this.readyState = 'closed'; this.sourceBuffers = []; this.activeSourceBuffers = []; this.duration = NaN; };
    MS.isTypeSupported = function () { return false; };
    MS.prototype.addSourceBuffer = function () {
      var sb = { updating: false, appendBuffer: noop, abort: noop, remove: noop, changeType: noop, addEventListener: noop, removeEventListener: noop, dispatchEvent: function () { return true; }, buffered: { length: 0, start: function () { return 0; }, end: function () { return 0; } } };
      this.sourceBuffers.push(sb); return sb;
    };
    MS.prototype.removeSourceBuffer = noop; MS.prototype.endOfStream = noop; MS.prototype.clearLiveSeekableRange = noop; MS.prototype.setLiveSeekableRange = noop;
    MS.prototype.addEventListener = noop; MS.prototype.removeEventListener = noop; MS.prototype.dispatchEvent = function () { return true; };
    window.MediaSource = MS;
    window.ManagedMediaSource = MS;
  }
  if (typeof window.AudioContext === 'undefined') {
    var audioParam = function (v) { return { value: v, defaultValue: v, minValue: -3.4e38, maxValue: 3.4e38, setValueAtTime: function () { return this; }, linearRampToValueAtTime: function () { return this; }, exponentialRampToValueAtTime: function () { return this; }, setTargetAtTime: function () { return this; }, setValueCurveAtTime: function () { return this; }, cancelScheduledValues: function () { return this; }, cancelAndHoldAtTime: function () { return this; } }; };
    var audioNode = function () {
      return { connect: function (d) { return d; }, disconnect: noop, gain: audioParam(1), frequency: audioParam(440), detune: audioParam(0), Q: audioParam(1), pan: audioParam(0), type: 'sine', start: noop, stop: noop, setPeriodicWave: noop, numberOfInputs: 1, numberOfOutputs: 1, channelCount: 2, addEventListener: noop, removeEventListener: noop, dispatchEvent: function () { return true; } };
    };
    var audioBuffer = function () { return { duration: 0, length: 0, numberOfChannels: 1, sampleRate: 44100, getChannelData: function () { return new Float32Array(0); }, copyFromChannel: noop, copyToChannel: noop }; };
    var AC = function () {
      this.state = 'suspended'; this.sampleRate = 44100; this.currentTime = 0; this.baseLatency = 0; this.outputLatency = 0;
      this.destination = audioNode(); this.listener = { positionX: audioParam(0), forwardX: audioParam(0), setPosition: noop, setOrientation: noop };
      this.audioWorklet = { addModule: function () { return Promise.resolve(); } };
    };
    var acProto = AC.prototype;
    ['createGain', 'createOscillator', 'createBufferSource', 'createBiquadFilter', 'createDynamicsCompressor', 'createConvolver', 'createDelay', 'createStereoPanner', 'createPanner', 'createWaveShaper', 'createChannelSplitter', 'createChannelMerger', 'createConstantSource', 'createScriptProcessor', 'createMediaElementSource', 'createMediaStreamSource', 'createMediaStreamDestination', 'createIIRFilter'].forEach(function (m) { acProto[m] = audioNode; });
    acProto.createAnalyser = function () { var n = audioNode(); n.fftSize = 2048; n.frequencyBinCount = 1024; n.minDecibels = -100; n.maxDecibels = -30; n.smoothingTimeConstant = 0.8; n.getByteFrequencyData = noop; n.getByteTimeDomainData = noop; n.getFloatFrequencyData = noop; n.getFloatTimeDomainData = noop; return n; };
    acProto.createBuffer = audioBuffer;
    acProto.createPeriodicWave = function () { return {}; };
    acProto.decodeAudioData = function (data, cb) { var b = audioBuffer(); if (typeof cb === 'function') { try { cb(b); } catch (e) {} } return Promise.resolve(b); };
    acProto.resume = function () { this.state = 'running'; return Promise.resolve(); };
    acProto.suspend = function () { return Promise.resolve(); };
    acProto.close = function () { this.state = 'closed'; return Promise.resolve(); };
    acProto.getOutputTimestamp = function () { return { contextTime: 0, performanceTime: 0 }; };
    acProto.addEventListener = noop; acProto.removeEventListener = noop; acProto.dispatchEvent = function () { return true; };
    window.AudioContext = AC;
    window.webkitAudioContext = AC;
    var OAC = function () { AC.call(this); this.length = 0; };
    OAC.prototype = Object.create(AC.prototype); OAC.prototype.constructor = OAC;
    OAC.prototype.startRendering = function () { return Promise.resolve(audioBuffer()); };
    window.OfflineAudioContext = OAC;
  }

  // ---- Tier 3 crash-avoidance stubs: navigator device APIs ----
  //
  // All inert: requestDevice/permission-style calls reject or resolve empty so a
  // feature-detecting app degrades instead of throwing at boot.
  if (typeof navigator !== 'undefined') {
    var rejectDevice = function () { var e = new Error('NotFoundError: no device selected'); e.name = 'NotFoundError'; return Promise.reject(e); };
    if (!navigator.bluetooth) navigator.bluetooth = { getAvailability: function () { return Promise.resolve(false); }, requestDevice: rejectDevice, getDevices: function () { return Promise.resolve([]); }, addEventListener: noop, removeEventListener: noop };
    if (!navigator.usb) navigator.usb = { getDevices: function () { return Promise.resolve([]); }, requestDevice: rejectDevice, addEventListener: noop, removeEventListener: noop };
    if (!navigator.serial) navigator.serial = { getPorts: function () { return Promise.resolve([]); }, requestPort: rejectDevice, addEventListener: noop, removeEventListener: noop };
    if (!navigator.hid) navigator.hid = { getDevices: function () { return Promise.resolve([]); }, requestDevice: function () { return Promise.resolve([]); }, addEventListener: noop, removeEventListener: noop };
    if (!navigator.xr) navigator.xr = { isSessionSupported: function () { return Promise.resolve(false); }, requestSession: function () { var e = new Error('NotSupportedError'); e.name = 'NotSupportedError'; return Promise.reject(e); }, addEventListener: noop, removeEventListener: noop };
    if (!navigator.gpu) navigator.gpu = { requestAdapter: function () { return Promise.resolve(null); }, getPreferredCanvasFormat: function () { return 'bgra8unorm'; }, wgslLanguageFeatures: { has: function () { return false; } } };
    if (!navigator.getGamepads) navigator.getGamepads = function () { return []; };
    if (!navigator.getBattery) navigator.getBattery = function () { return Promise.resolve({ charging: true, chargingTime: 0, dischargingTime: Infinity, level: 1, addEventListener: noop, removeEventListener: noop, onchargingchange: null, onlevelchange: null }); };
    if (!navigator.requestMIDIAccess) navigator.requestMIDIAccess = function () { return Promise.resolve({ inputs: new Map(), outputs: new Map(), sysexEnabled: false, addEventListener: noop, removeEventListener: noop, onstatechange: null }); };
    if (!navigator.wakeLock) navigator.wakeLock = { request: function () { return Promise.resolve({ released: false, type: 'screen', release: function () { this.released = true; return Promise.resolve(); }, addEventListener: noop, removeEventListener: noop }); } };
    if (!navigator.locks) navigator.locks = { request: function (name, opts, cb) { if (typeof opts === 'function') { cb = opts; } var lock = { name: String(name), mode: 'exclusive' }; return Promise.resolve(typeof cb === 'function' ? cb(lock) : undefined); }, query: function () { return Promise.resolve({ held: [], pending: [] }); } };
    if (!navigator.presentation) navigator.presentation = { defaultRequest: null, receiver: null };
    if (!navigator.ink) navigator.ink = { requestPresenter: function () { return Promise.reject(new Error('NotSupportedError')); } };
    if (!navigator.storage) navigator.storage = { estimate: function () { return Promise.resolve({ usage: 0, quota: 0 }); }, persist: function () { return Promise.resolve(false); }, persisted: function () { return Promise.resolve(false); }, getDirectory: function () { return Promise.reject(new Error('SecurityError')); } };
    if (!navigator.credentials) navigator.credentials = { get: function () { return Promise.resolve(null); }, store: function () { return Promise.resolve(); }, create: function () { return Promise.resolve(null); }, preventSilentAccess: function () { return Promise.resolve(); } };
    if (!navigator.contacts) navigator.contacts = { select: function () { return Promise.resolve([]); }, getProperties: function () { return Promise.resolve([]); } };
    if (typeof navigator.setAppBadge !== 'function') { navigator.setAppBadge = function () { return Promise.resolve(); }; navigator.clearAppBadge = function () { return Promise.resolve(); }; }
    if (typeof navigator.share !== 'function') { navigator.share = function () { var e = new Error('AbortError'); e.name = 'AbortError'; return Promise.reject(e); }; navigator.canShare = function () { return false; }; }
    if (typeof navigator.vibrate !== 'function') navigator.vibrate = function () { return false; };
    if (typeof navigator.registerProtocolHandler !== 'function') navigator.registerProtocolHandler = noop;
  }

  // ---- Tier 3 crash-avoidance stubs: niche constructors ----
  if (typeof window.RTCPeerConnection === 'undefined') {
    var RTCPC = function () { this.localDescription = null; this.remoteDescription = null; this.signalingState = 'stable'; this.iceConnectionState = 'new'; this.iceGatheringState = 'new'; this.connectionState = 'new'; this.__l = {}; };
    RTCPC.prototype.createOffer = function () { return Promise.resolve({ type: 'offer', sdp: '' }); };
    RTCPC.prototype.createAnswer = function () { return Promise.resolve({ type: 'answer', sdp: '' }); };
    RTCPC.prototype.setLocalDescription = function () { return Promise.resolve(); };
    RTCPC.prototype.setRemoteDescription = function () { return Promise.resolve(); };
    RTCPC.prototype.addIceCandidate = function () { return Promise.resolve(); };
    RTCPC.prototype.createDataChannel = function () { return { send: noop, close: noop, readyState: 'connecting', addEventListener: noop, removeEventListener: noop, dispatchEvent: function () { return true; } }; };
    RTCPC.prototype.addTrack = function () { return {}; }; RTCPC.prototype.removeTrack = noop;
    RTCPC.prototype.getSenders = function () { return []; }; RTCPC.prototype.getReceivers = function () { return []; }; RTCPC.prototype.getTransceivers = function () { return []; }; RTCPC.prototype.addTransceiver = function () { return {}; };
    RTCPC.prototype.getStats = function () { return Promise.resolve(new Map()); };
    RTCPC.prototype.close = noop; RTCPC.prototype.restartIce = noop;
    RTCPC.prototype.addEventListener = function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); }; RTCPC.prototype.removeEventListener = noop; RTCPC.prototype.dispatchEvent = function () { return true; };
    window.RTCPeerConnection = RTCPC; window.webkitRTCPeerConnection = RTCPC;
    window.RTCSessionDescription = function (o) { o = o || {}; this.type = o.type; this.sdp = o.sdp; };
    window.RTCIceCandidate = function (o) { o = o || {}; this.candidate = o.candidate || ''; this.sdpMid = o.sdpMid; this.sdpMLineIndex = o.sdpMLineIndex; };
    window.MediaStream = function () { this.active = false; this.id = ''; this.getTracks = function () { return []; }; this.getAudioTracks = function () { return []; }; this.getVideoTracks = function () { return []; }; this.addTrack = noop; this.removeTrack = noop; this.getTrackById = function () { return null; }; this.clone = function () { return this; }; this.addEventListener = noop; this.removeEventListener = noop; };
  }
  if (typeof window.PaymentRequest === 'undefined') {
    window.PaymentRequest = function () {
      this.show = function () { var e = new Error('AbortError'); e.name = 'AbortError'; return Promise.reject(e); };
      this.canMakePayment = function () { return Promise.resolve(false); };
      this.abort = function () { return Promise.resolve(); };
      this.addEventListener = noop; this.removeEventListener = noop;
    };
  }
  if (typeof window.speechSynthesis === 'undefined') {
    window.speechSynthesis = { speaking: false, pending: false, paused: false, speak: noop, cancel: noop, pause: noop, resume: noop, getVoices: function () { return []; }, addEventListener: noop, removeEventListener: noop, onvoiceschanged: null };
    window.SpeechSynthesisUtterance = function (text) { this.text = text || ''; this.lang = ''; this.voice = null; this.volume = 1; this.rate = 1; this.pitch = 1; this.onstart = null; this.onend = null; this.onerror = null; this.addEventListener = noop; this.removeEventListener = noop; };
  }
  if (typeof window.SpeechRecognition === 'undefined' && typeof window.webkitSpeechRecognition === 'undefined') {
    var SR = function () { this.lang = ''; this.continuous = false; this.interimResults = false; this.maxAlternatives = 1; this.start = noop; this.stop = noop; this.abort = noop; this.onresult = null; this.onerror = null; this.onend = null; this.addEventListener = noop; this.removeEventListener = noop; };
    window.SpeechRecognition = SR; window.webkitSpeechRecognition = SR;
  }
  if (typeof window.Accelerometer === 'undefined') {
    var Sensor = function () { this.activated = false; this.hasReading = false; this.x = null; this.y = null; this.z = null; this.start = noop; this.stop = noop; this.addEventListener = noop; this.removeEventListener = noop; this.onreading = null; this.onerror = null; this.onactivate = null; };
    ['Accelerometer', 'LinearAccelerationSensor', 'GravitySensor', 'Gyroscope', 'Magnetometer', 'AbsoluteOrientationSensor', 'RelativeOrientationSensor', 'AmbientLightSensor'].forEach(function (n) { window[n] = Sensor; });
  }
  if (typeof window.Notification === 'undefined') {
    // The priorities-doc escape hatch: present but permission 'denied'.
    var Notif = function (title, opts) { opts = opts || {}; this.title = title || ''; this.body = opts.body || ''; this.icon = opts.icon || ''; this.tag = opts.tag || ''; this.data = opts.data; this.onclick = null; this.onclose = null; this.onerror = null; this.onshow = null; this.close = noop; this.addEventListener = noop; this.removeEventListener = noop; };
    Notif.permission = 'denied'; Notif.maxActions = 0;
    Notif.requestPermission = function (cb) { var p = Promise.resolve('denied'); if (typeof cb === 'function') p.then(cb); return p; };
    window.Notification = Notif;
  }
  if (typeof window.BarcodeDetector === 'undefined') {
    window.BarcodeDetector = function () { this.detect = function () { return Promise.resolve([]); }; };
    window.BarcodeDetector.getSupportedFormats = function () { return Promise.resolve([]); };
  }
  if (typeof window.EyeDropper === 'undefined') {
    window.EyeDropper = function () { this.open = function () { var e = new Error('AbortError'); e.name = 'AbortError'; return Promise.reject(e); }; };
  }
  if (typeof window.IdleDetector === 'undefined') {
    var IdleDet = function () { this.userState = null; this.screenState = null; this.start = function () { return Promise.resolve(); }; this.addEventListener = noop; this.removeEventListener = noop; };
    IdleDet.requestPermission = function () { return Promise.resolve('denied'); };
    window.IdleDetector = IdleDet;
  }
  if (typeof window.WebTransport === 'undefined') {
    window.WebTransport = function (url) {
      this.url = String(url || '');
      this.closed = Promise.resolve({ closeCode: 0, reason: '' });
      this.ready = Promise.reject(new Error('WebTransport is not supported in this environment'));
      this.ready.catch(function () {});
      this.datagrams = { readable: null, writable: null };
      this.createBidirectionalStream = function () { return Promise.reject(new Error('closed')); };
      this.createUnidirectionalStream = function () { return Promise.reject(new Error('closed')); };
      this.close = noop;
    };
  }
  if (typeof window.CloseWatcher === 'undefined') {
    window.CloseWatcher = function () { this.__l = {}; this.destroy = noop; this.close = noop; this.requestClose = noop; this.oncancel = null; this.onclose = null; this.addEventListener = function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); }; this.removeEventListener = noop; this.dispatchEvent = function () { return true; }; };
  }

  // ---- Tier 3 crash-avoidance stubs: element/document interaction ----
  //
  // Popover/fullscreen/PiP are no-ops with no layout to change; the popover's
  // content is already in the light tree, so extraction sees it regardless.
  // View Transitions run the update callback synchronously (so a router's new
  // view materializes) and resolve.
  (function () {
    var ep = document.createElement ? Object.getPrototypeOf(document.createElement('span')) : null;
    if (ep && typeof ep.showPopover !== 'function') {
      ep.showPopover = noop; ep.hidePopover = noop;
      ep.togglePopover = function (force) { return force !== undefined ? !!force : true; };
    }
    if (ep && typeof ep.requestFullscreen !== 'function') {
      ep.requestFullscreen = function () { return Promise.resolve(); };
      ep.webkitRequestFullscreen = noop; ep.mozRequestFullScreen = noop; ep.msRequestFullscreen = noop;
    }
    if (ep && typeof ep.requestPictureInPicture !== 'function') {
      ep.requestPictureInPicture = function () { var e = new Error('NotSupportedError'); e.name = 'NotSupportedError'; return Promise.reject(e); };
    }
    if (ep && typeof ep.requestPointerLock !== 'function') { ep.requestPointerLock = noop; }
    if (ep && !ep.remote) {
      ep.remote = { state: 'disconnected', watchAvailability: function () { return Promise.resolve(0); }, cancelWatchAvailability: function () { return Promise.resolve(); }, prompt: function () { return Promise.reject(new Error('NotSupportedError')); }, addEventListener: noop, removeEventListener: noop };
    }
  })();
  if (typeof document !== 'undefined') {
    if (document.fullscreenElement === undefined) { document.fullscreenElement = null; document.fullscreenEnabled = true; document.webkitFullscreenElement = null; document.webkitFullscreenEnabled = true; }
    if (typeof document.exitFullscreen !== 'function') { document.exitFullscreen = function () { return Promise.resolve(); }; document.webkitExitFullscreen = noop; }
    if (document.pictureInPictureEnabled === undefined) { document.pictureInPictureEnabled = false; document.pictureInPictureElement = null; }
    if (typeof document.exitPictureInPicture !== 'function') { document.exitPictureInPicture = function () { return Promise.resolve(); }; }
    if (typeof document.exitPointerLock !== 'function') { document.exitPointerLock = noop; document.pointerLockElement = null; }
    if (typeof document.hasStorageAccess !== 'function') { document.hasStorageAccess = function () { return Promise.resolve(false); }; document.requestStorageAccess = function () { return Promise.resolve(); }; }
    if (typeof document.startViewTransition !== 'function') {
      document.startViewTransition = function (cb) {
        var done = Promise.resolve();
        try { if (typeof cb === 'function') { var res = cb(); if (res && typeof res.then === 'function') done = Promise.resolve(res); } } catch (e) { done = Promise.reject(e); }
        var swallow = function () {};
        return { ready: done.then(swallow, swallow), finished: done.then(swallow, swallow), updateCallbackDone: done, skipTransition: noop, types: { has: function () { return false; }, add: noop, 'delete': noop, clear: noop } };
      };
    }
  }
  if (typeof window.ToggleEvent === 'undefined') {
    window.ToggleEvent = function (type, opts) { opts = opts || {}; this.type = type; this.bubbles = !!opts.bubbles; this.cancelable = !!opts.cancelable; this.oldState = opts.oldState || ''; this.newState = opts.newState || ''; this.defaultPrevented = false; };
  }

  // ---- crypto.subtle (broad, Go-backed; ADR 0006) ----
  //
  // The WebCrypto object model (Promises, CryptoKey, formats, algorithm
  // normalization) lives here; the byte-level primitives are Go natives
  // (subtle.go). Supported: digest, HMAC sign/verify, AES-GCM/CBC/CTR
  // encrypt/decrypt, PBKDF2/HKDF derive, and symmetric generate/import/export.
  // RSA/ECDSA/ECDH and wrap/unwrap reject with NotSupportedError (never a sync
  // throw) so a feature-detecting app degrades to a catchable rejection.
  if (window.crypto && typeof __unblinkRandomBytes === 'function') {
    // Real entropy (crypto/rand), replacing the Math.random getRandomValues.
    window.crypto.getRandomValues = function (arr) {
      var bytes = new Uint8Array(__unblinkRandomBytes(arr.byteLength));
      new Uint8Array(arr.buffer, arr.byteOffset, arr.byteLength).set(bytes);
      return arr;
    };
  }
  if (window.crypto && !window.crypto.subtle && typeof __unblinkDigest === 'function') {
    var algoName = function (a) { return (typeof a === 'string') ? a.toUpperCase() : (a && a.name ? String(a.name).toUpperCase() : ''); };
    var normHash = function (a) { var h = (a && typeof a === 'object' && a.hash) ? a.hash : a; return algoName(h); };
    var ab = function (data) { return bytesToArrayBuffer(toU8(data)); };
    var mkKey = function (type, raw, algorithm, extractable, usages) { return { type: type, extractable: !!extractable, algorithm: algorithm, usages: usages || [], __raw: raw }; };
    var notSupported = function (op, name) { return Promise.reject(new Error('NotSupportedError: crypto.subtle.' + op + ' does not support ' + name)); };
    var u8ToB64url = function (u8) { var s = ''; for (var i = 0; i < u8.length; i++) s += String.fromCharCode(u8[i]); return window.btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); };
    var b64urlToU8 = function (str) { str = String(str).replace(/-/g, '+').replace(/_/g, '/'); while (str.length % 4) str += '='; var bin = window.atob(str), u8 = new Uint8Array(bin.length); for (var i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i); return u8; };

    window.crypto.subtle = {
      digest: function (algorithm, data) {
        try { return Promise.resolve(__unblinkDigest(normHash(algorithm), ab(data))); } catch (e) { return Promise.reject(e); }
      },
      sign: function (algorithm, key, data) {
        if (algoName(algorithm) !== 'HMAC') return notSupported('sign', algoName(algorithm));
        try { return Promise.resolve(__unblinkHmacSign(key.__hash, ab(key.__raw), ab(data))); } catch (e) { return Promise.reject(e); }
      },
      verify: function (algorithm, key, signature, data) {
        if (algoName(algorithm) !== 'HMAC') return notSupported('verify', algoName(algorithm));
        try {
          var expected = new Uint8Array(__unblinkHmacSign(key.__hash, ab(key.__raw), ab(data)));
          var got = toU8(signature);
          if (expected.length !== got.length) return Promise.resolve(false);
          var diff = 0; for (var i = 0; i < expected.length; i++) diff |= expected[i] ^ got[i];
          return Promise.resolve(diff === 0);
        } catch (e) { return Promise.reject(e); }
      },
      encrypt: function (algorithm, key, data) {
        var name = algoName(algorithm);
        try {
          if (name === 'AES-GCM') return Promise.resolve(__unblinkAesEncrypt('GCM', ab(key.__raw), ab(algorithm.iv), ab(data), ab(algorithm.additionalData || new Uint8Array(0))));
          if (name === 'AES-CBC') return Promise.resolve(__unblinkAesEncrypt('CBC', ab(key.__raw), ab(algorithm.iv), ab(data), ab(new Uint8Array(0))));
          if (name === 'AES-CTR') return Promise.resolve(__unblinkAesEncrypt('CTR', ab(key.__raw), ab(algorithm.counter), ab(data), ab(new Uint8Array(0))));
          return notSupported('encrypt', name);
        } catch (e) { return Promise.reject(e); }
      },
      decrypt: function (algorithm, key, data) {
        var name = algoName(algorithm);
        try {
          if (name === 'AES-GCM') return Promise.resolve(__unblinkAesDecrypt('GCM', ab(key.__raw), ab(algorithm.iv), ab(data), ab(algorithm.additionalData || new Uint8Array(0))));
          if (name === 'AES-CBC') return Promise.resolve(__unblinkAesDecrypt('CBC', ab(key.__raw), ab(algorithm.iv), ab(data), ab(new Uint8Array(0))));
          if (name === 'AES-CTR') return Promise.resolve(__unblinkAesDecrypt('CTR', ab(key.__raw), ab(algorithm.counter), ab(data), ab(new Uint8Array(0))));
          return notSupported('decrypt', name);
        } catch (e) { return Promise.reject(e); }
      },
      deriveBits: function (algorithm, baseKey, length) {
        var name = algoName(algorithm);
        try {
          if (name === 'PBKDF2') return Promise.resolve(__unblinkPbkdf2(normHash(algorithm), ab(baseKey.__raw), ab(algorithm.salt), algorithm.iterations || 1, length || 0));
          if (name === 'HKDF') return Promise.resolve(__unblinkHkdf(normHash(algorithm), ab(baseKey.__raw), ab(algorithm.salt || new Uint8Array(0)), ab(algorithm.info || new Uint8Array(0)), length || 0));
          return notSupported('deriveBits', name);
        } catch (e) { return Promise.reject(e); }
      },
      deriveKey: function (algorithm, baseKey, derivedKeyType, extractable, usages) {
        var self = this;
        var bits = derivedKeyType.length || 256;
        return this.deriveBits(algorithm, baseKey, bits).then(function (raw) { return self.importKey('raw', raw, derivedKeyType, extractable, usages); });
      },
      generateKey: function (algorithm, extractable, usages) {
        var name = algoName(algorithm);
        try {
          if (name === 'AES-GCM' || name === 'AES-CBC' || name === 'AES-CTR') {
            var raw = new Uint8Array(__unblinkRandomBytes((algorithm.length || 256) / 8));
            return Promise.resolve(mkKey('secret', raw, { name: name, length: algorithm.length || 256 }, extractable, usages));
          }
          if (name === 'HMAC') {
            var h = normHash(algorithm);
            var n = algorithm.length ? algorithm.length / 8 : ((h === 'SHA-512' || h === 'SHA-384') ? 128 : 64);
            var k = mkKey('secret', new Uint8Array(__unblinkRandomBytes(n)), { name: 'HMAC', hash: { name: h }, length: n * 8 }, extractable, usages);
            k.__hash = h;
            return Promise.resolve(k);
          }
          return notSupported('generateKey', name);
        } catch (e) { return Promise.reject(e); }
      },
      importKey: function (format, keyData, algorithm, extractable, usages) {
        var name = algoName(algorithm);
        try {
          var raw;
          if (format === 'raw') raw = toU8(keyData);
          else if (format === 'jwk' && keyData && keyData.k) raw = b64urlToU8(keyData.k);
          else return notSupported('importKey format', String(format));
          var alg = { name: name, length: raw.length * 8 };
          var k = mkKey('secret', raw, alg, extractable, usages);
          if (name === 'HMAC') { alg.hash = { name: normHash(algorithm) }; k.__hash = normHash(algorithm); }
          return Promise.resolve(k);
        } catch (e) { return Promise.reject(e); }
      },
      exportKey: function (format, key) {
        try {
          if (!key.extractable) return Promise.reject(new Error('InvalidAccessError: key is not extractable'));
          if (format === 'raw') return Promise.resolve(bytesToArrayBuffer(toU8(key.__raw)));
          if (format === 'jwk') return Promise.resolve({ kty: 'oct', k: u8ToB64url(toU8(key.__raw)), ext: true, key_ops: key.usages || [] });
          return notSupported('exportKey format', String(format));
        } catch (e) { return Promise.reject(e); }
      },
      wrapKey: function () { return Promise.reject(new Error('NotSupportedError: crypto.subtle.wrapKey')); },
      unwrapKey: function () { return Promise.reject(new Error('NotSupportedError: crypto.subtle.unwrapKey')); }
    };
  }

  // ---- indexedDB: in-memory, non-persistent stub ----
  //
  // THE STUB of the IndexedDB non-goal, not persistent IndexedDB: object stores
  // are plain Maps discarded when the runtime ends. Its whole job is to let
  // offline-first PWAs that open a DB at boot proceed to render instead of
  // ReferenceError-ing. SETTLE-CRITICAL: every request/transaction callback fires
  // through the WRAPPED setTimeout so it registers in the live-timer audit; a
  // native timer would let the render close early and truncate content.
  if (typeof window.indexedDB === 'undefined') {
    var idbDBs = {}; // name -> { version, stores: { storeName -> rec } }

    var idbKey = function (k) {
      if (k && typeof k === 'object') { try { return 'j:' + JSON.stringify(k); } catch (e) { return 'o:' + String(k); } }
      return typeof k + ':' + String(k);
    };
    var idbExtractKey = function (rec, value, explicitKey) {
      if (explicitKey !== undefined) return explicitKey;
      if (typeof rec.keyPath === 'string' && value) return value[rec.keyPath];
      if (rec.autoIncrement) return ++rec.autoInc;
      return undefined;
    };
    var idbStringList = function (names) {
      var arr = names.slice();
      var list = { length: arr.length, item: function (i) { return (i >= 0 && i < arr.length) ? arr[i] : null; }, contains: function (n) { return arr.indexOf(String(n)) >= 0; } };
      for (var i = 0; i < arr.length; i++) list[i] = arr[i];
      return list;
    };
    var idbMakeReq = function (source) {
      return { result: undefined, error: null, readyState: 'pending', source: source || null, transaction: null,
               onsuccess: null, onerror: null, __l: {},
               addEventListener: function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); },
               removeEventListener: function (t, f) { var a = this.__l[t]; if (a) { var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); } },
               dispatchEvent: function () { return true; } };
    };
    var idbFire = function (obj, type, extra) {
      var ev = { type: type, target: obj, currentTarget: obj };
      if (extra) for (var k in extra) ev[k] = extra[k];
      var h = obj['on' + type];
      if (typeof h === 'function') { try { h.call(obj, ev); } catch (e) {} }
      (obj.__l[type] || []).slice().forEach(function (f) { try { f.call(obj, ev); } catch (e) {} });
    };
    var idbSettle = function (req, compute) {
      // Run the data op SYNCHRONOUSLY (in program order, like a real
      // transaction), capturing the result; defer only the success/error event
      // through the wrapped setTimeout. This makes a get() after a put() in the
      // same handler observe the put regardless of timer ordering.
      var result, err = null, ok = true;
      try { result = compute(); } catch (e) { ok = false; err = e; }
      setTimeout(function () {
        if (ok) { req.result = result; req.readyState = 'done'; idbFire(req, 'success'); }
        else { req.error = err; req.readyState = 'done'; idbFire(req, 'error'); }
      }, 0);
      return req;
    };

    var idbMakeStore = function (rec) {
      var store = {
        name: rec.name, keyPath: rec.keyPath, autoIncrement: rec.autoIncrement, indexNames: idbStringList([]),
        put: function (value, key) { return idbSettle(idbMakeReq(this), function () { var k = idbExtractKey(rec, value, key); rec.map.set(idbKey(k), { key: k, value: value }); return k; }); },
        add: function (value, key) { return idbSettle(idbMakeReq(this), function () { var k = idbExtractKey(rec, value, key); rec.map.set(idbKey(k), { key: k, value: value }); return k; }); },
        get: function (key) { return idbSettle(idbMakeReq(this), function () { var e = rec.map.get(idbKey(key)); return e ? e.value : undefined; }); },
        getAll: function () { return idbSettle(idbMakeReq(this), function () { var out = []; rec.map.forEach(function (e) { out.push(e.value); }); return out; }); },
        getAllKeys: function () { return idbSettle(idbMakeReq(this), function () { var out = []; rec.map.forEach(function (e) { out.push(e.key); }); return out; }); },
        getKey: function (key) { return idbSettle(idbMakeReq(this), function () { var e = rec.map.get(idbKey(key)); return e ? e.key : undefined; }); },
        'delete': function (key) { return idbSettle(idbMakeReq(this), function () { rec.map['delete'](idbKey(key)); return undefined; }); },
        clear: function () { return idbSettle(idbMakeReq(this), function () { rec.map.clear(); return undefined; }); },
        count: function () { return idbSettle(idbMakeReq(this), function () { return rec.map.size; }); },
        // Cursor iteration is not modelled (returns end-of-cursor); getAll covers reads.
        openCursor: function () { return idbSettle(idbMakeReq(this), function () { return null; }); },
        openKeyCursor: function () { return idbSettle(idbMakeReq(this), function () { return null; }); },
        createIndex: function (name) { rec.indexes[name] = true; store.indexNames = idbStringList(Object.keys(rec.indexes)); return idbMakeIndex(rec, name, store); },
        deleteIndex: function (name) { delete rec.indexes[name]; store.indexNames = idbStringList(Object.keys(rec.indexes)); },
        index: function (name) { return idbMakeIndex(rec, name, store); }
      };
      return store;
    };
    var idbMakeIndex = function (rec, name, store) {
      return { name: name, objectStore: store, keyPath: null, multiEntry: false, unique: false,
        get: function (key) { return idbSettle(idbMakeReq(store), function () { var e = rec.map.get(idbKey(key)); return e ? e.value : undefined; }); },
        getAll: function () { return idbSettle(idbMakeReq(store), function () { var out = []; rec.map.forEach(function (e) { out.push(e.value); }); return out; }); },
        getAllKeys: function () { return idbSettle(idbMakeReq(store), function () { var out = []; rec.map.forEach(function (e) { out.push(e.key); }); return out; }); },
        count: function () { return idbSettle(idbMakeReq(store), function () { return rec.map.size; }); },
        openCursor: function () { return idbSettle(idbMakeReq(store), function () { return null; }); } };
    };

    var idbMakeDB = function (name, entry) {
      var db = {
        name: name, version: entry.version, objectStoreNames: idbStringList(Object.keys(entry.stores)),
        onversionchange: null, onclose: null, onabort: null, onerror: null, __l: {},
        addEventListener: function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); }, removeEventListener: noop, dispatchEvent: function () { return true; },
        createObjectStore: function (storeName, opts) {
          opts = opts || {};
          var rec = { name: storeName, map: new Map(), keyPath: opts.keyPath !== undefined ? opts.keyPath : null, autoIncrement: !!opts.autoIncrement, autoInc: 0, indexes: {} };
          entry.stores[storeName] = rec;
          db.objectStoreNames = idbStringList(Object.keys(entry.stores));
          return idbMakeStore(rec);
        },
        deleteObjectStore: function (storeName) { delete entry.stores[storeName]; db.objectStoreNames = idbStringList(Object.keys(entry.stores)); },
        transaction: function (names, mode) {
          var tx = {
            db: db, mode: mode || 'readonly', error: null, oncomplete: null, onerror: null, onabort: null, __l: {},
            addEventListener: function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); }, removeEventListener: noop, dispatchEvent: function () { return true; },
            objectStore: function (storeName) { var rec = entry.stores[storeName]; if (!rec) throw new Error('NotFoundError: object store ' + storeName + ' not found'); return idbMakeStore(rec); },
            abort: noop, commit: noop
          };
          setTimeout(function () { idbFire(tx, 'complete'); }, 0);
          return tx;
        },
        close: noop
      };
      return db;
    };

    window.indexedDB = {
      open: function (name, version) {
        name = String(name);
        var req = idbMakeReq(null);
        req.onupgradeneeded = null; req.onblocked = null;
        var isNew = !idbDBs[name];
        var oldVersion = isNew ? 0 : idbDBs[name].version;
        var newVersion = version || (isNew ? 1 : idbDBs[name].version);
        var upgrade = isNew || newVersion > oldVersion;
        if (isNew) idbDBs[name] = { version: newVersion, stores: {} };
        var entry = idbDBs[name];
        entry.version = newVersion;
        var db = idbMakeDB(name, entry);
        req.result = db;
        setTimeout(function () {
          if (upgrade && (typeof req.onupgradeneeded === 'function' || (req.__l.upgradeneeded && req.__l.upgradeneeded.length))) {
            var vtx = {
              db: db, mode: 'versionchange', error: null, oncomplete: null, onerror: null, onabort: null, __l: {},
              addEventListener: function (t, f) { (this.__l[t] = this.__l[t] || []).push(f); }, removeEventListener: noop, dispatchEvent: function () { return true; },
              objectStore: function (sn) { var rec = entry.stores[sn]; if (!rec) throw new Error('NotFoundError: ' + sn); return idbMakeStore(rec); },
              abort: noop
            };
            req.transaction = vtx;
            idbFire(req, 'upgradeneeded', { oldVersion: oldVersion, newVersion: newVersion });
            req.transaction = null;
          }
          db.objectStoreNames = idbStringList(Object.keys(entry.stores));
          req.readyState = 'done';
          req.result = db;
          idbFire(req, 'success');
        }, 0);
        return req;
      },
      deleteDatabase: function (name) {
        var req = idbMakeReq(null);
        return idbSettle(req, function () { delete idbDBs[String(name)]; return undefined; });
      },
      databases: function () { return Promise.resolve(Object.keys(idbDBs).map(function (n) { return { name: n, version: idbDBs[n].version }; })); },
      cmp: function (a, b) { return a < b ? -1 : (a > b ? 1 : 0); }
    };
    window.IDBKeyRange = {
      bound: function (l, u, lo, uo) { return { lower: l, upper: u, lowerOpen: !!lo, upperOpen: !!uo }; },
      only: function (v) { return { lower: v, upper: v, only: true }; },
      lowerBound: function (l, o) { return { lower: l, lowerOpen: !!o }; },
      upperBound: function (u, o) { return { upper: u, upperOpen: !!o }; }
    };
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
      ev.__uninitialized = true; // dispatching before initEvent must throw InvalidStateError
      ev.initEvent = function (type, bubbles, cancelable) {
        ev.type = type; ev.bubbles = !!bubbles; ev.cancelable = !!cancelable; ev.__uninitialized = false;
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
