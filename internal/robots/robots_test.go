package robots_test

import (
	"os"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/robots"
)

func TestParseGroupsAndStarGroup(t *testing.T) {
	body := `# a comment
User-agent: BadBot
Disallow: /

User-agent: Googlebot
User-agent: *
Disallow: /private
Allow: /private/public
Crawl-delay: 10

Sitemap: https://example.com/sitemap.xml
Sitemap: https://example.com/news.xml
`
	p := robots.Parse([]byte(body))
	if len(p.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(p.Groups))
	}
	if len(p.Sitemaps) != 2 || p.Sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Fatalf("sitemaps = %v", p.Sitemaps)
	}

	star := p.StarGroup()
	if star == nil {
		t.Fatal("StarGroup() = nil, want the Googlebot/* group")
	}
	// The group lists both Googlebot and *, accumulated from consecutive lines.
	if len(star.Agents) != 2 {
		t.Errorf("star group agents = %v, want [googlebot *]", star.Agents)
	}
	if star.CrawlDelay != 10 {
		t.Errorf("crawl-delay = %v, want 10", star.CrawlDelay)
	}
}

func TestNoStarGroup(t *testing.T) {
	p := robots.Parse([]byte("User-agent: BadBot\nDisallow: /\n"))
	if g := p.StarGroup(); g != nil {
		t.Errorf("StarGroup() = %+v, want nil (only a named bot present)", g)
	}
	// A nil group allows everything.
	if !p.StarGroup().Allowed("/anything") {
		t.Error("nil star group should allow all paths")
	}
}

func TestAllowed(t *testing.T) {
	body := `User-agent: *
Disallow: /admin
Disallow: /search
Allow: /search/help
Disallow: /*.json$
Allow: /api/*/public
Disallow: /api
`
	g := robots.Parse([]byte(body)).StarGroup()
	if g == nil {
		t.Fatal("no star group")
	}

	cases := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/about", true},
		{"/admin", false},
		{"/admin/users", false},  // prefix match
		{"/search", false},       // disallowed
		{"/search/help", true},   // longer Allow wins over /search Disallow
		{"/data.json", false},    // $ anchor matches a .json suffix
		{"/data.json?x=1", true}, // $ anchor: not a suffix, so the .json rule misses
		{"/api", false},          // /api disallowed
		{"/api/v1/public", true}, // /api/*/public allowed, longer than /api
		{"/api/v1/private", false},
	}
	for _, c := range cases {
		if got := g.Allowed(c.path); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestAllowWinsTie(t *testing.T) {
	// Equal-length Allow and Disallow for the same prefix: Allow wins.
	g := robots.Parse([]byte("User-agent: *\nDisallow: /x\nAllow: /x\n")).StarGroup()
	if !g.Allowed("/x/y") {
		t.Error("Allow should win a specificity tie")
	}
}

func TestEmptyDisallowAllowsAll(t *testing.T) {
	g := robots.Parse([]byte("User-agent: *\nDisallow:\n")).StarGroup()
	if g == nil {
		t.Fatal("expected a star group")
	}
	if !g.Allowed("/anything/here") {
		t.Error("an empty Disallow should allow all paths")
	}
}

func TestDisallowRootBlocksAll(t *testing.T) {
	g := robots.Parse([]byte("User-agent: *\nDisallow: /\n")).StarGroup()
	for _, p := range []string{"/", "/a", "/a/b/c"} {
		if g.Allowed(p) {
			t.Errorf("Disallow: / should block %q", p)
		}
	}
}

func TestRobustness(t *testing.T) {
	// CRLF line endings, a BOM, comments, blank lines, and a malformed line.
	body := "\xEF\xBB\xBF# header\r\nUser-agent: *\r\nthis line has no colon\r\nDisallow: /tmp\r\n\r\nCrawl-delay: 2.5\r\n"
	p := robots.Parse([]byte(body))
	g := p.StarGroup()
	if g == nil {
		t.Fatal("BOM/CRLF parsing dropped the star group")
	}
	if g.Allowed("/tmp/x") {
		t.Error("Disallow: /tmp not honored after CRLF/BOM handling")
	}
	if g.CrawlDelay != 2.5 {
		t.Errorf("crawl-delay = %v, want 2.5", g.CrawlDelay)
	}
}

func TestSizeCap(t *testing.T) {
	// A rule pushed beyond the 512 KiB cap must be ignored.
	var b strings.Builder
	b.WriteString("User-agent: *\nDisallow: /early\n")
	b.WriteString("#")
	b.WriteString(strings.Repeat("x", 600*1024))
	b.WriteString("\nDisallow: /late\n")
	g := robots.Parse([]byte(b.String())).StarGroup()
	if g.Allowed("/early/x") {
		t.Error("/early rule (before the cap) should be honored")
	}
	if !g.Allowed("/late/x") {
		t.Error("/late rule (past the 512 KiB cap) should have been ignored")
	}
}

func TestParseNeverNil(t *testing.T) {
	if robots.Parse(nil) == nil || robots.Parse([]byte("garbage")) == nil {
		t.Fatal("Parse must never return nil")
	}
	if !robots.Parse([]byte("garbage")).StarGroup().Allowed("/x") {
		t.Error("an unparseable body should allow all paths")
	}
}

func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/example.robots.txt")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	p := robots.Parse(data)
	if len(p.Sitemaps) == 0 {
		t.Error("fixture should yield at least one sitemap")
	}
	if g := p.StarGroup(); g == nil {
		t.Error("fixture should yield a star group")
	}
}
