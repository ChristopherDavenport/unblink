package browser_test

import (
	"context"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// interact, page JavaScript and extraction agree on a select's value: it is the
// selected option, kept in the tree.
func TestSelectValueIsOneState(t *testing.T) {
	srv := serveHTML(t, `<html><body><main><h1>Pick</h1><form id="f" action="/go">
		<select id="s" name="size"><option value="s">Small</option><option value="m">Medium</option><option value="l">Large</option></select>
		<p id="seen">-</p></form></main><script>
		document.getElementById('s').addEventListener('change', function (e) {
			document.getElementById('seen').textContent = 'js saw ' + e.target.value + ' at ' + e.target.selectedIndex;
		});
	</script></body></html>`)
	b := newJSBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)

	value := func() string {
		t.Helper()
		forms, err := b.Forms(ctx, browser.Request{SessionID: "s", UseCurrent: true})
		if err != nil {
			t.Fatalf("forms: %v", err)
		}
		for _, f := range forms {
			for _, fld := range f.Fields {
				if fld.Name == "size" {
					return fld.Value
				}
			}
		}
		t.Fatal("no size field")
		return ""
	}
	if got := value(); got != "s" {
		t.Errorf("before any choice the select shows its first option: got %q", got)
	}
	if _, err := b.Interact(ctx, "s", "#s", "change", "l", ""); err != nil {
		t.Fatalf("interact: %v", err)
	}
	if got := value(); got != "l" {
		t.Errorf("extraction after interact chose Large: got %q", got)
	}
	r, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true}, "full", 0, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "js saw l at 2"; !strings.Contains(r.Markdown, want) {
		t.Errorf("the page's handler should have read the same value (%q):\n%s", want, r.Markdown)
	}
}
