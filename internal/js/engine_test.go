package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// render parses pageHTML, runs scripts over it, and returns the serialized result.
func render(t *testing.T, pageHTML string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(2 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String()
}

func TestTextContentSet(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>document.getElementById("out").textContent = "after-" + (1+1);</script>
		</body></html>`)
	if !strings.Contains(out, "after-2") || strings.Contains(out, "before") {
		t.Errorf("textContent not applied:\n%s", out)
	}
}

func TestInnerHTMLSet(t *testing.T) {
	out := render(t, `<html><body><div id="app"></div>
		<script>document.querySelector("#app").innerHTML = "<h1>Hydrated Heading</h1><p>body</p>";</script>
		</body></html>`)
	if !strings.Contains(out, "<h1>Hydrated Heading</h1>") {
		t.Errorf("innerHTML subtree missing:\n%s", out)
	}
}

func TestCreateAndAppend(t *testing.T) {
	out := render(t, `<html><body><ul id="list"></ul>
		<script>
		  var ul = document.getElementById("list");
		  var li = document.createElement("li");
		  li.textContent = "item one";
		  ul.appendChild(li);
		</script></body></html>`)
	if !strings.Contains(out, "<li>item one</li>") {
		t.Errorf("appended element missing:\n%s", out)
	}
}

func TestClassList(t *testing.T) {
	out := render(t, `<html><body><div id="x" class="a"></div>
		<script>
		  var x = document.getElementById("x");
		  x.classList.add("b", "c");
		  x.classList.remove("a");
		</script></body></html>`)
	if !strings.Contains(out, `class="b c"`) {
		t.Errorf("classList not applied:\n%s", out)
	}
}

func TestIdentity(t *testing.T) {
	out := render(t, `<html><body><div id="x"></div>
		<script>
		  var a = document.getElementById("x");
		  var b = document.querySelector("#x");
		  a.setAttribute("data-same", String(a === b));
		</script></body></html>`)
	if !strings.Contains(out, `data-same="true"`) {
		t.Errorf("node identity (el === el) not preserved:\n%s", out)
	}
}

func TestAsyncSettles(t *testing.T) {
	// A setTimeout(0) + Promise mutation must complete before Render returns.
	out := render(t, `<html><body><div id="out">x</div>
		<script>
		  Promise.resolve().then(function () {
		    setTimeout(function () {
		      document.getElementById("out").textContent = "async-done";
		    }, 0);
		  });
		</script></body></html>`)
	if !strings.Contains(out, "async-done") {
		t.Errorf("event loop did not settle async work:\n%s", out)
	}
}

func TestInfiniteLoopInterrupted(t *testing.T) {
	// A runaway synchronous loop must be interrupted by the wall-clock guard, and
	// prior mutations must survive.
	doc, _ := html.Parse(strings.NewReader(`<html><body><div id="out">start</div>
		<script>
		  document.getElementById("out").textContent = "mutated";
		  while (true) {}
		</script></body></html>`))
	eng := js.New(js.WithTimeout(150 * time.Millisecond))

	start := time.Now()
	if err := eng.Render(context.Background(), doc, nil, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("interrupt did not fire promptly: took %s", elapsed)
	}
	var buf bytes.Buffer
	_ = html.Render(&buf, doc)
	if !strings.Contains(buf.String(), "mutated") {
		t.Errorf("pre-loop mutation lost after interrupt:\n%s", buf.String())
	}
}

func TestNoScriptsUnchanged(t *testing.T) {
	in := `<html><head><title>t</title></head><body><p>hello</p></body></html>`
	out := render(t, in)
	if !strings.Contains(out, "<p>hello</p>") {
		t.Errorf("content changed unexpectedly:\n%s", out)
	}
}
