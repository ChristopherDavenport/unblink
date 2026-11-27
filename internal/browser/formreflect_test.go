package browser_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/page"
)

// formSite serves pageHTML at / and records the raw body of every POST to /post.
func formSite(t *testing.T, pageHTML string) (srv *httptest.Server, posted func() string) {
	t.Helper()
	var mu sync.Mutex
	var body string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, pageHTML)
	})
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>Done</title></head><body><p>ok</p></body></html>`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return body
	}
}

func jsBrowser(t *testing.T) *browser.Browser {
	t.Helper()
	b, err := browser.New(browser.WithJS(3*time.Second), browser.WithJSAllowPrivate(true), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	return b
}

func fieldList(forms []page.Form, id string) string {
	for _, f := range forms {
		if f.ID != id {
			continue
		}
		var out []string
		for _, fld := range f.Fields {
			out = append(out, fld.Name+"="+fld.Value)
		}
		return strings.Join(out, " ")
	}
	return "<no form " + id + ">"
}

// What submit_form posts is what forms listed: fields that share a name are all
// sent, in order — a custom element's FormData entries, repeated inputs, checked
// checkboxes — while an unchecked checkbox or radio is not. A caller's value for a
// name replaces every entry of that name.
func TestSubmitSendsRepeatedNames(t *testing.T) {
	srv, posted := formSite(t, `<!doctype html><html><head><title>Tags</title></head><body>
	  <form id="t" action="/post" method="post">
	    <x-tags></x-tags>
	    <input name="line" value="one"><input name="line" value="two">
	    <input type="checkbox" name="opt" value="a" checked>
	    <input type="checkbox" name="opt" value="b">
	    <input type="checkbox" name="opt" value="c" checked>
	    <input type="checkbox" name="agree" checked>
	    <input type="radio" name="size" value="s"><input type="radio" name="size" value="m" checked><input type="radio" name="size" value="l">
	  </form>
	  <script>
	    class XTags extends HTMLElement {
	      static formAssociated = true;
	      constructor() { super(); this.internals = this.attachInternals(); }
	      connectedCallback() {
	        var fd = new FormData(); fd.append('tags', 'b'); fd.append('tags', 'a');
	        this.internals.setFormValue(fd);
	      }
	    }
	    customElements.define('x-tags', XTags);
	  </script></body></html>`)
	b, ctx := jsBrowser(t), context.Background()

	const sid = "repeat"
	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	forms, err := b.Forms(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("forms: %v", err)
	}
	if got := fieldList(forms, "t"); !strings.HasPrefix(got, "tags=b tags=a line=one line=two ") {
		t.Fatalf("forms listed %q", got)
	}
	if _, err := b.Submit(ctx, sid, "t", nil, nil, false); err != nil {
		t.Fatalf("submit: %v", err)
	}
	// url.Values.Encode orders names alphabetically and keeps each name's entry order.
	if got, want := posted(), "agree=on&line=one&line=two&opt=a&opt=c&size=m&tags=b&tags=a"; got != want {
		t.Errorf("posted %q\n  want %q", got, want)
	}

	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true}); err != nil {
		t.Fatalf("browse again: %v", err)
	}
	if _, err := b.Submit(ctx, sid, "t", map[string]string{"tags": "z", "opt": "b", "size": "l"}, nil, false); err != nil {
		t.Fatalf("submit with values: %v", err)
	}
	if got, want := posted(), "agree=on&line=one&line=two&opt=b&size=l&tags=z"; got != want {
		t.Errorf("posted with overrides %q\n  want %q", got, want)
	}
}

// A form-associated custom element inside a shadow root is not owned by a form
// outside it (nor can its form="id" reach one): the engine says so, and forms /
// submit_form — reading the composed tree, where the element sits inside that form —
// must say the same, on a one-shot render and on a live snapshot.
func TestShadowFormAssociatedElementsAreNotOwnedByOuterForm(t *testing.T) {
	srv, posted := formSite(t, `<!doctype html><html><head><title>Shadow</title></head><body>
	  <form id="f" action="/post" method="post">
	    <x-in name="light"></x-in>
	    <x-wrap></x-wrap>
	  </form>
	  <button id="poke">Poke</button>
	  <p id="engine"></p>
	  <script>
	    class XIn extends HTMLElement {
	      static formAssociated = true;
	      constructor() { super(); this.internals = this.attachInternals(); }
	      connectedCallback() { this.internals.setFormValue(this.getAttribute('name') + '-v'); }
	    }
	    customElements.define('x-in', XIn);
	    var sr = document.querySelector('x-wrap').attachShadow({ mode: 'open' });
	    var a = document.createElement('x-in'); a.setAttribute('name', 'inshadow');
	    var b = document.createElement('x-in'); b.setAttribute('name', 'shadowref'); b.setAttribute('form', 'f');
	    sr.appendChild(a); sr.appendChild(b);
	    function report() {
	      var f = document.getElementById('f');
	      document.getElementById('engine').textContent = 'engine: ' +
	        Array.prototype.map.call(f.elements, function (e) { return e.getAttribute('name'); }).join(',') +
	        ' owners=' + a.internals.form + ',' + b.internals.form;
	    }
	    report();
	    document.getElementById('poke').addEventListener('click', report);
	  </script></body></html>`)
	b, ctx := jsBrowser(t), context.Background()

	forms, err := b.Forms(ctx, browser.Request{URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("forms: %v", err)
	}
	if got := fieldList(forms, "f"); got != "light=light-v" {
		t.Errorf("one-shot forms listed %q, want only the light-tree element", got)
	}

	const sid = "shadow"
	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	if _, err := b.Interact(ctx, sid, "#poke", "click", "", ""); err != nil {
		t.Fatalf("interact: %v", err)
	}
	content, err := b.Read(ctx, browser.Request{SessionID: sid, UseCurrent: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(content.Markdown, "engine: light owners=null,null") {
		t.Fatalf("engine view unexpected:\n%s", content.Markdown)
	}
	forms, err = b.Forms(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("forms (live): %v", err)
	}
	if got := fieldList(forms, "f"); got != "light=light-v" {
		t.Errorf("live forms listed %q, want only the light-tree element", got)
	}
	if _, err := b.Submit(ctx, sid, "f", nil, nil, false); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := posted(); got != "light=light-v" {
		t.Errorf("posted %q, want light=light-v", got)
	}
}

// A live snapshot stamps the reflection and takes it back off. Where the page had
// written those attribute names on its own elements, taking it off means restoring
// what the page wrote — value and position — not deleting it: otherwise page JS can
// tell a snapshot happened.
func TestLiveSnapshotRestoresPageAuthoredMarkers(t *testing.T) {
	srv, _ := formSite(t, `<!doctype html><html><head><title>Markers</title></head><body>
	  <form id="f" action="/post" method="post" data-unblink-form-key="page-key">
	    <x-in id="x" name="n" data-unblink-form-value="n=forged" data-unblink-form-owner="page-owner"
	      data-unblink-default-role="page-role" data-unblink-default-aria-label="page-label"
	      data-unblink-default-labelledby-text="page-text" data-unblink-form-invalid="page-invalid" class="last"></x-in>
	  </form>
	  <span id="lbl">Engine label</span>
	  <button id="poke">Poke</button><button id="probe">Probe</button>
	  <p id="seen"></p>
	  <script>
	    class XIn extends HTMLElement {
	      static formAssociated = true;
	      constructor() { super(); this.internals = this.attachInternals(); }
	      connectedCallback() {
	        this.internals.setFormValue('real');
	        this.internals.setValidity({ customError: true }, 'bad');
	        this.internals.role = 'switch';
	        this.internals.ariaLabel = 'Engine';
	        this.internals.ariaLabelledByElements = [document.getElementById('lbl')];
	      }
	    }
	    customElements.define('x-in', XIn);
	    var x = document.getElementById('x'), f = document.getElementById('f'), records = 0;
	    new MutationObserver(function (list) { records += list.length; })
	      .observe(document.body, { attributes: true, subtree: true });
	    function state() {
	      return x.getAttributeNames().map(function (k) { return k + '=' + x.getAttribute(k); }).join(' ') +
	        ' / ' + f.getAttribute('data-unblink-form-key') + ' / ' + x.outerHTML.length;
	    }
	    var before = state();
	    document.getElementById('probe').addEventListener('click', function () {
	      var now = state();
	      // Reported a tick later so any (wrongly) queued mutation record is counted.
	      setTimeout(function () {
	        document.getElementById('seen').textContent =
	          'same=' + (now === before) + ' records=' + records + ' value=' + x.getAttribute('data-unblink-form-value');
	      }, 0);
	    });
	  </script></body></html>`)
	b, ctx := jsBrowser(t), context.Background()

	const sid = "markers"
	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL, Render: true}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	// Two snapshots (one per interact) before the page looks at itself.
	for _, sel := range []string{"#poke", "#poke", "#probe"} {
		if _, err := b.Interact(ctx, sid, sel, "click", "", ""); err != nil {
			t.Fatalf("interact %s: %v", sel, err)
		}
	}
	// The snapshot itself still carries the engine's values, not the page's.
	forms, err := b.Forms(ctx, browser.Request{SessionID: sid, UseCurrent: true})
	if err != nil {
		t.Fatalf("forms: %v", err)
	}
	if len(forms) != 1 || len(forms[0].Fields) != 1 {
		t.Fatalf("forms = %+v", forms)
	}
	if f := forms[0].Fields[0]; f.Name != "n" || f.Value != "real" || !f.Invalid {
		t.Errorf("field = %+v, want the engine's n=real, invalid", f)
	}
	content, err := b.Read(ctx, browser.Request{SessionID: sid, UseCurrent: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(content.Markdown, "same=true records=0 value=n=forged") {
		t.Errorf("page JS observed the reflection:\n%s", content.Markdown)
	}
}
