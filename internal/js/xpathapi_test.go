package js_test

import "testing"

const xpathPage = `<div id="a" hx-on:click="go()"><p id="p1">one</p><p id="p2" class="k">two</p></div><ul><li>x</li><li data-hx-on:load="y()">y</li></ul>`

func TestXPathDOMAPI(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"globals exist", `return [typeof XPathEvaluator, typeof XPathResult, typeof XPathExpression, typeof document.evaluate,
			typeof document.createExpression, typeof document.createNSResolver, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, XPathResult.FIRST_ORDERED_NODE_TYPE].join(',');`,
			"function,function,function,function,function,function,7,9"},
		{"snapshot", `var r = document.evaluate('//p', document, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
			return [r.resultType, r.snapshotLength, r.snapshotItem(0).id, r.snapshotItem(1).id, r.snapshotItem(2), r instanceof XPathResult].join(',');`,
			"7,2,p1,p2,,true"},
		{"iterator", `var r = document.evaluate('//p', document, null, XPathResult.ORDERED_NODE_ITERATOR_TYPE, null), ids = [], n;
			while ((n = r.iterateNext())) ids.push(n.id);
			return ids.join(',') + ',' + r.invalidIteratorState;`, "p1,p2,false"},
		{"iterator is invalidated by a mutation", `var r = document.evaluate('//p', document, null, 5, null);
			r.iterateNext(); document.body.appendChild(document.createElement('i'));
			try { r.iterateNext(); return 'no throw'; } catch (e) { return e.name + ',' + r.invalidIteratorState; }`, "InvalidStateError,true"},
		{"single node, and null when empty", `
			var a = document.evaluate('//p[@class="k"]', document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
			var b = document.evaluate('//nope', document, null, 9, null).singleNodeValue;
			return a.id + ',' + b;`, "p2,null"},
		{"ANY_TYPE picks the natural type", `
			var n = document.evaluate('count(//p)', document, null, 0, null), s = document.evaluate('string(//p)', document),
				b = document.evaluate('1 = 1', document, null, XPathResult.ANY_TYPE, null), set = document.evaluate('//p', document, null, 0, null);
			return [n.resultType, n.numberValue, s.resultType, s.stringValue, b.resultType, b.booleanValue, set.resultType].join(',');`,
			"1,2,2,one,3,true,4"},
		{"conversions", `return [document.evaluate('//p', document, null, XPathResult.STRING_TYPE, null).stringValue,
			document.evaluate('//p', document, null, XPathResult.BOOLEAN_TYPE, null).booleanValue,
			document.evaluate('"12"', document, null, XPathResult.NUMBER_TYPE, null).numberValue].join(',');`, "one,true,12"},
		{"wrong-type access and non-node-set conversion throw TypeError", `var out = [];
			var r = document.evaluate('//p', document, null, 7, null);
			try { r.numberValue; out.push('n'); } catch (e) { out.push(e instanceof TypeError); }
			try { r.iterateNext(); out.push('n'); } catch (e) { out.push(e instanceof TypeError); }
			try { document.evaluate('1', document, null, 7, null); out.push('n'); } catch (e) { out.push(e instanceof TypeError); }
			return out.join(',');`, "true,true,true"},
		{"syntax error is a SyntaxError DOMException", `
			try { document.evaluate('//p[', document, null, 0, null); return 'no throw'; } catch (e) { return e.name + ',' + (e instanceof DOMException); }`,
			"SyntaxError,true"},
		{"context node is relative", `var a = document.getElementById('a');
			var r = document.evaluate('p[2] | .', a, null, 7, null);
			return r.snapshotItem(0).id + ',' + r.snapshotItem(1).id + ',' + document.evaluate('count(.//p)', document.querySelector('ul')).numberValue;`,
			"a,p2,0"},
		{"attribute nodes come back as Attr objects", `
			var r = document.evaluate('//p/@class', document, null, 9, null).singleNodeValue;
			return [r.name, r.value, r.ownerElement.id, r.nodeType].join(',');`, "class,k,p2,2"},
		{"XPathEvaluator + createExpression (htmx's hx-on discovery)", `
			var ev = new XPathEvaluator();
			var ex = ev.createExpression('.//*[@*[ starts-with(name(), "hx-on:") or starts-with(name(), "data-hx-on:") or starts-with(name(), "hx-on-") or starts-with(name(), "data-hx-on-") ]]');
			var it = ex.evaluate(document.body), out = [], n;
			while ((n = it.iterateNext())) out.push(n.tagName);
			return out.join(',') + ',' + (ex instanceof XPathExpression) + ',' + (ev instanceof XPathEvaluator);`, "DIV,LI,true,true"},
		{"a resolver is accepted", `var res = document.createNSResolver(document.documentElement);
			return document.evaluate('count(//x:p)', document, res, 1, null).numberValue + ',' +
				document.evaluate('count(//x:p)', document, function () { return 'http://www.w3.org/1999/xhtml'; }, 1, null).numberValue;`, "2,2"},
		{"a parsed document can be queried", `var d = new DOMParser().parseFromString('<p>a</p><p>b</p>', 'text/html');
			return d.evaluate('count(//p)', d, null, 1, null).numberValue + ',' + document.evaluate('string(//p[2])', d.body).stringValue;`, "2,b"},
		{"a runaway expression is refused, not run", `
			var s = ''; for (var i = 0; i < 4000; i++) s += '<div><span></span></div>';
			var host = document.createElement('div'); host.innerHTML = s; document.body.appendChild(host);
			try { document.evaluate('//*[//*[//*]]', document, null, 7, null); return 'ran'; } catch (e) { return e.name; }`, "OperationError"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantJS(t, xpathPage, c.script, c.want) })
	}
}
