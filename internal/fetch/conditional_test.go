package fetch

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A conditional GET sends the validators and surfaces a 304 with no body; the
// plain Get never sends them.
func TestGetConditional(t *testing.T) {
	const etag = `"v1"`
	fullServes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fullServes++
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>fresh body</body></html>")
	}))
	defer srv.Close()

	c, _ := New()
	ctx := context.Background()

	p, err := c.Get(ctx, srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.StatusCode != http.StatusOK || p.Header.Get("Etag") != etag {
		t.Fatalf("initial fetch: status=%d etag=%q", p.StatusCode, p.Header.Get("Etag"))
	}

	np, err := c.GetConditional(ctx, srv.URL, etag, "")
	if err != nil {
		t.Fatalf("conditional get: %v", err)
	}
	if np.StatusCode != http.StatusNotModified {
		t.Errorf("conditional status = %d, want 304", np.StatusCode)
	}
	if len(np.Raw) != 0 {
		t.Errorf("304 should carry no body, got %d bytes", len(np.Raw))
	}
	if fullServes != 1 {
		t.Errorf("full body served %d times, want 1", fullServes)
	}
}

// SubmitMultipart posts a well-formed multipart/form-data body: fields and file
// parts arrive with their names, filename, declared MIME type, and content.
func TestSubmitMultipart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Errorf("content type = %q (%v)", r.Header.Get("Content-Type"), err)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		f, fh, err := r.FormFile("doc")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "user=%s file=%s mime=%s bytes=%s",
			r.FormValue("user"), fh.Filename, fh.Header.Get("Content-Type"), data)
	}))
	defer srv.Close()

	c, _ := New()
	p, err := c.SubmitMultipart(context.Background(), srv.URL, url.Values{"user": {"alice"}},
		[]FilePart{{Field: "doc", Filename: "notes.txt", MIME: "text/plain", Data: []byte("hello-upload")}})
	if err != nil {
		t.Fatalf("submit multipart: %v", err)
	}
	got := string(p.Raw)
	for _, want := range []string{"user=alice", "file=notes.txt", "mime=text/plain", "bytes=hello-upload"} {
		if !strings.Contains(got, want) {
			t.Errorf("response %q missing %q", got, want)
		}
	}
}

// A file part without a field name is a caller error, not a silent drop.
func TestSubmitMultipartRequiresField(t *testing.T) {
	c, _ := New()
	_, err := c.SubmitMultipart(context.Background(), "http://127.0.0.1:0/",
		nil, []FilePart{{Filename: "x.txt", Data: []byte("x")}})
	if err == nil || !strings.Contains(err.Error(), "field") {
		t.Errorf("err = %v, want missing-field error", err)
	}
}
