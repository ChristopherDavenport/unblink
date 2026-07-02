package js_test

import (
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// A JS bot-check interstitial of the shape Reddit serves: on DOMContentLoaded it
// reads document.forms[0], sets a hidden field via elements.namedItem, and calls
// requestSubmit() on a GET form. The engine must run all of that and record the
// serialized submission as a pending navigation (the whole point of form support).
func TestRenderChallengeFormSelfSubmits(t *testing.T) {
	page := `<html><body>
	  <form hidden method="GET" action="/page">
	    <input type="hidden" name="solution"/>
	    <input type="hidden" name="js_challenge" value="1"/>
	    <input type="hidden" name="token" value="abc123"/>
	  </form>
	  <script>
	    document.addEventListener("DOMContentLoaded", function () {
	      var f = document.forms[0];
	      f.elements.namedItem("solution").value = "deadbeef" + "deadbeef";
	      f.requestSubmit();
	    }, {once: true});
	  </script></body></html>`
	_, diag, _ := renderWaitDiag(t, page, js.Env{}, time.Second)
	nav := diag.PendingNavigation
	if nav == "" {
		t.Fatalf("challenge form did not submit; PendingNavigation empty")
	}
	for _, want := range []string{"/page?", "solution=deadbeefdeadbeef", "js_challenge=1", "token=abc123"} {
		if !strings.Contains(nav, want) {
			t.Errorf("PendingNavigation %q missing %q", nav, want)
		}
	}
}

// requestSubmit() fires a cancelable submit event; an onsubmit handler returning
// false cancels submission, so no navigation is recorded (classic content-attribute
// cancel convention).
func TestRenderFormSubmitCanceledByOnsubmit(t *testing.T) {
	page := `<html><body>
	  <form method="GET" action="/go"><input name="q" value="x"/></form>
	  <script>
	    var f = document.forms[0];
	    f.onsubmit = function () { return false; };
	    f.requestSubmit();
	  </script></body></html>`
	_, diag, _ := renderWaitDiag(t, page, js.Env{}, time.Second)
	if diag.PendingNavigation != "" {
		t.Errorf("onsubmit returning false should cancel submit, got %q", diag.PendingNavigation)
	}
}

// preventDefault() in an addEventListener('submit') handler also cancels.
func TestRenderFormSubmitCanceledByPreventDefault(t *testing.T) {
	page := `<html><body>
	  <form method="GET" action="/go"><input name="q" value="x"/></form>
	  <script>
	    var f = document.forms[0];
	    f.addEventListener("submit", function (e) { e.preventDefault(); });
	    f.requestSubmit();
	  </script></body></html>`
	_, diag, _ := renderWaitDiag(t, page, js.Env{}, time.Second)
	if diag.PendingNavigation != "" {
		t.Errorf("preventDefault should cancel submit, got %q", diag.PendingNavigation)
	}
}

// A GET submit replaces the action's query string with the serialized successful
// controls: named text inputs and checked boxes, never unchecked boxes or disabled
// controls.
func TestRenderFormGetSerialization(t *testing.T) {
	page := `<html><body>
	  <form method="GET" action="/search">
	    <input name="q" value="hello world"/>
	    <input type="checkbox" name="safe" value="1" checked/>
	    <input type="checkbox" name="nsfw" value="1"/>
	    <input name="ignored" value="x" disabled/>
	    <input value="noname"/>
	  </form>
	  <script>document.forms[0].submit();</script></body></html>`
	_, diag, _ := renderWaitDiag(t, page, js.Env{}, time.Second)
	nav := diag.PendingNavigation
	if !strings.Contains(nav, "/search?") {
		t.Fatalf("expected a GET navigation to /search, got %q", nav)
	}
	for _, want := range []string{"q=hello+world", "safe=1"} {
		if !strings.Contains(nav, want) {
			t.Errorf("nav %q missing %q", nav, want)
		}
	}
	for _, no := range []string{"nsfw", "ignored", "noname"} {
		if strings.Contains(nav, no) {
			t.Errorf("nav %q should not contain %q", nav, no)
		}
	}
}

// document.forms is a live collection: indexed access, length, and namedItem (by id
// or name). form.submit() on a plain <form> with no action targets the current URL.
func TestDocumentFormsCollection(t *testing.T) {
	page := `<html><body>
	  <form id="a" method="GET"><input name="one" value="1"/></form>
	  <form name="b" method="GET"><input name="two" value="2"/></form>
	  <script>
	    window.__len = document.forms.length;
	    window.__byIndex = document.forms[1] === document.forms.namedItem("b");
	    window.__byId = document.forms[0] === document.forms.namedItem("a");
	    document.forms.namedItem("b").submit();
	  </script></body></html>`
	out, diag, _ := renderWaitDiag(t, page, js.Env{}, time.Second)
	_ = out
	if !strings.Contains(diag.PendingNavigation, "two=2") {
		t.Errorf("namedItem('b').submit() should submit form b, got %q", diag.PendingNavigation)
	}
}
