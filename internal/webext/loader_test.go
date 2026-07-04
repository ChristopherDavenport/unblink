package webext_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func TestLoadDir(t *testing.T) {
	b, err := webext.Load("testdata/adblock-mini")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Manifest.Name != "Adblock Mini" || b.Manifest.ManifestVersion != 3 {
		t.Errorf("unexpected manifest: %+v", b.Manifest)
	}
	if got := b.Net.Count(); got != 3 {
		t.Fatalf("compiled rule count = %d, want 3", got)
	}
	if b.ID == "" {
		t.Error("expected a derived extension ID")
	}
	// The fixture's first rule blocks ||ads.example.com^ for scripts.
	d := b.Net.Match(req(t, "https://ads.example.com/a.js", webext.TypeScript, "news.test"))
	if !d.Block {
		t.Error("expected ads.example.com script to be blocked")
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var fixtureFiles = map[string]string{
	"manifest.json": `{"manifest_version":3,"name":"Zipped","version":"2.0",
		"declarative_net_request":{"rule_resources":[{"id":"r","enabled":true,"path":"rules.json"}]}}`,
	"rules.json": `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"/ad.js"}}]`,
}

func TestLoadZipArchive(t *testing.T) {
	data := makeZip(t, fixtureFiles)
	p := filepath.Join(t.TempDir(), "ext.xpi")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := webext.Load(p)
	if err != nil {
		t.Fatalf("Load(.xpi): %v", err)
	}
	if b.Manifest.Name != "Zipped" || b.Net.Count() != 1 {
		t.Errorf("unexpected bundle: name=%q rules=%d", b.Manifest.Name, b.Net.Count())
	}
	if !b.Net.Match(req(t, "https://x.com/ad.js", webext.TypeScript, "")).Block {
		t.Error("expected /ad.js to be blocked")
	}
}

func TestLoadCRXArchive(t *testing.T) {
	zipBytes := makeZip(t, fixtureFiles)
	// CRX3: "Cr24" + version(3) + headerLen(0) + zip.
	var crx bytes.Buffer
	crx.WriteString("Cr24")
	_ = binary.Write(&crx, binary.LittleEndian, uint32(3))
	_ = binary.Write(&crx, binary.LittleEndian, uint32(0))
	crx.Write(zipBytes)

	p := filepath.Join(t.TempDir(), "ext.crx")
	if err := os.WriteFile(p, crx.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := webext.Load(p)
	if err != nil {
		t.Fatalf("Load(.crx): %v", err)
	}
	if b.Net.Count() != 1 {
		t.Errorf("crx rules = %d, want 1", b.Net.Count())
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := webext.Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error loading a missing path")
	}
}
