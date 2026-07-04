package js

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"testing"
)

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// sriValue builds a well-formed integrity token for data under alg.
func sriValue(alg string, data []byte) string {
	var sum []byte
	switch alg {
	case "sha256":
		s := sha256.Sum256(data)
		sum = s[:]
	case "sha384":
		s := sha512.Sum384(data)
		sum = s[:]
	case "sha512":
		s := sha512.Sum512(data)
		sum = s[:]
	}
	return alg + "-" + base64.StdEncoding.EncodeToString(sum)
}

func TestVerifySRI(t *testing.T) {
	body := []byte("console.log('hi');\n")
	other := []byte("evil()")

	tests := []struct {
		name      string
		integrity string
		wantOK    bool
		wantEnf   bool
	}{
		{"empty not enforced", "", false, false},
		{"whitespace not enforced", "   \t ", false, false},
		{"unknown alg only not enforced", "md5-abc sha1-def", false, false},
		{"sha256 match", sriValue("sha256", body), true, true},
		{"sha256 mismatch", sriValue("sha256", other), false, true},
		{"sha384 match", sriValue("sha384", body), true, true},
		{"sha512 match", sriValue("sha512", body), true, true},
		{"sha512 mismatch", sriValue("sha512", other), false, true},
		// strongest algorithm wins: sha256 matches but a present sha512 fails → block.
		{"strongest wins block", sriValue("sha256", body) + " " + sriValue("sha512", other), false, true},
		// strongest algorithm wins: sha512 matches, weaker sha256 wrong → allowed.
		{"strongest wins allow", sriValue("sha256", other) + " " + sriValue("sha512", body), true, true},
		// multiple digests of the same (strongest) algorithm: any match passes.
		{"multi same-alg any match", sriValue("sha384", other) + " " + sriValue("sha384", body), true, true},
		// SRI option flags after the digest are stripped.
		{"option flag stripped", sriValue("sha384", body) + "?ct=application/javascript", true, true},
		// a malformed token is ignored, a valid sibling honored.
		{"malformed ignored", "sha384-!!!notbase64!!! " + sriValue("sha384", body), true, true},
		// unpadded base64 is accepted (browser leniency).
		{"unpadded base64", "sha256-" + base64.RawStdEncoding.EncodeToString(sha256Sum(body)), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, enf := verifySRI(tt.integrity, body)
			if ok != tt.wantOK || enf != tt.wantEnf {
				t.Fatalf("verifySRI(%q) = (ok=%v, enforced=%v), want (ok=%v, enforced=%v)",
					tt.integrity, ok, enf, tt.wantOK, tt.wantEnf)
			}
		})
	}
}
