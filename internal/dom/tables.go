package dom

import (
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

const (
	maxTables    = 64
	maxTableRows = 2000
	maxTableCols = 128
	maxCellLen   = 500 // runes
)

var selTableTag = cascadia.MustCompile("table")

// Tables extracts each top-level <table> as caption/headers/rows. Nested tables
// are skipped as standalone tables — their text is folded into the containing
// cell (collapsedText). colspan is expanded by repeating the cell value; rowspan
// is ignored. All dimensions are capped (see the max* constants).
func Tables(doc *html.Node) []page.Table {
	var out []page.Table
	for _, t := range selTableTag.MatchAll(doc) {
		if hasAncestor(t, "table") {
			continue // nested; folded into a parent cell's text
		}
		tbl := extractTable(t)
		if len(tbl.Headers) == 0 && len(tbl.Rows) == 0 {
			continue // empty
		}
		out = append(out, tbl)
		if len(out) >= maxTables {
			break
		}
	}
	return out
}

type rawCell struct {
	text    string
	colspan int
	rowspan int
}

type rawRow struct {
	cells  []rawCell
	inHead bool
	allTh  bool
}

// carry is a cell spanning down into later rows (rowspan > 1).
type carry struct {
	text      string
	remaining int
}

func extractTable(t *html.Node) page.Table {
	caption, rows, truncated := collectRows(t)
	tbl := page.Table{Caption: caption, Truncated: truncated}
	if len(rows) == 0 {
		return tbl
	}
	grid := expandGrid(rows)
	if first := rows[0]; first.inHead || first.allTh {
		tbl.Headers = grid[0]
		grid = grid[1:]
	}
	tbl.Rows = append(tbl.Rows, grid...)
	if len(tbl.Rows) == 0 {
		tbl.Rows = nil
	}
	return tbl
}

// expandGrid lays raw rows out on the table grid, expanding colspan by
// repetition and carrying rowspan cells down into the rows they span — the HTML
// table model — so a merged cell appears in every row it covers and later
// columns stay aligned. All dimensions remain capped by the max* constants.
func expandGrid(rows []rawRow) [][]string {
	out := make([][]string, 0, len(rows))
	carries := map[int]*carry{} // column index → cell spanning down from an earlier row
	for _, r := range rows {
		var cells []string
		col := 0
		place := func(text string) {
			cells = append(cells, text)
			col++
		}
		fillCarries := func() {
			for col < maxTableCols {
				c, ok := carries[col]
				if !ok {
					return
				}
				place(c.text)
				c.remaining--
				if c.remaining <= 0 {
					delete(carries, col-1)
				}
			}
		}
		for _, cell := range r.cells {
			fillCarries()
			for i := 0; i < cell.colspan && col < maxTableCols; i++ {
				if cell.rowspan > 1 {
					carries[col] = &carry{text: cell.text, remaining: cell.rowspan - 1}
				}
				place(cell.text)
			}
		}
		// Trailing carries past the row's own cells keep their columns aligned;
		// gaps before them pad with empty cells.
		for col < maxTableCols {
			next := -1
			for cc := range carries {
				if cc >= col && (next == -1 || cc < next) {
					next = cc
				}
			}
			if next == -1 {
				break
			}
			for col < next {
				place("")
			}
			fillCarries()
		}
		out = append(out, cells)
	}
	return out
}

// collectRows walks t (skipping nested tables), capturing the first <caption>
// and each <tr>, tracking whether the row sits in a <thead>. truncated reports
// rows dropped by the maxTableRows cap.
func collectRows(t *html.Node) (caption string, rows []rawRow, truncated bool) {
	var walk func(n *html.Node, inHead bool)
	walk = func(n *html.Node, inHead bool) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.Data {
			case "table":
				// nested table: don't descend (its text folds into its cell)
			case "caption":
				if caption == "" {
					caption = truncate(collapsedText(c), maxCellLen)
				}
			case "thead":
				walk(c, true)
			case "tr":
				if len(rows) < maxTableRows {
					cells, allTh := collectCells(c)
					rows = append(rows, rawRow{cells: cells, inHead: inHead, allTh: allTh})
				} else {
					truncated = true
				}
			default:
				walk(c, inHead)
			}
		}
	}
	walk(t, false)
	return
}

// collectCells returns a row's cells (text + colspan/rowspan) and whether every
// cell was a <th>. It does not descend into a cell (nested-table text is already
// captured by collapsedText) or into nested tables.
func collectCells(tr *html.Node) (cells []rawCell, allTh bool) {
	allTh = true
	count := 0
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.Data {
			case "td", "th":
				count++
				if c.Data != "th" {
					allTh = false
				}
				cells = append(cells, rawCell{
					text:    truncate(collapsedText(c), maxCellLen),
					colspan: colspan(c),
					rowspan: rowspan(c),
				})
			case "table":
				// don't descend into a nested table
			default:
				walk(c)
			}
		}
	}
	walk(tr)
	if count == 0 {
		allTh = false
	}
	return
}

// colspan returns a cell's colspan clamped to [1, maxTableCols].
func colspan(cell *html.Node) int {
	n, err := strconv.Atoi(strings.TrimSpace(attr(cell, "colspan")))
	if err != nil || n < 1 {
		return 1
	}
	if n > maxTableCols {
		return maxTableCols
	}
	return n
}

// rowspan parses the rowspan attribute, clamped to the row cap (rowspan=0 —
// "span to the end of the group" — is treated as 1; supporting it needs group
// bookkeeping that no data table this tool targets relies on).
func rowspan(cell *html.Node) int {
	n, err := strconv.Atoi(strings.TrimSpace(attr(cell, "rowspan")))
	if err != nil || n < 1 {
		return 1
	}
	if n > maxTableRows {
		return maxTableRows
	}
	return n
}

// hasAncestor reports whether n has an element ancestor with the given tag.
func hasAncestor(n *html.Node, tag string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return true
		}
	}
	return false
}
