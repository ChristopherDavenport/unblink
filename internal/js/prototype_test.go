package js_test

import (
	"strings"
	"testing"
)

// These exercise the DOM prototype chain and the new tree APIs added for framework
// rendering. Each script writes a PASS/FAIL marker into #out so the assertion can
// read it back from the serialized tree (render only returns serialized HTML).

func TestPrototypeChainInstanceof(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var el = document.createElement('div');
		  var txt = document.createTextNode('hi');
		  var ok = (el instanceof HTMLElement) && (el instanceof Element) &&
		           (el instanceof Node) && (el instanceof EventTarget) &&
		           (txt instanceof Text) && (txt instanceof CharacterData) &&
		           (txt instanceof Node) && !(txt instanceof Element);
		  document.getElementById('out').textContent = ok ? 'PASS' : 'FAIL';
		</script></body></html>`)
	if !strings.Contains(out, ">PASS<") {
		t.Errorf("instanceof chain not satisfied:\n%s", out)
	}
}

func TestPrototypeIdentityAndGetPrototypeOf(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var el = document.getElementById('out');
		  var ok = (Object.getPrototypeOf(el) === HTMLElement.prototype) &&
		           (el === document.getElementById('out')) &&     // wrapper identity
		           (el.classList === el.classList);                // companion identity
		  el.textContent = ok ? 'PASS' : 'FAIL';
		</script></body></html>`)
	if !strings.Contains(out, ">PASS<") {
		t.Errorf("prototype identity not satisfied:\n%s", out)
	}
}

func TestPrototypePatching(t *testing.T) {
	// A framework monkey-patching Element.prototype must be visible on instances.
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  Element.prototype.__unblinkTag = function () { return 'patched'; };
		  var el = document.getElementById('out');
		  el.textContent = (el.__unblinkTag() === 'patched') ? 'PASS' : 'FAIL';
		</script></body></html>`)
	if !strings.Contains(out, ">PASS<") {
		t.Errorf("prototype patching not visible on instance:\n%s", out)
	}
}

func TestCreateCommentAndFragment(t *testing.T) {
	out := render(t, `<html><body><ul id="list"></ul>
		<script>
		  var frag = document.createDocumentFragment();
		  ['a','b'].forEach(function (s) {
		    var li = document.createElement('li');
		    li.textContent = s;
		    frag.appendChild(li);
		  });
		  document.getElementById('list').appendChild(frag);          // moves children
		  document.getElementById('list').appendChild(document.createComment('marker'));
		</script></body></html>`)
	if !strings.Contains(out, "<li>a</li>") || !strings.Contains(out, "<li>b</li>") {
		t.Errorf("fragment children not moved into list:\n%s", out)
	}
	if !strings.Contains(out, "<!--marker-->") {
		t.Errorf("comment node not inserted:\n%s", out)
	}
}

func TestCloneNodeAndReplaceChild(t *testing.T) {
	out := render(t, `<html><body><div id="host"><span id="orig">x</span></div>
		<script>
		  var orig = document.getElementById('orig');
		  var clone = orig.cloneNode(true);   // deep clone copies attrs incl. id
		  clone.id = 'clone';
		  clone.textContent = 'cloned';
		  orig.parentNode.replaceChild(clone, orig);
		</script></body></html>`)
	if strings.Contains(out, `id="orig"`) || strings.Contains(out, ">x<") {
		t.Errorf("original not replaced:\n%s", out)
	}
	if !strings.Contains(out, `id="clone"`) || !strings.Contains(out, ">cloned<") {
		t.Errorf("clone not inserted:\n%s", out)
	}
}

func TestDatasetAndInsertAdjacentHTML(t *testing.T) {
	out := render(t, `<html><body><div id="anchor"></div>
		<script>
		  var a = document.getElementById('anchor');
		  a.dataset.userId = '42';
		  var got = a.getAttribute('data-user-id');
		  a.insertAdjacentHTML('afterend', '<p id="sib">sibling</p>');
		  a.dataset.ok = (got === '42') ? 'yes' : 'no';
		</script></body></html>`)
	if !strings.Contains(out, `data-user-id="42"`) || !strings.Contains(out, `data-ok="yes"`) {
		t.Errorf("dataset reflection failed:\n%s", out)
	}
	if !strings.Contains(out, `<p id="sib">sibling</p>`) {
		t.Errorf("insertAdjacentHTML did not splice sibling:\n%s", out)
	}
}

func TestMutationObserverFires(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var out = document.getElementById('out');
		  var obs = new MutationObserver(function (records) {
		    out.setAttribute('data-mo', String(records.length));
		    obs.disconnect();
		  });
		  obs.observe(document.body, { childList: true });
		  document.body.appendChild(document.createElement('div'));
		</script></body></html>`)
	if !strings.Contains(out, `data-mo="1"`) {
		t.Errorf("MutationObserver did not deliver childList record:\n%s", out)
	}
}

func TestQueueMicrotask(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>queueMicrotask(function () { document.getElementById('out').textContent = 'after'; });</script>
		</body></html>`)
	if !strings.Contains(out, ">after<") || strings.Contains(out, "before") {
		t.Errorf("queueMicrotask did not run:\n%s", out)
	}
}
