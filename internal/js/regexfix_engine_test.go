package js_test

import (
	"strings"
	"testing"
)

// A regex character class with a hyphen right after a \s/\d/\w shorthand — e.g.
// the ubiquitous camelCase/slugify helper /[\s-_]+/ — is valid ECMAScript but
// goja rejects it at compile time ("invalid character class range"), which used
// to abort the whole enclosing script/module and silently break SPA hydration.
// The engine now escapes that hyphen before compiling (see fixClassRangeHyphen),
// so the script runs. These assert it end-to-end on the classic and module paths.

func TestShorthandHyphenRegexClassicScript(t *testing.T) {
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  var camel = function(s){
		    return s.replace(/^([A-Z])|[\s-_]+(\w)/g, function(t,n,r){ return r?r.toUpperCase():n.toLowerCase(); });
		  };
		  document.getElementById('out').textContent = camel('foo bar-baz_qux');
		</script></body></html>`, nil)
	if !strings.Contains(out, "fooBarBazQux") {
		t.Errorf("classic script with /[\\s-_]/ regex did not run to completion\n--- output ---\n%s", out)
	}
}

// The poe.ninja failure path: the offending regex lived in a dynamically
// imported cross-origin module. This mirrors it — a classic script import()s a
// module whose body contains /[\s-_]/, then calls its export. Pre-fix the module
// failed to compile and the import rejected; post-fix it resolves and runs.
func TestShorthandHyphenRegexDynamicImport(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/slug.mjs": `export const camel = (s) => s.replace(/^([A-Z])|[\s-_]+(\w)/g, (t,n,r) => r ? r.toUpperCase() : n.toLowerCase());`,
	}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  import('https://cdn.example.org/slug.mjs').then(function(m){
		    document.getElementById('out').textContent = m.camel('foo bar-baz_qux');
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, "fooBarBazQux") {
		t.Errorf("dynamically imported module with /[\\s-_]/ regex did not run to completion\n--- output ---\n%s", out)
	}
}
