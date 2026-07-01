package js_test

import (
	"strings"
	"testing"
)

// Custom elements upgrade and render their (flattened) shadow content into the
// light tree, so an extractor sees it. These use vanilla web-component classes
// (the same lifecycle Lit drives); the real Lit bundle is exercised in Phase F.

func TestCustomElementUpgradeAndShadow(t *testing.T) {
	out := render(t, `<html><body><my-widget></my-widget>
		<script>
		  class MyWidget extends HTMLElement {
		    connectedCallback() {
		      var root = this.attachShadow({ mode: 'open' });
		      root.innerHTML = '<p class="w">widget body</p>';
		    }
		  }
		  customElements.define('my-widget', MyWidget);
		</script></body></html>`)
	if !strings.Contains(out, "widget body") {
		t.Errorf("custom element shadow content not rendered into tree:\n%s", out)
	}
}

func TestCustomElementInstanceAndState(t *testing.T) {
	out := render(t, `<html><body><x-counter></x-counter><div id="out"></div>
		<script>
		  class XCounter extends HTMLElement { constructor() { super(); this.n = 5; } }
		  customElements.define('x-counter', XCounter);
		  var el = document.querySelector('x-counter');
		  document.getElementById('out').textContent =
		    (el instanceof XCounter) + ':' + (el instanceof HTMLElement) + ':' + el.n;
		</script></body></html>`)
	if !strings.Contains(out, ">true:true:5<") {
		t.Errorf("upgraded element lost instanceof/state:\n%s", out)
	}
}

func TestObservedAttributeCallback(t *testing.T) {
	out := render(t, `<html><body><x-attr label="hello"></x-attr>
		<script>
		  class XAttr extends HTMLElement {
		    static get observedAttributes() { return ['label']; }
		    attributeChangedCallback(name, oldV, newV) { this.textContent = 'attr:' + name + '=' + newV; }
		  }
		  customElements.define('x-attr', XAttr);
		</script></body></html>`)
	if !strings.Contains(out, "attr:label=hello") {
		t.Errorf("attributeChangedCallback did not fire for observed attribute:\n%s", out)
	}
}

func TestTemplateContentClone(t *testing.T) {
	out := render(t, `<html><body><ul id="list"></ul>
		<script>
		  var tpl = document.createElement('template');
		  tpl.innerHTML = '<li class="ti">tmpl-item</li>';
		  document.getElementById('list').appendChild(tpl.content.cloneNode(true));
		</script></body></html>`)
	if !strings.Contains(out, `<li class="ti">tmpl-item</li>`) {
		t.Errorf("template.content clone not appended:\n%s", out)
	}
}

func TestWhenDefinedResolves(t *testing.T) {
	out := render(t, `<html><body><div id="out">waiting</div>
		<script>
		  customElements.whenDefined('x-late').then(function () {
		    document.getElementById('out').textContent = 'defined';
		  });
		  class XLate extends HTMLElement {}
		  customElements.define('x-late', XLate);
		</script></body></html>`)
	if !strings.Contains(out, ">defined<") {
		t.Errorf("whenDefined promise did not resolve:\n%s", out)
	}
}
