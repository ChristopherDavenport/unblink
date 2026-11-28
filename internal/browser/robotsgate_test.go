package browser_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/session"
)

const gateRobots = "User-agent: *\nDisallow: /private\nAllow: /private/public\nDisallow: /*?debug=\n" +
	"Crawl-delay: 0\n"

// gateSite is a host for the robots.txt enforcement tests. It counts every
// request by path, so a test can prove a refused URL was never requested.
type gateSite struct {
	*httptest.Server

	mu           sync.Mutex
	hits         map[string]int
	robotsStatus int    // 0 or 200: serve robotsBody; anything else: that status
	robotsBody   string // served as text/plain
	robotsAuth   []string
	homeLinks    string // extra HTML appended to /home (e.g. cross-origin links)
}

func newGateSite(t *testing.T, robotsBody string) *gateSite {
	t.Helper()
	g := &gateSite{hits: map[string]int{}, robotsBody: robotsBody}
	page := func(title, body string) string {
		return `<!doctype html><html><head><title>` + title + `</title></head><body><h1>` + title +
			`</h1><p>Page content for ` + title + `, with enough prose to read as an article body.</p>` +
			body + `</body></html>`
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.hits[r.URL.Path]++
		status, body, extra := g.robotsStatus, g.robotsBody, g.homeLinks
		if r.URL.Path == "/robots.txt" {
			g.robotsAuth = append(g.robotsAuth, r.Header.Get("Authorization")+r.Header.Get("Cookie"))
		}
		g.mu.Unlock()

		switch r.URL.Path {
		case "/robots.txt":
			if status != 0 && status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, body)
		case "/llms.txt", "/llms-full.txt", "/sitemap.xml":
			http.NotFound(w, r)
		case "/private/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
				`<url><loc>%s/from-private-sitemap</loc></url></urlset>`, g.URL)
		case "/to-private":
			http.Redirect(w, r, "/private/secret", http.StatusFound)
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("Home",
				`<a href="/open">Open page</a> <a href="/private/secret">Secret page</a> `+
					`<a href="/to-private">Sneaky redirect</a> `+extra+
					`<form id="ok" action="/open" method="get"><input type="text" name="q" value="x"></form>`+
					`<form id="getblocked" action="/private/search" method="get"><input type="text" name="q" value="x"></form>`+
					`<form id="postblocked" action="/private/post" method="post"><input type="text" name="q" value="x"></form>`))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("Page "+r.URL.Path, ""))
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gateSite) count(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hits[path]
}

func (g *gateSite) set(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

func newRobotsBrowser(t *testing.T, extra ...browser.Option) *browser.Browser {
	t.Helper()
	opts := append([]browser.Option{
		browser.WithAllowPrivate(true), browser.WithRespectRobots(true), browser.WithRetries(0),
	}, extra...)
	b, err := browser.New(opts...)
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

// wantRefused asserts err is a robots refusal and returns its detail.
func wantRefused(t *testing.T, what string, err error) *browser.RobotsError {
	t.Helper()
	if !errors.Is(err, browser.ErrRobotsDisallowed) {
		t.Fatalf("%s: err = %v, want ErrRobotsDisallowed", what, err)
	}
	var re *browser.RobotsError
	if !errors.As(err, &re) {
		t.Fatalf("%s: err = %v, want a *RobotsError", what, err)
	}
	return re
}

func TestRespectRobotsRead(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t)
	ctx := context.Background()

	// Disallowed: one-shot and through a session, by plain path, by query rule,
	// and by an equivalent percent-encoded spelling of the path.
	_, err := b.Read(ctx, req(g.URL+"/private/secret"), "full", 0, "")
	re := wantRefused(t, "one-shot read", err)
	if re.Rule != "/private" || re.Unreachable || re.URL != g.URL+"/private/secret" {
		t.Errorf("refusal detail = %+v", re)
	}
	_, err = b.Read(ctx, browser.Request{SessionID: "s", URL: g.URL + "/private/secret"}, "full", 0, "")
	wantRefused(t, "session read", err)
	_, err = b.Read(ctx, req(g.URL+"/open?debug=1"), "full", 0, "")
	if re := wantRefused(t, "query rule", err); re.Rule != "/*?debug=" {
		t.Errorf("query refusal rule = %q", re.Rule)
	}
	_, err = b.Read(ctx, req(g.URL+"/%70rivate/secret"), "full", 0, "")
	wantRefused(t, "percent-encoded path", err)
	for _, tool := range []struct {
		name string
		call func() error
	}{
		{"browse", func() error { _, err := b.Browse(ctx, req(g.URL+"/private/secret")); return err }},
		{"links", func() error { _, err := b.Links(ctx, req(g.URL+"/private/secret"), "", false); return err }},
		{"forms", func() error { _, err := b.Forms(ctx, req(g.URL+"/private/secret")); return err }},
		{"find", func() error { _, err := b.Find(ctx, req(g.URL+"/private/secret"), "content", 5); return err }},
		{"data", func() error { _, err := b.Data(ctx, req(g.URL+"/private/secret"), ""); return err }},
		{"one-shot auth read", func() error {
			r := req(g.URL + "/private/secret")
			r.Auth = &browser.RequestAuth{Bearer: "tok"}
			_, err := b.Read(ctx, r, "full", 0, "")
			return err
		}},
	} {
		wantRefused(t, tool.name, tool.call())
	}
	if n := g.count("/private/secret"); n != 0 {
		t.Fatalf("a refused URL was requested %d time(s)", n)
	}
	if n := g.count("/open"); n != 0 {
		t.Fatalf("/open?debug=1 was requested %d time(s)", n)
	}

	// Allowed: an unrelated path, and one a longer Allow rule carves out.
	for _, path := range []string{"/open", "/private/public/page"} {
		r, err := b.Read(ctx, req(g.URL+path), "full", 0, "")
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(r.Markdown, "Page content for Page "+path) {
			t.Errorf("read %s returned %q", path, r.Markdown)
		}
	}
	// The gate needs only robots.txt: one probe for the origin, no llms probes.
	if n := g.count("/robots.txt"); n != 1 {
		t.Errorf("robots.txt fetched %d times, want 1 (cached per origin)", n)
	}
	if n := g.count("/llms.txt") + g.count("/llms-full.txt"); n != 0 {
		t.Errorf("enforcement probed llms files %d time(s)", n)
	}
}

// With the option off (the default) nothing is gated and the output carries no
// enforcement marker.
func TestRobotsNotEnforcedByDefault(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newBrowser(t)
	t.Cleanup(b.Close)
	ctx := context.Background()

	r, err := b.Read(ctx, req(g.URL+"/private/secret"), "full", 0, "")
	if err != nil {
		t.Fatalf("default read of a disallowed path: %v", err)
	}
	if !strings.Contains(r.Markdown, "Page content for Page /private/secret") {
		t.Errorf("content = %q", r.Markdown)
	}
	s, err := b.Site(ctx, req(g.URL+"/private/secret"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Robots.AllowedForUs || s.Robots.Enforced {
		t.Errorf("robots = %+v, want allowed_for_us=false, enforced=false", s.Robots)
	}
	js, _ := json.Marshal(s)
	if strings.Contains(string(js), "enforced") {
		t.Errorf("default site JSON mentions enforcement: %s", js)
	}
	m, err := b.Map(ctx, req(g.URL+"/home"), 50, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.SkippedByRobots != 0 || g.count("/private/secret") == 0 {
		t.Errorf("default map skipped=%d, /private/secret hits=%d; want no skipping", m.SkippedByRobots, g.count("/private/secret"))
	}
}

// A redirect from an allowed path to a disallowed one is stopped at the hop:
// the target is never requested and no content comes back.
func TestRespectRobotsRedirect(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t)
	ctx := context.Background()

	r, err := b.Read(ctx, req(g.URL+"/to-private"), "full", 0, "")
	re := wantRefused(t, "redirected read", err)
	if r != nil {
		t.Errorf("a refused redirect returned content: %+v", r)
	}
	if re.URL != g.URL+"/private/secret" {
		t.Errorf("refused URL = %q, want the redirect target", re.URL)
	}
	_, err = b.Read(ctx, browser.Request{SessionID: "s", URL: g.URL + "/to-private"}, "full", 0, "")
	wantRefused(t, "redirected session read", err)
	// Asked again: still refused (nothing about the redirect was cached as a page).
	_, err = b.Read(ctx, req(g.URL+"/to-private"), "full", 0, "")
	wantRefused(t, "repeat redirected read", err)

	if n := g.count("/private/secret"); n != 0 {
		t.Fatalf("redirect target was requested %d time(s)", n)
	}
	if n := g.count("/to-private"); n != 3 {
		t.Errorf("/to-private requested %d times, want 3 (one per call, no retry)", n)
	}
}

func TestRespectRobotsClickAndSubmit(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t)
	ctx := context.Background()
	home := browser.Request{SessionID: "s", URL: g.URL + "/home"}
	if _, err := b.Browse(ctx, home); err != nil {
		t.Fatalf("browse home: %v", err)
	}

	_, err := b.Click(ctx, "s", 0, "Secret page", false)
	wantRefused(t, "click to a disallowed link", err)
	_, err = b.Click(ctx, "s", 0, "Sneaky redirect", false)
	wantRefused(t, "click through a redirect", err)
	_, err = b.Submit(ctx, "s", "getblocked", map[string]string{"q": "v"}, nil, false)
	if re := wantRefused(t, "GET form to a disallowed action", err); re.URL != g.URL+"/private/search?q=v" {
		t.Errorf("refused form URL = %q", re.URL)
	}
	_, err = b.Submit(ctx, "s", "postblocked", nil, nil, false)
	wantRefused(t, "POST form to a disallowed action", err)
	for _, p := range []string{"/private/secret", "/private/search", "/private/post"} {
		if n := g.count(p); n != 0 {
			t.Errorf("%s was requested %d time(s)", p, n)
		}
	}
	// A refusal leaves the session where it was.
	st, err := b.SessionState("s")
	if err != nil || !strings.HasSuffix(st.CurrentURL, "/home") {
		t.Fatalf("session state = %+v, %v; want still on /home", st, err)
	}

	if _, err := b.Submit(ctx, "s", "ok", nil, nil, false); err != nil {
		t.Fatalf("submit to an allowed action: %v", err)
	}
	if _, err := b.Browse(ctx, home); err != nil {
		t.Fatal(err)
	}
	if res, err := b.Click(ctx, "s", 0, "Open page", false); err != nil || !strings.Contains(res.Title, "/open") {
		t.Fatalf("click to an allowed link: %+v, %v", res, err)
	}
}

// map skips what robots.txt disallows (pages and sitemap files), reports the
// count, and still returns the rest; a disallowed seed fails the call.
func TestRespectRobotsMap(t *testing.T) {
	g := newGateSite(t, "")
	g.set(func() { g.robotsBody = gateRobots + "Sitemap: " + g.URL + "/private/sitemap.xml\n" })
	b := newRobotsBrowser(t)
	ctx := context.Background()

	m, err := b.Map(ctx, req(g.URL+"/home"), 50, 3, nil)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	// Skipped: the sitemap under /private, the /private/secret link, and the
	// /to-private link (its redirect hop is disallowed).
	if m.SkippedByRobots != 3 {
		t.Errorf("skipped_by_robots = %d, want 3 (%+v)", m.SkippedByRobots, m)
	}
	for _, p := range []string{"/private/sitemap.xml", "/private/secret", "/from-private-sitemap"} {
		if n := g.count(p); n != 0 {
			t.Errorf("%s was requested %d time(s)", p, n)
		}
	}
	found := false
	for _, e := range m.URLs {
		found = found || e.URL == g.URL+"/open"
	}
	if !found || g.count("/open") == 0 {
		t.Errorf("allowed /open missing from the crawl: %+v", m.URLs)
	}

	_, err = b.Map(ctx, req(g.URL+"/private/secret"), 50, 2, nil)
	wantRefused(t, "map with a disallowed seed", err)
}

// map's guess at the /sitemap.xml convention is a fetch like any other: a site
// that disallows it must not see the request.
func TestRespectRobotsMapSitemapGuess(t *testing.T) {
	g := newGateSite(t, gateRobots+"Disallow: /sitemap.xml\n")
	b := newRobotsBrowser(t)

	if _, err := b.Map(context.Background(), req(g.URL+"/home"), 50, 1, nil); err != nil {
		t.Fatalf("map: %v", err)
	}
	if n := g.count("/sitemap.xml"); n != 0 {
		t.Errorf("/sitemap.xml was requested %d time(s) although robots.txt disallows it", n)
	}
	if g.count("/home") == 0 {
		t.Error("the allowed seed page was not fetched")
	}
}

// RFC 9309 section 2.3.1: a 4xx robots.txt means no restrictions; a 5xx or a
// failed fetch means everything is disallowed.
func TestRespectRobotsUnavailable(t *testing.T) {
	ctx := context.Background()

	t.Run("404 allows", func(t *testing.T) {
		g := newGateSite(t, "")
		g.set(func() { g.robotsStatus = http.StatusNotFound })
		b := newRobotsBrowser(t)
		if _, err := b.Read(ctx, req(g.URL+"/private/secret"), "full", 0, ""); err != nil {
			t.Fatalf("read with a 404 robots.txt: %v", err)
		}
	})
	t.Run("403 allows", func(t *testing.T) {
		g := newGateSite(t, "")
		g.set(func() { g.robotsStatus = http.StatusForbidden })
		b := newRobotsBrowser(t)
		if _, err := b.Read(ctx, req(g.URL+"/open"), "full", 0, ""); err != nil {
			t.Fatalf("read with a 403 robots.txt: %v", err)
		}
	})
	t.Run("500 disallows", func(t *testing.T) {
		g := newGateSite(t, "")
		g.set(func() { g.robotsStatus = http.StatusInternalServerError })
		b := newRobotsBrowser(t)
		_, err := b.Read(ctx, req(g.URL+"/open"), "full", 0, "")
		re := wantRefused(t, "read with a 500 robots.txt", err)
		if !re.Unreachable || re.Status != 500 || re.Rule != "" {
			t.Errorf("refusal detail = %+v, want unreachable with status 500", re)
		}
		if n := g.count("/open"); n != 0 {
			t.Errorf("/open was requested %d time(s)", n)
		}
		// robots.txt itself stays readable, and site explains the refusal.
		if _, err := b.Read(ctx, req(g.URL+"/robots.txt"), "full", 0, ""); errors.Is(err, browser.ErrRobotsDisallowed) {
			t.Errorf("/robots.txt was refused: %v", err)
		}
		s, err := b.Site(ctx, req(g.URL+"/open"))
		if err != nil {
			t.Fatalf("site: %v", err)
		}
		if s.Robots.AllowedForUs || !s.Robots.Enforced || !strings.Contains(s.Robots.Note, "disallow-all") {
			t.Errorf("site robots = %+v", s.Robots)
		}
	})
	t.Run("network failure disallows", func(t *testing.T) {
		g := newGateSite(t, gateRobots)
		dead := g.URL
		g.Close()
		b := newRobotsBrowser(t)
		_, err := b.Read(ctx, req(dead+"/open"), "full", 0, "")
		re := wantRefused(t, "read with an unreachable robots.txt", err)
		if !re.Unreachable || re.Status != 0 {
			t.Errorf("refusal detail = %+v, want unreachable with status 0", re)
		}
	})
	t.Run("disallow all still serves robots.txt", func(t *testing.T) {
		g := newGateSite(t, "User-agent: *\nDisallow: /\n")
		b := newRobotsBrowser(t)
		if _, err := b.Read(ctx, req(g.URL+"/robots.txt"), "full", 0, ""); err != nil {
			t.Fatalf("read /robots.txt: %v", err)
		}
		_, err := b.Read(ctx, req(g.URL+"/"), "full", 0, "")
		wantRefused(t, "read / under Disallow: /", err)
	})
}

// A link is judged by the robots.txt of the origin it points at, not of the
// page that carries it.
func TestRespectRobotsCrossOrigin(t *testing.T) {
	strict := newGateSite(t, gateRobots)                 // disallows /private
	open := newGateSite(t, "User-agent: *\nDisallow:\n") // allows everything
	home := newGateSite(t, "User-agent: *\nDisallow: /private\n")
	home.set(func() {
		home.homeLinks = `<a href="` + strict.URL + `/private/x">Strict elsewhere</a> ` +
			`<a href="` + open.URL + `/private/y">Open elsewhere</a> `
	})
	b := newRobotsBrowser(t)
	ctx := context.Background()
	if _, err := b.Browse(ctx, browser.Request{SessionID: "s", URL: home.URL + "/home"}); err != nil {
		t.Fatal(err)
	}

	_, err := b.Click(ctx, "s", 0, "Strict elsewhere", false)
	wantRefused(t, "click to a path the target origin disallows", err)
	if strict.count("/private/x") != 0 || strict.count("/robots.txt") != 1 {
		t.Errorf("strict origin: page hits=%d robots hits=%d; want 0 and 1", strict.count("/private/x"), strict.count("/robots.txt"))
	}
	// The linking origin disallows /private, the target origin does not.
	if _, err := b.Click(ctx, "s", 0, "Open elsewhere", false); err != nil {
		t.Fatalf("click to a path only the linking origin disallows: %v", err)
	}
	if open.count("/private/y") != 1 {
		t.Errorf("open origin page hits = %d, want 1", open.count("/private/y"))
	}
}

// The agent-facing error names the cause, the operator setting, and that a
// retry is pointless; the site tool and the browse hint say rules are enforced.
func TestRespectRobotsReporting(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t)
	ctx := context.Background()

	_, err := b.Read(ctx, req(g.URL+"/private/secret"), "full", 0, "")
	be := browser.Classify(err)
	if be.Code != browser.ErrRobots || be.Retryable {
		t.Errorf("classified as %q retryable=%v", be.Code, be.Retryable)
	}
	for _, want := range []string{g.URL + "/private/secret", "disallowed by the site's robots.txt",
		`Disallow: /private`, "--respect-robots", "retrying will not help"} {
		if !strings.Contains(be.Message, want) {
			t.Errorf("message %q lacks %q", be.Message, want)
		}
	}
	if !errors.Is(be, browser.ErrRobotsDisallowed) {
		t.Error("the classified error no longer matches ErrRobotsDisallowed")
	}

	// site on a disallowed URL reports the rules without fetching the page.
	s, err := b.Site(ctx, req(g.URL+"/private/secret"))
	if err != nil {
		t.Fatalf("site on a disallowed URL: %v", err)
	}
	if !s.Robots.Enforced || s.Robots.AllowedForUs || !s.Robots.Present {
		t.Errorf("site robots = %+v, want enforced, not allowed", s.Robots)
	}
	if n := g.count("/private/secret"); n != 0 {
		t.Errorf("site fetched the disallowed page %d time(s)", n)
	}
	s, err = b.Site(ctx, req(g.URL+"/open"))
	if err != nil || !s.Robots.Enforced || !s.Robots.AllowedForUs {
		t.Errorf("site on an allowed URL = %+v, %v", s, err)
	}
	br, err := b.Browse(ctx, req(g.URL+"/open"))
	if err != nil {
		t.Fatal(err)
	}
	if br.Robots == nil || !br.Robots.Enforced || !br.Robots.AllowedForUs {
		t.Errorf("browse robots hint = %+v, want enforced + allowed", br.Robots)
	}
	if n := g.count("/robots.txt"); n != 1 {
		t.Errorf("robots.txt fetched %d times, want 1 (the gate and the site tool share one cache entry)", n)
	}
}

// --no-site-hints only controls what browse prints; enforcement still works.
func TestRespectRobotsWithoutSiteHints(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t, browser.WithSiteHints(false))
	ctx := context.Background()

	_, err := b.Browse(ctx, req(g.URL+"/private/secret"))
	wantRefused(t, "browse without site hints", err)
	br, err := b.Browse(ctx, req(g.URL+"/open"))
	if err != nil {
		t.Fatal(err)
	}
	if br.Robots != nil {
		t.Errorf("site hints are off but browse carries a robots hint: %+v", br.Robots)
	}
	if g.count("/private/secret") != 0 || g.count("/llms.txt") != 0 {
		t.Errorf("hits: private=%d llms=%d; want 0 and 0", g.count("/private/secret"), g.count("/llms.txt"))
	}
}

// The robots.txt probe a credentialed session triggers is anonymous: it carries
// neither the session's injected credentials nor its cookies.
func TestRespectRobotsProbeIsAnonymous(t *testing.T) {
	g := newGateSite(t, gateRobots)
	b := newRobotsBrowser(t)
	ctx := context.Background()
	id, err := b.NewSession("authed", session.Config{
		Origin:  g.URL,
		Bearer:  "secret-token",
		Cookies: []*http.Cookie{{Name: "sid", Value: "secret-cookie", Path: "/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(ctx, browser.Request{SessionID: id, URL: g.URL + "/open"}, "full", 0, ""); err != nil {
		t.Fatalf("session read: %v", err)
	}
	_, err = b.Read(ctx, browser.Request{SessionID: id, URL: g.URL + "/private/secret"}, "full", 0, "")
	wantRefused(t, "credentialed session read", err)

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.robotsAuth) != 1 || g.robotsAuth[0] != "" {
		t.Errorf("robots.txt requests carried credentials: %q", g.robotsAuth)
	}
}
