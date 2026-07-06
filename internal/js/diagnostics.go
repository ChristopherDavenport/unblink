package js

import (
	"time"

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

	// Saturation: how the settle ended and how much network the page attempted, so
	// a caller can tell a page that went quiet from one cut off mid-work by the
	// budget. NetPending > 0 or (DeadlineHit && DOMBusy) means the snapshot was
	// taken while the page was still working — content may be incomplete. All are
	// filled by the engine after the settle, not by collectDiagnostics.
	NetRequests int   // subrequests attempted (fetch/XHR, scripts, modules, dynamic import); asset-cache hits bypass the transport and are not counted — this reports real network attempts
	NetFailed   int   // subrequests that errored (network failures and budget/rate denials)
	NetBytes    int64 // response-body bytes downloaded across subrequests (against the --js-max-bytes budget)
	NetPending  int   // subrequests still in flight when the snapshot was taken
	DeadlineHit bool  // settle closed by the JS budget deadline rather than by quiescence
	DOMBusy     bool  // the DOM was still mutating when the settle closed
	// TimersPending is the count of clamped one-shot timers still scheduled when
	// the settle closed. Long timers are clamped into the budget so most fire,
	// but a non-zero count flags that content may still be behind a timer —
	// raise wait_timeout / use wait_for.
	TimersPending int
	// SettledIdle reports that the settle closed via the provable-idle fast
	// path: no in-flight network and no live timers, so no mechanism existed to
	// run more JS and the snapshot is complete by construction. False means the
	// quiet-window heuristic (or the budget) closed it.
	SettledIdle bool

	// Requests is the ordered log of subrequests the page's JS made (fetch/XHR,
	// scripts, modules, dynamic import), for the requests tool; Console is the
	// page's captured console.* output. Both are capped — the *Truncated flag
	// reports dropped entries. Filled by fillCaptureLogs after the settle.
	Requests          []RequestLog
	RequestsTruncated bool
	Console           []ConsoleMsg
	ConsoleTruncated  bool

	// Timing: where the render's wall clock went, filled by the engine after the
	// settle. SetupDur covers loop acquisition + bridge install + prelude; ExecDur
	// covers script/module execution + lifecycle events; SettleDur covers the settle
	// poll (including any wait_for gate) up to loop teardown. TotalDur is the whole
	// render excluding time queued on the concurrency semaphore.
	SetupDur  time.Duration
	ExecDur   time.Duration
	SettleDur time.Duration
	TotalDur  time.Duration
}

// RequestLog is one subrequest the page's JavaScript made, for the requests tool.
type RequestLog struct {
	Method string
	URL    string
	Status int    // 0 when the request errored before a response
	Failed bool   // network error or budget/rate/SSRF denial
	Err    string // error text when Failed
	Kind   string // devtools-like resource type (html/js/xhr/css/fonts/images/media/other)
}

// ConsoleMsg is one captured page console.* call.
type ConsoleMsg struct {
	Level string // log|info|warn|error|debug|trace
	Text  string
}

// collectDiagnostics snapshots the bridge's diagnostics after a render. Runs on the
// loop goroutine. The request/console logs are filled separately by fillCaptureLogs
// *after* the settle completes (settlePoll only schedules the poll and returns), so
// they include async activity — a fetch or a console.log from a callback.
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

// fillCaptureLogs copies the accumulated request/console logs into diag. The engine
// calls it after the settle completes and the loop is terminated, so the reads are
// race-free (Terminate's join is the happens-before edge for the loop-written
// console buffer; the request log is mutex-guarded).
func (b *bridge) fillCaptureLogs(diag *RenderResult) {
	if diag == nil {
		return
	}
	diag.Console, diag.ConsoleTruncated = b.consoleSnapshot()
	if b.netCount != nil {
		recs, truncated := b.netCount.snapshotRecords()
		diag.RequestsTruncated = truncated
		diag.Requests = make([]RequestLog, len(recs))
		for i, r := range recs {
			diag.Requests[i] = RequestLog{
				Method: r.method, URL: r.url, Status: r.status,
				Failed: r.errMsg != "", Err: r.errMsg, Kind: r.kind,
			}
		}
	}
}

// maxConsoleMsgs / maxConsoleTextLen bound the console buffer so a log-spamming
// page can't grow it (or any single message) unbounded.
const (
	maxConsoleMsgs    = 200
	maxConsoleTextLen = 2000
)

// recordConsole appends a captured console.* message (loop goroutine, no lock —
// like recordError). Text is length-capped.
func (b *bridge) recordConsole(level, text string) {
	b.bgDebug("console."+level, text)
	if len(b.consoleLog) >= maxConsoleMsgs {
		b.consoleDropped++
		return
	}
	if len(text) > maxConsoleTextLen {
		text = text[:maxConsoleTextLen] + "…"
	}
	b.consoleLog = append(b.consoleLog, ConsoleMsg{Level: level, Text: text})
}

// consoleSnapshot returns a copy of the captured console messages and whether any
// were dropped past the cap.
func (b *bridge) consoleSnapshot() ([]ConsoleMsg, bool) {
	return append([]ConsoleMsg(nil), b.consoleLog...), b.consoleDropped > 0
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
