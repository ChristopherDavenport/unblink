package browser_test

import (
	"context"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// A click on a checkbox or radio button changes it, as a browser's does: the
// state is flipped before the click event, a canceled click restores it, input
// and change follow, a radio button takes over its group, and a click on a
// label reaches the label's control.
func TestInteractClickActivatesCheckboxesAndRadios(t *testing.T) {
	srv := serveHTML(t, `<html><body><main><h1>Prefs</h1><form id="f">
		<input type="checkbox" id="news" name="news">
		<input type="checkbox" id="locked" name="locked">
		<input type="checkbox" id="off" name="off" disabled>
		<input type="radio" id="r1" name="plan" value="free" checked>
		<input type="radio" id="r2" name="plan" value="pro">
		<label id="lab" for="terms">I agree</label><input type="checkbox" id="terms" name="terms">
		<label id="wrap"><span id="inner">Wrapped</span><input type="checkbox" id="w" name="w"></label>
		<p id="log"></p></form></main><script>
		var log = [];
		function note(s) { log.push(s); document.getElementById('log').textContent = log.join(' '); }
		['news', 'r2', 'terms', 'w'].forEach(function (id) {
			var el = document.getElementById(id);
			el.addEventListener('click', function () { note(id + ':click=' + el.checked); });
			el.addEventListener('input', function () { note(id + ':input'); });
			el.addEventListener('change', function () { note(id + ':change=' + el.checked); });
		});
		document.getElementById('locked').addEventListener('click', function (e) { e.preventDefault(); note('locked:seen=' + e.target.checked); });
	</script></body></html>`)
	b := newJSBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)

	click := func(sel string) {
		t.Helper()
		if _, err := b.Interact(ctx, "s", sel, "click", "", ""); err != nil {
			t.Fatalf("interact %s: %v", sel, err)
		}
	}
	checked := func() map[string]bool {
		t.Helper()
		forms, err := b.Forms(ctx, browser.Request{SessionID: "s", UseCurrent: true})
		if err != nil {
			t.Fatalf("forms: %v", err)
		}
		out := map[string]bool{}
		for _, f := range forms {
			for _, fld := range f.Fields {
				key := fld.Name
				if fld.Type == "radio" {
					key += "=" + fld.Value
				}
				out[key] = fld.Checked == "true"
			}
		}
		return out
	}

	click("#news")
	click("#locked")
	click("#off")
	click("#r2")
	click("#lab")
	click("#inner")
	got := checked()
	for key, want := range map[string]bool{
		"news": true, "locked": false, "off": false, "plan=free": false, "plan=pro": true, "terms": true, "w": true,
	} {
		if got[key] != want {
			t.Errorf("%s checked = %v, want %v (all: %v)", key, got[key], want, got)
		}
	}
	r, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true}, "full", 0, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{
		"news:click=true news:input news:change=true",
		"locked:seen=true",
		"r2:click=true r2:input r2:change=true",
		"terms:click=true terms:input terms:change=true",
		"w:click=true w:input w:change=true",
	} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("page listeners did not see %q in:\n%s", want, r.Markdown)
		}
	}

	// A second click unchecks a checkbox; clicking the checked radio changes nothing.
	click("#news")
	click("#r2")
	got = checked()
	if got["news"] || !got["plan=pro"] {
		t.Errorf("after second clicks: news=%v plan=pro=%v, want false/true", got["news"], got["plan=pro"])
	}
}
