// Package robots parses robots.txt (the Robots Exclusion Protocol) and matches
// paths against it. It is pure: it never fetches and never decides what to do
// with a verdict. By default unblink only *exposes* the rules to the agent as
// context; an operator can opt in to enforcement (--respect-robots, ADR 0020),
// in which case internal/browser refuses navigations this package reports as
// disallowed.
//
// The parser understands groups, the "*" user-agent, Allow/Disallow with '*'
// and trailing '$' wildcards, Crawl-delay, and global Sitemap directives. The
// most specific rule is the one with the longest pattern (RFC 9309 section
// 2.2.2), Allow winning a tie. Allowed/Match compare the path as given;
// MatchNormalized first normalizes percent-encoding on both sides, which is what
// the enforcing path uses so an equivalent spelling of a path cannot slip past a
// rule.
package robots

import (
	"bytes"
	"strconv"
	"strings"
)

// maxSize bounds how many bytes of a robots.txt we parse, matching Google's
// 512 KiB limit. Anything beyond is ignored.
const maxSize = 512 * 1024

// Policy is a parsed robots.txt. Groups appear in file order; Sitemaps are
// global (they belong to the file, not to any single group).
type Policy struct {
	Groups   []Group
	Sitemaps []string
}

// Group is a User-agent record with its rules. Agents are lowercased tokens.
type Group struct {
	Agents     []string
	Rules      []Rule
	CrawlDelay float64 // seconds; 0 if unset
}

// Rule is one Allow or Disallow directive.
type Rule struct {
	Pattern string // path pattern; may contain '*' and a trailing '$'
	Allow   bool
}

// Parse parses robots.txt bytes. It never returns nil: an empty or
// unrecognizable body yields a Policy with no groups, which every path treats as
// allow-all.
func Parse(data []byte) *Policy {
	if len(data) > maxSize {
		data = data[:maxSize]
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // strip UTF-8 BOM

	p := &Policy{}
	var cur *Group   // group currently being filled
	var sawRule bool // has cur collected a rule yet? (closes the agent list)

	for _, raw := range strings.Split(string(data), "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(line[:colon]))
		value := strings.TrimSpace(line[colon+1:])

		switch field {
		case "user-agent", "useragent":
			if value == "" {
				continue
			}
			// A user-agent line after rules starts a fresh group; consecutive
			// user-agent lines accumulate onto the same group.
			if cur == nil || sawRule {
				p.Groups = append(p.Groups, Group{})
				cur = &p.Groups[len(p.Groups)-1]
				sawRule = false
			}
			cur.Agents = append(cur.Agents, strings.ToLower(value))
		case "allow", "disallow":
			if cur == nil {
				continue // rule before any user-agent — malformed, skip
			}
			cur.Rules = append(cur.Rules, Rule{Pattern: value, Allow: field == "allow"})
			sawRule = true
		case "crawl-delay", "crawldelay":
			if cur == nil {
				continue
			}
			if d, err := strconv.ParseFloat(value, 64); err == nil && d >= 0 {
				cur.CrawlDelay = d
			}
			sawRule = true
		case "sitemap":
			if value != "" {
				p.Sitemaps = append(p.Sitemaps, value)
			}
		}
	}
	return p
}

// StarGroup returns the group that applies to the "*" user-agent, or nil if the
// file has none. Because unblink presents a standard browser User-Agent (not a
// named bot token), "*" is the group that governs us.
func (p *Policy) StarGroup() *Group {
	if p == nil {
		return nil
	}
	for i := range p.Groups {
		for _, a := range p.Groups[i].Agents {
			if a == "*" {
				return &p.Groups[i]
			}
		}
	}
	return nil
}

// Allowed reports whether path is permitted by the group, using
// longest-matching-pattern-wins with Allow winning ties. A nil group (no rules
// apply) allows everything. Empty-pattern rules have no effect.
func (g *Group) Allowed(path string) bool {
	_, allowed := g.match(path, false)
	return allowed
}

// Match is Allowed plus the rule that decided: rule is the winning Allow or
// Disallow directive, or nil when no rule matched (the path is then allowed).
func (g *Group) Match(path string) (rule *Rule, allowed bool) {
	return g.match(path, false)
}

// MatchNormalized is Match after normalizing percent-encoding in both the path
// and each pattern (see Normalize), as RFC 9309 section 2.2.2 requires before
// comparison. The returned rule carries the pattern as written in robots.txt.
func (g *Group) MatchNormalized(path string) (rule *Rule, allowed bool) {
	return g.match(Normalize(path), true)
}

func (g *Group) match(path string, normalize bool) (*Rule, bool) {
	if g == nil {
		return nil, true
	}
	bestLen := -1
	var best *Rule
	for i := range g.Rules {
		r := &g.Rules[i]
		if r.Pattern == "" {
			continue
		}
		pattern := r.Pattern
		if normalize {
			pattern = Normalize(pattern)
		}
		if !pathMatch(pattern, path) {
			continue
		}
		switch {
		case len(pattern) > bestLen:
			bestLen, best = len(pattern), r
		case len(pattern) == bestLen && r.Allow && !best.Allow:
			best = r // Allow wins a specificity tie
		}
	}
	if best == nil {
		return nil, true // nothing matched
	}
	return best, best.Allow
}

// Normalize puts a URL path (with optional query) or a robots.txt pattern into
// the canonical form RFC 9309 section 2.2.2 compares in: bytes outside
// printable US-ASCII are percent-encoded, a percent-encoded unreserved
// character (ALPHA / DIGIT / "-" / "." / "_" / "~") is decoded, and the hex
// digits of every other escape are uppercased. Reserved characters are left
// exactly as written ("/a%2Fb" and "/a/b" stay distinct), as are the pattern
// metacharacters '*' and '$'. A '%' that does not start an escape becomes
// "%25", which keeps the result stable under a second pass.
func Normalize(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			v := unhex(s[i+1])<<4 | unhex(s[i+2])
			if isUnreserved(v) {
				b.WriteByte(v)
			} else {
				b.WriteByte('%')
				b.WriteByte(hex[v>>4])
				b.WriteByte(hex[v&0x0F])
			}
			i += 2
		case c <= 0x20 || c >= 0x7F || c == '%':
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// pathMatch reports whether a robots path pattern matches path. '*' matches any
// run of characters; a trailing '$' anchors the match to the end of path.
func pathMatch(pattern, path string) bool {
	anchorEnd := strings.HasSuffix(pattern, "$")
	if anchorEnd {
		pattern = pattern[:len(pattern)-1]
	}
	segs := strings.Split(pattern, "*")
	pos := 0
	for i, seg := range segs {
		last := i == len(segs)-1
		if seg == "" {
			if last && anchorEnd {
				// pattern ended with "*$": the '*' already consumed the rest.
				return true
			}
			continue
		}
		switch {
		case i == 0:
			if !strings.HasPrefix(path, seg) {
				return false
			}
			pos = len(seg)
			if last && anchorEnd {
				return pos == len(path)
			}
		case last && anchorEnd:
			// the final literal must end exactly at the end of path
			return len(path)-pos >= len(seg) && strings.HasSuffix(path, seg)
		default:
			idx := strings.Index(path[pos:], seg)
			if idx < 0 {
				return false
			}
			pos += idx + len(seg)
		}
	}
	return true
}
