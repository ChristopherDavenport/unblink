package js_test

import "testing"

func TestInnerTextGetter(t *testing.T) {
	cases := []struct{ name, body, script, want string }{
		{"exists as a property", `<p id="x">a</p>`, `return ('innerText' in x) + ',' + ('outerText' in x);`, "true,true"},
		{"blocks become lines", `<div id="x"><div>one</div><div>two</div><span>three</span> <span>four</span></div>`,
			`return JSON.stringify(x.innerText);`, `"one\ntwo\nthree four"`},
		{"paragraphs get a blank line", `<div id="x"><p>a</p><p>b</p>c</div>`, `return JSON.stringify(x.innerText);`, `"a\n\nb\n\nc"`},
		{"br is a newline and is not collapsed", `<div id="x">a<br><br>b</div>`, `return JSON.stringify(x.innerText);`, `"a\n\nb"`},
		{"white space collapses", "<div id=\"x\">  a \n\t b  <b> c </b>  d </div>", `return JSON.stringify(x.innerText);`, `"a b c d"`},
		{"pre keeps white space", "<pre id=\"x\">a  b\n  c</pre>", `return JSON.stringify(x.innerText);`, `"a  b\n  c"`},
		{"script style template hidden are skipped", `<div id="x">a<script>var z=1;</script><style>p{}</style><template>t</template><span hidden>h</span><span style="display: none">n</span>b</div>`,
			`return x.innerText;`, "ab"},
		{"an unrendered element returns textContent", `<div hidden id="x"><p>a</p> <p>b</p></div>`, `return JSON.stringify(x.innerText);`, `"a b"`},
		{"table cells tab, rows newline", `<table id="x"><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>`,
			`return JSON.stringify(x.innerText);`, `"a\tb\nc\td"`},
		{"lists", `<ul id="x"><li>a</li><li>b <b>c</b></li></ul>`, `return JSON.stringify(x.innerText);`, `"a\nb c"`},
		{"headings and inline display override", `<div id="x"><h1>T</h1><span style="display:block">s</span>t</div>`, `return JSON.stringify(x.innerText);`, `"T\ns\nt"`},
		{"textContent is unchanged by comparison", `<div id="x"><p>a</p><p>b</p></div>`, `return x.textContent;`, "ab"},
		{"outerText reads the same", `<div id="x"><p>a</p><p>b</p></div>`, `return x.outerText === x.innerText;`, "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, c.body, `var x = document.getElementById('x');`+c.script, c.want)
		})
	}
}

// The setter writes the DOM (it used to create a dead expando, so a page that
// updated a label through innerText changed nothing extraction could see).
func TestInnerTextSetter(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"replaces children with text", `x.innerText = '8 hours ago'; return x.innerHTML + '|' + x.childNodes.length;`, "8 hours ago|1"},
		{"newlines become br", `x.innerText = 'a\nb\r\nc';
			return Array.prototype.map.call(x.childNodes, function (n) { return n.nodeType === 3 ? n.data : n.tagName; }).join(',');`, "a,BR,b,BR,c"},
		{"markup is text, not parsed", `x.innerText = '<i>&</i>'; return x.innerHTML + '|' + x.firstChild.nodeType;`, "&lt;i&gt;&amp;&lt;/i&gt;|3"},
		{"empty string empties", `x.innerText = ''; return x.childNodes.length;`, "0"},
		{"null empties", `x.innerText = null; return x.childNodes.length;`, "0"},
		{"outerText replaces the element", `x.outerText = 'p\nq';
			return Array.prototype.map.call(document.getElementById('h').childNodes, function (n) { return n.nodeType === 3 ? n.data : n.tagName; }).join(',');`, "p,BR,q"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantJS(t, `<div id="h"><span id="x">old <b>bold</b></span></div>`, `var x = document.getElementById('x');`+c.script, c.want)
		})
	}
}

func TestInnerTextSetterIsObserved(t *testing.T) {
	wantJS(t, `<span id="x">old</span>`, `
		var mo = new MutationObserver(function (recs) { done(recs[0].type + ',' + recs[0].addedNodes.length + ',' + recs[0].removedNodes.length); });
		mo.observe(document.getElementById('x'), { childList: true });
		document.getElementById('x').innerText = 'new';`, "childList,1,1")
}
