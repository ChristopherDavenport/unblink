package js

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/dop251/goja"
	urlpkg "github.com/nationallibraryofnorway/whatwg-url/url"
	"golang.org/x/net/html"
)

// Bounds on a page-supplied import map. They are far above what real pages ship
// (hundreds of entries, a handful of scopes) and exist so untrusted input cannot
// make parsing or per-import resolution expensive: scope selection is linear in
// the number of scopes, so that one is the tightest.
const (
	maxImportMapBytes   = 4 << 20 // source text of the <script type="importmap">
	maxImportMapEntries = 50_000  // "imports" entries plus every scope's entries
	maxImportMapScopes  = 2_000   // "scopes" entries
)

// An importMap is the parsed, normalized form of the document's
// <script type="importmap">, following the HTML Standard's import map algorithms
// (https://html.spec.whatwg.org/multipage/webappapis.html#import-maps): top-level
// "imports" plus "scopes", which remap a specifier differently for modules whose
// URL falls under a scope prefix. A nil *importMap is valid and maps nothing.
//
// Every URL in it (keys, addresses, scope prefixes) and every specifier and
// referrer it is asked about is parsed and serialized by the WHATWG URL parser the
// page's own URL class uses (urlnative.go), so they compare as strings the way a
// browser compares them: dot segments removed, host lowercased, default port
// dropped, path percent-encoded.
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
	addrs map[string]string
	keys  []string // sorted; only for the deterministic serialization (writeTo)
	// prefixLens holds the length of every trailing-slash key and maxPrefix the
	// longest, so a prefix match probes only the specifier's own "/" positions that
	// some key could end at, never the key set.
	prefixLens map[int]struct{}
	maxPrefix  int
}

// jsonMember is one member of a JSON object, in source order.
type jsonMember struct {
	key string
	val json.RawMessage
}

// decodeJSONObject decodes raw as a JSON object, keeping its members in source
// order (a Go map would lose it, and the import map algorithms are order
// sensitive: of two keys that normalize to the same URL, the later one wins).
// Anything other than an object, including null, is an error.
func decodeJSONObject(raw []byte) ([]jsonMember, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []jsonMember
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, jsonMember{key: key, val: v})
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return out, nil
}

// parseImportMap implements "parse an import map string": text is the source of a
// <script type="importmap"> and base the document base URL its "imports" and
// "scopes" are normalized against. A map that is not a JSON object, or whose
// "imports" / "scopes" / scope value is not an object (null included), is rejected
// whole, where the spec throws a TypeError, as is one over the size bounds above;
// an individual invalid key, address, or scope prefix only drops or nulls that
// entry.
func parseImportMap(text string, base *url.URL) (*importMap, error) {
	if len(text) > maxImportMapBytes {
		return nil, fmt.Errorf("source is larger than %d bytes", maxImportMapBytes)
	}
	top, err := decodeJSONObject([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("top-level value: %w", err)
	}
	var importsRaw, scopesRaw json.RawMessage
	for _, m := range top {
		switch m.key {
		case "imports":
			importsRaw = m.val
		case "scopes":
			scopesRaw = m.val
		}
	}
	wbase := whatwgURL(base)
	entries := 0
	im := &importMap{}
	if importsRaw != nil {
		members, err := decodeJSONObject(importsRaw)
		if err != nil {
			return nil, fmt.Errorf(`"imports": %w`, err)
		}
		if im.imports, err = normalizeSpecifierMap(members, wbase, &entries); err != nil {
			return nil, err
		}
	}
	if scopesRaw != nil {
		scopes, err := decodeJSONObject(scopesRaw)
		if err != nil {
			return nil, fmt.Errorf(`"scopes": %w`, err)
		}
		if len(scopes) > maxImportMapScopes {
			return nil, fmt.Errorf("more than %d scopes", maxImportMapScopes)
		}
		byPrefix := make(map[string]*specifierMap, len(scopes))
		for _, sc := range scopes {
			members, err := decodeJSONObject(sc.val)
			if err != nil {
				return nil, fmt.Errorf("scope %q: %w", sc.key, err)
			}
			sm, err := normalizeSpecifierMap(members, wbase, &entries)
			if err != nil {
				return nil, err
			}
			pu := parseURLWithBase(sc.key, wbase)
			if pu == nil {
				continue // unparseable scope prefix: skip this scope only
			}
			byPrefix[pu.Href(false)] = sm // source order: a later duplicate wins
		}
		for prefix, sm := range byPrefix {
			im.scopes = append(im.scopes, importScope{prefix: prefix, imports: sm})
		}
		sort.Slice(im.scopes, func(i, j int) bool { return im.scopes[i].prefix > im.scopes[j].prefix })
	}
	return im, nil
}

// normalizeSpecifierMap implements "sort and normalize a module specifier map":
// URL-like keys are resolved against base, addresses must be URL-like, and a
// trailing-slash key needs a trailing-slash address. members is walked in source
// order, so of two keys that normalize to the same string the later one wins.
// entries counts members across the whole import map against maxImportMapEntries.
func normalizeSpecifierMap(members []jsonMember, base *urlpkg.Url, entries *int) (*specifierMap, error) {
	if *entries += len(members); *entries > maxImportMapEntries {
		return nil, fmt.Errorf("more than %d entries", maxImportMapEntries)
	}
	sm := &specifierMap{addrs: make(map[string]string, len(members)), prefixLens: make(map[int]struct{})}
	for _, m := range members {
		key := m.key
		if key == "" {
			continue
		}
		if ku := resolveURLLike(key, base); ku != nil {
			key = ku.Href(false)
		}
		addr := ""
		var s string
		if err := json.Unmarshal(m.val, &s); err == nil {
			if au := resolveURLLike(s, base); au != nil {
				addr = au.Href(false)
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
		if strings.HasSuffix(k, "/") {
			sm.prefixLens[len(k)] = struct{}{}
			sm.maxPrefix = max(sm.maxPrefix, len(k))
		}
	}
	sort.Strings(sm.keys)
	return sm, nil
}

// resolve implements "resolve a module specifier": spec as written in the module
// whose base URL is base (the importing module's URL, or the document base for an
// inline module / classic script). Scopes matching base are tried most specific
// first, then top-level imports, then spec itself when it is URL-like.
//
// It runs off the loop goroutine too (the registry's instantiate phase, esbuild's
// resolver), so its loops over page-sized input stop once ctx is cancelled.
func (m *importMap) resolve(ctx context.Context, spec string, base *urlpkg.Url) (string, error) {
	asURL := resolveURLLike(spec, base)
	normalized := spec
	if asURL != nil {
		normalized = asURL.Href(false)
	}
	if m != nil {
		baseStr := ""
		if base != nil {
			baseStr = base.Href(false)
		}
		for i, sc := range m.scopes {
			if i&0xff == 0xff && ctx.Err() != nil {
				return "", ctx.Err()
			}
			if sc.prefix != baseStr && (!strings.HasSuffix(sc.prefix, "/") || !strings.HasPrefix(baseStr, sc.prefix)) {
				continue
			}
			if got, ok, err := sc.imports.match(ctx, normalized, asURL); err != nil || ok {
				return got, err
			}
		}
		if got, ok, err := m.imports.match(ctx, normalized, asURL); err != nil || ok {
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
//
// The exact key is one map lookup. A prefix key must end at a "/" of the specifier,
// so the candidates are the specifier's own prefixes ending in "/", longest first,
// each probed only when some key has that length: the cost depends on the
// specifier, not on how many keys the map holds.
func (sm *specifierMap) match(ctx context.Context, normalized string, asURL *urlpkg.Url) (string, bool, error) {
	if sm == nil {
		return "", false, nil
	}
	if addr, ok := sm.addrs[normalized]; ok {
		if addr == "" {
			return "", false, fmt.Errorf("import map blocks specifier %q (invalid address)", normalized)
		}
		return addr, true, nil
	}
	// A prefix key applies to a bare specifier or a special-scheme URL only.
	if sm.maxPrefix == 0 || (asURL != nil && !asURL.IsSpecialScheme()) {
		return "", false, nil
	}
	// The whole specifier was the exact lookup above, so a prefix key is shorter.
	end := min(len(normalized)-1, sm.maxPrefix)
	for n := 0; end > 0; n++ {
		if n&0xff == 0xff && ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		i := strings.LastIndexByte(normalized[:end], '/')
		if i < 0 {
			break
		}
		end = i
		if _, ok := sm.prefixLens[i+1]; !ok {
			continue
		}
		key := normalized[:i+1]
		addr, ok := sm.addrs[key]
		if !ok {
			continue
		}
		if addr == "" {
			return "", false, fmt.Errorf("import map blocks specifier %q (invalid address for %q)", normalized, key)
		}
		au, err := urlpkg.Parse(addr)
		if err != nil {
			return "", false, err
		}
		ru, err := au.Parse(normalized[len(key):])
		if err != nil {
			return "", false, fmt.Errorf("import map: cannot resolve %q under %q: %w", normalized, key, err)
		}
		out := ru.Href(false)
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
func resolveURLLike(spec string, base *urlpkg.Url) *urlpkg.Url {
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		if base == nil {
			return nil
		}
		u, err := base.Parse(spec)
		if err != nil {
			return nil
		}
		return u
	}
	u, err := urlpkg.Parse(spec)
	if err != nil {
		return nil
	}
	return u
}

// parseURLWithBase parses s as a URL reference against base (a scope prefix is any
// URL, relative or absolute, unlike a specifier). nil means it does not parse.
func parseURLWithBase(s string, base *urlpkg.Url) *urlpkg.Url {
	var (
		u   *urlpkg.Url
		err error
	)
	if base != nil {
		u, err = base.Parse(s)
	} else {
		u, err = urlpkg.Parse(s)
	}
	if err != nil {
		return nil
	}
	return u
}

// whatwgURL re-parses a Go URL with the WHATWG parser. nil in, or a URL that
// parser rejects, gives nil.
func whatwgURL(u *url.URL) *urlpkg.Url {
	if u == nil {
		return nil
	}
	w, err := urlpkg.Parse(u.String())
	if err != nil {
		return nil
	}
	return w
}

// acquireImportMap registers the document's import map, once, at the moment module
// loading begins: the first <script type="module"> pass or the first import(). That
// is the HTML Standard's "import maps allowed" switch. A map a classic script
// inserted before that point is in the document and is honoured; one added after
// is ignored, and the registered map is never parsed again, so a module that has
// already resolved keeps its resolution.
//
// An inline import map is gated by CSP exactly like an inline script (the same
// nonce / hash / 'unsafe-inline' decision, cspAllowInlineElement): a map the policy
// refuses is skipped and recorded, and the next one in document order is
// considered, as in a browser where a blocked map never registers. The first map
// the policy allows is the document's import map even if it then fails to parse.
// An import map with a src attribute is not supported by the platform and is
// skipped.
//
// Loop goroutine only: it records diagnostics and writes b.importMap, which the
// off-loop resolvers read only from goroutines started after this returns.
func (b *bridge) acquireImportMap(doc *html.Node) {
	if b.importMapAcquired {
		return
	}
	b.importMapAcquired = true
	for _, n := range selAnyScript.MatchAll(doc) {
		if !b.isImportMapScript(n) || hasAttr(n, "src") {
			continue
		}
		text := scriptText(n)
		if !b.cspAllowInlineElement(`inline <script type="importmap">`, text, b.nodeNonce(n)) {
			continue
		}
		im, err := parseImportMap(text, b.docBaseNow())
		if err != nil {
			b.recordError(fmt.Errorf("import map ignored: %w", err))
		}
		b.importMap = im
		return
	}
}

// isImportMapScript reports whether a script element's type is "importmap". Like
// scriptSrc, it prefers the JS property: `el.type = 'importmap'` on a created
// element sets a property on the wrapper and does not reflect to the attribute.
func (b *bridge) isImportMapScript(n *html.Node) bool {
	typ := getAttr(n, "type")
	if obj := b.wrap(n).ToObject(b.vm); obj != nil {
		if v := obj.Get("type"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			if s := v.String(); s != "" {
				typ = s
			}
		}
	}
	return strings.EqualFold(typ, "importmap")
}
