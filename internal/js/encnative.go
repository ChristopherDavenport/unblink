package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/text/encoding/htmlindex"
)

// installEncoding binds the WHATWG "get an encoding" label lookup (x/text's
// htmlindex, already in the module graph) as __unblinkEncodingName, used by the
// TextDecoder constructor to validate and canonicalize its label. It returns the
// canonical encoding name (e.g. "utf-8", "windows-1252", "shift_jis") for any valid
// WHATWG label, or "" for an invalid one so the JS layer throws RangeError. unblink
// still DECODES only UTF-8 (legacy codecs are a documented non-goal); this fixes
// only label reflection/validation, so a real page that constructs a legacy-labeled
// decoder gets the right .encoding and doesn't crash on an unknown label.
func (b *bridge) installEncoding() {
	_ = b.vm.Set("__unblinkEncodingName", func(call goja.FunctionCall) goja.Value {
		label := strings.Trim(call.Argument(0).String(), "\t\n\f\r ")
		enc, err := htmlindex.Get(label)
		if err != nil {
			return b.vm.ToValue("")
		}
		name, err := htmlindex.Name(enc)
		if err != nil {
			return b.vm.ToValue("")
		}
		return b.vm.ToValue(name)
	})
}
