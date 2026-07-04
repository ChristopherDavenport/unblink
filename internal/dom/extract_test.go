package dom_test

import (
	"bytes"
	"net/url"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

func loadStructured(t *testing.T) *page.Page {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/structured.input.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u, _ := url.Parse("https://example.com/blog/post")
	p := &page.Page{RequestURL: u, FinalURL: u, Doc: doc, Raw: raw}
	if err := dom.Extract(p); err != nil {
		t.Fatalf("extract: %v", err)
	}
	return p
}

func TestExtractMeta(t *testing.T) {
	p := loadStructured(t)
	if p.Meta.Title != "Structured Test Page" {
		t.Errorf("title = %q", p.Meta.Title)
	}
	if p.Meta.Description == "" {
		t.Error("description empty")
	}
	if p.Meta.SiteName != "Unblink Test Suite" {
		t.Errorf("site name = %q", p.Meta.SiteName)
	}
	if p.Meta.Lang != "en" {
		t.Errorf("lang = %q", p.Meta.Lang)
	}
	if p.Meta.Canonical != "https://example.com/blog/post" {
		t.Errorf("canonical = %q", p.Meta.Canonical)
	}
}

func TestExtractLinks(t *testing.T) {
	p := loadStructured(t)

	byHref := map[string]page.Link{}
	for _, l := range p.Meta.Links {
		byHref[l.Href] = l
	}

	// Relative link resolved to absolute against the page URL.
	about, ok := byHref["https://example.com/about"]
	if !ok {
		t.Fatalf("missing resolved /about link; got %v", p.Meta.Links)
	}
	if !about.Internal {
		t.Error("/about should be internal")
	}
	if about.Text != "About Us" {
		t.Errorf("about text = %q", about.Text)
	}

	ext, ok := byHref["https://external-site.example.org/docs"]
	if !ok {
		t.Fatal("missing external link")
	}
	if ext.Internal {
		t.Error("external link should not be internal")
	}
}

func TestExtractForms(t *testing.T) {
	p := loadStructured(t)
	if len(p.Meta.Forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(p.Meta.Forms))
	}
	f := p.Meta.Forms[0]
	if f.Method != "POST" {
		t.Errorf("method = %q, want POST", f.Method)
	}
	if f.Action != "https://example.com/subscribe" {
		t.Errorf("action = %q", f.Action)
	}

	byName := map[string]page.Field{}
	for _, fld := range f.Fields {
		byName[fld.Name] = fld
	}
	if fld := byName["email"]; fld.Type != "email" || !fld.Required {
		t.Errorf("email field = %+v", fld)
	}
	if fld := byName["plan"]; fld.Type != "select" || len(fld.Options) != 2 {
		t.Errorf("plan field = %+v", fld)
	}
	if fld := byName["comments"]; fld.Type != "textarea" {
		t.Errorf("comments field = %+v", fld)
	}
}

func TestExtractHeadings(t *testing.T) {
	p := loadStructured(t)
	got := p.Meta.Headings
	want := []page.Heading{
		{Level: 1, Text: "Structured Test Page"},
		{Level: 2, Text: "Installation"},
		{Level: 3, Text: "Requirements"},
		{Level: 2, Text: "Usage"},
	}
	if len(got) != len(want) {
		t.Fatalf("headings = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Level != w.Level || got[i].Text != w.Text {
			t.Errorf("heading[%d] = {%d %q}, want {%d %q}", i, got[i].Level, got[i].Text, w.Level, w.Text)
		}
	}
}

func TestFind(t *testing.T) {
	p := loadStructured(t)
	hits := dom.Find(p.Doc, "pomegranate", 5)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].HeadingPath != "Structured Test Page > Installation > Requirements" {
		t.Errorf("heading path = %q", hits[0].HeadingPath)
	}
}

// Lowercasing can change byte length (invalid UTF-8 folds to the 3-byte
// U+FFFD; some case mappings resize), so match offsets from the lowered text
// must be mapped back before slicing the original. Fuzz-found regression:
// searching text containing a bare \x8e byte used to panic.
func TestFindOffsetShift(t *testing.T) {
	p := extractInline(t, "<p>0b\x8e0A</p><p>ÉCLAIR pastry</p>")
	if hits := dom.Find(p.Doc, "A", 5); len(hits) == 0 {
		t.Error("match after an invalid byte not found")
	}
	hits := dom.Find(p.Doc, "éclair", 5)
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "ÉCLAIR pastry") {
		t.Errorf("non-ASCII match: %+v", hits)
	}
}

func TestImagesAbsolute(t *testing.T) {
	p := loadStructured(t)
	if len(p.Meta.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(p.Meta.Images))
	}
	if p.Meta.Images[0].Src != "https://example.com/images/diagram.png" {
		t.Errorf("img src = %q", p.Meta.Images[0].Src)
	}
}

func extractInline(t *testing.T, body string) *page.Page {
	t.Helper()
	raw := []byte("<!doctype html><html><body>" + body + "</body></html>")
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u, _ := url.Parse("https://example.com/")
	p := &page.Page{RequestURL: u, FinalURL: u, Doc: doc, Raw: raw}
	if err := dom.Extract(p); err != nil {
		t.Fatalf("extract: %v", err)
	}
	return p
}

func TestExtractControls(t *testing.T) {
	p := extractInline(t, `
		<button id="go" onclick="doThing()">Press me</button>
		<input type="submit" value="Send">
		<span role="button" aria-label="Close dialog">x</span>
		<a href="/elsewhere">a real link, not a control</a>`)

	bySel := map[string]page.Control{}
	for _, c := range p.Meta.Controls {
		if _, dup := bySel[c.Selector]; dup {
			t.Errorf("duplicate selector %q", c.Selector)
		}
		bySel[c.Selector] = c
	}

	// <button id onclick> matches both `button` and `[onclick]` but appears once.
	if len(p.Meta.Controls) != 3 {
		t.Fatalf("controls = %d, want 3 (no anchor, deduped button): %+v", len(p.Meta.Controls), p.Meta.Controls)
	}
	if c, ok := bySel["#go"]; !ok || c.Kind != "button" || c.Text != "Press me" {
		t.Errorf("button control = %+v (ok=%v)", c, ok)
	}
	// label falls back to the input value; kind is submit.
	var submit *page.Control
	for i := range p.Meta.Controls {
		if p.Meta.Controls[i].Kind == "submit" {
			submit = &p.Meta.Controls[i]
		}
	}
	if submit == nil || submit.Text != "Send" {
		t.Errorf("submit control = %+v", submit)
	}
	// role=button is captured with its aria-label as the text.
	var roleBtn *page.Control
	for i := range p.Meta.Controls {
		if p.Meta.Controls[i].Kind == "role-button" {
			roleBtn = &p.Meta.Controls[i]
		}
	}
	if roleBtn == nil || roleBtn.Text != "Close dialog" {
		t.Errorf("role=button control = %+v", roleBtn)
	}
}

func TestExtractControlState(t *testing.T) {
	p := extractInline(t, `
		<button aria-expanded="false">Menu</button>
		<div role="tab" tabindex="0" aria-selected="true">Tab One</div>
		<button aria-pressed="mixed">Bold</button>
		<span role="button" aria-label="Toggle" aria-checked="true" tabindex="0">x</span>
		<button aria-invalid="true" aria-required="true" disabled>Broken</button>`)

	byText := map[string]page.Control{}
	for _, c := range p.Meta.Controls {
		byText[c.Text] = c
		if c.ID == "" {
			t.Errorf("control %q missing id", c.Text)
		}
	}
	if c := byText["Menu"]; c.Expanded != "false" {
		t.Errorf("Menu expanded = %q, want false", c.Expanded)
	}
	if c := byText["Tab One"]; c.Selected != "true" || c.Kind != "tab" {
		t.Errorf("Tab One = %+v", c)
	}
	if c := byText["Bold"]; c.Pressed != "mixed" {
		t.Errorf("Bold pressed = %q, want mixed", c.Pressed)
	}
	if c := byText["Toggle"]; c.Checked != "true" || c.Kind != "role-button" {
		t.Errorf("Toggle = %+v", c)
	}
	if c := byText["Broken"]; !c.Invalid || !c.Required || !c.Disabled {
		t.Errorf("Broken = %+v", c)
	}
}

func TestExtractHashIDStability(t *testing.T) {
	id := func(p *page.Page, text string) string {
		for _, c := range p.Meta.Controls {
			if c.Text == text {
				return c.ID
			}
		}
		return ""
	}
	// Sibling reorder + unrelated class churn must not change the base id.
	a := extractInline(t, `<main><div class="x"><button>Alpha</button><button>Beta</button></div></main>`)
	b := extractInline(t, `<main><div class="y"><button>Beta</button><button>Alpha</button></div></main>`)
	if idA, idB := id(a, "Alpha"), id(b, "Alpha"); idA == "" || idA != idB {
		t.Errorf("Alpha id not stable across reorder/class change: %q vs %q", idA, idB)
	}
	// Renaming the accessible name changes the id.
	c := extractInline(t, `<main><div class="x"><button>Gamma</button><button>Beta</button></div></main>`)
	if id(c, "Gamma") == id(a, "Alpha") {
		t.Error("renamed control kept its id")
	}
}

func TestExtractMetadata(t *testing.T) {
	m := loadStructured(t).Meta
	for name, got := range map[string]string{
		"canonical":    m.Canonical,
		"image":        m.Image,
		"author":       m.Author,
		"published":    m.Published,
		"modified":     m.Modified,
		"twitter_card": m.TwitterCard,
		"twitter_site": m.TwitterSite,
		"theme_color":  m.ThemeColor,
		"favicon":      m.Favicon,
	} {
		if got == "" {
			t.Errorf("%s empty", name)
		}
	}
	if m.Canonical != "https://example.com/blog/post" {
		t.Errorf("canonical = %q", m.Canonical)
	}
	if m.Image != "https://example.com/images/cover.png" {
		t.Errorf("image not absolutized: %q", m.Image)
	}
	if m.Author != "Ada Lovelace" {
		t.Errorf("author = %q", m.Author)
	}
	if m.Favicon != "https://example.com/favicon.ico" {
		t.Errorf("favicon = %q", m.Favicon)
	}
}
