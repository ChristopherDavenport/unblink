package webext

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Archive decompression bounds — a defense against zip bombs, mirroring the byte-budget
// posture elsewhere in unblink (ADR 0009). uBlock's compiled rulesets are a few MB, so
// these are generous while still finite.
const (
	maxArchiveFileSize  = 128 << 20 // per uncompressed entry
	maxArchiveTotalSize = 512 << 20 // sum of uncompressed entries
)

// loadArchive opens a .zip/.xpi (raw zip) or .crx (zip behind a signed header) and
// builds a bundle from its interior.
func loadArchive(path string) (*Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("webext: %w", err)
	}
	if strings.EqualFold(filepath.Ext(path), ".crx") {
		data, err = stripCRX(data)
		if err != nil {
			return nil, err
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("webext: open archive %q: %w", path, err)
	}
	if err := guardZip(zr); err != nil {
		return nil, err
	}
	// *zip.Reader implements fs.FS and rejects path-traversing (non-fs.ValidPath) names
	// on Open, so resources are read through the same fs.FS path as an unpacked dir.
	return loadFS(zr)
}

// guardZip rejects an archive whose declared uncompressed size could exhaust memory.
func guardZip(zr *zip.Reader) error {
	var total uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > maxArchiveFileSize {
			return fmt.Errorf("webext: archive entry %q too large (possible zip bomb)", f.Name)
		}
		total += f.UncompressedSize64
		if total > maxArchiveTotalSize {
			return fmt.Errorf("webext: archive too large (possible zip bomb)")
		}
	}
	return nil
}

// stripCRX removes a Chrome CRX2/CRX3 signature header, returning the embedded zip.
func stripCRX(data []byte) ([]byte, error) {
	if len(data) < 16 || string(data[0:4]) != "Cr24" {
		return nil, fmt.Errorf("webext: not a CRX file (bad magic)")
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	switch version {
	case 2:
		pubLen := binary.LittleEndian.Uint32(data[8:12])
		sigLen := binary.LittleEndian.Uint32(data[12:16])
		off := 16 + uint64(pubLen) + uint64(sigLen)
		if off > uint64(len(data)) {
			return nil, fmt.Errorf("webext: corrupt CRX2 header")
		}
		return data[off:], nil
	case 3:
		headerLen := binary.LittleEndian.Uint32(data[8:12])
		off := 12 + uint64(headerLen)
		if off > uint64(len(data)) {
			return nil, fmt.Errorf("webext: corrupt CRX3 header")
		}
		return data[off:], nil
	default:
		return nil, fmt.Errorf("webext: unsupported CRX version %d", version)
	}
}
