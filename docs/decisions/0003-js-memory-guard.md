# ADR 0003: JS memory guard is process-level, not per-runtime

Date: 2026-07-02
Status: accepted

## Context

Under `--js`, unblink runs untrusted page JavaScript on goja. Every other
resource an untrusted page can grow is bounded (live-runtime LRU cap 16, session
cap 256, call-stack depth 10000, localStorage 1024 keys / 64 KiB values, history
stacks, diagnostics buffers, per-render request budget, wall-clock render
budget). Heap memory was the one exception.

The wall-clock watchdog (`vm.Interrupt`) stops *execution* but not
*allocation*: a script can allocate unboundedly within its budget, and up to 16
live runtimes plus the concurrent one-shot renders share a single Go heap. A
hostile or runaway page (`while (true) sink.push(new Array(1e6))`) could exhaust
process memory and get the MCP server OOM-killed before any count-based bound
tripped.

The pinned goja exposes **no per-runtime heap accounting** (`grep` for
`MemUsage`/`MemoryLimit`/`SetMemoryLimit` over the module is empty); only
`Interrupt` and `SetMaxCallStackSize` exist. Per-forks that add heap metering do
exist, but adopting one is a goja bump — an ADR-0002-governed reviewed change,
not something to fold into a hardening pass. And with one shared Go heap,
attributing bytes to a single runtime is impossible anyway.

## Decision

Bound heap growth with a **process-level guard**, in two layers, neither
requiring a goja change:

1. **Active kill switch** (`internal/js/memguard.go`): a watchdog goroutine
   samples the live heap via `runtime/metrics`
   (`/memory/classes/heap/objects:bytes`, non-stop-the-world) every 250ms while
   any runtime is registered. When it crosses 90% of the limit, it calls
   `vm.Interrupt` on **every** registered runtime. Collective interrupt is the
   honest semantic: with one shared heap we cannot know which runtime is
   responsible, so we stop them all; each clears its own interrupt on its next
   operation, and legitimate renders resume. The interrupt surfaces through the
   existing diagnostic-error path (`js_errors`).

2. **Passive backstop** (`cmd/unblink/main.go`): `debug.SetMemoryLimit` sets a
   GC soft limit at 2× the interrupt limit, so the collector fights allocation
   before the kernel OOM-kills the process, giving the active guard headroom to
   fire first.

The limit is `--js-memory-limit` (MiB, default 1024, `0` disables). It is
plumbed as `js.WithMemoryLimit` → `browser.WithJSMemoryLimit`.

## Consequences

- Containment is process-wide, not per-page: a single allocation-bomb page
  interrupts every in-flight render for one sample cycle. Acceptable — an
  abusive page degrading its neighbors briefly beats an OOM kill, and legitimate
  pages never approach the default 1 GiB.
- The guard measures the whole Go heap, not just JS allocations, so the limit is
  a generous ceiling (default 1 GiB), not a tight per-render quota.
- **Revisit if upstream goja grows per-runtime `MemUsage`**: that would allow
  attributing and interrupting only the offending runtime. Until then, the
  process-level guard is the correct bound. See [[unblink-security-hardening]].
