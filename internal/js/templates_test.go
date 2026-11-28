package js_test

import (
	"strings"
	"testing"
)

// A <template>'s contents are inert: not children of the element, not reachable
// by a selector query or a tree walk, and present in .content from the start —
// including before any script has read .content, which is when a library that
// walks the document first (Alpine) used to find them.
func TestTemplateContentsAreInert(t *testing.T) {
	out := renderWith(t, `<html><body>
	<template id="t"><div class="in"><span>x</span><template id="inner"><i class="deep">y</i></template></div></template>
	<div id="host"></div><div id="out"></div><script>
	var t = document.getElementById('t'), o = [], S = function (a) { return a.map(String).join(','); };
	o.push('kids=' + S([t.firstElementChild, t.firstChild, t.children.length, t.childNodes.length, t.childElementCount, t.hasChildNodes()]));
	o.push('query=' + S([t.querySelector('span'), document.querySelector('template span'), document.querySelectorAll('.in').length, document.getElementsByClassName('in').length, document.getElementsByTagName('span').length, document.getElementById('inner')]));
	var w = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT), seen = false; while (w.nextNode()) { if (w.currentNode.className === 'in') seen = true; }
	o.push('walk=' + seen);
	var c = t.content, d = c.firstElementChild;
	o.push('content=' + S([c.nodeType, c.childNodes.length, d.className, d.parentNode === c, c.querySelector('span') !== null, t.contains(d)]));
	var inner = c.querySelector('#inner');
	o.push('nested=' + S([inner.childNodes.length, inner.content.firstElementChild.className, c.querySelector('.deep')]));
	o.push('html=' + t.innerHTML.replace(/</g, '[').replace(/>/g, ']').replace(/"/g, '~'));
	var deep = t.cloneNode(true), shallow = t.cloneNode(false);
	o.push('clone=' + S([deep.childNodes.length, deep.content.firstElementChild.className, deep.content.firstElementChild !== d, deep.content.querySelector('#inner').content.firstElementChild.className, shallow.content.childNodes.length]));
	document.getElementById('host').appendChild(t.content.cloneNode(true));
	o.push('stamped=' + S([document.querySelectorAll('.in').length, document.querySelector('#host .in span').textContent, document.querySelector('#host template').content.firstElementChild.className]));
	var made = document.createElement('template'); made.innerHTML = '<p class="m">z</p><template><b>q</b></template>';
	o.push('made=' + S([made.childNodes.length, made.content.childNodes.length, made.content.lastChild.content.firstChild.tagName]));
	var div = document.createElement('div'); div.innerHTML = '<template><u>1</u></template>'; div.insertAdjacentHTML('beforeend', '<template><s>2</s></template>');
	o.push('parsed=' + S([div.firstChild.childNodes.length, div.firstChild.content.firstChild.tagName, div.lastChild.childNodes.length, div.lastChild.content.firstChild.tagName, div.querySelector('u')]));
	var pd = new DOMParser().parseFromString('<template id="p"><em>3</em></template>', 'text/html');
	o.push('parser=' + S([pd.querySelector('em'), pd.getElementById('p').content.firstChild.tagName]));
	document.getElementById('out').textContent = o.join(' | ');
	</script></body></html>`, nil)
	for _, want := range []string{
		"kids=null,null,0,0,0,false",
		"query=null,null,0,0,0,null",
		"walk=false",
		"content=11,1,in,true,true,false",
		"nested=0,deep,null",
		"html=[div class=~in~][span]x[/span][template id=~inner~][i class=~deep~]y[/i][/template][/div]",
		"clone=0,in,true,deep,0",
		"stamped=1,x,deep",
		"made=0,2,B",
		"parsed=0,U,0,S,null",
		"parser=null,EM",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("%s", out[i:min(len(out), i+900)])
	}
}

// The failure this fixes, in miniature: a walk that reaches into a template and
// remembers its nodes, followed by a read of .content, must not leave those
// nodes orphaned — the walk never sees them in the first place.
func TestTemplateWalkBeforeContentRead(t *testing.T) {
	out := renderWith(t, `<html><body><div id="scope"><template id="t"><p class="inside">x</p></template></div><div id="out"></div><script>
	var found = [];
	(function walk(el) { found.push(el.tagName); for (var c = el.firstElementChild; c; c = c.nextElementSibling) walk(c); })(document.getElementById('scope'));
	var p = document.getElementById('t').content.firstElementChild;
	var chain = []; for (var n = p; n; n = n.parentNode) chain.push(n.nodeName);
	document.getElementById('out').textContent = 'walk=' + found.join('>') + ' chain=' + chain.join('<');
	</script></body></html>`, nil)
	if !strings.Contains(out, "walk=DIV&gt;TEMPLATE chain=P&lt;#document-fragment") {
		i := strings.Index(out, `<div id="out">`)
		t.Errorf("template contents leaked into a tree walk: %s", out[i:min(len(out), i+300)])
	}
}
