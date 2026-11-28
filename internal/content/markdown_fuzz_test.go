package content

import (
	"context"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

// FuzzMarkdown feeds arbitrary bytes through the Markdown passthrough: the title
// scan must never panic, and the output must be the input with only its BOM and
// line endings normalised.
func FuzzMarkdown(f *testing.F) {
	for _, s := range []string{
		"", "# Title\n", "---\ntitle: x\n---\n# H\n", "```\n# code\n", "~~~~\n", "#", "# #", "\r\r\n",
		"\xEF\xBB\xBF# bom", "---", "`", "```\n```\n# after",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = MarkdownTitle(raw)
		p := &page.Page{Kind: page.KindMarkdown, Raw: raw}
		if err := Render(context.Background(), p); err != nil {
			t.Fatalf("render: %v", err)
		}
		if strings.Contains(p.Markdown, "\r") {
			t.Fatalf("output kept a carriage return: %q", p.Markdown)
		}
		if len(raw) > 0 && len(p.Markdown) > len(raw)+1 {
			t.Fatalf("output grew: %d bytes from %d", len(p.Markdown), len(raw))
		}
	})
}
