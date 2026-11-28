package js_test

import (
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Env.ClockStart pins what the page reads as the time — Date and the
// performance origin — while timers, the timer clamp and the settle keep
// running on the real clock.
func TestClockStartPinsThePageClock(t *testing.T) {
	start := time.Date(2020, 2, 29, 12, 0, 0, 0, time.UTC)
	begin := time.Now()
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div><script>
	  var r = [];
	  var t0 = Date.now();
	  r.push('iso=' + new Date().toISOString().slice(0, 16));
	  r.push('origin=' + (Math.abs(performance.timeOrigin - t0) < 2000));
	  r.push('perfnow=' + (performance.now() < 2000));
	  r.push('navstart=' + (performance.timing.navigationStart === performance.timeOrigin));
	  setTimeout(function () {
	    r.push('short=' + (Date.now() - t0 >= 40 && Date.now() - t0 < 2000));
	    setTimeout(function () {
	      r.push('long=ran');
	      document.getElementById('out').textContent = r.join(' | ');
	    }, 7000);
	  }, 50);
	</script></body></html>`, js.Env{ClockStart: start}, 5*time.Second)
	for _, want := range []string{"iso=2020-02-29T12:00", "origin=true", "perfnow=true", "navstart=true", "short=true", "long=ran"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if el := time.Since(begin); el > 2*time.Second {
		t.Errorf("render took %v: the timer clamp or fast-forward lost track of the pinned clock", el)
	}
}

// Without ClockStart the page clock is the real one.
func TestClockDefaultsToRealTime(t *testing.T) {
	out := renderWithEnv(t, `<html><body><div id="out"></div><script>
	  document.getElementById('out').textContent = 'now=' + Date.now();
	</script></body></html>`, js.Env{})
	i := strings.Index(out, "now=")
	if i < 0 {
		t.Fatalf("no output: %s", out)
	}
	ms := int64(0)
	for _, c := range out[i+4:] {
		if c < '0' || c > '9' {
			break
		}
		ms = ms*10 + int64(c-'0')
	}
	if d := time.Since(time.UnixMilli(ms)); d < 0 || d > time.Minute {
		t.Errorf("page clock is %v from the real one", d)
	}
}
