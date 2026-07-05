package js_test

import (
	"strings"
	"testing"
)

// domProbe renders page + a script that writes '|'-joined results into #out and
// returns the #out text.
func domProbe(t *testing.T, script string) string {
	t.Helper()
	out := render(t, `<html><body><div id="out"></div><p id="p">資料xy</p>`+
		`<script>var r=[];(function(){`+script+`})();document.getElementById('out').textContent=r.join('|');</script></body></html>`)
	i := strings.Index(out, `id="out">`)
	j := strings.Index(out[i:], "</div>")
	if i < 0 || j < 0 {
		t.Fatalf("no #out in:\n%s", out)
	}
	return out[i+9 : i+j]
}

// TestCharacterDataMethods locks the UTF-16-correct CharacterData mutation methods
// and their IndexSizeError (a real DOMException).
func TestCharacterDataMethods(t *testing.T) {
	got := domProbe(t, `
	  var t = document.getElementById('p').firstChild;
	  r.push('len:' + t.length);                 // 資料xy = 4 UTF-16 units
	  r.push('sub:' + t.substringData(2, 2));     // xy
	  t.insertData(2, 'AB'); r.push('ins:' + t.data);
	  t.deleteData(0, 2); r.push('del:' + t.data);
	  t.replaceData(0, 2, 'Z'); r.push('rep:' + t.data);
	  t.appendData('!'); r.push('app:' + t.data);
	  t.data = null; r.push('null:[' + t.data + ']'); // [LegacyNullToEmptyString]
	  try { t.substringData(999, 1); } catch (e) { r.push('idx:' + e.name + ',' + e.code + ',' + (e instanceof DOMException)); }`)
	for _, want := range []string{"len:4", "sub:xy", "ins:資料ABxy", "del:ABxy", "rep:Zxy", "app:Zxy!", "null:[]", "idx:IndexSizeError,1,true"} {
		if !strings.Contains(got, want) {
			t.Errorf("CharacterData missing %q in %q", want, got)
		}
	}
}

// TestDOMConformanceBranding locks DOMTokenList branding, createHTMLDocument's
// Document identity + metadata, and the createEvent-before-initEvent InvalidStateError.
func TestDOMConformanceBranding(t *testing.T) {
	got := domProbe(t, `
	  var cl = document.getElementById('p').classList;
	  r.push('tokenlist:' + Object.prototype.toString.call(cl));
	  var doc = document.implementation.createHTMLDocument('T');
	  r.push('isDoc:' + (doc instanceof Document));
	  r.push('meta:' + doc.characterSet + ',' + doc.contentType);
	  var ev = document.createEvent('Event');
	  try { document.body.dispatchEvent(ev); r.push('dispatch:none'); }
	  catch (e) { r.push('dispatch:' + e.name + ',' + (e instanceof DOMException)); }
	  ev.initEvent('x', true, true); r.push('afterInit:' + document.body.dispatchEvent(ev));`)
	for _, want := range []string{
		"tokenlist:[object DOMTokenList]",
		"isDoc:true",
		"meta:UTF-8,text/html",
		"dispatch:InvalidStateError,true",
		"afterInit:true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DOM branding missing %q in %q", want, got)
		}
	}
}
