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
