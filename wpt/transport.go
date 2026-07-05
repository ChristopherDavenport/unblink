//go:build wpt

package wpt

import (
	"context"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/christopherdavenport/unblink/internal/js"
)

// wptTransport is the js.Transport the engine uses to load a test's subresources:
// /resources/* and /common/* shared helpers, and the test's own files. It serves
// static bytes from the vendored corpus, overrides /resources/testharnessreport.js
// with unblink's DOM-writing reporter, and applies the single-origin subset of
// WPT's .sub substitution. Anything else 404s — a 404 on a required resource
// surfaces to the classifier rather than hanging.
type wptTransport struct {
	root string // testdata/wpt
}

func (t *wptTransport) Do(_ context.Context, method, rawURL string, _ map[string]string, _ []byte) (*js.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return notFound(rawURL), nil
	}
	p := path.Clean(u.Path)

	// Our reporter replaces upstream's cross-document one.
	if p == "/resources/testharnessreport.js" {
		return served(reporterJS, ".js", rawURL), nil
	}

	// Map the URL path to a vendored file, refusing traversal outside root.
	fsPath := filepath.Join(t.root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
	if rootRel, err := filepath.Rel(t.root, fsPath); err != nil || strings.HasPrefix(rootRel, "..") {
		return notFound(rawURL), nil
	}
	body, err := os.ReadFile(fsPath)
	if err != nil {
		return notFound(rawURL), nil
	}
	if strings.Contains(p, ".sub.") {
		body = applySub(body, u)
	}
	return served(body, path.Ext(p), rawURL), nil
}

// applySub performs the single-origin subset of WPT server-side substitution:
// {{host}}, {{location[...]}}, and {{GET[name]}} from the request query. Tokens we
// can't honor single-origin (e.g. {{hosts[alt][...]}}) are left intact, so a test
// that truly depends on a second origin fails visibly rather than silently passing.
func applySub(body []byte, u *url.URL) []byte {
	s := string(body)
	s = strings.ReplaceAll(s, "{{host}}", u.Hostname())
	s = strings.ReplaceAll(s, "{{location[host]}}", u.Host)
	s = strings.ReplaceAll(s, "{{location[hostname]}}", u.Hostname())
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			s = strings.ReplaceAll(s, "{{GET["+k+"]}}", vs[0])
		}
	}
	return []byte(s)
}

func served(body []byte, ext, finalURL string) *js.Response {
	return &js.Response{
		Status:   200,
		Headers:  map[string]string{"Content-Type": contentType(ext)},
		Body:     body,
		Raw:      body,
		FinalURL: finalURL,
	}
}

func notFound(finalURL string) *js.Response {
	return &js.Response{Status: 404, Headers: map[string]string{}, Body: nil, FinalURL: finalURL}
}

func contentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".any.js":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".html", ".htm", ".xhtml":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
