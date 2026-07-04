package js

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"strings"
)

// Subresource Integrity (SRI, W3C spec) verification, applied to the <script> and
// module subresources unblink actually executes. It is a tamper-evidence posture
// over untrusted page code: when a resource carries an `integrity` attribute and
// the fetched bytes don't match, the resource is NOT executed — exactly as a
// browser refuses a CDN-drifted or MITM'd script. See ADR 0012.
//
// The bytes hashed must be the ones the server delivered (post content-encoding
// decode, pre charset-transcode). fetch transcodes JS to UTF-8 (see
// internal/fetch isTextualResponse), so callers pass Response.Raw, never the
// executed Body — otherwise a legitimate non-UTF-8/BOM'd script would
// false-mismatch and be wrongly blocked.

// sriBytes returns the bytes SRI must hash for res — the raw pre-transcode body
// the server sent (Response.Raw), falling back to Body when a transport didn't
// populate Raw (e.g. synthesized extension responses).
func sriBytes(res *Response) []byte {
	if res.Raw != nil {
		return res.Raw
	}
	return res.Body
}

// sriStrength ranks the SRI hash algorithms. Only the strongest algorithm named
// in the integrity metadata is enforced (weaker digests are ignored), matching
// the spec's "get the strongest metadata from set" step.
var sriStrength = map[string]int{"sha256": 1, "sha384": 2, "sha512": 3}

// sriHash returns a hash constructor for the named SRI algorithm, or nil for an
// unknown one. Unlike subtleHash (subtle.go) it never panics — the SRI parser
// simply skips tokens it can't hash.
func sriHash(alg string) func() hash.Hash {
	switch alg {
	case "sha256":
		return sha256.New
	case "sha384":
		return sha512.New384
	case "sha512":
		return sha512.New
	}
	return nil
}

// decodeSRIDigest decodes a base64 digest from an integrity token. The spec uses
// standard base64; we also accept the unpadded form for browser-like leniency.
func decodeSRIDigest(s string) []byte {
	if d, err := base64.StdEncoding.DecodeString(s); err == nil {
		return d
	}
	if d, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return d
	}
	return nil
}

// verifySRI reports whether raw satisfies the integrity attribute.
//
//	enforced == false           the attribute names no recognized hash (empty, or
//	                            only unknown algorithms). The caller must ALLOW the
//	                            resource, as a browser does for absent/unparseable
//	                            integrity.
//	enforced == true, ok == true    a digest of the strongest present algorithm matched.
//	enforced == true, ok == false   none matched — the caller must BLOCK the resource.
//
// Tokens are whitespace-separated `<alg>-<base64>[?options]`; unknown or malformed
// tokens are ignored (browser leniency). Must never panic (fuzzed).
func verifySRI(integrity string, raw []byte) (ok, enforced bool) {
	best := 0
	bestAlg := ""
	var want [][]byte
	for _, tok := range strings.Fields(integrity) {
		// Drop SRI option flags, e.g. "sha384-...?foo=bar".
		if i := strings.IndexByte(tok, '?'); i >= 0 {
			tok = tok[:i]
		}
		dash := strings.IndexByte(tok, '-')
		if dash <= 0 {
			continue
		}
		alg := strings.ToLower(tok[:dash])
		strength, known := sriStrength[alg]
		if !known {
			continue
		}
		digest := decodeSRIDigest(tok[dash+1:])
		if len(digest) == 0 {
			continue
		}
		switch {
		case strength > best:
			best, bestAlg, want = strength, alg, [][]byte{digest}
		case strength == best:
			want = append(want, digest)
		}
	}
	if best == 0 {
		return false, false // no recognized metadata → not enforced
	}
	h := sriHash(bestAlg)()
	h.Write(raw)
	sum := h.Sum(nil)
	for _, w := range want {
		if bytes.Equal(w, sum) {
			return true, true
		}
	}
	return false, true
}
