package dom_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

// A form-associated custom element reaches form extraction through the attributes
// the JS engine reflects onto the tree (page.FormValueAttr / FormInvalidAttr):
// extraction never reads the JS heap.
func TestExtractFormAssociatedCustomElements(t *testing.T) {
	p := extractInline(t, `<form id="f" action="/go">
	  <input name="first" value="1">
	  <my-input name="email" required `+page.FormValueAttr+`="email=a%40b.c+%26+co"></my-input>
	  <my-range name="range" `+page.FormValueAttr+`="from=1&amp;to=2"></my-range>
	  <my-input name="empty" disabled `+page.FormValueAttr+`=""></my-input>
	  <my-input name="bad" `+page.FormValueAttr+`="bad=v" `+page.FormInvalidAttr+`=""></my-input>
	  <sl-icon name="gear"></sl-icon>
	  <input name="native" value="n" `+page.FormValueAttr+`="native=spoofed">
	</form>`)
	if len(p.Meta.Forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(p.Meta.Forms))
	}
	want := []page.Field{
		{Name: "first", Type: "text", Value: "1"},
		{Name: "email", Type: "my-input", Value: "a@b.c & co", Required: true},
		{Name: "from", Type: "my-range", Value: "1"},
		{Name: "to", Type: "my-range", Value: "2"},
		{Name: "empty", Type: "my-input", Disabled: true},
		{Name: "bad", Type: "my-input", Value: "v", Invalid: true},
		// <sl-icon name> carries no marker: a named custom element is not a field.
		{Name: "native", Type: "text", Value: "n"}, // a native control ignores the marker
	}
	got := p.Meta.Forms[0].Fields
	if len(got) != len(want) {
		t.Fatalf("fields = %+v\nwant %d fields", got, len(want))
	}
	ids := map[string]bool{}
	for i, w := range want {
		g := got[i]
		if g.ID == "" || ids[g.ID] {
			t.Errorf("field %d (%s) has empty or duplicate id %q", i, g.Name, g.ID)
		}
		ids[g.ID] = true
		if g.Name != w.Name || g.Type != w.Type || g.Value != w.Value ||
			g.Required != w.Required || g.Disabled != w.Disabled || g.Invalid != w.Invalid {
			t.Errorf("field %d = %+v, want %+v", i, g, w)
		}
	}
}
