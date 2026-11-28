package js_test

import "testing"

// Every element is an instance of its tag's interface.
func TestPerTagInterfaces(t *testing.T) {
	cases := []struct{ tag, iface string }{
		{"div", "HTMLDivElement"}, {"script", "HTMLScriptElement"}, {"a", "HTMLAnchorElement"},
		{"input", "HTMLInputElement"}, {"h3", "HTMLHeadingElement"}, {"td", "HTMLTableCellElement"},
		{"tbody", "HTMLTableSectionElement"}, {"video", "HTMLVideoElement"}, {"template", "HTMLTemplateElement"},
		{"section", "HTMLElement"}, {"em", "HTMLElement"}, {"blink", "HTMLUnknownElement"},
		{"my-widget", "HTMLElement"},
	}
	for _, c := range cases {
		t.Run(c.tag, func(t *testing.T) {
			wantJS(t, ``, `var e = document.createElement('`+c.tag+`');
				return [e instanceof `+c.iface+`, e instanceof HTMLElement, e instanceof Element, e instanceof Node,
					Object.prototype.toString.call(e), e.constructor === `+c.iface+`, e.constructor.name].join(',');`,
				"true,true,true,true,[object "+c.iface+"],true,"+c.iface)
		})
	}
}

func TestInterfaceDetails(t *testing.T) {
	cases := []struct{ name, body, script, want string }{
		{"instanceof is exclusive", ``, `var d = document.createElement('div');
			return [d instanceof HTMLSpanElement, d instanceof HTMLScriptElement, d instanceof HTMLUnknownElement].join(',');`, "false,false,false"},
		{"parsed elements too", `<script id="s" type="text/plain"></script><p id="p"></p>`, `
			return [document.getElementById('s') instanceof HTMLScriptElement, document.getElementById('p') instanceof HTMLParagraphElement,
				document.body instanceof HTMLBodyElement, document.documentElement instanceof HTMLHtmlElement, document.head instanceof HTMLHeadElement].join(',');`,
			"true,true,true,true,true"},
		{"document.currentScript is an HTMLScriptElement", ``, `return document.currentScript instanceof HTMLScriptElement;`, "true"},
		{"media chain", ``, `var v = document.createElement('video'), a = document.createElement('audio');
			return [v instanceof HTMLMediaElement, a instanceof HTMLMediaElement, a instanceof HTMLVideoElement].join(',');`, "true,true,false"},
		{"no Go symbol leaks through constructor.name", ``, `
			var names = [document.createElement('div'), document.createElement('x-y'), document.createTextNode(''), document, document.createDocumentFragment()]
				.map(function (n) { return n.constructor.name; });
			return names.join(',') + '|' + /github|func\d|\(\*/.test(names.join(','));`,
			"HTMLDivElement,HTMLElement,Text,HTMLDocument,DocumentFragment|false"},
		{"toString tags of non-elements", ``, `var s = Object.prototype.toString;
			return [s.call(document), s.call(document.createTextNode('')), s.call(document.createComment('')), s.call(document.createDocumentFragment())].join(',');`,
			"[object HTMLDocument],[object Text],[object Comment],[object DocumentFragment]"},
		{"String(el) and anchors stringify to href", `<a id="l" href="/x?y#z"></a>`, `
			return String(document.createElement('div')) + ',' + String(document.getElementById('l')) + ',' + ('' + document.createElement('a'));`,
			"[object HTMLDivElement],https://example.com/x?y#z,"},
		{"members still live on HTMLElement/Element.prototype", ``, `
			HTMLElement.prototype.__h = function () { return 'h'; }; Element.prototype.__e = function () { return 'e'; };
			var s = document.createElement('span');
			return s.__h() + s.__e() + Object.getPrototypeOf(HTMLSpanElement.prototype).constructor.name + (Object.keys(HTMLSpanElement.prototype).length === 0);`,
			"heHTMLElementtrue"},
		{"svg", `<svg id="g"><circle id="c"/></svg>`, `
			var g = document.getElementById('g'), c = document.getElementById('c');
			return [g instanceof SVGElement, c instanceof SVGElement, g instanceof SVGSVGElement, c instanceof HTMLDivElement, typeof c.style, Object.prototype.toString.call(c)].join(',');`,
			"true,true,true,false,object,[object SVGCircleElement]"},
		{"a plain object is still a plain object", ``, `return Object.prototype.toString.call({});`, "[object Object]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantJS(t, c.body, c.script, c.want) })
	}
}

// Custom elements keep working: the class's own prototype replaces the tag's
// default on upgrade, for parsed, created and constructed instances.
func TestCustomElementsKeepTheirPrototype(t *testing.T) {
	wantJS(t, `<x-up id="p"></x-up>`, `
		class XUp extends HTMLElement { constructor() { super(); this.made = true; } hi() { return 'hi'; } }
		customElements.define('x-up', XUp);
		var parsed = document.getElementById('p'), created = document.createElement('x-up'), made = new XUp();
		return [parsed instanceof XUp, parsed.hi(), parsed.made, created instanceof XUp, made instanceof XUp, made.tagName,
			made instanceof HTMLElement, parsed.constructor.name, Object.prototype.toString.call(parsed)].join(',');`,
		"true,hi,true,true,true,X-UP,true,XUp,[object HTMLElement]")
}
