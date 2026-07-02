package js

import (
	"runtime/metrics"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// memGuard bounds the Go heap consumed by running page JS. goja has no
// per-runtime heap accounting (see ADR-0003), and vm.Interrupt stops execution
// but not allocation, so a hostile or runaway page could balloon memory within
// its wall-clock budget — and up to jsMaxLive live runtimes plus the concurrent
// one-shot renders share one Go heap. The guard samples the process heap and,
// when it crosses the limit, interrupts *every* registered runtime: attribution
// to a single runtime is impossible with one shared heap, so a collective
// interrupt is the honest containment. debug.SetMemoryLimit (set in main) is the
// GC-side backstop; this is the active kill switch.
type memGuard struct {
	limit     uint64 // bytes; 0 disables the guard entirely
	threshold uint64 // interrupt when live heap exceeds this (90% of limit)

	mu       sync.Mutex
	runtimes map[*goja.Runtime]struct{}

	stop     chan struct{}
	stopOnce sync.Once
}

const (
	// memGuardSampleInterval is how often the watchdog reads the heap gauge.
	// runtime/metrics is non-stop-the-world, so this is cheap.
	memGuardSampleInterval = 250 * time.Millisecond
	// heapObjectsMetric is the live heap-objects gauge — the allocation an
	// allocation-bomb grows. It excludes stacks/GC metadata, which is what we
	// want: it tracks what page JS actually retained.
	heapObjectsMetric = "/memory/classes/heap/objects:bytes"
)

// newMemGuard returns a running guard, or nil when limit is 0 (disabled). Call
// close to stop the watchdog goroutine.
func newMemGuard(limit uint64) *memGuard {
	if limit == 0 {
		return nil
	}
	g := &memGuard{
		limit:     limit,
		threshold: limit / 10 * 9, // 90%, computed without overflow
		runtimes:  make(map[*goja.Runtime]struct{}),
		stop:      make(chan struct{}),
	}
	go g.watch()
	return g
}

// register adds vm to the watched set (a no-op on a nil guard). unregister must
// be called when the runtime is done.
func (g *memGuard) register(vm *goja.Runtime) {
	if g == nil || vm == nil {
		return
	}
	g.mu.Lock()
	g.runtimes[vm] = struct{}{}
	g.mu.Unlock()
}

func (g *memGuard) unregister(vm *goja.Runtime) {
	if g == nil || vm == nil {
		return
	}
	g.mu.Lock()
	delete(g.runtimes, vm)
	g.mu.Unlock()
}

// close stops the watchdog. Idempotent.
func (g *memGuard) close() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() { close(g.stop) })
}

// watch samples the heap on a ticker and interrupts all registered runtimes
// whenever the live heap exceeds the threshold. It idles cheaply when nothing is
// registered.
func (g *memGuard) watch() {
	sample := []metrics.Sample{{Name: heapObjectsMetric}}
	ticker := time.NewTicker(memGuardSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.mu.Lock()
			n := len(g.runtimes)
			g.mu.Unlock()
			if n == 0 {
				continue
			}
			metrics.Read(sample)
			if sample[0].Value.Kind() != metrics.KindUint64 {
				continue
			}
			if sample[0].Value.Uint64() <= g.threshold {
				continue
			}
			g.interruptAll()
		}
	}
}

// interruptAll arms an interrupt on every registered runtime. The interrupt is
// delivered at the next JS instruction, unwinding the running script with the
// message; the render/live path surfaces it as a diagnostic error. Registration
// is left intact — a runtime clears its own interrupt on the next operation.
func (g *memGuard) interruptAll() {
	g.mu.Lock()
	rts := make([]*goja.Runtime, 0, len(g.runtimes))
	for vm := range g.runtimes {
		rts = append(rts, vm)
	}
	g.mu.Unlock()
	for _, vm := range rts {
		vm.Interrupt("unblink: JS memory limit exceeded")
	}
}
