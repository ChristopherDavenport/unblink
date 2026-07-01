package js_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRequireDisabled ensures page JavaScript cannot read host files via require().
// goja_nodejs's default require source loader is os.Open, so a .json module would be
// read from disk, parsed, and returned to the script — a read-local-file-then-exfil
// chain. After hardening, require() must reject every path.
func TestRequireDisabled(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"TOPSECRET"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// On a successful (vulnerable) require the div would hold the file's secret value;
	// when blocked it holds "blocked". The script source references the path and the
	// property name but never the secret, so the secret's absence is the real signal.
	page := fmt.Sprintf(`<html><body><div id="out">init</div><script>
	  var r = 'init';
	  try { r = String(require(%q).token); }
	  catch (e) { r = 'blocked'; }
	  document.getElementById('out').textContent = r;
	</script></body></html>`, secret)

	out := render(t, page)
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("require() leaked host file contents:\n%s", out)
	}
	if !strings.Contains(out, "blocked") {
		t.Fatalf("expected require() to be blocked, got:\n%s", out)
	}
}

// TestConsoleIsSafeNoop verifies the page gets a working (no-op) console: calls must
// not throw (real browsers always expose console) and must not reach the host stdout
// that is reserved for the MCP transport.
func TestConsoleIsSafeNoop(t *testing.T) {
	out := render(t, `<html><body><div id="out">no</div><script>
	  console.log('x'); console.error('y'); console.warn('z'); console.debug('d');
	  document.getElementById('out').textContent = 'ok';
	</script></body></html>`)
	if !strings.Contains(out, "ok") {
		t.Fatalf("console.* should be safe no-ops; script did not complete:\n%s", out)
	}
}
