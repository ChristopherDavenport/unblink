package js

import "time"

// Env bundles the per-render capabilities the browser supplies to the engine, so
// the Render signature stays small as capabilities grow. Every field is optional.
type Env struct {
	Transport Transport // page-JS network (external scripts, fetch/XHR, modules); nil = no JS network
	Cookies   CookieJar // backs document.cookie; nil = document.cookie is empty/no-op
	// Storage/SessionStorage back window.localStorage and window.sessionStorage.
	// Both are session-lived (a session is a tab: sessionStorage survives
	// navigations within it, dies with it). nil = fresh per-render in-memory map.
	Storage        Storage
	SessionStorage Storage
	Diag           *RenderResult  // if non-nil, the engine fills it with render diagnostics
	Wait           *WaitCondition // if set, keep the render alive until it holds or the budget elapses
	Timeout        time.Duration  // per-render budget override; 0 = engine default (hard-capped by the engine)
}

// CookieJar exposes the page's cookies to JavaScript (document.cookie), backed by
// the same jar the page's HTTP requests use.
type CookieJar interface {
	// Cookies returns the cookie header value for pageURL ("k=v; k2=v2").
	Cookies(pageURL string) string
	// SetCookie stores one Set-Cookie-style string ("name=value; attrs...") for pageURL.
	SetCookie(pageURL, cookie string)
}
