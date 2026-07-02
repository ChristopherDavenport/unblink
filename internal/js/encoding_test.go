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
