package webext

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

// Bundle is a loaded, ready-to-use extension: its parsed manifest, a filesystem view
// of its files (an unpacked dir or the archive interior, read uniformly through fs.FS),
// a stable ID, and its compiled static network rules.
type Bundle struct {
	ID       string
	Manifest *Manifest
	FS       fs.FS
	BaseURL  string       // "chrome-extension://<id>/"
	Net      *RuleMatcher // compiled static declarativeNetRequest rulesets (never nil)
	Locales  *Locales     // default-locale messages for i18n / __MSG__ substitution
}

// ContentScriptsFor returns the content scripts that should run on u, in manifest
// order.
func (b *Bundle) ContentScriptsFor(u *url.URL) []ContentScript {
	if b.Manifest == nil {
		return nil
	}
	var out []ContentScript
	for _, cs := range b.Manifest.ContentScripts {
		if cs.MatchesURL(u) {
			out = append(out, cs)
		}
	}
	return out
}

// ReadResource reads an extension-relative file (a content-script JS/CSS path) through
// the bundle's filesystem view, rejecting paths that escape the extension root.
func (b *Bundle) ReadResource(p string) ([]byte, error) {
	rel := cleanRel(p)
	if rel == "" {
		return nil, fmt.Errorf("webext: invalid resource path %q", p)
	}
	return fs.ReadFile(b.FS, rel)
}

// buildBundle assembles a Bundle from a parsed manifest and its file view, compiling
// every enabled static ruleset.
func buildBundle(m *Manifest, fsys fs.FS) (*Bundle, error) {
	id := extensionID(m)
	b := &Bundle{
		ID:       id,
		Manifest: m,
		FS:       fsys,
		BaseURL:  "chrome-extension://" + id + "/",
	}
	var rules []DNRRule
	for _, rr := range m.DNR.RuleResources {
		if !rr.Enabled {
			continue
		}
		rel := cleanRel(rr.Path)
		if rel == "" {
			return nil, fmt.Errorf("webext: invalid ruleset path %q", rr.Path)
		}
		data, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, fmt.Errorf("webext: read ruleset %q: %w", rr.Path, err)
		}
		rs, err := ParseRules(data)
		if err != nil {
			return nil, fmt.Errorf("webext: ruleset %q: %w", rr.Path, err)
		}
		rules = append(rules, rs...)
	}
	nm, err := NewRuleMatcher(rules)
	if err != nil {
		return nil, err
	}
	b.Net = nm
	b.Locales = loadLocales(fsys, m.DefaultLocale)
	return b, nil
}

// cleanRel normalizes a manifest-relative resource path into a valid fs.FS path,
// returning "" if it escapes the extension root.
func cleanRel(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	p = path.Clean(p)
	if p == "." || !fs.ValidPath(p) {
		return ""
	}
	return p
}

// extensionID derives a stable extension ID. When the manifest carries a base64 SPKI
// public key it uses Chrome's algorithm (SHA-256 of the DER key, first 16 bytes mapped
// to a–p); otherwise it derives a deterministic ID from the name and version so the
// same extension always gets the same chrome-extension:// origin.
func extensionID(m *Manifest) string {
	if m.Key != "" {
		if der, err := base64.StdEncoding.DecodeString(m.Key); err == nil && len(der) > 0 {
			sum := sha256.Sum256(der)
			return mpDecode(sum[:16])
		}
	}
	sum := sha256.Sum256([]byte(m.Name + "\x00" + m.Version))
	return mpDecode(sum[:16])
}

// mpDecode maps bytes to Chrome's "mpdecimal" alphabet: each nibble becomes a letter
// a–p, so 16 bytes yield a 32-character ID.
func mpDecode(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, x := range b {
		sb.WriteByte('a' + (x >> 4))
		sb.WriteByte('a' + (x & 0x0f))
	}
	return sb.String()
}
