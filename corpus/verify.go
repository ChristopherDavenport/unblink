//go:build corpus

package corpus

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Manifest verification. The manifest's cell labels say which stack a site is
// believed to run; they were written by hand and sites get rebuilt. verifyCells
// checks each claimed cell against what the capture itself shows and reports
// the result. It only reports: the manifest is never rewritten.
const (
	verdictSupported    = "supported"    // the capture shows it
	verdictContradicted = "contradicted" // a signal that cannot be minified away says otherwise
	verdictNoEvidence   = "no-evidence"  // looked and found nothing, but the marker can be absent from a real site
	verdictUnchecked    = "unchecked"    // no offline test exists for this cell
)

type cellCheck struct {
	Cell     string `json:"cell"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence,omitempty"`
}

// evidence is what one capture shows about the page's stack.
type evidence struct {
	// stacks maps a detected stack name to the first marker that showed it.
	stacks map[string]string
	// own marks stacks shown by the page's own document or by a script served
	// from the page's own site. A third-party script (an ad, a widget, an
	// analytics bundle) brings its own stack along, which says nothing about how
	// the page is built.
	own map[string]bool
	// dataFetches counts completed non-analytics fetch/XHR exchanges.
	dataFetches int
	oracle      *oracle
}

// stackMarker is one detection rule: a literal looked for in the document
// HTML, in script bodies, or in script URLs.
type stackMarker struct {
	stack string
	in    int
	lit   string
}

const (
	inDoc = 1 << iota
	inScript
	inURL
)

// stackMarkers are strings a build of each stack carries that minification does
// not remove (property keys, attribute names, runtime globals, URL layouts).
var stackMarkers = []stackMarker{
	{"react", inScript, "react.dev/errors/"},
	{"react", inScript, "reactjs.org/docs/error-decoder"},
	{"react", inScript, "__reactFiber$"},
	{"react", inScript, "_reactRootContainer"},
	{"react", inDoc, "data-reactroot"},
	{"next", inURL | inDoc, "/_next/"},
	{"next", inDoc, "__NEXT_DATA__"},
	{"next-app-router", inDoc, "self.__next_f"},
	{"streaming", inDoc, "self.__next_f.push("},
	{"streaming", inDoc, `<template id="B:`},
	{"streaming", inDoc, "<!--$?-->"},
	{"streaming", inDoc, "$RC("},
	{"turbopack", inScript, "TURBOPACK"},
	{"vue", inScript, "__v_isRef"},
	{"vue", inScript, "__vue_app__"},
	{"vue", inDoc, "data-v-app"},
	{"vue-ssr", inDoc, "data-server-rendered"},
	{"nuxt", inDoc, "__NUXT__"},
	{"nuxt", inDoc, `id="__nuxt"`},
	{"nuxt", inURL | inDoc, "/_nuxt/"},
	{"svelte", inScript, "__svelte"},
	{"svelte", inDoc, "__sveltekit_"},
	{"svelte", inDoc, "data-sveltekit-"},
	{"solid", inDoc, "_$HY"},
	{"solid", inDoc, "data-hk="},
	{"solid", inScript, "_$HY"},
	{"solid", inScript, "solid-proxy"},
	{"angular", inDoc, "ng-version="},
	{"angular", inDoc, "_nghost-"},
	{"angular-ssr", inDoc, "ng-server-context"},
	{"angular-ssr", inDoc, " ngh="},
	{"preact", inURL, "preact"},
	{"preact", inScript, "__PREACT_DEVTOOLS__"},
	{"lit", inScript, "_$litPart$"},
	{"lit", inScript, "litElementVersions"},
	{"lit", inScript, "litHtmlVersions"},
	{"lit", inDoc, "<!--?lit$"},
	{"alpine", inDoc, "x-data"},
	{"alpine", inScript, "Alpine.start"},
	{"htmx", inDoc, " hx-get="},
	{"htmx", inDoc, " hx-post="},
	{"htmx", inDoc, " hx-boost="},
	{"htmx", inDoc, " hx-trigger="},
	{"htmx", inURL, "htmx"},
	{"htmx", inScript, "htmx:afterSwap"},
	{"jquery", inURL, "jquery"},
	{"jquery", inScript, "jQuery v"},
	{"jquery", inScript, "jquery.org/license"},
	{"elm", inScript, "_VirtualDom_"},
	{"elm", inScript, "$elm$"},
	{"elm", inScript, "Elm.Main"},
	{"elm", inDoc, "Elm.Main"},
	{"ember", inScript, "@ember/"},
	{"ember", inScript, "ember-source"},
	{"ember", inDoc, "ember-view"},
	{"ember", inDoc, "ember-cli"},
	{"ember-ssr", inDoc, "fastboot-body-start"},
	{"qwik", inDoc, "q:container"},
	{"astro", inURL | inDoc, "/_astro/"},
	{"astro", inDoc, "data-astro-"},
	{"astro-island", inDoc, "<astro-island"},
	{"vite", inScript, "__vite__mapDeps"},
	{"vite", inScript, "__vitePreload"},
	{"vite", inScript, "vite:preloadError"},
	{"vite", inDoc, "/@vite/"},
	{"webpack", inScript, "webpackChunk"},
	{"webpack", inScript, "__webpack_require__"},
	{"webpack", inScript, "webpackJsonp"},
	{"esbuild", inScript, "__toESM"},
	{"esbuild", inScript, "__commonJS"},
	{"lazy", inDoc, `loading="lazy"`},
	{"lazy", inDoc, "loading=lazy"},
	{"lazy", inScript, "IntersectionObserver"},
	{"noscript", inDoc, "<noscript"},
}

var svelteClassRE = regexp.MustCompile(`class="[^"]*\bsvelte-[a-z0-9]{5,}`)

// maxEvidenceBytes bounds how much script text one page's scan reads.
const maxEvidenceBytes = 256 << 20

// gatherEvidence scans a capture for stack markers. It reads the document and
// script bodies only, never executes anything, and is bounded.
func gatherEvidence(dir string, a *archive, o *oracle) *evidence {
	ev := &evidence{stacks: map[string]string{}, own: map[string]bool{}, oracle: o}
	site := siteOf(a.URL)
	firstParty := false
	note := func(stack, how string) {
		if _, ok := ev.stacks[stack]; !ok || (firstParty && !ev.own[stack]) {
			ev.stacks[stack] = how
		}
		if firstParty {
			ev.own[stack] = true
		}
	}
	var budget int64 = maxEvidenceBytes
	scanned := map[string]bool{}
	for _, e := range a.Entries {
		if e == nil || e.Failed != "" {
			continue
		}
		lowURL := strings.ToLower(e.URL)
		firstParty = siteOf(e.URL) == site
		isDoc := e.Type == "Document" && firstParty
		isScript := e.Type == "Script" || (e.Type != "Document" && scriptExtRE.MatchString(strings.SplitN(lowURL, "?", 2)[0]))
		if (e.Type == "Fetch" || e.Type == "XHR") && e.Body != "" && e.Status >= 200 && e.Status < 300 &&
			!isScript && !analyticsHostRE.MatchString(siteHost(e.URL)) {
			ev.dataFetches++
		}
		if isScript {
			for _, m := range stackMarkers {
				if m.in&inURL != 0 && strings.Contains(lowURL, strings.ToLower(m.lit)) {
					note(m.stack, "script URL contains "+m.lit)
				}
			}
		}
		if (!isDoc && !isScript) || e.Body == "" || scanned[e.Body] || budget <= 0 {
			continue
		}
		scanned[e.Body] = true
		b, err := e.body(dir)
		if err != nil {
			continue
		}
		budget -= int64(len(b))
		where, mask := "a third-party script", inScript
		if firstParty {
			where = "a first-party script"
		}
		if isDoc {
			where, mask = "the document", inDoc
			if svelteClassRE.Match(b) {
				note("svelte", "the document has svelte-<hash> classes")
			}
		}
		for _, m := range stackMarkers {
			if m.in&mask != 0 && bytes.Contains(b, []byte(m.lit)) {
				note(m.stack, where+" contains "+m.lit)
			}
		}
	}
	return ev
}

func siteHost(rawURL string) string {
	s := strings.ToLower(rawURL)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s, "]") {
		s = s[:i]
	}
	return s
}

// detected lists the stacks the capture shows, sorted.
func (ev *evidence) detected() []string {
	out := make([]string, 0, len(ev.stacks))
	for s := range ev.stacks {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// cellRule says how one manifest cell is checked. any lists stacks of which one
// must be present; reliable means their absence contradicts the label (the
// marker is structural — an attribute, a URL layout, a runtime global — and
// survives minification). ssr additionally requires the served HTML to carry
// the content.
type cellRule struct {
	any      []string
	reliable bool
	ssr      bool
}

var cellRules = map[string]cellRule{
	"framework:react":            {any: []string{"react", "next"}},
	"framework:vue":              {any: []string{"vue", "nuxt", "vue-ssr"}},
	"framework:svelte":           {any: []string{"svelte"}},
	"framework:solid":            {any: []string{"solid"}},
	"framework:angular":          {any: []string{"angular", "angular-ssr"}, reliable: true},
	"framework:preact":           {any: []string{"preact"}},
	"framework:lit":              {any: []string{"lit"}},
	"framework:alpine":           {any: []string{"alpine"}, reliable: true},
	"framework:htmx":             {any: []string{"htmx"}, reliable: true},
	"framework:jquery":           {any: []string{"jquery"}},
	"framework:elm":              {any: []string{"elm"}},
	"framework:ember":            {any: []string{"ember", "ember-ssr"}},
	"framework:qwik":             {any: []string{"qwik"}, reliable: true},
	"hydration:react-ssr":        {any: []string{"react", "next"}, ssr: true},
	"hydration:next":             {any: []string{"next"}, reliable: true, ssr: true},
	"hydration:streaming":        {any: []string{"streaming"}},
	"hydration:vue-ssr":          {any: []string{"vue", "nuxt", "vue-ssr"}, ssr: true},
	"hydration:nuxt":             {any: []string{"nuxt"}, reliable: true, ssr: true},
	"hydration:svelte-ssr":       {any: []string{"svelte"}, ssr: true},
	"hydration:solid-ssr":        {any: []string{"solid"}, ssr: true},
	"hydration:angular-ssr":      {any: []string{"angular", "angular-ssr"}, reliable: true, ssr: true},
	"hydration:preact-ssr":       {any: []string{"preact"}, ssr: true},
	"hydration:ember-ssr":        {any: []string{"ember", "ember-ssr"}, ssr: true},
	"hydration:astro-islands":    {any: []string{"astro-island"}, reliable: true},
	"hydration:resumable":        {any: []string{"qwik"}, reliable: true},
	"bundler:vite":               {any: []string{"vite"}},
	"bundler:webpack":            {any: []string{"webpack"}},
	"bundler:esbuild":            {any: []string{"esbuild"}},
	"arrival:lazy":               {any: []string{"lazy"}},
	"shape:noscript":             {any: []string{"noscript"}, reliable: true},
	"hydration:client-only":      {},
	"components:custom-elements": {},
	"components:shadow-dom":      {},
	"arrival:fetch-after-load":   {},
	"shape:non-utf8":             {},
	"shape:cjk":                  {},
	"shape:large-dom":            {},
	"shape:static-article":       {},
}

// SSR thresholds on static (render=false) recall of the oracle's main text.
const (
	ssrHas  = 0.50 // at or above: the served HTML carries the content
	ssrNone = 0.30 // below: the content only exists after JavaScript ran
)

// largeDOMNodes is the element count from which a page is "large" here — about
// where Lighthouse starts flagging DOM size.
const largeDOMNodes = 1500

// verifyCells checks every claimed cell. staticRecall is the page's
// render=false recall; haveStatic is false when the page could not be scored,
// in which case SSR-dependent checks say so instead of guessing.
func verifyCells(cells []string, ev *evidence, staticRecall float64, haveStatic bool) []cellCheck {
	out := make([]cellCheck, 0, len(cells))
	for _, c := range cells {
		out = append(out, verifyCell(c, ev, staticRecall, haveStatic))
	}
	return out
}

func verifyCell(cell string, ev *evidence, static float64, haveStatic bool) cellCheck {
	chk := cellCheck{Cell: cell}
	set := func(v, e string) cellCheck { chk.Verdict, chk.Evidence = v, e; return chk }
	o := ev.oracle
	ssrNote := func() (string, bool) {
		switch {
		case !haveStatic:
			return "served HTML not checked (page unscored)", true
		case static >= ssrHas:
			return fmt.Sprintf("served HTML carries the content (static recall %.2f)", static), true
		default:
			return fmt.Sprintf("served HTML lacks the content (static recall %.2f): not server-rendered", static), false
		}
	}

	switch cell {
	case "hydration:client-only":
		switch {
		case !haveStatic:
			return set(verdictUnchecked, "page unscored")
		case static < ssrNone:
			return set(verdictSupported, fmt.Sprintf("served HTML lacks the content (static recall %.2f)", static))
		case static >= passRecall:
			return set(verdictContradicted, fmt.Sprintf("served HTML already carries the content (static recall %.2f): server-rendered or static", static))
		}
		return set(verdictNoEvidence, fmt.Sprintf("served HTML carries part of the content (static recall %.2f)", static))
	case "components:custom-elements":
		if o.CustomElements > 0 {
			return set(verdictSupported, fmt.Sprintf("%d custom element(s) in Chromium's DOM", o.CustomElements))
		}
		return set(verdictContradicted, "no custom element in Chromium's DOM")
	case "components:shadow-dom":
		if o.ShadowRoots > 0 {
			return set(verdictSupported, fmt.Sprintf("%d open shadow root(s) in Chromium's DOM", o.ShadowRoots))
		}
		return set(verdictContradicted, "no open shadow root in Chromium's DOM (closed roots are invisible to the capture)")
	case "arrival:fetch-after-load":
		if ev.dataFetches > 0 {
			return set(verdictSupported, fmt.Sprintf("%d completed non-analytics fetch/XHR response(s)", ev.dataFetches))
		}
		return set(verdictContradicted, "no completed non-analytics fetch/XHR in the capture")
	case "shape:non-utf8":
		if cs := strings.ToLower(o.Charset); cs != "" && cs != "utf-8" {
			return set(verdictSupported, "document.characterSet is "+o.Charset)
		}
		return set(verdictContradicted, "document.characterSet is "+o.Charset)
	case "shape:cjk":
		n, cjk := 0, 0
		for _, r := range o.BodyText {
			if unicode.IsLetter(r) {
				n++
				if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
					cjk++
				}
			}
		}
		if n > 0 && cjk*100 >= n*30 {
			return set(verdictSupported, fmt.Sprintf("%d%% of visible letters are CJK", cjk*100/n))
		}
		return set(verdictContradicted, "under 30% of visible letters are CJK")
	case "shape:large-dom":
		if o.DOMNodes >= largeDOMNodes {
			return set(verdictSupported, fmt.Sprintf("%d elements", o.DOMNodes))
		}
		return set(verdictContradicted, fmt.Sprintf("%d elements (under %d)", o.DOMNodes, largeDOMNodes))
	case "shape:static-article":
		switch {
		case !haveStatic:
			return set(verdictUnchecked, "page unscored")
		case static >= passRecall:
			return set(verdictSupported, fmt.Sprintf("served HTML carries the content (static recall %.2f)", static))
		case static < ssrHas:
			return set(verdictContradicted, fmt.Sprintf("served HTML lacks the content (static recall %.2f)", static))
		}
		return set(verdictNoEvidence, fmt.Sprintf("static recall %.2f", static))
	}

	rule, ok := cellRules[cell]
	if !ok || len(rule.any) == 0 {
		return set(verdictUnchecked, "")
	}
	found := ""
	for _, s := range rule.any {
		if how, ok := ev.stacks[s]; ok {
			found = how
			break
		}
	}
	if found == "" {
		if rule.reliable {
			return set(verdictContradicted, "no "+strings.Join(rule.any, "/")+" marker anywhere in the capture")
		}
		return set(verdictNoEvidence, "no "+strings.Join(rule.any, "/")+" marker found (can be minified away)")
	}
	if rule.ssr {
		note, ok := ssrNote()
		if !ok {
			return set(verdictContradicted, found+"; but "+note)
		}
		return set(verdictSupported, found+"; "+note)
	}
	return set(verdictSupported, found)
}

// unclaimedStacks lists stacks the page's own document or first-party scripts
// show that no claimed cell accounts for — a hint that a label is missing. Helper detections (lazy, noscript, the *-ssr
// refinements) are not stacks in their own right and are left out.
func unclaimedStacks(cells []string, ev *evidence) []string {
	claimed := map[string]bool{}
	for _, c := range cells {
		for _, s := range cellRules[c].any {
			claimed[s] = true
		}
	}
	var out []string
	for _, s := range ev.detected() {
		switch s {
		case "lazy", "noscript", "vue-ssr", "angular-ssr", "ember-ssr", "streaming", "next-app-router", "astro":
			continue
		}
		if !claimed[s] && ev.own[s] {
			out = append(out, s)
		}
	}
	return out
}
