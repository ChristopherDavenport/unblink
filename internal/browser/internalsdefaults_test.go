package browser_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/page"
)

// A design-system page states its semantics through ElementInternals, not markup: a
// control that sits outside its form and names it with form="id", and a switch whose
// role, label and checked state exist only as internals defaults. Both must reach the
// forms / controls / browse output and submit_form, live changes included.
func TestInternalsSemanticsReachFormsAndControls(t *testing.T) {
	const pageHTML = `<!doctype html><html><head><title>Settings</title></head><body>
	  <form id="settings" action="/save" method="post"><input type="hidden" name="csrf" value="tok"></form>
	  <span id="caption">Dark mode</span>
	  <my-switch id="dark" name="dark" form="settings" tabindex="0"></my-switch>
	  <my-switch id="beta" name="beta" form="settings" tabindex="0" aria-label="Beta features"></my-switch>
	  <input name="nick" value="sam" form="settings">
	  <my-nav><a href="/account">Account</a></my-nav>
	  <script>
	    class MySwitch extends HTMLElement {
	      static formAssociated = true;
	      constructor() {
	        super();
	        this.internals = this.attachInternals();
	        this.internals.role = 'switch';
	        this.internals.ariaLabel = 'Unnamed switch';
	        this.set(false);
	        this.addEventListener('click', () => this.set(!this.on));
	      }
	      connectedCallback() {
	        if (this.id === 'dark') this.internals.ariaLabelledByElements = [document.getElementById('caption')];
	      }
	      set(on) {
	        this.on = on;
	        this.internals.ariaChecked = String(on);
	        this.internals.setFormValue(on ? 'on' : null);
	      }
	    }
	    customElements.define('my-switch', MySwitch);
	    customElements.define('my-nav', class extends HTMLElement {
	      constructor() { super(); var i = this.attachInternals(); i.role = 'navigation'; i.ariaLabel = 'Account menu'; }
	    });
	  </script></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, pageHTML)
	})
	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>Saved</title></head><body><p>dark=%s nick=%s csrf=%s</p></body></html>`,
			r.PostFormValue("dark"), r.PostFormValue("nick"), r.PostFormValue("csrf"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b, err := browser.New(browser.WithJS(3*time.Second), browser.WithJSAllowPrivate(true), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	ctx := context.Background()
	control := func(t *testing.T, controls []page.Control, sel string) page.Control {
		t.Helper()
		for _, c := range controls {
			if c.Selector == sel {
				return c
			}
		}
		t.Fatalf("no control %q in %+v", sel, controls)
		return page.Control{}
	}
	fieldNames := func(forms []page.Form) string {
		if len(forms) != 1 {
			t.Fatalf("forms = %d, want 1", len(forms))
		}
		var names []string
		for _, f := range forms[0].Fields {
			names = append(names, f.Name+"="+f.Value+"["+f.Checked+"]")
		}
		return strings.Join(names, " ")
	}

	// One-shot: the form owns the controls that name it, and the switches carry the
	// role, name and state their internals declare. An explicit aria-label wins.
	forms, err := b.Forms(ctx, browser.Request{URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("forms: %v", err)
	}
	if got, want := fieldNames(forms), "csrf=tok[] dark=[false] beta=[false] nick=sam[]"; got != want {
		t.Errorf("fields = %q, want %q", got, want)
	}
	controls, err := b.Controls(ctx, browser.Request{URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("controls: %v", err)
	}
	if c := control(t, controls, "#dark"); c.Role != "switch" || c.Text != "Dark mode" || c.Checked != "false" {
		t.Errorf("#dark = %+v", c)
	}
	if c := control(t, controls, "#beta"); c.Role != "switch" || c.Text != "Beta features" {
		t.Errorf("#beta = %+v", c)
	}

	// Live: browse sees the same semantics (here a landmark that exists only as
	// internals.role), and toggling the switch changes what the next snapshot reports.
	const sid = "internals"
	res, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if res.Counts.Controls != 2 || len(res.Regions) != 1 || res.Regions[0].Role != "navigation" ||
		res.Regions[0].Label != "Account menu" || res.Regions[0].Links != 1 {
		t.Errorf("browse counts = %+v regions = %+v; want 2 controls and the internals-declared landmark", res.Counts, res.Regions)
	}
	if _, err := b.Interact(ctx, sid, "#dark", "click", "", ""); err != nil {
		t.Fatalf("interact: %v", err)
	}
	controls, err = b.Controls(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("controls (current): %v", err)
	}
	if c := control(t, controls, "#dark"); c.Checked != "true" {
		t.Errorf("#dark after click = %+v, want checked", c)
	}
	forms, err = b.Forms(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("forms (current): %v", err)
	}
	if got, want := fieldNames(forms), "csrf=tok[] dark=on[true] beta=[false] nick=sam[]"; got != want {
		t.Errorf("fields after click = %q, want %q", got, want)
	}

	// submit_form carries the out-of-form controls.
	if _, err := b.Submit(ctx, sid, "settings", nil, nil, false); err != nil {
		t.Fatalf("submit: %v", err)
	}
	content, err := b.Read(ctx, browser.Request{SessionID: sid, UseCurrent: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(content.Markdown, "dark=on nick=sam csrf=tok") {
		t.Errorf("submission missed a control that names its form by id:\n%s", content.Markdown)
	}
}
