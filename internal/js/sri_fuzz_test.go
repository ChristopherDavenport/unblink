package js

import (
	"encoding/base64"
	"testing"
)

// FuzzVerifySRI feeds arbitrary, attacker-controlled integrity attributes and
// bodies (both are untrusted page input) at the SRI parser. It must never panic,
// must never report ok without enforced, and a freshly computed self-hash must
// always verify — so no input can silently disable enforcement or bypass a match.
func FuzzVerifySRI(f *testing.F) {
	f.Add("sha256-abc", []byte("data"))
	f.Add("", []byte(""))
	f.Add("sha512-"+base64.StdEncoding.EncodeToString(make([]byte, 64)), []byte("x"))
	f.Add("sha384-!!! sha256-?? garbage", []byte("y"))
	f.Add("  sha256-  ", []byte("z"))

	f.Fuzz(func(t *testing.T, integrity string, raw []byte) {
		ok, enforced := verifySRI(integrity, raw)
		if ok && !enforced {
			t.Fatalf("verifySRI(%q) returned ok=true with enforced=false", integrity)
		}
		// A correct digest of the very bytes passed must always verify.
		if okv, enfv := verifySRI(sriValue("sha256", raw), raw); !okv || !enfv {
			t.Fatalf("self-hash sha256 failed to verify: ok=%v enforced=%v", okv, enfv)
		}
	})
}
