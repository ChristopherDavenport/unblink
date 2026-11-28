package js_test

import (
	"bytes"
	"context"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// pageStateProbe writes every piece of page-dependent state the preludes and the
// installed globals expose. A pooled runtime is prepared before its page exists
// (Engine.prepare) and bound afterwards, so each of these has to be filled in at
// bind time; a value captured while the runtime was being prepared shows up here
// as an empty URL, a stale clock or a missing store.
const pageStateProbe = `<html><head></head><body><div id="out">x</div><script>
  var out = [];
  function put(k, v) { out.push(k + '=' + v); }
  put('href', location.href);
  put('origin', location.origin);
  put('path', location.pathname);
  put('search', location.search);
  put('winorigin', window.origin);
  put('docurl', document.URL);
  put('baseuri', document.baseURI);
  put('domain', document.domain);
  put('secure', window.isSecureContext);
  put('histlen', history.length);
  put('now', performance.now() < 1500);
  put('origin_age', (Date.now() - performance.timeOrigin) < 1500);
  put('navstart', performance.timing.navigationStart === performance.timeOrigin);
  put('naventry', performance.getEntriesByType('navigation')[0].name);
  put('navapi', (window.navigation && navigation.currentEntry) ? navigation.currentEntry.url : 'none');
  put('hooks', typeof __unblinkBind + '/' + typeof __unblinkOnBind);
  put('storeproto', Object.prototype.toString.call(localStorage));
  localStorage.setItem('k', 'stored');
  put('store', localStorage.getItem('k'));
  put('cookie', document.cookie);
  put('html', document.documentElement === document.querySelector('html'));
  put('parent', document.documentElement.parentNode === document);
  navigator.serviceWorker.getRegistration().then(function () {}, function () {});
  navigator.serviceWorker.ready.then(function (r) {
    put('swscope', r.scope);
    return fetch('/api').then(function (r) { return r.text(); });
  }).then(function (t) {
    put('fetch', t);
    setTimeout(function () {
      put('late', 'ran');
      document.getElementById('out').textContent = out.join(' | ');
    }, 60000);
  });
  document.getElementById('out').textContent = out.join(' | ');
</script></body></html>`

type poolStore map[string]string

func (s poolStore) Get(k string) (string, bool) { v, ok := s[k]; return v, ok }
func (s poolStore) Set(k, v string)             { s[k] = v }
func (s poolStore) Remove(k string)             { delete(s, k) }
func (s poolStore) Clear() {
	for k := range s {
		delete(s, k)
	}
}
func (s poolStore) Keys() []string {
	ks := make([]string, 0, len(s))
	for k := range s {
		ks = append(ks, k)
	}
	return ks
}

func waitPoolFull(t testing.TB, eng *js.Engine) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !eng.PoolFull() {
		if time.Now().After(deadline) {
			t.Fatal("pre-warm pool never filled")
		}
		time.Sleep(time.Millisecond)
	}
}

func renderProbe(t *testing.T, eng *js.Engine, store poolStore) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageStateProbe))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/dir/page?q=1")
	tr := &stubTransport{routes: map[string]string{"/api": "FETCHED-OK"}}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr, Storage: store, SessionStorage: poolStore{}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = html.Render(&buf, doc)
	out := buf.String()
	i := strings.Index(out, `<div id="out">`)
	j := strings.Index(out, "</div>")
	if i < 0 || j < i {
		t.Fatalf("no probe output in %s", out)
	}
	return out[i+len(`<div id="out">`) : j]
}

// TestPooledRuntimeIsBoundToItsPage renders the probe on a runtime that was
// prepared ahead of time and left idle, and on a cold one, and requires the same
// page state from both.
func TestPooledRuntimeIsBoundToItsPage(t *testing.T) {
	cold := js.New(js.WithTimeout(2 * time.Second))
	defer cold.Close()
	pooled := js.New(js.WithTimeout(2*time.Second), js.WithPrewarm(2))
	defer pooled.Close()
	waitPoolFull(t, pooled)
	// Let the prepared runtimes age: a clock captured at preparation would now be
	// visibly stale against the 1.5s bound the probe checks.
	time.Sleep(1600 * time.Millisecond)

	coldStore, pooledStore := poolStore{}, poolStore{}
	want := renderProbe(t, cold, coldStore)
	got := renderProbe(t, pooled, pooledStore)
	if got != want {
		t.Errorf("pooled runtime saw different page state\n cold:   %s\n pooled: %s", want, got)
	}
	for _, frag := range []string{
		"href=https://example.com/dir/page?q=1", "origin=https://example.com", "path=/dir/page", "search=?q=1",
		"winorigin=https://example.com", "docurl=https://example.com/dir/page?q=1", "secure=true", "histlen=1",
		"now=true", "origin_age=true", "navstart=true", "naventry=https://example.com/dir/page?q=1",
		"hooks=undefined/undefined", "storeproto=[object Storage]", "store=stored", "html=true", "parent=true",
		"swscope=https://example.com/", "fetch=FETCHED-OK", "late=ran",
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("pooled render missing %q in: %s", frag, got)
		}
	}
	if pooledStore["k"] != "stored" {
		t.Errorf("localStorage write did not reach the page's store: %v", pooledStore)
	}
}

// TestPooledRuntimeWithoutTransport: a runtime prepared with the network-backed
// fetch must fall back to the rejecting stubs when its page has no transport.
func TestPooledRuntimeWithoutTransport(t *testing.T) {
	for _, prewarm := range []int{0, 1} {
		eng := js.New(js.WithTimeout(2*time.Second), js.WithPrewarm(prewarm))
		if prewarm > 0 {
			waitPoolFull(t, eng)
		}
		doc, _ := html.Parse(strings.NewReader(`<html><body><div id="out">x</div><script>
		  fetch('/api').then(function () { document.getElementById('out').textContent = 'fetched'; },
		    function (e) { document.getElementById('out').textContent = 'rejected:' + e.message + ':' + typeof __unblinkFetch + ':' + (new XMLHttpRequest()).readyState; });
		</script></body></html>`))
		base, _ := url.Parse("https://example.com/")
		if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		_ = html.Render(&buf, doc)
		if !strings.Contains(buf.String(), "rejected:unblink: network is disabled in render:undefined:0") {
			t.Errorf("prewarm=%d: no-transport render did not get the rejecting stubs: %s", prewarm, buf.String())
		}
		eng.Close()
	}
}

// TestPoolCloseStopsPreparedRuntimes: a pooled runtime has a started loop, so
// closing the engine must stop the ones never handed out.
func TestPoolCloseStopsPreparedRuntimes(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		eng := js.New(js.WithPrewarm(4))
		waitPoolFull(t, eng)
		eng.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+4 {
		t.Errorf("goroutines leaked across engine close: %d before, %d after", before, n)
	}
}

// BenchmarkRenderMinimalPooled times a render that takes a prepared runtime: the
// request-path cost once the pool has paid for install and the preludes.
func BenchmarkRenderMinimalPooled(b *testing.B) {
	eng := js.New(js.WithPrewarm(2))
	defer eng.Close()
	base, _ := url.Parse("https://example.com/")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		waitPoolFull(b, eng)
		doc, _ := html.Parse(strings.NewReader(`<html><body><div id="app"></div><script>document.getElementById('app').textContent='ok'</script></body></html>`))
		b.StartTimer()
		if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
			b.Fatal(err)
		}
	}
}
