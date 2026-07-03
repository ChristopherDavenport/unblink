package js_test

import (
	"strings"
	"testing"
	"time"
)

// --- Batch F: geometry & graphics math ---

// TestGeometry proves DOMRect/DOMPoint/DOMMatrix/DOMQuad/Path2D do real math
// (matrix compose, inverse, transformPoint) — the transform-library surface.
func TestGeometry(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var r = new DOMRect(10, 20, 30, 40);
		  var m = new DOMMatrix().translate(5, 7).scale(2);
		  var tp = m.transformPoint(new DOMPoint(1, 1));
		  var inv = new DOMMatrix().translate(10, 20).inverse().transformPoint(new DOMPoint(10, 20));
		  var path = new Path2D();
		  path.moveTo(0, 0); path.lineTo(5, 5);
		  document.getElementById('out').textContent = [
		    'rect=' + r.x + ',' + r.y + ',' + r.right + ',' + r.bottom,
		    'is2d=' + new DOMMatrix().is2D,
		    'tp=' + tp.x + ',' + tp.y,
		    'inv=' + Math.round(inv.x) + ',' + Math.round(inv.y),
		    'ops=' + path.__ops.length,
		    'quad=' + DOMQuad.fromRect({ x: 0, y: 0, width: 4, height: 4 }).getBounds().width
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{"rect=10,20,40,60", "is2d=true", "tp=7,9", "inv=0,0", "ops=2", "quad=4"} {
		if !strings.Contains(out, want) {
			t.Errorf("geometry missing %q:\n%s", want, out)
		}
	}
}

// --- Batch G: fonts / animation / misc ---

// TestFontsAnimationMisc proves document.fonts.ready resolves, Element.animate
// returns a finished animation whose onfinish fires (wrapped timer), reportError
// doesn't throw, and cookieStore reads document.cookie. The content is written
// from the innermost async callback — the settle proof for this batch.
func TestFontsAnimationMisc(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var steps = {};
		  reportError(new Error('handled-not-thrown'));
		  var ff = new FontFace('X', 'url(x.woff2)');
		  document.fonts.add(ff);
		  document.fonts.ready.then(function () {
		    steps.fonts = 'check=' + document.fonts.check('12px X') + ' size=' + document.fonts.size;
		    var el = document.getElementById('out');
		    var anim = el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200 });
		    steps.anim = 'state=' + anim.playState + ' n=' + el.getAnimations().length;
		    anim.onfinish = function () {
		      cookieStore.getAll().then(function (all) {
		        el.textContent = steps.fonts + ' | ' + steps.anim + ' | cookies=' + all.length + ' | finished';
		      });
		    };
		  });
		</script></body></html>`)
	for _, want := range []string{"check=true", "size=1", "state=finished", "n=1", "cookies=0", "finished"} {
		if !strings.Contains(out, want) {
			t.Errorf("fonts/animation/misc missing %q:\n%s", want, out)
		}
	}
}

// --- Batch H: encoding streams ---

// TestEncodingStreams round-trips 'AB' through TextEncoderStream -> bytes ->
// TextDecoderStream, proving both are wired over the buffer-backed Streams.
func TestEncodingStreams(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var enc = new TextEncoderStream();
		  var w = enc.writable.getWriter();
		  w.write('AB'); w.close();
		  enc.readable.getReader().read().then(function (r) {
		    var bytes = r.value;
		    var dec = new TextDecoderStream();
		    var dw = dec.writable.getWriter();
		    dw.write(bytes); dw.close();
		    dec.readable.getReader().read().then(function (r2) {
		      document.getElementById('out').textContent =
		        'enc=' + bytes[0] + ',' + bytes[1] + ' dec=' + r2.value + ' encoding=' + dec.encoding;
		    });
		  });
		</script></body></html>`)
	for _, want := range []string{"enc=65,66", "dec=AB", "encoding=utf-8"} {
		if !strings.Contains(out, want) {
			t.Errorf("encoding streams missing %q:\n%s", want, out)
		}
	}
}

// --- Batch I: custom element lifecycle + ElementInternals ---

// TestCustomElementLifecycle proves connectedCallback fires on insert,
// disconnectedCallback fires on removal (the new hook), and attachInternals
// returns an inert ElementInternals so form-associated components don't throw.
func TestCustomElementLifecycle(t *testing.T) {
	out := render(t, `<html><body><div id="host"></div><div id="out">?</div>
		<script>
		  var log = [];
		  class MyEl extends HTMLElement {
		    connectedCallback() { log.push('connected'); }
		    disconnectedCallback() { log.push('disconnected'); }
		    constructor() { super(); this._internals = this.attachInternals(); }
		  }
		  customElements.define('my-el', MyEl);
		  var host = document.getElementById('host');
		  var el = document.createElement('my-el');
		  host.appendChild(el);
		  var ok = el._internals && typeof el._internals.setFormValue === 'function';
		  host.removeChild(el);
		  document.getElementById('out').textContent = 'log=' + log.join(',') + ' internals=' + ok;
		</script></body></html>`)
	for _, want := range []string{"log=connected,disconnected", "internals=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("custom element lifecycle missing %q:\n%s", want, out)
		}
	}
}

// --- Batch J: Navigation API ---

// TestNavigationAPI proves navigate() fires the 'navigate' event, intercept()'s
// handler runs (the router's view update), the entry list advances, and state is
// carried. Content is written from finished.then — the settle proof.
func TestNavigationAPI(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var intercepted = false;
		  navigation.addEventListener('navigate', function (e) {
		    if (e.canIntercept) e.intercept({ handler: function () { intercepted = true; return Promise.resolve(); } });
		  });
		  navigation.navigate('/route/2', { state: { page: 2 } }).finished.then(function () {
		    document.getElementById('out').textContent = [
		      'intercepted=' + intercepted,
		      'state=' + navigation.currentEntry.getState().page,
		      'entries=' + navigation.entries().length,
		      'canBack=' + navigation.canGoBack,
		      'url2=' + (navigation.currentEntry.url.indexOf('/route/2') >= 0)
		    ].join(' ');
		  });
		</script></body></html>`)
	for _, want := range []string{"intercepted=true", "state=2", "entries=2", "canBack=true", "url2=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("Navigation API missing %q:\n%s", want, out)
		}
	}
}

// --- Batch K: broad crypto.subtle ---

// TestCryptoSubtle exercises the Go-backed WebCrypto: the SHA-256('abc') vector,
// an HMAC sign/verify round-trip, an AES-GCM encrypt/decrypt round-trip, PBKDF2
// deriveBits, and that an unsupported algorithm REJECTS (never a sync throw).
// The deep promise chain also proves the synchronous-resolve path is settle-safe.
func TestCryptoSubtle(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var enc = new TextEncoder();
		  var r = {};
		  function hex(buf) { var u = new Uint8Array(buf), s = ''; for (var i = 0; i < u.length; i++) { var h = u[i].toString(16); s += h.length === 1 ? '0' + h : h; } return s; }
		  crypto.subtle.digest('SHA-256', enc.encode('abc')).then(function (d) {
		    r.sha256 = hex(d);
		    return crypto.subtle.generateKey({ name: 'HMAC', hash: 'SHA-256' }, true, ['sign', 'verify']);
		  }).then(function (key) {
		    var msg = enc.encode('message');
		    return crypto.subtle.sign('HMAC', key, msg).then(function (sig) {
		      return crypto.subtle.verify('HMAC', key, sig, msg);
		    }).then(function (ok) {
		      r.hmac = ok;
		      return crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']);
		    });
		  }).then(function (aesKey) {
		    var iv = crypto.getRandomValues(new Uint8Array(12));
		    return crypto.subtle.encrypt({ name: 'AES-GCM', iv: iv }, aesKey, enc.encode('secret-data')).then(function (ct) {
		      return crypto.subtle.decrypt({ name: 'AES-GCM', iv: iv }, aesKey, ct);
		    });
		  }).then(function (dt) {
		    r.aes = new TextDecoder().decode(dt);
		    return crypto.subtle.importKey('raw', enc.encode('password'), { name: 'PBKDF2' }, false, ['deriveBits']);
		  }).then(function (pbk) {
		    return crypto.subtle.deriveBits({ name: 'PBKDF2', salt: enc.encode('salt'), iterations: 1000, hash: 'SHA-256' }, pbk, 128);
		  }).then(function (bits) {
		    r.pbkdf2Len = new Uint8Array(bits).length;
		    return crypto.subtle.generateKey({ name: 'RSASSA-PKCS1-v1_5' }, true, ['sign']).then(function () { return 'resolved'; }, function () { return 'rejected'; });
		  }).then(function (rsa) {
		    r.rsa = rsa;
		    document.getElementById('out').textContent =
		      'sha256=' + r.sha256 + ' hmac=' + r.hmac + ' aes=' + r.aes + ' pbkdf2Len=' + r.pbkdf2Len + ' rsa=' + r.rsa;
		  });
		</script></body></html>`)
	for _, want := range []string{
		"sha256=ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"hmac=true", "aes=secret-data", "pbkdf2Len=16", "rsa=rejected",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("crypto.subtle missing %q:\n%s", want, out)
		}
	}
}

// TestTier2LiveContext confirms every Phase 25 surface is installed in the
// persistent (interact/session) runtime too, not just one-shot Render.
func TestTier2LiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">?</div>
		<script>
		  document.getElementById('out').textContent = [
		    'matrix=' + (typeof DOMMatrix === 'function'),
		    'rect=' + (typeof DOMRect === 'function'),
		    'path=' + (typeof Path2D === 'function'),
		    'fonts=' + (typeof document.fonts.check === 'function'),
		    'animate=' + (typeof document.body.animate === 'function'),
		    'internals=' + (typeof document.createElement('x-y').attachInternals === 'function'),
		    'cookieStore=' + (typeof cookieStore.getAll === 'function'),
		    'reportError=' + (typeof reportError === 'function'),
		    'encStream=' + (typeof TextEncoderStream === 'function'),
		    'nav=' + (typeof navigation.navigate === 'function'),
		    'subtle=' + (typeof crypto.subtle.digest === 'function')
		  ].join(' ');
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	snap := snapshot(t, lc)
	for _, want := range []string{
		"matrix=true", "rect=true", "path=true", "fonts=true", "animate=true",
		"internals=true", "cookieStore=true", "reportError=true", "encStream=true", "nav=true",
		"subtle=true",
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("Tier 2 live context missing %q:\n%s", want, snap)
		}
	}
}
