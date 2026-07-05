package js_test

import (
	"strings"
	"testing"
)

// TestURLSearchParams covers the URLSearchParams shim routers rely on at hydration time
// (its absence was a ReferenceError that aborted mobalytics' routing).
func TestURLSearchParams(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var p = new URLSearchParams('?a=1&b=2&a=3&e=%20x%2By');
		  var parts = [];
		  parts.push('get:' + p.get('a'));
		  parts.push('all:' + p.getAll('a').join(','));
		  parts.push('has:' + p.has('b') + ',' + p.has('z'));
		  parts.push('dec:' + p.get('e'));
		  p.set('a', '9'); parts.push('set:' + p.getAll('a').join(','));
		  p.append('c', 'x');
		  p['delete']('b'); parts.push('del:' + p.has('b'));
		  var keys = []; p.forEach(function (v, k) { keys.push(k); }); parts.push('keys:' + keys.join(''));
		  document.getElementById('out').textContent = parts.join('|');
		</script></body></html>`)
	for _, want := range []string{"get:1", "all:1,3", "has:true,false", "dec: x+y", "set:9", "del:false", "keys:aec"} {
		if !strings.Contains(out, want) {
			t.Errorf("URLSearchParams missing %q:\n%s", want, out)
		}
	}
}

// TestURL covers the URL parser + relative resolution + .searchParams.
func TestURL(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var u = new URL('https://ex.com:8080/a/b?x=1&y=2#frag');
		  var rel = new URL('../c?z=9', 'https://ex.com/a/b/page');
		  document.getElementById('out').textContent =
		    'proto:' + u.protocol + '|host:' + u.host + '|path:' + u.pathname +
		    '|search:' + u.search + '|hash:' + u.hash + '|origin:' + u.origin +
		    '|sp:' + u.searchParams.get('y') + '|rel:' + rel.href + '|relz:' + rel.searchParams.get('z');
		</script></body></html>`)
	for _, want := range []string{
		// & serializes to &amp; in the emitted HTML; the parsed value is ?x=1&y=2.
		"proto:https:", "host:ex.com:8080", "path:/a/b", "search:?x=1&amp;y=2", "hash:#frag",
		"origin:https://ex.com:8080", "sp:2", "rel:https://ex.com/a/c?z=9", "relz:9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("URL missing %q:\n%s", want, out)
		}
	}
}

// TestURLNativeParser covers the behaviors the native WHATWG parser (urlnative.go)
// adds over the former regex shim: reparsing setters, canParse/parse statics, a live
// searchParams link back to url.search, IDNA host normalization, and two-arg
// has/delete on URLSearchParams.
func TestURLNativeParser(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div><script>
	  var parts = [];
	  var u = new URL('https://ex.com/a/b?x=1');
	  u.pathname = '/c/d'; parts.push('setpath:' + u.href);                 // setter reparses href
	  u.host = 'other.com:9'; parts.push('sethost:' + u.host);
	  u.searchParams.append('q', 'v');                                      // live link -> url.search
	  parts.push('splink:' + (u.search.indexOf('q=v') >= 0) + ',' + (u.href.indexOf('q=v') >= 0));
	  parts.push('canParse:' + URL.canParse('https://a.b/') + ',' + URL.canParse('http://'));
	  parts.push('parseNull:' + (URL.parse('http://') === null));
	  parts.push('parseOk:' + URL.parse('https://a.b/').host);
	  parts.push('idna:' + new URL('http://xn--r8jz45g.jp/').hostname);     // stays punycode
	  var sp = new URLSearchParams('a=1&a=2&b=3');
	  parts.push('has2:' + sp.has('a','1') + ',' + sp.has('a','9'));
	  sp['delete']('a','1'); parts.push('del2:' + sp.getAll('a').join(','));
	  parts.push('size:' + new URLSearchParams('a=1&b=2').size);
	  document.getElementById('out').textContent = parts.join('|');
	</script></body></html>`)
	for _, want := range []string{
		"setpath:https://ex.com/c/d?x=1",
		"sethost:other.com:9",
		"splink:true,true",
		"canParse:true,false",
		"parseNull:true",
		"parseOk:a.b",
		"idna:xn--r8jz45g.jp",
		"has2:true,false",
		"del2:2",
		"size:2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("URLNativeParser missing %q:\n%s", want, out)
		}
	}
}
