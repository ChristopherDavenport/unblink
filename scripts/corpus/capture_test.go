package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests drive a real Chromium against a loopback page, so they skip when
// none is installed. Nothing here touches the network.

const syntheticPage = `<!doctype html><html><head><title>Synthetic</title></head><body>
<main>
<h1>Visible heading</h1>
<p>Visible paragraph.</p>
<div style="opacity:0"><span>Parked Author</span><span>&lt;p&gt;parked &amp;quot;raw&amp;quot; markup&lt;/p&gt;</span></div>
<p id="late">not yet</p>
<x-card><span slot="title">Slotted title</span>Light body text<span slot="unused">never assigned</span></x-card>
</main>
<script>
customElements.define('x-card', class extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({mode: 'open'}).innerHTML =
      '<style>.gone{display:none}</style><h2><slot name="title"></slot></h2>' +
      '<p>Shadow only text</p><p class="gone">Shadow hidden text</p><div><slot></slot></div>';
  }
});
console.error('page said', {code: 7});
setTimeout(function () { throw new Error('thrown later'); }, 50);
Promise.reject(new Error('rejected'));
var s = document.createElement('script'); s.src = '/slow.js'; document.head.appendChild(s);
</script>
</body></html>`

func captureSynthetic(t *testing.T, slow time.Duration) (*oracle, time.Time) {
	t.Helper()
	bin := resolveChrome(os.Getenv("CHROME"))
	if bin == "" {
		t.Skip("no Chromium found")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, syntheticPage)
		case "/slow.js":
			time.Sleep(slow)
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `document.getElementById('late').textContent = 'arrived late';`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	before := time.Now()
	o, err := captureOne(bin, "synthetic", srv.URL+"/", t.TempDir(), "test")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return o, before
}

func TestOracleDeviceClockAndConsole(t *testing.T) {
	o, before := captureSynthetic(t, 0)
	if o.ScreenW != 1280 || o.ScreenH != 720 || o.InnerW != 1280 || o.InnerH != 720 || o.DPR != 1 {
		t.Errorf("device = screen %dx%d inner %dx%d @%g, want 1280x720@1", o.ScreenW, o.ScreenH, o.InnerW, o.InnerH, o.DPR)
	}
	at := time.UnixMilli(o.CapturedAtUnixMs)
	if at.Before(before.Add(-time.Second)) || at.After(time.Now()) {
		t.Errorf("captured_at_unix_ms = %v, want between %v and now", at, before)
	}
	joined := strings.Join(o.ConsoleErrors, "\n")
	for _, want := range []string{"console.error: page said", "exception: Error: thrown later", "rejected"} {
		if !strings.Contains(joined, want) {
			t.Errorf("console_errors lacks %q:\n%s", want, joined)
		}
	}
	if len(o.Exceptions) == 0 {
		t.Error("exceptions (the original field) is empty")
	}
}

// A fully transparent subtree is in innerText but never on screen: the oracle
// must not carry it (Mastodon's parked rows hold the post's raw HTML as text).
func TestOracleLeavesOutTransparentText(t *testing.T) {
	o, _ := captureSynthetic(t, 0)
	for _, text := range []string{o.MainText, o.BodyText} {
		if strings.Contains(text, "parked") || strings.Contains(text, "Parked Author") {
			t.Errorf("oracle text carries transparent content: %q", text)
		}
		if !strings.Contains(text, "Visible heading") || !strings.Contains(text, "Visible paragraph.") {
			t.Errorf("oracle text lost visible content: %q", text)
		}
	}
	if o.TransparentHidden != 1 {
		t.Errorf("transparent_hidden = %d, want 1", o.TransparentHidden)
	}
}

// A script still loading when the network first goes quiet is waited for, so its
// effect is in the oracle and nothing is reported in flight.
func TestSettleWaitsForInflightScript(t *testing.T) {
	o, _ := captureSynthetic(t, 5*time.Second)
	if !strings.Contains(o.MainText, "arrived late") {
		t.Errorf("oracle was taken before the slow script ran: %q", o.MainText)
	}
	if o.InflightAtSnapshot != 0 || o.HitCap || !o.Settled {
		t.Errorf("inflight=%d hit_cap=%v settled=%v, want 0/false/true", o.InflightAtSnapshot, o.HitCap, o.Settled)
	}
	if o.SettleRule != settleRule {
		t.Errorf("settle_rule = %q", o.SettleRule)
	}
}

// Text that exists only inside a shadow tree is on screen and must be in the
// oracle; innerText alone stops at the host.
func TestOracleReadsAcrossShadowRoots(t *testing.T) {
	o, _ := captureSynthetic(t, 0)
	if !o.ShadowComposed {
		t.Fatal("shadow_composed = false on a page with an open shadow root")
	}
	for _, text := range []string{o.MainText, o.BodyText} {
		for _, want := range []string{"Shadow only text", "Slotted title", "Light body text", "Visible heading", "arrived late"} {
			if !strings.Contains(text, want) {
				t.Errorf("oracle text lacks %q: %q", want, text)
			}
		}
		for _, not := range []string{"Shadow hidden text", "never assigned", "parked", "display:none"} {
			if strings.Contains(text, not) {
				t.Errorf("oracle text carries %q: %q", not, text)
			}
		}
		if strings.Count(text, "Slotted title") != 1 {
			t.Errorf("slotted text appears %d times: %q", strings.Count(text, "Slotted title"), text)
		}
	}
}
