package js_test

import (
	"strings"
	"testing"
)

// TestDocumentTitleSetter proves document.title assignment lands in the live
// <title> element (visible to extraction) and reads back, creating the element
// when the page has none.
func TestDocumentTitleSetter(t *testing.T) {
	out := render(t, `<html><head></head><body><div id="out"></div>
		<script>
		  var before = document.title;
		  document.title = 'SPA Route Title';
		  document.getElementById('out').textContent = 'before=[' + before + '] after=' + document.title;
		</script></body></html>`)
	if !strings.Contains(out, "before=[] after=SPA Route Title") {
		t.Errorf("title getter/setter wrong:\n%s", out)
	}
	if !strings.Contains(out, "<title>SPA Route Title</title>") {
		t.Errorf("title assignment did not create/fill the <title> element:\n%s", out)
	}
}

// TestDocumentURLProps proves the document-address surface SPAs and analytics
// snippets read: URL/documentURI, location alias, referrer, charset, compatMode.
func TestDocumentURLProps(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  document.getElementById('out').textContent = [
		    'url=' + document.URL,
		    'uri=' + document.documentURI,
		    'loc=' + (document.location === window.location),
		    'path=' + document.location.pathname,
		    'ref=[' + document.referrer + ']',
		    'cs=' + document.characterSet + '/' + document.charset,
		    'compat=' + document.compatMode,
		    'ct=' + document.contentType
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{
		"url=https://example.com/page", "uri=https://example.com/page",
		"loc=true", "path=/page", "ref=[]", "cs=UTF-8/UTF-8", "compat=CSS1Compat", "ct=text/html",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("document props missing %q:\n%s", want, out)
		}
	}
}

// TestDocumentCurrentScript proves currentScript points at the executing
// classic <script> element (and is null after scripts finish, checked from a
// timeout callback).
func TestDocumentCurrentScript(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script id="me" data-cfg="from-attr">
		  var cur = document.currentScript;
		  var during = cur && cur.id === 'me' && cur.getAttribute('data-cfg') === 'from-attr';
		  setTimeout(function () {
		    document.getElementById('out').textContent =
		      'during=' + during + ' after=' + (document.currentScript === null);
		  }, 0);
		</script></body></html>`)
	for _, want := range []string{"during=true", "after=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("currentScript missing %q:\n%s", want, out)
		}
	}
}

// TestGetElementsByName proves the name-attribute lookup (radio groups, legacy
// form scripting) without selector-escaping pitfalls.
func TestGetElementsByName(t *testing.T) {
	out := render(t, `<html><body>
		<input name="opt" value="a"><input name="opt" value="b"><input name="other" value="c">
		<div id="out"></div>
		<script>
		  var got = document.getElementsByName('opt');
		  document.getElementById('out').textContent =
		    'n=' + got.length + ' v=' + got[0].getAttribute('value') + got[1].getAttribute('value') +
		    ' none=' + document.getElementsByName('missing').length;
		</script></body></html>`)
	for _, want := range []string{"n=2", "v=ab", "none=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("getElementsByName missing %q:\n%s", want, out)
		}
	}
}

// TestDOMParser proves parseFromString returns an inert queryable document
// (jQuery.parseHTML / sanitizer path) that never touches the live page.
func TestDOMParser(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var doc = new DOMParser().parseFromString(
		    '<html><head><title>inert</title></head><body><p class="x">parsed <b>rich</b></p></body></html>',
		    'text/html');
		  var p = doc.querySelector('p.x');
		  var live = document.querySelector('p.x'); // must NOT leak into the page
		  document.getElementById('out').textContent =
		    'title=' + doc.title + ' text=' + p.textContent + ' live=' + (live === null) +
		    ' frag=' + (doc.body.innerHTML.indexOf('<b>rich</b>') >= 0);
		</script></body></html>`)
	for _, want := range []string{"title=inert", "text=parsed rich", "live=true", "frag=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("DOMParser missing %q:\n%s", want, out)
		}
	}
}

// TestXMLSerializerAndRange proves serializeToString round-trips an element,
// and Range.createContextualFragment really parses markup (the template-engine
// path) while the rest of Range/Selection is inert but present.
func TestXMLSerializerAndRange(t *testing.T) {
	out := render(t, `<html><body><div id="host"><em>x</em></div><div id="out"></div>
		<script>
		  var ser = new XMLSerializer().serializeToString(document.getElementById('host'));
		  var range = document.createRange();
		  var frag = range.createContextualFragment('<span id="from-range">made</span>');
		  document.getElementById('host').appendChild(frag);
		  var sel = window.getSelection();
		  var selOK = sel.rangeCount === 0 && sel.isCollapsed && sel.toString() === '' && document.getSelection() === sel;
		  var rectOK = range.getBoundingClientRect().width === 0 && range.getClientRects().length === 0;
		  document.getElementById('out').textContent =
		    'ser=' + (ser.indexOf('<em>x</em>') >= 0) +
		    ' made=' + (document.getElementById('from-range') !== null) +
		    ' sel=' + selOK + ' rect=' + rectOK;
		</script></body></html>`)
	for _, want := range []string{"ser=true", "made=true", "sel=true", "rect=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("XMLSerializer/Range missing %q:\n%s", want, out)
		}
	}
}

// TestCreateEventLegacy proves the deprecated document.createEvent + initEvent
// path still drives the real dispatcher (old libraries use it).
func TestCreateEventLegacy(t *testing.T) {
	out := render(t, `<html><body><button id="b">go</button><div id="out"></div>
		<script>
		  var fired = false;
		  document.getElementById('b').addEventListener('legacy-ping', function () { fired = true; });
		  var ev = document.createEvent('Event');
		  ev.initEvent('legacy-ping', true, true);
		  document.getElementById('b').dispatchEvent(ev);
		  document.getElementById('out').textContent = 'fired=' + fired;
		</script></body></html>`)
	if !strings.Contains(out, "fired=true") {
		t.Errorf("createEvent/initEvent dispatch failed:\n%s", out)
	}
}
