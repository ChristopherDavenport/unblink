package feed_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/content/feed"
)

// FuzzConvert hammers the feed converter with malformed input. A feed body is
// attacker-controlled bytes handed to a third-party parser (gofeed, over
// encoding/xml and a JSON decoder), so Convert's contract must hold for ANY
// input: either an error with empty results, or Markdown carrying the items
// section — never a panic.
func FuzzConvert(f *testing.F) {
	for _, name := range []string{"rss2.xml", "atom.xml", "jsonfeed.json", "empty_rss.xml", "truncated_rss.xml"} {
		f.Add(fixture(f, name))
	}
	f.Add([]byte(`<rss version="2.0"></rss>`))
	f.Add([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><title type="xhtml"><div>x</div></title></entry></feed>`))
	f.Add([]byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/"><channel><title>t</title></channel><item><title>i</title></item></rdf:RDF>`))
	f.Add([]byte(`{"version":"https://jsonfeed.org/version/1","items":[null]}`))
	f.Add([]byte(`<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>` + "\xe9" + `</title></channel></rss>`))
	f.Add([]byte("not a feed at all"))

	f.Fuzz(func(t *testing.T, data []byte) {
		md, title, err := feed.Convert(data, nil)
		if err != nil {
			if md != "" || title != "" {
				t.Fatalf("error %v returned alongside md=%q title=%q", err, md, title)
			}
			return
		}
		if !strings.Contains(md, "## Items (") {
			t.Fatalf("successful conversion lacks the items section:\n%q", md)
		}
	})
}
