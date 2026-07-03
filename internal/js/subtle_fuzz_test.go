package js

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// FuzzSubtlePrimitives checks the hand-rolled crypto helpers on arbitrary,
// attacker-influenced bytes: PKCS#7 pad/unpad round-trips losslessly, unpad on
// arbitrary input errors-or-succeeds but never panics, and PBKDF2/HKDF produce
// exactly the requested length without panicking. The iteration/length DoS knobs
// are bounded here just as the native layer bounds them in production.
func FuzzSubtlePrimitives(f *testing.F) {
	f.Add([]byte("data"), []byte("password"), []byte("salt"), 100, 32)
	f.Add([]byte(""), []byte(""), []byte(""), 1, 16)
	f.Add([]byte("\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10\x10"), []byte("k"), []byte(""), 3, 0)

	f.Fuzz(func(t *testing.T, data, password, salt []byte, iter, keyLen int) {
		if iter < 1 {
			iter = 1
		}
		if iter > 4096 {
			iter = 4096
		}
		if keyLen < 0 {
			keyLen = 0
		}
		if keyLen > 4096 {
			keyLen = 4096
		}

		// PKCS#7 pad/unpad round-trips.
		padded := pkcs7Pad(data, 16)
		if len(padded)%16 != 0 {
			t.Fatalf("pad not block-aligned: %d", len(padded))
		}
		un, err := pkcs7Unpad(padded, 16)
		if err != nil || !bytes.Equal(un, data) {
			t.Fatalf("pkcs7 roundtrip failed: err=%v", err)
		}
		// Arbitrary bytes into unpad: error or ok, never a panic.
		_, _ = pkcs7Unpad(data, 16)

		// PBKDF2 / HKDF yield exactly keyLen bytes and never panic.
		if got := len(pbkdf2Key(sha256.New, password, salt, iter, keyLen)); got != keyLen {
			t.Fatalf("pbkdf2 length: got %d want %d", got, keyLen)
		}
		if hk, herr := hkdfKey(sha256.New, password, salt, data, keyLen); herr == nil && len(hk) != keyLen {
			t.Fatalf("hkdf length: got %d want %d", len(hk), keyLen)
		}
	})
}
