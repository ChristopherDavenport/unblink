package js

import (
	"net/url"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// This file builds the DOM prototype chain shared by every wrapped node, so the
// runtime exposes real Node/Element/HTMLElement/Text/Comment/DocumentFragment/
// Document constructors. Frameworks rely on `el instanceof HTMLElement`,
// `Object.getPrototypeOf(el)`, and patching `Element.prototype.*`; none of that
// works when each wrapper carries only own accessor properties.
//
// Every accessor/method lives once on a prototype and recovers its backing
// *html.Node from the receiver (`call.This`) via the objNode map, instead of
// closing over a per-instance node. `wrap` therefore allocates a bare object with
// the right prototype and no own properties — fewer descriptors per node than the
// old per-instance scheme.

// nodeOf recovers the *html.Node behind a receiver (`this`) for a prototype
// accessor/method. Returns nil if the receiver is not one of our wrappers.
func (b *bridge) nodeOf(this goja.Value) *html.Node {
	if this == nil || goja.IsUndefined(this) || goja.IsNull(this) {
		return nil
	}
	o := this.ToObject(b.vm)
	if o == nil {
		return nil
	}
	return b.objNode[o]
}

// --- prototype-targeted accessor/method helpers (node recovered from `this`) ---

func (b *bridge) protoGetter(proto *goja.Object, name string, get func(n *html.Node) goja.Value) {
	getter := b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		n := b.nodeOf(call.This)
		if n == nil {
			return goja.Undefined()
		}
		return get(n)
	})
	_ = proto.DefineAccessorProperty(name, getter, nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
}

func (b *bridge) protoProp(proto *goja.Object, name string, get func(n *html.Node) goja.Value, set func(n *html.Node, v goja.Value)) {
	getter := b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		n := b.nodeOf(call.This)
		if n == nil {
			return goja.Undefined()
		}
		return get(n)
	})
	var setter goja.Value
	if set != nil {
		setter = b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			if n := b.nodeOf(call.This); n != nil {
				set(n, call.Argument(0))
			}
			return goja.Undefined()
		})
	}
	_ = proto.DefineAccessorProperty(name, getter, setter, goja.FLAG_FALSE, goja.FLAG_TRUE)
}

func (b *bridge) protoMethod(proto *goja.Object, name string, fn func(n *html.Node, call goja.FunctionCall) goja.Value) {
	_ = proto.Set(name, func(call goja.FunctionCall) goja.Value {
		n := b.nodeOf(call.This)
		if n == nil {
			return goja.Undefined()
		}
		return fn(n, call)
	})
}

// installPrototypes builds the prototype chain and registers the matching global
// constructors so instanceof works. Called once per bridge from install().
func (b *bridge) installPrototypes() {
	vm := b.vm

	b.protoEventTarget = vm.NewObject()
	b.protoNode = vm.CreateObject(b.protoEventTarget)
	b.protoElement = vm.CreateObject(b.protoNode)
	b.protoHTMLElement = vm.CreateObject(b.protoElement)
	b.protoCharacterData = vm.CreateObject(b.protoNode)
	b.protoText = vm.CreateObject(b.protoCharacterData)
	b.protoComment = vm.CreateObject(b.protoCharacterData)
	b.protoDocumentFragment = vm.CreateObject(b.protoNode)
	b.protoDocument = vm.CreateObject(b.protoNode)

	b.installNodeProto()
	b.installElementProto()
	b.installHTMLElementProto()
	b.installCharacterDataProto()
	b.installGeometry()

	// Global constructors. The .prototype/.constructor link is what `instanceof`
	// and `class X extends HTMLElement` consume; concrete construction behaviour
	// (custom-element upgrade) is layered on in customelements.go.
	b.defineCtor("EventTarget", b.protoEventTarget)
	nodeCtor := b.defineCtor("Node", b.protoNode)
	// Node's numeric constants live on both the interface object and the
	// prototype (Angular's sanitizer tests nodeType against Node.ELEMENT_NODE and
	// masks compareDocumentPosition with Node.DOCUMENT_POSITION_CONTAINED_BY).
	for name, val := range map[string]int{
		"ELEMENT_NODE": 1, "ATTRIBUTE_NODE": 2, "TEXT_NODE": 3, "CDATA_SECTION_NODE": 4,
		"PROCESSING_INSTRUCTION_NODE": 7, "COMMENT_NODE": 8, "DOCUMENT_NODE": 9,
		"DOCUMENT_TYPE_NODE": 10, "DOCUMENT_FRAGMENT_NODE": 11,
		"DOCUMENT_POSITION_DISCONNECTED": 1, "DOCUMENT_POSITION_PRECEDING": 2,
		"DOCUMENT_POSITION_FOLLOWING": 4, "DOCUMENT_POSITION_CONTAINS": 8,
		"DOCUMENT_POSITION_CONTAINED_BY": 16, "DOCUMENT_POSITION_IMPLEMENTATION_SPECIFIC": 32,
	} {
		_ = nodeCtor.Set(name, val)
		_ = b.protoNode.Set(name, val)
	}
	b.defineCtor("CharacterData", b.protoCharacterData)
	b.defineCtor("Text", b.protoText)
	b.defineCtor("Comment", b.protoComment)
	b.defineCtor("DocumentFragment", b.protoDocumentFragment)
	b.defineCtor("Document", b.protoDocument)
	b.defineCtor("Element", b.protoElement)
	b.defineHTMLElementCtor()

	// Specific HTML*Element / SVGElement interface constructors. Frameworks probe
	// these via `node instanceof HTMLInputElement` etc. (React's focus/selection and
	// form code). We don't have per-tag prototypes, so each gets a fresh prototype
	// chaining to HTMLElement: instanceof returns false for our generic wrappers
	// (safe) instead of throwing on `instanceof undefined`.
	for _, name := range htmlInterfaceNames {
		b.defineCtor(name, vm.CreateObject(b.protoHTMLElement))
	}
	// HTMLAudioElement/HTMLVideoElement chain through HTMLMediaElement as in a
	// real DOM; zone.js dereferences HTMLMediaElement.prototype unguarded.
	mediaProto := vm.CreateObject(b.protoHTMLElement)
	b.defineCtor("HTMLMediaElement", mediaProto)
	b.defineCtor("HTMLAudioElement", vm.CreateObject(mediaProto))
	b.defineCtor("HTMLVideoElement", vm.CreateObject(mediaProto))
	b.defineCtor("SVGElement", vm.CreateObject(b.protoElement))
}

var htmlInterfaceNames = []string{
	"HTMLUnknownElement", "HTMLIFrameElement", "HTMLInputElement", "HTMLTextAreaElement",
	"HTMLSelectElement", "HTMLOptionElement", "HTMLButtonElement", "HTMLAnchorElement",
	"HTMLImageElement", "HTMLFormElement", "HTMLLabelElement", "HTMLDivElement",
	"HTMLSpanElement", "HTMLParagraphElement", "HTMLUListElement", "HTMLOListElement",
	"HTMLLIElement", "HTMLTableElement", "HTMLTemplateElement", "HTMLStyleElement",
	"HTMLScriptElement", "HTMLLinkElement", "HTMLSlotElement", "HTMLCanvasElement",
	"HTMLPreElement", "HTMLHeadingElement", "HTMLBRElement", "HTMLHRElement",
	// zone.js's property-descriptor patch dereferences these unguarded.
	"HTMLBodyElement", "HTMLHtmlElement", "HTMLHeadElement", "HTMLFrameElement",
	"HTMLFrameSetElement", "HTMLMarqueeElement", "HTMLEmbedElement", "HTMLObjectElement",
	"HTMLSourceElement", "HTMLTrackElement", "HTMLAreaElement", "HTMLBaseElement",
	"HTMLMapElement", "HTMLMetaElement", "HTMLTitleElement", "HTMLTableRowElement",
	"HTMLTableCellElement", "HTMLTableSectionElement", "HTMLTableCaptionElement",
	"HTMLTableColElement", "HTMLDataListElement", "HTMLFieldSetElement", "HTMLLegendElement",
	"HTMLOptGroupElement", "HTMLOutputElement", "HTMLProgressElement", "HTMLMeterElement",
	"HTMLDetailsElement", "HTMLDialogElement", "HTMLTimeElement", "HTMLPictureElement",
	"HTMLDListElement", "HTMLQuoteElement", "HTMLModElement", "HTMLDataElement",
}

// connect runs the custom-element upgrade hook for a newly-inserted node. It is a
// no-op until Phase C installs the registry; defined here so the mutation helpers
// can call it unconditionally.
func (b *bridge) connect(n *html.Node) {
	b.upgradeTree(n)
	b.loadInsertedScript(n)
}

// defineCtor registers a native constructor global whose .prototype is proto.
// Constructing one yields a fresh object on that prototype (rarely used directly;
// present so instanceof and subclassing resolve).
func (b *bridge) defineCtor(name string, proto *goja.Object) *goja.Object {
	ctor := b.vm.ToValue(func(call goja.ConstructorCall) *goja.Object { return nil }).(*goja.Object)
	_ = ctor.Set("prototype", proto)
	_ = proto.Set("constructor", ctor)
	_ = b.vm.Set(name, ctor)
	return ctor
}

// protoFor selects the prototype for a node by type/role. The real document node
// (b.doc) never reaches here — wrap returns the dedicated document object.
func (b *bridge) protoFor(n *html.Node) *goja.Object {
	switch n.Type {
	case html.TextNode:
		return b.protoText
	case html.CommentNode:
		return b.protoComment
	case html.DocumentNode:
		return b.protoDocumentFragment // a synthetic fragment sentinel
	case html.ElementNode:
		return b.protoHTMLElement
	default:
		return b.protoNode
	}
}

// --- Node.prototype: members common to every node ---

func (b *bridge) installNodeProto() {
	vm := b.vm
	p := b.protoNode

	b.protoGetter(p, "nodeType", func(n *html.Node) goja.Value { return vm.ToValue(nodeType(n)) })
	b.protoGetter(p, "nodeName", func(n *html.Node) goja.Value { return vm.ToValue(nodeName(n)) })
	b.protoProp(p, "nodeValue",
		func(n *html.Node) goja.Value {
			if n.Type == html.TextNode || n.Type == html.CommentNode {
				return vm.ToValue(n.Data)
			}
			return goja.Null()
		},
		func(n *html.Node, v goja.Value) {
			if n.Type == html.TextNode || n.Type == html.CommentNode {
				n.Data = v.String()
				b.onMutate(mutationRecord{typ: "characterData", target: n})
			}
		})
	b.protoProp(p, "textContent",
		func(n *html.Node) goja.Value { return vm.ToValue(textContent(n)) },
		func(n *html.Node, v goja.Value) {
			setTextContent(n, v.String())
			b.onMutate(mutationRecord{typ: "childList", target: n})
		})

	b.protoGetter(p, "parentNode", func(n *html.Node) goja.Value { return b.wrap(n.Parent) })
	b.protoGetter(p, "parentElement", func(n *html.Node) goja.Value {
		if n.Parent != nil && n.Parent.Type == html.ElementNode {
			return b.wrap(n.Parent)
		}
		return goja.Null()
	})
	b.protoGetter(p, "ownerDocument", func(n *html.Node) goja.Value {
		if n == b.doc {
			return goja.Null()
		}
		return b.documentObj
	})
	b.protoGetter(p, "isConnected", func(n *html.Node) goja.Value { return vm.ToValue(b.isConnected(n)) })
	b.protoMethod(p, "getRootNode", func(n *html.Node, call goja.FunctionCall) goja.Value {
		composed := false
		if opts := call.Argument(0); opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
			if oo := opts.ToObject(vm); oo != nil {
				composed = boolProp(oo, "composed")
			}
		}
		// Climb to the top of n's tree; if that is a shadow-root backing node, either
		// return its ShadowRoot (getRootNode) or cross to the host and keep climbing
		// (getRootNode({composed:true})). A host itself lives in the light tree, so its
		// root is the document — matching the spec (not its own shadow root).
		cur := n
		for {
			root := cur
			for root.Parent != nil {
				root = root.Parent
			}
			host, ok := b.shadowHostOf[root]
			if !ok {
				return b.documentObj
			}
			if composed {
				cur = host
				continue
			}
			if sr := b.shadowRoots[host]; sr != nil {
				return sr
			}
			return b.documentObj
		}
	})
	b.protoGetter(p, "firstChild", func(n *html.Node) goja.Value { return b.wrap(n.FirstChild) })
	b.protoGetter(p, "lastChild", func(n *html.Node) goja.Value { return b.wrap(n.LastChild) })
	b.protoGetter(p, "nextSibling", func(n *html.Node) goja.Value { return b.wrap(n.NextSibling) })
	b.protoGetter(p, "previousSibling", func(n *html.Node) goja.Value { return b.wrap(n.PrevSibling) })
	b.protoGetter(p, "childNodes", func(n *html.Node) goja.Value { return b.nodeList(childNodes(n)) })

	b.protoMethod(p, "appendChild", func(n *html.Node, call goja.FunctionCall) goja.Value {
		child := b.unwrap(call.Argument(0))
		if child != nil {
			b.insert(n, child, nil)
		}
		return call.Argument(0)
	})
	b.protoMethod(p, "removeChild", func(n *html.Node, call goja.FunctionCall) goja.Value {
		child := b.unwrap(call.Argument(0))
		if child != nil && child.Parent == n {
			removed := []*html.Node{child}
			n.RemoveChild(child)
			b.onMutate(mutationRecord{typ: "childList", target: n, removed: removed})
		}
		return call.Argument(0)
	})
	b.protoMethod(p, "insertBefore", func(n *html.Node, call goja.FunctionCall) goja.Value {
		newNode := b.unwrap(call.Argument(0))
		ref := b.unwrap(call.Argument(1))
		if newNode != nil {
			if ref != nil && ref.Parent == n {
				b.insert(n, newNode, ref)
			} else {
				b.insert(n, newNode, nil)
			}
		}
		return call.Argument(0)
	})
	b.protoMethod(p, "replaceChild", func(n *html.Node, call goja.FunctionCall) goja.Value {
		newNode := b.unwrap(call.Argument(0))
		oldNode := b.unwrap(call.Argument(1))
		if newNode != nil && oldNode != nil && oldNode.Parent == n {
			b.insert(n, newNode, oldNode)
			n.RemoveChild(oldNode)
			b.onMutate(mutationRecord{typ: "childList", target: n, removed: []*html.Node{oldNode}})
		}
		return call.Argument(1)
	})
	b.protoMethod(p, "cloneNode", func(n *html.Node, call goja.FunctionCall) goja.Value {
		deep := call.Argument(0).ToBoolean()
		return b.wrap(cloneNode(n, deep))
	})
	b.protoMethod(p, "contains", func(n *html.Node, call goja.FunctionCall) goja.Value {
		other := b.unwrap(call.Argument(0))
		return vm.ToValue(contains(n, other))
	})
	b.protoMethod(p, "compareDocumentPosition", func(n *html.Node, call goja.FunctionCall) goja.Value {
		other := b.unwrap(call.Argument(0))
		switch {
		case other == nil || other == n:
			return vm.ToValue(0)
		case contains(n, other):
			return vm.ToValue(16 | 4) // CONTAINED_BY | FOLLOWING
		case contains(other, n):
			return vm.ToValue(8 | 2) // CONTAINS | PRECEDING
		case treeRoot(n) != treeRoot(other):
			return vm.ToValue(1 | 32 | 2) // DISCONNECTED | IMPLEMENTATION_SPECIFIC | PRECEDING
		case firstInTreeOrder(treeRoot(n), n, other) == n:
			return vm.ToValue(4) // FOLLOWING (other comes after n)
		default:
			return vm.ToValue(2) // PRECEDING
		}
	})
	b.protoMethod(p, "hasChildNodes", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		return vm.ToValue(n.FirstChild != nil)
	})

	b.protoMethod(p, "addEventListener", func(n *html.Node, call goja.FunctionCall) goja.Value {
		b.nodeAddListener(n, call)
		return goja.Undefined()
	})
	b.protoMethod(p, "removeEventListener", func(n *html.Node, call goja.FunctionCall) goja.Value {
		b.nodeRemoveListener(n, call)
		return goja.Undefined()
	})
	b.protoMethod(p, "dispatchEvent", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return vm.ToValue(b.dispatchUserEvent(call.Argument(0), call.This, func(ev *domEvent) bool {
			return b.dispatchOnNode(n, ev)
		}))
	})
}

// --- Element.prototype: element-only members ---

func (b *bridge) installElementProto() {
	vm := b.vm
	p := b.protoElement

	b.protoGetter(p, "tagName", func(n *html.Node) goja.Value { return vm.ToValue(strings.ToUpper(n.Data)) })
	b.protoGetter(p, "localName", func(n *html.Node) goja.Value { return vm.ToValue(n.Data) })

	b.protoProp(p, "id",
		func(n *html.Node) goja.Value { return vm.ToValue(getAttr(n, "id")) },
		func(n *html.Node, v goja.Value) { b.setAttrMut(n, "id", v.String()) })
	b.protoProp(p, "className",
		func(n *html.Node) goja.Value { return vm.ToValue(getAttr(n, "class")) },
		func(n *html.Node, v goja.Value) { b.setAttrMut(n, "class", v.String()) })

	b.protoProp(p, "innerHTML",
		func(n *html.Node) goja.Value { return vm.ToValue(innerHTML(n)) },
		func(n *html.Node, v goja.Value) {
			setInnerHTML(n, v.String())
			b.onMutate(mutationRecord{typ: "childList", target: n})
		})
	b.protoGetter(p, "outerHTML", func(n *html.Node) goja.Value { return vm.ToValue(outerHTML(n)) })

	b.protoGetter(p, "children", func(n *html.Node) goja.Value { return b.nodeList(childElements(n)) })
	b.protoGetter(p, "firstElementChild", func(n *html.Node) goja.Value {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				return b.wrap(c)
			}
		}
		return goja.Null()
	})
	b.protoGetter(p, "childElementCount", func(n *html.Node) goja.Value { return vm.ToValue(len(childElements(n))) })
	b.protoGetter(p, "nextElementSibling", func(n *html.Node) goja.Value { return b.wrap(nextElement(n)) })
	b.protoGetter(p, "previousElementSibling", func(n *html.Node) goja.Value { return b.wrap(prevElement(n)) })

	b.protoMethod(p, "getAttribute", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if !hasAttr(n, call.Argument(0).String()) {
			return goja.Null()
		}
		return vm.ToValue(getAttr(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "setAttribute", func(n *html.Node, call goja.FunctionCall) goja.Value {
		b.setAttrMut(n, call.Argument(0).String(), call.Argument(1).String())
		return goja.Undefined()
	})
	b.protoMethod(p, "removeAttribute", func(n *html.Node, call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()
		had := hasAttr(n, key)
		removeAttr(n, key)
		if had {
			b.onMutate(mutationRecord{typ: "attributes", target: n, attr: key})
		}
		return goja.Undefined()
	})
	b.protoMethod(p, "hasAttribute", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return vm.ToValue(hasAttr(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "hasAttributes", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		return vm.ToValue(len(n.Attr) > 0)
	})
	b.protoMethod(p, "getAttributeNames", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		names := make([]interface{}, len(n.Attr))
		for i, a := range n.Attr {
			names[i] = a.Key
		}
		return vm.NewArray(names...)
	})
	b.protoMethod(p, "toggleAttribute", func(n *html.Node, call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		on := !hasAttr(n, name)
		if len(call.Arguments) > 1 {
			on = call.Argument(1).ToBoolean()
		}
		if on {
			b.setAttrMut(n, name, "")
		} else if hasAttr(n, name) {
			removeAttr(n, name)
			b.onMutate(mutationRecord{typ: "attributes", target: n, attr: name})
		}
		return vm.ToValue(on)
	})
	b.protoGetter(p, "attributes", func(n *html.Node) goja.Value { return b.namedNodeMap(n) })

	b.protoMethod(p, "querySelector", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.wrap(query(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "querySelectorAll", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "getElementsByTagName", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "getElementsByClassName", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.nodeList(queryAll(n, "."+call.Argument(0).String()))
	})
	b.protoMethod(p, "matches", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return vm.ToValue(matchesSelector(n, call.Argument(0).String()))
	})
	b.protoMethod(p, "closest", func(n *html.Node, call goja.FunctionCall) goja.Value {
		sel := call.Argument(0).String()
		for a := n; a != nil; a = a.Parent {
			if a.Type == html.ElementNode && matchesSelector(a, sel) {
				return b.wrap(a)
			}
		}
		return goja.Null()
	})

	b.protoMethod(p, "insertAdjacentHTML", func(n *html.Node, call goja.FunctionCall) goja.Value {
		b.insertAdjacentHTML(n, call.Argument(0).String(), call.Argument(1).String())
		return goja.Undefined()
	})
	b.protoMethod(p, "insertAdjacentElement", func(n *html.Node, call goja.FunctionCall) goja.Value {
		el := b.unwrap(call.Argument(1))
		if el != nil {
			b.insertAdjacentNode(n, call.Argument(0).String(), el)
		}
		return call.Argument(1)
	})

	b.protoGetter(p, "classList", func(n *html.Node) goja.Value { return b.classListFor(n) })

	// Pointer-capture no-op stubs. There is no layout engine, but press-based
	// widgets (react-aria's usePress) call setPointerCapture(e.pointerId) inside
	// their pointerdown handler — without these the call throws and callSafe
	// silently swallows it, aborting press setup.
	b.protoMethod(p, "setPointerCapture", func(n *html.Node, _ goja.FunctionCall) goja.Value { return goja.Undefined() })
	b.protoMethod(p, "releasePointerCapture", func(n *html.Node, _ goja.FunctionCall) goja.Value { return goja.Undefined() })
	b.protoMethod(p, "hasPointerCapture", func(n *html.Node, _ goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
}

// isFocusable reports whether n can receive focus (roughly the natively-focusable
// elements plus anything with a tabindex or editable host).
func isFocusable(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "a", "area":
		return hasAttr(n, "href")
	case "button", "select", "textarea":
		return true
	case "input":
		return getAttr(n, "type") != "hidden"
	}
	if hasAttr(n, "tabindex") {
		return true
	}
	if hasAttr(n, "contenteditable") {
		v := getAttr(n, "contenteditable")
		return v == "" || v == "true"
	}
	return false
}

// focusableTarget returns the nearest focusable element at or above n (browsers
// move focus to the closest focusable ancestor on mousedown), or nil if none.
func focusableTarget(n *html.Node) *html.Node {
	for cur := n; cur != nil; cur = cur.Parent {
		if isFocusable(cur) {
			return cur
		}
	}
	return nil
}

// --- HTMLElement.prototype: HTML-specific members ---

func (b *bridge) installHTMLElementProto() {
	vm := b.vm
	p := b.protoHTMLElement

	// Form submission: <form>.elements (its controls) and submit()/requestSubmit()
	// (which navigate — recorded in pendingNav — since a render never re-fetches).
	// A JS bot-check interstitial that submits itself on DOMContentLoaded lands here.
	b.protoGetter(p, "elements", func(n *html.Node) goja.Value {
		if n.Data != "form" {
			return goja.Undefined()
		}
		return b.htmlCollection(formControls(n))
	})
	b.protoMethod(p, "submit", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		b.submitForm(n, false)
		return goja.Undefined()
	})
	b.protoMethod(p, "requestSubmit", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		b.submitForm(n, true)
		return goja.Undefined()
	})

	// Minimal form-control state so handlers can read/write what was typed.
	b.protoProp(p, "value",
		func(n *html.Node) goja.Value {
			if n.Data == "textarea" {
				return vm.ToValue(textContent(n))
			}
			return vm.ToValue(getAttr(n, "value"))
		},
		func(n *html.Node, v goja.Value) { b.setControlValue(n, v.String()) })
	b.protoProp(p, "checked",
		func(n *html.Node) goja.Value { return vm.ToValue(hasAttr(n, "checked")) },
		func(n *html.Node, v goja.Value) {
			if v.ToBoolean() {
				b.setAttrMut(n, "checked", "")
			} else {
				if hasAttr(n, "checked") {
					removeAttr(n, "checked")
					b.onMutate(mutationRecord{typ: "attributes", target: n, attr: "checked"})
				}
			}
		})

	// href/src reflect their attributes (the getter resolves to an absolute URL,
	// as real browsers do), so `a.href = "/x"` on a created element produces a
	// link that extraction sees — not a wrapper-only property.
	for _, attrName := range []string{"href", "src"} {
		name := attrName
		b.protoProp(p, name,
			func(n *html.Node) goja.Value {
				v := getAttr(n, name)
				if v == "" {
					return vm.ToValue("")
				}
				if u := b.resolveNav(v); u != nil {
					return vm.ToValue(u.String())
				}
				return vm.ToValue(v)
			},
			func(n *html.Node, v goja.Value) { b.setAttrMut(n, name, v.String()) })
	}

	// The anchor-as-URL-parser trick (createElement('a'); a.href = u; read back
	// a.pathname) is load-bearing in Angular's getBaseHref and many libraries.
	// Mirror HTMLHyperlinkElementUtils on <a>/<area>: each component resolves the
	// href attribute against the current location; undefined on other tags.
	for name, comp := range map[string]func(*url.URL) string{
		"protocol": func(u *url.URL) string { return u.Scheme + ":" },
		"host":     func(u *url.URL) string { return u.Host },
		"hostname": func(u *url.URL) string { return u.Hostname() },
		"port":     func(u *url.URL) string { return u.Port() },
		"pathname": func(u *url.URL) string {
			if u.Path == "" && (u.Scheme == "http" || u.Scheme == "https") {
				return "/"
			}
			return u.Path
		},
		"search": func(u *url.URL) string { return rawQuery(u.RawQuery) },
		"hash":   func(u *url.URL) string { return rawFragment(u.Fragment) },
		"origin": func(u *url.URL) string { return u.Scheme + "://" + u.Host },
	} {
		comp := comp
		b.protoGetter(p, name, func(n *html.Node) goja.Value {
			if n.Data != "a" && n.Data != "area" {
				return goja.Undefined()
			}
			if !hasAttr(n, "href") {
				return vm.ToValue("")
			}
			if u := b.resolveNav(getAttr(n, "href")); u != nil {
				return vm.ToValue(comp(u))
			}
			return vm.ToValue("")
		})
	}

	b.protoGetter(p, "dataset", func(n *html.Node) goja.Value { return b.datasetFor(n) })
	b.protoGetter(p, "style", func(n *html.Node) goja.Value { return b.styleFor(n) })

	b.protoMethod(p, "click", func(n *html.Node, call goja.FunctionCall) goja.Value {
		b.dispatchOnNode(n, b.newEvent("click", true, true, call.This))
		return goja.Undefined()
	})
	b.protoMethod(p, "focus", func(n *html.Node, _ goja.FunctionCall) goja.Value { b.focusNode(n); return goja.Undefined() })
	b.protoMethod(p, "blur", func(n *html.Node, _ goja.FunctionCall) goja.Value { b.blurNode(n); return goja.Undefined() })

	// Flat Shadow DOM + <template>.content (Phase C).
	b.protoMethod(p, "attachShadow", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.attachShadow(n, call.Argument(0))
	})
	b.protoGetter(p, "shadowRoot", func(n *html.Node) goja.Value {
		if sr, ok := b.shadowRoots[n]; ok {
			// A closed root is hidden from page JS (browser-faithful feature detection),
			// but its content is still composed into extraction output (mission: see
			// everything) — the compose pass iterates b.shadowRoots regardless of mode.
			if m := sr.Get("mode"); m != nil && m.String() == "closed" {
				return goja.Null()
			}
			return sr
		}
		return goja.Null()
	})
	b.protoGetter(p, "content", func(n *html.Node) goja.Value {
		if n.Data == "template" {
			return b.templateContent(n)
		}
		return goja.Undefined()
	})

	// <slot> API (Phase 23). Distribution itself is computed by the compose pass; these
	// expose it to component code. assignedNodes/assignedElements resolve the slot's host
	// across the shadow boundary and return the host's light children for this slot name.
	b.protoProp(p, "slot",
		func(n *html.Node) goja.Value { return vm.ToValue(getAttr(n, "slot")) },
		func(n *html.Node, v goja.Value) { b.setAttrMut(n, "slot", v.String()) })
	b.protoMethod(p, "assignedNodes", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.slotAssigned(n, call.Argument(0), false)
	})
	b.protoMethod(p, "assignedElements", func(n *html.Node, call goja.FunctionCall) goja.Value {
		return b.slotAssigned(n, call.Argument(0), true)
	})
	b.protoMethod(p, "remove", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		if n.Parent != nil {
			parent := n.Parent
			parent.RemoveChild(n)
			b.onMutate(mutationRecord{typ: "childList", target: parent, removed: []*html.Node{n}})
		}
		return goja.Undefined()
	})
}

// --- Lenient geometry (Phase B): honest constant stubs, never computes layout ---

// zeroRect returns a fresh DOMRect-shaped object with every dimension 0. There is
// no layout engine; this only keeps framework geometry probes from throwing.
func (b *bridge) zeroRect() goja.Value {
	o := b.vm.NewObject()
	for _, k := range []string{"x", "y", "top", "left", "right", "bottom", "width", "height"} {
		_ = o.Set(k, 0)
	}
	_ = o.Set("toJSON", func(goja.FunctionCall) goja.Value { return o })
	return o
}

// installGeometry adds the read-only geometry surface to Element/HTMLElement.
// Every value is a constant (zeros / null) — documented as fake.
func (b *bridge) installGeometry() {
	vm := b.vm
	el := b.protoElement
	he := b.protoHTMLElement

	b.protoMethod(el, "getBoundingClientRect", func(_ *html.Node, _ goja.FunctionCall) goja.Value { return b.zeroRect() })
	b.protoMethod(el, "getClientRects", func(_ *html.Node, _ goja.FunctionCall) goja.Value { return vm.NewArray() })
	b.protoMethod(el, "scrollIntoView", func(_ *html.Node, _ goja.FunctionCall) goja.Value { return goja.Undefined() })

	for _, name := range []string{
		"offsetWidth", "offsetHeight", "offsetTop", "offsetLeft",
		"clientWidth", "clientHeight", "clientTop", "clientLeft",
		"scrollWidth", "scrollHeight", "scrollTop", "scrollLeft",
	} {
		b.protoGetter(he, name, func(_ *html.Node) goja.Value { return vm.ToValue(0) })
	}
	b.protoGetter(he, "offsetParent", func(_ *html.Node) goja.Value { return goja.Null() })
}

// --- CharacterData.prototype: Text/Comment members ---

func (b *bridge) installCharacterDataProto() {
	vm := b.vm
	p := b.protoCharacterData
	b.protoProp(p, "data",
		func(n *html.Node) goja.Value { return vm.ToValue(n.Data) },
		func(n *html.Node, v goja.Value) {
			n.Data = v.String()
			b.onMutate(mutationRecord{typ: "characterData", target: n})
		})
	b.protoGetter(p, "length", func(n *html.Node) goja.Value { return vm.ToValue(len(n.Data)) })
}
