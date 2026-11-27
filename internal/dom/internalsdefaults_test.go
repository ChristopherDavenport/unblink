package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

// A field belongs to its form owner, which a form="id" attribute can name from
// anywhere in the document — the rule the JS engine's form.elements follows.
func TestExtractFormAttributeOwnership(t *testing.T) {
	p := extractInline(t, `
	<input name="before" value="b" form="f">
	<form id="f" action="/f">
	  <input name="in" value="1">
	  <input name="away" value="x" form="g">
	  <input name="dangling" value="d" form="nope">
	  <input name="notaform" value="n" form="div">
	</form>
	<div id="div"></div>
	<form id="g" action="/g"><select name="pick"><option>a</option></select></form>
	<textarea name="notes" form="f">hi</textarea>
	<my-input name="email" form="f" `+page.FormValueAttr+`="email=a%40b.c"></my-input>
	<my-input name="plain" form="f"></my-input>
	<form id="f" action="/dup"><input name="dup" value="2"></form>`)
	want := map[string]string{
		"/f":   "before,in,notes,email",
		"/g":   "away,pick",
		"/dup": "dup", // a second element with the id is never the one named
	}
	if len(p.Meta.Forms) != len(want) {
		t.Fatalf("forms = %d, want %d", len(p.Meta.Forms), len(want))
	}
	for _, f := range p.Meta.Forms {
		var names []string
		for _, fld := range f.Fields {
			names = append(names, fld.Name)
		}
		path := strings.TrimPrefix(f.Action, "https://example.com")
		if got := strings.Join(names, ","); got != want[path] {
			t.Errorf("form %s fields = %s, want %s", path, got, want[path])
		}
	}
}

// Without any form attribute in the document, a form owns its descendant fields.
func TestExtractFormFieldsWithoutFormAttribute(t *testing.T) {
	p := extractInline(t, `<form action="/a"><input name="x"><textarea name="y"></textarea></form>
	<input name="orphan"><form action="/b"><select name="z"></select></form>`)
	var got []string
	for _, f := range p.Meta.Forms {
		for _, fld := range f.Fields {
			got = append(got, fld.Name)
		}
	}
	if strings.Join(got, ",") != "x,y,z" {
		t.Errorf("fields = %v, want x,y,z", got)
	}
}

func controlBySelector(t *testing.T, p *page.Page, sel string) page.Control {
	t.Helper()
	for _, c := range p.Meta.Controls {
		if c.Selector == sel {
			return c
		}
	}
	t.Fatalf("no control %q in %+v", sel, p.Meta.Controls)
	return page.Control{}
}

// A custom element's ElementInternals defaults, reflected by the engine under
// page.AriaDefaultPrefix, give the control its role, name and state — and an
// explicit attribute on the element wins over each default.
func TestExtractControlsUseInternalsDefaults(t *testing.T) {
	d := page.AriaDefaultPrefix
	p := extractInline(t, `
	<span id="lbl">Dark mode</span>
	<my-switch id="sw" tabindex="0" `+d+`role="switch" `+d+`aria-label="Theme" `+d+`aria-checked="true"
	  `+d+`aria-required="true" `+d+`aria-invalid="true" `+d+`aria-disabled="true"></my-switch>
	<my-button id="btn" `+d+`role="button" `+d+`aria-pressed="false" `+d+`aria-expanded="true">Menu</my-button>
	<my-tab id="tab" `+d+`role="tab" `+d+`aria-selected="true" `+page.AriaDefaultLabelledByAttr+`="Billing"></my-tab>
	<my-switch id="own" tabindex="0" role="checkbox" aria-label="Own" aria-checked="false" aria-disabled="false"
	  aria-labelledby="" `+d+`role="switch" `+d+`aria-label="Default" `+d+`aria-checked="true" `+d+`aria-disabled="true"
	  `+page.AriaDefaultLabelledByAttr+`="Default text"></my-switch>
	<my-button id="lb" aria-labelledby="lbl" `+d+`role="button" `+d+`aria-label="Default"></my-button>
	<my-button id="pres" role="presentation" `+d+`role="button">not a control</my-button>
	<div id="plain" tabindex="0" `+d+`role="switch" `+d+`aria-label="Spoof" `+d+`aria-checked="true">native</div>`)

	sw := controlBySelector(t, p, "#sw")
	if sw.Role != "switch" || sw.Text != "Theme" || sw.Checked != "true" || !sw.Required || !sw.Invalid || !sw.Disabled {
		t.Errorf("#sw = %+v", sw)
	}
	btn := controlBySelector(t, p, "#btn")
	if btn.Kind != "role-button" || btn.Role != "button" || btn.Text != "Menu" || btn.Pressed != "false" || btn.Expanded != "true" {
		t.Errorf("#btn = %+v", btn)
	}
	tab := controlBySelector(t, p, "#tab")
	if tab.Kind != "tab" || tab.Text != "Billing" || tab.Selected != "true" {
		t.Errorf("#tab = %+v", tab)
	}
	own := controlBySelector(t, p, "#own")
	if own.Role != "checkbox" || own.Text != "Own" || own.Checked != "false" || own.Disabled {
		t.Errorf("explicit attributes must win over the defaults: %+v", own)
	}
	// An explicit aria-labelledby outranks a default aria-label, as it would a real one.
	if lb := controlBySelector(t, p, "#lb"); lb.Text != "Dark mode" {
		t.Errorf("#lb = %+v", lb)
	}
	for _, c := range p.Meta.Controls {
		if c.Selector == "#pres" {
			t.Errorf("explicit role=presentation must keep the element out of controls: %+v", c)
		}
	}
	// Only a custom element has ElementInternals; elsewhere the attributes mean nothing.
	if plain := controlBySelector(t, p, "#plain"); plain.Role != "" || plain.Text != "native" || plain.Checked != "" {
		t.Errorf("#plain = %+v", plain)
	}
}

// The same defaults describe a form-associated custom element as a form field.
func TestExtractCustomFieldUsesInternalsDefaults(t *testing.T) {
	d := page.AriaDefaultPrefix
	p := extractInline(t, `<form>
	  <my-check name="tos" `+page.FormValueAttr+`="tos=on" `+d+`aria-checked="true" `+d+`aria-required="true" `+d+`aria-invalid="true"></my-check>
	  <my-check name="news" aria-checked="false" aria-required="false" `+page.FormValueAttr+`="" `+d+`aria-checked="true" `+d+`aria-required="true"></my-check>
	  <my-input name="plain" `+page.FormValueAttr+`=""></my-input>
	</form>`)
	f := p.Meta.Forms[0].Fields
	if len(f) != 3 {
		t.Fatalf("fields = %+v", f)
	}
	if f[0].Checked != "true" || !f[0].Required || !f[0].Invalid {
		t.Errorf("tos = %+v", f[0])
	}
	if f[1].Checked != "false" || f[1].Required {
		t.Errorf("explicit attributes must win: news = %+v", f[1])
	}
	if f[2].Checked != "" || f[2].Required || f[2].Invalid {
		t.Errorf("plain = %+v", f[2])
	}
}

// internals.role can make a custom element a landmark.
func TestExtractRegionFromInternalsRole(t *testing.T) {
	d := page.AriaDefaultPrefix
	p := extractInline(t, `<my-nav `+d+`role="navigation" `+d+`aria-label="Primary"><a href="/a">A</a></my-nav>
	<my-nav role="none" `+d+`role="navigation"><a href="/b">B</a></my-nav>`)
	if len(p.Meta.Regions) != 1 || p.Meta.Regions[0].Role != "navigation" || p.Meta.Regions[0].Label != "Primary" {
		t.Errorf("regions = %+v", p.Meta.Regions)
	}
}
