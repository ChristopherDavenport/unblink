//go:build wpt

// Package wpt is unblink's offline web-platform-tests conformance harness. It runs
// the in-scope subset of WPT testharness.js tests directly against the JS engine
// (internal/js) — parse the test HTML, Render it, recover the results the test
// wrote into the DOM — and reports an honest, bucketed conformance scorecard.
//
// It is opt-in (build tag "wpt", run via `make wpt`) so it never touches `make
// test`, and it reads a pinned, vendored corpus under testdata/wpt/ so it stays
// offline and deterministic. It is an on-demand analysis tool, not a CI gate — see
// docs/decisions/0016-wpt-conformance.md and docs/wpt-compatibility.md.
package wpt

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed scope.json
var scopeJSON []byte

//go:embed testharnessreport.js
var reporterJS []byte

// bucketKind is a directory's scope classification.
type bucketKind string

const (
	inScope    bucketKind = "in-scope"     // run; failures count toward conformance
	partial    bucketKind = "partial"      // run; most failures are runner limits (excluded)
	stubOnly   bucketKind = "stub-only"    // run; genuine failures are out-of-scope-by-design
	outOfScope bucketKind = "out-of-scope" // never walked
)

// scope is the parsed scope.json: directory buckets + global skip rules.
type scope struct {
	Version string `json:"version"`
	Rules   []struct {
		Path   string     `json:"path"`
		Bucket bucketKind `json:"bucket"`
		Note   string     `json:"note"`
	} `json:"rules"`
	GlobalSkip struct {
		Suffixes     []string `json:"suffixes"`
		PathSegments []string `json:"path_segments"`
		Resources    []string `json:"resources"`
	} `json:"global_skip"`
	ByDesignPaths       []string `json:"by_design_paths"`
	HarnessLimitedPaths []string `json:"harness_limited_paths"`
}

// byDesignPath reports whether a wpt-root-relative path is under a subtree declared
// out-of-scope-by-design (e.g. legacy non-UTF-8 encodings). Such tests are counted
// as by-design rather than run — see scope.json's by_design_note.
func (s *scope) byDesignPath(relPath string) bool { return underAny(relPath, s.ByDesignPaths) }

// harnessLimitedPath reports whether a path is under a subtree the runner cannot
// measure (server-side header observation). Counted as (d), not run — see
// scope.json's harness_limited_note.
func (s *scope) harnessLimitedPath(relPath string) bool {
	return underAny(relPath, s.HarnessLimitedPaths)
}

func underAny(relPath string, prefixes []string) bool {
	for _, p := range prefixes {
		if relPath == p || strings.HasPrefix(relPath, p+"/") {
			return true
		}
	}
	return false
}

func loadScope() (*scope, error) {
	var s scope
	if err := json.Unmarshal(scopeJSON, &s); err != nil {
		return nil, fmt.Errorf("parse scope.json: %w", err)
	}
	return &s, nil
}

// dirFor returns the scope rule matching a wpt-root-relative path by longest
// segment-wise prefix, plus whether a rule matched. A path under no rule (or under
// an out-of-scope rule) is not runnable.
func (s *scope) dirFor(relPath string) (dir string, bucket bucketKind, ok bool) {
	segs := strings.Split(relPath, "/")
	best := -1
	for _, r := range s.Rules {
		rsegs := strings.Split(r.Path, "/")
		if len(rsegs) > len(segs) {
			continue
		}
		match := true
		for i := range rsegs {
			if rsegs[i] != segs[i] {
				match = false
				break
			}
		}
		if match && len(rsegs) > best {
			best = len(rsegs)
			dir = r.Path
			bucket = r.Bucket
			ok = true
		}
	}
	return dir, bucket, ok
}

// runnable reports whether a directory bucket is one the harness executes.
func runnable(b bucketKind) bool { return b == inScope || b == partial || b == stubOnly }

// skipByPath reports whether a wpt-root-relative path is globally skipped: a
// non-test suffix (ref/manual/handler/sidecar), or a helper file under a
// resources/ or support/ segment (those hold fixtures, not tests).
func (s *scope) skipByPath(relPath string) bool {
	for _, suf := range s.GlobalSkip.Suffixes {
		if strings.HasSuffix(relPath, suf) {
			return true
		}
	}
	segs := strings.Split(relPath, "/")
	// Only intermediate segments (not the filename) mark a helper directory.
	for _, seg := range segs[:len(segs)-1] {
		for _, skip := range s.GlobalSkip.PathSegments {
			if seg == skip {
				return true
			}
		}
	}
	return false
}
