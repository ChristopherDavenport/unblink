// Package webext models Firefox/Chrome WebExtensions well enough to load a
// user-supplied extension (uBlock Origin and friends) at runtime. It is pure Go with
// no dependency on unblink's browser or JS engine: internal/js and internal/browser
// import it, never the reverse.
//
// unblink ships no extension code. An extension is a runtime-loaded artifact the
// operator supplies — an unpacked directory or a .xpi/.crx/.zip archive — which keeps
// GPL-licensed extensions fully separate from unblink's MIT source (see ADR 0010).
//
// Phase 1 acts only on the declarative_net_request static rulesets (network blocking);
// the content-script, background, and messaging fields are parsed for completeness and
// consumed by later phases.
package webext

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// Manifest is the parsed, normalized subset of an extension's manifest.json. It spans
// Manifest V2 and V3; shape differences (background, web_accessible_resources) are
// normalized away during parsing.
type Manifest struct {
	ManifestVersion     int
	Name                string
	Version             string
	Description         string
	DefaultLocale       string
	Key                 string // base64 SPKI public key, when present; derives the extension ID
	Permissions         []string
	OptionalPermissions []string
	HostPermissions     []string
	Background          Background
	ContentScripts      []ContentScript
	WebAccessible       []WebAccessibleResource
	DNR                 DeclarativeNetRequest
	Action              *Action
}

// Background describes the extension's background context: an MV3 service worker, an MV2
// background-page HTML file, or an MV2 background scripts list.
type Background struct {
	ServiceWorker string   // MV3 service_worker
	Page          string   // MV2 background.page (an HTML file whose <script>s are the background)
	Scripts       []string // MV2 background.scripts
	Module        bool     // service_worker type == "module"
	Persistent    bool     // MV2 persistent background page (default true in MV2)
}

// HasBackground reports whether the manifest declares any background context.
func (b Background) HasBackground() bool {
	return b.ServiceWorker != "" || b.Page != "" || len(b.Scripts) > 0
}

// RunAt is a content script's injection timing.
type RunAt string

// Content-script injection phases.
const (
	RunAtStart RunAt = "document_start"
	RunAtEnd   RunAt = "document_end"
	RunAtIdle  RunAt = "document_idle"
)

// ContentScript is one content_scripts entry: JS/CSS injected into matching pages.
type ContentScript struct {
	Matches        []MatchPattern
	ExcludeMatches []MatchPattern
	IncludeGlobs   []Glob
	ExcludeGlobs   []Glob
	JS             []string
	CSS            []string
	RunAt          RunAt
	AllFrames      bool
	World          string // "ISOLATED" (default) or "MAIN"
}

// MatchesURL reports whether this content script should run on u: at least one
// Matches pattern matches, no ExcludeMatches does, and the include/exclude globs (if
// any) are satisfied against the full URL string.
func (cs ContentScript) MatchesURL(u *url.URL) bool {
	if !anyPatternMatch(cs.Matches, u) || anyPatternMatch(cs.ExcludeMatches, u) {
		return false
	}
	if len(cs.IncludeGlobs) > 0 && !anyGlobMatch(cs.IncludeGlobs, u.String()) {
		return false
	}
	return !anyGlobMatch(cs.ExcludeGlobs, u.String())
}

func anyPatternMatch(ps []MatchPattern, u *url.URL) bool {
	for _, p := range ps {
		if p.Matches(u) {
			return true
		}
	}
	return false
}

func anyGlobMatch(gs []Glob, s string) bool {
	for _, g := range gs {
		if g.Matches(s) {
			return true
		}
	}
	return false
}

// WebAccessibleResource is one web_accessible_resources entry. MV2's flat glob array
// is normalized to a single entry with empty Matches.
type WebAccessibleResource struct {
	Resources []Glob
	Matches   []MatchPattern
}

// Action is browser_action/action metadata. unblink renders no UI, so only the
// descriptive fields are retained.
type Action struct {
	DefaultTitle string
	DefaultIcon  json.RawMessage
}

// DeclarativeNetRequest is the declarative_net_request manifest key: the static
// rulesets an MV3 extension ships. Dynamic/session rules (added by extension JS at
// runtime) live on the RuleMatcher, not here.
type DeclarativeNetRequest struct {
	RuleResources []RuleResource
}

// RuleResource names one static ruleset JSON file bundled in the extension.
type RuleResource struct {
	ID      string
	Enabled bool
	Path    string
}

type rawManifest struct {
	ManifestVersion     int                `json:"manifest_version"`
	Name                string             `json:"name"`
	Version             string             `json:"version"`
	Description         string             `json:"description"`
	DefaultLocale       string             `json:"default_locale"`
	Key                 string             `json:"key"`
	Permissions         []string           `json:"permissions"`
	OptionalPermissions []string           `json:"optional_permissions"`
	HostPermissions     []string           `json:"host_permissions"`
	Background          rawBackground      `json:"background"`
	ContentScripts      []rawContentScript `json:"content_scripts"`
	WebAccessible       json.RawMessage    `json:"web_accessible_resources"`
	DNR                 rawDNR             `json:"declarative_net_request"`
	Action              *rawAction         `json:"action"`
	BrowserAction       *rawAction         `json:"browser_action"`
}

type rawBackground struct {
	ServiceWorker string   `json:"service_worker"`
	Page          string   `json:"page"`
	Scripts       []string `json:"scripts"`
	Type          string   `json:"type"`
	Persistent    *bool    `json:"persistent"`
}

type rawContentScript struct {
	Matches        []string `json:"matches"`
	ExcludeMatches []string `json:"exclude_matches"`
	IncludeGlobs   []string `json:"include_globs"`
	ExcludeGlobs   []string `json:"exclude_globs"`
	JS             []string `json:"js"`
	CSS            []string `json:"css"`
	RunAt          string   `json:"run_at"`
	AllFrames      bool     `json:"all_frames"`
	World          string   `json:"world"`
}

type rawAction struct {
	DefaultTitle string          `json:"default_title"`
	DefaultIcon  json.RawMessage `json:"default_icon"`
}

type rawDNR struct {
	RuleResources []rawRuleResource `json:"rule_resources"`
}

type rawRuleResource struct {
	ID      string `json:"id"`
	Enabled *bool  `json:"enabled"`
	Path    string `json:"path"`
}

type rawWAR struct {
	Resources []string `json:"resources"`
	Matches   []string `json:"matches"`
}

// ParseManifest parses and normalizes an extension's manifest.json.
func ParseManifest(data []byte) (*Manifest, error) {
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("webext: parse manifest: %w", err)
	}
	if raw.ManifestVersion == 0 {
		return nil, fmt.Errorf("webext: manifest missing manifest_version")
	}
	m := &Manifest{
		ManifestVersion:     raw.ManifestVersion,
		Name:                raw.Name,
		Version:             raw.Version,
		Description:         raw.Description,
		DefaultLocale:       raw.DefaultLocale,
		Key:                 raw.Key,
		Permissions:         raw.Permissions,
		OptionalPermissions: raw.OptionalPermissions,
		HostPermissions:     raw.HostPermissions,
		Background: Background{
			ServiceWorker: raw.Background.ServiceWorker,
			Page:          raw.Background.Page,
			Scripts:       raw.Background.Scripts,
			Module:        raw.Background.Type == "module",
			// MV2 background pages are persistent unless told otherwise; MV3 has no
			// such field, so the default is harmless there.
			Persistent: raw.Background.Persistent == nil || *raw.Background.Persistent,
		},
	}
	for _, cs := range raw.ContentScripts {
		m.ContentScripts = append(m.ContentScripts, ContentScript{
			Matches:        parseMatchList(cs.Matches),
			ExcludeMatches: parseMatchList(cs.ExcludeMatches),
			IncludeGlobs:   toGlobs(cs.IncludeGlobs),
			ExcludeGlobs:   toGlobs(cs.ExcludeGlobs),
			JS:             cs.JS,
			CSS:            cs.CSS,
			RunAt:          normalizeRunAt(cs.RunAt),
			AllFrames:      cs.AllFrames,
			World:          cs.World,
		})
	}
	m.WebAccessible = parseWAR(raw.WebAccessible)
	for _, rr := range raw.DNR.RuleResources {
		m.DNR.RuleResources = append(m.DNR.RuleResources, RuleResource{
			ID:      rr.ID,
			Enabled: rr.Enabled == nil || *rr.Enabled, // lenient: absent → enabled
			Path:    rr.Path,
		})
	}
	if a := raw.Action; a != nil {
		m.Action = &Action{DefaultTitle: a.DefaultTitle, DefaultIcon: a.DefaultIcon}
	} else if a := raw.BrowserAction; a != nil {
		m.Action = &Action{DefaultTitle: a.DefaultTitle, DefaultIcon: a.DefaultIcon}
	}
	return m, nil
}

// parseMatchList parses each pattern, dropping the invalid ones (a single bad pattern
// should not sink the whole extension; Phase 1 does not act on these yet).
func parseMatchList(patterns []string) []MatchPattern {
	var out []MatchPattern
	for _, p := range patterns {
		if mp, err := ParseMatchPattern(p); err == nil {
			out = append(out, mp)
		}
	}
	return out
}

func toGlobs(ss []string) []Glob {
	if len(ss) == 0 {
		return nil
	}
	out := make([]Glob, len(ss))
	for i, s := range ss {
		out[i] = Glob(s)
	}
	return out
}

func normalizeRunAt(s string) RunAt {
	switch RunAt(s) {
	case RunAtStart, RunAtEnd:
		return RunAt(s)
	default:
		return RunAtIdle // WebExtensions default
	}
}

// parseWAR handles both manifest shapes: MV2's flat ["*.png", ...] and MV3's
// [{resources, matches}, ...].
func parseWAR(raw json.RawMessage) []WebAccessibleResource {
	if len(raw) == 0 {
		return nil
	}
	var flat []string
	if err := json.Unmarshal(raw, &flat); err == nil {
		if len(flat) == 0 {
			return nil
		}
		return []WebAccessibleResource{{Resources: toGlobs(flat)}}
	}
	var objs []rawWAR
	if err := json.Unmarshal(raw, &objs); err != nil {
		return nil
	}
	out := make([]WebAccessibleResource, 0, len(objs))
	for _, o := range objs {
		out = append(out, WebAccessibleResource{
			Resources: toGlobs(o.Resources),
			Matches:   parseMatchList(o.Matches),
		})
	}
	return out
}
