package fetch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// h2ClientOpening is what a client put on the wire to open an HTTP/2 connection
// and send its first request: everything a server-side HTTP/2 fingerprinter keys
// on.
type h2ClientOpening struct {
	alpn         string
	settings     []http2.Setting // in wire order
	windowUpdate uint32          // connection-level (stream 0) increment
	sawWindow    bool
	pseudo       []string // pseudo-header names, in wire order
	regular      []string // regular header names, in wire order
}

// captureH2Opening stands up a raw TLS+h2 listener, drives one --tls-mimic GET at
// it, and returns the client's opening frames up to and including the first
// HEADERS. The frames are read off the socket with a Framer; nothing is taken
// from the client's own configuration.
func captureH2Opening(t *testing.T) h2ClientOpening {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{selfSignedCert(t)},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		got h2ClientOpening
		err error
	}
	resc := make(chan result, 1)
	go func() {
		var got h2ClientOpening
		fail := func(err error) { resc <- result{got: got, err: err} }
		conn, err := ln.Accept()
		if err != nil {
			fail(err)
			return
		}
		defer conn.Close()
		tc := conn.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			fail(err)
			return
		}
		got.alpn = tc.ConnectionState().NegotiatedProtocol
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(tc, preface); err != nil {
			fail(err)
			return
		}
		if string(preface) != http2.ClientPreface {
			fail(fmt.Errorf("bad client preface %q", preface))
			return
		}
		fr := http2.NewFramer(tc, tc)
		// Decode HEADERS with the pre-SETTINGS default table size: this server
		// never advertises another, so the client's encoder stays at 4096.
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		// A real server opens with its own SETTINGS; send an empty one so the
		// client is never left waiting on it.
		if err := fr.WriteSettings(); err != nil {
			fail(err)
			return
		}
		sawSettings := false
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				fail(err)
				return
			}
			switch f := f.(type) {
			case *http2.SettingsFrame:
				if f.IsAck() {
					continue
				}
				if sawSettings {
					fail(errors.New("client sent a second SETTINGS frame before HEADERS"))
					return
				}
				sawSettings = true
				_ = f.ForeachSetting(func(s http2.Setting) error {
					got.settings = append(got.settings, s)
					return nil
				})
			case *http2.WindowUpdateFrame:
				if f.StreamID != 0 {
					continue
				}
				if got.sawWindow {
					fail(errors.New("client sent a second connection WINDOW_UPDATE before HEADERS"))
					return
				}
				got.sawWindow = true
				got.windowUpdate = f.Increment
			case *http2.MetaHeadersFrame:
				for _, hf := range f.Fields {
					if hf.IsPseudo() {
						got.pseudo = append(got.pseudo, hf.Name)
					} else {
						got.regular = append(got.regular, hf.Name)
					}
				}
				resc <- result{got: got}
				return
			}
		}
	}()

	// The server hangs up once it has the HEADERS frame, so this Get errors out;
	// only the captured frames matter, not the (absent) response.
	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _, _ = c.Get(ctx, "https://"+ln.Addr().String()+"/") }()

	select {
	case r := <-resc:
		if r.err != nil {
			t.Fatalf("capturing client opening frames: %v (got so far %+v)", r.err, r.got)
		}
		return r.got
	case <-time.After(6 * time.Second):
		t.Fatal("timed out waiting for the client's opening frames")
	}
	return h2ClientOpening{}
}

// TestTLSMimicH2Settings pins the --tls-mimic HTTP/2 opening where a
// fingerprinter can observe it: the full SETTINGS frame (which settings are
// present, their order, their values), the connection WINDOW_UPDATE, and the
// pseudo-header order. The three Chrome-tuned values come from
// newUTLSRoundTripper; the rest is whatever Go's HTTP/2 client emits, pinned so
// that a toolchain bump, an x/net bump, or a move to a different client API
// cannot change the fingerprint unnoticed. A failure here after such a change
// means the fingerprint moved, which is a decision to make, not an expectation to
// update.
func TestTLSMimicH2Settings(t *testing.T) {
	got := captureH2Opening(t)

	if got.alpn != "h2" {
		t.Errorf("negotiated ALPN = %q, want h2", got.alpn)
	}

	// Literal values, not the consts in tls.go: editing a const must fail here
	// rather than move the wire value with the test agreeing.
	wantSettings := []http2.Setting{
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: 4194304},
		{ID: http2.SettingMaxFrameSize, Val: 16384},
		{ID: http2.SettingMaxHeaderListSize, Val: 262144},
		{ID: http2.SettingHeaderTableSize, Val: 65536},
	}
	if !slices.Equal(got.settings, wantSettings) {
		t.Errorf("client SETTINGS frame\n got: %v\nwant: %v", got.settings, wantSettings)
	}

	if !got.sawWindow {
		t.Error("client sent no connection-level WINDOW_UPDATE before HEADERS")
	} else if got.windowUpdate != 1073741824 {
		t.Errorf("connection WINDOW_UPDATE increment = %d, want 1073741824", got.windowUpdate)
	}

	wantPseudo := []string{":authority", ":method", ":path", ":scheme"}
	if !slices.Equal(got.pseudo, wantPseudo) {
		t.Errorf("pseudo-header order = %v, want %v", got.pseudo, wantPseudo)
	}

	// Regular headers follow the pseudo-headers in Go map-iteration order, so
	// only their presence is stable enough to pin.
	for _, name := range []string{"user-agent", "accept-encoding"} {
		if !slices.Contains(got.regular, name) {
			t.Errorf("request HEADERS missing %q (got %v)", name, got.regular)
		}
	}
}

// selfSignedCert makes a throwaway P-256 cert for 127.0.0.1 so the test server can
// complete a TLS handshake; the client uses withInsecureTLS so validity is moot.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "unblink-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}
