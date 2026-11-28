package js_test

import "testing"

// A select's value is its selected options, one state for page JavaScript,
// interact, submission and extraction.
func TestSelectState(t *testing.T) {
	const body = `<form id="f"><select id="s" name="s"><option value="a">A</option><optgroup label="g"><option value="b" selected>B</option><option>C text</option></optgroup></select>
		<select id="m" multiple><option value="1" selected>1</option><option value="2">2</option><option value="3" selected>3</option></select>
		<select id="none"><option disabled value="x">x</option><option value="y">y</option></select></form>`
	expectChecks(t, body, [][3]string{
		{"read", "(function(){ var s = document.getElementById('s'); return [s.value, s.selectedIndex, s.length, s.type, s.options.length, s.options[1].value, s.options[2].value, s.selectedOptions.length, s.selectedOptions[0].text].join(); })()", "b,1,3,select-one,3,b,C text,1,B"},
		{"options collection", "(function(){ var o = document.getElementById('s').options; return [o instanceof HTMLOptionsCollection, o instanceof HTMLCollection, o.selectedIndex, o.item(0).value, Object.prototype.toString.call(o)].join(); })()", "true,true,1,a,[object HTMLOptionsCollection]"},
		// What React's updateOptions does for a controlled select.
		{"react-style update", "(function(){ var s = document.getElementById('s'), o = s.options; for (var i = 0; i < o.length; i++) { var want = o[i].value === 'a'; if (o[i].selected !== want) o[i].selected = want; } return s.value + ':' + s.selectedIndex + ':' + document.querySelectorAll('#s option[selected]').length; })()", "a:0:1"},
		{"value setter", "(function(){ var s = document.getElementById('s'); s.value = 'C text'; var a = s.selectedIndex; s.value = 'nope'; return a + ':' + s.selectedIndex + ':' + JSON.stringify(s.value) + ':' + s.hasAttribute('value'); })()", "2:0:\"a\":false"}, // no match: nothing is marked, so the first option shows (see selectstate.go)
		{"selectedIndex setter", "(function(){ var s = document.getElementById('s'); s.selectedIndex = 1; return s.value + ':' + s.options[1].selected + ':' + s.options[0].selected; })()", "b:true:false"},
		{"implicit first enabled option", "(function(){ var s = document.getElementById('none'); return [s.value, s.selectedIndex, s.options[1].selected, s.options[0].selected].join(); })()", "y,1,true,false"},
		{"multiple", "(function(){ var m = document.getElementById('m'); m.options[1].selected = true; return [m.type, m.value, m.selectedOptions.length, Array.from(m.selectedOptions).map(function (o) { return o.value; }).join('+')].join(); })()", "select-multiple,1,3,1+2+3"},
		{"add and remove", "(function(){ var s = document.getElementById('none'); s.add(new Option('Zed', 'z', false, true)); s.options.add(new Option('First', 'f'), 0); var n = s.length; s.remove(0); return [n, s.length, s.value, s.options[s.length - 1].text, s.options[0].index].join(); })()", "4,3,z,Zed,0"},
		{"new Option", "(function(){ var o = new Option('T', 'v', true); return [o instanceof HTMLOptionElement, o.text, o.value, o.defaultSelected, o.selected, new Option('only text').value, new Option().text].join(); })()", "true,T,v,true,true,only text,"},
		{"select.remove() with no argument removes the select", "(function(){ var s = document.createElement('select'); document.body.appendChild(s); s.remove(); return s.isConnected; })()", "false"},
		{"FormData sees the selection", "(function(){ var s = document.getElementById('s'); s.value = 'a'; return new FormData(document.getElementById('f')).get('s'); })()", "a"},
	})
}
