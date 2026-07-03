package main

// knownAdapters is every MCP tool the harness can measure; "chrome" is the
// special non-MCP raw-Chromium baseline handled in main. Competitor adapters
// register here as they land.
var knownAdapters = []adapter{
	unblinkAdapter{},
	playwrightAdapter{},
	charlotteAdapter{},
	obscuraAdapter{},
	lightpandaAdapter{},
}

// allToolOrder is the canonical column order for -tools=all and table output:
// unblink first, the raw-Chromium baseline beside it, then the competitors.
var allToolOrder = []string{"unblink", "chrome", "playwright", "charlotte", "obscura", "lightpanda"}

func adapterFor(name string) (adapter, bool) {
	for _, a := range knownAdapters {
		if a.name() == name {
			return a, true
		}
	}
	return nil, false
}

func knownTools() []string {
	out := make([]string, 0, len(knownAdapters)+1)
	for _, a := range knownAdapters {
		out = append(out, a.name())
	}
	return append(out, "chrome")
}
