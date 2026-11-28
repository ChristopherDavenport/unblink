# ADR 0004: Settle closes on provable idleness, not only a quiet window

Date: 2026-07-03
Status: accepted

## Context

A `Start()`ed goja event loop never drains its job queue (a background
keepalive holds +1, and any `setInterval` pins it forever), so "the page
settled" cannot be observed — it has to be inferred. Since Phase 12 the
inference was a quiet-window heuristic: no DOM mutation, no in-flight network,
and no clamped one-shot timers for 4 consecutive 15 ms ticks (≈60 ms), bounded
by the render budget. That window is a correctness constant — 30 ms proved too
aggressive (a framework idling between microtask batches, or an XHR scheduled
but not yet dispatched, could trip it and return a half-hydrated page).

The cost: **every render with any script paid the full ~60 ms even when the
page was finished at the first check.** Measured on the render benchmarks,
`RenderMinimal` was 62.8 ms of which ~60 ms was the settle wait; the floor was
75–95 % of per-render latency once caches were warm, and the dominant reason
unblink's SPA latency trailed warm-browser MCP tools (~15–22 ms/page).

The key observation is that the sandbox already instruments every mechanism
that can run more JavaScript:

- **Network** — fetch/XHR (`fetchPromise`) and dynamically-inserted
  `<script src>` chunks (`loadExternalScript`) bracket the off-loop request
  with `bridge.pending`, incremented *synchronously on-loop* before the fetch
  goroutine spawns — no race window.
- **Macrotasks** — the prelude wraps `setTimeout`/`setInterval` into a handle
  table (`live`) for zone.js-style numeric-id compatibility; rAF,
  requestIdleCallback, `AbortSignal.timeout`, `window.postMessage`, and
  MessagePort delivery all route through the wrapped `setTimeout`. Go-side
  keepalives captured the natives *before* the wrapper, so they are excluded
  by construction.
- **Microtasks** — goja drains the promise-job queue when the outermost JS
  frame returns; settle checks run in Go between loop jobs, after any drain.
- **Modules** — `runModules`/dynamic `import()` execute synchronously on-loop.

One mechanism escaped: goja's event loop exposes a **native `setImmediate`**
that the prelude did not wrap, so immediates were invisible to any audit —
and React's scheduler prefers `setImmediate` over MessageChannel when it
exists.

## Decision

1. The prelude re-routes `setImmediate`/`clearImmediate` through the wrapped
   `setTimeout`, closing the leak; the timer audit now reports
   `{c: clamped one-shots unfired, t: all live wrapped timers}`.
2. `settlePoll` gains a first tier above the quiet-window heuristic:
   **provable idleness**. When the wait condition is satisfied,
   `pending == 0`, and the live-timer count is 0, no mechanism exists to run
   more JavaScript — the page cannot change again, so the poll closes after a
   single ~1 ms confirmation tick instead of waiting out the window. The
   confirmation tick is defense-in-depth against a future work source that
   escapes the audit; it is documented as droppable with field evidence.
3. Anything armed (an interval, a pending timer, in-flight network) falls back
   to the quiet-window heuristic **unchanged at 60 ms of wall clock**, now
   detected at 5 ms granularity (`settleTick` 15 ms → 5 ms) with the first
   check running synchronously at poll entry. Clamped one-shot timers still
   hold the window open; unclamped live timers still do not (an interval must
   not pin the settle to the budget).
4. Renders that close via the fast path report `SettledIdle: true` in the
   render diagnostics, so a field regression is attributable.

## The load-bearing invariant

Provable idleness is a *proof* only while **every** source of future JS work
is visible to the settle check: network through the `pending` bracket,
macrotasks through the audited wrapped-timer table. **Adding a new async
primitive to the sandbox (a real WebSocket, a Worker, a native scheduler API,
a second timer surface) requires routing it through one of those two channels
or extending the audit** — otherwise the fast path can snapshot between that
primitive's callbacks. `TestRenderSetImmediateChainContentLands` is the
template regression test for this class of leak.

## Consequences

- Idle pages settle in ~1–3 ms instead of ~60 ms; `RenderMinimal` drops from
  ~63 ms to ~3 ms and framework renders land in warm-browser territory. Live
  `interact` dispatches whose handlers finish synchronously no longer pay the
  window per click.
- Pages with real deferred work (timers, network, intervals) behave exactly as
  before: the 60 ms-quiet-after-last-activity semantics and the budget bound
  are unchanged, as are the saturation diagnostics
  (`net_pending`/`dom_busy`/`render_budget_hit`/`timers_pending`).
- `wait_for`/`wait_text` remain the caller-side correctness tool: a non-empty
  unmet condition disables the fast path and holds the poll to the budget,
  exactly as before.
- The settle constants stay compile-time (no flags): the fast path is safe by
  construction, and the heuristic window remains a measured correctness
  constant, not a tuning knob.

## Amendment (2026-11-27): queued tasks hold the quiet window, up to a bound

**What was wrong.** Decision 3 says unclamped live timers do not hold the quiet
window. That is right for timers and wrong for one class of work that the
prelude had folded into them: `setImmediate`, `MessageChannel` and
`window.postMessage` were delivered through the wrapped `setTimeout(…, 0)`, so
a scheduler's continuation looked like any other live timer. React's work loop
yields every ~5 ms through `setImmediate` and, during hydration, mutates
nothing until it commits. A hydration longer than the 60 ms window with no
request in flight therefore read as quiet, and the render closed in the middle
of it. The real-page corpus showed it on Docusaurus and Next.js pages that
stopped at 16 requests and reached several hundred once the window was held.

**Decision.** The prelude gives those three primitives their own entry point
(`queueTask`): still a zero-delay native timer, still in the `live` table — so
decision 2's proof is untouched, a pending task keeps the page from being
provably idle exactly as before — and additionally counted in a `tasks` table.
The audit reports `{c, t, q}`. In the quiet-window tier a tick that sees
`q > 0` is not quiet, the same as a tick that sees a DOM mutation.

The hold is bounded: pending tasks may keep the window open for
`settleTaskHoldMax` (2 s) in total per settle, never past the budget. After
that they are ignored and the window behaves as in decision 3. A page that
re-queues itself forever through these primitives costs 2 s, not the budget;
time-sliced work that needs longer is cut off, as all of it was before.
`RenderResult.TasksHeld` reports the time held.

**What is deliberately not a task.** `setTimeout(fn, 0)` and
`requestAnimationFrame` keep decision 3's behaviour. Both have delay
semantics and are how pages idle, poll and animate; a zero-delay timer loop or
an animation loop must not hold a render. The three primitives chosen have no
delay at all — "run this as soon as the current task returns" is their only
meaning — which is what makes a pending one evidence of unfinished work rather
than of a page ticking over. `requestIdleCallback` is also not one: it fires
within a millisecond and inside the window regardless.

**MessagePort delivery, corrected on the way.** A port's flush drained its
whole queue in one task, including messages posted from inside a handler.
"Post a message to myself to yield" therefore never yielded, and an endless
ping-pong was a synchronous loop only the render watchdog could stop. A flush
now delivers the messages queued when it began; later ones get the next task.

**The invariant, restated.** A new primitive that means "continue as soon as
possible" (`scheduler.postTask`, `scheduler.yield`, a real `queueMicrotask`
replacement that defers to a task) must go through `queueTask`, or its
continuation chains will be cut at the quiet window again. One with a delay
goes through the wrapped `setTimeout` as before.

**Evidence.** `TestSettleWaitsForTimeSlicedWork` (20 slices of 10 ms through
each primitive complete; on the previous code the `setImmediate` and
`postMessage` cases close after ~80 ms), `TestSettleBoundsAnEndlessTaskChain`
(an endless chain settles just past the bound, not at an 8 s budget),
`TestSettleDoesNotHoldForTimerLoops`, `TestSettleIdleTierUnaffectedByTaskHold`.
`RenderMinimal` is unchanged (2.40 ms → 2.41 ms). On the corpus no page's
score moved by more than 0.002; `jest-docs`, `tailwind-docs` and
`storybook-docs` now run their hydration to the end (16 → 356, 16 → 344 and
23 → 132 requests), and total replay time rose from 88.5 s to 91.9 s.

## Amendment (2026-11-28): pulled-in timers are run when the page is otherwise quiet

The timer clamp pulls a one-shot timer that would outlast the budget in to
600 ms before the budget ends, and the settle held open until it fired. That is
correct for content behind a timer and expensive for everything else: on the
real-page corpus 14 of 74 pages waited about 4.4 s for a version poll, an idle
animation or a timeout guard, and settle wait was 83% of all read time (median
read 341 ms, 90th percentile 4.5 s).

The settle now **fast-forwards**: when the only thing pending is pulled-in
timers — no request in flight, no DOM change, no queued task — it runs them, in
the order they were originally due, one task each so microtasks drain between
them. "Nothing else pending" uses the two tiers this ADR already has: at once
when no other timer is live, after the quiet window otherwise.

The invariant is unchanged and no primitive is added: the fast-forward is a
call made from the settle's own tick, and everything the callback starts
(requests, timers, tasks) goes through the same audit and `pending` bracket.

What makes it safe to run timers early is a narrower definition of which timers
are pulled in at all:

- a delay over 30 s is never pulled in (refresh intervals, session timeouts);
- a callback is not pulled in again from inside its own pulled-in run, and at
  most three times in a render (a poll re-arming itself);
- a chain of pulled-in timers is followed four deep;
- after four fast-forward rounds nothing more is pulled in (loops that re-arm
  under fresh closures or through a promise).

A timer that is not pulled in keeps its real delay: it does not fire in the
render and does not hold the settle. The post-deadline bound of 32 late
pull-ins stays as a backstop.

Measured on the corpus, same captures: median read 341 → 221 ms, 90th
percentile 4.5 → 2.4 s, total 109 → 66 s; no page's recall or bucket changed.
What remains above one second is JavaScript executing, not waiting.

Known cost: a guard such as `setTimeout(reject, 5000)` was already pulled in to
4.4 s; it now fires as soon as the page is quiet. If what it guards is a request,
the request holds the fast-forward; if it is something the engine will never
deliver (a custom element that is never defined), the guard fires sooner than a
browser's would. That was true before, 600 ms earlier than the budget.
