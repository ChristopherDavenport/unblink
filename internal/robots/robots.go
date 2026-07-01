// Package robots parses robots.txt (the Robots Exclusion Protocol) for the
// purpose of *exposing* a site's crawl rules to an agent — not enforcing them.
// unblink never blocks a fetch based on robots.txt; it surfaces the rules as
// context so the agent (or its user) can decide.
//
// The parser is deliberately exposure-grade: it understands groups, the "*"
// user-agent, Allow/Disallow with '*' and trailing '$' wildcards, Crawl-delay,
// and global Sitemap directives. It uses pattern length as the match-specificity
// tie-breaker, a documented simplification of Google's matched-length rule that
// is adequate when the result is informational rather than gating.
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
	if g == nil {
		return true
	}
	bestLen := -1
	bestAllow := true
	for _, r := range g.Rules {
		if r.Pattern == "" {
			continue
		}
		if !pathMatch(r.Pattern, path) {
			continue
		}
		switch {
		case len(r.Pattern) > bestLen:
			bestLen, bestAllow = len(r.Pattern), r.Allow
		case len(r.Pattern) == bestLen && r.Allow:
			bestAllow = true // Allow wins a specificity tie
		}
	}
	if bestLen < 0 {
		return true // nothing matched
	}
	return bestAllow
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
