package mcpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/session"
)

// authArg describes credentials for a session or a one-shot request. A secret may
// be given literally (token/password) or, preferably, by the name of a server env
// var (token_env/password_env) so it never appears in the MCP transcript.
type authArg struct {
	Type        string `json:"type,omitempty" jsonschema:"bearer (default) or basic"`
	Token       string `json:"token,omitempty" jsonschema:"bearer token, literal (prefer token_env to keep secrets out of the transcript)"`
	TokenEnv    string `json:"token_env,omitempty" jsonschema:"name of a server-side env var holding the bearer token"`
	Username    string `json:"username,omitempty" jsonschema:"basic-auth username"`
	Password    string `json:"password,omitempty" jsonschema:"basic-auth password, literal (prefer password_env)"`
	PasswordEnv string `json:"password_env,omitempty" jsonschema:"name of a server-side env var holding the basic-auth password"`
}

// cookieArg is one cookie to seed into a session's jar, scoped to the session url.
type cookieArg struct {
	Name  string `json:"name" jsonschema:"cookie name"`
	Value string `json:"value" jsonschema:"cookie value"`
}

// resolve turns an authArg into (bearer, user, pass), applying env indirection.
// Exactly one of bearer or user/pass is populated on success.
func (a *authArg) resolve() (bearer, user, pass string, err error) {
	typ := strings.ToLower(strings.TrimSpace(a.Type))
	if typ == "" {
		if a.Username != "" || a.Password != "" || a.PasswordEnv != "" {
			typ = "basic"
		} else {
			typ = "bearer"
		}
	}
	switch typ {
	case "bearer":
		bearer, err = resolveSecret(a.Token, a.TokenEnv)
		if err != nil {
			return "", "", "", fmt.Errorf("bearer token: %w", err)
		}
		if bearer == "" {
			return "", "", "", fmt.Errorf("bearer auth requires token or token_env")
		}
		return bearer, "", "", nil
	case "basic":
		pass, err = resolveSecret(a.Password, a.PasswordEnv)
		if err != nil {
			return "", "", "", fmt.Errorf("basic password: %w", err)
		}
		return "", a.Username, pass, nil
	default:
		return "", "", "", fmt.Errorf("unknown auth type %q (want bearer|basic)", a.Type)
	}
}

// resolveSecret returns the literal value, or the value of the named env var when
// envName is set. It errors when envName is set but the env var is empty/unset.
func resolveSecret(literal, envName string) (string, error) {
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("env var %q is not set", envName)
	}
	return literal, nil
}

// sessionConfig builds a per-session credential config from the session args,
// resolving env indirection. Credentials must be scoped to an origin, so a url is
// required whenever any header/auth/cookie is supplied.
func sessionConfig(args sessionArgs) (session.Config, error) {
	cfg := session.Config{Headers: args.Headers}
	if args.Auth != nil {
		b, u, p, err := args.Auth.resolve()
		if err != nil {
			return session.Config{}, err
		}
		cfg.Bearer, cfg.BasicUser, cfg.BasicPass = b, u, p
	}
	for _, c := range args.Cookies {
		cfg.Cookies = append(cfg.Cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	if cfg.HasCredentials() {
		origin := originOf(args.URL)
		if origin == "" {
			return session.Config{}, fmt.Errorf("a valid url is required when creating a session with auth/headers/cookies (it scopes the credentials to one origin)")
		}
		cfg.Origin = origin
	}
	return cfg, nil
}

// applyOneShot resolves one-shot headers/auth onto a stateless request.
func applyOneShot(req *browser.Request, headers map[string]string, auth *authArg) error {
	req.Headers = headers
	if auth == nil {
		return nil
	}
	b, u, p, err := auth.resolve()
	if err != nil {
		return err
	}
	req.Auth = &browser.RequestAuth{Bearer: b, BasicUser: u, BasicPass: p}
	return nil
}

// originOf reduces rawURL to "scheme://host", or "" if it has no host.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
