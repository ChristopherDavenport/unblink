package js_test

import (
	"strings"
	"testing"
)

// reflectOut renders page (which writes its result into #out) and returns the
// serialized document plus #out's text.
func reflectOut(t *testing.T, page string) (string, string) {
	t.Helper()
	out := render(t, page)
	i := strings.Index(out, `<div id="out">`)
	if i < 0 {
		t.Fatalf("no #out in:\n%s", out)
	}
	rest := out[i+len(`<div id="out">`):]
	return out, rest[:strings.Index(rest, "</div>")]
}

// A property write must land in the tree, where extraction reads it.
func TestReflectedWritesReachTheTree(t *testing.T) {
	doc, _ := reflectOut(t, `<html><body><div id="out"></div><script>
		var a = document.createElement('a'); a.id = 'a';
		a.title = 'T'; a.target = '_blank'; a.rel = 'noopener'; a.hidden = true; a.tabIndex = 3; a.lang = 'fr';
		a.ariaLabel = 'Close'; a.role = 'button'; a.ariaExpanded = 'false';
		document.body.appendChild(a);
		var i = document.createElement('input'); i.id = 'i';
		i.type = 'checkbox'; i.name = 'agree'; i.required = true; i.disabled = true; i.placeholder = 'p';
		i.maxLength = 12; i.defaultChecked = true;
		document.body.appendChild(i);
		var l = document.createElement('label'); l.id = 'l'; l.htmlFor = 'i'; document.body.appendChild(l);
		var m = document.createElement('meta'); m.name = 'description'; m.content = 'C'; m.httpEquiv = 'x'; document.head.appendChild(m);
		var img = document.createElement('img'); img.id = 'g'; img.alt = 'Alt text'; img.width = 40; img.loading = 'lazy'; document.body.appendChild(img);
		var d = document.createElement('details'); d.id = 'd'; d.open = true; document.body.appendChild(d);
		var td = document.createElement('td'); td.colSpan = 2;
		var f = document.createElement('form'); f.id = 'f'; f.method = 'POST'; f.action = '/go'; f.noValidate = true; f.acceptCharset = 'utf-8';
		document.body.appendChild(f);
	</script></body></html>`)
	for _, want := range []string{
		`<a id="a" title="T" target="_blank" rel="noopener" hidden="" tabindex="3" lang="fr" aria-label="Close" role="button" aria-expanded="false">`,
		`<input id="i" type="checkbox" name="agree" required="" disabled="" placeholder="p" maxlength="12" checked=""/>`,
		`<label id="l" for="i">`,
		`<meta name="description" content="C" http-equiv="x"/>`,
		`<img id="g" alt="Alt text" width="40" loading="lazy"/>`,
		`<details id="d" open="">`,
		`<form id="f" method="POST" action="/go" novalidate="" accept-charset="utf-8">`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in:\n%s", want, doc)
		}
	}
}

func TestReflectedReadsFollowTheRules(t *testing.T) {
	_, got := reflectOut(t, `<html><body>
		<a id="a" href="/x">l</a><input id="i" type="BOGUS" maxlength="x" size="-3"><input id="c" type="CheckBox">
		<img id="g" crossorigin="x" loading="nope"><form id="f" method="PUT"><button id="b">s</button></form>
		<div id="d" hidden="until-found" dir="RTL" contenteditable></div><span id="s" draggable="true"></span>
		<td id="t"></td><ol id="o"></ol><textarea id="ta">hello</textarea>
		<select id="sel"><option id="o1">  One  two </option><option id="o2" value="v2" selected>Two</option></select>
		<div id="out"></div><script>
		var $ = function (id) { return document.getElementById(id); }, r = [];
		r.push($('a').tabIndex, $('s').tabIndex, $('i').type, $('c').type, $('i').maxLength, $('i').size);
		r.push($('g').crossOrigin, $('s').crossOrigin === undefined, document.createElement('img').crossOrigin === null, $('g').loading);
		r.push($('f').method, $('f').action, $('b').type, $('b').formAction, $('b').form === $('f'));
		r.push($('d').hidden, $('d').dir, $('d').contentEditable, $('a').contentEditable, $('s').draggable, $('a').draggable, $('d').draggable);
		r.push($('d').ariaLabel === null, $('d').title === '', $('d').spellcheck, $('d').translate);
		r.push($('o').start, $('o').reversed, $('ta').defaultValue, $('ta').rows);
		r.push($('o1').value, $('o1').text, $('o1').label, $('o2').value, $('o2').selected, $('o1').selected, $('o2').defaultSelected);
		$('o1').selected = true; r.push($('o2').selected);
		$('d').hidden = false; r.push($('d').hasAttribute('hidden'));
		$('d').ariaLabel = 'x'; $('d').ariaLabel = null; r.push($('d').hasAttribute('aria-label'));
		$('d').contentEditable = 'inherit'; r.push($('d').hasAttribute('contenteditable'));
		$('i').required = true; $('i').required = false; r.push($('i').hasAttribute('required'));
		$('out').textContent = r.join(',');
	</script></body></html>`)
	want := "0,-1,text,checkbox,-1,20," +
		"anonymous,true,true,eager," +
		"get,https://example.com/page,submit,https://example.com/page,true," +
		"until-found,rtl,true,inherit,true,true,false," +
		"true,true,true,true," +
		"1,false,hello,2," +
		"One two,One two,One two,v2,true,false,true," +
		"false,false,false,false,false"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// `name in el` is how Preact and Vue choose between a property and an
// attribute, so presence has to be per interface, not global.
func TestReflectedPresenceIsPerInterface(t *testing.T) {
	_, got := reflectOut(t, `<html><body><div id="out"></div><script>
		var div = document.createElement('div'), input = document.createElement('input'), r = [];
		r.push('title' in div, 'disabled' in div, 'disabled' in input, 'name' in div, 'name' in input,
			'htmlFor' in document.createElement('label'), 'htmlFor' in div,
			'noModule' in HTMLScriptElement.prototype, 'loading' in HTMLImageElement.prototype,
			'ariaLabel' in div, 'colSpan' in document.createElement('td'), 'colSpan' in div,
			typeof Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'placeholder').set);
		div.disabled = true; // an expando on a div, exactly as in a browser
		r.push(div.hasAttribute('disabled'), div.disabled);
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "true,false,true,false,true,true,false,true,true,true,true,false,function,false,true" {
		t.Errorf("got %s", got)
	}
}

// A custom element's attributeChangedCallback sees a reflected property write.
func TestReflectedWriteNotifiesCustomElement(t *testing.T) {
	_, got := reflectOut(t, `<html><body><x-el id="x"></x-el><div id="out"></div><script>
		var seen = [];
		customElements.define('x-el', class extends HTMLElement {
			static get observedAttributes() { return ['title', 'hidden']; }
			attributeChangedCallback(n, o, v) { seen.push(n + '=' + v); }
		});
		var x = document.getElementById('x');
		x.title = 'hi'; x.hidden = true;
		document.getElementById('out').textContent = seen.join(',');
	</script></body></html>`)
	if got != "title=hi,hidden=" {
		t.Errorf("got %s", got)
	}
}

func TestStyleReflectsToAttribute(t *testing.T) {
	doc, got := reflectOut(t, `<html><body>
		<p id="p" style="color: red; margin:0 !important">x</p><p id="q">q</p><p id="h">hidden by script</p>
		<div id="out"></div><script>
		var p = document.getElementById('p'), q = document.getElementById('q'), s = p.style, r = [];
		r.push(s.color, s.margin, s.getPropertyPriority('margin'), s.length, s[0], s.display === '', p.style === s);
		s.backgroundColor = 'blue'; s.setProperty('--gap', '4px'); s.setProperty('z-index', '2', 'important'); s.cssFloat = 'left';
		r.push(s.getPropertyValue('background-color'), s['background-color'], s.getPropertyValue('--gap'), s.zIndex);
		s.color = ''; r.push(s.removeProperty('margin'), s.color === '', 'transform' in s);
		r.push(Object.keys(s).join('|'));
		q.style.cssText = 'display:none;top:1px'; r.push(q.style.display, q.style.top, q.style.cssText);
		q.style.display = null; r.push(q.getAttribute('style'));
		q.setAttribute('style', 'left: 2px'); r.push(q.style.left, q.style.top === '');
		Object.assign(q.style, { opacity: 0.5, webkitTransform: 'none' }); r.push(q.getAttribute('style'));
		var h = document.getElementById('h'); h.style.display = 'none';
		r.push(document.body.innerText.indexOf('hidden by script') < 0);
		h.style.display = ''; r.push(document.body.innerText.indexOf('hidden by script') >= 0, h.getAttribute('style'));
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	want := "red,0,important,2,color,true,true," +
		"blue,blue,4px,2," +
		"0,true,true," +
		"backgroundColor|--gap|zIndex|cssFloat," +
		"none,1px,display:none;top:1px," +
		"top: 1px;," +
		"2px,true," +
		"left: 2px; opacity: 0.5; -webkit-transform: none;," +
		"true,true,"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !strings.Contains(doc, `<p id="p" style="background-color: blue; --gap: 4px; z-index: 2 !important; float: left;">`) {
		t.Errorf("style attribute not written back:\n%s", doc)
	}
}

// Inline-style writes reach MutationObservers, as in a browser.
func TestStyleWriteIsObserved(t *testing.T) {
	_, got := reflectOut(t, `<html><body><p id="p">x</p><div id="out"></div><script>
		var p = document.getElementById('p'), n = 0;
		new MutationObserver(function (recs) {
			document.getElementById('out').textContent = recs.length + ':' + recs[0].attributeName + ':' + recs[0].oldValue;
		}).observe(p, { attributes: true, attributeOldValue: true });
		p.style.display = 'none';
	</script></body></html>`)
	if got != "1:style:" {
		t.Errorf("got %q", got)
	}
}

// A page animating inline styles every frame must not hold the render open:
// style writes are not settle activity.
func TestStyleAnimationDoesNotPinSettle(t *testing.T) {
	doc, diag := renderCSP(t, `<html><body><p id="p">x</p><script>
		var p = document.getElementById('p'), i = 0;
		(function frame() { p.style.transform = 'translateX(' + (i++) + 'px)'; requestAnimationFrame(frame); })();
	</script></body></html>`, "https://example.com/p", nil, nil)
	_ = doc
	if diag.DeadlineHit || diag.DOMBusy {
		t.Errorf("animation loop pinned the settle: deadline=%v domBusy=%v total=%v", diag.DeadlineHit, diag.DOMBusy, diag.TotalDur)
	}
}

func TestInnerHTMLSerializesVoidElementsAsHTML(t *testing.T) {
	_, got := reflectOut(t, `<html><body><div id="s"><br><img src="a.png" alt="a/>b"><input disabled><p>t</p><!-- c/> --><script>var x = "<br/>";</script></div>
		<div id="out"></div><script>
		var s = document.getElementById('s');
		document.getElementById('out').textContent = s.innerHTML + '|' + document.createElement('hr').outerHTML;
	</script></body></html>`)
	want := `<br><img src="a.png" alt="a/>b"><input disabled=""><p>t</p><!-- c/> --><script>var x = "<br/>";</script>|<hr>`
	// #out's text is re-serialized by the test harness, so compare unescaped.
	got = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&#34;", `"`, "&amp;", "&").Replace(got)
	if got != strings.ReplaceAll(want, `alt="a/>b"`, `alt="a/&gt;b"`) && got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Most interfaces get their reflected attributes when the first element of that
// interface is wrapped. The ones pages probe on the prototype, with no element
// in hand, are installed up front — and an attribute that would advertise a
// feature the engine lacks is not reflected at all.
func TestReflectedPrototypeProbes(t *testing.T) {
	_, got := reflectOut(t, `<html><body><div id="out"></div><script>
		var r = [];
		r.push('noModule' in HTMLScriptElement.prototype, 'loading' in HTMLImageElement.prototype,
			'loading' in HTMLIFrameElement.prototype, 'inert' in HTMLElement.prototype, 'ariaLabel' in Element.prototype,
			'popover' in HTMLElement.prototype, 'shadowRootMode' in HTMLTemplateElement.prototype);
		var v = document.createElement('video');
		r.push('poster' in v, 'controls' in v, 'controls' in document.createElement('audio'), 'poster' in document.createElement('audio'));
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "true,true,true,true,true,false,false,true,true,true,false" {
		t.Errorf("got %s", got)
	}
}
