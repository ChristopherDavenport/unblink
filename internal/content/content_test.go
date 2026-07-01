package content

import (
	"context"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

func TestRenderJSON(t *testing.T) {
	p := &page.Page{Kind: page.KindJSON, Raw: []byte(`{"name":"unblink","stars":42}`)}
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(p.Markdown, "```json") {
		t.Errorf("expected json fence, got:\n%s", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "\"name\": \"unblink\"") {
		t.Errorf("expected indented key, got:\n%s", p.Markdown)
	}
}

func TestRenderJSONInvalidFallsBackToText(t *testing.T) {
	p := &page.Page{Kind: page.KindJSON, Raw: []byte(`not json at all`)}
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(p.Markdown, "not json at all") {
		t.Errorf("expected raw text fallback, got:\n%s", p.Markdown)
	}
}

func TestRenderText(t *testing.T) {
	p := &page.Page{Kind: page.KindText, Raw: []byte("plain body\nsecond line")}
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(p.Markdown, "plain body") || !strings.HasPrefix(p.Markdown, "```") {
		t.Errorf("unexpected text render:\n%s", p.Markdown)
	}
}

func TestFencedBlockEscapesEmbeddedFence(t *testing.T) {
	// A body containing ``` must be wrapped in a longer fence.
	got := fencedBlock("", "a\n```\nb")
	if !strings.HasPrefix(got, "````") {
		t.Errorf("expected 4+ backtick fence, got:\n%s", got)
	}
}

const rssFixture = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Example Feed</title>
  <description>A test feed</description>
  <link>https://example.com</link>
  <item><title>First Post</title><link>https://example.com/1</link>
    <description>&lt;p&gt;Hello &amp;amp; welcome&lt;/p&gt;</description>
    <pubDate>Mon, 02 Jan 2006 15:04:05 MST</pubDate></item>
  <item><title>Second Post</title><link>https://example.com/2</link></item>
</channel></rss>`

func TestRenderFeed(t *testing.T) {
	p := &page.Page{Kind: page.KindFeed, Raw: []byte(rssFixture)}
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"# Example Feed", "First Post", "Second Post", "https://example.com/1"} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("feed markdown missing %q:\n%s", want, p.Markdown)
		}
	}
	// The feed title should upgrade Meta.Title, and HTML in the summary is stripped.
	if p.Meta.Title != "Example Feed" {
		t.Errorf("Meta.Title = %q, want Example Feed", p.Meta.Title)
	}
	if strings.Contains(p.Markdown, "<p>") {
		t.Errorf("expected HTML stripped from summary:\n%s", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "Hello & welcome") {
		t.Errorf("expected entity-decoded summary:\n%s", p.Markdown)
	}
}
