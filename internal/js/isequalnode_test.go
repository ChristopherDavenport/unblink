package js_test

import (
	"strings"
	"testing"
)

// isEqualNode compares structure and data, not identity; attribute order does
// not matter. Next.js's head manager calls it on every client-side navigation.
func TestIsEqualNodeAndIsSameNode(t *testing.T) {
	out := renderWith(t, `<html><head><meta name="a" content="1"></head><body>
		<div id="x"><p class="c" title="t">hi <b>there</b></p></div><script>
		function rec(k, v) { document.body.setAttribute('data-' + k, String(v)); }
		var p = document.querySelector('p');
		var clone = p.cloneNode(true);
		rec('clone', p.isEqualNode(clone));
		rec('same', p.isSameNode(p) + '/' + p.isSameNode(clone));
		var reordered = document.createElement('p');
		reordered.setAttribute('title', 't'); reordered.setAttribute('class', 'c');
		reordered.innerHTML = 'hi <b>there</b>';
		rec('order', p.isEqualNode(reordered));
		var shallow = p.cloneNode(false);
		rec('shallow', p.isEqualNode(shallow));
		var attr = p.cloneNode(true); attr.setAttribute('title', 'other');
		rec('attr', p.isEqualNode(attr));
		var extra = p.cloneNode(true); extra.setAttribute('lang', 'en');
		rec('extra', p.isEqualNode(extra));
		var text = p.cloneNode(true); text.firstChild.data = 'bye ';
		rec('text', p.isEqualNode(text));
		rec('null', p.isEqualNode(null));
		rec('type', p.firstChild.isEqualNode(document.createComment('hi ')));
		var m = document.createElement('meta'); m.setAttribute('content', '1'); m.setAttribute('name', 'a');
		rec('meta', document.querySelector('meta').isEqualNode(m));
		</script></body></html>`, nil)
	for _, w := range []string{
		`data-clone="true"`, `data-same="true/false"`, `data-order="true"`, `data-shallow="false"`,
		`data-attr="false"`, `data-extra="false"`, `data-text="false"`, `data-null="false"`,
		`data-type="false"`, `data-meta="true"`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in:\n%s", w, out)
		}
	}
}
