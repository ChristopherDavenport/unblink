package js

import (
	"errors"
	"math"

	"github.com/dop251/goja"
)

// The DOM XPath API (DOM Standard §8, "XPath"): XPathEvaluator,
// XPathExpression, XPathResult and the XPathEvaluatorBase mixin on Document
// (evaluate / createExpression / createNSResolver), over the evaluator in
// xpath.go. Namespace resolvers are accepted and ignored — see xpath.go for why
// that is sound for HTML documents.

// XPathResult type constants.
const (
	xrAny = iota
	xrNumber
	xrString
	xrBoolean
	xrUnorderedIterator
	xrOrderedIterator
	xrUnorderedSnapshot
	xrOrderedSnapshot
	xrAnyUnorderedNode
	xrFirstOrderedNode
)

func (b *bridge) throwXPathError(err error) {
	switch {
	case errors.Is(err, errXPathType):
		panic(b.vm.NewTypeError("The expression cannot be converted to return the specified type."))
	case errors.Is(err, errXPathBudget):
		b.throwDOMException("OperationError", "XPath evaluation exceeded its step budget")
	default:
		b.throwDOMException("SyntaxError", err.Error())
	}
}

// xpathContextNode maps the JS context-node argument to an evaluator node. An
// Attr object (from getAttributeNode) is not node-backed, so it is located
// through its ownerElement.
func (b *bridge) xpathContextNode(v goja.Value) (xnode, bool) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return treeNode(b.doc), true
	}
	if n := b.unwrap(v); n != nil {
		return treeNode(n), true
	}
	o := v.ToObject(b.vm)
	if o == nil {
		return xnode{}, false
	}
	owner := b.unwrap(o.Get("ownerElement"))
	name := o.Get("name")
	if owner == nil || name == nil {
		return xnode{}, false
	}
	for i, a := range owner.Attr {
		if a.Key == name.String() {
			return xnode{n: owner, attr: i}, true
		}
	}
	return xnode{}, false
}

func (b *bridge) xnodeValue(x xnode) goja.Value {
	if x.attr >= 0 {
		return b.attrNodeCached(x.n, x.n.Attr[x.attr].Key)
	}
	return b.wrap(x.n)
}

// xpathResult builds the XPathResult for value v requested as type typ.
func (b *bridge) xpathResult(v xvalue, typ int) goja.Value {
	vm := b.vm
	set, isSet := v.([]xnode)
	if typ == xrAny {
		switch v.(type) {
		case float64:
			typ = xrNumber
		case string:
			typ = xrString
		case bool:
			typ = xrBoolean
		default:
			typ = xrUnorderedIterator
		}
	}
	if typ < xrNumber || typ > xrFirstOrderedNode {
		b.throwDOMException("NotSupportedError", "Unknown XPathResult type")
	}
	if typ >= xrUnorderedIterator && !isSet {
		panic(vm.NewTypeError("The result is not a node set, and therefore cannot be converted to the desired type."))
	}
	res := vm.CreateObject(b.protoXPathResult)
	_ = res.Set("resultType", typ)
	wrong := func(name string) func() goja.Value {
		return func() goja.Value {
			panic(vm.NewTypeError("The result type is not compatible with " + name + "."))
		}
	}
	prop := func(name string, ok bool, get func() goja.Value) {
		if ok {
			b.defineGetter(res, name, get)
		} else {
			b.defineGetter(res, name, wrong(name))
		}
	}
	prop("numberValue", typ == xrNumber, func() goja.Value { return vm.ToValue(xtoNumber(v)) })
	prop("stringValue", typ == xrString, func() goja.Value { return vm.ToValue(xtoString(v)) })
	prop("booleanValue", typ == xrBoolean, func() goja.Value { return vm.ToValue(xtoBool(v)) })
	prop("singleNodeValue", typ == xrAnyUnorderedNode || typ == xrFirstOrderedNode, func() goja.Value {
		if len(set) == 0 {
			return goja.Null()
		}
		return b.xnodeValue(set[0])
	})
	snapshot := typ == xrUnorderedSnapshot || typ == xrOrderedSnapshot
	iterator := typ == xrUnorderedIterator || typ == xrOrderedIterator
	prop("snapshotLength", snapshot, func() goja.Value { return vm.ToValue(len(set)) })
	// The iterator is invalidated by a document mutation after the result was
	// produced (domVersion is the tree's mutation counter).
	version := b.domVersion
	b.defineGetter(res, "invalidIteratorState", func() goja.Value {
		return vm.ToValue(iterator && b.domVersion != version)
	})
	next := 0
	_ = res.Set("iterateNext", func(goja.FunctionCall) goja.Value {
		if !iterator {
			panic(vm.NewTypeError("The result type is not an iterator."))
		}
		if b.domVersion != version {
			b.throwDOMException("InvalidStateError", "The document has mutated since the result was returned.")
		}
		if next >= len(set) {
			return goja.Null()
		}
		next++
		return b.xnodeValue(set[next-1])
	})
	_ = res.Set("snapshotItem", func(call goja.FunctionCall) goja.Value {
		if !snapshot {
			panic(vm.NewTypeError("The result type is not a snapshot."))
		}
		i := call.Argument(0).ToInteger()
		if i < 0 || i >= int64(len(set)) {
			return goja.Null()
		}
		return b.xnodeValue(set[i])
	})
	return res
}

// xpathRun evaluates a compiled expression against the JS context node.
func (b *bridge) xpathRun(e xexpr, ctxArg, typArg goja.Value) goja.Value {
	x, ok := b.xpathContextNode(ctxArg)
	if !ok {
		panic(b.vm.NewTypeError("XPath context node is not a Node."))
	}
	typ := xrAny
	if typArg != nil && !goja.IsUndefined(typArg) && !goja.IsNull(typArg) {
		f := typArg.ToFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		typ = int(f)
	}
	v, err := xpathEvaluate(e, x)
	if err != nil {
		b.throwXPathError(err)
	}
	return b.xpathResult(v, typ)
}

func (b *bridge) xpathCompileOrThrow(src goja.Value) xexpr {
	e, err := xpathCompile(src.String())
	if err != nil {
		b.throwXPathError(err)
	}
	return e
}

// installXPathBase adds the XPathEvaluatorBase mixin to o. A call that passes
// no context node evaluates against the page document.
func (b *bridge) installXPathBase(o *goja.Object) {
	vm := b.vm
	_ = o.Set("createExpression", func(call goja.FunctionCall) goja.Value {
		e := b.xpathCompileOrThrow(call.Argument(0))
		expr := vm.CreateObject(b.protoXPathExpression)
		_ = expr.Set("evaluate", func(c goja.FunctionCall) goja.Value {
			return b.xpathRun(e, c.Argument(0), c.Argument(1))
		})
		return expr
	})
	// createNSResolver returns its argument (the modern, spec'd behaviour): a
	// Node is its own resolver.
	_ = o.Set("createNSResolver", func(call goja.FunctionCall) goja.Value { return call.Argument(0) })
	_ = o.Set("evaluate", func(call goja.FunctionCall) goja.Value {
		e := b.xpathCompileOrThrow(call.Argument(0))
		return b.xpathRun(e, call.Argument(1), call.Argument(3))
	})
}

// installXPath registers XPathResult / XPathExpression / XPathEvaluator and
// puts the evaluator mixin on Document.prototype (so the page document and
// parsed/created documents all have it).
func (b *bridge) installXPath() {
	vm := b.vm
	b.protoXPathResult = vm.NewObject()
	rc := b.defineCtor("XPathResult", b.protoXPathResult)
	b.setToStringTag(b.protoXPathResult, "XPathResult")
	for name, val := range map[string]int{
		"ANY_TYPE": xrAny, "NUMBER_TYPE": xrNumber, "STRING_TYPE": xrString, "BOOLEAN_TYPE": xrBoolean,
		"UNORDERED_NODE_ITERATOR_TYPE": xrUnorderedIterator, "ORDERED_NODE_ITERATOR_TYPE": xrOrderedIterator,
		"UNORDERED_NODE_SNAPSHOT_TYPE": xrUnorderedSnapshot, "ORDERED_NODE_SNAPSHOT_TYPE": xrOrderedSnapshot,
		"ANY_UNORDERED_NODE_TYPE": xrAnyUnorderedNode, "FIRST_ORDERED_NODE_TYPE": xrFirstOrderedNode,
	} {
		_ = rc.Set(name, val)
		_ = b.protoXPathResult.Set(name, val)
	}
	b.protoXPathExpression = vm.NewObject()
	b.defineCtor("XPathExpression", b.protoXPathExpression)
	b.setToStringTag(b.protoXPathExpression, "XPathExpression")

	evalProto := vm.NewObject()
	b.installXPathBase(evalProto)
	b.setToStringTag(evalProto, "XPathEvaluator")
	ctor := vm.ToValue(func(call goja.ConstructorCall) *goja.Object { return nil }).(*goja.Object)
	b.finishCtor("XPathEvaluator", ctor, evalProto)

	b.installXPathBase(b.protoDocument)
}
