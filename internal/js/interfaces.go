package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Per-tag element interfaces. Every element is an instance of the interface the
// HTML Standard assigns its tag (`<script>` is an HTMLScriptElement), so
// `el instanceof HTMLScriptElement`, `Object.prototype.toString.call(el)` and
// `el.constructor.name` answer as a browser's do. Each interface prototype
// chains to HTMLElement.prototype, where the members actually live — these
// prototypes carry identity (constructor + @@toStringTag), not behaviour, so
// patching `HTMLElement.prototype` or `Element.prototype` still reaches every
// element.

// tagInterface maps an HTML tag to its interface name. A tag absent here is
// either a plain HTMLElement (htmlElementTags) or an HTMLUnknownElement.
var tagInterface = map[string]string{
	"a": "HTMLAnchorElement", "area": "HTMLAreaElement", "audio": "HTMLAudioElement",
	"base": "HTMLBaseElement", "blockquote": "HTMLQuoteElement", "body": "HTMLBodyElement",
	"br": "HTMLBRElement", "button": "HTMLButtonElement", "canvas": "HTMLCanvasElement",
	"caption": "HTMLTableCaptionElement", "col": "HTMLTableColElement",
	"colgroup": "HTMLTableColElement", "data": "HTMLDataElement", "datalist": "HTMLDataListElement",
	"del": "HTMLModElement", "details": "HTMLDetailsElement", "dialog": "HTMLDialogElement",
	"dir": "HTMLDirectoryElement", "div": "HTMLDivElement", "dl": "HTMLDListElement",
	"embed": "HTMLEmbedElement", "fieldset": "HTMLFieldSetElement", "font": "HTMLFontElement",
	"form": "HTMLFormElement", "frame": "HTMLFrameElement", "frameset": "HTMLFrameSetElement",
	"h1": "HTMLHeadingElement", "h2": "HTMLHeadingElement", "h3": "HTMLHeadingElement",
	"h4": "HTMLHeadingElement", "h5": "HTMLHeadingElement", "h6": "HTMLHeadingElement",
	"head": "HTMLHeadElement", "hr": "HTMLHRElement", "html": "HTMLHtmlElement",
	"iframe": "HTMLIFrameElement", "img": "HTMLImageElement", "input": "HTMLInputElement",
	"ins": "HTMLModElement", "label": "HTMLLabelElement", "legend": "HTMLLegendElement",
	"li": "HTMLLIElement", "link": "HTMLLinkElement", "listing": "HTMLPreElement",
	"map": "HTMLMapElement", "marquee": "HTMLMarqueeElement", "menu": "HTMLMenuElement",
	"meta": "HTMLMetaElement", "meter": "HTMLMeterElement", "object": "HTMLObjectElement",
	"ol": "HTMLOListElement", "optgroup": "HTMLOptGroupElement", "option": "HTMLOptionElement",
	"output": "HTMLOutputElement", "p": "HTMLParagraphElement", "param": "HTMLParamElement",
	"picture": "HTMLPictureElement", "pre": "HTMLPreElement", "progress": "HTMLProgressElement",
	"q": "HTMLQuoteElement", "script": "HTMLScriptElement", "select": "HTMLSelectElement",
	"slot": "HTMLSlotElement", "source": "HTMLSourceElement", "span": "HTMLSpanElement",
	"style": "HTMLStyleElement", "table": "HTMLTableElement", "tbody": "HTMLTableSectionElement",
	"td": "HTMLTableCellElement", "template": "HTMLTemplateElement",
	"textarea": "HTMLTextAreaElement", "tfoot": "HTMLTableSectionElement",
	"th": "HTMLTableCellElement", "thead": "HTMLTableSectionElement", "time": "HTMLTimeElement",
	"title": "HTMLTitleElement", "tr": "HTMLTableRowElement", "track": "HTMLTrackElement",
	"ul": "HTMLUListElement", "video": "HTMLVideoElement", "xmp": "HTMLPreElement",
}

// htmlElementTags are the HTML elements whose interface is plain HTMLElement.
var htmlElementTags = func() map[string]bool {
	m := make(map[string]bool)
	for _, t := range strings.Fields(`abbr acronym address article aside b bdi bdo big center cite code dd
		dfn dt em figcaption figure footer header hgroup i kbd main mark nav nobr noembed noframes
		noscript plaintext rb rp rt rtc ruby s samp search section small strike strong sub summary
		sup tt u var wbr`) {
		m[t] = true
	}
	return m
}()

// extraInterfaceNames are interface constructors with no tag mapped to them
// here (or reached through a parent interface) that pages still probe.
var extraInterfaceNames = []string{"HTMLUnknownElement", "HTMLMediaElement"}

// setToStringTag gives proto the @@toStringTag that makes
// Object.prototype.toString.call(x) answer "[object <name>]".
func (b *bridge) setToStringTag(proto *goja.Object, name string) {
	_ = proto.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue(name), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
}

// installElementInterfaces builds one prototype per HTML interface and the
// tag → prototype table protoFor consults.
func (b *bridge) installElementInterfaces() {
	vm := b.vm
	b.ifaceProtos = make(map[string]*goja.Object)
	b.tagProtos = make(map[string]*goja.Object)
	b.svgTagBuild = make(map[string]func() *goja.Object)

	iface := func(name string, parent *goja.Object) *goja.Object {
		if p, ok := b.ifaceProtos[name]; ok {
			return p
		}
		p := vm.CreateObject(parent)
		b.ifaceProtos[name] = p
		b.defineCtor(name, p)
		b.setToStringTag(p, name)
		return p
	}
	// HTMLAudioElement/HTMLVideoElement chain through HTMLMediaElement as in a
	// real DOM; zone.js dereferences HTMLMediaElement.prototype unguarded.
	media := iface("HTMLMediaElement", b.protoHTMLElement)
	iface("HTMLAudioElement", media)
	iface("HTMLVideoElement", media)
	for _, name := range extraInterfaceNames {
		iface(name, b.protoHTMLElement)
	}
	for tag, name := range tagInterface {
		b.tagProtos[tag] = iface(name, b.protoHTMLElement)
	}
	b.protoUnknownElement = b.ifaceProtos["HTMLUnknownElement"]

	// <a>/<area> stringify to their href (the HTMLHyperlinkElementUtils
	// stringifier): `'' + anchor` and template-literal interpolation rely on it.
	for _, name := range []string{"HTMLAnchorElement", "HTMLAreaElement"} {
		b.protoMethod(b.ifaceProtos[name], "toString", func(n *html.Node, _ goja.FunctionCall) goja.Value {
			if !hasAttr(n, "href") {
				return vm.ToValue("")
			}
			if u := b.resolveNav(getAttr(n, "href")); u != nil {
				return vm.ToValue(u.String())
			}
			return vm.ToValue(getAttr(n, "href"))
		})
	}

	// SVG elements keep the HTMLElement member set (style, dataset, focus —
	// which SVGElement really has, through other mixins) by chaining to it. That
	// makes `svg instanceof HTMLElement` true where a browser says false; it was
	// already true before per-tag interfaces existed, and splitting the shared
	// members out is not worth the risk it carries.
	b.protoSVGElement = vm.CreateObject(b.protoHTMLElement)
	b.defineCtor("SVGElement", b.protoSVGElement)
	b.setToStringTag(b.protoSVGElement, "SVGElement")
	for _, name := range []string{"SVGGraphicsElement", "SVGSVGElement"} {
		p := vm.CreateObject(b.protoSVGElement)
		b.defineCtor(name, p)
		b.setToStringTag(p, name)
		b.ifaceProtos[name] = p
	}
	// The rest of the SVG interface family, for instanceof tests (SvelteKit's
	// click handler asks `x instanceof SVGAElement` on every click). They are
	// published as constructors chained to SVGElement / SVGGraphicsElement;
	// elements other than <svg> keep the SVGElement prototype, so the test
	// answers false — never a ReferenceError.
	// Built on first use — the first read of the global, or the first element
	// of that kind wrapped — because a page with no inline SVG never needs them.
	global := vm.GlobalObject()
	lazy := func(name string, parent func() *goja.Object) {
		tag := svgInterfaceTag[name]
		build := func() *goja.Object {
			if p, ok := b.ifaceProtos[name]; ok {
				return p
			}
			_ = global.Delete(name)
			p := vm.CreateObject(parent())
			b.ifaceProtos[name] = p
			b.defineCtor(name, p)
			b.setToStringTag(p, name)
			return p
		}
		if tag != "" {
			b.svgTagBuild[tag] = build
		}
		_ = global.DefineAccessorProperty(name,
			vm.ToValue(func(goja.FunctionCall) goja.Value { build(); return global.Get(name) }),
			nil, // no setter: one function per interface, not two; the property is configurable
			goja.FLAG_TRUE, goja.FLAG_FALSE)
	}
	for _, name := range svgGraphicsInterfaces {
		lazy(name, func() *goja.Object { return b.ifaceProtos["SVGGraphicsElement"] })
	}
	for _, name := range svgPlainInterfaces {
		lazy(name, func() *goja.Object { return b.protoSVGElement })
	}
}

// elementProto picks the interface prototype for an element node.
func (b *bridge) elementProto(n *html.Node) *goja.Object {
	switch n.Namespace {
	case "svg":
		if n.Data == "svg" {
			return b.ifaceProtos["SVGSVGElement"]
		}
		if build, ok := b.svgTagBuild[n.Data]; ok {
			return build()
		}
		return b.protoSVGElement
	case "":
	default: // MathML: a plain Element in browsers; kept on HTMLElement's member set
		return b.protoHTMLElement
	}
	if p, ok := b.tagProtos[n.Data]; ok {
		return p
	}
	// A valid custom-element name (contains a hyphen) is an HTMLElement until it
	// is upgraded, when upgradeElement swaps in the class's own prototype.
	if htmlElementTags[n.Data] || strings.Contains(n.Data, "-") {
		return b.protoHTMLElement
	}
	return b.protoUnknownElement
}

// SVG interfaces published beyond SVGElement/SVGGraphicsElement/SVGSVGElement,
// and the element each one is the interface of.
var (
	svgGraphicsInterfaces = []string{
		"SVGAElement", "SVGGElement", "SVGDefsElement", "SVGUseElement", "SVGImageElement", "SVGSwitchElement",
		"SVGForeignObjectElement", "SVGGeometryElement", "SVGTextContentElement",
	}
	svgPlainInterfaces = []string{
		"SVGTitleElement", "SVGDescElement", "SVGStyleElement", "SVGScriptElement", "SVGSymbolElement", "SVGMarkerElement",
		"SVGClipPathElement", "SVGMaskElement", "SVGPatternElement", "SVGStopElement", "SVGGradientElement",
		"SVGLinearGradientElement", "SVGRadialGradientElement", "SVGFilterElement", "SVGMetadataElement", "SVGViewElement",
		"SVGPathElement", "SVGRectElement", "SVGCircleElement", "SVGEllipseElement", "SVGLineElement",
		"SVGPolylineElement", "SVGPolygonElement", "SVGTextElement", "SVGTSpanElement", "SVGTextPathElement",
		"SVGAnimationElement", "SVGAnimateElement", "SVGAnimateTransformElement", "SVGAnimateMotionElement", "SVGSetElement",
	}
	svgInterfaceTag = map[string]string{
		"SVGAElement": "a", "SVGGElement": "g", "SVGDefsElement": "defs", "SVGUseElement": "use", "SVGImageElement": "image",
		"SVGSwitchElement": "switch", "SVGForeignObjectElement": "foreignObject", "SVGTitleElement": "title", "SVGDescElement": "desc",
		"SVGStyleElement": "style", "SVGScriptElement": "script", "SVGSymbolElement": "symbol", "SVGMarkerElement": "marker",
		"SVGClipPathElement": "clipPath", "SVGMaskElement": "mask", "SVGPatternElement": "pattern", "SVGStopElement": "stop",
		"SVGLinearGradientElement": "linearGradient", "SVGRadialGradientElement": "radialGradient", "SVGFilterElement": "filter",
		"SVGMetadataElement": "metadata", "SVGViewElement": "view", "SVGPathElement": "path", "SVGRectElement": "rect",
		"SVGCircleElement": "circle", "SVGEllipseElement": "ellipse", "SVGLineElement": "line", "SVGPolylineElement": "polyline",
		"SVGPolygonElement": "polygon", "SVGTextElement": "text", "SVGTSpanElement": "tspan", "SVGTextPathElement": "textPath",
		"SVGAnimateElement": "animate", "SVGAnimateTransformElement": "animateTransform", "SVGAnimateMotionElement": "animateMotion", "SVGSetElement": "set",
	}
)
