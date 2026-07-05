package mcpserver

import (
	"runtime/debug"
	"testing"
)

// White-box because resolveVersion is unexported: it is the version-precedence
// logic that keeps the reported version correct by construction, so it is worth
// pinning directly rather than through the build-info that Version() reads.
func TestResolveVersion(t *testing.T) {
	buildInfo := func(mainVersion string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: mainVersion}}
	}

	tests := []struct {
		name     string
		override string
		info     *debug.BuildInfo
		ok       bool
		want     string
	}{
		{"ldflags override wins", "0.23.0", buildInfo("v0.99.0"), true, "0.23.0"},
		{"override wins even without build info", "0.23.0", nil, false, "0.23.0"},
		{"module tag drives go install builds", "", buildInfo("v0.23.0"), true, "0.23.0"},
		{"module pseudo-version passes through", "", buildInfo("v0.23.1-0.20260705-70729caa"), true, "0.23.1-0.20260705-70729caa"},
		{"bare local build reports devel", "", buildInfo("(devel)"), true, "(devel)"},
		{"empty module version reports devel", "", buildInfo(""), true, "(devel)"},
		{"no build info reports devel", "", nil, false, "(devel)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.override, tt.info, tt.ok); got != tt.want {
				t.Errorf("resolveVersion(%q, %+v, %v) = %q, want %q", tt.override, tt.info, tt.ok, got, tt.want)
			}
		})
	}
}
