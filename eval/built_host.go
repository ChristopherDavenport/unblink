//go:build eval

package eval

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// builtRoot is where the framework fixtures built from source live, relative to
// the eval package directory (the test cwd). See testdata/frameworks/README.md
// and `make eval-fixtures`.
const builtRoot = "../testdata/frameworks/built"

// builtOpt adjusts how builtHost serves a build.
type builtOpt func(*builtCfg)

type builtCfg struct {
	fallback string                    // served for a path with no file: an SPA's history-mode index
	deny     func(urlPath string) bool // paths answered 404 even though the file exists
}

// withSPAFallback serves file (relative to the build directory) for any path that
// names no file — what a static host configured for a history-mode SPA does.
func withSPAFallback(file string) builtOpt {
	return func(c *builtCfg) { c.fallback = file }
}

// withDenied answers 404 for every path containing sub, as a missing or blocked
// chunk would on the live web.
func withDenied(sub string) builtOpt {
	return func(c *builtCfg) {
		prev := c.deny
		c.deny = func(p string) bool {
			return strings.Contains(p, sub) || (prev != nil && prev(p))
		}
	}
}

// platformHost serves the hand-written platform-behaviours page (eval/corpus/
// platform): not a build, just a page and one deferred script, served the same
// way as the built fixtures.
func platformHost() Host {
	return staticDirHost("corpus/platform", "/platform")
}

// builtHost serves one built fixture directory as a static site: real files at
// their built (hashed) paths, directory indexes, the content types a static host
// sends, and a 404 for everything else.
func builtHost(name string, opts ...builtOpt) Host {
	return func() (http.Handler, error) {
		cfg := builtCfg{}
		for _, o := range opts {
			o(&cfg)
		}
		return staticDirHandler(filepath.Join(builtRoot, name), "",
			"built fixture "+name+" (run `make eval-fixtures`?)", cfg)
	}
}

// staticDirHost serves dir under the URL prefix.
func staticDirHost(dir, prefix string) Host {
	return func() (http.Handler, error) {
		return staticDirHandler(dir, prefix, dir, builtCfg{})
	}
}

// staticDirHandler serves every file under root at prefix + its relative path.
// The whole directory is read up front, so a missing fixture fails the case
// cleanly rather than at request time; what names it in that error.
func staticDirHandler(root, prefix, what string, cfg builtCfg) (http.Handler, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files[prefix+"/"+filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s is empty", what)
	}
	if cfg.fallback != "" {
		if _, ok := files["/"+cfg.fallback]; !ok {
			return nil, fmt.Errorf("%s has no fallback file %s", what, cfg.fallback)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		switch p {
		case "/robots.txt", "/llms.txt", "/llms-full.txt", "/favicon.ico":
			http.NotFound(w, r)
			return
		}
		if cfg.deny != nil && cfg.deny(p) {
			http.NotFound(w, r)
			return
		}
		for _, cand := range []string{p, path.Join(p, "index.html"), p + ".html"} {
			if b, ok := files[cand]; ok {
				w.Header().Set("Content-Type", builtContentType(cand))
				_, _ = w.Write(b)
				return
			}
		}
		if cfg.fallback != "" && path.Ext(p) == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(files["/"+cfg.fallback])
			return
		}
		http.NotFound(w, r)
	}), nil
}

// builtContentType is the Content-Type a static host sends for a built file.
func builtContentType(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".css":
		return "text/css; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
