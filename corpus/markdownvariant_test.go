//go:build corpus

package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// markdownVariant mirrors scripts/corpus's markdown.json: what the origin
// answered when asked for text/markdown (ADR 0021). It is never scored as the
// page; TestMarkdownVariants measures it against the same Chromium oracle so
// "does the origin's Markdown say what its HTML shows" has a number.
type markdownVariant struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Served      bool   `json:"served_markdown"`
	Body        string `json:"body"`
	Size        int    `json:"size"`
	Error       string `json:"error"`
}

type markdownScore struct {
	ID          string  `json:"id"`
	Asked       bool    `json:"asked"`
	Status      int     `json:"status,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
	Served      bool    `json:"served_markdown"`
	Bytes       int     `json:"bytes,omitempty"`
	Words       int     `json:"words,omitempty"`
	Recall      float64 `json:"recall,omitempty"`
	TokenRecall float64 `json:"token_recall,omitempty"`
	Precision   float64 `json:"precision,omitempty"`
	OracleUsed  string  `json:"oracle_source,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// TestMarkdownVariants writes, to CORPUS_MARKDOWN_OUT, how each captured
// Markdown variant scores against the page's Chromium oracle. It reads the
// capture only (no network, no engine) and is skipped unless that variable is
// set: it is a measurement, not a check.
func TestMarkdownVariants(t *testing.T) {
	out := os.Getenv("CORPUS_MARKDOWN_OUT")
	if out == "" {
		t.Skip("set CORPUS_MARKDOWN_OUT=<file> to score the captured text/markdown variants")
	}
	pages, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("CORPUS_DIR")
	if root == "" {
		root = filepath.Join(repoRoot(), defaultDir)
	}
	var scores []markdownScore
	for _, p := range pages {
		dir := filepath.Join(root, p.ID)
		raw, err := readBounded(filepath.Join(dir, "markdown.json"), 1<<20)
		if err != nil {
			continue // captured before the variant was recorded, or not captured
		}
		var v markdownVariant
		s := markdownScore{ID: p.ID, Asked: true}
		if err := json.Unmarshal(raw, &v); err != nil {
			s.Error = err.Error()
			scores = append(scores, s)
			continue
		}
		s.Status, s.ContentType, s.Served, s.Error = v.Status, v.ContentType, v.Served, v.Error
		if v.Served && v.Body != "" {
			o, oerr := loadOracle(dir)
			body, berr := (&entry{Body: v.Body}).body(dir)
			switch {
			case oerr != nil:
				s.Error = oerr.Error()
			case berr != nil:
				s.Error = berr.Error()
			default:
				want, source := tokenize(o.MainText), o.MainSelector
				if len(want) < minOracleTokens {
					want, source = tokenize(o.BodyText), "body"
				}
				got := tokenize(stripMarkdown(string(body)))
				s.Bytes, s.Words, s.OracleUsed = len(body), len(got), source
				s.Recall, s.TokenRecall = shingleRecall(want, got), tokenRecall(want, got)
				s.Precision = tokenPrecision(got, tokenize(o.BodyText))
			}
		}
		scores = append(scores, s)
	}
	b, err := json.MarshalIndent(scores, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("scored %d captured Markdown variants → %s", len(scores), out)
}
