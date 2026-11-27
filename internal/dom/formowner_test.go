package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

// A form-associated custom element stamped with page.FormOwnerAttr belongs to the
// form carrying that key, whatever its ancestry or form attribute suggest: the
// engine resolved the owner before shadow trees were flattened into this tree.
func TestExtractHonoursStampedFormOwner(t *testing.T) {
	val := func(name string) string { return ` name="` + name + `" ` + page.FormValueAttr + `="` + name + `=v" ` }
	p := extractInline(t, `
	<form id="f" `+page.FormKeyAttr+`="1">
	  <x-in`+val("mine")+page.FormOwnerAttr+`="1"></x-in>
	  <x-in`+val("inshadow")+page.FormOwnerAttr+`=""></x-in>
	  <x-in`+val("shadowref")+`form="f" `+page.FormOwnerAttr+`=""></x-in>
	  <x-in`+val("theirs")+page.FormOwnerAttr+`="2"></x-in>
	  <x-in`+val("dangling")+page.FormOwnerAttr+`="9"></x-in>
	  <x-in`+val("unstamped")+`></x-in>
	  <input name="native" value="n" `+page.FormOwnerAttr+`="2">
	</form>
	<form id="g" `+page.FormKeyAttr+`="2"></form>
	<x-in`+val("outside")+`form="g" `+page.FormOwnerAttr+`="1"></x-in>`)
	got := map[string]string{}
	for _, f := range p.Meta.Forms {
		var names []string
		for _, fld := range f.Fields {
			names = append(names, fld.Name)
		}
		got[f.ID] = strings.Join(names, ",")
	}
	// The stamp is the engine's and only describes custom elements: a native control
	// and an unstamped custom element keep the ancestry / form="id" rule.
	if got["f"] != "mine,unstamped,native,outside" || got["g"] != "theirs" {
		t.Errorf("fields by form = %v", got)
	}
}
