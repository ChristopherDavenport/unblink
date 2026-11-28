package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/md" && strings.HasPrefix(r.Header.Get("Accept"), "text/markdown") {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("Vary", "Accept")
			w.Header().Set("X-Markdown-Tokens", "3")
			fmt.Fprint(w, "# Title\n")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Title</h1>")
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/bodies", 0o755); err != nil {
		t.Fatal(err)
	}
	v := fetchMarkdown(context.Background(), srv.Client(), srv.URL+"/md", "ua", dir)
	if !v.Served || v.Size != 8 || v.Body == "" || v.Vary != "Accept" || v.TokensHeader != "3" {
		t.Errorf("markdown origin: %+v", v)
	}
	if b, err := os.ReadFile(dir + "/bodies/" + v.Body); err != nil || string(b) != "# Title\n" {
		t.Errorf("stored body = %q, %v", b, err)
	}
	v = fetchMarkdown(context.Background(), srv.Client(), srv.URL+"/html", "ua", dir)
	if v.Served || v.Body != "" || v.Status != 200 {
		t.Errorf("html origin: %+v", v)
	}
}
