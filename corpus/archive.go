//go:build corpus

// Package corpus is the real-page differential harness (ADR 0022): it replays
// pages captured from the live web through the full unblink pipeline with the
// network replaced by the capture, and scores the result against what headless
// Chromium rendered at capture time. Like wpt/ it is an on-demand measurement
// behind a build tag, never a CI gate.
package corpus

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// The capture format (version 1) is written by scripts/corpus — a separate
// module that this one deliberately does not import — so these types mirror
// scripts/corpus/archive.go and must change together with it.
const archiveVersion = 1

// Captures are untrusted input (bytes off the open web, written by another
// tool): every read is size-bounded and no path is ever built from a URL.
const (
	maxIndexBytes  = 32 << 20  // archive.json / oracle.json
	maxBodyBytes   = 32 << 20  // one response body
	maxTotalBytes  = 512 << 20 // all bodies of one page
	maxEntries     = 20000
	maxManifestLen = 2000
)

type archive struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Entries []*entry `json:"entries"`
}

type entry struct {
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Status     int                 `json:"status,omitempty"`
	StatusText string              `json:"status_text,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	MIME       string              `json:"mime,omitempty"`
	Type       string              `json:"type,omitempty"`
	Body       string              `json:"body,omitempty"`
	Size       int                 `json:"size,omitempty"`
	Text       bool                `json:"text,omitempty"`
	Omitted    string              `json:"omitted,omitempty"`
	Failed     string              `json:"failed,omitempty"`
	// ReqBody is the SHA-256 of the request body Chromium sent (captures made
	// before the field existed leave it empty); replay uses it to tell apart
	// repeated POSTs to one URL.
	ReqBody string `json:"req_body,omitempty"`
}

type oracle struct {
	URL            string   `json:"url"`
	FinalURL       string   `json:"final_url"`
	Title          string   `json:"title"`
	BodyText       string   `json:"body_text"`
	MainText       string   `json:"main_text"`
	MainSelector   string   `json:"main_selector"`
	LinksTotal     int      `json:"links_total"`
	LinksVisible   int      `json:"links_visible"`
	Forms          int      `json:"forms"`
	Controls       int      `json:"controls_visible"`
	DOMNodes       int      `json:"dom_nodes"`
	ShadowRoots    int      `json:"shadow_roots"`
	CustomElements int      `json:"custom_elements"`
	Charset        string   `json:"charset"`
	DocStatus      int      `json:"doc_status"`
	Exceptions     []string `json:"exceptions,omitempty"`
	Settled        bool     `json:"settled"`
	Requests       int      `json:"requests"`
	Chromium       string   `json:"chromium"`
	UserAgent      string   `json:"user_agent"`
	Viewport       string   `json:"viewport"`
	CapturedAt     string   `json:"captured_at"`
	// SettleRule names the settle rule the capture ran under, and SettledAfterMS
	// how long it watched. Captures made before the fields existed leave them
	// empty: which rule produced those cannot be recovered from the archive.
	SettleRule     string `json:"settle_rule,omitempty"`
	SettledAfterMS int64  `json:"settled_after_ms,omitempty"`
	// BodyRendered is false when document.body had no layout box at the snapshot,
	// which makes innerText fall back to textContent (hidden text included).
	BodyRendered *bool `json:"body_rendered,omitempty"`
}

// manifestPage is one checked-in corpus entry: where to capture, and which
// feature-matrix cells ("group:name") the page is evidence for.
type manifestPage struct {
	ID    string   `json:"id"`
	URL   string   `json:"url"`
	Cells []string `json:"cells"`
	Note  string   `json:"note"`
}

//go:embed manifest.json
var manifestJSON []byte

var (
	idRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	bodyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	cellRE = regexp.MustCompile(`^[a-z]+:[a-z0-9-]+$`)
)

func loadManifest() ([]manifestPage, error) {
	var m struct {
		Pages []manifestPage `json:"pages"`
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if len(m.Pages) > maxManifestLen {
		return nil, fmt.Errorf("manifest.json: %d pages exceeds %d", len(m.Pages), maxManifestLen)
	}
	seen := map[string]bool{}
	for _, p := range m.Pages {
		switch {
		case !idRE.MatchString(p.ID):
			return nil, fmt.Errorf("manifest.json: invalid id %q", p.ID)
		case seen[p.ID]:
			return nil, fmt.Errorf("manifest.json: duplicate id %q", p.ID)
		case len(p.Cells) == 0:
			return nil, fmt.Errorf("manifest.json: %s has no cells", p.ID)
		}
		seen[p.ID] = true
		for _, c := range p.Cells {
			if !cellRE.MatchString(c) {
				return nil, fmt.Errorf("manifest.json: %s: invalid cell %q (want group:name)", p.ID, c)
			}
		}
	}
	return m.Pages, nil
}

// readBounded reads a file of at most limit bytes.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", filepath.Base(path), limit)
	}
	return b, nil
}

// loadArchive reads and validates a page's index. Bodies stay on disk and are
// read on demand by body(); the only filename ever joined to dir is a validated
// 64-hex content hash, so a hostile index cannot walk out of the capture.
func loadArchive(dir string) (*archive, error) {
	raw, err := readBounded(filepath.Join(dir, "archive.json"), maxIndexBytes)
	if err != nil {
		return nil, err
	}
	var a archive
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("archive.json: %w", err)
	}
	if a.Version != archiveVersion {
		return nil, fmt.Errorf("archive.json: version %d, want %d (recapture)", a.Version, archiveVersion)
	}
	if len(a.Entries) > maxEntries {
		return nil, fmt.Errorf("archive.json: %d entries exceeds %d", len(a.Entries), maxEntries)
	}
	var total int64
	seen := map[string]bool{}
	for i, e := range a.Entries {
		if e == nil {
			return nil, fmt.Errorf("archive.json: entry %d is null", i)
		}
		if e.Body == "" {
			continue
		}
		if !bodyRE.MatchString(e.Body) {
			return nil, fmt.Errorf("archive.json: entry %d: body name is not a content hash", i)
		}
		if seen[e.Body] {
			continue
		}
		seen[e.Body] = true
		fi, err := os.Lstat(filepath.Join(dir, "bodies", e.Body))
		if err != nil {
			return nil, fmt.Errorf("archive.json: entry %d: %w", i, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("archive.json: entry %d: body is not a regular file", i)
		}
		if fi.Size() > maxBodyBytes {
			return nil, fmt.Errorf("archive.json: entry %d: body larger than %d bytes", i, maxBodyBytes)
		}
		if total += fi.Size(); total > maxTotalBytes {
			return nil, fmt.Errorf("archive.json: bodies exceed %d bytes", maxTotalBytes)
		}
	}
	return &a, nil
}

// body returns an entry's recorded bytes (nil when none were stored).
func (e *entry) body(dir string) ([]byte, error) {
	if e.Body == "" {
		return nil, nil
	}
	if !bodyRE.MatchString(e.Body) {
		return nil, fmt.Errorf("body name is not a content hash")
	}
	return readBounded(filepath.Join(dir, "bodies", e.Body), maxBodyBytes)
}

func loadOracle(dir string) (*oracle, error) {
	raw, err := readBounded(filepath.Join(dir, "oracle.json"), maxIndexBytes)
	if err != nil {
		return nil, err
	}
	var o oracle
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("oracle.json: %w", err)
	}
	return &o, nil
}
