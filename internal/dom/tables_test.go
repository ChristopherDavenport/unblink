package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func TestTables(t *testing.T) {
	doc := parseDoc(t, `<!doctype html><html><body>
	<table>
	  <caption>Scores</caption>
	  <thead><tr><th>Name</th><th>Q1</th><th>Q2</th></tr></thead>
	  <tbody>
	    <tr><td>Ada</td><td colspan="2">n/a</td></tr>
	    <tr><td>Bob</td><td>3</td><td>4</td></tr>
	  </tbody>
	</table></body></html>`)

	tables := dom.Tables(doc)
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(tables))
	}
	tbl := tables[0]
	if tbl.Caption != "Scores" {
		t.Errorf("caption = %q", tbl.Caption)
	}
	if got := strings.Join(tbl.Headers, "|"); got != "Name|Q1|Q2" {
		t.Errorf("headers = %q", got)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(tbl.Rows))
	}
	// colspan="2" expands "n/a" across two columns
	if got := strings.Join(tbl.Rows[0], "|"); got != "Ada|n/a|n/a" {
		t.Errorf("row0 = %q", got)
	}
	if got := strings.Join(tbl.Rows[1], "|"); got != "Bob|3|4" {
		t.Errorf("row1 = %q", got)
	}
}

func TestTablesHeaderlessAllTh(t *testing.T) {
	// No <thead>, but the first row is all <th> — treated as headers.
	doc := parseDoc(t, `<!doctype html><html><body>
	<table>
	  <tr><th>A</th><th>B</th></tr>
	  <tr><td>1</td><td>2</td></tr>
	</table></body></html>`)
	tbl := dom.Tables(doc)[0]
	if strings.Join(tbl.Headers, "|") != "A|B" {
		t.Errorf("headers = %v", tbl.Headers)
	}
	if len(tbl.Rows) != 1 || strings.Join(tbl.Rows[0], "|") != "1|2" {
		t.Errorf("rows = %v", tbl.Rows)
	}
}

func TestTablesNestedFolded(t *testing.T) {
	doc := parseDoc(t, `<!doctype html><html><body>
	<table><tr><td>outer <table><tr><td>inner</td></tr></table></td></tr></table>
	</body></html>`)
	tables := dom.Tables(doc)
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want 1 (nested folded, not standalone)", len(tables))
	}
	if len(tables[0].Rows) != 1 || !strings.Contains(tables[0].Rows[0][0], "inner") {
		t.Errorf("nested text should fold into the outer cell: %#v", tables[0].Rows)
	}
}

func TestTablesRowspanExpanded(t *testing.T) {
	// The "West" cell spans two rows; both rows must show it so columns align
	// (previously rowspan was ignored and row 2 shifted left).
	doc := parseDoc(t, `<!doctype html><html><body>
	<table>
	  <tr><th>Region</th><th>City</th><th>Pop</th></tr>
	  <tr><td rowspan="2">West</td><td>SF</td><td>800k</td></tr>
	  <tr><td>LA</td><td>4m</td></tr>
	  <tr><td>East</td><td>NYC</td><td>8m</td></tr>
	</table></body></html>`)
	tbl := dom.Tables(doc)[0]
	want := [][]string{
		{"West", "SF", "800k"},
		{"West", "LA", "4m"},
		{"East", "NYC", "8m"},
	}
	if len(tbl.Rows) != len(want) {
		t.Fatalf("rows = %v", tbl.Rows)
	}
	for i := range want {
		if strings.Join(tbl.Rows[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %v, want %v", i, tbl.Rows[i], want[i])
		}
	}
}

func TestTablesRowspanColspanCombo(t *testing.T) {
	// A 2x2 merged block: both spanned rows repeat it across both columns.
	doc := parseDoc(t, `<!doctype html><html><body>
	<table>
	  <tr><td rowspan="2" colspan="2">A</td><td>B</td></tr>
	  <tr><td>C</td></tr>
	</table></body></html>`)
	tbl := dom.Tables(doc)[0]
	if strings.Join(tbl.Rows[0], "|") != "A|A|B" || strings.Join(tbl.Rows[1], "|") != "A|A|C" {
		t.Errorf("rows = %v, want [A A B] [A A C]", tbl.Rows)
	}
}

func TestTablesTrailingRowspanGap(t *testing.T) {
	// A rowspan in the last column with the next row having fewer cells: the gap
	// pads empty and the carried cell stays in its own column.
	doc := parseDoc(t, `<!doctype html><html><body>
	<table>
	  <tr><td>a</td><td>b</td><td rowspan="2">note</td></tr>
	  <tr><td>c</td></tr>
	</table></body></html>`)
	tbl := dom.Tables(doc)[0]
	if strings.Join(tbl.Rows[1], "|") != "c||note" {
		t.Errorf("row 1 = %v, want [c  note]", tbl.Rows[1])
	}
}
