package js_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// dispatchKey fires a keyboard interaction through the live context, optionally
// setting a control value first.
func dispatchKey(t *testing.T, lc js.LiveContext, selector, event, value, key string) js.DispatchResult {
	t.Helper()
	res, err := lc.Dispatch(context.Background(), js.Action{Selector: selector, Type: event, Value: value, Key: key})
	if err != nil {
		t.Fatalf("dispatch key %s: %v", selector, err)
	}
	return res
}

// TestKeydownCarriesKeyFields proves event=keydown dispatches a real
// KeyboardEvent with key/code/keyCode/which, not the bare Event it used to.
func TestKeydownCarriesKeyFields(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<input id="q"><div id="out">?</div>
		<script>
		  document.getElementById('q').addEventListener('keydown', function (e) {
		    document.getElementById('out').textContent =
		      'key=' + e.key + ' code=' + e.code + ' keyCode=' + e.keyCode + ' which=' + e.which +
		      ' trusted=' + e.isTrusted + ' mod=' + e.getModifierState('Shift');
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	dispatchKey(t, lc, "#q", "keydown", "", "Enter")
	out := snapshot(t, lc)
	for _, want := range []string{"key=Enter", "code=Enter", "keyCode=13", "which=13", "trusted=true", "mod=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("keydown fields missing %q:\n%s", want, out)
		}
	}
}

// TestKeydownArrowAndChar proves a named non-printable key (ArrowDown) and a
// single printable character resolve to the right code/keyCode, and that a
// printable key appends to the control value and fires input.
func TestKeydownArrowAndChar(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<input id="q" value="ab"><div id="arrow">?</div><div id="typed">?</div>
		<script>
		  document.getElementById('q').addEventListener('keydown', function (e) {
		    if (e.key === 'ArrowDown') document.getElementById('arrow').textContent = 'code=' + e.code + ' kc=' + e.keyCode;
		  });
		  document.getElementById('q').addEventListener('input', function () {
		    document.getElementById('typed').textContent = 'val=' + document.getElementById('q').value;
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	dispatchKey(t, lc, "#q", "keydown", "", "ArrowDown")
	dispatchKey(t, lc, "#q", "keydown", "", "c") // printable: appends to "ab"
	out := snapshot(t, lc)
	for _, want := range []string{"code=ArrowDown kc=40", "val=abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("keydown arrow/char missing %q:\n%s", want, out)
		}
	}
}

// TestKeydownEnterSubmitsForm proves Enter on a control inside a form triggers
// implicit submission (the search-box gesture), surfaced as a pending navigation.
func TestKeydownEnterSubmitsForm(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<form action="/search" method="get">
		  <input id="q" name="term">
		</form>
		<script></script></body></html>`, 2*time.Second)
	defer cleanup()
	// Type a query, then press Enter — the whole search gesture in one interact.
	dispatchKey(t, lc, "#q", "keydown", "pomegranate", "Enter")
	nav, err := lc.PendingNavigation(context.Background())
	if err != nil {
		t.Fatalf("pending nav: %v", err)
	}
	if !strings.Contains(nav, "/search") || !strings.Contains(nav, "term=pomegranate") {
		t.Errorf("Enter did not submit the form with the typed value: nav=%q", nav)
	}
}

// TestKeydownEnterPreventDefaultSuppressesSubmit proves a handler that cancels
// the keydown stops implicit form submission (a real browser behavior).
func TestKeydownEnterPreventDefaultSuppressesSubmit(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<form action="/search" method="get">
		  <input id="q" name="term">
		</form>
		<script>
		  document.getElementById('q').addEventListener('keydown', function (e) {
		    if (e.key === 'Enter') e.preventDefault();
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	dispatchKey(t, lc, "#q", "keydown", "x", "Enter")
	nav, err := lc.PendingNavigation(context.Background())
	if err != nil {
		t.Fatalf("pending nav: %v", err)
	}
	if nav != "" {
		t.Errorf("preventDefault did not suppress implicit submit: nav=%q", nav)
	}
}

// TestKeydownDefaultsToEnter proves an empty key defaults to Enter (the common
// "press Enter" gesture) rather than dispatching a keyless event.
func TestKeydownDefaultsToEnter(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<input id="q"><div id="out">?</div>
		<script>
		  document.getElementById('q').addEventListener('keydown', function (e) {
		    document.getElementById('out').textContent = 'key=' + e.key;
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	dispatchKey(t, lc, "#q", "keydown", "", "")
	if out := snapshot(t, lc); !strings.Contains(out, "key=Enter") {
		t.Errorf("empty key did not default to Enter:\n%s", out)
	}
}
