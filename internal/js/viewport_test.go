package js_test

import (
	"strings"
	"testing"
)

// TestViewportConstants proves the constant desktop environment (1280×720@1x)
// is visible where responsive code reads it. Element geometry stays zero (no
// layout engine) — only the viewport is a truthful constant.
func TestViewportConstants(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  document.getElementById('out').textContent = [
		    'iw=' + window.innerWidth,
		    'ih=' + window.innerHeight,
		    'dpr=' + window.devicePixelRatio,
		    'sw=' + screen.width,
		    'sh=' + screen.height,
		    'avail=' + screen.availWidth,
		    'vv=' + visualViewport.width + 'x' + visualViewport.height,
		    'scroll=' + window.scrollX + ',' + window.scrollY,
		    'orient=' + screen.orientation.type
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{"iw=1280", "ih=720", "dpr=1", "sw=1280", "sh=720", "avail=1280", "vv=1280x720", "scroll=0,0", "orient=landscape-primary"} {
		if !strings.Contains(out, want) {
			t.Errorf("viewport missing %q:\n%s", want, out)
		}
	}
}

// TestMatchMediaEvaluates drives the media-query mini-parser through the
// breakpoints, feature keywords, combinators, and failure modes pages actually
// use. Before Phase 21 matchMedia returned a blanket false, which sent every
// width-branching page down its narrowest code path.
func TestMatchMediaEvaluates(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var cases = [
		    // [query, expected]
		    ['(min-width: 768px)', true],
		    ['(min-width: 1280px)', true],
		    ['(min-width: 1281px)', false],
		    ['(max-width: 767px)', false],
		    ['(max-width: 1280px)', true],
		    ['(width: 1280px)', true],
		    ['(min-width: 40em)', true],       // 640px
		    ['(max-width: 40em)', false],
		    ['(min-height: 700px)', true],
		    ['(max-height: 500px)', false],
		    ['screen', true],
		    ['all', true],
		    ['print', false],
		    ['not print', true],
		    ['only screen', true],
		    ['not screen', false],
		    ['screen and (min-width: 600px)', true],
		    ['screen and (min-width: 600px) and (max-width: 2000px)', true],
		    ['(min-width: 9999px), (orientation: landscape)', true], // OR list
		    ['(min-width: 9999px), print', false],
		    ['(orientation: landscape)', true],
		    ['(orientation: portrait)', false],
		    ['(prefers-color-scheme: light)', true],
		    ['(prefers-color-scheme: dark)', false],
		    ['(prefers-reduced-motion: no-preference)', true],
		    ['(prefers-reduced-motion: reduce)', false],
		    ['(prefers-reduced-motion)', false],   // bare: "is it reduced?" — no
		    ['(hover: hover)', true],
		    ['(hover: none)', false],
		    ['(pointer: fine)', true],
		    ['(pointer: coarse)', false],
		    ['(min-resolution: 2dppx)', false],
		    ['(max-resolution: 1.5dppx)', true],
		    ['(resolution: 96dpi)', true],
		    ['(aspect-ratio: 16/9)', true],
		    ['(min-aspect-ratio: 4/3)', true],
		    ['(max-aspect-ratio: 4/3)', false],
		    ['(display-mode: browser)', true],
		    ['(display-mode: standalone)', false],
		    ['(unknown-feature: whatever)', false], // unknown -> not all
		    ['', true]
		  ];
		  var fails = [];
		  for (var i = 0; i < cases.length; i++) {
		    var mql = matchMedia(cases[i][0]);
		    if (mql.matches !== cases[i][1]) fails.push(cases[i][0] + '=>' + mql.matches);
		    if (mql.media !== String(cases[i][0])) fails.push(cases[i][0] + ':badmedia');
		  }
		  // listener surface must exist and never throw
		  var mm = matchMedia('(min-width: 600px)');
		  mm.addListener(function () {}); mm.addEventListener('change', function () {});
		  mm.removeListener(function () {}); mm.removeEventListener('change', function () {});
		  document.getElementById('out').textContent = fails.length === 0 ? 'MM-ALL-PASS' : 'FAIL: ' + fails.join(' | ');
		</script></body></html>`)
	if !strings.Contains(out, "MM-ALL-PASS") {
		t.Errorf("matchMedia table failed:\n%s", out)
	}
}
