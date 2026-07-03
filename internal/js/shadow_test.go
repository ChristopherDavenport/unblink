package js_test

import (
	"strings"
	"testing"
)

// Phase 23: encapsulating Shadow DOM. Shadow content lives in a detached subtree (real
// boundary for page-JS queries) and is flattened — with <slot> distribution — into the
// light tree by the compose pass so extraction still sees it.

func TestShadowSlotDistribution(t *testing.T) {
	out := render(t, `<html><body><my-card><span slot="title">Card Title</span><p>Card body text</p></my-card>
		<script>
		  class MyCard extends HTMLElement {
		    connectedCallback() {
		      this.attachShadow({mode:'open'}).innerHTML =
		        '<section class="card"><h2><slot name="title"></slot></h2><div class="body"><slot></slot></div></section>';
		    }
		  }
		  customElements.define('my-card', MyCard);
		</script></body></html>`)
	if !strings.Contains(out, `<section class="card">`) {
		t.Fatalf("shadow scaffold missing from composed output:\n%s", out)
	}
	if !strings.Contains(out, `<h2><span slot="title">Card Title</span></h2>`) {
		t.Errorf("named slot content not composed into its slot position:\n%s", out)
	}
	if !strings.Contains(out, `<div class="body"><p>Card body text</p></div>`) {
		t.Errorf("default slot content not composed into its slot position:\n%s", out)
	}
	// The composed <my-card> subtree must contain no <slot> placeholder (checking the
	// whole doc would false-match the <slot> literals in the inline <script> source).
	if card := out[strings.Index(out, "<my-card>"):strings.Index(out, "</my-card>")]; strings.Contains(card, "<slot") {
		t.Errorf("raw <slot> leaked into composed output:\n%s", card)
	}
}

// A component that sets shadowRoot.innerHTML after light children exist must NOT destroy
// them (the old flat model did, because the shadow root aliased the host).
func TestShadowSlottedContentSurvivesInnerHTML(t *testing.T) {
	out := render(t, `<html><body><x-wrap>KEEP ME</x-wrap>
		<script>
		  class XWrap extends HTMLElement {
		    connectedCallback(){ this.attachShadow({mode:'open'}).innerHTML = '<div class="w"><slot></slot></div>'; }
		  }
		  customElements.define('x-wrap', XWrap);
		</script></body></html>`)
	if !strings.Contains(out, `<div class="w">KEEP ME</div>`) {
		t.Fatalf("slotted light text destroyed/misplaced by shadow innerHTML:\n%s", out)
	}
}

func TestShadowSlotFallback(t *testing.T) {
	out := render(t, `<html><body><x-fb></x-fb>
		<script>
		  class XFb extends HTMLElement {
		    connectedCallback(){ this.attachShadow({mode:'open'}).innerHTML = '<div><slot>fallback text</slot></div>'; }
		  }
		  customElements.define('x-fb', XFb);
		</script></body></html>`)
	if !strings.Contains(out, "fallback text") {
		t.Errorf("slot fallback content missing when nothing is assigned:\n%s", out)
	}
}

// A closed root is invisible to page JS (.shadowRoot === null) but its content is still
// composed into extraction output (mission: see everything).
func TestShadowClosedModeHiddenButComposed(t *testing.T) {
	out := render(t, `<html><body><x-closed></x-closed><div id="probe"></div>
		<script>
		  class XClosed extends HTMLElement {
		    connectedCallback(){
		      this.attachShadow({mode:'closed'}).innerHTML = '<p>secret content</p>';
		      document.getElementById('probe').textContent = 'sr:' + (this.shadowRoot === null);
		    }
		  }
		  customElements.define('x-closed', XClosed);
		</script></body></html>`)
	if !strings.Contains(out, "sr:true") {
		t.Errorf(".shadowRoot should be null for a closed root:\n%s", out)
	}
	if !strings.Contains(out, "secret content") {
		t.Errorf("closed shadow content must still be composed into extraction output:\n%s", out)
	}
}

func TestSlotAssignedNodesAndElements(t *testing.T) {
	out := render(t, `<html><body><x-an><b>one</b><i slot="x">two</i></x-an><div id="o"></div>
		<script>
		  class XAn extends HTMLElement {
		    connectedCallback(){
		      this.attachShadow({mode:'open'}).innerHTML = '<slot></slot><slot name="x"></slot>';
		      var def = this.shadowRoot.querySelector('slot:not([name])');
		      var named = this.shadowRoot.querySelector('slot[name="x"]');
		      document.getElementById('o').textContent =
		        'def=' + def.assignedElements().map(function(e){ return e.tagName; }).join(',') +
		        ';named=' + named.assignedNodes().length;
		    }
		  }
		  customElements.define('x-an', XAn);
		</script></body></html>`)
	if !strings.Contains(out, "def=B;named=1") {
		t.Errorf("assignedElements/assignedNodes wrong:\n%s", out)
	}
}

// connectedCallback must still fire for a custom element nested inside another
// component's (now detached) shadow content — the connectivity bridge over shadowHostOf.
func TestNestedShadowComponentConnected(t *testing.T) {
	out := render(t, `<html><body><x-outer></x-outer><div id="o"></div>
		<script>
		  class XInner extends HTMLElement { connectedCallback(){ document.getElementById('o').textContent += 'inner-connected;'; } }
		  class XOuter extends HTMLElement { connectedCallback(){ this.attachShadow({mode:'open'}).innerHTML = '<x-inner></x-inner>'; } }
		  customElements.define('x-inner', XInner);
		  customElements.define('x-outer', XOuter);
		</script></body></html>`)
	if !strings.Contains(out, "inner-connected;") {
		t.Errorf("connectedCallback did not fire for a component nested in another's shadow:\n%s", out)
	}
}

// A page-JS document.querySelector must NOT reach into a shadow tree (real encapsulation),
// but the shadow-internal querySelector must still work.
func TestShadowQueryBoundary(t *testing.T) {
	out := render(t, `<html><body><x-enc></x-enc><div id="o"></div>
		<script>
		  class XEnc extends HTMLElement {
		    connectedCallback(){ this.attachShadow({mode:'open'}).innerHTML = '<span class="inside">x</span>'; }
		  }
		  customElements.define('x-enc', XEnc);
		  var outside = document.querySelector('.inside');           // must not cross the boundary
		  var inside = document.querySelector('x-enc').shadowRoot.querySelector('.inside');
		  document.getElementById('o').textContent = 'outside=' + (outside === null) + ';inside=' + (inside !== null);
		</script></body></html>`)
	if !strings.Contains(out, "outside=true;inside=true") {
		t.Errorf("shadow query boundary wrong (document query should miss, shadow query should hit):\n%s", out)
	}
}

// --- cross-boundary event propagation ---

// A composed click on a shadow-internal button bubbles to a delegated document listener,
// which sees the retargeted host as target; the shadow-internal listener sees the real node.
func TestShadowComposedEventCrossesBoundary(t *testing.T) {
	out := render(t, `<html><body><x-btn></x-btn><div id="log"></div>
		<script>
		  var log = document.getElementById('log');
		  document.addEventListener('click', function(e){ log.textContent += 'doc:' + e.target.tagName + ';'; });
		  class XBtn extends HTMLElement {
		    connectedCallback(){
		      var r = this.attachShadow({mode:'open'});
		      r.innerHTML = '<button id="b">go</button>';
		      var b = r.querySelector('#b');
		      b.addEventListener('click', function(e){ log.textContent += 'inner:' + e.target.id + ';'; });
		      b.click();
		    }
		  }
		  customElements.define('x-btn', XBtn);
		</script></body></html>`)
	if !strings.Contains(out, "inner:b;") {
		t.Errorf("shadow-internal click listener did not fire with the real target:\n%s", out)
	}
	if !strings.Contains(out, "doc:X-BTN;") {
		t.Errorf("composed click did not bubble to document with retargeted host target:\n%s", out)
	}
}

func TestShadowComposedPath(t *testing.T) {
	out := render(t, `<html><body><x-cp></x-cp><div id="o"></div>
		<script>
		  class XCp extends HTMLElement {
		    connectedCallback(){
		      var r = this.attachShadow({mode:'open'});
		      r.innerHTML = '<button id="b">x</button>';
		      var b = r.querySelector('#b');
		      b.addEventListener('click', function(e){
		        var tags = e.composedPath().map(function(n){
		          return n === document ? '#doc' : (n === window ? '#win' : (n.tagName || n.nodeName));
		        });
		        document.getElementById('o').textContent = tags.join('>');
		      });
		      b.click();
		    }
		  }
		  customElements.define('x-cp', XCp);
		</script></body></html>`)
	for _, want := range []string{"BUTTON", "X-CP", "#doc", "#win"} {
		if !strings.Contains(out, want) {
			t.Errorf("composedPath missing %q:\n%s", want, out)
		}
	}
}

// bubbles and composed are orthogonal: a non-bubbling composed event (focus) still fires a
// document CAPTURE listener across the boundary (retargeted), but no bubble-phase listener.
func TestNonBubblingComposedFiresCaptureAcrossBoundary(t *testing.T) {
	out := render(t, `<html><body><x-nb></x-nb><div id="o"></div>
		<script>
		  var seen = [];
		  document.addEventListener('focus', function(e){ seen.push('capture:' + e.target.tagName); }, true);
		  document.addEventListener('focus', function(e){ seen.push('bubble:' + e.target.tagName); }, false);
		  class XNb extends HTMLElement {
		    connectedCallback(){
		      this.attachShadow({mode:'open'}).innerHTML = '<input id="i">';
		      this.shadowRoot.querySelector('#i').focus();
		      document.getElementById('o').textContent = seen.join(';');
		    }
		  }
		  customElements.define('x-nb', XNb);
		</script></body></html>`)
	// Assert on the result marker (>...<) — the whole doc includes the <script> source,
	// which contains the literal strings "capture:" and "bubble:".
	if !strings.Contains(out, ">capture:X-NB<") {
		t.Errorf("non-bubbling composed focus should fire a document capture listener with retargeted target:\n%s", out)
	}
	if strings.Contains(out, "bubble:X-NB") {
		t.Errorf("a non-bubbling event must not fire bubble-phase listeners:\n%s", out)
	}
}

// A non-composed event confined to a shadow tree must not reach the document.
func TestNonComposedEventStaysInShadow(t *testing.T) {
	out := render(t, `<html><body><x-nc></x-nc><div id="o"></div>
		<script>
		  var hit = 'no';
		  document.addEventListener('ping', function(){ hit = 'yes'; });
		  class XNc extends HTMLElement {
		    connectedCallback(){
		      this.attachShadow({mode:'open'}).innerHTML = '<span id="s">x</span>';
		      this.shadowRoot.querySelector('#s').dispatchEvent(new Event('ping', {bubbles:true, composed:false}));
		      document.getElementById('o').textContent = 'hit:' + hit;
		    }
		  }
		  customElements.define('x-nc', XNc);
		</script></body></html>`)
	if !strings.Contains(out, "hit:no") {
		t.Errorf("a non-composed event must not cross the shadow boundary to document:\n%s", out)
	}
}
