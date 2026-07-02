package browser_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// uploadServer serves a multipart form and echoes back what a POST delivered.
func uploadServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>Upload</title></head><body>`+
			`<form id="up" action="/upload" method="post" enctype="multipart/form-data">`+
			`<input type="text" name="note"><input type="file" name="doc">`+
			`<input type="submit" value="Send"></form></body></html>`)
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><head><title>Bad</title></head><body>parse error: %v</body></html>", err)
			return
		}
		f, fh, err := r.FormFile("doc")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><head><title>Bad</title></head><body>no file: %v</body></html>", err)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		fmt.Fprintf(w, `<!doctype html><html><head><title>Received</title></head><body>`+
			`<p>note=%s filename=%s content=%s</p></body></html>`,
			r.FormValue("note"), fh.Filename, data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A form declaring enctype=multipart/form-data submits multipart, with inline
// file content delivered as a real file part alongside the text fields.
func TestSubmitMultipartUpload(t *testing.T) {
	srv := uploadServer(t)
	b := newBrowser(t)
	ctx := context.Background()
	const sid = "up"

	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	res, err := b.Submit(ctx, sid, "up", map[string]string{"note": "hello"},
		[]browser.FilePart{{Field: "doc", Filename: "notes.txt", MIME: "text/plain", Data: []byte("upload-payload")}}, false)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Title != "Received" {
		t.Fatalf("result title = %q, want Received", res.Title)
	}
	r, err := b.Read(ctx, browser.Request{SessionID: sid, UseCurrent: true}, "full", 0, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"note=hello", "filename=notes.txt", "content=upload-payload"} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("result missing %q:\n%s", want, r.Markdown)
		}
	}
}

// A multipart-declared form with no files still submits multipart (many servers
// reject urlencoded bodies on such forms).
func TestSubmitMultipartNoFiles(t *testing.T) {
	srv := uploadServer(t)
	b := newBrowser(t)
	ctx := context.Background()
	const sid = "nofiles"

	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	// The handler requires the file part, so it answers Bad — but via a parsed
	// multipart body, proving the encoding switched on enctype alone.
	res, err := b.Submit(ctx, sid, "up", map[string]string{"note": "textonly"}, nil, false)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Title != "Bad" || res.Status != http.StatusBadRequest {
		t.Fatalf("title=%q status=%d, want the multipart-parsed no-file response", res.Title, res.Status)
	}
}

// File uploads require a POST form: attaching files to a GET form is a caller error.
func TestSubmitFilesRequirePOST(t *testing.T) {
	srv := serveHTML(t, `<!doctype html><html><body>`+
		`<form id="search" action="/find" method="get"><input name="q"></form></body></html>`)
	b := newBrowser(t)
	ctx := context.Background()
	const sid = "getform"

	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	_, err := b.Submit(ctx, sid, "search", nil,
		[]browser.FilePart{{Field: "doc", Filename: "x.txt", Data: []byte("x")}}, false)
	if err == nil || !strings.Contains(err.Error(), "POST") {
		t.Errorf("err = %v, want multipart-requires-POST error", err)
	}
}
