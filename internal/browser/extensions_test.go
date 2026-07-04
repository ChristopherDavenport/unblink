package browser_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

const fixtureExt = "../webext/testdata/adblock-mini"

func TestNewWithExtensionLoads(t *testing.T) {
	b, err := browser.New(browser.WithJS(0), browser.WithExtension(fixtureExt))
	if err != nil {
		t.Fatalf("New with extension + JS: %v", err)
	}
	t.Cleanup(b.Close)
}

func TestNewWithExtensionRequiresJS(t *testing.T) {
	// Extensions attach only to the built-in JS engine; loading one under disabled JS
	// must fail loudly rather than be silently ignored.
	if _, err := browser.New(browser.WithExtension(fixtureExt)); err == nil {
		t.Fatal("expected an error: extension configured without the JS engine")
	}
}

func TestNewWithMissingExtensionErrors(t *testing.T) {
	if _, err := browser.New(browser.WithJS(0), browser.WithExtension(t.TempDir()+"/nope")); err == nil {
		t.Fatal("expected an error loading a missing extension")
	}
}
