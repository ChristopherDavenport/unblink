package dom

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

const (
	maxRecords       = 200  // hard ceiling on emitted records
	maxRecordFields  = 50   // hard ceiling on schema size
	maxFieldValueLen = 1000 // runes, mirrors maxPropValueLen
)

// FieldSpec selects one field's value within a record scope: the first node
// matching Selector, then its named Attr (an attribute value) or, when Attr is
// empty, that node's collapsed text.
type FieldSpec struct {
	Selector string
	Attr     string // "" → collapsed text; else the named attribute
}

// Record is one extracted row: field name → value. A field whose selector
// matched no node (or whose requested attribute is absent) is omitted.
type Record map[string]string

// compiledField pairs a field name with its compiled selector, so selectors
// compile once and iteration stays in a deterministic (sorted) order.
type compiledField struct {
	name string
	sel  cascadia.Selector
	attr string
}

// Records extracts structured records from doc per a caller-supplied schema.
// With root == "" the whole document is one record scope; otherwise each element
// matching root (document order, cascadia MatchAll semantics — nested matches
// included) is one scope, capped at limit. Within a scope each field takes the
// FIRST node matching its selector (scope-inclusive MatchFirst) and reads that
// node's collapsed text (Attr == "") or the named attribute; a field that
// matches nothing, or whose attribute is absent, is omitted. truncated reports
// that more scopes matched than limit. Field selectors and root compile once
// with cascadia.Compile (in sorted field-name order, so a bad selector yields a
// deterministic error); an invalid selector is returned as an error, never a
// panic. Read-only over doc — attribute values are returned verbatim (trimmed,
// not URL-resolved).
func Records(doc *html.Node, root string, fields map[string]FieldSpec, limit int) (recs []Record, truncated bool, err error) {
	if limit <= 0 || limit > maxRecords {
		limit = maxRecords
	}
	if len(fields) > maxRecordFields {
		return nil, false, fmt.Errorf("dom: extract: too many fields: %d (max %d)", len(fields), maxRecordFields)
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	cfs := make([]compiledField, 0, len(names))
	for _, name := range names {
		spec := fields[name]
		sel, cerr := cascadia.Compile(spec.Selector)
		if cerr != nil {
			return nil, false, fmt.Errorf("dom: extract: invalid selector for field %q: %w", name, cerr)
		}
		cfs = append(cfs, compiledField{name: name, sel: sel, attr: spec.Attr})
	}

	var scopes []*html.Node
	if root == "" {
		scopes = []*html.Node{doc}
	} else {
		rootSel, cerr := cascadia.Compile(root)
		if cerr != nil {
			return nil, false, fmt.Errorf("dom: extract: invalid root selector %q: %w", root, cerr)
		}
		matches := rootSel.MatchAll(doc)
		if len(matches) > limit {
			truncated = true
			matches = matches[:limit]
		}
		scopes = matches
	}

	recs = make([]Record, 0, len(scopes))
	for _, scope := range scopes {
		rec := Record{}
		for _, cf := range cfs {
			node := cf.sel.MatchFirst(scope)
			if node == nil {
				continue // field omitted
			}
			if cf.attr == "" {
				rec[cf.name] = truncate(collapsedText(node), maxFieldValueLen)
				continue
			}
			if hasAttr(node, cf.attr) {
				rec[cf.name] = truncate(strings.TrimSpace(attr(node, cf.attr)), maxFieldValueLen)
			}
		}
		recs = append(recs, rec)
	}
	return recs, truncated, nil
}
