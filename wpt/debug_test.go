//go:build wpt

package wpt

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// TestWPTDebug renders a single WPT test (path via WPT_ONE, default a simple
// encoding test) and dumps whether the results node appeared, the render
// diagnostics, and a tail of the serialized DOM. Run:
//
//	WPT_ONE=encoding/api-basics.any.js go test -tags wpt ./wpt -run TestWPTDebug -v
func TestWPTDebug(t *testing.T) {
	root := repoRoot() + "/" + defaultRoot
	sc, err := loadScope()
	if err != nil {
		t.Fatal(err)
	}
	one := "encoding/api-basics.any.js"
	if v := os.Getenv("WPT_ONE"); v != "" {
		one = v
	}
	descs, _, err := enumerate(root, sc)
	if err != nil {
		t.Fatal(err)
	}
	var target *testDesc
	for i := range descs {
		if descs[i].RelPath == one {
			target = &descs[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("descriptor for %s not found among %d", one, len(descs))
	}
	t.Logf("descriptor: url=%s preskip=%q budget=%s", target.URLPath, target.Preskip, target.Budget)
	t.Logf("wrapper HTML:\n%s", target.HTML)

	doc, _ := html.Parse(strings.NewReader(target.HTML))
	base, _ := url.Parse("https://web-platform.test" + target.URLPath)
	diag := &js.RenderResult{}
	env := js.Env{
		Transport: &wptTransport{root: root},
		Wait:      &js.WaitCondition{Selector: "#__wpt_results"},
		Timeout:   target.Budget,
		Diag:      diag,
	}
	ctx, cancel := context.WithTimeout(context.Background(), target.Budget+3*time.Second)
	defer cancel()
	rerr := eng().Render(ctx, doc, base, env)
	t.Logf("render err: %v", rerr)
	t.Logf("diag: WaitRequested=%v WaitMet=%v SettledIdle=%v NetReq=%d NetFailed=%d NetPending=%d DeadlineHit=%v errors=%v",
		diag.WaitRequested, diag.WaitMet, diag.SettledIdle, diag.NetRequests, diag.NetFailed, diag.NetPending, diag.DeadlineHit, diag.Errors)

	payload, found := extractResults(doc)
	t.Logf("results found=%v", found)
	if found {
		t.Logf("harness_status=%d msg=%q tests=%d", payload.HarnessStatus, payload.HarnessMessage, len(payload.Tests))
		for i, tc := range payload.Tests {
			if i >= 8 {
				t.Logf("  ... (%d more)", len(payload.Tests)-8)
				break
			}
			t.Logf("  [%d] status=%d %q %s", tc.Status, tc.Status, tc.Name, firstLine(tc.Message))
		}
	}
	var buf bytes.Buffer
	_ = html.Render(&buf, doc)
	s := buf.String()
	if len(s) > 1200 {
		s = s[len(s)-1200:]
	}
	t.Logf("serialized DOM tail:\n%s", s)
}

func eng() *js.Engine { return js.New(js.WithTimeout(defaultBudget)) }
