package js

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// Content-Security-Policy enforcement over untrusted page JavaScript, on by
// default. unblink parses the document's CSP (response header(s) + <meta
// http-equiv>) and enforces the directives that govern the code it runs:
// script-src (inline nonce/hash + external host allowlist), connect-src
// (fetch/XHR), and unsafe-eval. Content-Security-Policy-Report-Only parses and
// surfaces would-be violations in diagnostics but never blocks. A resource must
// satisfy EVERY enforced policy. See ADR 0014.
//
// Out of scope (no-ops or non-goals): img/style/font/media/frame-src (unblink
// fetches no such passive subresources), frame-ancestors/form-action/base-uri.
// The parser is pure and must never panic (it is fuzzed).

// cspSourceKind classifies a parsed source expression.
type cspSourceKind int

const (
	srcKeyword cspSourceKind = iota // 'self' 'none' 'unsafe-inline' 'unsafe-eval' 'strict-dynamic' …
	srcNonce                        // 'nonce-<value>'
	srcHash                         // 'sha256/384/512-<b64>'
	srcHost                         // [scheme://]host[:port][/path], host may be * or *.suffix
	srcScheme                       // scheme-source, e.g. https: data: blob:
)

type cspSource struct {
	kind    cspSourceKind
	keyword string // for srcKeyword (unquoted)
	nonce   string // for srcNonce
	hashAlg string // for srcHash: sha256|sha384|sha512
	hashB64 string // for srcHash: the base64 digest
	scheme  string // host-source scheme (may be "") or scheme-source
	host    string // host-source host
	port    string // host-source port ("" = default, "*" = any)
	path    string // host-source path ("" = any)
}

type cspPolicy struct {
	directives map[string][]cspSource
}

// cspContext is the resolved policy set for a document: enforced policies block,
// report-only policies only record.
type cspContext struct {
	enforced   []*cspPolicy
	reportOnly []*cspPolicy
}

var selMetaCSP = cascadia.MustCompile("meta[http-equiv]")

// buildCSP parses the enforced and report-only policies from the response headers
// and the document's <meta http-equiv> tags. Returns nil when the document
// declared no usable policy.
func buildCSP(headers http.Header, doc *html.Node) *cspContext {
	c := &cspContext{}
	add := func(dst *[]*cspPolicy, raw string, meta bool) {
		for _, part := range strings.Split(raw, ",") { // commas separate policies
			p := parseCSPPolicy(part)
			if p == nil || len(p.directives) == 0 {
				continue
			}
			if meta {
				// <meta> cannot host these; drop them per spec.
				delete(p.directives, "frame-ancestors")
				delete(p.directives, "report-uri")
				delete(p.directives, "sandbox")
				if len(p.directives) == 0 {
					continue
				}
			}
			*dst = append(*dst, p)
		}
	}
	if headers != nil {
		for _, v := range headers.Values("Content-Security-Policy") {
			add(&c.enforced, v, false)
		}
		for _, v := range headers.Values("Content-Security-Policy-Report-Only") {
			add(&c.reportOnly, v, false)
		}
	}
	if doc != nil {
		for _, m := range selMetaCSP.MatchAll(doc) {
			if strings.EqualFold(strings.TrimSpace(getAttr(m, "http-equiv")), "content-security-policy") {
				add(&c.enforced, getAttr(m, "content"), true)
			}
		}
	}
	if len(c.enforced) == 0 && len(c.reportOnly) == 0 {
		return nil
	}
	return c
}

// parseCSPPolicy parses one policy (";"-separated directives). The first
// occurrence of a directive name wins (later duplicates are ignored, per spec).
func parseCSPPolicy(s string) *cspPolicy {
	p := &cspPolicy{directives: map[string][]cspSource{}}
	for _, dir := range strings.Split(s, ";") {
		fields := strings.Fields(dir)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		if _, dup := p.directives[name]; dup {
			continue
		}
		srcs := make([]cspSource, 0, len(fields)-1)
		for _, tok := range fields[1:] {
			if src, ok := parseCSPSource(tok); ok {
				srcs = append(srcs, src)
			}
		}
		p.directives[name] = srcs
	}
	if len(p.directives) == 0 {
		return nil
	}
	return p
}

// parseCSPSource parses one source expression.
func parseCSPSource(tok string) (cspSource, bool) {
	t := strings.TrimSpace(tok)
	if t == "" {
		return cspSource{}, false
	}
	low := strings.ToLower(t)
	switch low {
	case "'none'", "'self'", "'unsafe-inline'", "'unsafe-eval'", "'strict-dynamic'",
		"'unsafe-hashes'", "'report-sample'", "'wasm-unsafe-eval'":
		return cspSource{kind: srcKeyword, keyword: strings.Trim(low, "'")}, true
	}
	if strings.HasPrefix(low, "'nonce-") && strings.HasSuffix(t, "'") {
		return cspSource{kind: srcNonce, nonce: t[len("'nonce-") : len(t)-1]}, true
	}
	for _, alg := range []string{"sha256", "sha384", "sha512"} {
		pfx := "'" + alg + "-"
		if strings.HasPrefix(low, pfx) && strings.HasSuffix(t, "'") {
			return cspSource{kind: srcHash, hashAlg: alg, hashB64: t[len(pfx) : len(t)-1]}, true
		}
	}
	// scheme-source: "scheme:" with nothing after.
	if strings.HasSuffix(t, ":") && isBareScheme(t[:len(t)-1]) {
		return cspSource{kind: srcScheme, scheme: strings.ToLower(t[:len(t)-1])}, true
	}
	return parseHostSource(t)
}

// isBareScheme reports whether s is a plausible URL scheme (letters/digits/+/-/.).
func isBareScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !isASCIILetter(r) {
			return false
		}
		if !isASCIILetter(r) && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// parseHostSource parses `[scheme://]host[:port][/path]` with `*`/`*.` wildcards.
func parseHostSource(t string) (cspSource, bool) {
	s := cspSource{kind: srcHost}
	rest := t
	if i := strings.Index(rest, "://"); i >= 0 {
		s.scheme = strings.ToLower(rest[:i])
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		s.path = rest[i:]
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		s.port = rest[i+1:]
		rest = rest[:i]
	}
	s.host = strings.ToLower(rest)
	if s.host == "" {
		return cspSource{}, false
	}
	return s, true
}

// ---- directive resolution ----

// resolve returns the first present directive's sources (with fallback order) and
// whether any applied. An absent directive chain means "not restricted".
func (p *cspPolicy) resolve(names ...string) ([]cspSource, bool) {
	for _, n := range names {
		if s, ok := p.directives[n]; ok {
			return s, true
		}
	}
	return nil, false
}

// ---- per-policy matchers ----

func (p *cspPolicy) allowsInlineScript(base *url.URL, text, nonce string) bool {
	srcs, restricted := p.resolve("script-src-elem", "script-src", "default-src")
	if !restricted {
		return true
	}
	var hasNonce, hasHash, hasUnsafeInline, hasStrictDynamic bool
	for _, s := range srcs {
		switch {
		case s.kind == srcNonce:
			hasNonce = true
			if nonce != "" && s.nonce == nonce {
				return true
			}
		case s.kind == srcHash:
			hasHash = true
		case s.kind == srcKeyword && s.keyword == "unsafe-inline":
			hasUnsafeInline = true
		case s.kind == srcKeyword && s.keyword == "strict-dynamic":
			hasStrictDynamic = true
		}
	}
	if hasHash {
		for _, s := range srcs {
			if s.kind == srcHash && cspHashMatches(s, text) {
				return true
			}
		}
	}
	// 'unsafe-inline' is ignored when a nonce/hash source or strict-dynamic is present.
	return hasUnsafeInline && !hasNonce && !hasHash && !hasStrictDynamic
}

func (p *cspPolicy) allowsExternalScript(base, u *url.URL, nonce string, parserInserted bool) bool {
	srcs, restricted := p.resolve("script-src-elem", "script-src", "default-src")
	if !restricted {
		return true
	}
	var hasStrictDynamic bool
	for _, s := range srcs {
		if s.kind == srcNonce && nonce != "" && s.nonce == nonce {
			return true
		}
		if s.kind == srcKeyword && s.keyword == "strict-dynamic" {
			hasStrictDynamic = true
		}
	}
	if hasStrictDynamic {
		// host/scheme/'self'/'unsafe-inline' are ignored; trust propagates to
		// script-inserted nodes (nonce already handled above).
		return !parserInserted
	}
	return matchesHostOrScheme(srcs, base, u)
}

func (p *cspPolicy) allowsConnect(base, u *url.URL) bool {
	srcs, restricted := p.resolve("connect-src", "default-src")
	if !restricted {
		return true
	}
	return matchesHostOrScheme(srcs, base, u)
}

func (p *cspPolicy) blocksEval() bool {
	srcs, restricted := p.resolve("script-src", "default-src")
	if !restricted {
		return false
	}
	for _, s := range srcs {
		if s.kind == srcKeyword && s.keyword == "unsafe-eval" {
			return false
		}
	}
	return true
}

// matchesHostOrScheme reports whether any 'self'/scheme/host source in srcs allows
// the URL. Keyword sources other than 'self' (none/unsafe-*) never grant a URL.
func matchesHostOrScheme(srcs []cspSource, base, u *url.URL) bool {
	for _, s := range srcs {
		switch s.kind {
		case srcKeyword:
			if s.keyword == "self" && base != nil && sameOrigin(u, base) {
				return true
			}
		case srcScheme:
			if strings.EqualFold(s.scheme, u.Scheme) {
				return true
			}
		case srcHost:
			if cspHostMatches(s, u) {
				return true
			}
		}
	}
	return false
}

// cspHostMatches reports whether a host-source matches the URL.
func cspHostMatches(s cspSource, u *url.URL) bool {
	if s.scheme != "" {
		if !strings.EqualFold(s.scheme, u.Scheme) {
			return false
		}
	} else if !isNetworkScheme(u.Scheme) {
		// A scheme-less host-source matches only network-scheme URLs (not data:/blob:).
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case s.host == "*":
		// bare * matches any host on a network scheme (scheme already checked).
	case strings.HasPrefix(s.host, "*."):
		if !strings.HasSuffix(host, s.host[1:]) { // ".example.com"
			return false
		}
	default:
		if host != s.host {
			return false
		}
	}
	if s.port != "" && s.port != "*" {
		if !cspPortMatches(s.port, u) {
			return false
		}
	}
	if s.path != "" && s.path != "/" {
		if !cspPathMatches(s.path, u.EscapedPath()) {
			return false
		}
	}
	return true
}

func cspPortMatches(port string, u *url.URL) bool {
	up := u.Port()
	if up == "" {
		up = schemeDefaultPort(u.Scheme)
	}
	sp := port
	// A source port may also be a scheme default written explicitly.
	return up == sp
}

// cspPathMatches implements the CSP path-match: a trailing "/" is a prefix match,
// otherwise the paths must be equal.
func cspPathMatches(srcPath, urlPath string) bool {
	if urlPath == "" {
		urlPath = "/"
	}
	if strings.HasSuffix(srcPath, "/") {
		return strings.HasPrefix(urlPath, srcPath)
	}
	return urlPath == srcPath
}

func isNetworkScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ws", "wss", "ftp":
		return true
	}
	return false
}

// cspHashMatches reports whether text hashes to the source's digest, reusing the
// SRI hash helpers (sri.go).
func cspHashMatches(s cspSource, text string) bool {
	h := sriHash(s.hashAlg)
	if h == nil {
		return false
	}
	want := decodeSRIDigest(s.hashB64)
	if want == nil {
		return false
	}
	hh := h()
	hh.Write([]byte(text))
	return bytes.Equal(hh.Sum(nil), want)
}

// ---- aggregate decisions (a resource must satisfy every enforced policy) ----

func (c *cspContext) allowsInlineScript(base *url.URL, text, nonce string) bool {
	for _, p := range c.enforced {
		if !p.allowsInlineScript(base, text, nonce) {
			return false
		}
	}
	return true
}

func (c *cspContext) reportOnlyBlocksInline(base *url.URL, text, nonce string) bool {
	for _, p := range c.reportOnly {
		if !p.allowsInlineScript(base, text, nonce) {
			return true
		}
	}
	return false
}

func (c *cspContext) allowsExternalScript(base, u *url.URL, nonce string, parserInserted bool) bool {
	for _, p := range c.enforced {
		if !p.allowsExternalScript(base, u, nonce, parserInserted) {
			return false
		}
	}
	return true
}

func (c *cspContext) reportOnlyBlocksExternal(base, u *url.URL, nonce string, parserInserted bool) bool {
	for _, p := range c.reportOnly {
		if !p.allowsExternalScript(base, u, nonce, parserInserted) {
			return true
		}
	}
	return false
}

func (c *cspContext) allowsConnect(base, u *url.URL) bool {
	for _, p := range c.enforced {
		if !p.allowsConnect(base, u) {
			return false
		}
	}
	return true
}

func (c *cspContext) reportOnlyBlocksConnect(base, u *url.URL) bool {
	for _, p := range c.reportOnly {
		if !p.allowsConnect(base, u) {
			return true
		}
	}
	return false
}

func (c *cspContext) blocksEval() bool {
	for _, p := range c.enforced {
		if p.blocksEval() {
			return true
		}
	}
	return false
}

// ---- bridge enforcement helpers (record diagnostics; report-only never blocks) ----

// cspAllowInline reports whether an inline script may run, recording an enforced
// block or a report-only would-block as a diagnostic.
func (b *bridge) cspAllowInline(text, nonce string) bool {
	if b.csp == nil {
		return true
	}
	if !b.csp.allowsInlineScript(b.base, text, nonce) {
		b.recordError(fmt.Errorf("blocked by Content-Security-Policy: inline <script> (no matching nonce/hash/'unsafe-inline')"))
		return false
	}
	if b.csp.reportOnlyBlocksInline(b.base, text, nonce) {
		b.recordError(fmt.Errorf("Content-Security-Policy report-only: would block inline <script>"))
	}
	return true
}

// cspAllowExternalScript reports whether an external script URL may load.
func (b *bridge) cspAllowExternalScript(abs, nonce string, parserInserted bool) bool {
	if b.csp == nil {
		return true
	}
	u, err := url.Parse(abs)
	if err != nil {
		return true // unparseable → don't block on CSP (other layers will reject)
	}
	if !b.csp.allowsExternalScript(b.base, u, nonce, parserInserted) {
		b.recordError(fmt.Errorf("blocked by Content-Security-Policy: script-src does not allow %s", abs))
		return false
	}
	if b.csp.reportOnlyBlocksExternal(b.base, u, nonce, parserInserted) {
		b.recordError(fmt.Errorf("Content-Security-Policy report-only: would block script %s", abs))
	}
	return true
}

// hideNonces captures every element's CSP nonce into b.nonces and blanks the
// content attribute in the tree, so untrusted page JS cannot scrape a nonce (via
// getAttribute or a CSS attribute selector) and reuse it to slip a script past
// script-src. Enforcement reads the real value through nodeNonce; the DOM unblink
// hands the agent shows an empty nonce, matching a browser's nonce hiding. Runs on
// the loop goroutine right after buildCSP, before any page script executes, so an
// early inline script sees siblings' nonces already blanked. Only invoked when a
// CSP is present (the sole context where a leaked nonce is a bypass).
func (b *bridge) hideNonces() {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i := range n.Attr {
				if n.Attr[i].Key == "nonce" && n.Attr[i].Val != "" {
					if b.nonces == nil {
						b.nonces = make(map[*html.Node]string)
					}
					b.nonces[n] = n.Attr[i].Val
					n.Attr[i].Val = "" // blank, don't remove — a browser keeps an empty content attribute
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(b.doc)
}

// nodeNonce returns an element's real CSP nonce: the value captured by hideNonces
// (the content attribute is now blank), falling back to the live attribute for
// nodes created after setup (e.g. script-inserted chunks), which were never hidden.
func (b *bridge) nodeNonce(n *html.Node) string {
	if v, ok := b.nonces[n]; ok {
		return v
	}
	return getAttr(n, "nonce")
}

// installCSPEvalGate replaces eval/Function with EvalError-throwing shims when the
// document's CSP script-src lacks 'unsafe-eval'. Called after the prelude, just
// before page scripts run (extension content scripts, which are trusted, keep
// eval). F.prototype is re-pointed at the real Function.prototype so instanceof and
// Function.prototype.* keep working — only fn.constructor identity changes. The
// string-timer path (globals.go new Function(String(fn))) then correctly throws for
// setTimeout("code"), matching a browser. A thrown EvalError surfaces through the
// normal uncaught-exception diagnostics if the page doesn't catch it.
func (b *bridge) installCSPEvalGate() {
	if b.csp == nil || !b.csp.blocksEval() {
		return
	}
	_, _ = b.vm.RunString(`(function () {
	  var realFn = Function;
	  var blocked = function () {
	    throw new EvalError("call to eval()/Function() blocked by Content-Security-Policy (script-src lacks 'unsafe-eval')");
	  };
	  eval = blocked;
	  var F = function () { return blocked(); };
	  F.prototype = realFn.prototype;
	  Function = F;
	})();`)
}

// cspAllowConnect reports whether a fetch/XHR to abs is permitted by connect-src.
func (b *bridge) cspAllowConnect(abs string) bool {
	if b.csp == nil {
		return true
	}
	u, err := url.Parse(abs)
	if err != nil {
		return true
	}
	if !b.csp.allowsConnect(b.base, u) {
		b.recordError(fmt.Errorf("blocked by Content-Security-Policy: connect-src does not allow %s", abs))
		return false
	}
	if b.csp.reportOnlyBlocksConnect(b.base, u) {
		b.recordError(fmt.Errorf("Content-Security-Policy report-only: would block connection to %s", abs))
	}
	return true
}
