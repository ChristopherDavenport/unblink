package webext

import (
	"fmt"
	"io/fs"
	"os"
)

// Load reads an extension from an unpacked directory (containing manifest.json) or a
// .zip / .xpi / .crx archive, returning a ready-to-use Bundle.
func Load(path string) (*Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("webext: %w", err)
	}
	if info.IsDir() {
		return loadFS(os.DirFS(path))
	}
	return loadArchive(path)
}

// loadFS parses manifest.json from a filesystem view and builds the bundle.
func loadFS(fsys fs.FS) (*Bundle, error) {
	data, err := fs.ReadFile(fsys, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("webext: read manifest.json: %w", err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	return buildBundle(m, fsys)
}
