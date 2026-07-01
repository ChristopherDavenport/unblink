package fetch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// TestTLSMimicH2Settings proves the --tls-mimic HTTP/2 transport advertises the
// Chrome-tuned SETTINGS (not Go's defaults). It stands up a raw TLS+h2 listener,
// reads the client's initial SETTINGS frame with an http2.Framer, and asserts the
// tuned values. This is the real check that the knobs in newUTLSRoundTripper take
// effect on the wire — the offline eval fixtures don't inspect fingerprints.
func TestTLSMimicH2Settings(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{selfSignedCert(t)},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		settings map[http2.SettingID]uint32
		alpn     string
		err      error
	}
	resc := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer conn.Close()
		tc := conn.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			resc <- result{err: err}
			return
		}
		alpn := tc.ConnectionState().NegotiatedProtocol
		// The client sends the connection preface then its SETTINGS frame right
		// after the handshake, before waiting for us — read the preface, then frames.
		if _, err := io.ReadFull(tc, make([]byte, len(http2.ClientPreface))); err != nil {
			resc <- result{err: err, alpn: alpn}
			return
		}
		fr := http2.NewFramer(io.Discard, tc)
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				resc <- result{err: err, alpn: alpn}
				return
			}
			sf, ok := f.(*http2.SettingsFrame)
			if !ok || sf.IsAck() {
				continue
			}
			settings := map[http2.SettingID]uint32{}
			_ = sf.ForeachSetting(func(s http2.Setting) error {
				settings[s.ID] = s.Val
				return nil
			})
			resc <- result{settings: settings, alpn: alpn}
			return
		}
	}()

	// Fire the request; the server closes after capturing SETTINGS, so this Get
	// errors out — we only care about the captured frame, not the (absent) response.
	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _, _ = c.Get(ctx, "https://"+ln.Addr().String()+"/") }()

	select {
	case r := <-resc:
		if r.settings == nil {
			t.Fatalf("did not capture client SETTINGS: %v (alpn=%q)", r.err, r.alpn)
		}
		if r.alpn != "h2" {
			t.Errorf("negotiated ALPN = %q, want h2", r.alpn)
		}
		for _, tc := range []struct {
			name string
			id   http2.SettingID
			want uint32
		}{
			{"HEADER_TABLE_SIZE", http2.SettingHeaderTableSize, chromeH2HeaderTableSize},
			{"MAX_HEADER_LIST_SIZE", http2.SettingMaxHeaderListSize, chromeH2MaxHeaderListSize},
			{"MAX_FRAME_SIZE", http2.SettingMaxFrameSize, chromeH2MaxFrameSize},
		} {
			if got, ok := r.settings[tc.id]; !ok {
				t.Errorf("%s not advertised in client SETTINGS", tc.name)
			} else if got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
			}
		}
	case <-time.After(6 * time.Second):
		t.Fatal("timed out waiting for client SETTINGS")
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
