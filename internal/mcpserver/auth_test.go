package mcpserver

// White-box tests for the credential plumbing: env indirection, origin scoping,
// and the guarantee that a missing url never yields unscoped credentials. This
// is security-sensitive glue — argument mistakes here would leak secrets.

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

func TestAuthResolveBearer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arg     authArg
		env     map[string]string
		bearer  string
		wantErr string
	}{
		{name: "literal token", arg: authArg{Token: "tok-lit"}, bearer: "tok-lit"},
		{name: "explicit type", arg: authArg{Type: "bearer", Token: "tok-2"}, bearer: "tok-2"},
		{name: "token_env wins over literal", arg: authArg{Token: "lit", TokenEnv: "UNBLINK_TEST_TOK"},
			env: map[string]string{"UNBLINK_TEST_TOK": "from-env"}, bearer: "from-env"},
		{name: "token_env unset errors", arg: authArg{TokenEnv: "UNBLINK_TEST_UNSET"},
			wantErr: "UNBLINK_TEST_UNSET"},
		{name: "no token errors", arg: authArg{Type: "bearer"}, wantErr: "requires token"},
		{name: "unknown type errors", arg: authArg{Type: "digest", Token: "x"}, wantErr: "unknown auth type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			bearer, user, pass, err := tc.arg.resolve()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if bearer != tc.bearer || user != "" || pass != "" {
				t.Errorf("resolve = (%q, %q, %q), want bearer %q only", bearer, user, pass, tc.bearer)
			}
		})
	}
}

func TestAuthResolveBasic(t *testing.T) {
	t.Setenv("UNBLINK_TEST_PW", "pw-env")

	// Username/password fields imply basic without an explicit type.
	for _, tc := range []struct {
		name       string
		arg        authArg
		user, pass string
	}{
		{name: "explicit", arg: authArg{Type: "basic", Username: "u", Password: "p"}, user: "u", pass: "p"},
		{name: "implied by username", arg: authArg{Username: "u2", Password: "p2"}, user: "u2", pass: "p2"},
		{name: "password_env", arg: authArg{Username: "u3", PasswordEnv: "UNBLINK_TEST_PW"}, user: "u3", pass: "pw-env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bearer, user, pass, err := tc.arg.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if bearer != "" || user != tc.user || pass != tc.pass {
				t.Errorf("resolve = (%q, %q, %q), want basic %q/%q", bearer, user, pass, tc.user, tc.pass)
			}
		})
	}

	if _, _, _, err := (&authArg{Username: "u", PasswordEnv: "UNBLINK_TEST_PW_UNSET"}).resolve(); err == nil {
		t.Error("unset password_env should error")
	}
}

// Credentials without a url must fail — an unscoped credential would be sent
// nowhere (fetch fails closed), but the tool contract is an explicit error.
func TestSessionConfigRequiresOriginForCredentials(t *testing.T) {
	for _, args := range []sessionArgs{
		{Auth: &authArg{Token: "t"}},
		{Headers: map[string]string{"X-K": "v"}},
		{Cookies: []cookieArg{{Name: "sid", Value: "1"}}},
		{Auth: &authArg{Token: "t"}, URL: "not a url"},
	} {
		if _, err := sessionConfig(args); err == nil {
			t.Errorf("sessionConfig(%+v) should require a valid url", args)
		}
	}

	cfg, err := sessionConfig(sessionArgs{
		URL:     "https://api.example.com/v1/login?next=/x",
		Auth:    &authArg{Token: "tok"},
		Headers: map[string]string{"X-K": "v"},
		Cookies: []cookieArg{{Name: "sid", Value: "1"}},
	})
	if err != nil {
		t.Fatalf("sessionConfig: %v", err)
	}
	if cfg.Origin != "https://api.example.com" {
		t.Errorf("Origin = %q, want scheme://host only (no path/query)", cfg.Origin)
	}
	if cfg.Bearer != "tok" || len(cfg.Cookies) != 1 || cfg.Headers["X-K"] != "v" {
		t.Errorf("config not fully populated: %+v", cfg)
	}

	// Anonymous session: no url needed, no origin set.
	anon, err := sessionConfig(sessionArgs{})
	if err != nil || anon.HasCredentials() || anon.Origin != "" {
		t.Errorf("anonymous config = %+v, err = %v", anon, err)
	}
}

func TestApplyOneShot(t *testing.T) {
	t.Setenv("UNBLINK_TEST_OS_TOK", "one-shot")
	var req browser.Request
	if err := applyOneShot(&req, map[string]string{"X-Api-Key": "k"}, &authArg{TokenEnv: "UNBLINK_TEST_OS_TOK"}); err != nil {
		t.Fatalf("applyOneShot: %v", err)
	}
	if req.Headers["X-Api-Key"] != "k" || req.Auth == nil || req.Auth.Bearer != "one-shot" {
		t.Errorf("request not populated: %+v", req)
	}

	var plain browser.Request
	if err := applyOneShot(&plain, nil, nil); err != nil || plain.Auth != nil {
		t.Errorf("nil auth should be a no-op: %+v, err=%v", plain, err)
	}

	var bad browser.Request
	if err := applyOneShot(&bad, nil, &authArg{TokenEnv: "UNBLINK_TEST_OS_UNSET"}); err == nil {
		t.Error("unset env should error")
	}
}
