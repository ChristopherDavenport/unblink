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
)

// TestSessionStorageOriginPartitioned proves the Same-Origin Policy for Web
// Storage (ADR 0015): within one session, a page on origin B cannot read the
// localStorage a page on origin A wrote, while each origin's storage still
// persists across navigations back to it. Two loopback ports = two origins.
func TestSessionStorageOriginPartitioned(t *testing.T) {
	// Origin A reads any prior value, then writes its own secret.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body><p id="out"></p><script>
			var prev = localStorage.getItem('secret');
			localStorage.setItem('secret', 'from-A');
			document.getElementById('out').textContent = 'A-prev=' + prev;
		</script></body></html>`)
	}))
	defer srvA.Close()
	// Origin B reports whatever it can read (must be null — partitioned from A).
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body><p id="out"></p><script>
			document.getElementById('out').textContent = 'B-sees=' + localStorage.getItem('secret');
		</script></body></html>`)
	}))
	defer srvB.Close()

	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	const sid = "tab"

	rA, err := b.Read(ctx, browser.Request{SessionID: sid, URL: srvA.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rA.Markdown, "A-prev=null") {
		t.Errorf("first A visit should see no prior value; got %q", rA.Markdown)
	}
	// Same session, different origin: must NOT see A's secret.
	rB, err := b.Read(ctx, browser.Request{SessionID: sid, URL: srvB.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rB.Markdown, "B-sees=null") {
		t.Errorf("origin B leaked origin A's localStorage (SOP violation): %q", rB.Markdown)
	}
	// Back to A: its own storage persisted across the cross-origin hop.
	rA2, err := b.Read(ctx, browser.Request{SessionID: sid, URL: srvA.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rA2.Markdown, "A-prev=from-A") {
		t.Errorf("origin A's own localStorage should persist across navigation; got %q", rA2.Markdown)
	}
}

// The live-runtime cap must hold: opening an (N+1)th live context tears down
// the least-recently-used one; the evicted session keeps its page and interact
// still works there (it reopens).
func TestLiveRuntimeCap(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><body><button id="b" onclick="this.textContent='clicked'">go</button></body></html>`
	})
	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true), browser.WithJSMaxLive(2))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		sid := fmt.Sprintf("s%d", i)
		if _, err := b.Read(ctx, browser.Request{SessionID: sid, URL: srv.URL}, "", 0, ""); err != nil {
			t.Fatalf("read %s: %v", sid, err)
		}
		if _, err := b.Interact(ctx, sid, "#b", "click", "", ""); err != nil {
			t.Fatalf("interact %s: %v", sid, err)
		}
	}

	live := 0
	for _, st := range b.SessionList() {
		if st.LiveJS {
			live++
		}
	}
	if live > 2 {
		t.Errorf("live runtimes = %d, want <= 2 (cap)", live)
	}
	// The evicted session still works: interact reopens a runtime from its page.
	if r, err := b.Interact(ctx, "s0", "#b", "click", "", ""); err != nil || !r.Matched {
		t.Errorf("interact on evicted-live session: r=%+v err=%v", r, err)
	}
}

// localStorage AND sessionStorage persist across a session's renders (a session
// is a tab: both areas survive navigations within it), with separate keyspaces,
// so SPA auth/state flows survive between calls.
func TestSessionStoragePersists(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><body><p id="out"></p><script>
			var n = parseInt(localStorage.getItem("visits") || "0", 10) + 1;
			localStorage.setItem("visits", String(n));
			var m = parseInt(sessionStorage.getItem("svisits") || "0", 10) + 1;
			sessionStorage.setItem("svisits", String(m));
			// The two areas must not share a keyspace.
			var bleed = sessionStorage.getItem("visits") !== null || localStorage.getItem("svisits") !== null;
			document.getElementById("out").textContent =
				"local-" + n + " session-" + m + (bleed ? " BLEED" : "");
		</script></body></html>`
	})
	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	req := browser.Request{SessionID: "tab", URL: srv.URL, Render: true}
	if _, err := b.Read(ctx, req, "full", 0, ""); err != nil {
		t.Fatal(err)
	}
	r2, err := b.Read(ctx, req, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r2.Markdown, "local-2") || !strings.Contains(r2.Markdown, "session-2") {
		t.Errorf("second render should see both persisted stores; got %q", r2.Markdown)
	}
	if strings.Contains(r2.Markdown, "BLEED") {
		t.Error("localStorage and sessionStorage must have separate keyspaces")
	}

	// A different session gets fresh stores.
	r3, err := b.Read(ctx, browser.Request{SessionID: "other-tab", URL: srv.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r3.Markdown, "local-1") || !strings.Contains(r3.Markdown, "session-1") {
		t.Errorf("a new session should start with empty stores; got %q", r3.Markdown)
	}
}

// The benign globals must not throw and must behave sanely: structuredClone
// really clones, WebSocket reports error->close instead of crashing, Worker
// constructs, document.write appends, and fragment navigation fires hashchange.
func TestBenignGlobals(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><body><p id="out"></p><script>
			var out = [];
			try {
				var src = { a: [1, 2], d: new Date(0), m: new Map([["k", "v"]]) };
				src.self = src; // cycle
				var c = structuredClone(src);
				out.push("clone:" + (c !== src && c.a[1] === 2 && c.m.get("k") === "v" && c.self === c));
			} catch (e) { out.push("clone:threw"); }
			try {
				var w = new Worker("x.js"); w.postMessage("hi");
				out.push("worker:ok");
			} catch (e) { out.push("worker:threw"); }
			document.write("<p>written-content</p>");
			window.addEventListener("hashchange", function (e) { out.push("hash:" + location.hash); render(); });
			var ws = new WebSocket("wss://example.com/feed");
			ws.onclose = function (e) { out.push("ws-close:" + e.code); render(); };
			function render() { document.getElementById("out").textContent = out.join(" "); }
			render();
			location.href = "#section";
		</script></body></html>`
	})
	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"clone:true", "worker:ok", "ws-close:1006", "hash:#section", "written-content"} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s\njs_errors: %v", want, r.Markdown, r.JSErrors)
		}
	}
	if len(r.JSErrors) > 0 {
		t.Errorf("unexpected js errors: %v", r.JSErrors)
	}
}

// click/submit with render=true run the destination page's JavaScript — and a
// throwing script is visible on the result, same contract as read.
func TestClickRendersDestination(t *testing.T) {
	srv := serveDynamicHTML(t, func(r *http.Request) string {
		if r.URL.Path == "/dest" {
			return `<html><body><p>dest</p><script>
				var a = document.createElement("a");
				a.href = "/added-by-js"; // property write must reflect to the attribute
				a.textContent = "js link";
				document.body.appendChild(a);
				throw new Error("boom on dest");
			</script></body></html>`
		}
		return `<html><body><a href="/dest">go</a></body></html>`
	})
	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	if _, err := b.Read(ctx, browser.Request{SessionID: "nav", URL: srv.URL}, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	r, err := b.Click(ctx, "nav", 0, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts.Links != 1 {
		t.Errorf("rendered click should see the JS-added link: counts=%+v", r.Counts)
	}
	if len(r.JSErrors) == 0 || !strings.Contains(strings.Join(r.JSErrors, " "), "boom on dest") {
		t.Errorf("rendered click should surface the destination's JS error: %v", r.JSErrors)
	}
}
