package js_test

import (
	"strings"
	"testing"
)

func TestPushStateUpdatesLocation(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  history.pushState({page: 1}, '', '/items/42?tab=specs');
		  document.getElementById('out').textContent =
		    location.pathname + '|' + location.search + '|' + history.length;
		</script></body></html>`)
	if !strings.Contains(out, ">/items/42|?tab=specs|2<") {
		t.Errorf("pushState did not update location/history:\n%s", out)
	}
}

func TestBackFiresPopstate(t *testing.T) {
	// A router listens for popstate and reads location.pathname to pick a view.
	out := render(t, `<html><body><div id="out">start</div>
		<script>
		  var out = document.getElementById('out');
		  window.addEventListener('popstate', function (e) {
		    out.textContent = 'pop:' + location.pathname + ':' + (e.state ? e.state.n : 'none');
		  });
		  history.pushState({n: 1}, '', '/a');
		  history.pushState({n: 2}, '', '/b');
		  history.back();   // -> back to /a, state {n:1}
		</script></body></html>`)
	if !strings.Contains(out, ">pop:/a:1<") {
		t.Errorf("history.back did not fire popstate with restored location/state:\n%s", out)
	}
}
