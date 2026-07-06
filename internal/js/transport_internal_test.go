package js

import (
	"context"
	"strconv"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func TestResourceKind(t *testing.T) {
	tests := []struct {
		name    string
		reqType webext.ResourceType
		ctype   string
		url     string
		want    string
	}{
		// Initiator-decided.
		{"script by initiator", webext.TypeScript, "", "https://x/app.mjs", "js"},
		{"script mislabeled html stays js", webext.TypeScript, "text/html", "https://x/app.js", "js"},
		{"xhr json data call", webext.TypeXHR, "application/json", "https://x/api/build", "xhr"},
		{"xhr plain text", webext.TypeXHR, "text/plain; charset=utf-8", "https://x/api", "xhr"},
		{"xhr no content-type", webext.TypeXHR, "", "https://x/api", "xhr"},
		{"unknown initiator, no ctype", webext.TypeOther, "", "https://x/thing", "other"},
		// Content-type-decided (a fetch that pulls a subresource).
		{"fetch html", webext.TypeXHR, "text/html; charset=utf-8", "https://x/frag", "html"},
		{"fetch css", webext.TypeXHR, "text/css", "https://x/a.css", "css"},
		{"fetch png", webext.TypeXHR, "image/png", "https://x/a.png", "images"},
		{"fetch svg", webext.TypeXHR, "image/svg+xml", "https://x/a.svg", "images"},
		{"fetch woff2", webext.TypeXHR, "font/woff2", "https://x/a.woff2", "fonts"},
		{"fetch legacy font", webext.TypeXHR, "application/vnd.ms-fontobject", "https://x/a.eot", "fonts"},
		{"fetch mp4", webext.TypeXHR, "video/mp4", "https://x/a.mp4", "media"},
		{"fetch audio", webext.TypeXHR, "audio/mpeg", "https://x/a.mp3", "media"},
		{"fetch javascript ctype", webext.TypeXHR, "application/javascript", "https://x/a.js", "js"},
		// Scheme-decided.
		{"websocket ws", webext.TypeOther, "", "ws://x/live", "websocket"},
		{"websocket wss", webext.TypeXHR, "", "WSS://x/live", "websocket"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceKind(tc.reqType, tc.ctype, tc.url); got != tc.want {
				t.Errorf("resourceKind(%v, %q, %q) = %q, want %q", tc.reqType, tc.ctype, tc.url, got, tc.want)
			}
		})
	}
}

// kindTransport returns a response with the given content-type, echoing the URL
// so the ring-order test can assert which records survived.
type kindTransport struct{ ctype string }

func (k *kindTransport) Do(_ context.Context, _ string, _ string, _ map[string]string, _ []byte) (*Response, error) {
	return &Response{Status: 200, Headers: map[string]string{"content-type": k.ctype}}, nil
}

func TestCountingTransportRingKeepsTail(t *testing.T) {
	ct := &countingTransport{inner: &kindTransport{ctype: "application/json"}}
	// Fire one more than the cap so exactly the oldest record is evicted.
	total := maxReqRecords + 1
	for i := 0; i < total; i++ {
		if _, err := ct.Do(xhrCtx(context.Background()), "GET", "https://x/"+strconv.Itoa(i), nil, nil); err != nil {
			t.Fatalf("Do %d: %v", i, err)
		}
	}
	recs, truncated := ct.snapshotRecords()
	if !truncated {
		t.Fatal("want truncated=true after exceeding the cap")
	}
	if len(recs) != maxReqRecords {
		t.Fatalf("want %d records retained, got %d", maxReqRecords, len(recs))
	}
	// The oldest (request 0) must be gone; the newest must be last and in order.
	if want := "https://x/1"; recs[0].url != want {
		t.Errorf("oldest surviving record = %q, want %q (request 0 should be evicted)", recs[0].url, want)
	}
	if want := "https://x/" + strconv.Itoa(total-1); recs[len(recs)-1].url != want {
		t.Errorf("newest record = %q, want %q", recs[len(recs)-1].url, want)
	}
	// Classification flowed through: an xhr-initiated JSON call is "xhr".
	if recs[0].kind != "xhr" {
		t.Errorf("record kind = %q, want xhr", recs[0].kind)
	}
}

func TestCountingTransportBelowCapKeepsAllInOrder(t *testing.T) {
	ct := &countingTransport{inner: &kindTransport{ctype: "application/javascript"}}
	for i := 0; i < 3; i++ {
		if _, err := ct.Do(scriptCtx(context.Background()), "GET", "https://x/"+strconv.Itoa(i), nil, nil); err != nil {
			t.Fatalf("Do %d: %v", i, err)
		}
	}
	recs, truncated := ct.snapshotRecords()
	if truncated {
		t.Fatal("want truncated=false below the cap")
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	for i, r := range recs {
		if want := "https://x/" + strconv.Itoa(i); r.url != want {
			t.Errorf("record %d url = %q, want %q", i, r.url, want)
		}
		if r.kind != "js" {
			t.Errorf("record %d kind = %q, want js", i, r.kind)
		}
	}
}
