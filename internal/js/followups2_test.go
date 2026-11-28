package js_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// document.forms (and any HTMLCollection) answers by id and name.
func TestHTMLCollectionNamedAccess(t *testing.T) {
	out := renderWithEnv(t, `<html><body><form name="login" id="lf"><input name="u"></form><form id="search"></form>
	<img name="logo" src="/l.png"><div id="out"></div><script>
	  var f = document.forms;
	  document.getElementById('out').textContent = [
	    f.login === f[0], f.lf === f[0], f.search === f[1], f.namedItem('login') === f[0], f.length,
	    Object.keys(f).join(''), document.images.logo === document.images[0], typeof f.nothere
	  ].join('/');
	</script></body></html>`, js.Env{})
	if !strings.Contains(out, ">true/true/true/true/2/01/true/undefined<") {
		t.Errorf("HTMLCollection named access:\n%s", out)
	}
}

// The SVG interface family exists for instanceof tests, elements are instances
// of their own interface, and nothing is a ReferenceError.
func TestSVGInterfaceFamily(t *testing.T) {
	out := renderWithEnv(t, `<html><body><a id="h" href="/x">html</a>
	<svg id="s"><a id="sa" href="/y"><path id="p" d="M0 0"/><circle id="c" r="1"/></a><g id="g"></g><text id="t">x</text></svg>
	<div id="out"></div><script>
	  var $ = function (id) { return document.getElementById(id); };
	  var r = [];
	  r.push($('sa') instanceof SVGAElement, $('h') instanceof SVGAElement, $('sa') instanceof SVGGraphicsElement, $('sa') instanceof SVGElement);
	  r.push($('p') instanceof SVGPathElement, $('c') instanceof SVGCircleElement, $('g') instanceof SVGGElement, $('t') instanceof SVGTextElement);
	  r.push(Object.prototype.toString.call($('p')), $('p').constructor.name, $('s') instanceof SVGSVGElement);
	  r.push(typeof SVGUseElement, typeof SVGLinearGradientElement, typeof SVGForeignObjectElement, typeof SVGAnimateElement, 'SVGMaskElement' in window);
	  r.push($('sa').getAttribute('href'), $('sa').classList.length);
	  $('out').textContent = r.join('/');
	</script></body></html>`, js.Env{})
	want := ">true/false/true/true/true/true/true/true/[object SVGPathElement]/SVGPathElement/true/function/function/function/function/true//y/0<"
	if !strings.Contains(out, want) {
		t.Errorf("SVG interfaces:\n got %s\nwant %s", out[strings.Index(out, `id="out"`):], want)
	}
}

// SvelteKit's version check: a timer an hour out that fetches and re-arms
// itself. A browser never runs it during a page load; neither does a render.
func TestHourLongPollIsNeverPulledIn(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/v": "1"}}
	var diag js.RenderResult
	out := renderWithEnv(t, `<html><body><div id="out">quiet</div><script>
	  function p() { fetch('/v').finally(function () { setTimeout(p, 36e5); }); }
	  setTimeout(p, 36e5);
	</script></body></html>`, js.Env{Transport: tr, Diag: &diag})
	if tr.count != 0 {
		t.Errorf("the hour-long poll was fetched %d time(s) during the render, want 0", tr.count)
	}
	if !strings.Contains(out, ">quiet<") || diag.DeadlineHit || diag.SettleDur > 500_000_000 {
		t.Errorf("render did not settle promptly: deadline=%v settle=%v", diag.DeadlineHit, diag.SettleDur)
	}
}
