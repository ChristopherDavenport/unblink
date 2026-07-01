package js_test

import (
	"strings"
	"testing"
)

func TestGeometryStubs(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var el = document.getElementById('out');
		  var r = el.getBoundingClientRect();
		  var cs = getComputedStyle(el);
		  var ok = (r.width === 0) && (r.top === 0) && (el.offsetWidth === 0) &&
		           (el.clientHeight === 0) && (cs.display !== 'none') &&
		           (cs.getPropertyValue('color') === '');
		  el.textContent = ok ? 'PASS' : 'FAIL';
		</script></body></html>`)
	if !strings.Contains(out, ">PASS<") {
		t.Errorf("geometry/CSSOM stubs not satisfied:\n%s", out)
	}
}

func TestIntersectionObserverFires(t *testing.T) {
	// Content revealed only when an IntersectionObserver reports visibility must render.
	out := render(t, `<html><body><div id="lazy">placeholder</div>
		<script>
		  var io = new IntersectionObserver(function (entries) {
		    if (entries[0].isIntersecting) {
		      document.getElementById('lazy').textContent = 'revealed';
		    }
		  });
		  io.observe(document.getElementById('lazy'));
		</script></body></html>`)
	if !strings.Contains(out, ">revealed<") {
		t.Errorf("IntersectionObserver did not reveal content:\n%s", out)
	}
}
