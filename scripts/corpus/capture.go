package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxBody     = 32 << 20        // per-response body cap
	minDwell    = 3 * time.Second // always watch at least this long
	quietWindow = 2 * time.Second
	hardCap     = 30 * time.Second // give up waiting for quiet after this
)

// settleRule names the settle rule above in every oracle, so a later change to
// it leaves old captures identifiable instead of silently mixed in.
const settleRule = "load+2s-network-silence,min3s,cap30s"

// omitTypes are resource types unblink's engine never requests (no layout, no
// media): their bodies are dropped to keep captures small. Headers and status
// are still recorded so a page-JS fetch of one is answered, just bodiless.
var omitTypes = map[string]bool{"Image": true, "Media": true, "Font": true}

// oracleJS runs in the settled page and returns what a user would see. Text is
// innerText (CSS-aware: hidden content is excluded), which is the whole point of
// using a real browser as the oracle.
const oracleJS = `(() => {
  const vis = el => {
    if (!el.getClientRects().length) return false;
    const s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none';
  };
  const txt = el => (el && el.innerText) || '';
  const longest = sel => {
    let best = null, n = 0;
    for (const el of document.querySelectorAll(sel)) {
      const l = txt(el).length;
      if (l > n) { best = el; n = l; }
    }
    return best;
  };
  let sel = 'main', main = longest('main, [role=main]');
  if (!main) { sel = 'article'; main = longest('article'); }
  if (!main) sel = '';
  let shadow = 0, custom = 0, nodes = 0;
  for (const el of document.getElementsByTagName('*')) {
    nodes++;
    if (el.shadowRoot) shadow++;
    if (el.localName.includes('-')) custom++;
  }
  // A body with no layout box (or no body at all) makes innerText fall back to
  // textContent, hidden text included: record it so replay refuses the oracle.
  const body = document.body;
  return JSON.stringify({
    final_url: location.href,
    title: document.title,
    body_rendered: !!body && body.getClientRects().length > 0,
    body_text: txt(body),
    main_text: txt(main),
    main_selector: sel,
    links_total: document.querySelectorAll('a[href]').length,
    links_visible: [...document.querySelectorAll('a[href]')].filter(vis).length,
    forms: document.forms.length,
    controls_visible: [...document.querySelectorAll(
      'input:not([type=hidden]), select, textarea, button')].filter(vis).length,
    dom_nodes: nodes, shadow_roots: shadow, custom_elements: custom,
    charset: document.characterSet,
    user_agent: navigator.userAgent,
  });
})()`

type reqState struct {
	method, url, typ string
	reqBody          string // SHA-256 of the request body, "" when none
	status           int
	statusText, mime string
	headers          map[string][]string
	hasResp          bool
}

// capturePage loads one URL in a fresh incognito context, records every
// exchange into dir, and writes the oracle. One navigation, no retries.
func capturePage(ctx context.Context, c *cdp, id, url, dir, chromium string) (*oracle, error) {
	if err := os.MkdirAll(filepath.Join(dir, "bodies"), 0o755); err != nil {
		return nil, err
	}
	var bc struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := c.call(ctx, "", "Target.createBrowserContext", map[string]any{}, &bc); err != nil {
		return nil, err
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.call(dctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": bc.BrowserContextID}, nil)
		cancel()
	}()
	var tg struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{
		"url": "about:blank", "browserContextId": bc.BrowserContextID, "width": 1280, "height": 720,
	}, &tg); err != nil {
		return nil, err
	}
	var at struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": tg.TargetID, "flatten": true}, &at); err != nil {
		return nil, err
	}
	sid := at.SessionID
	events := c.subscribe(sid)
	defer c.unsubscribe(sid)

	for _, cmd := range []struct {
		m string
		p any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Network.enable", map[string]any{"maxTotalBufferSize": 1 << 30, "maxResourceBufferSize": 64 << 20}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		// A service worker that installs and claims the page mid-load would answer
		// later requests itself, from a session this capture does not observe.
		// unblink has no service workers, so the faithful capture is the page
		// talking to the network directly.
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 720, "deviceScaleFactor": 1, "mobile": false}},
	} {
		if err := c.call(ctx, sid, cmd.m, cmd.p, nil); err != nil {
			return nil, err
		}
	}

	arc := &archive{Version: archiveVersion, ID: id, URL: url}
	reqs := map[string]*reqState{}
	inflight := map[string]bool{}
	var exceptions []string
	docStatus := 0
	navRID := "" // the navigation's own request: the first Document requested
	loaded := false

	finish := func(rid string, failed string) {
		st := reqs[rid]
		delete(inflight, rid)
		if st == nil {
			return
		}
		delete(reqs, rid)
		e := entry{Method: st.method, URL: st.url, Type: st.typ, ReqBody: st.reqBody}
		if !st.hasResp {
			if failed == "" {
				failed = "no response"
			}
			e.Failed = failed
			arc.Entries = append(arc.Entries, e)
			return
		}
		e.Status, e.StatusText, e.Headers, e.MIME = st.status, st.statusText, st.headers, st.mime
		// The page's status is the navigation's final response — not whichever
		// document (an ad iframe's, say) happened to finish first.
		if rid == navRID {
			docStatus = st.status
		}
		switch {
		case omitTypes[st.typ]:
			e.Omitted = "resource type " + st.typ
		case failed != "":
			e.Omitted = "load failed after headers: " + failed
		default:
			var body struct {
				Body          string `json:"body"`
				Base64Encoded bool   `json:"base64Encoded"`
			}
			bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.call(bctx, sid, "Network.getResponseBody", map[string]any{"requestId": rid}, &body)
			cancel()
			if err != nil {
				e.Omitted = "body unavailable: " + err.Error()
				break
			}
			b := []byte(body.Body)
			if body.Base64Encoded {
				if b, err = base64.StdEncoding.DecodeString(body.Body); err != nil {
					e.Omitted = "body undecodable"
					break
				}
			} else {
				e.Text = true
			}
			if len(b) > maxBody {
				e.Omitted = fmt.Sprintf("over %d byte cap", maxBody)
				break
			}
			name, werr := writeBody(dir, b)
			if werr != nil {
				e.Omitted = "write failed: " + werr.Error()
				break
			}
			e.Body, e.Size = name, len(b)
		}
		arc.Entries = append(arc.Entries, e)
	}

	onEvent := func(ev event) {
		switch ev.Method {
		case "Network.requestWillBeSent":
			var p struct {
				RequestID string `json:"requestId"`
				Type      string `json:"type"`
				Request   struct {
					URL         string `json:"url"`
					Method      string `json:"method"`
					PostData    string `json:"postData"`
					HasPostData bool   `json:"hasPostData"`
				} `json:"request"`
				RedirectResponse *cdpResponse `json:"redirectResponse"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				return
			}
			if prev := reqs[p.RequestID]; prev != nil && p.RedirectResponse != nil {
				// A redirect hop: same requestId, new URL. Record the hop bodiless
				// so the replayed client follows the same chain.
				arc.Entries = append(arc.Entries, entry{
					Method: prev.method, URL: prev.url, Type: prev.typ, ReqBody: prev.reqBody,
					Status: p.RedirectResponse.Status, StatusText: p.RedirectResponse.StatusText,
					Headers: splitHeaders(p.RedirectResponse.Headers), MIME: p.RedirectResponse.MimeType,
				})
			}
			if !strings.HasPrefix(p.Request.URL, "http://") && !strings.HasPrefix(p.Request.URL, "https://") {
				delete(reqs, p.RequestID)
				return
			}
			typ := p.Type
			if prev := reqs[p.RequestID]; prev != nil && typ == "" {
				typ = prev.typ
			}
			if navRID == "" && typ == "Document" {
				navRID = p.RequestID
			}
			// The request body's hash lets replay tell apart repeated POSTs to one
			// URL (search and GraphQL endpoints). CDP inlines small bodies and
			// hands over large ones on request.
			post := p.Request.PostData
			if post == "" && p.Request.HasPostData {
				var pd struct {
					PostData string `json:"postData"`
				}
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if c.call(pctx, sid, "Network.getRequestPostData", map[string]any{"requestId": p.RequestID}, &pd) == nil {
					post = pd.PostData
				}
				cancel()
			}
			st := &reqState{method: p.Request.Method, url: stripFragment(p.Request.URL), typ: typ}
			if post != "" {
				sum := sha256.Sum256([]byte(post))
				st.reqBody = hex.EncodeToString(sum[:])
			}
			reqs[p.RequestID] = st
			inflight[p.RequestID] = true
		case "Network.responseReceived":
			var p struct {
				RequestID string      `json:"requestId"`
				Type      string      `json:"type"`
				Response  cdpResponse `json:"response"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				return
			}
			if st := reqs[p.RequestID]; st != nil {
				st.hasResp = true
				st.status, st.statusText, st.mime = p.Response.Status, p.Response.StatusText, p.Response.MimeType
				st.headers = splitHeaders(p.Response.Headers)
				if p.Type != "" {
					st.typ = p.Type
				}
			}
		case "Network.loadingFinished":
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(ev.Params, &p) == nil {
				finish(p.RequestID, "")
			}
		case "Network.loadingFailed":
			var p struct {
				RequestID string `json:"requestId"`
				ErrorText string `json:"errorText"`
			}
			if json.Unmarshal(ev.Params, &p) == nil {
				if p.ErrorText == "" {
					p.ErrorText = "failed"
				}
				finish(p.RequestID, p.ErrorText)
			}
		case "Page.loadEventFired":
			loaded = true
		case "Runtime.exceptionThrown":
			var p struct {
				Details struct {
					Text      string `json:"text"`
					Exception struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			if json.Unmarshal(ev.Params, &p) == nil && len(exceptions) < 20 {
				msg := p.Details.Exception.Description
				if msg == "" {
					msg = p.Details.Text
				}
				if i := strings.IndexByte(msg, '\n'); i >= 0 {
					msg = msg[:i]
				}
				exceptions = append(exceptions, msg)
			}
		}
	}

	start := time.Now()
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.call(ctx, sid, "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
		return nil, err
	}
	if nav.ErrorText != "" {
		return nil, fmt.Errorf("navigate: %s", nav.ErrorText)
	}

	// Settle: load fired, the network silent (no Network event at all) for
	// quietWindow, and at least minDwell on the page — or hardCap, whichever
	// first. Silence is judged by events, not by the in-flight set: a long-poll
	// or an out-of-process iframe's document never reports completion here, and
	// would otherwise pin every such page to hardCap.
	settled := false
	var settledAfter time.Duration
	quietSince := time.Now()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case ev := <-events:
			onEvent(ev)
			if strings.HasPrefix(ev.Method, "Network.") {
				quietSince = time.Now()
			}
		case <-tick.C:
			el := time.Since(start)
			if loaded && time.Since(quietSince) >= quietWindow && el >= minDwell {
				settled, settledAfter = true, el
				break loop
			}
			if el >= hardCap {
				settledAfter = el
				break loop
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	var ev struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, sid, "Runtime.evaluate", map[string]any{"expression": oracleJS, "returnByValue": true}, &ev); err != nil {
		return nil, err
	}
	if ev.ExceptionDetails != nil {
		return nil, fmt.Errorf("oracle script: %s", ev.ExceptionDetails.Text)
	}
	o := &oracle{}
	if err := json.Unmarshal([]byte(ev.Result.Value), o); err != nil {
		return nil, fmt.Errorf("oracle decode: %w", err)
	}
	// Requests still in flight at the snapshot are recorded as such, so replay
	// reports them as capture-side gaps rather than replay misses.
	for rid := range inflight {
		if st := reqs[rid]; st != nil {
			arc.Entries = append(arc.Entries, entry{Method: st.method, URL: st.url, Type: st.typ, ReqBody: st.reqBody, Failed: "in flight at snapshot"})
		}
	}
	o.URL, o.DocStatus, o.Exceptions, o.Settled = url, docStatus, exceptions, settled
	o.Requests, o.Chromium, o.Viewport = len(arc.Entries), chromium, "1280x720@1x"
	o.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	o.SettleRule, o.SettledAfterMS = settleRule, settledAfter.Milliseconds()

	_ = c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": tg.TargetID}, nil)
	if err := writeJSON(filepath.Join(dir, "archive.json"), arc); err != nil {
		return nil, err
	}
	// oracle.json is written last: its presence marks a complete capture.
	return o, writeJSON(filepath.Join(dir, "oracle.json"), o)
}

type cdpResponse struct {
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	MimeType   string            `json:"mimeType"`
	Headers    map[string]string `json:"headers"`
}

// splitHeaders undoes CDP's folding of repeated headers into one "\n"-joined value.
func splitHeaders(h map[string]string) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = strings.Split(v, "\n")
	}
	return out
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}
