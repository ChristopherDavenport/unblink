package js

import (
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Reflected IDL attributes (HTML Standard, "Reflecting content attributes in IDL
// attributes"): input.name, a.title, el.hidden = true, link.rel = "…".
//
// Without them a property write is an expando on the JS wrapper that the node
// tree never sees — so extraction, which reads only the tree, missed whatever a
// page set through properties, and frameworks that pick "property or attribute"
// with `name in el` (Preact, Vue) took the attribute branch by accident. With
// them the property and the content attribute are one piece of state, which is
// the only kind of state this engine has: there is no separate "current value".
//
// The table below is the standard's reflection rules in a compact form, parsed
// once per process. Per runtime, each distinct rule becomes ONE native function
// that is both getter and setter and is shared by every interface using the rule.

type reflKind uint8

const (
	reflString   reflKind = iota // DOMString: "" when absent
	reflBool                     // boolean: presence
	reflURL                      // USVString URL: resolved against the document base
	reflLong                     // long, with a default
	reflULong                    // unsigned long, with a default
	reflEnum                     // enumerated: limited to known values
	reflNullable                 // nullable DOMString (ARIA): null when absent
	reflTokens                   // DOMTokenList
	reflSpecial                  // per-name rule in reflectSpecial
)

type reflSpec struct {
	idl, attr string
	kind      reflKind
	def       int
	enum      *reflEnumDef
	key       string // identity for sharing the accessor function
}

// reflEnumDef is an enumerated attribute's keyword set. known maps a lowercased
// attribute value to its canonical keyword; missing/invalid are the IDL values
// for an absent and an unrecognised attribute. nullable makes "absent" read as
// null (crossOrigin, popover).
type reflEnumDef struct {
	known            map[string]string
	missing, invalid string
	nullable         bool
}

const (
	reflRefPolicy = "e(||no-referrer,no-referrer-when-downgrade,same-origin,origin,strict-origin,origin-when-cross-origin,strict-origin-when-cross-origin,unsafe-url)"
	reflEnctype   = "e(application/x-www-form-urlencoded|application/x-www-form-urlencoded|application/x-www-form-urlencoded,multipart/form-data,text/plain)"
	reflFormEnc   = "e(||application/x-www-form-urlencoded,multipart/form-data,text/plain)"
	reflCORS      = "n(|anonymous|anonymous,use-credentials)"
	reflPriority  = "e(auto|auto|high,low,auto)"
	reflLoading   = "e(eager|eager|lazy,eager)"
)

// reflectTable lists, per interface, its reflected attributes as
// "idl[=attr][:kind]". attr defaults to the lowercased idl name; kind defaults
// to a string. Kinds: b boolean, u URL, l(def) long, ul(def) unsigned long,
// e(missing|invalid|v1,v2,…) enumerated, n(…) nullable enumerated, t token list,
// x special. Members the engine already implements elsewhere (id, className,
// slot, href, src, value, checked) are not repeated here.
//
// Leave out an attribute whose presence pages read as a FEATURE the engine does
// not have: `'popover' in HTMLElement.prototype` means showPopover() works,
// `'shadowRootMode' in HTMLTemplateElement.prototype` means the parser attaches
// declarative shadow roots (it does not on the JS path, and saying so would
// switch off the polyfills that do it). Reflecting those would be a lie that
// breaks the page.
var reflectTable = map[string]string{
	"Element": "role:ns " + ariaReflectSpecs(),
	"HTMLElement": "title lang dir:e(||ltr,rtl,auto) hidden:x tabIndex:x accessKey draggable:x spellcheck:x " +
		"translate:x inert:b autofocus:b " +
		"enterKeyHint:e(||enter,done,go,next,previous,search,send) " +
		"inputMode:e(||none,text,tel,url,email,numeric,decimal,search) autocapitalize contentEditable:x",

	"HTMLAnchorElement": "target download ping rel hreflang type referrerPolicy:" + reflRefPolicy + " name rev charset coords shape text:x",
	"HTMLAreaElement":   "alt coords shape target download ping rel referrerPolicy:" + reflRefPolicy + " noHref:b",
	"HTMLBaseElement":   "target",
	"HTMLBodyElement":   "text link vLink aLink bgColor background",
	"HTMLBRElement":     "clear",
	"HTMLButtonElement": "disabled:b formAction:x formEnctype:" + reflFormEnc + " formMethod:e(||get,post,dialog) " +
		"formNoValidate:b formTarget name type:e(submit|submit|submit,reset,button) form:x",
	"HTMLCanvasElement":   "width:ul(300) height:ul(150)",
	"HTMLDetailsElement":  "open:b name",
	"HTMLDialogElement":   "open:b",
	"HTMLDivElement":      "align",
	"HTMLDListElement":    "compact:b",
	"HTMLEmbedElement":    "type width height align name",
	"HTMLFieldSetElement": "disabled:b name form:x",
	"HTMLFontElement":     "color face size",
	"HTMLFormElement": "acceptCharset=accept-charset action:x autocomplete:e(on|on|on,off) enctype:" + reflEnctype +
		" encoding=enctype:" + reflEnctype + " method:e(get|get|get,post,dialog) name noValidate:b target rel",
	"HTMLHeadingElement": "align",
	"HTMLHRElement":      "align color noShade:b size width",
	"HTMLHtmlElement":    "version",
	"HTMLIFrameElement": "srcdoc name allow allowFullscreen:b width height referrerPolicy:" + reflRefPolicy +
		" loading:" + reflLoading + " sandbox:t align scrolling frameBorder longDesc:u marginHeight marginWidth",
	"HTMLImageElement": "alt srcset sizes crossOrigin:" + reflCORS + " useMap isMap:b width:ul(0) height:ul(0) referrerPolicy:" + reflRefPolicy +
		" decoding:e(auto|auto|sync,async,auto) loading:" + reflLoading + " fetchPriority:" + reflPriority +
		" name lowsrc:u align hspace:ul(0) vspace:ul(0) longDesc:u border",
	"HTMLInputElement": "accept alt autocomplete defaultChecked=checked:b defaultValue=value dirName disabled:b formAction:x " +
		"formEnctype:" + reflFormEnc + " formMethod:e(||get,post,dialog) formNoValidate:b formTarget height:ul(0) max " +
		"maxLength:l(-1) min minLength:l(-1) multiple:b name pattern placeholder readOnly:b required:b size:ul(20) step " +
		"type:e(text|text|hidden,text,search,tel,url,email,password,date,month,week,time,datetime-local,number,range,color," +
		"checkbox,radio,file,submit,image,reset,button) width:ul(0) align useMap form:x",
	"HTMLLabelElement":  "htmlFor=for form:x",
	"HTMLLegendElement": "align",
	"HTMLLIElement":     "type",
	"HTMLLinkElement": "crossOrigin:" + reflCORS + " rel as media integrity hreflang type referrerPolicy:" + reflRefPolicy +
		" disabled:b imageSrcset imageSizes fetchPriority:" + reflPriority + " charset rev target sizes:t blocking:t",
	"HTMLMapElement":  "name",
	"HTMLMenuElement": "compact:b",
	"HTMLMetaElement": "name httpEquiv=http-equiv content media scheme",
	"HTMLModElement":  "cite:u dateTime",
	"HTMLObjectElement": "data:u type name useMap width height align archive code declare:b hspace:ul(0) standby vspace:ul(0) " +
		"codeBase:u codeType border form:x",
	"HTMLOListElement":     "reversed:b start:l(1) type compact:b",
	"HTMLOptGroupElement":  "disabled:b label",
	"HTMLOptionElement":    "disabled:b defaultSelected=selected:b selected:x label:x text:x value:x form:x",
	"HTMLOutputElement":    "htmlFor=for:t name form:x",
	"HTMLParagraphElement": "align",
	"HTMLParamElement":     "name type valueType",
	"HTMLPreElement":       "width:l(0)",
	"HTMLQuoteElement":     "cite:u",
	"HTMLScriptElement": "type noModule:b async:b defer:b crossOrigin:" + reflCORS + " text:x integrity referrerPolicy:" + reflRefPolicy +
		" charset event htmlFor=for fetchPriority:" + reflPriority + " blocking:t",
	"HTMLSelectElement":       "autocomplete disabled:b multiple:b name required:b size:ul(0) form:x",
	"HTMLSlotElement":         "name",
	"HTMLSourceElement":       "type srcset sizes media width:ul(0) height:ul(0)",
	"HTMLStyleElement":        "media type blocking:t",
	"HTMLTableElement":        "align border frame rules summary width bgColor cellPadding cellSpacing",
	"HTMLTableCaptionElement": "align",
	"HTMLTableCellElement": "colSpan:ul(1) rowSpan:ul(1) headers scope:e(||row,col,rowgroup,colgroup) abbr align axis height width " +
		"ch=char chOff=charoff noWrap:b vAlign bgColor",
	"HTMLTableColElement":     "span:ul(1) align ch=char chOff=charoff vAlign width",
	"HTMLTableRowElement":     "align ch=char chOff=charoff vAlign bgColor",
	"HTMLTableSectionElement": "align ch=char chOff=charoff vAlign",
	"HTMLTextAreaElement": "autocomplete cols:ul(20) dirName disabled:b maxLength:l(-1) minLength:l(-1) name placeholder " +
		"readOnly:b required:b rows:ul(2) wrap defaultValue:x form:x",
	"HTMLTimeElement":  "dateTime",
	"HTMLTrackElement": "kind:e(subtitles|metadata|subtitles,captions,descriptions,chapters,metadata) srclang label default:b",
	"HTMLUListElement": "compact:b type",
	"HTMLMediaElement": "crossOrigin:" + reflCORS + " preload:e(metadata|metadata|none,metadata,auto) autoplay:b loop:b controls:b defaultMuted=muted:b",
	"HTMLVideoElement": "width:ul(0) height:ul(0) poster:u playsInline:b",
}

// ariaReflectSpecs builds ARIAMixin: ariaLabel ↔ aria-label, and so on. Each is
// a nullable string.
func ariaReflectSpecs() string {
	names := strings.Fields(`Atomic AutoComplete BrailleLabel BrailleRoleDescription Busy Checked ColCount ColIndex
		ColIndexText ColSpan Current Description Disabled Expanded HasPopup Hidden Invalid KeyShortcuts Label
		Level Live Modal MultiLine MultiSelectable Orientation Placeholder PosInSet Pressed ReadOnly Relevant
		Required RoleDescription RowCount RowIndex RowIndexText RowSpan Selected SetSize Sort ValueMax ValueMin
		ValueNow ValueText`)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("aria" + n + "=aria-" + strings.ToLower(n) + ":ns ")
	}
	return sb.String()
}

// reflectSpecs is reflectTable parsed: interface name → specs.
var reflectSpecs = func() map[string][]reflSpec {
	out := make(map[string][]reflSpec, len(reflectTable))
	enums := map[string]*reflEnumDef{}
	for iface, line := range reflectTable {
		for _, tok := range strings.Fields(line) {
			out[iface] = append(out[iface], parseReflSpec(tok, enums))
		}
	}
	return out
}()

func parseReflSpec(tok string, enums map[string]*reflEnumDef) reflSpec {
	name, kind, _ := strings.Cut(tok, ":")
	s := reflSpec{}
	s.idl, s.attr, _ = strings.Cut(name, "=")
	if s.attr == "" {
		s.attr = strings.ToLower(s.idl)
	}
	arg := ""
	if i := strings.IndexByte(kind, '('); i >= 0 && strings.HasSuffix(kind, ")") {
		kind, arg = kind[:i], kind[i+1:len(kind)-1]
	}
	switch kind {
	case "":
		s.kind = reflString
	case "b":
		s.kind = reflBool
	case "u":
		s.kind = reflURL
	case "l", "ul":
		s.kind = reflLong
		if kind == "ul" {
			s.kind = reflULong
		}
		s.def, _ = strconv.Atoi(arg)
	case "e", "n":
		s.kind = reflEnum
		id := kind + arg
		def := enums[id]
		if def == nil {
			parts := strings.SplitN(arg, "|", 3)
			def = &reflEnumDef{missing: parts[0], invalid: parts[1], known: map[string]string{}, nullable: kind == "n"}
			for _, v := range strings.Split(parts[2], ",") {
				def.known[v] = v
			}
			enums[id] = def
		}
		s.enum = def
	case "ns":
		s.kind = reflNullable
	case "t":
		s.kind = reflTokens
	case "x":
		s.kind = reflSpecial
	default:
		panic("reflect: unknown kind in " + tok)
	}
	s.key = s.idl + "\x00" + s.attr + "\x00" + kind + arg
	return s
}

// reflectEager are the interfaces whose reflected attributes are defined when
// the runtime is set up: the two every element shares, and those pages probe on
// the prototype before any such element exists
// (`'noModule' in HTMLScriptElement.prototype`, `'loading' in HTMLIFrameElement.prototype`,
// `'loading' in HTMLImageElement.prototype`).
var reflectEager = []string{"Element", "HTMLElement", "HTMLScriptElement", "HTMLImageElement", "HTMLLinkElement", "HTMLIFrameElement"}

// reflectParent names the interface whose attributes an interface inherits and
// which must therefore be installed with it.
var reflectParent = map[string]string{"HTMLVideoElement": "HTMLMediaElement", "HTMLAudioElement": "HTMLMediaElement"}

// installReflection defines the reflected attributes of the eager interfaces
// and arms the rest: an accessor costs allocations on every render, and a page
// touches only some of the ~60 interfaces, so each of the others is installed
// the first time an element of that interface is wrapped (reflectOnWrap).
func (b *bridge) installReflection() {
	b.reflectAccessors = make(map[string]goja.Value)
	b.reflectPending = make(map[*goja.Object]string, len(reflectSpecs))
	for iface := range reflectSpecs {
		if p := b.reflectProto(iface); p != nil {
			b.reflectPending[p] = iface
		}
	}
	for iface, parent := range reflectParent { // an interface with no rows of its own still inherits
		if p := b.reflectProto(iface); p != nil && b.reflectProto(parent) != nil {
			if _, ok := b.reflectPending[p]; !ok {
				b.reflectPending[p] = iface
			}
		}
	}
	for _, iface := range reflectEager {
		b.reflectInstall(iface)
	}
}

func (b *bridge) reflectProto(iface string) *goja.Object {
	switch iface {
	case "Element":
		return b.protoElement
	case "HTMLElement":
		return b.protoHTMLElement
	}
	return b.ifaceProtos[iface]
}

// reflectOnWrap installs the reflected attributes of the interface behind proto
// if they are still pending. Called for every element wrap, so the common case
// — nothing pending for this prototype — is one map lookup.
func (b *bridge) reflectOnWrap(proto *goja.Object) {
	if len(b.reflectPending) == 0 {
		return
	}
	if iface, ok := b.reflectPending[proto]; ok {
		b.reflectInstall(iface)
	}
}

// reflectInstall defines iface's reflected attributes (and its parent's) on its
// prototype. A name the table lists for an interface overrides a generic member
// further down the chain for that interface only.
func (b *bridge) reflectInstall(iface string) {
	proto := b.reflectProto(iface)
	if proto == nil {
		return
	}
	if _, pending := b.reflectPending[proto]; !pending {
		return
	}
	delete(b.reflectPending, proto)
	if parent := reflectParent[iface]; parent != "" {
		b.reflectInstall(parent)
	}
	specs := reflectSpecs[iface]
	for i := range specs {
		s := &specs[i]
		acc := b.reflectAccessors[s.key]
		if acc == nil {
			acc = b.reflectAccessor(s)
			b.reflectAccessors[s.key] = acc
		}
		set := acc
		if s.kind == reflSpecial && s.idl == "form" {
			set = nil // read-only
		}
		_ = proto.DefineAccessorProperty(s.idl, acc, set, goja.FLAG_TRUE, goja.FLAG_TRUE)
	}
}

// reflectAccessor builds the one function that reads (no argument) and writes
// (one argument) a reflected attribute on whatever element it is invoked on.
func (b *bridge) reflectAccessor(s *reflSpec) goja.Value {
	return b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		n := b.nodeOf(call.This)
		if n == nil || n.Type != html.ElementNode {
			return goja.Undefined()
		}
		if len(call.Arguments) == 0 {
			return b.reflectGet(n, s)
		}
		b.reflectSet(n, s, call.Arguments[0])
		return goja.Undefined()
	})
}

func (b *bridge) reflectGet(n *html.Node, s *reflSpec) goja.Value {
	vm := b.vm
	switch s.kind {
	case reflString:
		return vm.ToValue(getAttr(n, s.attr))
	case reflBool:
		return vm.ToValue(hasAttr(n, s.attr))
	case reflURL:
		return vm.ToValue(b.reflectURL(n, s.attr, false))
	case reflLong, reflULong:
		if v, err := strconv.Atoi(strings.TrimSpace(getAttr(n, s.attr))); err == nil && (s.kind == reflLong || v >= 0) {
			return vm.ToValue(v)
		}
		return vm.ToValue(s.def)
	case reflEnum:
		if !hasAttr(n, s.attr) {
			if s.enum.nullable {
				return goja.Null()
			}
			return vm.ToValue(s.enum.missing)
		}
		if v, ok := s.enum.known[strings.ToLower(getAttr(n, s.attr))]; ok {
			return vm.ToValue(v)
		}
		return vm.ToValue(s.enum.invalid)
	case reflNullable:
		if !hasAttr(n, s.attr) {
			return goja.Null()
		}
		return vm.ToValue(getAttr(n, s.attr))
	case reflTokens:
		return b.tokenListFor(n, s.attr, nil)
	}
	return b.reflectSpecialGet(n, s)
}

func (b *bridge) reflectSet(n *html.Node, s *reflSpec, v goja.Value) {
	switch s.kind {
	case reflString, reflURL, reflTokens: // a token list forwards assignment to its value
		b.setAttrMut(n, s.attr, v.String())
	case reflBool:
		b.setBoolAttr(n, s.attr, v.ToBoolean())
	case reflLong:
		b.setAttrMut(n, s.attr, strconv.FormatInt(int64(int32(v.ToInteger())), 10))
	case reflULong:
		i := v.ToInteger()
		if i < 0 || i > 2147483647 {
			i = int64(s.def)
		}
		b.setAttrMut(n, s.attr, strconv.FormatInt(i, 10))
	case reflEnum, reflNullable:
		if (s.kind == reflNullable || s.enum.nullable) && (goja.IsNull(v) || goja.IsUndefined(v)) {
			b.removeAttrMut(n, s.attr)
			return
		}
		b.setAttrMut(n, s.attr, v.String())
	case reflSpecial:
		b.reflectSpecialSet(n, s, v)
	}
}

// setBoolAttr adds or removes a boolean attribute, doing nothing when it is
// already in the wanted state.
func (b *bridge) setBoolAttr(n *html.Node, attr string, on bool) {
	switch {
	case on && !hasAttr(n, attr):
		b.setAttrMut(n, attr, "")
	case !on:
		b.removeAttrMut(n, attr)
	}
}

// removeAttrMut removes an attribute and records the mutation; a no-op when the
// attribute is absent.
func (b *bridge) removeAttrMut(n *html.Node, attr string) {
	if !hasAttr(n, attr) {
		return
	}
	old := getAttr(n, attr)
	removeAttr(n, attr)
	if len(attr) > 2 && attr[0] == 'o' && attr[1] == 'n' {
		b.handlerAttrRemoved(n, attr[2:])
	}
	b.onMutate(mutationRecord{typ: "attributes", target: n, attr: attr, oldValue: old})
}

// reflectURL returns attr resolved against the document base. An absent
// attribute reads "" — or the document's own URL when emptyIsDocument is set
// (form.action, button.formAction).
func (b *bridge) reflectURL(n *html.Node, attr string, emptyIsDocument bool) string {
	raw := strings.TrimSpace(getAttr(n, attr))
	if raw == "" {
		if emptyIsDocument && b.currentURL != nil {
			return b.currentURL.String()
		}
		return ""
	}
	if u := b.resolveNav(raw); u != nil {
		return u.String()
	}
	return raw
}

// focusableByDefault reports whether n takes part in sequential focus
// navigation without a tabindex attribute (tabIndex defaults to 0, else -1).
func focusableByDefault(n *html.Node) bool {
	switch n.Data {
	case "a", "area":
		return hasAttr(n, "href")
	case "input":
		return !strings.EqualFold(getAttr(n, "type"), "hidden")
	case "button", "select", "textarea", "iframe", "summary":
		return true
	}
	return hasAttr(n, "contenteditable") && !strings.EqualFold(getAttr(n, "contenteditable"), "false")
}

func (b *bridge) reflectSpecialGet(n *html.Node, s *reflSpec) goja.Value {
	vm := b.vm
	switch s.idl {
	case "hidden":
		if !hasAttr(n, "hidden") {
			return vm.ToValue(false)
		}
		if strings.EqualFold(getAttr(n, "hidden"), "until-found") {
			return vm.ToValue("until-found")
		}
		return vm.ToValue(true)
	case "tabIndex":
		if v, err := strconv.Atoi(strings.TrimSpace(getAttr(n, "tabindex"))); err == nil {
			return vm.ToValue(v)
		}
		if focusableByDefault(n) {
			return vm.ToValue(0)
		}
		return vm.ToValue(-1)
	case "draggable":
		switch strings.ToLower(getAttr(n, "draggable")) {
		case "true":
			return vm.ToValue(true)
		case "false":
			return vm.ToValue(false)
		}
		return vm.ToValue(n.Data == "img" || (n.Data == "a" && hasAttr(n, "href")))
	case "spellcheck":
		return vm.ToValue(!strings.EqualFold(getAttr(n, "spellcheck"), "false"))
	case "translate":
		return vm.ToValue(!strings.EqualFold(getAttr(n, "translate"), "no"))
	case "contentEditable":
		if !hasAttr(n, "contenteditable") {
			return vm.ToValue("inherit")
		}
		switch v := strings.ToLower(getAttr(n, "contenteditable")); v {
		case "", "true":
			return vm.ToValue("true")
		case "false", "plaintext-only":
			return vm.ToValue(v)
		}
		return vm.ToValue("inherit")
	case "text":
		if n.Data == "option" {
			return vm.ToValue(strings.Join(strings.Fields(textContent(n)), " "))
		}
		return vm.ToValue(textContent(n))
	case "defaultValue": // <textarea>: its child text
		return vm.ToValue(textContent(n))
	case "action", "formAction":
		return vm.ToValue(b.reflectURL(n, strings.ToLower(s.idl), true))
	case "selected":
		// Selectedness, not just the attribute: a single-choice select with
		// nothing marked has its first enabled option selected.
		if sel := enclosingSelect(n); sel != nil {
			for _, o := range selectedOptions(sel) {
				if o == n {
					return vm.ToValue(true)
				}
			}
			return vm.ToValue(false)
		}
		return vm.ToValue(hasAttr(n, "selected"))
	case "label": // <option>: the label attribute, else its text
		if hasAttr(n, "label") {
			return vm.ToValue(getAttr(n, "label"))
		}
		return vm.ToValue(strings.Join(strings.Fields(textContent(n)), " "))
	case "value": // <option>: the value attribute, else its text
		if hasAttr(n, "value") {
			return vm.ToValue(getAttr(n, "value"))
		}
		return vm.ToValue(strings.Join(strings.Fields(textContent(n)), " "))
	case "form":
		if f := b.formOwner(n); f != nil {
			return b.wrap(f)
		}
		return goja.Null()
	}
	return goja.Undefined()
}

func (b *bridge) reflectSpecialSet(n *html.Node, s *reflSpec, v goja.Value) {
	switch s.idl {
	case "hidden":
		// false, null, undefined, "", 0 and NaN unset it; "until-found" keeps its
		// keyword; anything else is plain hidden.
		if str, ok := v.Export().(string); ok && strings.EqualFold(str, "until-found") {
			b.setAttrMut(n, "hidden", "until-found")
			return
		}
		if !v.ToBoolean() {
			b.removeAttrMut(n, "hidden")
			return
		}
		if !hasAttr(n, "hidden") || getAttr(n, "hidden") != "" {
			b.setAttrMut(n, "hidden", "")
		}
	case "tabIndex":
		b.setAttrMut(n, "tabindex", strconv.FormatInt(int64(int32(v.ToInteger())), 10))
	case "draggable", "spellcheck":
		b.setAttrMut(n, s.attr, strconv.FormatBool(v.ToBoolean()))
	case "translate":
		val := "no"
		if v.ToBoolean() {
			val = "yes"
		}
		b.setAttrMut(n, "translate", val)
	case "contentEditable":
		switch val := strings.ToLower(v.String()); val {
		case "inherit":
			b.removeAttrMut(n, "contenteditable")
		case "true", "false", "plaintext-only":
			b.setAttrMut(n, "contenteditable", val)
		default:
			b.throwDOMException("SyntaxError", "The value provided ('"+v.String()+"') is not one of 'true', 'false', 'plaintext-only', or 'inherit'.")
		}
	case "text", "defaultValue":
		setTextContent(n, v.String())
		b.onMutate(mutationRecord{typ: "childList", target: n})
	case "action", "formAction":
		b.setAttrMut(n, strings.ToLower(s.idl), v.String())
	case "selected":
		on := v.ToBoolean()
		if on {
			// Selecting an option of a single-choice <select> deselects the rest.
			if sel := enclosingSelect(n); sel != nil && !hasAttr(sel, "multiple") {
				for _, o := range queryAll(sel, "option") {
					if o != n {
						b.removeAttrMut(o, "selected")
					}
				}
			}
		}
		b.setBoolAttr(n, "selected", on)
	case "label", "value":
		b.setAttrMut(n, s.idl, v.String())
	}
}

// enclosingSelect returns the <select> an <option> belongs to, nil if none.
func enclosingSelect(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "select" {
			return p
		}
	}
	return nil
}
