package mcpserver

import (
	"sort"
	"strings"
	"testing"
)

func enabledNames(cfg Config) (string, []string) {
	set, unknown := resolveEnabledTools(cfg)
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ","), unknown
}

func TestResolveEnabledTools(t *testing.T) {
	const all = "browse,click,controls,data,find,forms,interact,links,map,read,search,session,site,submit_form"

	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"default full both caps", Config{JSEnabled: true, SearchEnabled: true}, all},
		{"default, no JS drops interact", Config{JSEnabled: false, SearchEnabled: true},
			"browse,click,controls,data,find,forms,links,map,read,search,session,site,submit_form"},
		{"default, no search drops search", Config{JSEnabled: true, SearchEnabled: false},
			"browse,click,controls,data,find,forms,interact,links,map,read,session,site,submit_form"},
		{"default, neither cap", Config{},
			"browse,click,controls,data,find,forms,links,map,read,session,site,submit_form"},
		{"core preset", Config{Tools: "core", JSEnabled: true, SearchEnabled: true}, "browse,find,read"},
		{"read-only preset with search off", Config{Tools: "read-only", JSEnabled: true},
			"browse,controls,data,find,forms,links,map,read,site"}, // search gated out
		{"explicit names", Config{Tools: "read,browse", JSEnabled: true, SearchEnabled: true}, "browse,read"},
		{"preset plus name", Config{Tools: "core,data", JSEnabled: true, SearchEnabled: true}, "browse,data,find,read"},
		{"denylist subtracts", Config{DisableTools: "map,search", JSEnabled: true, SearchEnabled: true},
			"browse,click,controls,data,find,forms,interact,links,read,session,site,submit_form"},
		{"deny a preset", Config{Tools: "full", DisableTools: "core", JSEnabled: true, SearchEnabled: true},
			"click,controls,data,forms,interact,links,map,search,session,site,submit_form"},
		{"capability gate beats explicit request", Config{Tools: "read,interact", JSEnabled: false}, "read"},
		{"whitespace tolerated", Config{Tools: " core , data ", JSEnabled: true}, "browse,data,find,read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, unknown := enabledNames(tc.cfg)
			if got != tc.want {
				t.Errorf("enabled = %q, want %q", got, tc.want)
			}
			if len(unknown) != 0 {
				t.Errorf("unexpected unknown tokens: %v", unknown)
			}
		})
	}
}

func TestResolveEnabledToolsUnknown(t *testing.T) {
	got, unknown := enabledNames(Config{Tools: "core,bogus,also-bad", JSEnabled: true})
	if got != "browse,find,read" {
		t.Errorf("enabled = %q, want the core tools", got)
	}
	if strings.Join(unknown, ",") != "bogus,also-bad" {
		t.Errorf("unknown = %v, want [bogus also-bad]", unknown)
	}
}

func TestExplicitlyNamed(t *testing.T) {
	if !explicitlyNamed("read, interact ,browse", "interact") {
		t.Error("interact should be seen as explicitly named")
	}
	if explicitlyNamed("core", "interact") {
		t.Error("a preset that contains interact is not an explicit name")
	}
}
