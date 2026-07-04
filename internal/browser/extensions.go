package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// loadExtensions loads every configured WebExtension: each explicit --extension path,
// then every entry of every --extensions-dir. A load failure aborts startup so a
// misconfigured extension is loud, not silently ignored.
func loadExtensions(paths, dirs []string) ([]*webext.Bundle, error) {
	var out []*webext.Bundle
	for _, p := range paths {
		b, err := webext.Load(p)
		if err != nil {
			return nil, fmt.Errorf("browser: load extension %q: %w", p, err)
		}
		out = append(out, b)
	}
	for _, d := range dirs {
		bs, err := loadExtensionsDir(d)
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

// loadExtensionsDir loads every extension in dir: each subdirectory holding a
// manifest.json and each .zip/.xpi/.crx archive. Non-extension entries are skipped.
func loadExtensionsDir(dir string) ([]*webext.Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("browser: read extensions dir %q: %w", dir, err)
	}
	var out []*webext.Bundle
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "manifest.json")); err != nil {
				continue // not an unpacked extension
			}
		} else {
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".zip", ".xpi", ".crx":
			default:
				continue
			}
		}
		b, err := webext.Load(p)
		if err != nil {
			return nil, fmt.Errorf("browser: load extension %q: %w", p, err)
		}
		out = append(out, b)
	}
	return out, nil
}
