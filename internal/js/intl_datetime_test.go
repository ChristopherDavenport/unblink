package js_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// evalLines runs each expression in a page and returns "expr => value" lines.
func evalLines(t *testing.T, exprs []string) map[string]string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<html><body><pre id="out"></pre><script>
	  var d = new Date(Date.UTC(2026, 10, 28, 13, 37, 9, 45)); // Sat 2026-11-28T13:37:09.045Z
	  var morning = new Date(Date.UTC(2026, 0, 5, 0, 5, 3));   // Mon 2026-01-05T00:05:03Z
	  var out = [];
	  function run(i, f) { var v; try { v = f(); } catch (e) { v = 'throws ' + e.name; } out.push(i + '\t' + (typeof v === 'string' ? v : JSON.stringify(v))); }
	`)
	for i, e := range exprs {
		sb.WriteString("run(" + strconv.Itoa(i) + ", function () { return " + e + "; });\n")
	}
	sb.WriteString(`document.getElementById('out').textContent = out.join('\n');</script></body></html>`)
	html := renderWithEnv(t, sb.String(), js.Env{})
	i := strings.Index(html, `<pre id="out">`)
	j := strings.Index(html, "</pre>")
	if i < 0 || j < i {
		t.Fatalf("no output in %s", html)
	}
	got := map[string]string{}
	for _, line := range strings.Split(unescapeHTML(html[i+len(`<pre id="out">`):j]), "\n") {
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(k); err == nil && n < len(exprs) {
			got[exprs[n]] = v
		}
	}
	return got
}

func unescapeHTML(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&amp;", "&").Replace(s)
}

// English date and time formatting. Expected strings are the CLDR English
// patterns (common/main/en.xml: dateFormats, timeFormats, dateTimeFormats and
// availableFormats), which is what a browser prints for en-US; every case
// names its zone, so the table holds whatever zone the process runs in.
func TestIntlDateTimeFormatEnglish(t *testing.T) {
	const U = `timeZone: 'UTC'`
	cases := map[string]string{
		// the three Date methods and their defaults
		`d.toLocaleString('en-US', {` + U + `})`:                                           "11/28/2026, 1:37:09 PM",
		`d.toLocaleDateString('en-US', {` + U + `})`:                                       "11/28/2026",
		`d.toLocaleTimeString('en-US', {` + U + `})`:                                       "1:37:09 PM",
		`d.toLocaleTimeString(undefined, {hour: 'numeric', minute: '2-digit', ` + U + `})`: "1:37 PM",
		`d.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', ` + U + `})`:        "01:37 PM",
		`d.toLocaleDateString('en-US', {month: 'short', day: 'numeric', ` + U + `})`:       "Nov 28",
		`d.toLocaleDateString('en-US', {hour: 'numeric', ` + U + `})`:                      "11/28/2026, 1 PM",
		`d.toLocaleTimeString('en-US', {month: 'long', ` + U + `})`:                        "November at 1:37:09 PM",
		`new Date(NaN).toLocaleString()`:                                                   "Invalid Date",
		// component skeletons
		`new Intl.DateTimeFormat('en-US', {` + U + `}).format(d)`:                                                                                     "11/28/2026",
		`new Intl.DateTimeFormat('en-US', {year: 'numeric', month: 'long', day: 'numeric', ` + U + `}).format(d)`:                                     "November 28, 2026",
		`new Intl.DateTimeFormat('en-US', {year: 'numeric', month: 'short', day: 'numeric', ` + U + `}).format(d)`:                                    "Nov 28, 2026",
		`new Intl.DateTimeFormat('en-US', {weekday: 'long', year: 'numeric', month: 'long', day: 'numeric', ` + U + `}).format(d)`:                    "Saturday, November 28, 2026",
		`new Intl.DateTimeFormat('en-US', {weekday: 'short', month: 'short', day: 'numeric', ` + U + `}).format(d)`:                                   "Sat, Nov 28",
		`new Intl.DateTimeFormat('en-US', {year: '2-digit', month: '2-digit', day: '2-digit', ` + U + `}).format(d)`:                                  "11/28/26",
		`new Intl.DateTimeFormat('en-US', {month: 'long', year: 'numeric', ` + U + `}).format(d)`:                                                     "November 2026",
		`new Intl.DateTimeFormat('en-US', {weekday: 'long', ` + U + `}).format(d)`:                                                                    "Saturday",
		`new Intl.DateTimeFormat('en-US', {year: 'numeric', ` + U + `}).format(d)`:                                                                    "2026",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', ` + U + `}).format(d)`:                                                 "1:37 PM",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', ` + U + `}).format(d)`:                                                                    "1 PM",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', hour12: false, ` + U + `}).format(d)`:                                  "13:37",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', hourCycle: 'h23', ` + U + `}).format(morning)`:                         "00:05",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', ` + U + `}).format(morning)`:                                           "12:05 AM",
		`new Intl.DateTimeFormat('en-US', {minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, ` + U + `}).format(d)`:                    "37:09.045",
		`new Intl.DateTimeFormat('en-US', {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', ` + U + `}).format(d)`:                 "Nov 28, 1:37 PM",
		`new Intl.DateTimeFormat('en-US', {month: 'long', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit', ` + U + `}).format(d)`: "November 28, 2026 at 1:37 PM",
		// styles
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'full', ` + U + `}).format(d)`:                       "Saturday, November 28, 2026",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'long', ` + U + `}).format(d)`:                       "November 28, 2026",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'medium', ` + U + `}).format(d)`:                     "Nov 28, 2026",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'short', ` + U + `}).format(d)`:                      "11/28/26",
		`new Intl.DateTimeFormat('en-US', {timeStyle: 'short', ` + U + `}).format(d)`:                      "1:37 PM",
		`new Intl.DateTimeFormat('en-US', {timeStyle: 'medium', ` + U + `}).format(d)`:                     "1:37:09 PM",
		`new Intl.DateTimeFormat('en-US', {timeStyle: 'long', ` + U + `}).format(d)`:                       "1:37:09 PM UTC",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'medium', timeStyle: 'short', ` + U + `}).format(d)`: "Nov 28, 2026, 1:37 PM",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'long', timeStyle: 'short', ` + U + `}).format(d)`:   "November 28, 2026 at 1:37 PM",
		// zones: the option is honoured, across a date line and a DST rule
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', timeZone: 'America/New_York'}).format(d)`:                                         "8:37 AM",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'short', timeStyle: 'short', timeZone: 'Pacific/Auckland'}).format(d)`:                                     "11/29/26, 2:37 AM",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', timeZoneName: 'short', timeZone: 'America/Los_Angeles'}).format(d)`:                                  "5 AM PST",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', timeZoneName: 'short', timeZone: 'America/Los_Angeles'}).format(new Date(Date.UTC(2026, 6, 1, 12)))`: "5 AM PDT",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', timeZoneName: 'shortOffset', timeZone: 'Asia/Kolkata'}).format(d)`:                "7:07 PM GMT+5:30",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', timeZoneName: 'longOffset', timeZone: 'America/New_York'}).format(d)`:                                "8 AM GMT-05:00",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', timeZone: 'america/new_york'}).resolvedOptions().timeZone`:                                           "America/New_York",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', timeZone: '+05:30'}).format(d)`:                                                   "7:07 PM",
		`new Intl.DateTimeFormat('en-US', {timeZone: 'Mars/Olympus_Mons'})`:                                                                                      "throws RangeError",
		// parts, options, errors, other locales
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', minute: '2-digit', ` + U + `}).formatToParts(d).map(function (p) { return p.type + ':' + p.value; }).join('|')`: "hour:1|literal::|minute:37|literal: |dayPeriod:PM",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', ` + U + `}).resolvedOptions().hourCycle`:                                                                        "h12",
		`new Intl.DateTimeFormat('en-US', {hour: 'numeric', hour12: false, ` + U + `}).resolvedOptions().hour12`:                                                            "false",
		`new Intl.DateTimeFormat('en-US', {dateStyle: 'short', year: 'numeric'})`:                                                                                           "throws TypeError",
		`new Intl.DateTimeFormat('en-US', {month: 'huge'})`:                                                                                                                 "throws RangeError",
		`new Intl.DateTimeFormat('en-US').format(NaN)`:                                                                                                                      "throws RangeError",
		`new Intl.DateTimeFormat('de-DE', {dateStyle: 'medium', ` + U + `}).format(d)`:                                                                                      "Nov 28, 2026",
		`Intl.DateTimeFormat('en-US', {` + U + `}).format(d)`:                                                                                                               "11/28/2026",
		`[d].map(new Intl.DateTimeFormat('en-US', {` + U + `}).format)[0]`:                                                                                                  "11/28/2026",
	}
	exprs := make([]string, 0, len(cases))
	for e := range cases {
		exprs = append(exprs, e)
	}
	got := evalLines(t, exprs)
	for e, want := range cases {
		if got[e] != want {
			t.Errorf("%s\n  got  %q\n  want %q", e, got[e], want)
		}
	}
}

// With no timeZone option the formatter uses the zone Date itself uses, and
// reports it. This holds in any process zone (the suite is run under TZ=UTC
// and TZ=Pacific/Auckland).
func TestIntlDefaultTimeZoneMatchesDate(t *testing.T) {
	exprs := []string{
		`(function () { var z = new Intl.DateTimeFormat().resolvedOptions().timeZone; return typeof z === 'string' && z.length > 0 && z !== 'Local'; })()`,
		`(function () { var z = new Intl.DateTimeFormat().resolvedOptions().timeZone; return d.toLocaleString('en-US') === d.toLocaleString('en-US', {timeZone: z}); })()`,
		`(function () { var p = new Intl.DateTimeFormat('en-US', {hour: 'numeric', hourCycle: 'h23'}).formatToParts(d); return Number(p[0].value) === d.getHours(); })()`,
		`(function () { return d.toLocaleDateString('en-US') === (d.getMonth() + 1) + '/' + d.getDate() + '/' + d.getFullYear(); })()`,
		`navigator.language + '|' + navigator.languages.join(',') + '|' + new Intl.DateTimeFormat().resolvedOptions().locale`,
	}
	got := evalLines(t, exprs)
	for _, e := range exprs[:4] {
		if got[e] != "true" {
			t.Errorf("%s = %q, want true", e, got[e])
		}
	}
	if got[exprs[4]] != "en-US|en-US,en|en-US" {
		t.Errorf("locale = %q", got[exprs[4]])
	}
}

// Relative time, plurals, unit style and collation in English (CLDR en.xml
// dateFields and units; plurals.xml/ordinals.xml).
func TestIntlRelativeTimePluralUnitCollator(t *testing.T) {
	cases := map[string]string{
		`new Intl.RelativeTimeFormat('en').format(-1, 'day')`:                                                                                           "1 day ago",
		`new Intl.RelativeTimeFormat('en').format(3, 'hours')`:                                                                                          "in 3 hours",
		`new Intl.RelativeTimeFormat('en').format(0, 'day')`:                                                                                            "in 0 days",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(-1, 'day')`:                                                                        "yesterday",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(1, 'day')`:                                                                         "tomorrow",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(0, 'second')`:                                                                      "now",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(-1, 'month')`:                                                                      "last month",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(1, 'year')`:                                                                        "next year",
		`new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(-2, 'day')`:                                                                        "2 days ago",
		`new Intl.RelativeTimeFormat('en', {style: 'short'}).format(-3, 'month')`:                                                                       "3 mo. ago",
		`new Intl.RelativeTimeFormat('en', {style: 'short'}).format(2, 'quarter')`:                                                                      "in 2 qtrs.",
		`new Intl.RelativeTimeFormat('en').format(-1500, 'day')`:                                                                                        "1,500 days ago",
		`new Intl.RelativeTimeFormat('en').format(1, 'fortnight')`:                                                                                      "throws RangeError",
		`new Intl.RelativeTimeFormat('en').formatToParts(-2, 'day').map(function (p) { return p.type; }).join(',')`:                                     "integer,literal",
		`new Intl.PluralRules('en').select(1) + '/' + new Intl.PluralRules('en').select(2) + '/' + new Intl.PluralRules('en').select(0)`:                "one/other/other",
		`[1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 101].map(function (n) { return new Intl.PluralRules('en', {type: 'ordinal'}).select(n); }).join(',')`:     "one,two,few,other,other,other,other,one,two,few,one",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'kilometer'}).format(88)`:                                                                 "88 km",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'kilometer', unitDisplay: 'long'}).format(1)`:                                             "1 kilometer",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'kilometer', unitDisplay: 'long'}).format(1234)`:                                          "1,234 kilometers",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'megabyte', unitDisplay: 'narrow'}).format(12)`:                                           "12MB",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'kilometer-per-hour'}).format(60)`:                                                        "60 km/h",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'mile-per-hour', unitDisplay: 'long'}).format(60)`:                                        "60 miles per hour",
		`new Intl.NumberFormat('en-US', {style: 'unit', unit: 'day'}).format(3)`:                                                                        "3 days",
		`['b', 'C', 'ä', 'a'].sort(new Intl.Collator('en').compare).join('')`:                                                                           "aäbC",
		`['Z', 'a', 'A', 'z'].sort(new Intl.Collator('en').compare).join('')`:                                                                           "aAzZ",
		`['item 10', 'item 9', 'item 2'].sort(new Intl.Collator('en', {numeric: true}).compare).join('|')`:                                              "item 2|item 9|item 10",
		`new Intl.Collator('en', {sensitivity: 'base'}).compare('a', 'Á')`:                                                                              "0",
		`new Intl.Collator('en', {sensitivity: 'accent'}).compare('a', 'A') + '/' + new Intl.Collator('en', {sensitivity: 'accent'}).compare('a', 'á')`: "0/-1",
	}
	exprs := make([]string, 0, len(cases))
	for e := range cases {
		exprs = append(exprs, e)
	}
	got := evalLines(t, exprs)
	for e, want := range cases {
		if got[e] != want {
			t.Errorf("%s\n  got  %q\n  want %q", e, got[e], want)
		}
	}
}
