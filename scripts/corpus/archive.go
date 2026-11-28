package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// The on-disk capture format (version 1). The replay side (the root module's
// `corpus` package, build tag `corpus`) carries its own copy of these types —
// the two modules deliberately do not import each other — so a change here must
// be mirrored in corpus/archive.go and bump archiveVersion. Fields added since
// version 1 was first written (req_body, settle_rule, settled_after_ms,
// body_rendered) are optional on the replay side, so older captures still load.
//
//	<out>/<id>/archive.json   index of every recorded exchange
//	<out>/<id>/bodies/<hex>   response bodies, named by SHA-256 of their bytes
//	<out>/<id>/oracle.json    what Chromium rendered
const archiveVersion = 1

type archive struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	URL     string  `json:"url"`
	Entries []entry `json:"entries"`
}

type entry struct {
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Status     int                 `json:"status,omitempty"`
	StatusText string              `json:"status_text,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	MIME       string              `json:"mime,omitempty"`
	Type       string              `json:"type,omitempty"` // CDP resource type (Document, Script, Fetch, ...)
	Body       string              `json:"body,omitempty"` // SHA-256 hex of the body file
	Size       int                 `json:"size,omitempty"`
	// Text marks a body Chromium handed back as decoded text: it is UTF-8
	// regardless of the charset the server declared, so replay must present it as
	// UTF-8. Charset transcoding is therefore NOT exercised by replay.
	Text bool `json:"text,omitempty"`
	// Omitted says why the body was not stored (resource type unblink never
	// fetches, over the size cap, or the browser no longer had it).
	Omitted string `json:"omitted,omitempty"`
	// Failed is Chromium's network error when the request never produced a
	// response; replay fails it too, without counting it as a replay miss.
	Failed string `json:"failed,omitempty"`
	// ReqBody is the SHA-256 (hex) of the request body Chromium sent, when there
	// was one. Replay prefers the exchange whose request body matches, so two
	// POSTs to one URL are not answered in mere capture order.
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
	// SettleRule names the rule the capture settled under and SettledAfterMS how
	// long it watched the page before taking the oracle.
	SettleRule     string `json:"settle_rule"`
	SettledAfterMS int64  `json:"settled_after_ms"`
	// BodyRendered is whether document.body had a layout box at the snapshot.
	// When it did not, innerText is textContent and the oracle is unusable.
	BodyRendered bool `json:"body_rendered"`
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// writeBody stores b under its content hash and returns the hex name.
func writeBody(dir string, b []byte) (string, error) {
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:])
	return name, os.WriteFile(filepath.Join(dir, "bodies", name), b, 0o644)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
