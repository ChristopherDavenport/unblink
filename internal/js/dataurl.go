package js

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// data: URLs as script sources (RFC 2397, as refined by the Fetch Standard's
// "data: URL processor").
//
// A <script src="data:text/javascript;base64,…">, a module specifier, or a
// preload hint that names a data: URL carries its own bytes: there is nothing to
// request. Every script-loading path fetches through scriptDo, which decodes
// such a URL locally instead of handing it to the transport — so it is never
// sent anywhere and never appears in the requests log as a network request.
//
// Everything else about loading a script still applies, because scriptDo is
// called exactly where the transport was:
//
//   - CSP: the callers' existing script-src check runs first, on the same URL.
//     A data: URL matches only an explicit `data:` scheme-source (or the trust
//     'strict-dynamic' propagates); a host-source, `*` and 'self' never match it
//     (see sourceMatchesURL). So a policy that does not opt into data: scripts
//     blocks them, as a browser does.
//   - SRI: the decoded bytes are returned as both Body and Raw, so an integrity
//     attribute is checked against what will execute.
//   - The byte budget (ADR 0009): the decoded length is charged to the
//     transport's download budget, so a page cannot use megabytes of inline
//     data: scripts to run more code than --js-max-bytes allows over the network.
//
// blob: URLs are a different mechanism (a registry of in-memory Blobs keyed by
// an origin-scoped URL) and are not handled here.

var errDataURL = errors.New("malformed data: URL")

func isDataURL(u string) bool {
	return len(u) >= 5 && strings.EqualFold(u[:5], "data:")
}

// decodeDataURL returns the body and MIME type of a data: URL. The media type
// defaults to text/plain;charset=US-ASCII when absent; `;base64` (matched
// case-insensitively, optionally surrounded by spaces) selects base64 over
// percent-encoding. A fragment is not part of the data.
func decodeDataURL(u string) (body []byte, mime string, err error) {
	if !isDataURL(u) {
		return nil, "", errDataURL
	}
	rest := u[5:]
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	meta, data, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", errDataURL
	}
	meta = strings.TrimSpace(meta)
	isBase64 := false
	if i := strings.LastIndexByte(meta, ';'); i >= 0 && strings.EqualFold(strings.TrimSpace(meta[i+1:]), "base64") {
		isBase64 = true
		meta = strings.TrimSpace(meta[:i])
	}
	mime = meta
	if mime == "" || strings.HasPrefix(mime, ";") {
		mime = "text/plain" + mime
		if !strings.Contains(strings.ToLower(mime), "charset=") {
			mime += ";charset=US-ASCII"
		}
	}
	raw, uerr := url.PathUnescape(data)
	if uerr != nil {
		// A stray '%' is literal in a data: URL, not an error: decode what parses.
		raw = lenientPercentDecode(data)
	}
	if !isBase64 {
		return []byte(raw), mime, nil
	}
	// Forgiving base64: ASCII white space is ignored and padding is optional.
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			return -1
		}
		return r
	}, raw)
	cleaned = strings.TrimRight(cleaned, "=")
	dec, derr := base64.RawStdEncoding.DecodeString(cleaned)
	if derr != nil {
		// base64url alphabet is not valid in a data: URL, but tolerate it rather
		// than fail a script over an encoder's choice.
		if dec, derr = base64.RawURLEncoding.DecodeString(cleaned); derr != nil {
			return nil, "", errDataURL
		}
	}
	return dec, mime, nil
}

// lenientPercentDecode decodes valid %XX escapes and leaves anything else as is.
func lenientPercentDecode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, lo := unhex(s[i+1]), unhex(s[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// byteCharger is implemented by a transport that enforces a download budget and
// can be charged for bytes that did not come over the network.
type byteCharger interface {
	ChargeBytes(n int64) error
}

// chargeBytes charges n bytes to t's download budget, if it has one. The js
// wrappers forward it down the chain to the browser's budget-enforcing
// transport; a transport without a budget charges nothing.
func chargeBytes(t Transport, n int64) error {
	if c, ok := t.(byteCharger); ok {
		return c.ChargeBytes(n)
	}
	return nil
}

func (t *countingTransport) ChargeBytes(n int64) error    { return chargeBytes(t.inner, n) }
func (t *secFetchTransport) ChargeBytes(n int64) error    { return chargeBytes(t.inner, n) }
func (t *blockingTransport) ChargeBytes(n int64) error    { return chargeBytes(t.inner, n) }
func (t *extResourceTransport) ChargeBytes(n int64) error { return chargeBytes(t.inner, n) }

// scriptDo performs the GET for a script or module source at abs. It is the
// one place the script-loading paths reach the network: parser-inserted and
// script-inserted classic scripts, module entries and their dependencies,
// dynamic import(), and the preload/modulepreload warmers. A data: URL is
// answered locally (see the file comment); anything else goes to the transport
// unchanged.
func (b *bridge) scriptDo(ctx context.Context, abs string) (*Response, error) {
	if !isDataURL(abs) {
		return b.transport.Do(ctx, "GET", abs, nil, nil)
	}
	body, mime, err := decodeDataURL(abs)
	if err != nil {
		return nil, err
	}
	if err := chargeBytes(b.transport, int64(len(body))); err != nil {
		return nil, err
	}
	return &Response{Status: 200, Headers: map[string]string{"content-type": mime}, Body: body, Raw: body, FinalURL: abs}, nil
}
