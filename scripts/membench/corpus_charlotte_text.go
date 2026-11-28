//go:build corpus

package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Charlotte answers with one JSON document, not Markdown. Two different
// quantities are taken from it, deliberately:
//
//   - Token cost is counted on the payload exactly as returned — keys, ids,
//     bounds and all — because that is what lands in the model's context.
//   - Fidelity is scored on the text a reader gets out of it: every decoded
//     string value, in document order, except machine identifiers and the
//     diagnostics block. Scoring the raw JSON instead would break the text at
//     every escape ("\n" tokenises as a stray "n") and every key, and understate
//     Charlotte for a formatting reason.
//
// charlotteSkipKeys are the values that are not page text. Everything else —
// full_content, headings, landmark and control labels, the content summary,
// the title — is kept, which is the generous reading.
var charlotteSkipKeys = map[string]bool{
	"id": true, "type": true, "role": true, "url": true, "href": true, "src": true,
	"timestamp": true, "errors": true, "level": true,
}

// charlotteReadable returns the readable text of a Charlotte payload. A payload
// that is not JSON (an error string) is returned unchanged.
func charlotteReadable(payload string) string {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return payload
	}
	var sb strings.Builder
	walkStrings(json.RawMessage(payload), &sb)
	return sb.String()
}

// walkStrings appends string leaves in document order. It walks the raw
// message with a token decoder because Go maps do not keep key order, and
// order is what keeps a block's shingles intact.
func walkStrings(raw json.RawMessage, sb *strings.Builder) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func() bool
	walk = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				for dec.More() {
					kt, err := dec.Token()
					if err != nil {
						return false
					}
					key, _ := kt.(string)
					if charlotteSkipKeys[key] {
						var skip json.RawMessage
						if dec.Decode(&skip) != nil {
							return false
						}
						continue
					}
					if !walk() {
						return false
					}
				}
				_, err := dec.Token() // '}'
				return err == nil
			case '[':
				for dec.More() {
					if !walk() {
						return false
					}
				}
				_, err := dec.Token() // ']'
				return err == nil
			}
		case string:
			if strings.TrimSpace(t) != "" {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		}
		return true
	}
	walk()
}

// charlotteCounts reads the link and form counts out of a payload: the typed
// element list at summary/full detail, the per-landmark tally at minimal.
func charlotteCounts(payload string) (links, forms int) {
	var d struct {
		Interactive []struct {
			Type string `json:"type"`
		} `json:"interactive"`
		Forms              []json.RawMessage `json:"forms"`
		InteractiveSummary struct {
			ByLandmark map[string]map[string]int `json:"by_landmark"`
		} `json:"interactive_summary"`
	}
	if json.Unmarshal([]byte(payload), &d) != nil {
		return 0, 0
	}
	for _, e := range d.Interactive {
		if e.Type == "link" {
			links++
		}
	}
	if d.Interactive == nil {
		for _, m := range d.InteractiveSummary.ByLandmark {
			links += m["link"]
		}
	}
	return links, len(d.Forms)
}

// charlotteWithoutDiagnostics returns the payload with its errors block
// emptied, for the token count. Charlotte reports the page's console and
// network errors in every response, and under replay those are overwhelmingly
// the replay's own doing: fonts and images are not stored in a capture, so
// Chromium logs "Failed to decode downloaded font" once per use, and every
// request the archive cannot answer logs a load failure. On the live web those
// lines would not exist, so charging Charlotte's context for them would
// overstate its cost. Emptying the block errs the other way — a real page's
// genuine console errors are dropped too — which is the side to err on. Keys
// come back sorted, which does not change what a tokenizer counts.
func charlotteWithoutDiagnostics(payload string) string {
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &top) != nil {
		return payload
	}
	if _, ok := top["errors"]; !ok {
		return payload
	}
	top["errors"] = json.RawMessage(`{"console":[],"network":[]}`)
	out, err := json.Marshal(top)
	if err != nil {
		return payload
	}
	return string(out)
}
