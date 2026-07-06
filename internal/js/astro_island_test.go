package js_test

import (
	"strings"
	"testing"
)

// TestAstroIslandClientOnly renders an Astro `client:only` React island the way
// Astro ships one: the real, verbatim astro-island custom element
// (testdata/frameworks/astro-island-runtime.js) upgrades a pre-existing
// <astro-island>, whose start() dynamically import()s a cross-origin renderer +
// component and mounts a React root. The component's render calls a camelCase
// helper built on /^([A-Z])|[\s-_]+(\w)/g — the exact regex idiom goja rejects
// unless fixClassRangeHyphen escapes it. So this is both the end-to-end proof
// that unblink renders Astro islands and a regression guard that the regex fix
// holds inside the real hydration machinery. (poe.ninja is the field example.)
func TestAstroIslandClientOnly(t *testing.T) {
	runtime := loadBundle(t, "astro-island-runtime.js")

	// Cross-origin (page base is https://example.com/app) renderer + component ES
	// modules, served by URL substring and imported dynamically by the island's
	// start(). The component camelCases its prop with the /^([A-Z])|[\s-_]+(\w)/g
	// idiom that used to abort module compilation in goja (killing hydration
	// outright); the renderer writes the result into the island's light DOM and
	// fetches from the mount so a failure is also visible in diagnostics. It uses
	// a plain string render rather than a React root on purpose: React-via-dynamic-
	// import mount timing is covered elsewhere, and this test's job is the island
	// machinery + the regex fix, not the framework.
	const renderer = `export default (element) => (Component, props) => {
		fetch('/hit/widget');
		element.textContent = Component(props);
	};`
	const component = `const camel = (s) => s.replace(/^([A-Z])|[\s-_]+(\w)/g, (t,n,r) => r ? r.toUpperCase() : n.toLowerCase());
	export const Widget = (props) => camel(props.title);`

	routes := map[string]string{
		"//assets.test/renderer.mjs":  renderer,
		"//assets.test/component.mjs": component,
		"/hit/widget":                 `{"ok":true}`,
	}

	page := `<html><body>
		<astro-island uid="w1" component-url="https://assets.test/component.mjs" component-export="Widget"
			renderer-url="https://assets.test/renderer.mjs" client="only" ssr
			opts="{&quot;name&quot;:&quot;Widget&quot;,&quot;value&quot;:true}"
			props="{&quot;title&quot;:[0,&quot;foo bar-baz_qux&quot;]}"><!--astro:end--></astro-island>
		<script>(()=>{var e=async t=>{await(await t())()};(self.Astro||(self.Astro={})).only=e;window.dispatchEvent(new Event("astro:only"));})();</script>
		<script>` + runtime + `</script>
	</body></html>`

	out, diag := renderApp(t, page, routes)

	// The camelCased marker only appears if the component module imported and
	// compiled (regex fix applied) and the renderer wrote into the island.
	if !strings.Contains(out, "fooBarBazQux") {
		fired := false
		for _, r := range diag.Requests {
			if strings.Contains(r.URL, "/hit/widget") {
				fired = true
			}
		}
		t.Errorf("astro client:only island did not render (mount fired=%v)\nerrors=%v\n--- output ---\n%s",
			fired, diag.Errors, out)
	}
	// ssr is removed only when hydrate() completes.
	if strings.Contains(out, `ssr=""`) || strings.Contains(out, " ssr>") {
		t.Errorf("island still carries the ssr attribute — hydration did not complete\n%s", out)
	}
}
