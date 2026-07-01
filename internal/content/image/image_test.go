package image

import (
	"bytes"
	stdimage "image"
	"image/png"
	"net/url"
	"strings"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestConvertPNGManifest(t *testing.T) {
	raw := makePNG(t, 7, 3)
	u, _ := url.Parse("https://example.com/pics/logo.png")
	md, title, err := Convert(raw, u, "image/png")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, want := range []string{"logo.png", "7×3", "image/png", "bytes:"} {
		if !strings.Contains(md, want) {
			t.Errorf("manifest missing %q:\n%s", want, md)
		}
	}
	if title != "logo.png" {
		t.Errorf("title = %q, want logo.png", title)
	}
}

func TestConvertUndecodableStillManifests(t *testing.T) {
	u, _ := url.Parse("https://example.com/broken.png")
	md, _, err := Convert([]byte("not really a png"), u, "image/png")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(md, "dimensions: unknown") {
		t.Errorf("expected unknown dimensions for undecodable image:\n%s", md)
	}
}
