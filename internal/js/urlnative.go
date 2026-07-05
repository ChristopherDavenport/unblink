package js

import (
	"strings"

	"github.com/dop251/goja"
	urlpkg "github.com/nlnwa/whatwg-url/url"
)

// installURL binds a native, spec-conformant WHATWG URL parser
// (github.com/nlnwa/whatwg-url) as the __unblinkURLParse / __unblinkURLSet
// primitives; the JS window.URL class in the prelude wraps them. It mirrors
// installSubtle (subtle.go): stateless per-call primitives, one parser per bridge
// (every call runs on this runtime's single loop goroutine, and concurrent renders
// use separate bridges). Replacing the former hand-written regex URL fixes
// correctness — reparsing setters, IDNA/punycode hosts, percent-encoding, special
// schemes, userinfo, opaque origins — and removes the corpus-scale interpreter cost
// that made the WPT url/ suite time out. The Go-side link/fetch pipeline keeps
// using net/url and is unaffected.
func (b *bridge) installURL() {
	vm := b.vm
	parser := urlpkg.NewParser()

	toObj := func(u *urlpkg.Url) goja.Value {
		o := vm.NewObject()
		_ = o.Set("href", u.Href(false))
		_ = o.Set("protocol", u.Protocol())
		_ = o.Set("username", u.Username())
		_ = o.Set("password", u.Password())
		_ = o.Set("host", u.Host())
		_ = o.Set("hostname", u.Hostname())
		_ = o.Set("port", u.Port())
		_ = o.Set("pathname", u.Pathname())
		_ = o.Set("search", u.Search())
		_ = o.Set("hash", u.Hash())
		_ = o.Set("origin", urlOrigin(u))
		return o
	}
	invalid := func() goja.Value {
		o := vm.NewObject()
		_ = o.Set("invalid", true)
		return o
	}
	parse := func(input string, base goja.Value) (*urlpkg.Url, bool) {
		var (
			u   *urlpkg.Url
			err error
		)
		if base != nil && !goja.IsUndefined(base) && !goja.IsNull(base) {
			u, err = parser.ParseRef(base.String(), input)
		} else {
			u, err = parser.Parse(input)
		}
		if err != nil || u == nil {
			return nil, false
		}
		return u, true
	}

	// __unblinkURLParse(input, base?) -> {href, protocol, …, origin} or {invalid:true}.
	_ = vm.Set("__unblinkURLParse", func(call goja.FunctionCall) goja.Value {
		if u, ok := parse(call.Argument(0).String(), call.Argument(1)); ok {
			return toObj(u)
		}
		return invalid()
	})

	// __unblinkURLSet(href, prop, value) reparses href, applies the setter, and
	// returns the re-serialized components — this is what makes the url-setters
	// family work (a setter reparses instead of overwriting a field).
	_ = vm.Set("__unblinkURLSet", func(call goja.FunctionCall) goja.Value {
		u, ok := parse(call.Argument(0).String(), nil)
		if !ok {
			return invalid()
		}
		val := call.Argument(2).String()
		switch call.Argument(1).String() {
		case "href":
			nu, ok := parse(val, nil)
			if !ok {
				return invalid()
			}
			u = nu
		case "protocol":
			u.SetProtocol(val)
		case "username":
			u.SetUsername(val)
		case "password":
			u.SetPassword(val)
		case "host":
			u.SetHost(val)
		case "hostname":
			u.SetHostname(val)
		case "port":
			u.SetPort(val)
		case "pathname":
			u.SetPathname(val)
		case "search":
			u.SetSearch(val)
		case "hash":
			u.SetHash(val)
		}
		return toObj(u)
	})
}

// urlOrigin serializes a URL's origin: the scheme/host tuple for special
// network schemes, the inner URL's origin for blob:, and the opaque "null"
// otherwise (file:, data:, …). Host() already omits a default port.
func urlOrigin(u *urlpkg.Url) string {
	scheme := strings.ToLower(u.Scheme())
	switch scheme {
	case "http", "https", "ws", "wss", "ftp":
		return scheme + "://" + u.Host()
	case "blob":
		if inner, err := urlpkg.Parse(u.Pathname()); err == nil {
			return urlOrigin(inner)
		}
		return "null"
	default:
		return "null"
	}
}
