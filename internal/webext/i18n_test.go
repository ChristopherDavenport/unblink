package webext_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func TestLocalesAndContentScripts(t *testing.T) {
	b, err := webext.Load("testdata/cosmetic-mini")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// i18n: plain message, placeholder+positional substitution, and __MSG__ expansion.
	if got := b.Locales.Get("extName"); got != "Cosmetic Mini" {
		t.Errorf("Get(extName) = %q, want Cosmetic Mini", got)
	}
	if got := b.Locales.GetSub("greeting", []string{"Bob"}); got != "Hello Bob" {
		t.Errorf("GetSub(greeting,[Bob]) = %q, want Hello Bob", got)
	}
	if got := b.Locales.Substitute("<<__MSG_extName__>>"); got != "<<Cosmetic Mini>>" {
		t.Errorf("Substitute = %q, want <<Cosmetic Mini>>", got)
	}
	// The manifest name is a __MSG__ reference; the raw parse keeps it literal.
	if b.Manifest.Name != "__MSG_extName__" {
		t.Errorf("manifest name = %q", b.Manifest.Name)
	}

	// Content-script selection honors matches / exclude_matches.
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"https://www.example.com/page", 1},
		{"https://example.com/", 1},
		{"https://noads.example.com/page", 0}, // excluded
		{"https://other.test/page", 0},        // unmatched
	} {
		got := len(b.ContentScriptsFor(mustURL(t, tc.url)))
		if got != tc.want {
			t.Errorf("ContentScriptsFor(%q) = %d scripts, want %d", tc.url, got, tc.want)
		}
	}

	// The matched content script names its resources.
	cs := b.ContentScriptsFor(mustURL(t, "https://www.example.com/x"))
	if len(cs) != 1 || len(cs[0].CSS) != 1 || len(cs[0].JS) != 1 || cs[0].RunAt != webext.RunAtIdle {
		t.Fatalf("unexpected content script: %+v", cs)
	}
	if _, err := b.ReadResource(cs[0].CSS[0]); err != nil {
		t.Errorf("ReadResource(%q): %v", cs[0].CSS[0], err)
	}
}
