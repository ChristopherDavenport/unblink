package webext

import "testing"

func TestStripCRXErrors(t *testing.T) {
	if _, err := stripCRX([]byte("not a crx")); err == nil {
		t.Error("expected error for bad magic")
	}
	// Valid magic, unsupported version.
	bad := append([]byte("Cr24"), 9, 0, 0, 0, 0, 0, 0, 0)
	if _, err := stripCRX(bad); err == nil {
		t.Error("expected error for unsupported CRX version")
	}
	// CRX3 header length overruns the buffer.
	overrun := append([]byte("Cr24"), 3, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f)
	if _, err := stripCRX(overrun); err == nil {
		t.Error("expected error for corrupt CRX3 header length")
	}
}

func TestCleanRelRejectsTraversal(t *testing.T) {
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../b", ""} {
		if got := cleanRel(p); got != "" && (got == "../secret" || got == "/etc/passwd") {
			t.Errorf("cleanRel(%q) = %q, expected sanitized/rejected", p, got)
		}
	}
	if cleanRel("rules.json") != "rules.json" {
		t.Error("cleanRel should keep a simple relative path")
	}
	if cleanRel("/rules.json") != "rules.json" {
		t.Error("cleanRel should strip a leading slash")
	}
}
