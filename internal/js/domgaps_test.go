package js_test

import (
	"strings"
	"testing"
)

// Each of these was a script error on a corpus page.
func TestDOMGaps(t *testing.T) {
	expectChecks(t, `<!--lit-node 0--><p id="p" class="a">one</p><div id="d">x<!--c--><span id="s"></span></div>
		<svg id="g" class="icon big"><use href="#x"></use></svg>
		<input id="i" value="hello world"><input id="n" type="number" value="5"><textarea id="ta">abc</textarea>
		<template id="t"><b>1</b></template>`, [][3]string{
		// Lit SSR hydration: marker comment → the element it annotates.
		{"comment.nextElementSibling", "document.body.firstChild.nodeType + ':' + document.body.firstChild.nextElementSibling.id", "8:p"},
		{"text.previousElementSibling", "document.getElementById('d').firstChild.previousElementSibling === null && document.getElementById('d').firstChild.nextElementSibling.id", "s"},
		{"normalize", "(function(){ var d = document.createElement('div'); d.append('a', '', 'b', document.createElement('i'), 'c', 'd'); d.lastChild.previousSibling.previousSibling.append('x', 'y'); d.normalize(); return d.childNodes.length + ':' + d.firstChild.data + ':' + d.lastChild.data + ':' + d.querySelector('i').childNodes.length; })()", "3:ab:cd:1"},
		{"no doctype", "String(document.doctype)", "null"},
		{"createAttribute", "(function(){ var a = document.createAttribute('Data-X'); a.value = 'v'; var p = document.getElementById('p'); p.setAttributeNode(a); return a.name + '=' + p.getAttribute('data-x'); })()", "data-x=v"},
		{"createAttribute invalid", "(function(){ try { document.createAttribute('a b'); return 'ok'; } catch (e) { return e.name; } })()", "InvalidCharacterError"},
		// No layout: the hit test answers from the same model as the rects.
		{"elementFromPoint in viewport", "document.elementFromPoint(10, 10) === document.body", "true"},
		{"elementFromPoint outside", "String(document.elementFromPoint(-1, 10)) + String(document.elementFromPoint(5000, 10))", "nullnull"},
		{"elementsFromPoint", "document.elementsFromPoint(1, 1).map(function (e) { return e.tagName; }).join()", "BODY,HTML"},
		{"hit test agrees with rects", "(function(){ var e = document.elementFromPoint(100, 100), r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; })()", "true"},
		// Text-control selection.
		{"selection default", "(function(){ var i = document.getElementById('i'); return [i.selectionStart, i.selectionEnd, i.selectionDirection].join(); })()", "11,11,none"},
		{"setSelectionRange", "(function(){ var i = document.getElementById('i'); i.setSelectionRange(0, 5, 'forward'); return [i.selectionStart, i.selectionEnd, i.selectionDirection].join(); })()", "0,5,forward"},
		{"select()", "(function(){ var ta = document.getElementById('ta'); ta.select(); return ta.selectionStart + ',' + ta.selectionEnd; })()", "0,3"},
		{"setRangeText", "(function(){ var i = document.getElementById('i'); i.setSelectionRange(0, 5); i.setRangeText('HOWDY!'); return i.value + '|' + i.selectionStart + ',' + i.selectionEnd; })()", "HOWDY! world|0,6"},
		{"selection clamps to a shorter value", "(function(){ var i = document.getElementById('i'); i.setSelectionRange(3, 100); i.value = 'ab'; return i.selectionStart + ',' + i.selectionEnd; })()", "2,2"},
		{"selection on a number input", "(function(){ var n = document.getElementById('n'); try { n.setSelectionRange(0, 1); return 'no throw'; } catch (e) { return String(n.selectionStart) + ':' + e.name; } })()", "null:InvalidStateError"},
		// SVG className is an SVGAnimatedString, as in a browser.
		{"svg className", "(function(){ var g = document.getElementById('g'), c = g.className; return [c instanceof SVGAnimatedString, typeof c, c.baseVal, c.animVal, g.className === c].join(); })()", "true,object,icon big,icon big,true"},
		{"svg className.baseVal write", "(function(){ var g = document.getElementById('g'); g.className.baseVal = 'x'; return g.getAttribute('class') + ':' + g.classList.contains('x'); })()", "x:true"},
		{"html className stays a string", "typeof document.getElementById('p').className", "string"},
		// A template's markup is its content, before and after .content is read.
		{"template innerHTML", "(function(){ var t = document.getElementById('t'); var a = t.innerHTML; t.content; var b = t.innerHTML; t.innerHTML = '<i>2</i><i>3</i>'; return [a, b, t.innerHTML, t.content.childNodes.length, t.childNodes.length].join('|'); })()", "<b>1</b>|<b>1</b>|<i>2</i><i>3</i>|2|0"},
	})
}

func TestDocumentDoctype(t *testing.T) {
	out := render(t, `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd"><html><body><pre id="o"></pre><script>
		var dt = document.doctype, r;
		try {
			r = [dt instanceof DocumentType, dt instanceof Node, dt.nodeType, dt.name, dt.publicId, dt.systemId, document.firstChild === dt, Object.prototype.toString.call(dt), dt.nextSibling === document.documentElement].join();
		} catch (e) { r = 'ERR ' + e; }
		document.getElementById('o').textContent = r;
	</script></body></html>`)
	want := `<pre id="o">true,true,10,html,-//W3C//DTD XHTML 1.0 Strict//EN,http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd,true,[object DocumentType],true</pre>`
	if !strings.Contains(out, want) {
		t.Errorf("document.doctype:\n%s", out)
	}
}

// The URL members belong to the elements that have them. On every element, the
// read-only ones made an ordinary component field name unassignable.
func TestURLMembersAreOnTheirInterfaces(t *testing.T) {
	expectChecks(t, `<a id="a" href="/p/a?q=1#h">a</a><div id="d"></div><img id="i" src="/x.png"><link id="l" rel="preload" href="/s.css"><form id="f" rel="noopener"></form>`, [][3]string{
		// A custom element keeping its own search/host/hash/origin (redux.js.org's
		// search component did; class bodies are strict, so this used to throw).
		{"component fields", "(function(){ 'use strict'; class C extends HTMLElement { constructor() { super(); this.search = function () { return 's'; }; this.host = 'h'; this.hash = 1; this.origin = 'o'; this.port = 2; this.protocol = 'p'; this.pathname = '/x'; this.hostname = 'hn'; } } customElements.define('url-fields', C); var e = document.createElement('url-fields'); return [e.search(), e.host, e.hash, e.origin, e.port, e.protocol, e.pathname, e.hostname].join(); })()", "s,h,1,o,2,p,/x,hn"},
		{"not on a div", "(function(){ var d = document.getElementById('d'); return ['href', 'src', 'search', 'host', 'origin', 'relList'].map(function (k) { return k in d; }).join(); })()", "false,false,false,false,false,false"},
		{"anchor components", "(function(){ var a = document.getElementById('a'); return [a.href, a.protocol, a.host, a.hostname, a.port, a.pathname, a.search, a.hash, a.origin].join(' '); })()", "https://example.com/p/a?q=1#h https: example.com example.com  /p/a ?q=1 #h https://example.com"},
		{"anchor as URL parser", "(function(){ var a = document.createElement('a'); a.href = 'https://h.test:8443/a/b?x=1#f'; return [a.host, a.port, a.pathname, a.search, a.hash].join(' '); })()", "h.test:8443 8443 /a/b ?x=1 #f"},
		{"component setters", "(function(){ var a = document.createElement('a'); a.href = 'https://h.test/a?x=1#f'; a.pathname = '/z'; a.search = '?y=2'; a.hash = 'g'; a.hostname = 'k.test'; a.port = '81'; a.protocol = 'http:'; return a.getAttribute('href'); })()", "http://k.test:81/z?y=2#g"},
		{"origin is read-only", "(function(){ 'use strict'; var a = document.getElementById('a'); try { a.origin = 'x'; return 'assigned'; } catch (e) { return e.name; } })()", "TypeError"},
		{"src", "document.getElementById('i').src + ' ' + ('src' in document.createElement('script')) + ('src' in document.createElement('iframe')) + ('src' in document.createElement('video'))", "https://example.com/x.png truetruetrue"},
		{"link.href and relList", "(function(){ var l = document.getElementById('l'); return l.href + ' ' + l.relList.contains('preload') + l.relList.supports('modulepreload') + l.relList.supports('stylesheet'); })()", "https://example.com/s.css truetruefalse"},
		{"form.relList", "document.getElementById('f').relList.contains('noopener')", "true"},
		{"created link is a link extraction sees", "(function(){ var a = document.createElement('a'); a.href = '/made'; document.body.appendChild(a); return document.body.lastChild.getAttribute('href'); })()", "/made"},
	})
}

func TestIntlLegacyCallsAndBoundFormat(t *testing.T) {
	expectChecks(t, "", [][3]string{
		// nuxt.com: Intl.NumberFormat(...) with no `new`.
		{"NumberFormat without new", "(function(){ var f = Intl.NumberFormat('en-GB', {notation: 'compact', maximumFractionDigits: 1}); return (f instanceof Intl.NumberFormat) + ':' + typeof f.format(1200); })()", "true:string"},
		{"DateTimeFormat without new", "Intl.DateTimeFormat('en-US') instanceof Intl.DateTimeFormat", "true"},
		{"Collator without new", "Intl.Collator('en').compare('a', 'b')", "-1"},
		{"detached format", "[1234, 5].map(new Intl.NumberFormat('en-US').format).join('|')", "1,234|5"},
		{"detached compare", "['b', 'a', 'c'].sort(new Intl.Collator('en').compare).join('')", "abc"},
		{"format is stable", "(function(){ var f = new Intl.NumberFormat('en-US'); return f.format === f.format; })()", "true"},
		{"toLocaleString still works", "(6574).toLocaleString('en-US')", "6,574"},
	})
}

// No CSSOM is built, but the (empty) sheet list is a list.
func TestStyleSheetsIsAnIterableEmptyList(t *testing.T) {
	expectChecks(t, `<style>p{}</style>`, [][3]string{
		{"iterable", "(function(){ var n = 0; for (var s of document.styleSheets) n++; return n + ':' + document.styleSheets.length + ':' + document.styleSheets.item(0) + ':' + [...document.styleSheets].length; })()", "0:0:null:0"},
	})
}

func TestNavigatorLegacyMembers(t *testing.T) {
	expectChecks(t, "", [][3]string{
		// open-wc.org: navigator.appVersion.indexOf("Mac") at module top level.
		{"appVersion", "navigator.appVersion.indexOf('Mac') + ':' + (navigator.userAgent === 'Mozilla/' + navigator.appVersion)", "-1:true"},
		{"frozen values", "[navigator.appName, navigator.appCodeName, navigator.product, navigator.productSub, navigator.vendorSub].join('|')", "Netscape|Mozilla|Gecko|20030107|"},
		{"no plugins", "[navigator.plugins.length, navigator.mimeTypes.length, navigator.plugins.namedItem('x'), navigator.javaEnabled(), navigator.pdfViewerEnabled].join()", "0,0,,false,false"},
	})
}

func TestIntlListFormatLocaleSegmenter(t *testing.T) {
	expectChecks(t, "", [][3]string{
		// tanstack.com: new Intl.ListFormat("en-US", {style: "long", type: "conjunction"}) at module top level.
		{"conjunction", "['a','b','c'].map(function(_, i, all){ return new Intl.ListFormat('en-US', {style: 'long', type: 'conjunction'}).format(all.slice(0, i + 1)); }).join(' / ')", "a / a and b / a, b, and c"},
		{"disjunction", "new Intl.ListFormat('en', {type: 'disjunction'}).format(['x', 'y', 'z'])", "x, y, or z"},
		{"unit narrow", "new Intl.ListFormat('en', {type: 'unit', style: 'narrow'}).format(['1h', '2m'])", "1h 2m"},
		{"short", "new Intl.ListFormat('en', {style: 'short'}).format(['a', 'b', 'c'])", "a, b, & c"},
		{"parts", "new Intl.ListFormat('en').formatToParts(['a', 'b']).map(function (p) { return p.type; }).join()", "element,literal,element"},
		{"empty and iterable", "new Intl.ListFormat('en').format([]) + '|' + new Intl.ListFormat('en').format(new Set(['q']))", "|q"},
		{"requires new", "(function(){ try { Intl.ListFormat('en'); return 'ok'; } catch (e) { return e.name; } })()", "TypeError"},
		{"Locale", "(function(){ var l = new Intl.Locale('zh-hant-tw'); return [l.language, l.script, l.region, l.baseName, String(l), new Intl.Locale('en').region].join(); })()", "zh,Hant,TW,zh-Hant-TW,zh-Hant-TW,"},
		{"Locale invalid", "(function(){ try { new Intl.Locale('!'); return 'ok'; } catch (e) { return e.name; } })()", "RangeError"},
		{"DisplayNames", "new Intl.DisplayNames('en', {type: 'region'}).of('US') + '|' + String(new Intl.DisplayNames('en', {type: 'region', fallback: 'none'}).of('US'))", "US|undefined"},
		{"Segmenter words", "Array.from(new Intl.Segmenter('en', {granularity: 'word'}).segment(\"Don't stop, go!\")).filter(function (s) { return s.isWordLike; }).map(function (s) { return s.segment; }).join('|')", "Don't|stop|go"},
		{"Segmenter graphemes", "Array.from(new Intl.Segmenter().segment('ae\\u0301z')).map(function (s) { return s.segment.length; }).join()", "1,2,1"},
		{"Segmenter sentences", "Array.from(new Intl.Segmenter('en', {granularity: 'sentence'}).segment('One. Two! Three')).length", "3"},
		{"Segmenter containing", "new Intl.Segmenter('en', {granularity: 'word'}).segment('hi there').containing(4).segment", "there"},
	})
}
