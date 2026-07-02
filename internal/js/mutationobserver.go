package js

import (
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// MutationObserver, backed by a single Go mutation sink (onMutate) that every
// low-level DOM mutator funnels through. The sink also bumps domVersion on every
// change — even with zero observers — so the live Context can detect "DOM quiet"
// for settle. Records are batched and delivered on a microtask, matching the spec
// closely enough for framework reactivity and "wait for content" patterns.

// mutationRecord is the Go-side description of one DOM change.
type mutationRecord struct {
	typ      string // "childList" | "attributes" | "characterData"
	target   *html.Node
	added    []*html.Node
	removed  []*html.Node
	attr     string // attribute name (attributes)
	oldValue string // previous attribute value (attributes)
}

// observeTarget is one node an observer is watching, with its options.
type observeTarget struct {
	node          *html.Node
	subtree       bool
	childList     bool
	attributes    bool
	characterData bool
	attrFilter    map[string]bool // nil = all attributes
}

type mutationObserver struct {
	callback goja.Callable
	targets  []*observeTarget
}

// onMutate is the single sink for every DOM mutation made through the bridge. It
// always bumps domVersion (the settle signal) and, when observers exist, queues
// the record for microtask delivery.
func (b *bridge) onMutate(rec mutationRecord) {
	b.domVersion++
	if len(b.observers) == 0 {
		return
	}
	b.pendingRecords = append(b.pendingRecords, rec)
	b.scheduleFlush()
}

func (b *bridge) scheduleFlush() {
	if b.flushScheduled || b.promiseThen == nil {
		return
	}
	b.flushScheduled = true
	_, _ = b.promiseThen(b.resolvedPromise, b.vm.ToValue(func(goja.FunctionCall) goja.Value {
		b.flushScheduled = false
		b.flushMutations()
		return goja.Undefined()
	}))
}

// flushMutations delivers all queued records to every matching observer, then
// clears the queue. Runs on the loop goroutine (no locking needed).
func (b *bridge) flushMutations() {
	if len(b.pendingRecords) == 0 {
		return
	}
	records := b.pendingRecords
	b.pendingRecords = nil
	for _, obs := range b.observers {
		var matched []mutationRecord
		for _, r := range records {
			if obs.matches(r) {
				matched = append(matched, r)
			}
		}
		if len(matched) == 0 {
			continue
		}
		b.callSafe(obs.callback, goja.Undefined(), b.recordList(matched))
	}
}

// matches reports whether record r should be delivered to this observer.
func (obs *mutationObserver) matches(r mutationRecord) bool {
	for _, t := range obs.targets {
		if !t.watches(r.typ) {
			continue
		}
		if r.typ == "attributes" && t.attrFilter != nil && !t.attrFilter[r.attr] {
			continue
		}
		if t.node == r.target || (t.subtree && isAncestor(t.node, r.target)) {
			return true
		}
	}
	return false
}

func (t *observeTarget) watches(typ string) bool {
	switch typ {
	case "childList":
		return t.childList
	case "attributes":
		return t.attributes
	case "characterData":
		return t.characterData
	}
	return false
}

func isAncestor(anc, n *html.Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p == anc {
			return true
		}
	}
	return false
}

// recordList builds the JS MutationRecord[] argument for a callback.
func (b *bridge) recordList(recs []mutationRecord) goja.Value {
	vm := b.vm
	out := make([]interface{}, len(recs))
	for i, r := range recs {
		o := vm.NewObject()
		_ = o.Set("type", r.typ)
		_ = o.Set("target", b.wrap(r.target))
		_ = o.Set("addedNodes", b.nodeList(r.added))
		_ = o.Set("removedNodes", b.nodeList(r.removed))
		_ = o.Set("previousSibling", goja.Null())
		_ = o.Set("nextSibling", goja.Null())
		if r.typ == "attributes" {
			_ = o.Set("attributeName", r.attr)
			_ = o.Set("oldValue", r.oldValue)
		} else {
			_ = o.Set("attributeName", goja.Null())
			_ = o.Set("oldValue", goja.Null())
		}
		out[i] = o
	}
	return vm.ToValue(out)
}

// promiseResolveProgram is compiled once and shared across runtimes (a
// goja.Program is immutable); each render still gets its own resolved promise
// by running it on that render's runtime.
var promiseResolveProgram = goja.MustCompile("resolved-promise.js", "Promise.resolve()", false)

// installMutationObserver registers window.MutationObserver as a Go constructor
// and prepares the microtask scheduler. Replaces the prelude no-op.
func (b *bridge) installMutationObserver() {
	vm := b.vm
	if prom, err := vm.RunProgram(promiseResolveProgram); err == nil {
		b.resolvedPromise = prom.ToObject(vm)
		b.promiseThen, _ = goja.AssertFunction(b.resolvedPromise.Get("then"))
	}

	ctor := func(call goja.ConstructorCall) *goja.Object {
		cb, _ := goja.AssertFunction(call.Argument(0))
		obs := &mutationObserver{callback: cb}
		this := call.This

		_ = this.Set("observe", func(c goja.FunctionCall) goja.Value {
			n := b.nodeOf(c.Argument(0))
			if n == nil {
				return goja.Undefined()
			}
			t := &observeTarget{node: n, childList: true} // childList defaults true if unspecified per common use
			if opts := c.Argument(1); opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
				oo := opts.ToObject(vm)
				t.subtree = boolProp(oo, "subtree")
				t.childList = boolProp(oo, "childList")
				t.attributes = boolProp(oo, "attributes")
				t.characterData = boolProp(oo, "characterData")
				if f := oo.Get("attributeFilter"); f != nil && !goja.IsUndefined(f) && !goja.IsNull(f) {
					t.attrFilter = map[string]bool{}
					vm.ForOf(f, func(v goja.Value) bool { t.attrFilter[v.String()] = true; return true })
					t.attributes = true
				}
			}
			obs.targets = append(obs.targets, t)
			return goja.Undefined()
		})
		_ = this.Set("disconnect", func(goja.FunctionCall) goja.Value {
			obs.targets = nil
			b.removeObserver(obs)
			return goja.Undefined()
		})
		_ = this.Set("takeRecords", func(goja.FunctionCall) goja.Value {
			return b.takeRecords(obs)
		})

		b.observers = append(b.observers, obs)
		return nil
	}
	_ = vm.Set("MutationObserver", ctor)
}

func (b *bridge) removeObserver(obs *mutationObserver) {
	out := b.observers[:0]
	for _, o := range b.observers {
		if o != obs {
			out = append(out, o)
		}
	}
	b.observers = out
}

// takeRecords returns and removes this observer's currently-queued records.
func (b *bridge) takeRecords(obs *mutationObserver) goja.Value {
	var matched []mutationRecord
	var rest []mutationRecord
	for _, r := range b.pendingRecords {
		if obs.matches(r) {
			matched = append(matched, r)
		} else {
			rest = append(rest, r)
		}
	}
	b.pendingRecords = rest
	return b.recordList(matched)
}
