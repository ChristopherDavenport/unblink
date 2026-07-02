package robots_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/robots"
)

// FuzzParse feeds arbitrary bytes to the robots.txt parser and exercises the
// matching path. robots.txt is fetched from every origin unblink touches, so
// the parser must never panic and matching must hold its "empty pattern never
// matches" contract regardless of input.
func FuzzParse(f *testing.F) {
	f.Add([]byte("User-agent: *\nDisallow: /private\nAllow: /private/public\nCrawl-delay: 2\nSitemap: https://e.example/s.xml\n"))
	f.Add([]byte("User-agent: a\nUser-agent: b\nDisallow: *.gif$\nDisallow: /*?\n"))
	f.Add([]byte("Disallow: /orphan-before-any-group\n\x00\xff\nUser-agent:\nCrawl-delay: -1e300\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		pol := robots.Parse(data)
		if pol == nil {
			t.Fatal("Parse returned nil policy")
		}
		if g := pol.StarGroup(); g != nil {
			_ = g.Allowed("/")
			_ = g.Allowed("/a/b.gif?q=1")
			_ = g.Allowed("")
		}
	})
}
