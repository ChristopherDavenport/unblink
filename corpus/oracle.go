//go:build corpus

package corpus

import (
	"fmt"
	"strings"
)

// checkOracle decides whether a capture's oracle can be scored against at all,
// and lists the reasons to read its score with care.
//
// unusable is a reason the oracle is not what a user saw: scoring against it
// would produce a number that means nothing, so the page goes to bucket (d)
// unscored. warnings are weaker: the oracle is real but may be partial or
// measured on the wrong element; the page is still scored and the warning is
// carried next to the score.
func checkOracle(o *oracle, a *archive) (unusable string, warnings []string) {
	body, main := strings.TrimSpace(o.BodyText), strings.TrimSpace(o.MainText)
	switch {
	case o.DocStatus >= 400:
		unusable = fmt.Sprintf("capture got HTTP %d", o.DocStatus)
	case challengeRE.MatchString(o.Title) || challengeRE.MatchString(head(o.BodyText, 2000)):
		unusable = "bot challenge or block page at capture"
	case o.BodyRendered != nil && !*o.BodyRendered:
		unusable = "document.body had no layout box at the snapshot: innerText is textContent, hidden text included"
	case body == "" && main != "":
		// innerText of a rendered body contains the innerText of its rendered
		// main. An empty body beside a non-empty main means the body was not being
		// rendered, so the main text is textContent — not what a user saw.
		unusable = "document.body rendered no text at the snapshot while its main landmark has some: the oracle is textContent, hidden text included"
	case o.LinksVisible == 0 && o.LinksTotal >= 20:
		unusable = fmt.Sprintf("none of the page's %d links was visible at the snapshot: the page was not laid out", o.LinksTotal)
	}
	if unusable != "" {
		return unusable, nil
	}

	if !o.Settled {
		warnings = append(warnings, "capture hit the hard cap before the network went quiet: the oracle may be mid-load")
	}
	bt, mt := len(tokenize(o.BodyText)), len(tokenize(o.MainText))
	switch {
	case mt >= minOracleTokens && bt > 0 && mt*100 < bt*15:
		warnings = append(warnings, fmt.Sprintf("the %q landmark holds %d%% of the visible text (%d of %d tokens): it may be the wrong element",
			o.MainSelector, mt*100/bt, mt, bt))
	case mt < minOracleTokens && o.MainSelector != "":
		warnings = append(warnings, fmt.Sprintf("the %q landmark is too thin (%d tokens): scored against the whole visible page, navigation included",
			o.MainSelector, mt))
	}
	if s, f := siteOf(o.URL), siteOf(o.FinalURL); o.FinalURL != "" && s != f {
		warnings = append(warnings, fmt.Sprintf("navigation ended on another site (%s): the oracle may not be the intended page", f))
	}
	if a != nil {
		n := 0
		for _, e := range a.Entries {
			if e != nil && e.Failed == inFlight && (e.Type == "Script" || e.Type == "Fetch" || e.Type == "XHR") {
				n++
			}
		}
		if n > 0 {
			warnings = append(warnings, fmt.Sprintf("%d script/data request(s) were still in flight at the snapshot", n))
		}
	}
	return "", warnings
}

// inFlight is the Failed value scripts/corpus writes for a request that had not
// completed when the oracle was taken.
const inFlight = "in flight at snapshot"
