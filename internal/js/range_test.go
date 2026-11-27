package js_test

import (
	"html"
	"strings"
	"testing"
)

// rangeFixture is the tree every Range case starts from (reset() rebuilds it):
//
//	host > p#a["Hello ", b#b["bold"], " world"], p#c["Second ", i#i["para"], "graph"]
const rangeFixture = `<p id="a">Hello <b id="b">bold</b> world</p><p id="c">Second <i id="i">para</i>graph</p>`

// rangeProbe runs script against a fresh rangeFixture and returns the values it
// recorded with put(name, value), keyed by name. The script sees host/a/b/c/i and
// the text nodes ta ("Hello "), tb ("bold"), tw (" world"), tc ("Second "),
// ti ("para"); pos(r) renders a range's boundary points and name(e) an exception.
func rangeProbe(t *testing.T, script string) map[string]string {
	t.Helper()
	out := render(t, `<html><body><div id="out"></div><div id="host"></div><script>
	var r = [], host = document.getElementById('host'), a, b, c, i, ta, tb, tw, tc, ti;
	function reset() {
	  host.innerHTML = '`+rangeFixture+`';
	  a = document.getElementById('a'); b = document.getElementById('b');
	  c = document.getElementById('c'); i = document.getElementById('i');
	  ta = a.firstChild; tb = b.firstChild; tw = a.lastChild; tc = c.firstChild; ti = i.firstChild;
	}
	function put(k, v) { r.push(k + '=' + v); }
	function label(n) {
	  if (n === document) return '#document';
	  if (n === host) return 'host';
	  if (n.nodeType === 3) return '"' + n.data + '"';
	  return n.id || n.nodeName.toLowerCase();
	}
	function pos(rg) { return label(rg.startContainer) + ',' + rg.startOffset + ' ' + label(rg.endContainer) + ',' + rg.endOffset; }
	function name(f) { try { f(); return 'no throw'; } catch (e) { return e.name + ',' + (e instanceof DOMException); } }
	function mk(sc, so, ec, eo) { var rg = document.createRange(); rg.setStart(sc, so); rg.setEnd(ec, eo); return rg; }
	function fragHTML(f) { var d = document.createElement('div'); d.appendChild(f); return d.innerHTML; }
	reset();
	try { (function () {`+script+`})(); } catch (e) { put('THROWN', e && (e.stack || e)); }
	document.getElementById('out').textContent = r.join('|');
	</script></body></html>`)
	i := strings.Index(out, `id="out">`)
	if i < 0 {
		t.Fatalf("no #out in:\n%s", out)
	}
	j := strings.Index(out[i:], "</div>")
	got := map[string]string{}
	for _, kv := range strings.Split(html.UnescapeString(out[i+9:i+j]), "|") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if e, ok := got["THROWN"]; ok {
		t.Fatalf("script threw: %s", e)
	}
	return got
}

func checkRange(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: not recorded", k)
		} else if g != w {
			t.Errorf("%s:\n got  %q\n want %q", k, g, w)
		}
	}
}

// TestRangeBoundaryPoints covers the boundary-point setters, the derived
// attributes, and the start-after-end collapse rule.
func TestRangeBoundaryPoints(t *testing.T) {
	got := rangeProbe(t, `
	  var rg = document.createRange();
	  put('fresh', pos(rg) + ' ' + rg.collapsed + ' ' + label(rg.commonAncestorContainer));
	  put('ctor', pos(new Range()) + ' ' + (rg instanceof Range));

	  rg.setStart(ta, 2); rg.setEnd(tw, 3);
	  put('set', pos(rg) + ' ' + rg.collapsed + ' ' + label(rg.commonAncestorContainer));
	  rg.setStart(ti, 1);
	  put('startAfterEnd', pos(rg) + ' ' + rg.collapsed);
	  rg.setEnd(ta, 1);
	  put('endBeforeStart', pos(rg) + ' ' + rg.collapsed);
	  var other = document.createElement('div'); other.textContent = 'x';
	  rg.setEnd(other, 1);
	  put('otherRoot', label(rg.startContainer) + ',' + rg.startOffset + ' ' + (rg.endContainer === other) + ' ' + rg.collapsed);

	  rg = document.createRange();
	  rg.setStartBefore(b); rg.setEndAfter(b);
	  put('beforeAfter', pos(rg) + ' [' + rg + ']');
	  rg.setStartAfter(b); rg.setEndBefore(c);
	  put('afterBefore', pos(rg) + ' [' + rg + '] ' + label(rg.commonAncestorContainer));

	  rg.selectNode(b);
	  put('selectNode', pos(rg) + ' ' + label(rg.commonAncestorContainer));
	  rg.selectNodeContents(a);
	  put('selectContents', pos(rg) + ' [' + rg + ']');
	  rg.selectNodeContents(tb);
	  put('selectText', pos(rg));

	  rg = mk(ta, 2, tw, 3);
	  var cl = rg.cloneRange();
	  rg.collapse(true);
	  put('collapseStart', pos(rg) + ' ' + rg.collapsed);
	  put('cloneIndependent', pos(cl) + ' ' + (cl !== rg) + ' ' + (cl instanceof Range));
	  cl.collapse();
	  put('collapseEnd', pos(cl) + ' ' + cl.collapsed);

	  put('offsetTooBig', name(function () { rg.setStart(ta, 99); }));
	  put('elemOffsetTooBig', name(function () { rg.setEnd(a, 4); }));
	  put('selectDetached', name(function () { rg.selectNode(document.createElement('p')); }));
	  put('beforeDetached', name(function () { rg.setStartBefore(document.createElement('p')); }));
	  put('afterDetached', name(function () { rg.setEndAfter(document.createElement('p')); }));
	  put('notNode', name(function () { rg.setStart(null, 0); }));
	  put('consts', [Range.START_TO_START, Range.START_TO_END, Range.END_TO_END, Range.END_TO_START, rg.END_TO_START].join(','));
	`)
	checkRange(t, got, map[string]string{
		"fresh":            "#document,0 #document,0 true #document",
		"ctor":             "#document,0 #document,0 true",
		"set":              `"Hello ",2 " world",3 false a`,
		"startAfterEnd":    `"para",1 "para",1 true`,
		"endBeforeStart":   `"Hello ",1 "Hello ",1 true`,
		"otherRoot":        "div,1 true true",
		"beforeAfter":      "a,1 a,2 [bold]",
		"afterBefore":      "a,2 host,1 [ world] host",
		"selectNode":       "a,1 a,2 a",
		"selectContents":   "a,0 a,3 [Hello bold world]",
		"selectText":       `"bold",0 "bold",4`,
		"collapseStart":    `"Hello ",2 "Hello ",2 true`,
		"cloneIndependent": `"Hello ",2 " world",3 true true`,
		"collapseEnd":      `" world",3 " world",3 true`,
		"offsetTooBig":     "IndexSizeError,true",
		"elemOffsetTooBig": "IndexSizeError,true",
		"selectDetached":   "InvalidNodeTypeError,true",
		"beforeDetached":   "InvalidNodeTypeError,true",
		"afterDetached":    "InvalidNodeTypeError,true",
		"notNode":          "TypeError,false",
		"consts":           "0,1,2,3,3",
	})
}

// TestRangeComparison covers compareBoundaryPoints, comparePoint, isPointInRange
// and intersectsNode, including the different-tree outcomes.
func TestRangeComparison(t *testing.T) {
	got := rangeProbe(t, `
	  var r1 = document.createRange(), r2 = document.createRange();
	  r1.selectNodeContents(a); r2.selectNodeContents(b);
	  put('cbp', [r1.compareBoundaryPoints(Range.START_TO_START, r2), r1.compareBoundaryPoints(Range.START_TO_END, r2),
	    r1.compareBoundaryPoints(Range.END_TO_END, r2), r1.compareBoundaryPoints(Range.END_TO_START, r2),
	    r2.compareBoundaryPoints(Range.START_TO_START, r1), r1.compareBoundaryPoints(Range.END_TO_END, r1)].join(','));
	  put('cbpHow', name(function () { r1.compareBoundaryPoints(4, r2); }));
	  var det = document.createElement('div'); det.innerHTML = '<s>z</s>';
	  var r3 = document.createRange(); r3.selectNodeContents(det);
	  put('cbpOtherTree', name(function () { r1.compareBoundaryPoints(0, r3); }));

	  var rg = mk(ta, 2, tw, 3);
	  put('comparePoint', [rg.comparePoint(ta, 0), rg.comparePoint(ta, 2), rg.comparePoint(tb, 1), rg.comparePoint(a, 1),
	    rg.comparePoint(tw, 3), rg.comparePoint(tw, 5), rg.comparePoint(c, 0), rg.comparePoint(host, 0), rg.comparePoint(host, 1)].join(','));
	  put('comparePointBig', name(function () { rg.comparePoint(ta, 99); }));
	  put('comparePointOtherTree', name(function () { rg.comparePoint(det, 0); }));
	  put('inRange', [rg.isPointInRange(tb, 1), rg.isPointInRange(ta, 1), rg.isPointInRange(c, 0), rg.isPointInRange(det, 0)].join(','));
	  put('inRangeBig', name(function () { rg.isPointInRange(ta, 99); }));
	  put('intersects', [rg.intersectsNode(b), rg.intersectsNode(tb), rg.intersectsNode(a), rg.intersectsNode(ta),
	    rg.intersectsNode(c), rg.intersectsNode(host), rg.intersectsNode(document), rg.intersectsNode(det.firstChild)].join(','));
	  // A node that only touches the range's boundary does not intersect it.
	  var edge = mk(a, 2, c, 0);
	  put('intersectsEdge', [edge.intersectsNode(b), edge.intersectsNode(tw), edge.intersectsNode(tc), edge.intersectsNode(c)].join(','));
	`)
	checkRange(t, got, map[string]string{
		"cbp":                   "-1,1,1,-1,1,0",
		"cbpHow":                "NotSupportedError,true",
		"cbpOtherTree":          "WrongDocumentError,true",
		"comparePoint":          "-1,0,0,0,0,1,1,-1,1",
		"comparePointBig":       "IndexSizeError,true",
		"comparePointOtherTree": "WrongDocumentError,true",
		"inRange":               "true,false,false,false",
		"inRangeBig":            "IndexSizeError,true",
		"intersects":            "true,true,true,true,false,true,true,false",
		"intersectsEdge":        "false,true,false,true",
	})
}

// TestRangeToString covers stringification: partial start/end text nodes, wholly
// contained text across subtrees, comments excluded, UTF-16 offsets.
func TestRangeToString(t *testing.T) {
	got := rangeProbe(t, `
	  put('sameText', '[' + mk(tb, 1, tb, 3) + ']');
	  put('partialBoth', '[' + mk(ta, 2, tw, 3) + ']');
	  put('crossSubtree', '[' + mk(ta, 3, ti, 2) + ']');
	  put('elemToText', '[' + mk(a, 1, tc, 3) + ']');
	  put('textToElem', '[' + mk(tb, 2, c, 1) + ']');
	  put('collapsed', '[' + mk(a, 1, a, 1) + ']');
	  put('whole', '[' + mk(host, 0, host, 2) + ']');
	  a.insertBefore(document.createComment('skip me'), b);
	  put('comment', '[' + mk(a, 0, a, 4) + ']');
	  var u = document.createTextNode('資料xy'); c.appendChild(u);
	  put('utf16', '[' + mk(u, 1, u, 3) + ']');
	`)
	checkRange(t, got, map[string]string{
		"sameText":     "[ol]",
		"partialBoth":  "[llo bold wo]",
		"crossSubtree": "[lo bold worldSecond pa]",
		"elemToText":   "[bold worldSec]",
		"textToElem":   "[ld worldSecond ]",
		"collapsed":    "[]",
		"whole":        "[Hello bold worldSecond paragraph]",
		"comment":      "[Hello bold world]",
		"utf16":        "[料x]",
	})
}

// TestRangeCloneAndExtract covers cloneContents / extractContents /
// deleteContents: same-text, partially contained text and elements, and a range
// spanning two subtrees.
func TestRangeCloneAndExtract(t *testing.T) {
	got := rangeProbe(t, `
	  var before = host.innerHTML, rg;

	  rg = mk(ta, 2, tb, 2);
	  put('clonePartial', fragHTML(rg.cloneContents()));
	  put('cloneLeavesTree', (host.innerHTML === before) + ' ' + pos(rg));
	  put('cloneCross', fragHTML(mk(ta, 3, ti, 2).cloneContents()));
	  put('cloneSameText', fragHTML(mk(tb, 1, tb, 3).cloneContents()));
	  put('cloneCollapsed', mk(a, 1, a, 1).cloneContents().childNodes.length + ' ' + mk(a, 1, a, 1).cloneContents().nodeType);
	  put('cloneChildren', fragHTML(mk(a, 1, a, 3).cloneContents()));
	  put('cloneUntouched', host.innerHTML === before);

	  rg = mk(ta, 3, ti, 2);
	  put('extractCross', fragHTML(rg.extractContents()));
	  put('extractCrossTree', host.innerHTML);
	  put('extractCrossPos', pos(rg) + ' ' + rg.collapsed);

	  reset(); rg = mk(tb, 1, tb, 3);
	  var f = rg.extractContents();
	  put('extractSameText', f.firstChild.nodeType + ' ' + fragHTML(f) + ' ' + a.innerHTML + ' ' + pos(rg));

	  reset(); rg = mk(a, 1, a, 2);
	  f = rg.extractContents();
	  put('extractMovesNode', (f.firstChild === b) + ' ' + a.innerHTML + ' ' + pos(rg));

	  reset(); rg = mk(tb, 2, c, 1);
	  put('extractTextToElem', fragHTML(rg.extractContents()) + ' // ' + host.innerHTML + ' // ' + pos(rg));

	  reset(); rg = mk(tb, 1, tb, 3); rg.deleteContents();
	  put('deleteSameText', a.innerHTML + ' ' + pos(rg) + ' ' + rg.collapsed);

	  reset(); rg = mk(ta, 2, tw, 3); rg.deleteContents();
	  put('deletePartial', a.innerHTML + ' ' + a.childNodes.length + ' ' + pos(rg));

	  reset(); rg = mk(ta, 3, ti, 2); rg.deleteContents();
	  put('deleteCross', host.innerHTML + ' ' + pos(rg));

	  reset(); rg = mk(host, 0, host, 2); rg.deleteContents();
	  put('deleteAll', '[' + host.innerHTML + '] ' + pos(rg));

	  reset(); rg = mk(a, 1, a, 1); rg.deleteContents();
	  put('deleteCollapsed', host.innerHTML === before);
	`)
	checkRange(t, got, map[string]string{
		"clonePartial":      `llo <b id="b">bo</b>`,
		"cloneLeavesTree":   `true "Hello ",2 "bold",2`,
		"cloneCross":        `<p id="a">lo <b id="b">bold</b> world</p><p id="c">Second <i id="i">pa</i></p>`,
		"cloneSameText":     "ol",
		"cloneCollapsed":    "0 11",
		"cloneChildren":     `<b id="b">bold</b> world`,
		"cloneUntouched":    "true",
		"extractCross":      `<p id="a">lo <b id="b">bold</b> world</p><p id="c">Second <i id="i">pa</i></p>`,
		"extractCrossTree":  `<p id="a">Hel</p><p id="c"><i id="i">ra</i>graph</p>`,
		"extractCrossPos":   "host,1 host,1 true",
		"extractSameText":   `3 ol Hello <b id="b">bd</b> world "bd",1 "bd",1`,
		"extractMovesNode":  `true Hello  world a,1 a,1`,
		"extractTextToElem": `<p id="a"><b id="b">ld</b> world</p><p id="c">Second </p> // <p id="a">Hello <b id="b">bo</b></p><p id="c"><i id="i">para</i>graph</p> // host,1 host,1`,
		"deleteSameText":    `Hello <b id="b">bd</b> world "bd",1 "bd",1 true`,
		"deletePartial":     "Herld 2 a,1 a,1",
		"deleteCross":       `<p id="a">Hel</p><p id="c"><i id="i">ra</i>graph</p> host,1 host,1`,
		"deleteAll":         "[] host,0 host,0",
		"deleteCollapsed":   "true",
	})
}

// TestRangeInsertAndSurround covers insertNode (text splitting, fragments, moving
// an attached node) and surroundContents (including its InvalidStateError for a
// partially contained non-Text node).
func TestRangeInsertAndSurround(t *testing.T) {
	got := rangeProbe(t, `
	  function el(tag, text) { var e = document.createElement(tag); if (text) e.textContent = text; return e; }
	  var rg;

	  rg = mk(ta, 2, ta, 2); rg.insertNode(el('u', 'X'));
	  put('insertSplitsText', a.innerHTML + ' // ' + pos(rg) + ' [' + rg + '] ' + a.childNodes.length);

	  reset(); rg = mk(ta, 1, ta, 4); rg.insertNode(el('em'));
	  put('insertKeepsRange', a.innerHTML + ' // ' + pos(rg) + ' [' + rg + ']');

	  reset(); rg = mk(a, 1, a, 1);
	  var f = document.createDocumentFragment(); f.appendChild(el('u', '1')); f.appendChild(el('u', '2'));
	  rg.insertNode(f);
	  put('insertFragment', a.innerHTML + ' // ' + pos(rg) + ' [' + rg + ']');

	  reset(); rg = mk(a, 3, a, 3); rg.insertNode(el('u', 'end'));
	  put('insertAtEnd', a.innerHTML + ' // ' + pos(rg));

	  reset(); rg = mk(c, 0, c, 1); rg.insertNode(b);
	  put('insertMoves', host.innerHTML + ' // ' + pos(rg));

	  reset(); rg = mk(tb, 0, tb, 0);
	  put('insertAncestor', name(function () { rg.insertNode(a); }) + ' ' + (host.innerHTML.length > 0) + ' ' + b.childNodes.length);
	  var cm = document.createComment('note'); a.appendChild(cm);
	  put('insertInComment', name(function () { mk(cm, 1, cm, 1).insertNode(el('u')); }));
	  var lone = document.createTextNode('lone');
	  put('insertInOrphanText', name(function () { mk(lone, 1, lone, 1).insertNode(el('u')); }));

	  reset(); rg = mk(ta, 1, ta, 4);
	  var span = el('span', 'old'); rg.surroundContents(span);
	  put('surroundText', a.innerHTML + ' // ' + pos(rg) + ' [' + rg + ']');

	  reset(); rg = mk(ta, 3, tw, 2);
	  rg.surroundContents(el('mark'));
	  put('surroundAcrossElem', a.innerHTML + ' // ' + pos(rg) + ' [' + rg + ']');

	  reset(); rg = mk(ta, 1, tb, 2);
	  var beforeBad = host.innerHTML;
	  put('surroundPartial', name(function () { rg.surroundContents(el('span')); }) + ' ' + (host.innerHTML === beforeBad));
	  put('surroundBadParent', name(function () { mk(ta, 1, ta, 2).surroundContents(document.createDocumentFragment()); }));
	`)
	checkRange(t, got, map[string]string{
		"insertSplitsText":   `He<u>X</u>llo <b id="b">bold</b> world // "He",2 a,2 [X] 5`,
		"insertKeepsRange":   `H<em></em>ello <b id="b">bold</b> world // "H",1 "ello ",3 [ell]`,
		"insertFragment":     `Hello <u>1</u><u>2</u><b id="b">bold</b> world // a,1 a,3 [12]`,
		"insertAtEnd":        `Hello <b id="b">bold</b> world<u>end</u> // a,3 a,4`,
		"insertMoves":        `<p id="a">Hello  world</p><p id="c"><b id="b">bold</b>Second <i id="i">para</i>graph</p> // c,0 c,2`,
		"insertAncestor":     "HierarchyRequestError,true true 1",
		"insertInComment":    "HierarchyRequestError,true",
		"insertInOrphanText": "HierarchyRequestError,true",
		"surroundText":       `H<span>ell</span>o <b id="b">bold</b> world // a,1 a,2 [ell]`,
		"surroundAcrossElem": `Hel<mark>lo <b id="b">bold</b> w</mark>orld // a,1 a,2 [lo bold w]`,
		"surroundPartial":    "InvalidStateError,true true",
		"surroundBadParent":  "InvalidNodeTypeError,true",
	})
}

// TestRangeStubsAndFragment locks what Range keeps from before: a parsing
// createContextualFragment and zero-geometry rects (layout is a non-goal).
func TestRangeStubsAndFragment(t *testing.T) {
	got := rangeProbe(t, `
	  var rg = document.createRange();
	  var f = rg.createContextualFragment('<li class="x">one</li><li>two</li>');
	  put('fragment', f.nodeType + ' ' + f.childNodes.length + ' ' + fragHTML(f));
	  var rect = mk(ta, 0, tw, 2).getBoundingClientRect();
	  put('rect', [rect.x, rect.y, rect.width, rect.height, rect.top, rect.bottom].join(','));
	  put('rects', rg.getClientRects().length);
	  put('detach', typeof rg.detach + ' ' + rg.detach());
	`)
	checkRange(t, got, map[string]string{
		"fragment": `11 2 <li class="x">one</li><li>two</li>`,
		"rect":     "0,0,0,0,0,0",
		"rects":    "0",
		"detach":   "function undefined",
	})
}

// TestSelectionTracksRange proves the Selection is backed by the Range it was
// given: its anchor/focus, text and edits all follow the same boundary points.
func TestSelectionTracksRange(t *testing.T) {
	got := rangeProbe(t, `
	  var sel = window.getSelection();
	  function st() { return sel.rangeCount + ' ' + sel.type + ' ' + sel.isCollapsed + ' [' + sel + ']'; }
	  put('same', (sel === document.getSelection()) + ' ' + (sel instanceof Selection));
	  put('empty', st() + ' ' + sel.anchorNode + ' ' + sel.focusNode + ' ' + sel.anchorOffset);
	  put('emptyGetRangeAt', name(function () { sel.getRangeAt(0); }));

	  var rg = mk(ta, 2, tw, 3);
	  sel.addRange(rg);
	  put('added', st() + ' ' + (sel.getRangeAt(0) === rg) + ' ' + label(sel.anchorNode) + ',' + sel.anchorOffset + ' ' + label(sel.focusNode) + ',' + sel.focusOffset);
	  sel.addRange(mk(tc, 0, tc, 2));
	  put('secondIgnored', sel.rangeCount + ' ' + (sel.getRangeAt(0) === rg));
	  rg.setEnd(tb, 2);
	  put('followsRange', '[' + sel + ']');
	  put('contains', [sel.containsNode(tb), sel.containsNode(tb, true), sel.containsNode(c, true), sel.containsNode(ta), sel.containsNode(ta, true)].join(','));
	  put('removeOther', name(function () { sel.removeRange(document.createRange()); }));
	  sel.removeRange(rg);
	  put('removed', st());

	  sel.setBaseAndExtent(tw, 3, ta, 2);
	  put('backwards', st() + ' ' + label(sel.anchorNode) + ',' + sel.anchorOffset + ' ' + label(sel.focusNode) + ',' + sel.focusOffset + ' ' + pos(sel.getRangeAt(0)));
	  sel.extend(tc, 3);
	  put('extended', '[' + sel + '] ' + label(sel.anchorNode) + ' ' + label(sel.focusNode));
	  sel.collapseToStart();
	  put('collapsedStart', st() + ' ' + label(sel.anchorNode) + ',' + sel.anchorOffset);
	  sel.collapse(tb, 1);
	  put('caret', st() + ' ' + pos(sel.getRangeAt(0)));
	  sel.selectAllChildren(b);
	  put('allChildren', st() + ' ' + sel.containsNode(tb));
	  sel.collapseToEnd();
	  put('collapsedEnd', pos(sel.getRangeAt(0)));
	  sel.selectAllChildren(b);
	  sel.deleteFromDocument();
	  put('deleted', a.innerHTML + ' ' + st());
	  sel.removeAllRanges();
	  put('cleared', st());
	  put('collapseNoRange', name(function () { sel.collapseToStart(); }));
	  sel.selectAllChildren(c); sel.empty(); sel.modify('move', 'forward', 'character');
	  put('emptied', st());
	`)
	checkRange(t, got, map[string]string{
		"same":            "true true",
		"empty":           "0 None true [] null null 0",
		"emptyGetRangeAt": "IndexSizeError,true",
		"added":           `1 Range false [llo bold wo] true "Hello ",2 " world",3`,
		"secondIgnored":   "1 true",
		"followsRange":    "[llo bo]",
		"contains":        "false,true,false,false,true",
		"removeOther":     "NotFoundError,true",
		"removed":         "0 None true []",
		"backwards":       `1 Range false [llo bold wo] " world",3 "Hello ",2 "Hello ",2 " world",3`,
		"extended":        `[rldSec] " world" "Second "`,
		"collapsedStart":  `1 Caret true [] " world",3`,
		"caret":           `1 Caret true [] "bold",1 "bold",1`,
		"allChildren":     "1 Range false [bold] true",
		"collapsedEnd":    "b,1 b,1",
		"deleted":         `Hello <b id="b"></b> world 1 Caret true []`,
		"cleared":         "0 None true []",
		"collapseNoRange": "InvalidStateError,true",
		"emptied":         "0 None true []",
	})
}
