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
// body_rendered, and the device, clock, console and settle fields at the end of
// oracle) are optional on the replay side, so older captures still load.
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
	// HitCap is true when the capture gave up at the hard cap without the
	// network going quiet; InflightAtSnapshot counts script/data requests that
	// had still not completed when the oracle was taken (after the grace wait).
	HitCap             bool `json:"hit_cap"`
	InflightAtSnapshot int  `json:"inflight_at_snapshot"`
	// ConsoleErrors is what Chromium itself reported while loading the page:
	// uncaught exceptions and unhandled rejections ("exception: …"),
	// console.error/assert calls ("console.error: …") and the browser's own
	// error-level log entries ("log.network: …", "log.security: …"). It answers
	// "did Chromium raise this too" for an error unblink's engine records.
	// Exceptions, above, keeps its original meaning (uncaught exceptions only).
	ConsoleErrors []string `json:"console_errors,omitempty"`
	// The device the page saw. The capture fails if it is not unblink's constant
	// 1280x720@1x, so every stored oracle carries these exact values.
	ScreenW int     `json:"screen_width"`
	ScreenH int     `json:"screen_height"`
	InnerW  int     `json:"inner_width"`
	InnerH  int     `json:"inner_height"`
	DPR     float64 `json:"device_pixel_ratio"`
	// CapturedAtUnixMs is the page's own clock at navigation start
	// (performance.timeOrigin, milliseconds since the epoch). Replay can pin its
	// clock to it so "3 hours ago" renders as it did at capture. CapturedAt, the
	// wall-clock time the oracle was written, is kept as before.
	CapturedAtUnixMs int64 `json:"captured_at_unix_ms"`
	// TransparentHidden counts the opacity:0 subtrees left out of BodyText and
	// MainText (see oracleJS).
	TransparentHidden int `json:"transparent_hidden"`
	// ShadowComposed is true when BodyText/MainText were read across open shadow
	// roots (the composed tree) because plain innerText stops at a shadow
	// boundary. False means the page has no open shadow root and the text is
	// innerText exactly.
	ShadowComposed bool `json:"shadow_composed"`
	// Page-shape counts the manifest's feature cells are checked against: data
	// tables (visible, two rows or more), <details> elements, <text> elements
	// inside inline SVG, elements carrying an on* handler attribute, and the
	// body's computed text direction and the document language.
	Tables       int    `json:"tables"`
	Details      int    `json:"details"`
	SVGTexts     int    `json:"svg_texts"`
	HandlerAttrs int    `json:"handler_attrs"`
	Dir          string `json:"dir"`
	Lang         string `json:"lang"`
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
