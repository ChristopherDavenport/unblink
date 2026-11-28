package js_test

import (
	"strings"
	"testing"
)

func TestDialogShowAndClose(t *testing.T) {
	doc, got := reflectOut(t, `<html><body>
		<dialog id="d"><p>dialog body</p></dialog><dialog id="k"><p>kept open</p></dialog>
		<div id="out"></div><script>
		var d = document.getElementById('d'), k = document.getElementById('k'), r = [];
		r.push(d.open, document.body.innerText.indexOf('dialog body') < 0);
		d.showModal(); r.push(d.open, d.hasAttribute('open'), document.body.innerText.indexOf('dialog body') >= 0);
		try { d.showModal(); r.push('no-throw'); } catch (e) { r.push(e.name); }
		d.show(); // already open: a no-op
		d.addEventListener('close', function () {
			r.push('close:' + d.returnValue + ':' + d.open);
			document.getElementById('out').textContent = r.join(',');
		});
		d.close('done'); r.push(d.open);
		d.close(); // already closed: no second event
		k.show();
	</script></body></html>`)
	if got != "false,true,true,true,true,InvalidStateError,false,close:done:false" {
		t.Errorf("got %s", got)
	}
	if !strings.Contains(doc, `<dialog id="k" open="">`) || strings.Contains(doc, `<dialog id="d" open`) {
		t.Errorf("open attributes wrong:\n%s", doc)
	}
}
