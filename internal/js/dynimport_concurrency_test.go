package js_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// concurrencyProbeTransport records the maximum number of simultaneously in-flight
// requests, so a test can prove dynamic import() chunks fetch concurrently rather
// than one-at-a-time. Each request holds a short observable window.
type concurrencyProbeTransport struct {
	mu       sync.Mutex
	inflight int
	maxSeen  int
	hits     int
}

func (s *concurrencyProbeTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	s.mu.Lock()
	s.inflight++
	s.hits++
	if s.inflight > s.maxSeen {
		s.maxSeen = s.inflight
	}
	s.mu.Unlock()

	time.Sleep(40 * time.Millisecond) // overlap window

	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()

	// The URL path's last segment (c0..cN) becomes the chunk's default export.
	seg := url[strings.LastIndex(url, "/")+1:]
	name := strings.TrimSuffix(seg, ".mjs")
	body := "export default '" + name + "';"
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/javascript"}, Body: []byte(body), FinalURL: url}, nil
}

func (s *concurrencyProbeTransport) max() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxSeen
}

// TestDynamicImportsFetchConcurrently is the Option-2 proof: a page that lazy-imports
// several chunks via Promise.all must fetch them concurrently (max in-flight > 1),
// not serially (max in-flight == 1, the old __unblinkImportSync behavior). It also
// confirms every chunk still renders correctly.
func TestDynamicImportsFetchConcurrently(t *testing.T) {
	const n = 6
	tr := &concurrencyProbeTransport{}

	var imports []string
	for i := 0; i < n; i++ {
		imports = append(imports, fmt.Sprintf("import('/c%d.mjs')", i))
	}
	page := `<html><body><div id="out">before</div>
		<script>
		  Promise.all([` + strings.Join(imports, ", ") + `]).then(function (mods) {
		    document.getElementById("out").textContent =
		      'cache-marker:' + mods.map(function (m) { return m.default; }).join(',');
		  });
		</script></body></html>`

	out := renderWith(t, page, tr)

	if !strings.Contains(out, "cache-marker:c0,c1,c2,c3,c4,c5") {
		t.Errorf("not all lazy chunks rendered:\n%s", out)
	}
	if got := tr.max(); got < 2 {
		t.Errorf("dynamic imports did not overlap: max in-flight = %d, want >= 2 (serial regression)", got)
	} else {
		t.Logf("max concurrent in-flight fetches: %d (of %d imports)", got, n)
	}
}
