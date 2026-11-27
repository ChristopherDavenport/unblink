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

// A design-system form control — a form-associated custom element whose value lives
// in ElementInternals, not in any attribute or native <input name> — must be visible
// to the forms tool and carried by submit_form. The value travels JS heap → node tree
// (the engine's reflection) → form extraction; nothing below the engine reads JS.
func TestFormAssociatedCustomElementReachesFormsAndSubmit(t *testing.T) {
	const pageHTML = `<!doctype html><html><head><title>Signup</title></head><body>
	  <form id="signup" action="/signup" method="post">
	    <input type="hidden" name="csrf" value="tok">
	    <my-input name="email" required></my-input>
	    <my-input name="plan" default="pro"></my-input>
	  </form>
	  <button id="upgrade">Upgrade</button>
	  <script>
	    class MyInput extends HTMLElement {
	      static formAssociated = true;
	      constructor() {
	        super();
	        this.internals = this.attachInternals();
	        this.attachShadow({ mode: 'open' }).innerHTML = '<input>';
	      }
	      connectedCallback() {
	        if (this.hasAttribute('default')) this.internals.setFormValue(this.getAttribute('default'));
	      }
	    }
	    customElements.define('my-input', MyInput);
	    document.getElementById('upgrade').addEventListener('click', function () {
	      document.querySelector('my-input[name=plan]').internals.setFormValue('team');
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
	mux.HandleFunc("/signup", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>Done</title></head><body><p>email=%s plan=%s csrf=%s</p></body></html>`,
			r.PostFormValue("email"), r.PostFormValue("plan"), r.PostFormValue("csrf"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b, err := browser.New(browser.WithJS(3*time.Second), browser.WithJSAllowPrivate(true), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	ctx := context.Background()
	field := func(t *testing.T, forms []page.Form, name string) page.Field {
		t.Helper()
		if len(forms) != 1 {
			t.Fatalf("forms = %d, want 1", len(forms))
		}
		for _, f := range forms[0].Fields {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("no field %q in %+v", name, forms[0].Fields)
		return page.Field{}
	}

	// The forms tool lists both custom controls, with the value one of them set.
	forms, err := b.Forms(ctx, browser.Request{URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("forms: %v", err)
	}
	if f := field(t, forms, "email"); f.Type != "my-input" || f.Value != "" || !f.Required {
		t.Errorf("email field = %+v", f)
	}
	if f := field(t, forms, "plan"); f.Type != "my-input" || f.Value != "pro" {
		t.Errorf("plan field = %+v", f)
	}

	// A handler changing the value in the live page shows up in the next snapshot...
	const sid = "face"
	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	if _, err := b.Interact(ctx, sid, "#upgrade", "click", "", ""); err != nil {
		t.Fatalf("interact: %v", err)
	}
	forms, err = b.Forms(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("forms (current): %v", err)
	}
	if f := field(t, forms, "plan"); f.Value != "team" {
		t.Errorf("plan after interact = %+v, want value team", f)
	}
	// ...and submit_form sends the element's own value alongside the caller's.
	if _, err := b.Submit(ctx, sid, "signup", map[string]string{"email": "a@b.c"}, nil, false); err != nil {
		t.Fatalf("submit: %v", err)
	}
	content, err := b.Read(ctx, browser.Request{SessionID: sid, UseCurrent: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(content.Markdown, "email=a@b.c plan=team csrf=tok") {
		t.Errorf("submission did not carry the custom element's value:\n%s", content.Markdown)
	}
}
