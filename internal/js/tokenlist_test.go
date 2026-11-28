package js_test

import "testing"

func TestDOMTokenList(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"length, index, item, value", `return [c.length, c[0], c[1], c[2], c.item(1), c.item(9), c.value, String(c), 'length' in c].join('|');`,
			"2|a|b||b||a  b a|a  b a|true"},
		{"live against the attribute", `e.setAttribute('class', 'x y z'); var n = c.length; e.removeAttribute('class'); return n + ',' + c.length + ',' + (c === e.classList);`, "3,0,true"},
		{"iteration", `var out = []; for (var k of c) out.push(k); c.forEach(function (v, i) { out.push(i + v); });
			return out.join(',') + '|' + Array.from(c).join('') + '|' + [].slice.call(c).length;`, "a,b,0a,1b|ab|2"},
		{"add / remove / contains", `c.add('q', 'a', 'r'); c.remove('b'); return e.className + '|' + c.contains('q') + c.contains('b');`, "a q r|truefalse"},
		{"toggle with force", `return [c.toggle('a'), c.toggle('a'), c.toggle('a', true), c.toggle('z', false), c.toggle('a', false), e.className].join(',');`,
			"false,true,true,false,false,b"},
		{"replace", `return [c.replace('a', 'n'), c.replace('nope', 'm'), e.className].join(',');`, "true,false,n b"},
		{"value setter", `c.value = 'p q'; return e.className + c.length;`, "p q2"},
		{"empty and spaced tokens throw", `var out = [];
			try { c.add(''); } catch (x) { out.push(x.name); }
			try { c.add('a b'); } catch (x) { out.push(x.name); }
			try { c.supports('a'); } catch (x) { out.push(x instanceof TypeError); }
			return out.join(',');`, "SyntaxError,InvalidCharacterError,true"},
		{"brand", `return Object.prototype.toString.call(c);`, "[object DOMTokenList]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantJS(t, `<p id="x" class="a  b a"></p>`, `var e = document.getElementById('x'), c = e.classList;`+tc.script, tc.want)
		})
	}
}

// link.relList.supports() is how Vite's and Qwik's loaders choose between
// modulepreload and preload; it answers from what the engine acts on.
func TestRelList(t *testing.T) {
	wantJS(t, `<a id="a" rel="noopener nofollow" href="/x"></a>`, `
		var l = document.createElement('link'), a = document.getElementById('a');
		l.setAttribute('rel', 'preload');
		l.relList.add('x');
		return [l.relList.supports('modulepreload'), l.relList.supports('preload'), l.relList.supports('stylesheet'),
			l.relList.supports('prefetch'), l.getAttribute('rel'), a.relList.length, a.relList.contains('nofollow'),
			typeof document.createElement('div').relList].join(',');`,
		"true,true,false,false,preload x,2,true,undefined")
}
