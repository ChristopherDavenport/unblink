package js_test

import (
	"strings"
	"testing"
)

// Compact notation is what a "61.1K stars" badge is formatted with; without it
// the client rendered "61,110" over the server's "61.1K" (a hydration mismatch
// on nuxt.com, and visibly different text on solidjs.com).
func TestIntlNumberFormatCompactAndSignificant(t *testing.T) {
	out := renderWith(t, `<html><body><div id="out">x</div><script>
	  var o = [];
	  function c(n, opts) { opts = opts || {}; opts.notation = 'compact'; return new Intl.NumberFormat('en', opts).format(n); }
	  function f(n, opts) { return new Intl.NumberFormat('en', opts).format(n); }
	  o.push('a=' + [c(61110, { maximumFractionDigits: 1 }), c(36939, { maximumFractionDigits: 1 }), c(1234), c(12345), c(123456), c(1234567), c(999), c(0), c(1.234), c(12.5)].join(';'));
	  o.push('b=' + [c(999999), c(999950, { maximumFractionDigits: 1 }), c(1500000000), c(2.5e12), c(-4200), c(1000)].join(';'));
	  o.push('c=' + [c(61110, { compactDisplay: 'long' }), c(1200000, { compactDisplay: 'long' }), c(61110, { minimumFractionDigits: 2, maximumFractionDigits: 2 })].join(';'));
	  o.push('d=' + [f(1234.5678, { maximumSignificantDigits: 3 }), f(0.012345, { maximumSignificantDigits: 2 }), f(5, { minimumSignificantDigits: 3 }), f(123456, { maximumSignificantDigits: 2 })].join(';'));
	  o.push('e=' + [f(1234.5), f(1234.5678), f(0.5, { style: 'percent' }), f(12.5, { style: 'currency', currency: 'USD' }), f(1234.5, { minimumFractionDigits: 2 }), (61110).toLocaleString('en-US')].join(';'));
	  document.getElementById('out').textContent = o.join(' | ');
	</script></body></html>`, nil)
	for _, want := range []string{
		"a=61.1K;36.9K;1.2K;12K;123K;1.2M;999;0;1.2;13",
		"b=1M;1M;1.5B;2.5T;-4.2K;1K",
		"c=61 thousand;1.2 million;61.11K",
		"d=1,230;0.012;5.00;120,000",
		"e=1,234.5;1,234.568;50%;$12.50;1,234.50;61,110",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("%s", out[i:min(len(out), i+500)])
	}
}
