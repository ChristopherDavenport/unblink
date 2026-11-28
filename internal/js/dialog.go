package js

import (
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// HTMLDialogElement's show()/showModal()/close(). A dialog's state is its
// `open` content attribute, which the tree carries: an open dialog's content is
// rendered, a closed one's is not (the user-agent stylesheet hides
// dialog:not([open]); innerText and extraction's hidden check follow the same
// rule). There is no top layer, focus trap or backdrop — nothing here has a
// box — so show() and showModal() differ only in the error they raise.
func (b *bridge) installDialog() {
	p := b.ifaceProtos["HTMLDialogElement"]
	if p == nil {
		return
	}
	vm := b.vm
	open := func(n *html.Node, modal bool) {
		if hasAttr(n, "open") {
			if modal {
				b.throwDOMException("InvalidStateError", "The dialog is already open.")
			}
			return
		}
		if modal && !b.isConnected(n) {
			b.throwDOMException("InvalidStateError", "The dialog is not in a document.")
		}
		b.setAttrMut(n, "open", "")
	}
	b.protoMethod(p, "show", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		open(n, false)
		return goja.Undefined()
	})
	b.protoMethod(p, "showModal", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		open(n, true)
		return goja.Undefined()
	})
	closeDialog := func(n *html.Node, call goja.FunctionCall) goja.Value {
		if !hasAttr(n, "open") {
			return goja.Undefined()
		}
		if v := call.Argument(0); !goja.IsUndefined(v) {
			b.dialogReturn(n, v.String())
		}
		b.removeAttrMut(n, "open")
		// The close event is queued as a task, not fired inside close().
		if b.jsSetTimeout != nil {
			_, _ = b.jsSetTimeout(goja.Undefined(), vm.ToValue(func(goja.FunctionCall) goja.Value {
				b.dispatchOnNode(n, b.newEvent("close", false, false, b.wrap(n)))
				return goja.Undefined()
			}), vm.ToValue(0))
		}
		return goja.Undefined()
	}
	b.protoMethod(p, "close", closeDialog)
	b.protoMethod(p, "requestClose", closeDialog)
	b.protoProp(p, "returnValue",
		func(n *html.Node) goja.Value { return vm.ToValue(b.dialogReturns[n]) },
		func(n *html.Node, v goja.Value) { b.dialogReturn(n, v.String()) })
}

func (b *bridge) dialogReturn(n *html.Node, v string) {
	if b.dialogReturns == nil {
		b.dialogReturns = make(map[*html.Node]string)
	}
	b.dialogReturns[n] = v
}
