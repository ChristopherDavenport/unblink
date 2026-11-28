package js_test

import "testing"

// The viewport root (<html>/<body>) reports the constant 1280x720 viewport,
// agreeing with innerWidth/innerHeight; every other element stays zero.
func TestViewportRootGeometry(t *testing.T) {
	wantJS(t, `<div id="d">x</div>`, `
		var h = document.documentElement, b = document.body, d = document.getElementById('d');
		var r = h.getBoundingClientRect(), rb = b.getBoundingClientRect(), rd = d.getBoundingClientRect();
		return [r.top, r.left, r.right, r.bottom, r.width, r.height, rb.width, rb.height,
			rd.width, rd.height, h.clientWidth === innerWidth, h.clientHeight === innerHeight,
			d.clientHeight, h.getClientRects().length, d.getClientRects().length,
			document.scrollingElement === h, h.scrollHeight, d.offsetHeight].join(',');`,
		"0,0,1280,720,1280,720,1280,720,0,0,true,true,0,1,0,true,720,0")
}

// A scroll-spy that climbs ancestors until one has height (Docusaurus's
// table-of-contents highlighter) must stop at the root, not walk off into
// `document`, which has no getBoundingClientRect.
func TestAncestorRectWalkTerminatesAtRoot(t *testing.T) {
	wantJS(t, `<main><h2 id="a">A</h2></main>`, `
		function l(e){ var t = e.getBoundingClientRect(); return t.top === t.bottom ? l(e.parentNode) : t; }
		return l(document.getElementById('a')).height;`, "720")
}

// A detached <html>/<body> (DOMParser, createHTMLDocument) is not the viewport.
func TestDetachedRootHasNoBox(t *testing.T) {
	wantJS(t, ``, `
		var d = document.implementation.createHTMLDocument('x');
		var e = document.createElement('body');
		return d.body.getBoundingClientRect().height + ',' + d.documentElement.clientHeight + ',' + e.getBoundingClientRect().height;`,
		"0,0,0")
}
