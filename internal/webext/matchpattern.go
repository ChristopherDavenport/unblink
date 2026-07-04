package webext

import (
	"fmt"
	"net/url"
	"strings"
)

// MatchPattern is a WebExtensions match pattern such as "*://*.example.com/*" or the
// special "<all_urls>". It restricts which pages a content script runs on and which
// requests a web_accessible_resource is exposed to. It is matched, never mutated,
// after parsing, so the zero value is an (empty, never-matching) pattern.
type MatchPattern struct {
	allURLs         bool
	scheme          string // "http","https","ws","wss","file","ftp", or "*" (any web scheme)
	anyHost         bool   // host was "*"
	matchSubdomains bool   // host began with "*."
	host            string // lowercased host (the bare domain when matchSubdomains)
	path            string // glob path, always begins with "/"
}

const allURLsPattern = "<all_urls>"

// ParseMatchPattern parses one match pattern. It rejects structurally invalid
// patterns; a manifest loader skips the invalid ones rather than failing the whole
// extension (Phase 1 does not act on content scripts yet).
func ParseMatchPattern(s string) (MatchPattern, error) {
	if s == allURLsPattern {
		return MatchPattern{allURLs: true}, nil
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return MatchPattern{}, fmt.Errorf("webext: match pattern %q: missing scheme separator", s)
	}
	switch scheme {
	case "*", "http", "https", "ws", "wss", "file", "ftp":
	default:
		return MatchPattern{}, fmt.Errorf("webext: match pattern %q: unsupported scheme %q", s, scheme)
	}
	host, path, hasPath := strings.Cut(rest, "/")
	if !hasPath {
		return MatchPattern{}, fmt.Errorf("webext: match pattern %q: missing path", s)
	}
	m := MatchPattern{scheme: scheme, path: "/" + path}
	host = strings.ToLower(host)
	switch {
	case host == "*":
		m.anyHost = true
	case strings.HasPrefix(host, "*."):
		m.matchSubdomains = true
		m.host = host[2:]
	case strings.Contains(host, "*"):
		return MatchPattern{}, fmt.Errorf("webext: match pattern %q: invalid host wildcard", s)
	case host == "" && scheme != "file":
		return MatchPattern{}, fmt.Errorf("webext: match pattern %q: empty host", s)
	default:
		m.host = host
	}
	return m, nil
}

// Matches reports whether u satisfies the pattern.
func (m MatchPattern) Matches(u *url.URL) bool {
	if u == nil {
		return false
	}
	if m.allURLs {
		switch u.Scheme {
		case "http", "https", "ws", "wss", "ftp", "file":
			return true
		}
		return false
	}
	if !m.schemeMatches(u.Scheme) {
		return false
	}
	if !m.anyHost {
		host := strings.ToLower(u.Hostname())
		if m.matchSubdomains {
			if host != m.host && !strings.HasSuffix(host, "."+m.host) {
				return false
			}
		} else if host != m.host {
			return false
		}
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	// Match-pattern paths special-case only '*'; a literal '?' in the URL is matched
	// by the pattern's wildcard, so '?' is not a single-char wildcard here.
	return globMatch(m.path, p, false)
}

func (m MatchPattern) schemeMatches(s string) bool {
	if m.scheme == "*" {
		return s == "http" || s == "https" || s == "ws" || s == "wss"
	}
	return s == m.scheme
}

// Glob is a shell-style wildcard string (with '*' and '?') used by
// include_globs/exclude_globs and web_accessible_resources entries.
type Glob string

// Matches reports whether s matches the glob.
func (g Glob) Matches(s string) bool { return globMatch(string(g), s, true) }

// globMatch reports whether pattern matches s. '*' matches any run (including empty);
// '?' matches exactly one character only when qmark is true. It is an iterative
// two-pointer matcher with linear backtracking on '*' — no regexp, so no ReDoS.
func globMatch(pattern, s string, qmark bool) bool {
	var p, si, star, starMatch int
	star = -1
	for si < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == s[si] || (qmark && pattern[p] == '?')):
			p++
			si++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			starMatch = si
			p++
		case star != -1:
			p = star + 1
			starMatch++
			si = starMatch
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
