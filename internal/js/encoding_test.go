package js_test

import (
	"strings"
	"testing"
)

// TestBase64RoundTrip proves atob/btoa exist and agree, and that invalid input
// throws instead of returning garbage. Bundles use these for inline data
// (source maps, JWT payloads, data: URIs) without feature detection.
func TestBase64RoundTrip(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var enc = btoa('hello unblink');
		  var dec = atob(enc);
		  var threwRange = false, threwBad = false;
		  try { btoa('€'); } catch (e) { threwRange = true; }
		  try { atob('!!!not-base64!!!'); } catch (e) { threwBad = true; }
		  var ws = atob('aGVs\n bG8=');
		  document.getElementById('out').textContent =
		    'enc=' + enc + ' dec=' + dec + ' range=' + threwRange + ' bad=' + threwBad + ' ws=' + ws;
		</script></body></html>`)
	for _, want := range []string{"enc=aGVsbG8gdW5ibGluaw==", "dec=hello unblink", "range=true", "bad=true", "ws=hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("base64 missing %q:\n%s", want, out)
		}
	}
}

// TestTextEncoderDecoder round-trips ASCII, BMP, and astral-plane text through
// the pure-JS UTF-8 codec, and checks lone-surrogate/invalid-byte handling
// degrades to U+FFFD rather than throwing.
func TestTextEncoderDecoder(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var te = new TextEncoder(), td = new TextDecoder();
		  var ascii = td.decode(te.encode('plain')) === 'plain';
		  var bmp = td.decode(te.encode('café €')) === 'café €';
		  var astral = td.decode(te.encode('😀')) === '😀'; // U+1F600
		  var enc = te.encode('€');
		  var bytes = enc.length === 3 && enc[0] === 0xE2 && enc[1] === 0x82 && enc[2] === 0xAC;
		  var bad = td.decode(new Uint8Array([0x61, 0xFF, 0x62]));
		  var replaced = bad.charCodeAt(1) === 0xFFFD && bad.length === 3;
		  var ab = td.decode(te.encode('buf').buffer) === 'buf';
		  document.getElementById('out').textContent =
		    'ascii=' + ascii + ' bmp=' + bmp + ' astral=' + astral + ' bytes=' + bytes + ' replaced=' + replaced + ' ab=' + ab;
		</script></body></html>`)
	for _, want := range []string{"ascii=true", "bmp=true", "astral=true", "bytes=true", "replaced=true", "ab=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("TextEncoder/Decoder missing %q:\n%s", want, out)
		}
	}
}

// TestTextDecoderConformance locks the WHATWG-correctness fixes: fatal mode throws
// (and is reflected), ignoreBOM/BOM stripping, label normalization (utf-8 aliases
// canonicalize, valid legacy labels are accepted, invalid labels throw RangeError),
// and lone-surrogate encoding as U+FFFD (not WTF-8).
func TestTextDecoderConformance(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div><script>
	  var parts = [];
	  // fatal mode throws on malformed input; non-fatal substitutes.
	  var fd = new TextDecoder('utf-8', { fatal: true });
	  parts.push('fatalAttr:' + fd.fatal);
	  try { fd.decode(new Uint8Array([0xFF])); parts.push('fatalThrew:false'); }
	  catch (e) { parts.push('fatalThrew:' + (e instanceof TypeError)); }
	  parts.push('nonFatal:' + (new TextDecoder().decode(new Uint8Array([0xFF])).charCodeAt(0) === 0xFFFD));
	  // BOM: stripped by default, kept with ignoreBOM.
	  var bom = new Uint8Array([0xEF, 0xBB, 0xBF, 0x41]);
	  parts.push('bomStrip:' + (new TextDecoder().decode(bom) === 'A'));
	  parts.push('bomKeep:' + (new TextDecoder('utf-8', { ignoreBOM: true }).decode(bom).charCodeAt(0) === 0xFEFF));
	  // label normalization.
	  parts.push('alias:' + new TextDecoder('unicode-1-1-utf-8').encoding);
	  parts.push('legacy:' + new TextDecoder('iso-8859-2').encoding);
	  try { new TextDecoder('utf-7'); parts.push('invalidThrew:false'); }
	  catch (e) { parts.push('invalidThrew:' + (e instanceof RangeError)); }
	  // lone surrogate -> U+FFFD (EF BF BD), not WTF-8 (ED ...).
	  var lone = new TextEncoder().encode('\uD800');
	  parts.push('surrogate:' + (lone[0] === 0xEF && lone[1] === 0xBF && lone[2] === 0xBD));
	  document.getElementById('out').textContent = parts.join('|');
	</script></body></html>`)
	for _, want := range []string{
		"fatalAttr:true", "fatalThrew:true", "nonFatal:true",
		"bomStrip:true", "bomKeep:true",
		"alias:utf-8", "legacy:iso-8859-2", "invalidThrew:true",
		"surrogate:true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("TextDecoder conformance missing %q:\n%s", want, out)
		}
	}
}

// TestPerformanceNow proves performance.now/timeOrigin/mark/measure exist —
// bundles call performance.now() unguarded, and its absence threw before
// Phase 21.
func TestPerformanceNow(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var a = performance.now();
		  var monotonic = typeof a === 'number' && a >= 0;
		  performance.mark('start');
		  performance.mark('end');
		  var m = performance.measure('span', 'start', 'end');
		  var marks = performance.getEntriesByType('mark').length === 2;
		  var named = performance.getEntriesByName('span').length === 1;
		  performance.clearMarks();
		  var cleared = performance.getEntriesByType('mark').length === 0;
		  var origin = performance.timeOrigin > 0;
		  var po = new PerformanceObserver(function () {});
		  po.observe({ entryTypes: ['longtask'] }); po.disconnect();
		  document.getElementById('out').textContent =
		    'mono=' + monotonic + ' marks=' + marks + ' named=' + named + ' cleared=' + cleared +
		    ' origin=' + origin + ' dur=' + (typeof m.duration === 'number');
		</script></body></html>`)
	for _, want := range []string{"mono=true", "marks=true", "named=true", "cleared=true", "origin=true", "dur=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("performance missing %q:\n%s", want, out)
		}
	}
}
