package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tokenFixtures builds the token-pass corpus: pages where what the agent
// *reads* matters more than what the engine *does*. All three are static
// (no JS required), so engines with limited JS coverage aren't disadvantaged
// on the token metric — it isolates representation cost.
func tokenFixtures(root string) ([]fixture, error) {
	junky, err := os.ReadFile(filepath.Join(root, "eval", "corpus", "junky.input.html"))
	if err != nil {
		return nil, fmt.Errorf("token fixture: %w", err)
	}
	longread, err := os.ReadFile(filepath.Join(root, "eval", "corpus", "longread.input.html"))
	if err != nil {
		return nil, fmt.Errorf("token fixture: %w", err)
	}
	return []fixture{
		{
			name: "junky-portal", path: "/junky", html: string(junky),
			sentinel: "Edmund Hale", title: "The Lighthouse Keeper Who Counted the Tides",
			endSentinel: "2026 Coastal Review. All rights reserved",
			tokenOnly:   true,
		},
		{
			name: "longread", path: "/longread", html: string(longread),
			sentinel: "alpha-sentinel", title: "The Long Form of a Machine-Readable Web",
			endSentinel: "omega-sentinel",
			tokenOnly:   true,
		},
		{
			name: "noisy-portal", path: "/noisy", html: noisyPortal(),
			sentinel: noisySentinel, title: noisyTitle,
			endSentinel: "2026 Northgate Daily. All rights reserved",
			tokenOnly:   true,
		},
	}, nil
}

const (
	noisyTitle    = "Northgate Daily — Regional News Portal"
	noisySentinel = "forty-one minutes ahead of the published schedule"
)

// noisyPortal deterministically generates a nav-heavy news-portal page (~80 KB)
// around one short article — the page shape where semantic reduction and
// full-page dumps should diverge the most. Generated rather than committed so
// the corpus stays small and reproducible.
func noisyPortal() string {
	sections := []string{"Local", "Business", "Sport", "Culture", "Politics", "Science", "Health", "Travel", "Opinion", "Weather"}
	topics := []string{"council", "harbour", "transit", "schools", "housing", "energy", "rivers", "markets", "startups", "heritage", "festivals", "elections", "libraries", "parks", "airports", "farming", "fisheries", "museums", "theatre", "cycling"}

	var b strings.Builder
	b.Grow(96 * 1024)
	b.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>` + noisyTitle + `</title>
<meta name="description" content="News, sport, business and culture for the Northgate region.">
<style>.drawer{display:none}.modal{position:fixed}.ad{border:1px solid #ccc}</style>
<script>window.dataLayer=[];console.log('pageview');</script>
</head><body>
<div id="cookie-banner" class="modal" role="dialog">
<p>We value your privacy. We and our 941 partners store and access cookies on your device for personalised ads, audience measurement, and product development.</p>
<button>Accept all</button><button>Reject non-essential</button><button>Manage my choices</button>
</div>
`)

	// The mega-nav, twice: a desktop <nav> and an identical mobile drawer —
	// the duplicated link-dense pair real portals ship and unblink's full-mode
	// duplicate suppression exists for.
	nav := func(id string) {
		fmt.Fprintf(&b, `<nav id=%q aria-label="Site sections"><ul>`+"\n", id)
		for _, s := range sections {
			fmt.Fprintf(&b, `<li><a href="/%s">%s</a><ul>`, strings.ToLower(s), s)
			for _, t := range topics {
				fmt.Fprintf(&b, `<li><a href="/%s/%s">%s: latest %s coverage</a></li>`, strings.ToLower(s), t, s, t)
			}
			b.WriteString("</ul></li>\n")
		}
		b.WriteString("</ul></nav>\n")
	}
	b.WriteString("<header>\n")
	nav("main-nav")
	b.WriteString("</header>\n")
	b.WriteString(`<div class="drawer" id="mobile-drawer">` + "\n")
	nav("mobile-nav")
	b.WriteString("</div>\n")

	b.WriteString(`<aside class="ad"><p>ADVERTISEMENT — Northgate Motors mid-year clearance: zero-deposit finance on all models, this week only.</p></aside>
<main>
<article>
<h1>Harbour Bridge Reopens After Three-Year Rebuild</h1>
<p>The Northgate harbour bridge carried its first scheduled bus at dawn on
Tuesday, forty-one minutes ahead of the published schedule, ending a
three-year closure that split the city's two halves and rerouted eleven
thousand daily crossings through the valley tunnel.</p>
<p>Engineers replaced the entire deck and both approach spans while keeping
the original stone piers, a compromise that preserved the bridge's listed
status but doubled the projected cost. The council's final account puts the
rebuild at £64 million, against an initial estimate of £31 million.</p>
<p>For businesses on the north bank the reopening matters more than the
overrun. Footfall on Quay Street fell by more than half during the closure,
and traders say the tunnel diversion added forty minutes to deliveries that
used to take five.</p>
<p>The bridge reopens with a dedicated cycle lane in each direction and a
weight limit that keeps heavy freight on the ring road — both changes carried
over from the temporary arrangements the closure forced, and both, the
council argues, quietly the point of the rebuild.</p>
</article>
<div class="share"><p>Share:</p><a href="#">Twitter</a> <a href="#">Facebook</a> <a href="#">Email</a></div>
</main>
`)

	// Trending sidebar, related-story grid, and per-section story cards — the
	// bulk of a portal's non-article surface.
	b.WriteString(`<aside id="trending"><h2>Trending now</h2><ol>` + "\n")
	for i, t := range topics {
		fmt.Fprintf(&b, `<li><a href="/trending/%s">What the new %s report means for Northgate — a %d-minute read</a></li>`+"\n", t, t, 3+i%7)
	}
	b.WriteString("</ol></aside>\n")

	b.WriteString(`<section id="related"><h2>Related stories</h2><ul>` + "\n")
	for i := 0; i < 24; i++ {
		s, t := sections[i%len(sections)], topics[(i*3)%len(topics)]
		fmt.Fprintf(&b, `<li class="card"><a href="/%s/%s/story-%d">%s: %s plan clears committee stage after %d-hour session</a><span class="byline">Northgate Daily staff</span></li>`+"\n",
			strings.ToLower(s), t, i, s, t, 2+i%9)
	}
	b.WriteString("</ul></section>\n")

	for _, s := range sections[:6] {
		fmt.Fprintf(&b, `<section class="section-block"><h2>More from %s</h2><ul>`+"\n", s)
		for i, t := range topics[:12] {
			fmt.Fprintf(&b, `<li class="card"><a href="/%s/%s/more-%d">%s in focus: the %s question returns to the agenda</a><p class="standfirst">Our correspondents on why %s is back in the headlines this week.</p></li>`+"\n",
				strings.ToLower(s), t, i, s, t, t)
		}
		b.WriteString("</ul></section>\n")
	}

	b.WriteString(`<div id="newsletter-modal" class="modal"><h2>The Northgate briefing</h2><p>Join 62,000 readers. The morning's five essential stories, in your inbox by 7am.</p><form><input type="email" placeholder="you@example.com"><button>Sign up</button></form></div>` + "\n")

	// Footer sitemap: one column per section.
	b.WriteString("<footer><h2>Northgate Daily</h2>\n")
	for _, s := range sections {
		fmt.Fprintf(&b, `<div class="footer-col"><h3>%s</h3><ul>`, s)
		for _, t := range topics[:15] {
			fmt.Fprintf(&b, `<li><a href="/%s/%s/archive">%s %s archive</a></li>`, strings.ToLower(s), t, s, t)
		}
		b.WriteString("</ul></div>\n")
	}
	b.WriteString(`<p>&copy; 2026 Northgate Daily. All rights reserved. <a href="/terms">Terms</a> <a href="/privacy">Privacy</a> <a href="/cookies">Cookie choices</a></p></footer>
<script>console.log('consent-check');document.getElementById('newsletter-modal');</script>
</body></html>`)
	return b.String()
}
