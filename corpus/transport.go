//go:build corpus

package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Match tiers, in the order they are tried. Anything past "exact" is a
// tolerance for capture/replay non-determinism and is reported per page.
const (
	matchExact     = "exact"
	matchCacheBust = "cachebust" // equal once volatile query params are dropped
	matchPath      = "path"      // same path and param names, different values
)

// replayStats is what one page's replay asked of its archive.
type replayStats struct {
	Requests      int `json:"requests"`
	Exact         int `json:"exact"`
	CacheBust     int `json:"cachebust"`
	PathOnly      int `json:"path_only"`
	CaptureFailed int `json:"capture_failed"` // Chromium's request failed too
	// CaptureNoBody counts requests the archive knows but cannot answer: Chromium
	// got response headers and then the load was cut short (the page aborted it,
	// or the browser no longer had the bytes), so there is no body to serve. They
	// are failed rather than answered with an empty 200, which would hand the
	// page a truncated response Chromium's own page never saw.
	CaptureNoBody int `json:"capture_no_body"`
	Unmatched     int `json:"unmatched"`
	// MissConsequential / MissHarmless split Unmatched + CaptureNoBody by whether
	// the lost response could change what the page renders (see missKind). The
	// classifier only excuses a low score for consequential losses.
	MissConsequential int `json:"miss_consequential"`
	MissHarmless      int `json:"miss_harmless"`
	// MissFirstParty counts lost requests to the page's own registrable domain.
	MissFirstParty int            `json:"miss_first_party"`
	MissKinds      map[string]int `json:"miss_kinds,omitempty"`   // by missKind
	MissMethods    map[string]int `json:"miss_methods,omitempty"` // by HTTP method
	// MissNear says how close the archive came: "never" (Chromium requested no
	// such host+path at all), "query" (same path, a query no tier reconciled),
	// "method" (same path under another method), "nobody" (CaptureNoBody).
	MissNear map[string]int `json:"miss_near,omitempty"`
	// NonHTTP counts requests for a non-http(s) URL (data:, blob:, ...) that
	// reached the HTTP transport. Chromium resolves those without the network, so
	// no capture can hold them: they are the engine's doing, not a replay miss.
	NonHTTP       int        `json:"non_http"`
	NonHTTPSample []string   `json:"non_http_sample,omitempty"`
	Misses        []missInfo `json:"misses,omitempty"` // sample of the lost requests (all of them under CORPUS_DEBUG)
}

type missInfo struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Dest       string `json:"dest,omitempty"` // Sec-Fetch-Dest of the unblink request
	Kind       string `json:"kind"`
	FirstParty bool   `json:"first_party,omitempty"`
	Near       string `json:"near"`
}

const maxMissSample = 12

// replay is an http.RoundTripper that answers from one page's archive and never
// touches the network. A request the archive cannot answer gets a synthetic
// transport error and is counted.
type replay struct {
	dir     string
	site    string // the page's registrable domain, for the first-party split
	allMiss bool   // keep every lost request, not a sample
	mu      sync.Mutex
	exact   map[string][]*entry // "METHOD url"
	byPath  map[string][]*entry // "METHOD scheme://host/path"
	anyPath map[string]bool     // "scheme://host/path", any method
	next    map[string]int      // per exact key: next response in capture order
	stats   replayStats
}

func newReplay(dir string, a *archive) *replay {
	r := &replay{
		dir: dir, site: siteOf(a.URL), allMiss: debugOn(),
		exact: map[string][]*entry{}, byPath: map[string][]*entry{}, anyPath: map[string]bool{},
		next: map[string]int{},
	}
	for _, e := range a.Entries {
		u, err := url.Parse(e.URL)
		if err != nil {
			continue
		}
		canonURL(u)
		m := strings.ToUpper(e.Method)
		r.exact[m+" "+u.String()] = append(r.exact[m+" "+u.String()], e)
		r.byPath[pathKey(m, u)] = append(r.byPath[pathKey(m, u)], e)
		r.anyPath[pathKey("", u)] = true
	}
	// A key Chromium both failed and completed (an aborted request re-issued) is
	// answered from the completed exchanges only.
	for k, es := range r.exact {
		var ok []*entry
		for _, e := range es {
			if e.Failed == "" {
				ok = append(ok, e)
			}
		}
		if len(ok) > 0 {
			r.exact[k] = ok
		}
	}
	return r
}

func pathKey(method string, u *url.URL) string {
	return method + " " + u.Scheme + "://" + u.Host + u.EscapedPath()
}

// canonURL normalises the parts of a URL that are case-insensitive or implied —
// scheme and host case, a default port, the fragment — in place. Chromium
// records the serialised (lower-cased) host; a page that builds a request URL
// with an upper-case host must still find it.
func canonURL(u *url.URL) {
	u.Fragment, u.RawFragment = "", ""
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	u.Host = host
}

// volatileParams are query names conventionally used to defeat caches.
var volatileParams = map[string]bool{
	"_": true, "t": true, "ts": true, "_t": true, "_ts": true, "cb": true, "cachebust": true,
	"cachebuster": true, "cache_bust": true, "nocache": true, "rand": true, "rnd": true,
	"random": true, "timestamp": true, "_dc": true, "bust": true,
}

// stableQuery is the query with volatile params (by name, or an epoch-like
// all-digit value) removed, canonically ordered.
func stableQuery(q url.Values) string {
	keep := url.Values{}
	for k, vs := range q {
		if volatileParams[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			if epochLike(v) {
				continue
			}
			keep.Add(k, v)
		}
	}
	return keep.Encode()
}

func epochLike(v string) bool {
	if len(v) < 10 || len(v) > 19 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func paramNames(q url.Values) string {
	names := make([]string, 0, len(q))
	for k := range q {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, "&")
}

// match finds the recorded exchange for a request and the tier that found it.
//
// reqBody is the SHA-256 of the request body ("" when there is none). When the
// capture recorded request-body hashes for this URL, an exchange with the same
// body wins over capture order — two POSTs to one endpoint (a GraphQL or search
// API) are different requests. Captures made before the field existed carry no
// hashes and fall back to capture order.
func (r *replay) match(method string, u *url.URL, reqBody string) (*entry, string) {
	u2 := *u
	canonURL(&u2)
	method = strings.ToUpper(method)
	key := method + " " + u2.String()
	if es := r.exact[key]; len(es) > 0 {
		if reqBody != "" {
			for _, e := range es {
				if e.ReqBody == reqBody {
					return e, matchExact
				}
			}
		}
		// Repeats are answered in capture order; the last response is sticky.
		i := r.next[key]
		if i >= len(es) {
			i = len(es) - 1
		}
		r.next[key] = i + 1
		return es[i], matchExact
	}
	cands := r.byPath[pathKey(method, &u2)]
	if len(cands) == 0 {
		return nil, ""
	}
	q := u2.Query()
	want := stableQuery(q)
	for _, e := range cands {
		if eu, err := url.Parse(e.URL); err == nil && e.Failed == "" && stableQuery(eu.Query()) == want {
			return e, matchCacheBust
		}
	}
	// Last resort: identical parameter names, most values in common. A request
	// with a different parameter set is a different resource and stays a miss.
	names := paramNames(q)
	var best *entry
	bestN := -1
	for _, e := range cands {
		eu, err := url.Parse(e.URL)
		if err != nil || e.Failed != "" {
			continue
		}
		eq := eu.Query()
		if paramNames(eq) != names {
			continue
		}
		n := 0
		for k := range q {
			if q.Get(k) == eq.Get(k) {
				n++
			}
		}
		if n > bestN {
			best, bestN = e, n
		}
	}
	if best != nil {
		return best, matchPath
	}
	return nil, ""
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBody := ""
	if req.Body != nil {
		h := sha256.New()
		n, _ := io.Copy(h, io.LimitReader(req.Body, maxBodyBytes))
		_ = req.Body.Close()
		if n > 0 {
			reqBody = hex.EncodeToString(h.Sum(nil))
		}
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		r.mu.Lock()
		r.stats.NonHTTP++
		if len(r.stats.NonHTTPSample) < 3 {
			r.stats.NonHTTPSample = append(r.stats.NonHTTPSample, head(req.URL.String(), 80))
		}
		r.mu.Unlock()
		return nil, fmt.Errorf("corpus: %s: URL reached the HTTP transport", req.URL.Scheme)
	}
	r.mu.Lock()
	r.stats.Requests++
	e, tier := r.match(req.Method, req.URL, reqBody)
	noBody := e != nil && e.Failed == "" && e.bodyLost()
	switch {
	case e == nil:
		r.stats.Unmatched++
		r.lost(req, r.near(req))
	case e.Failed != "":
		r.stats.CaptureFailed++
	case noBody:
		r.stats.CaptureNoBody++
		r.lost(req, "nobody")
	case tier == matchExact:
		r.stats.Exact++
	case tier == matchCacheBust:
		r.stats.CacheBust++
	default:
		r.stats.PathOnly++
	}
	r.mu.Unlock()

	if e == nil {
		return nil, fmt.Errorf("corpus: replay miss: %s %s is not in the archive", req.Method, req.URL)
	}
	if e.Failed != "" {
		return nil, fmt.Errorf("corpus: request failed at capture too (%s): %s", e.Failed, req.URL)
	}
	if noBody {
		return nil, fmt.Errorf("corpus: capture has no body for %s %s (%s)", req.Method, req.URL, e.Omitted)
	}
	body, err := e.body(r.dir)
	if err != nil {
		return nil, fmt.Errorf("corpus: archive body: %w", err)
	}
	h := http.Header{}
	for k, vs := range e.Headers {
		switch http.CanonicalHeaderKey(k) {
		// The stored body is already content-decoded and de-chunked.
		case "Content-Encoding", "Content-Length", "Transfer-Encoding":
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if e.Text {
		// Chromium returned this body as decoded text, so it is UTF-8 whatever the
		// server declared; say so, or the fetch layer would transcode it twice.
		h.Set("Content-Type", utf8ContentType(h.Get("Content-Type"), e.MIME))
	}
	status := e.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, e.StatusText),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func utf8ContentType(header, mimeType string) string {
	mt, params, err := mime.ParseMediaType(header)
	if err != nil || mt == "" {
		mt, params = mimeType, nil
	}
	if mt == "" {
		mt = "text/plain"
	}
	if params == nil {
		params = map[string]string{}
	}
	params["charset"] = "utf-8"
	if out := mime.FormatMediaType(mt, params); out != "" {
		return out
	}
	return mt + "; charset=utf-8"
}

func (r *replay) snapshot() replayStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Misses = append([]missInfo(nil), r.stats.Misses...)
	s.MissKinds, s.MissMethods, s.MissNear = cloneCounts(s.MissKinds), cloneCounts(s.MissMethods), cloneCounts(s.MissNear)
	s.NonHTTPSample = append([]string(nil), r.stats.NonHTTPSample...)
	return s
}
