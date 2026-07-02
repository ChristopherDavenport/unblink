package js_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// errTransport fails every request, for exercising the failed-subrequest counter.
type errTransport struct{}

func (errTransport) Do(context.Context, string, string, map[string]string, []byte) (*js.Response, error) {
	return nil, errors.New("boom")
}

// A page that settles cleanly reports no saturation: the budget was not hit, the
// DOM went quiet, nothing was left in flight — and the one fetch it made is counted.
func TestRenderCleanSettleReportsNoSaturation(t *testing.T) {
	env := js.Env{Transport: &delayedTransport{delay: 20 * time.Millisecond, body: "DATA"}}
	out, diag, _ := renderWaitDiag(t, `<html><body><div id="app"></div>
		<script>fetch('/api').then(function (r) { return r.text(); }).then(function (t) {
			document.getElementById('app').textContent = t;
		});</script></body></html>`, env, 2*time.Second)
	if !strings.Contains(out, "DATA") {
		t.Fatalf("fetched content missing:\n%s", out)
	}
	if diag.DeadlineHit || diag.DOMBusy || diag.NetPending != 0 {
		t.Errorf("clean settle should report no saturation, got %+v", diag)
	}
	if diag.NetRequests != 1 || diag.NetFailed != 0 {
		t.Errorf("want NetRequests=1 NetFailed=0, got %+v", diag)
	}
}

// A fetch that outlasts the budget is reported as still pending at the snapshot,
// with the deadline flagged — the caller can tell the page had not finished loading.
func TestRenderReportsPendingRequestAtDeadline(t *testing.T) {
	env := js.Env{Transport: &delayedTransport{delay: 5 * time.Second, body: "LATE"}}
	// DOM mutations hold the settle open (else it would settle before a delayed
	// fetch ever starts), then the fetch fires ~120ms in: its per-request timeout
	// (the render budget, counted from the call) outlives the settle deadline, so
	// the request is still genuinely in flight when the snapshot is taken.
	_, diag, _ := renderWaitDiag(t, `<html><body><div id="app"></div>
		<script>
		var n = 0;
		var iv = setInterval(function () {
			document.getElementById('app').textContent = 'tick' + (++n);
			if (n === 8) {
				clearInterval(iv);
				fetch('/api').then(function (r) { return r.text(); }).then(function (t) {
					document.getElementById('app').textContent = t;
				});
			}
		}, 15);</script></body></html>`, env, 300*time.Millisecond)
	if !diag.DeadlineHit {
		t.Errorf("want DeadlineHit with a fetch outlasting the budget, got %+v", diag)
	}
	if diag.NetPending != 1 {
		t.Errorf("want NetPending=1, got %+v", diag)
	}
	if diag.NetRequests != 1 {
		t.Errorf("want NetRequests=1, got %+v", diag)
	}
}

// A page whose DOM never goes quiet runs to the budget and is reported DOM-busy, so
// a hydration cut off mid-render is distinguishable from a page that finished.
func TestRenderReportsDOMBusyAtDeadline(t *testing.T) {
	_, diag, _ := renderWaitDiag(t, `<html><body><ul id="list"></ul>
		<script>setInterval(function () {
			var li = document.createElement('li'); li.textContent = 'x';
			document.getElementById('list').appendChild(li);
		}, 5);</script></body></html>`, js.Env{}, 300*time.Millisecond)
	if !diag.DeadlineHit || !diag.DOMBusy {
		t.Errorf("want DeadlineHit && DOMBusy for a persistently-mutating page, got %+v", diag)
	}
	if diag.NetPending != 0 {
		t.Errorf("want NetPending=0 (no network), got %+v", diag)
	}
}

// Subrequest errors — network failures and the browser's budget denials look the
// same here — are counted, not just dropped.
func TestRenderCountsFailedSubrequests(t *testing.T) {
	env := js.Env{Transport: errTransport{}}
	_, diag, _ := renderWaitDiag(t, `<html><body>
		<script>fetch('/a').catch(function () {}); fetch('/b').catch(function () {});</script>
		</body></html>`, env, 2*time.Second)
	if diag.NetRequests != 2 || diag.NetFailed != 2 {
		t.Errorf("want NetRequests=2 NetFailed=2, got %+v", diag)
	}
	if diag.NetPending != 0 {
		t.Errorf("failed requests are not pending, got %+v", diag)
	}
}
