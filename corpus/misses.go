//go:build corpus

package corpus

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Lost-request kinds. A request is "lost" when replay could not answer it: the
// archive has no such exchange (unmatched), or has it without a body
// (capture_no_body). The kind is judged from the unblink request alone — its
// Sec-Fetch-Dest, method and URL — since by definition no response exists.
const (
	kindScript    = "script"
	kindDocument  = "document"
	kindFetch     = "fetch/xhr"
	kindStyle     = "style"
	kindPreflight = "preflight"
	kindImage     = "image"
	kindFont      = "font"
	kindMedia     = "media"
	kindAnalytics = "analytics/ads"
)

// consequentialKind reports whether losing a response of this kind can change
// what the page renders. Analytics beacons, ad calls and resources unblink has
// no use for (pixels, fonts, media) cannot; code, data and documents can. Style
// is kept consequential: the engine has no layout, but a page can read a
// stylesheet's load/error event, and it is too rare here to be worth guessing.
func consequentialKind(k string) bool {
	switch k {
	case kindAnalytics, kindImage, kindFont, kindMedia:
		return false
	}
	return true
}

// analyticsHostRE matches hosts whose only job is measurement or advertising.
// It is deliberately a short list of unambiguous names: a host it does not know
// is treated as consequential, so an unknown tracker can at worst excuse a page
// into bucket (d) — it can never blame the engine for a harness loss.
var analyticsHostRE = regexp.MustCompile(`(^|\.)(` +
	`google-analytics\.com|googletagmanager\.com|googlesyndication\.com|doubleclick\.net|googleadservices\.com|` +
	`amazon-adsystem\.com|adsrvr\.org|carbonads\.net|carbonads\.com|ay\.delivery|adnxs\.com|criteo\.(com|net)|` +
	`rubiconproject\.com|pubmatic\.com|openx\.net|taboola\.com|outbrain\.com|scorecardresearch\.com|chartbeat\.(com|net)|` +
	`segment\.(io|com)|sentry\.io|plausible\.io|hotjar\.(com|io)|clarity\.ms|newrelic\.com|nr-data\.net|` +
	`datadoghq\.com|cloudflareinsights\.com|facebook\.net|connect\.facebook\.com|analytics\.twitter\.com|` +
	`bat\.bing\.com|mixpanel\.com|amplitude\.com|heapanalytics\.com|fullstory\.com|optimizely\.com|` +
	`quantserve\.com|permutive\.(com|app)|parsely\.com|comscore\.com|telemetry\.algolia\.com|insights\.algolia\.io` +
	`)$`)

var (
	scriptExtRE = regexp.MustCompile(`(?i)\.(m?js|cjs)$`)
	styleExtRE  = regexp.MustCompile(`(?i)\.css$`)
	imageExtRE  = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|avif|svg|ico|bmp)$`)
	fontExtRE   = regexp.MustCompile(`(?i)\.(woff2?|ttf|otf|eot)$`)
	mediaExtRE  = regexp.MustCompile(`(?i)\.(mp4|webm|mp3|ogg|wav|m4a|mov|m3u8)$`)
)

// missKind classifies a request replay could not answer.
func missKind(req *http.Request) string {
	if analyticsHostRE.MatchString(strings.ToLower(req.URL.Hostname())) {
		return kindAnalytics
	}
	p := path.Base(req.URL.Path)
	switch strings.ToLower(req.Header.Get("Sec-Fetch-Dest")) {
	case "script", "worker", "sharedworker", "serviceworker":
		return kindScript
	case "document", "iframe", "frame":
		return kindDocument
	case "style":
		return kindStyle
	case "image":
		return kindImage
	case "font":
		return kindFont
	case "audio", "video", "track":
		return kindMedia
	}
	// Sec-Fetch-Dest "empty" (fetch/XHR) or absent: the URL is the only hint. A
	// bundler runtime prefetching a chunk through fetch() is still code.
	switch {
	case strings.EqualFold(req.Method, http.MethodOptions):
		return kindPreflight
	case scriptExtRE.MatchString(p):
		return kindScript
	case styleExtRE.MatchString(p):
		return kindStyle
	case imageExtRE.MatchString(p):
		return kindImage
	case fontExtRE.MatchString(p):
		return kindFont
	case mediaExtRE.MatchString(p):
		return kindMedia
	}
	return kindFetch
}

// siteOf is the registrable domain (eTLD+1) of a URL or host, lower-cased; a
// host with no public suffix (localhost, an IP, a .test name) is its own site.
func siteOf(rawURL string) string {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if s, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return s
	}
	return host
}

// near says how close the archive came to a request it could not match.
// Caller holds r.mu.
func (r *replay) near(req *http.Request) string {
	u := *req.URL
	canonURL(&u)
	switch {
	case len(r.byPath[pathKey(strings.ToUpper(req.Method), &u)]) > 0:
		return "query"
	case r.anyPath[pathKey("", &u)]:
		return "method"
	}
	return "never"
}

// lost records one request replay could not answer. Caller holds r.mu.
func (r *replay) lost(req *http.Request, near string) {
	kind := missKind(req)
	first := siteOf(req.URL.Hostname()) == r.site
	s := &r.stats
	if consequentialKind(kind) {
		s.MissConsequential++
	} else {
		s.MissHarmless++
	}
	if first {
		s.MissFirstParty++
	}
	bump(&s.MissKinds, kind)
	bump(&s.MissMethods, strings.ToUpper(req.Method))
	bump(&s.MissNear, near)
	if r.allMiss || len(s.Misses) < maxMissSample {
		s.Misses = append(s.Misses, missInfo{
			Method: req.Method, URL: req.URL.String(), Dest: req.Header.Get("Sec-Fetch-Dest"),
			Kind: kind, FirstParty: first, Near: near,
		})
	}
}

func bump(m *map[string]int, k string) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[k]++
}

func cloneCounts(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func addCounts(dst *map[string]int, src map[string]int) {
	for k, v := range src {
		if *dst == nil {
			*dst = map[string]int{}
		}
		(*dst)[k] += v
	}
}

// bodyLost reports whether the capture should have a body for this exchange and
// does not: the load was cut short after the headers, or the browser no longer
// had the bytes. Resource types the capture drops on purpose (image, font,
// media), bodiless statuses and bodiless methods are not losses — replay
// answers those with their recorded status and headers.
func (e *entry) bodyLost() bool {
	if e.Body != "" || e.Omitted == "" || strings.HasPrefix(e.Omitted, "resource type ") {
		return false
	}
	switch m := strings.ToUpper(e.Method); m {
	case http.MethodHead, http.MethodOptions:
		return false
	}
	switch {
	case e.Status < 200, e.Status == http.StatusNoContent, e.Status == http.StatusResetContent,
		e.Status >= 300 && e.Status < 400:
		return false
	}
	return true
}

// debugOn reports whether CORPUS_DEBUG asks for full per-page diagnostics.
func debugOn() bool { return os.Getenv("CORPUS_DEBUG") != "" }
