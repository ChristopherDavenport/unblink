package js

import (
	"errors"
	"testing"
)

func TestDecodeDataURL(t *testing.T) {
	cases := []struct{ in, body, mime string }{
		{"data:text/javascript;base64,d2luZG93Lng9MQ==", "window.x=1", "text/javascript"},
		{"data:text/javascript;base64,d2luZG93Lng9MQ", "window.x=1", "text/javascript"}, // padding optional
		{"data:text/javascript;charset=utf-8;BASE64 ,d2lu ZG93\nLng9MQ==", "window.x=1", "text/javascript;charset=utf-8"},
		{"data:text/javascript,window.x%3D1%3B%20y()", "window.x=1; y()", "text/javascript"},
		{"data:,plain%20text", "plain text", "text/plain;charset=US-ASCII"},
		{"data:;charset=utf-8,h%C3%A9", "hé", "text/plain;charset=utf-8"},
		{"DATA:text/javascript,a#frag", "a", "text/javascript"},
		{"data:text/javascript,100%done%2", "100%done%2", "text/javascript"}, // stray % is literal
		{"data:application/json;base64,eyJhIjoxfQ==", `{"a":1}`, "application/json"},
		{"data:text/javascript,", "", "text/javascript"},
	}
	for _, c := range cases {
		body, mime, err := decodeDataURL(c.in)
		if err != nil || string(body) != c.body || mime != c.mime {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q)", c.in, body, mime, err, c.body, c.mime)
		}
	}
	for _, bad := range []string{"data:text/javascript", "http://x/", "data:text/javascript;base64,@@@@", ""} {
		if _, _, err := decodeDataURL(bad); !errors.Is(err, errDataURL) {
			t.Errorf("%q: want errDataURL, got %v", bad, err)
		}
	}
}
