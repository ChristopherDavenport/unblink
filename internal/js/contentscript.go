package js

import "github.com/christopherdavenport/unblink/internal/webext"

// Content scripts: an extension's JS/CSS injected into pages matching its patterns.
// unblink runs them in the page's world (one JS global, with the chrome API added —
// the same-world simplification recorded in ADR 0010; true isolated worlds are a later
// phase). CSS is not applied as style (there is no CSSOM) — its element-hiding rules
// feed the cosmetic pass. Injection timing maps onto the render lifecycle:
// document_start before the page's own scripts, document_end after them (around
// DOMContentLoaded/load), document_idle after the load event.

// injectContentScriptCSS gathers element-hiding CSS from every content script matching
// the page into the cosmetic buffer. Called once, early, since CSS applies regardless
// of run_at.
func (b *bridge) injectContentScriptCSS() {
	if b.extHost == nil || b.base == nil {
		return
	}
	for _, bundle := range b.extHost.bundles {
		for _, cs := range bundle.ContentScriptsFor(b.base) {
			for _, cssPath := range cs.CSS {
				if data, err := bundle.ReadResource(cssPath); err == nil {
					b.addCosmeticCSS(bundle.Locales.Substitute(string(data)))
				}
			}
		}
	}
}

// injectContentScripts runs the JS of every content script matching the page at the
// given run_at phase, in bundle + manifest order. A script that fails to compile or
// throws is recorded and skipped (like a page script), never aborting the render.
func (b *bridge) injectContentScripts(runAt webext.RunAt) {
	if b.extHost == nil || b.base == nil {
		return
	}
	for _, bundle := range b.extHost.bundles {
		for _, cs := range bundle.ContentScriptsFor(b.base) {
			if cs.RunAt != runAt {
				continue
			}
			for _, jsPath := range cs.JS {
				data, err := bundle.ReadResource(jsPath)
				if err != nil {
					continue
				}
				b.compileAndRun("content-script:"+bundle.ID+"/"+jsPath, string(data))
			}
		}
	}
}
