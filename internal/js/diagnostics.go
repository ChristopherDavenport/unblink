package js

import (
	"github.com/dop251/goja"
)

// RenderResult is optional, transport-agnostic diagnostics about a render: which
// framework was detected, any uncaught script/upgrade errors, and how many custom
// elements upgraded. It carries no MCP/browser types; the caller opts in by setting
// Env.Diag, and maps it onto whatever it surfaces.
type RenderResult struct {
	Framework string   // "react" | "vue" | "preact" | "svelte" | "webcomponents" | ""
	Errors    []string // uncaught script/module/upgrade exceptions (best-effort)
	Upgrades  int      // custom elements upgraded

	// WaitRequested/WaitMet report a wait_for gate: WaitRequested is true when the
	// caller set Env.Wait, WaitMet whether it held before the render returned. Both
	// are filled by the engine after the settle, not by collectDiagnostics.
	WaitRequested bool
	WaitMet       bool

	// PendingNavigation is a URL the page's JS asked to navigate to (location.href /
	// assign / replace) that unblink did not follow — best-effort, empty when none.
	PendingNavigation string
}

// collectDiagnostics snapshots the bridge's diagnostics after a render. Runs on the
// loop goroutine.
func (b *bridge) collectDiagnostics() RenderResult {
	errs := append([]string(nil), b.diagErrors...)
	for p := range b.rejections {
		errs = append(errs, "unhandled rejection: "+rejectionText(p))
	}
	return RenderResult{
		Framework: b.detectFramework(),
		Errors:    errs,
		Upgrades:  len(b.upgraded),
	}
}

func rejectionText(p *goja.Promise) string {
	if r := p.Result(); r != nil {
		return r.String()
	}
	return "(no reason)"
}

// detectFramework fingerprints the page by probing well-known globals and DOM
// markers. Best-effort and order-sensitive (most specific first).
func (b *bridge) detectFramework() string {
	has := func(global string) bool {
		v := b.vm.GlobalObject().Get(global)
		return v != nil && !goja.IsUndefined(v) && !goja.IsNull(v)
	}
	domHas := func(sel string) bool { return query(b.doc, sel) != nil }
	switch {
	case has("React") || has("ReactDOM") || domHas("[data-reactroot]") || domHas("#__next") || domHas("script#__NEXT_DATA__"):
		return "react"
	case has("Vue") || has("__VUE__") || domHas("[data-v-app]") || domHas("[data-server-rendered]"):
		return "vue"
	case has("preact") || has("__PREACT_DEVTOOLS__"):
		return "preact"
	case len(b.customElements) > 0:
		return "webcomponents"
	case domHas("[class*='svelte-']"):
		return "svelte"
	default:
		return ""
	}
}
