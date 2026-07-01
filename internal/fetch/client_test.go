package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andybalholm/brotli"
)

// withInsecureTLS is a test-only option to accept httptest's self-signed cert on
// the utls path.
func withInsecureTLS() Option { return func(c *Client) { c.tlsInsecure = true } }

func TestBrotliDecode(t *testing.T) {
	const payload = "brotli body works <h1>ok</h1>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		bw := brotli.NewWriter(&buf)
		_, _ = bw.Write([]byte(payload))
		_ = bw.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c, _ := New()
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(p.Raw) != payload {
		t.Errorf("brotli body = %q, want %q", p.Raw, payload)
	}
}

func TestGzipDecode(t *testing.T) {
	const payload = "gzip body still works"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(payload))
		_ = gw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c, _ := New()
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(p.Raw) != payload {
		t.Errorf("gzip body = %q, want %q", p.Raw, payload)
	}
}

func TestRetryOnTransient(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt64(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c, _ := New(WithRetries(2))
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.StatusCode != http.StatusOK || string(p.Raw) != "recovered" {
		t.Errorf("retry result = %d %q", p.StatusCode, p.Raw)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("upstream requests = %d, want 3 (2 failures + success)", got)
	}
}

func TestBinaryBodyNotTranscoded(t *testing.T) {
	// A PNG signature followed by high bytes that a windows-1252→UTF-8 charset
	// transcode would mangle. The content-type gate must leave binary untouched.
	payload := append([]byte("\x89PNG\r\n\x1a\n"), 0xFF, 0xFE, 0x80, 0x81, 0x9F, 0xA0, 0x00, 0x01)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c, _ := New()
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(p.Raw, payload) {
		t.Errorf("binary body corrupted:\n got %x\nwant %x", p.Raw, payload)
	}
}

func TestMislabeledBinaryNotTranscoded(t *testing.T) {
	// Server lies and calls a PDF text/html; the magic-byte corroboration must
	// still treat it as binary and skip charset decoding.
	payload := append([]byte("%PDF-1.7\n"), 0x80, 0x81, 0x9F, 0xFF)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c, _ := New()
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(p.Raw, payload) {
		t.Errorf("mislabeled binary corrupted:\n got %x\nwant %x", p.Raw, payload)
	}
}

func TestMaxBytesCap(t *testing.T) {
	big := bytes.Repeat([]byte("A"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	c, _ := New(WithMaxBytes(1024))
	if _, err := c.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error for over-cap body, got nil")
	}

	// A body at or under the cap succeeds.
	c2, _ := New(WithMaxBytes(8192))
	p, err := c2.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("under-cap get: %v", err)
	}
	if len(p.Raw) != len(big) {
		t.Errorf("under-cap body len = %d, want %d", len(p.Raw), len(big))
	}
}

func TestCredentialsScopedToOrigin(t *testing.T) {
	var gotAuth, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Scoped to the server's origin: the bearer + custom header are injected.
	c, _ := New(WithBearer("tok123"), WithHeaders(map[string]string{"X-Api-Key": "k"}), WithCredentialScope(srv.URL))
	if _, err := c.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	if gotAuth != "Bearer tok123" || gotKey != "k" {
		t.Errorf("injected headers = %q / %q, want %q / %q", gotAuth, gotKey, "Bearer tok123", "k")
	}

	// Credentials scoped to a different origin are never sent here (fail-closed).
	gotAuth, gotKey = "", ""
	c2, _ := New(WithBearer("tok123"), WithCredentialScope("https://example.com"))
	if _, err := c2.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("get2: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("off-origin Authorization leaked = %q", gotAuth)
	}
}

func TestCredentialsStrippedOnCrossOriginRedirect(t *testing.T) {
	var downstreamAuth, downstreamKey string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamAuth, downstreamKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte("downstream"))
	}))
	defer downstream.Close()

	var originAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, downstream.URL+"/sink", http.StatusFound)
	}))
	defer origin.Close()

	c, _ := New(
		WithBearer("secret-token"),
		WithHeaders(map[string]string{"X-Api-Key": "secret-key"}),
		WithCredentialScope(origin.URL),
	)
	if _, err := c.Get(context.Background(), origin.URL+"/go"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if originAuth != "Bearer secret-token" {
		t.Errorf("origin did not receive the bearer: %q", originAuth)
	}
	// The redirect target is a different origin (different port): both the standard
	// Authorization header and the custom X-Api-Key must be dropped.
	if downstreamAuth != "" {
		t.Errorf("bearer leaked cross-origin on redirect: %q", downstreamAuth)
	}
	if downstreamKey != "" {
		t.Errorf("custom header leaked cross-origin on redirect: %q", downstreamKey)
	}
}

func TestTLSMimicHandshake(t *testing.T) {
	var gotProto atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto.Store(int32(r.ProtoMajor))
		_, _ = w.Write([]byte("tls-mimic ok"))
	}))
	srv.EnableHTTP2 = true // advertise h2 via ALPN so the utls hello negotiates it
	srv.StartTLS()
	defer srv.Close()

	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get over utls: %v", err)
	}
	if string(p.Raw) != "tls-mimic ok" {
		t.Errorf("utls body = %q", p.Raw)
	}
	// The server advertises h2 and the utls ClientHello offers it, so the tuned h2
	// path (not the h1 fallback) must be what served the request end-to-end.
	if got := gotProto.Load(); got != 2 {
		t.Errorf("negotiated HTTP/%d.x, want HTTP/2 over the utls path", got)
	}
}

func TestChromeMimicHeaders(t *testing.T) {
	newReq := func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		return r
	}

	// Plain path: keeps the minimal legacy header set — no Chrome client hints or
	// Sec-Fetch metadata, and the older Accept without image/avif.
	plain, _ := New()
	rp := newReq()
	plain.setHeaders(rp)
	if h := rp.Header.Get("sec-ch-ua"); h != "" {
		t.Errorf("plain path sent client hint sec-ch-ua = %q, want none", h)
	}
	if h := rp.Header.Get("Sec-Fetch-Mode"); h != "" {
		t.Errorf("plain path sent Sec-Fetch-Mode = %q, want none", h)
	}
	if h := rp.Header.Get("Accept"); strings.Contains(h, "image/avif") {
		t.Errorf("plain Accept = %q, want the legacy value", h)
	}

	// Mimic path: the full modern-Chrome persona.
	mimic, _ := New(WithTLSMimic(true))
	rm := newReq()
	mimic.setHeaders(rm)
	for _, h := range []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "Upgrade-Insecure-Requests",
		"Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User",
	} {
		if rm.Header.Get(h) == "" {
			t.Errorf("mimic path missing %s", h)
		}
	}
	if h := rm.Header.Get("Accept"); !strings.Contains(h, "image/avif") {
		t.Errorf("mimic Accept = %q, want the modern Chrome value", h)
	}
	// The UA and the sec-ch-ua hint must agree on the Chrome major (no contradiction).
	if ua := rm.Header.Get("User-Agent"); !strings.Contains(ua, "Chrome/"+chromeMajor) {
		t.Errorf("UA %q missing Chrome/%s", ua, chromeMajor)
	}
	if h := rm.Header.Get("sec-ch-ua"); !strings.Contains(h, chromeMajor) {
		t.Errorf("sec-ch-ua %q missing major %s", h, chromeMajor)
	}
	// Accept-Encoding stays fetch-owned (no zstd) even under --tls-mimic.
	if h := rm.Header.Get("Accept-Encoding"); h != "gzip, br" {
		t.Errorf("mimic Accept-Encoding = %q, want gzip, br", h)
	}
}
