package webext_test

import (
	"net/url"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(`{"manifest_version":3,"name":"x","content_scripts":[{"matches":["*://*/*"],"js":["a.js"]}]}`))
	f.Add([]byte(`{"manifest_version":2,"background":{"scripts":["bg.js"]},"web_accessible_resources":["*.png"]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = webext.ParseManifest(data)
	})
}

func FuzzParseRules(f *testing.F) {
	f.Add([]byte(`[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||ads.example.com^"}}]`))
	f.Add([]byte(`[{"id":2,"action":{"type":"redirect"},"condition":{"regexFilter":"a.*b"}}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rules, err := webext.ParseRules(data)
		if err != nil {
			return
		}
		m, err := webext.NewRuleMatcher(rules)
		if err != nil {
			return
		}
		u, err := url.Parse("https://ads.example.com/a.js?x=1")
		if err != nil {
			return
		}
		_ = m.Match(webext.Request{URL: u, Method: "get", Type: webext.TypeScript, Initiator: "news.test"})
	})
}

func FuzzMatchPattern(f *testing.F) {
	f.Add("*://*.example.com/*")
	f.Add("<all_urls>")
	f.Fuzz(func(t *testing.T, s string) {
		mp, err := webext.ParseMatchPattern(s)
		if err != nil {
			return
		}
		u, err := url.Parse("https://www.example.com/p?q=1")
		if err != nil {
			return
		}
		_ = mp.Matches(u)
	})
}
