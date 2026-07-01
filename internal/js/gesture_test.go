package js_test

import (
	"strings"
	"testing"
	"time"
)

// A react-aria-style widget activates on the pointerdown→pointerup press pair
// (and calls setPointerCapture inside pointerdown), not on a bare click. It also
// treats a click with falsy detail as a virtual/AT press. A default interact
// "click" must drive the real press so the panel opens.
func TestInteractPressActivatesPointerWidget(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<div id="tab">Tab</div><div id="panel">closed</div>
		<script>
		  var down = false;
		  var tab = document.getElementById('tab'), panel = document.getElementById('panel');
		  tab.addEventListener('pointerdown', function (e) { this.setPointerCapture(e.pointerId); down = true; });
		  tab.addEventListener('pointerup', function () { if (down) panel.textContent = 'opened'; });
		  tab.addEventListener('click', function (e) { if (!e.detail) panel.textContent = 'virtual-ignored'; });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	if !dispatch(t, lc, "#tab", "click") {
		t.Fatal("selector #tab did not match")
	}
	out := snapshot(t, lc)
	if !strings.Contains(out, ">opened<") {
		t.Errorf("press sequence should open the panel, got:\n%s", out)
	}
	if strings.Contains(out, ">virtual-ignored<") {
		t.Errorf("synthetic click must carry detail>0 (non-virtual), got:\n%s", out)
	}
}

// The gesture must fire each event type exactly once — never double-activate a
// single type (a handler bound to both mouseup and click firing twice would match
// real-browser behavior, but firing e.g. click twice would not).
func TestInteractPressFiresEachTypeOnce(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="out"></div>
		<script>
		  var c = { pointerdown: 0, mousedown: 0, pointerup: 0, mouseup: 0, click: 0 };
		  var b = document.getElementById('b');
		  Object.keys(c).forEach(function (t) {
		    b.addEventListener(t, function () {
		      c[t]++;
		      document.getElementById('out').textContent =
		        'pd' + c.pointerdown + 'md' + c.mousedown + 'pu' + c.pointerup + 'mu' + c.mouseup + 'ck' + c.click;
		    });
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#b", "click")
	if out := snapshot(t, lc); !strings.Contains(out, "pd1md1pu1mu1ck1") {
		t.Errorf("each gesture event should fire exactly once, got:\n%s", out)
	}
}

// The synthesized events carry realistic, non-"virtual" fields.
func TestInteractPressEventProperties(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="pd"></div><div id="ck"></div>
		<script>
		  var b = document.getElementById('b');
		  b.addEventListener('pointerdown', function (e) {
		    document.getElementById('pd').textContent =
		      e.pointerType + '/' + e.buttons + '/' + e.isPrimary + '/' + (e.width > 0) + '/' + (e.pressure > 0) + '/' + e.isTrusted;
		  });
		  b.addEventListener('click', function (e) {
		    document.getElementById('ck').textContent = 'detail' + e.detail + 'button' + e.button;
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#b", "click")
	out := snapshot(t, lc)
	if !strings.Contains(out, "mouse/1/true/true/true/true") {
		t.Errorf("pointerdown should be a real primary mouse press, got:\n%s", out)
	}
	if !strings.Contains(out, "detail1button0") {
		t.Errorf("click should carry detail=1, button=0, got:\n%s", out)
	}
}

// Focus is now real: clicking a focusable input focuses it (revealing a
// focus-triggered menu) and document.activeElement tracks it; moving focus fires
// blur/focusout on the previous element.
func TestInteractFocusTracksActiveElement(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<input id="search"><div id="menu">hidden</div>
		<button id="other">x</button><div id="log">?</div>
		<script>
		  var s = document.getElementById('search');
		  s.addEventListener('focusin', function () { document.getElementById('menu').textContent = 'suggestions'; });
		  s.addEventListener('blur', function () { document.getElementById('log').textContent = 'blurred'; });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#search", "click")
	if out := snapshot(t, lc); !strings.Contains(out, ">suggestions<") {
		t.Errorf("focusin (via press focus) should reveal the menu, got:\n%s", out)
	}
	dispatch(t, lc, "#other", "focus")
	if out := snapshot(t, lc); !strings.Contains(out, ">blurred<") {
		t.Errorf("moving focus should blur the input, got:\n%s", out)
	}
}

// A hover interaction reveals a hover-triggered submenu.
func TestInteractHoverRevealsSubmenu(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<div id="menu">Menu</div><div id="sub">hidden</div>
		<script>
		  document.getElementById('menu').addEventListener('mouseenter', function () {
		    document.getElementById('sub').textContent = 'submenu';
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#menu", "hover")
	if out := snapshot(t, lc); !strings.Contains(out, ">submenu<") {
		t.Errorf("hover should reveal the submenu, got:\n%s", out)
	}
}

// element.click() (the JS method) must stay a single bare click per the DOM spec
// — only the interact gesture path emulates a full press.
func TestElementClickMethodStaysBare(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="out">init</div>
		<script>
		  var b = document.getElementById('b'), out = document.getElementById('out');
		  b.addEventListener('pointerdown', function () { out.textContent = 'pressed'; });
		  b.addEventListener('click', function () { if (out.textContent === 'init') out.textContent = 'clicked'; });
		  b.click();
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	if out := snapshot(t, lc); !strings.Contains(out, ">clicked<") {
		t.Errorf("element.click() should fire a lone click (no pointerdown), got:\n%s", out)
	}
}
