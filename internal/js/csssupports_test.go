package js_test

import "testing"

// CSS.supports is how pages tell engines apart. The engine presents as desktop
// Chrome, so it must not also claim Safari's and Firefox's vendor properties:
// saying yes to '-webkit-backdrop-filter' made a component library take its
// WebKit branch, render different markup on the client and fail React hydration
// (error #418) on Next.js App Router pages.
func TestCSSSupportsAnswersLikeChromeForVendorProbes(t *testing.T) {
	got := handlerOut(t, `<html><body><div id="out"></div><script>
		var q = [
			['-webkit-backdrop-filter:none'], ['-webkit-backdrop-filter', 'none'], ['-moz-appearance', 'none'],
			['(-webkit-touch-callout: none)'], ['-ms-ime-align', 'auto'], ['(-webkit-overflow-scrolling: touch)'],
			['display', 'grid'], ['(display: grid)'], ['-webkit-line-clamp', '2'], ['-webkit-appearance', 'none'],
			['(display: grid) and (-webkit-appearance: none)'], ['(display: grid) and (-moz-appearance: none)'],
			['selector(:has(a))'], ['backdrop-filter', 'none'], [''],
			['(grid-template-rows: subgrid)'], ['aspect-ratio: 1'], ['(color: hsl(from white h s l))'], ['grid-template-rows', 'subgrid']
		];
		document.getElementById('out').textContent = q.map(function (a) { return CSS.supports.apply(CSS, a) ? 1 : 0; }).join('');
	</script></body></html>`)
	if got != "0000001111101101111" {
		t.Errorf("got %s", got)
	}
}
