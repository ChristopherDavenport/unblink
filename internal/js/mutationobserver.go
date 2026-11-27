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
	// queue is this observer's record queue (DOM Standard: each observer has its
	// own). A record is queued at mutation time, to the observers interested in it
	// then — so a mutation made while an observer is disconnected never reaches it,
	// even if it re-observes before the next delivery.
	queue []mutationRecord
}

// onMutate is the single sink for every DOM mutation made through the bridge. It
// always bumps domVersion (the settle signal) and, when observers exist, queues
// the record for microtask delivery.
func (b *bridge) onMutate(rec mutationRecord) {
	b.domVersion++
	// Fire disconnectedCallback for upgraded custom elements leaving the tree,
	// synchronously (browser semantics), before observer delivery.
	if len(rec.removed) > 0 && len(b.customElements) > 0 {
		for _, n := range rec.removed {
			b.disconnectTree(n)
		}
	}
	// Form-associated custom elements: leaving the tree can drop the form owner or a
	// disabling <fieldset>, and a disabled attribute change (theirs or a fieldset's)
	// flips formDisabledCallback. Insertion is handled by scan.
	if len(b.faces) > 0 {
		for _, n := range rec.removed {
			b.syncFACETree(n)
		}
		if rec.typ == "attributes" && rec.attr == "disabled" {
			b.syncFACETree(rec.target)
		}
		// form="id": the attribute itself changing re-associates the element, and so
		// does anything that changes which element the id names.
		if rec.typ == "attributes" && rec.attr == "form" {
			if _, ok := b.faces[rec.target]; ok {
				b.syncFACE(rec.target)
			}
		}
		if rec.typ == "childList" || rec.attr == "id" {
			b.formRefEpoch++
		}
		b.syncFormRefs(rec)
	}
	queued := false
	for _, obs := range b.observers {
		if obs.matches(rec) {
			obs.queue = append(obs.queue, rec)
			queued = true
		}
	}
	if queued {
		b.scheduleFlush()
	}
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

// flushMutations delivers each observer's queued records to its callback, then
// clears that queue. Runs on the loop goroutine (no locking needed). It iterates
// a copy of the observer list: a callback may observe or disconnect.
func (b *bridge) flushMutations() {
	for _, obs := range append([]*mutationObserver(nil), b.observers...) {
		if len(obs.queue) == 0 {
			continue
		}
		records := obs.queue
		obs.queue = nil
		b.callSafe(obs.callback, goja.Undefined(), b.recordList(records))
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
			// Observing a node again replaces its options (DOM Standard), and an
			// observer that was disconnected starts receiving records again.
			replaced := false
			for i, old := range obs.targets {
				if old.node == n {
					obs.targets[i] = t
					replaced = true
					break
				}
			}
			if !replaced {
				obs.targets = append(obs.targets, t)
			}
			b.addObserver(obs)
			return goja.Undefined()
		})
		_ = this.Set("disconnect", func(goja.FunctionCall) goja.Value {
			// disconnect stops observation and empties the record queue.
			obs.targets = nil
			obs.queue = nil
			b.removeObserver(obs)
			return goja.Undefined()
		})
		_ = this.Set("takeRecords", func(goja.FunctionCall) goja.Value {
			return b.takeRecords(obs)
		})

		return nil
	}
	_ = vm.Set("MutationObserver", ctor)
}

// addObserver registers obs for record queueing; a no-op when already registered.
func (b *bridge) addObserver(obs *mutationObserver) {
	for _, o := range b.observers {
		if o == obs {
			return
		}
	}
	b.observers = append(b.observers, obs)
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

// takeRecords returns and empties this observer's record queue.
func (b *bridge) takeRecords(obs *mutationObserver) goja.Value {
	records := obs.queue
	obs.queue = nil
	return b.recordList(records)
}
