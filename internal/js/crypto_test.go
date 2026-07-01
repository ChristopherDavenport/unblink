package js_test

import (
	"strings"
	"testing"
	"time"
)

// TestCryptoGlobalWorks proves the crypto shim (getRandomValues + randomUUID) is
// present and runs in the one-shot render path. goja provides no crypto global on its
// own, so uuid/nanoid/react-aria useId throw at import time without this — breaking
// hydration.
func TestCryptoGlobalWorks(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var id = crypto.randomUUID();
		  var buf = new Uint8Array(8);
		  var ret = crypto.getRandomValues(buf);
		  var dashes = (id[8] === '-') && (id[13] === '-') && (id[18] === '-') && (id[23] === '-');
		  document.getElementById("out").textContent =
		    "len=" + id.length + " dashes=" + dashes + " ret=" + (ret === buf);
		</script></body></html>`)
	for _, want := range []string{"len=36", "dashes=true", "ret=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("crypto test missing %q:\n%s", want, out)
		}
	}
}

// TestCryptoGlobalInLiveContext proves the same shim is installed in the persistent
// (interact/session) runtime, not just one-shot Render — preludeJS runs in both
// context.go (live) and engine.go (one-shot).
func TestCryptoGlobalInLiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">?</div>
		<script>document.getElementById('out').textContent = 'uuidlen:' + crypto.randomUUID().length;</script>
		</body></html>`, 2*time.Second)
	defer cleanup()
	if out := snapshot(t, lc); !strings.Contains(out, "uuidlen:36") {
		t.Errorf("crypto missing in live context:\n%s", out)
	}
}

// TestGojaParsesModernSyntaxNatively documents why unblink does NOT unconditionally
// downlevel classic scripts through esbuild: the pinned goja already parses ES2022
// private fields, logical assignment, and optional chaining natively. (An older goja
// could not — the premise of the dropped Phase 15 downleveling work; downleveling to
// esbuild's ES2017 target would route private fields through goja's buggy WeakMap and
// silently corrupt them, so it was not adopted.) The only esbuild pass on the classic
// path is a surgical dynamic-import() lowering (lowerDynamicImport) taken ONLY when
// goja fails to compile a bundle, pinned to Target ESNext + Supported{dynamic-import:
// false} so private fields et al. are still never routed through goja's WeakMap — see
// the *DynamicImport* tests. If a future goja bump regresses parsing, this canary fails
// and the decision can be revisited.
func TestGojaParsesModernSyntaxNatively(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>
		  class Counter { #n = 0; bump() { this.#n += 7; return this.#n; } }
		  var cfg = {}; cfg.label ??= "ok";
		  document.getElementById("out").textContent = cfg.label + "-" + new Counter().bump();
		</script></body></html>`)
	if !strings.Contains(out, "ok-7") || strings.Contains(out, "before") {
		t.Errorf("goja no longer runs modern classic-script syntax natively:\n%s", out)
	}
}
