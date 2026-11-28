package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func TestFlattenSVGText(t *testing.T) {
	cases := []struct{ name, in, want, absent string }{
		{
			"chart labels are separate",
			`<div><svg><g><text x="1">Ember</text><text x="2">React</text><text x="3">Angular 1</text></g><rect width="5"/><text>4326</text></svg></div>`,
			`<p>Ember · React · Angular 1 · 4326</p>`, "<svg",
		},
		{
			"tspans are one label",
			`<div><svg><text><tspan x="0">Benchmark Times</tspan><tspan x="0" dy="12">on Chrome 52</tspan></text></svg></div>`,
			`<p>Benchmark Times on Chrome 52</p>`, "tspan",
		},
		{
			"an icon keeps its name, inline",
			`<a href="/gh"><svg viewBox="0 0 1 1"><title>GitHub</title><path d="M0 0"/></svg></a>`,
			`<a href="/gh"><span>GitHub</span></a>`, "<path",
		},
		{
			"title and desc repeated in the labels appear once",
			`<div><svg><title>Sales</title><desc>Sales</desc><text>Sales</text><text>2026</text></svg></div>`,
			`<p>Sales · 2026</p>`, "Sales · Sales",
		},
		{
			"an icon beside text adds nothing",
			`<a href="/c"><svg><title>Comments</title><path d="M0 0"/></svg> 43 comments</a><div><svg><title>Decoration</title></svg></div>`,
			`<a href="/c"> 43 comments</a><div></div>`, "Decoration",
		},
		{
			"an icon-only button keeps its name",
			`<button><span><svg><title>Close</title></svg></span></button>`,
			`<button><span><span>Close</span></span></button>`, "<svg",
		},
		{
			"equal labels are both data",
			`<div><svg><text>100</text><text>93</text><text>100</text><title>100</title></svg></div>`,
			`<p>100 · 93 · 100</p>`, "100 · 93 · 100 · 100",
		},
		{
			"a shape's tooltip inside a label group is kept once",
			`<div><svg><g><title>2019: 45%</title><rect/></g><g><title>2019: 45%</title><rect/></g><text>Share</text></svg></div>`,
			`<p>2019: 45% · Share</p>`, "45% · 2019",
		},
		{
			"a drawing with no words is removed",
			`<p>before<svg><style>.a{fill:red}</style><defs><text>hidden def</text></defs><path d="M0 0"/></svg>after</p>`,
			`<p>beforeafter</p>`, "fill",
		},
		{
			"foreignObject text is kept",
			`<div><svg><foreignObject><div>HTML label</div></foreignObject></svg></div>`,
			`HTML label`, "foreignObject",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parse(t, `<html><body>`+c.in+`</body></html>`)
			dom.FlattenSVGText(doc)
			got := renderString(t, doc)
			if !strings.Contains(got, c.want) {
				t.Errorf("got %s\nwant it to contain %s", got, c.want)
			}
			if c.absent != "" && strings.Contains(got, c.absent) {
				t.Errorf("got %s\nmust not contain %q", got, c.absent)
			}
		})
	}
	dom.FlattenSVGText(nil)
}
