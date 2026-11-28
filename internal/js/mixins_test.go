package js_test

import "testing"

// ParentNode on every interface that includes it: Element, DocumentFragment
// (template.content, createDocumentFragment, Range fragments, new
// DocumentFragment), ShadowRoot, and Document objects from DOMParser /
// createHTMLDocument.
func TestParentNodeMixinEverywhere(t *testing.T) {
	probe := `function has(o){ return ['querySelector','querySelectorAll','append','prepend','replaceChildren'].map(function(m){ return typeof o[m] === 'function' ? 1 : 0; }).join('') + (('children' in o) ? 1 : 0) + (('firstElementChild' in o) ? 1 : 0) + (('childElementCount' in o) ? 1 : 0); }`
	cases := []struct{ name, expr string }{
		{"element", `document.createElement('div')`},
		{"createDocumentFragment", `document.createDocumentFragment()`},
		{"new DocumentFragment", `new DocumentFragment()`},
		{"template.content", `document.getElementById('t').content`},
		{"range fragment", `document.createRange().createContextualFragment('<i>a</i>')`},
		{"shadow root", `document.createElement('div').attachShadow({mode:'open'})`},
		{"document", `document`},
		{"DOMParser document", `new DOMParser().parseFromString('<p>a</p>', 'text/html')`},
		{"createHTMLDocument", `document.implementation.createHTMLDocument('x')`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, `<template id="t"><b>x</b></template>`, probe+`return has(`+c.expr+`);`, "11111111")
		})
	}
}

func TestFragmentQueriesAndMutation(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"template.content query and clone", `
			var c = document.getElementById('t').content;
			var k = c.cloneNode(true);
			return [c.querySelector('b').textContent, c.querySelectorAll('b,i').length, c.children.length,
				c.firstElementChild.tagName, c.lastElementChild.tagName, c.childElementCount,
				k.querySelector('i').textContent, c.getElementById('x').tagName].join(',');`, "x,2,2,B,I,2,y,I"},
		{"fragment append mixes nodes and strings", `
			var f = document.createDocumentFragment(), e = document.createElement('em');
			f.append('a', e, 'b'); f.prepend('0');
			var d = document.createElement('div'); d.append(f);
			return d.innerHTML + '|' + f.childNodes.length;`, "0a<em></em>b|0"},
		{"replaceChildren may reuse a current child", `
			var d = document.createElement('div'); d.innerHTML = '<a></a><b></b><i></i>';
			var keep = d.querySelector('b');
			d.replaceChildren(keep, 'z');
			return d.innerHTML;`, "<b></b>z"},
		{"replaceChildren with nothing empties", `
			var d = document.createElement('div'); d.innerHTML = '<a></a>x';
			d.replaceChildren(); return d.childNodes.length;`, "0"},
		{"new DocumentFragment is a real node", `
			var f = new DocumentFragment(); f.appendChild(document.createElement('u'));
			return [f.nodeType, f instanceof DocumentFragment, f.firstChild.tagName, f.childNodes.length].join(',');`, "11,true,U,1"},
		{"new Text / new Comment", `
			var t = new Text('hi'), c = new Comment('note'), d = document.createElement('div');
			d.append(t, c);
			return [t.nodeType, t instanceof Text, c.nodeType, d.innerHTML].join(',');`, "3,true,8,hi<!--note-->"},
		{"range fragment is queryable", `
			var f = document.createRange().createContextualFragment('<p class="k">a</p><p>b</p>');
			return f.querySelector('.k').textContent + f.querySelectorAll('p').length;`, "a2"},
		{"shadow root is a fragment, not an element", `
			var h = document.createElement('div'); document.body.appendChild(h);
			var s = h.attachShadow({mode:'open'});
			s.innerHTML = '<span id="in">S</span>';
			s.append(document.createElement('hr'));
			return [s instanceof ShadowRoot, s instanceof DocumentFragment, s instanceof Element, s.nodeType,
				s.getElementById('in').textContent, s.querySelector('hr') !== null, s.children.length,
				s.host === h, typeof s.getAttribute, Object.prototype.toString.call(s)].join(',');`,
			"true,true,false,11,S,true,2,true,undefined,[object ShadowRoot]"},
		{"parsed document is a queryable node tree", `
			var d = new DOMParser().parseFromString('<title>T</title><ul><li>a</li><li>b</li></ul>', 'text/html');
			var li = d.getElementsByTagName('li');
			return [li.length, d.children.length, d.firstChild === d.documentElement, d.childNodes.length,
				d.documentElement.parentNode === d, d.contains(li[0]), d.body.firstElementChild.tagName,
				d instanceof Document].join(',');`, "2,1,true,1,true,true,UL,true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, `<template id="t"><b>x</b><i id="x">y</i></template>`, c.script, c.want)
		})
	}
}

// ChildNode on Element and CharacterData (Text, Comment).
func TestChildNodeMixin(t *testing.T) {
	setup := `var d = document.createElement('div'); d.innerHTML = '<a></a>t<b></b>'; var a = d.firstChild, tx = a.nextSibling, b = d.lastChild;`
	cases := []struct{ name, script, want string }{
		{"text.remove", `tx.remove(); return d.innerHTML;`, "<a></a><b></b>"},
		{"comment.remove", `var c = document.createComment('c'); d.appendChild(c); c.remove(); return d.innerHTML;`, "<a></a>t<b></b>"},
		{"text.before/after", `tx.before('1', document.createElement('i')); tx.after('2'); return d.innerHTML;`, "<a></a>1<i></i>t2<b></b>"},
		{"element.before moving a sibling", `b.before(a); return d.innerHTML;`, "t<a></a><b></b>"},
		{"after with the next sibling in the set", `a.after(b, tx); return d.innerHTML;`, "<a></a><b></b>t"},
		{"replaceWith several", `tx.replaceWith(document.createElement('u'), 'x'); return d.innerHTML;`, "<a></a><u></u>x<b></b>"},
		{"replaceWith nothing removes", `a.replaceWith(); return d.innerHTML;`, "t<b></b>"},
		{"replaceWith a fragment", `var f = document.createDocumentFragment(); f.append('p', 'q'); b.replaceWith(f); return d.innerHTML;`, "<a></a>tpq"},
		{"detached node is a no-op", `var x = document.createElement('p'); x.before('z'); x.after('z'); x.remove(); return x.parentNode === null;`, "true"},
		{"fragments have no ChildNode", `return typeof document.createDocumentFragment().remove;`, "undefined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantJS(t, ``, setup+c.script, c.want) })
	}
}

// The mixin mutations are real tree mutations: observers see them and inserted
// custom elements connect.
func TestMixinMutationsAreObserved(t *testing.T) {
	wantJS(t, `<div id="h"><p id="p"></p></div>`, `
		var n = 0, connected = 0;
		customElements.define('x-m', class extends HTMLElement { connectedCallback() { connected++; } });
		var mo = new MutationObserver(function (recs) { n += recs.length; done(n > 0 ? 'seen,' + connected : 'none'); });
		mo.observe(document.getElementById('h'), { childList: true, subtree: true });
		document.getElementById('p').after(document.createElement('x-m'));
		document.getElementById('p').remove();`, "seen,1")
}

func TestAttrNodes(t *testing.T) {
	wantJS(t, `<a id="x" href="/h" data-k="v"></a>`, `
		var e = document.getElementById('x');
		var n = e.getAttributeNode('data-k');
		var out = [n.name, n.value, n.ownerElement === e, e.getAttributeNode('nope')];
		n.value = 'w'; out.push(e.getAttribute('data-k'));
		e.removeAttributeNode(n); out.push(e.hasAttribute('data-k'));
		var threw = false; try { e.removeAttributeNode(n); } catch (x) { threw = x.name; }
		out.push(threw);
		return out.join(',');`, "data-k,v,true,,w,false,NotFoundError")
}

// el.attributes is live: React's singleton reset loops
// `while (attrs.length) el.removeAttributeNode(attrs[0])`.
func TestNamedNodeMapIsLive(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"drain by removeAttributeNode", `
			var a = e.attributes, n = 0;
			for (; a.length; ) { e.removeAttributeNode(a[0]); if (++n > 10) return 'runaway'; }
			return n + ',' + e.attributes.length + ',' + e.outerHTML;`, "3,0,<p></p>"},
		{"sees later changes", `
			var a = e.attributes; e.setAttribute('z', '1'); e.removeAttribute('id');
			return [a.length, a[0].name, a.item(2).name, a.getNamedItem('z').value, a.getNamedItem('id'), a.z.value, a === e.attributes, a[0] === a[0]].join(',');`,
			"3,class,z,1,,1,true,true"},
		{"iterable and array-like", `
			var names = []; for (var x of e.attributes) names.push(x.name);
			return names.join(',') + '|' + Array.prototype.map.call(e.attributes, function (x) { return x.value; }).join(',') + '|' + Array.from(e.attributes).length;`,
			"id,class,data-k|x,c,v|3"},
		{"named mutation", `
			var a = e.attributes; a.removeNamedItem('class');
			var at = document.createAttribute ? null : null;
			return e.hasAttribute('class') + ',' + a.length;`, "false,2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, `<p id="x" class="c" data-k="v"></p>`, `var e = document.getElementById('x');`+c.script, c.want)
		})
	}
}

func TestElementScrollMethodsExist(t *testing.T) {
	wantJS(t, `<div id="x"></div>`, `var e = document.getElementById('x');
		e.scrollTo(0, 10); e.scrollTo({ top: 5 }); e.scroll(0, 0); e.scrollBy(1, 1); e.scrollIntoView();
		return e.scrollTop;`, "0")
}

// textContent by node kind: character data reads and writes its own data.
func TestTextContentByNodeKind(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"comment reads its data", `return document.body.firstChild.nodeType + ':' + document.body.firstChild.textContent;`, "8:nghm"},
		{"text node write changes the text", `var t = document.getElementById('p').firstChild; t.textContent = 'new';
			return [t.data, t.childNodes.length, document.getElementById('p').innerHTML].join('|');`, "new|0|new"},
		{"comment write", `var c = document.body.firstChild; c.textContent = 'x'; return c.data + c.childNodes.length;`, "x0"},
		{"element write of '' leaves no child", `var p = document.getElementById('p'); p.textContent = ''; return p.childNodes.length;`, "0"},
		{"element write of null empties", `var p = document.getElementById('p'); p.textContent = null; return p.childNodes.length + p.innerHTML;`, "0"},
		{"document is null", `return String(document.textContent);`, "null"},
		{"text write is a characterData mutation", `var t = document.getElementById('p').firstChild;
			new MutationObserver(function (r) { done(r[0].type); }).observe(t, { characterData: true }); t.textContent = 'z';`, "characterData"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantJS(t, `<!--nghm--><p id="p">old</p>`, c.script, c.want) })
	}
}

func TestNodeIterator(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"comments in document order", `
			var it = document.createNodeIterator(document.body, NodeFilter.SHOW_COMMENT), out = [], n;
			while ((n = it.nextNode())) out.push(n.data);
			return out.join(',');`, "a,b,c"},
		{"root is included; next then previous returns the same node", `
			var r = document.getElementById('r'), it = document.createNodeIterator(r, NodeFilter.SHOW_ELEMENT);
			var a = it.nextNode(), b = it.nextNode(), c = it.previousNode(), d = it.previousNode(), e = it.previousNode();
			return [a === r, b.tagName, c === b, d === r, e, it.referenceNode === r, it.pointerBeforeReferenceNode].join(',');`,
			"true,P,true,true,,true,true"},
		{"filter function and object", `
			var f = function (n) { return n.data === 'b' ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT; };
			var one = document.createNodeIterator(document.body, NodeFilter.SHOW_COMMENT, f), two = document.createNodeIterator(document.body, NodeFilter.SHOW_COMMENT, { acceptNode: f });
			return one.nextNode().data + one.nextNode().data + one.nextNode() + two.nextNode().data + (one.root === document.body);`, "acnullatrue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, `<!--a--><div id="r"><p>x<!--b--></p><i></i></div><!--c-->`, c.script, c.want)
		})
	}
}
