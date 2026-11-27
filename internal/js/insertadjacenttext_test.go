package js_test

import (
	"strings"
	"testing"
)

// insertAdjacentText inserts a Text node at each of the four positions, never
// parses its argument as markup, and rejects an unknown position.
func TestInsertAdjacentText(t *testing.T) {
	out := renderWith(t, `<html><body><div id="d"><p id="p">mid</p></div><script>
		var p = document.getElementById('p'), d = document.getElementById('d');
		p.insertAdjacentText('beforebegin', 'A');
		p.insertAdjacentText('afterbegin', 'B');
		p.insertAdjacentText('beforeend', 'C');
		p.insertAdjacentText('AfterEnd', '<b>D</b>');
		var err = 'none';
		try { p.insertAdjacentText('sideways', 'x'); } catch (e) { err = e.name; }
		var detached = document.createElement('span');
		detached.insertAdjacentText('beforebegin', 'ignored');
		document.body.setAttribute('data-text', d.textContent);
		document.body.setAttribute('data-kids', d.childNodes.length + '/' + d.children.length);
		document.body.setAttribute('data-err', err);
		</script></body></html>`, nil)
	for _, w := range []string{`data-text="ABmidC&lt;b&gt;D&lt;/b&gt;"`, `data-kids="3/1"`, `data-err="SyntaxError"`} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in:\n%s", w, out)
		}
	}
}
