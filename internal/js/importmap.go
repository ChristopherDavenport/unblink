package js

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// An importMap is the parsed, normalized form of the document's
// <script type="importmap">, following the HTML Standard's import map algorithms
// (https://html.spec.whatwg.org/multipage/webappapis.html#import-maps): top-level
// "imports" plus "scopes", which remap a specifier differently for modules whose
// URL falls under a scope prefix. A nil *importMap is valid and maps nothing.
type importMap struct {
	imports *specifierMap
	scopes  []importScope // most specific (longest) prefix first
}

// importScope is one "scopes" entry: the specifier map that applies to any
// referrer whose URL equals prefix, or starts with it when prefix ends in "/".
type importScope struct {
	prefix  string
	imports *specifierMap
}

// specifierMap is a normalized specifier map. A "" address is the spec's null
// entry: the key was present but its address was invalid, so a specifier matching
// it fails to resolve instead of falling through to a less specific mapping.
type specifierMap struct {
	keys  []string // descending order, so the longest matching prefix key wins
	addrs map[string]string
}

// parseImportMap reads the first <script type="importmap"> and normalizes its
// "imports" and "scopes" against base (the document base URL). A map that is not
// JSON, or whose "imports"/"scopes"/scope value is not an object, is rejected whole
// (the spec throws a TypeError); an individual invalid key, address, or scope prefix
// only drops or nulls that entry.
func parseImportMap(doc *html.Node, base *url.URL) *importMap {
	n := selImportMap.MatchFirst(doc)
	if n == nil {
		return nil
	}
	var raw struct {
		Imports json.RawMessage `json:"imports"`
		Scopes  json.RawMessage `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(scriptText(n)), &raw); err != nil {
		return nil
	}
	im := &importMap{}
	if len(raw.Imports) > 0 {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw.Imports, &m); err != nil {
			return nil
		}
		im.imports = normalizeSpecifierMap(m, base)
	}
	if len(raw.Scopes) > 0 {
		var scopes map[string]json.RawMessage
		if err := json.Unmarshal(raw.Scopes, &scopes); err != nil {
			return nil
		}
		byPrefix := make(map[string]*specifierMap, len(scopes))
		for prefix, v := range scopes {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(v, &m); err != nil {
				return nil
			}
			pu, err := parseURLWithBase(prefix, base)
			if err != nil {
				continue // unparseable scope prefix: skip this scope only
			}
			byPrefix[serializeModuleURL(pu)] = normalizeSpecifierMap(m, base)
		}
		for prefix, sm := range byPrefix {
			im.scopes = append(im.scopes, importScope{prefix: prefix, imports: sm})
		}
		sort.Slice(im.scopes, func(i, j int) bool { return im.scopes[i].prefix > im.scopes[j].prefix })
	}
	return im
}

// normalizeSpecifierMap implements "sort and normalize a module specifier map":
// URL-like keys are resolved against base, addresses must be URL-like, and a
// trailing-slash key needs a trailing-slash address.
func normalizeSpecifierMap(m map[string]json.RawMessage, base *url.URL) *specifierMap {
	sm := &specifierMap{addrs: make(map[string]string, len(m))}
	for key, v := range m {
		if key == "" {
			continue
		}
		if ku := resolveURLLike(key, base); ku != nil {
			key = serializeModuleURL(ku)
		}
		addr := ""
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if au := resolveURLLike(s, base); au != nil {
				addr = serializeModuleURL(au)
			}
		}
		if strings.HasSuffix(key, "/") && !strings.HasSuffix(addr, "/") {
			addr = ""
		}
		sm.addrs[key] = addr
	}
	sm.keys = make([]string, 0, len(sm.addrs))
	for k := range sm.addrs {
		sm.keys = append(sm.keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sm.keys)))
	return sm
}

// resolve implements "resolve a module specifier": spec as written in the module
// whose base URL is base (the importing module's URL, or the document base for an
// inline module / classic script). Scopes matching base are tried most specific
// first, then top-level imports, then spec itself when it is URL-like.
func (m *importMap) resolve(spec string, base *url.URL) (string, error) {
	asURL := resolveURLLike(spec, base)
	normalized := spec
	if asURL != nil {
		normalized = serializeModuleURL(asURL)
	}
	if m != nil {
		baseStr := ""
		if base != nil {
			baseStr = serializeModuleURL(base)
		}
		for _, sc := range m.scopes {
			if sc.prefix != baseStr && (!strings.HasSuffix(sc.prefix, "/") || !strings.HasPrefix(baseStr, sc.prefix)) {
				continue
			}
			if got, ok, err := sc.imports.match(normalized, asURL); err != nil || ok {
				return got, err
			}
		}
		if got, ok, err := m.imports.match(normalized, asURL); err != nil || ok {
			return got, err
		}
	}
	if asURL != nil {
		return normalized, nil
	}
	return "", fmt.Errorf("unresolved bare specifier %q", spec)
}

// match implements "resolve an imports match": an exact key, else the longest
// trailing-slash prefix key. ok=false means no key applied (the caller falls back);
// an error means a key applied but its mapping is unusable, which is final.
func (sm *specifierMap) match(normalized string, asURL *url.URL) (string, bool, error) {
	if sm == nil {
		return "", false, nil
	}
	for _, key := range sm.keys {
		addr := sm.addrs[key]
		if key == normalized {
			if addr == "" {
				return "", false, fmt.Errorf("import map blocks specifier %q (invalid address)", normalized)
			}
			return addr, true, nil
		}
		if !strings.HasSuffix(key, "/") || !strings.HasPrefix(normalized, key) {
			continue
		}
		// A prefix key applies to a bare specifier or a special-scheme URL only.
		if asURL != nil && !isSpecialScheme(asURL.Scheme) {
			continue
		}
		if addr == "" {
			return "", false, fmt.Errorf("import map blocks specifier %q (invalid address for %q)", normalized, key)
		}
		au, err := url.Parse(addr)
		if err != nil {
			return "", false, err
		}
		ru, err := url.Parse(normalized[len(key):])
		if err != nil {
			return "", false, fmt.Errorf("import map: cannot resolve %q under %q: %w", normalized, key, err)
		}
		out := serializeModuleURL(au.ResolveReference(ru))
		// The remainder must not climb out of the mapped address ("../" backtracking).
		if !strings.HasPrefix(out, addr) {
			return "", false, fmt.Errorf("import map: %q escapes the address mapped for %q", normalized, key)
		}
		return out, true, nil
	}
	return "", false, nil
}

// writeTo feeds a deterministic serialization of the map to w (bundleKey's hash).
func (m *importMap) writeTo(w io.Writer) {
	if m == nil {
		return
	}
	m.imports.writeTo(w)
	for _, sc := range m.scopes {
		_, _ = io.WriteString(w, "\x03"+sc.prefix+"\x03")
		sc.imports.writeTo(w)
	}
}

func (sm *specifierMap) writeTo(w io.Writer) {
	if sm == nil {
		return
	}
	for _, k := range sm.keys {
		_, _ = io.WriteString(w, k+"\x01"+sm.addrs[k]+"\x02")
	}
}

// resolveURLLike implements "resolve a URL-like module specifier": a specifier
// starting with "/", "./" or "../" resolves against base; anything else must parse
// as an absolute URL. nil means spec is a bare specifier (or unresolvable).
func resolveURLLike(spec string, base *url.URL) *url.URL {
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		if base == nil {
			return nil
		}
		u, err := url.Parse(spec)
		if err != nil {
			return nil
		}
		return base.ResolveReference(u)
	}
	u, err := url.Parse(spec)
	if err != nil || !u.IsAbs() {
		return nil
	}
	return u
}

// parseURLWithBase parses s as a URL reference against base (a scope prefix is any
// URL, relative or absolute, unlike a specifier).
func parseURLWithBase(s string, base *url.URL) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.IsAbs() {
		return u, nil
	}
	if base == nil {
		return nil, fmt.Errorf("cannot resolve %q without a base url", s)
	}
	return base.ResolveReference(u), nil
}

// serializeModuleURL is the one serialization every import-map key, address, scope
// prefix, and referrer goes through, so they compare as strings. It gives an
// http(s) origin its "/" path, as the URL Standard serializer does.
func serializeModuleURL(u *url.URL) string {
	if u.Path == "" && u.Host != "" && u.Opaque == "" && (u.Scheme == "http" || u.Scheme == "https") {
		c := *u
		c.Path = "/"
		return c.String()
	}
	return u.String()
}

// isSpecialScheme reports whether scheme is one of the URL Standard's special schemes.
func isSpecialScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ftp", "file", "ws", "wss":
		return true
	}
	return false
}
