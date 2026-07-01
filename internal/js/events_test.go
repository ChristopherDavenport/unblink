package js_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

func TestEventBubbling(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  document.body.addEventListener('click', function (e) {
		    document.body.setAttribute('data-bubbled', e.target.tagName + '/' + e.currentTarget.tagName);
		  });
		  document.getElementById('b').click();
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-bubbled="BUTTON/BODY"`) {
		t.Errorf("click did not bubble from button to body:\n%s", out)
	}
}

func TestEventCaptureOrder(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var order = [];
		  document.body.addEventListener('click', function () { order.push('capture'); }, true);
		  document.getElementById('b').addEventListener('click', function () { order.push('target'); });
		  document.getElementById('b').click();
		  document.body.setAttribute('data-order', order.join(','));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-order="capture,target"`) {
		t.Errorf("capture should run before target:\n%s", out)
	}
}

func TestStopPropagation(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var fired = false;
		  document.body.addEventListener('click', function () { fired = true; });
		  document.getElementById('b').addEventListener('click', function (e) { e.stopPropagation(); });
		  document.getElementById('b').click();
		  document.body.setAttribute('data-bubbled', String(fired));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-bubbled="false"`) {
		t.Errorf("stopPropagation should prevent the ancestor handler:\n%s", out)
	}
}

func TestPreventDefault(t *testing.T) {
	out := renderWith(t, `<html><body><a id="a">x</a>
		<script>
		  var a = document.getElementById('a');
		  a.addEventListener('click', function (e) { e.preventDefault(); });
		  var ev = new Event('click', { bubbles: true, cancelable: true });
		  var result = a.dispatchEvent(ev);
		  document.body.setAttribute('data-result', String(result));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-result="false"`) {
		t.Errorf("dispatchEvent should return false when default prevented:\n%s", out)
	}
}

func TestRemoveEventListener(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var count = 0;
		  function h() { count++; }
		  var btn = document.getElementById('b');
		  btn.addEventListener('click', h);
		  btn.removeEventListener('click', h);
		  btn.click();
		  document.body.setAttribute('data-count', String(count));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-count="0"`) {
		t.Errorf("removed listener should not fire:\n%s", out)
	}
}

func TestDOMContentLoadedBubblesToWindow(t *testing.T) {
	// A window-registered DOMContentLoaded listener fires (it bubbles from document).
	out := renderWith(t, `<html><body>
		<script>
		  window.addEventListener('DOMContentLoaded', function () {
		    document.body.setAttribute('data-ready', 'yes');
		  });
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-ready="yes"`) {
		t.Errorf("DOMContentLoaded did not reach a window listener:\n%s", out)
	}
}

// stubCookies implements js.CookieJar with a simple in-memory store.
type stubCookies struct {
	mu    sync.Mutex
	store map[string]string
}

func (s *stubCookies) Cookies(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := make([]string, 0, len(s.store))
	for k, v := range s.store {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (s *stubCookies) SetCookie(_, cookie string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kv := strings.SplitN(strings.SplitN(cookie, ";", 2)[0], "=", 2)
	if len(kv) == 2 {
		s.store[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
}

func TestDocumentCookie(t *testing.T) {
	jar := &stubCookies{store: map[string]string{"a": "1"}}
	out := renderWithEnv(t, `<html><body>
		<script>
		  document.body.setAttribute('data-before', document.cookie);
		  document.cookie = 'b=2; path=/';
		</script></body></html>`, js.Env{Cookies: jar})

	if !strings.Contains(out, "a=1") {
		t.Errorf("document.cookie read did not return the seeded cookie:\n%s", out)
	}
	if jar.store["b"] != "2" {
		t.Errorf("document.cookie write did not reach the jar: %v", jar.store)
	}
}
