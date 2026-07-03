package browser

import (
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/page"
)

// The memo is tied to its cache entry by pointer identity: a replaced page
// must never inherit the old page's markdown, a 304 touch must keep it, and
// eviction drops both together.
func TestCacheMemoIdentity(t *testing.T) {
	c := newCache(time.Minute, 4)
	p1, p2 := &page.Page{}, &page.Page{}

	c.put("k", p1)
	m := c.memoFor("k", p1)
	if m == nil {
		t.Fatal("no memo for a freshly stored entry")
	}
	m.store(memoKey{mode: "article", safeOutput: true}, memoVal{markdown: "one"})

	if got := c.memoFor("k", p2); got != nil {
		t.Fatal("memoFor matched a different page pointer")
	}

	c.touch("k")
	m2 := c.memoFor("k", p1)
	if m2 != m {
		t.Fatal("touch (304 revalidation) did not preserve the memo")
	}
	if v, ok := m2.lookup(memoKey{mode: "article", safeOutput: true}); !ok || v.markdown != "one" {
		t.Fatal("memo value lost across touch")
	}

	c.put("k", p2) // refetch replaced the page: fresh memo, old one unreachable
	if got := c.memoFor("k", p1); got != nil {
		t.Fatal("stale page still resolves a memo after replacement")
	}
	fresh := c.memoFor("k", p2)
	if fresh == nil || fresh == m {
		t.Fatal("replacement entry did not get a fresh memo")
	}
	if _, ok := fresh.lookup(memoKey{mode: "article", safeOutput: true}); ok {
		t.Fatal("fresh memo inherited the old entry's markdown")
	}
}
