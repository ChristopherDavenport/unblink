package browser

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

func TestClassify(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00"
	webp := "RIFF\x00\x00\x00\x00WEBPVP8 "
	cases := []struct {
		name string
		ct   string
		body string
		want page.Kind
	}{
		{"html by type", "text/html; charset=utf-8", "<!doctype html><html></html>", page.KindHTML},
		{"xhtml", "application/xhtml+xml", "<html/>", page.KindHTML},
		{"pdf by type", "application/pdf", "%PDF-1.7\n...", page.KindPDF},
		{"pdf by magic mislabeled html", "text/html", "%PDF-1.5\nstuff", page.KindPDF},
		{"png by magic", "application/octet-stream", png, page.KindImage},
		{"png by type", "image/png", png, page.KindImage},
		{"jpeg by magic", "", "\xFF\xD8\xFF\xE0abc", page.KindImage},
		{"gif by magic", "", "GIF89a....", page.KindImage},
		{"webp by magic", "", webp, page.KindImage},
		{"svg is text", "image/svg+xml", "<svg xmlns='...'></svg>", page.KindText},
		{"json", "application/json", `{"a":1}`, page.KindJSON},
		{"json+suffix", "application/vnd.api+json", `{"data":[]}`, page.KindJSON},
		{"json feed", "application/json", `{"version":"https://jsonfeed.org/version/1.1","items":[]}`, page.KindFeed},
		{"rss by type", "application/rss+xml", "<?xml version='1.0'?><rss></rss>", page.KindFeed},
		{"atom by type", "application/atom+xml", "<feed></feed>", page.KindFeed},
		{"rss served as xml", "application/xml", "<?xml version='1.0'?><rss version='2.0'>", page.KindFeed},
		{"atom served as text/xml", "text/xml", "<?xml version='1.0'?><feed xmlns='...'>", page.KindFeed},
		{"plain xml not feed", "application/xml", "<?xml version='1.0'?><catalog></catalog>", page.KindText},
		{"plain text", "text/plain; charset=utf-8", "hello world", page.KindText},
		{"markdown", "text/markdown", "# Title", page.KindText},
		{"octet-stream text sniff", "application/octet-stream", "just some plain ascii text here", page.KindText},
		{"unknown binary", "application/octet-stream", "\x00\x01\x02\x03\xff\xfe", page.KindBinary},
		{"empty ct html sniff", "", "<!DOCTYPE html><html>", page.KindHTML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.ct, []byte(tc.body)); got != tc.want {
				t.Errorf("classify(%q, %q) = %q, want %q", tc.ct, tc.body, got, tc.want)
			}
		})
	}
}
