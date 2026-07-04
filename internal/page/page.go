// Package page defines the core data model that flows through the unblink
// pipeline. Every stage (fetch, parse, reduce, emit) takes a *Page and enriches
// it in place; stages never re-fetch or re-parse. The canonical parsed document
// is always Doc — the *html.Node tree from golang.org/x/net/html — and every
// stage reads or mutates that one tree rather than introducing a parallel model.
package page

import (
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/html"
)

// Page is the single value that flows through the pipeline, progressively
// filled in by each stage.
//
// Stage contract:
//   - fetch  fills the transport fields and Raw (body decoded to UTF-8)
//   - dom    fills Doc, then Meta (via Extract)
//   - reduce fills Article
//   - emit   fills Markdown
//
// Kind classifies a fetched body so the pipeline knows whether to parse it as
// HTML or route it to a non-HTML converter. The zero value ("") is treated as
// HTML by downstream stages, preserving behaviour for internally-built pages.
type Kind string

const (
	KindHTML   Kind = "html"
	KindPDF    Kind = "pdf"
	KindJSON   Kind = "json"
	KindFeed   Kind = "feed"
	KindText   Kind = "text"
	KindImage  Kind = "image"
	KindBinary Kind = "binary"
)

type Page struct {
	RequestURL  *url.URL // what was asked for
	FinalURL    *url.URL // after redirects
	StatusCode  int
	Header      http.Header
	ContentType string
	Charset     string
	Kind        Kind // content classification; set by the orchestrator, "" == html

	Raw []byte     // response body: decoded to UTF-8 for textual types, raw bytes otherwise
	Doc *html.Node // canonical parsed tree — single source of truth (nil for non-HTML)

	Meta       PageMeta
	Article    *Article    // populated only after reduce; nil otherwise
	Markdown   string      // populated only after emit
	Rendered   bool        // did JavaScript run? (always false until the JS phase)
	RenderDiag *RenderDiag // JS render diagnostics; nil unless JavaScript ran

	FetchedAt time.Time
}

// RenderDiag is best-effort diagnostics from a JavaScript render: the detected
// framework, any uncaught script errors, and how many custom elements upgraded.
// Mirrors js.RenderResult without importing the js package (page is the base model).
type RenderDiag struct {
	Framework string
	Errors    []string
	Upgrades  int

	// WaitRequested/WaitMet report a read wait_for gate; PendingNavigation is a URL
	// the page's JS asked to navigate to (location.href/assign/replace) but that the
	// render did not follow. All best-effort.
	WaitRequested     bool
	WaitMet           bool
	PendingNavigation string

	// Saturation: whether the snapshot was taken while the page was still working.
	// NetPending > 0 or (DeadlineHit && DOMBusy) means the render was cut off
	// mid-hydration and the content may be incomplete. NetDenied is filled by the
	// browser from its per-render request-budget guard (the js layer only sees a
	// generic error); the rest mirror js.RenderResult.
	NetRequests   int  // subrequests the page attempted
	NetFailed     int  // subrequests that errored (incl. budget/rate denials)
	NetPending    int  // subrequests still in flight at snapshot
	NetDenied     int  // subrequests blocked by the per-render request budget
	DeadlineHit   bool // the JS budget elapsed before the page went quiet
	DOMBusy       bool // the DOM was still mutating when the snapshot was taken
	TimersPending int  // one-shot timers still scheduled at snapshot (content may be behind one)

	// Requests/Console are the page-JS network log and console output captured
	// during the render, surfaced by the requests/console tools (not dumped on
	// every read, for token budget). Both are capped; *Truncated flags drops.
	Requests          []NetRequest
	RequestsTruncated bool
	Console           []ConsoleMessage
	ConsoleTruncated  bool

	// Timing: where the render's wall clock went (setup / script execution /
	// settle poll). Debug-observability only — never surfaced in tool output.
	SetupDur  time.Duration
	ExecDur   time.Duration
	SettleDur time.Duration
	TotalDur  time.Duration
}

// NetRequest is one subrequest the page's JavaScript made (the requests tool).
type NetRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"` // 0 when the request errored before a response
	Failed bool   `json:"failed,omitempty"`
	Err    string `json:"error,omitempty"`
}

// ConsoleMessage is one captured page console.* call (the console tool).
type ConsoleMessage struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// PageMeta holds page-level metadata and extracted structure, populated by the
// dom.Extract pass. It is never marshaled directly — the browser layer groups
// the scalar metadata into a token-compact struct for tool output.
type PageMeta struct {
	Title       string
	Byline      string
	Description string
	SiteName    string
	Lang        string
	Canonical   string

	// Extended head metadata (surfaced grouped, all best-effort).
	Image       string // og:image / twitter:image, resolved absolute
	Author      string // author / article:author
	Published   string // article:published_time (verbatim ISO)
	Modified    string // article:modified_time (verbatim ISO)
	Favicon     string // link[rel~=icon], resolved absolute
	ThemeColor  string // theme-color
	TwitterCard string // twitter:card
	TwitterSite string // twitter:site

	Links       []Link
	Forms       []Form
	Images      []Image
	Headings    []Heading
	Controls    []Control    // non-anchor interactive controls (interact targets)
	Regions     []Region     // semantic landmark regions (banner/nav/main/…)
	Collections []Collection // auto-detected repeating record-sets (browse → extract schemas)
}

// Link is an anchor with its href resolved to an absolute URL.
type Link struct {
	Text     string `json:"text"`
	Href     string `json:"href"`
	Rel      string `json:"rel,omitempty"`
	Internal bool   `json:"internal"` // same registrable domain as the page
}

// Image is an image reference kept as text (alt/src) for the agent.
type Image struct {
	Alt string `json:"alt,omitempty"`
	Src string `json:"src"`
}

// Form is an HTML form and its fields.
type Form struct {
	ID      string  `json:"id,omitempty"`
	Name    string  `json:"name,omitempty"`
	Action  string  `json:"action"`
	Method  string  `json:"method"`
	Enctype string  `json:"enctype,omitempty"` // e.g. multipart/form-data; "" = urlencoded
	Fields  []Field `json:"fields,omitempty"`
}

// Field is a single form control.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Value       string   `json:"value,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	Checked     string   `json:"checked,omitempty"` // "true"|"false" for checkbox/radio
	Invalid     bool     `json:"invalid,omitempty"`
	Options     []string `json:"options,omitempty"`
	ID          string   `json:"id,omitempty"` // stable content-hash reference (ADR 0008)
}

// Control is a non-anchor interactive element an agent can drive via the interact
// tool (a button, role=button, onclick/tabindex element, submit/reset input, tab,
// or summary). Selector is a stable CSS selector that re-resolves the element; ID
// is a mutation-resilient reference key (a cacheable handle), not an interact
// target — the selector remains authoritative (ADR 0008).
//
// The state fields mirror the WAI-ARIA states an agent needs to reason about a
// control. checked/expanded/pressed/selected are tri/bi-state string enums
// ("true"/"false"/"mixed") because a "false" (a collapsed menu, an un-pressed
// toggle) is semantically meaningful; binary-only states are bools.
type Control struct {
	Text        string `json:"text,omitempty"` // accessible name (aria-label/labelledby, value, text…)
	Selector    string `json:"selector"`       // stable selector accepted by the interact tool
	ID          string `json:"id,omitempty"`   // stable content-hash reference key
	Kind        string `json:"kind"`           // button|submit|reset|tab|summary|role-button|interactive
	Role        string `json:"role,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Checked     string `json:"checked,omitempty"`  // aria-checked / native checked
	Expanded    string `json:"expanded,omitempty"` // aria-expanded
	Pressed     string `json:"pressed,omitempty"`  // aria-pressed (toggle buttons)
	Selected    string `json:"selected,omitempty"` // aria-selected
	Required    bool   `json:"required,omitempty"`
	Invalid     bool   `json:"invalid,omitempty"`
	Value       string `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Href        string `json:"href,omitempty"` // for anchor-as-control (role=button on <a>), absolute
}

// Heading is a section heading used to build a page outline.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	ID    string `json:"id,omitempty"` // authored DOM id, else a stable content-hash anchor
}

// Region is a semantic landmark (a page area with an ARIA landmark role), with a
// per-region interactive inventory. It gives an agent orientation — where the
// nav / main / footer are and how much is in each — without any spatial data,
// which unblink has no engine to compute.
type Region struct {
	ID       string `json:"id,omitempty"`
	Role     string `json:"role"` // banner|navigation|main|complementary|contentinfo|form|search|region
	Label    string `json:"label,omitempty"`
	Controls int    `json:"controls,omitempty"`
	Links    int    `json:"links,omitempty"`
	Forms    int    `json:"forms,omitempty"`
	Headings int    `json:"headings,omitempty"`
}

// SchemaField is one field of a detected collection's extraction schema. It maps
// 1:1 onto dom.FieldSpec: Selector is record-relative; Attr == "" means the
// element's collapsed text, else the named attribute value. The json tags mirror
// the extract tool's field shape so an agent can copy a field verbatim.
type SchemaField struct {
	Name     string `json:"name"`
	Selector string `json:"selector"`
	Attr     string `json:"attr,omitempty"`
}

// Collection is a detected repeating record-set — a run of same-shape sibling
// containers (product cards, search results, table-like rows) with a ready-to-use
// extraction schema. It is schema-only (no sample values, to keep browse terse):
// hand Root plus each field's {selector, attr} straight to the extract tool.
type Collection struct {
	Root   string        `json:"root"`             // selector matching the repeated containers
	Count  int           `json:"count"`            // containers the root resolves to, doc-wide
	Region string        `json:"region,omitempty"` // enclosing landmark role (main/navigation/…)
	Fields []SchemaField `json:"fields,omitempty"`
}

// Article is the reduced, readable content extracted from a Page.
type Article struct {
	Title       string
	Byline      string
	Excerpt     string
	SiteName    string
	ContentHTML string     // reduced + sanitized HTML
	ContentNode *html.Node // the cleaned content subtree (clone, safe to mutate)
	TextLength  int
	// Source records which reduction actually produced the content: "readability"
	// (a distinct article body was extracted) or "full" (the whole reduced page —
	// either requested, or the fallback when readability found no article). Lets
	// callers report an article→full fallback instead of silently mislabeling it.
	Source string
}

// Table is an extracted HTML data table. Computed on demand by dom.Tables (via
// the data tool), never as part of the per-read Extract pass. Truncated reports
// that rows were dropped by the extraction cap — never truncate silently.
type Table struct {
	Caption   string     `json:"caption,omitempty"`
	Headers   []string   `json:"headers,omitempty"`
	Rows      [][]string `json:"rows,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// MicrodataItem is one schema.org microdata item (an itemscope subtree). Each
// property value is a string, or a nested *MicrodataItem when the property is
// itself an itemscope. Computed on demand by dom.Microdata (via the data tool).
type MicrodataItem struct {
	Type       string           `json:"type,omitempty"` // itemtype
	ID         string           `json:"id,omitempty"`   // itemid
	Properties map[string][]any `json:"properties,omitempty"`
}
