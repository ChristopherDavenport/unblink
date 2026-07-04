package webext

import "strings"

// urlFilter is a compiled declarativeNetRequest / Adblock-Plus urlFilter. The syntax:
//
//	||  leading  anchor to the start of a (sub)domain
//	|   leading  anchor to the start of the URL; trailing anchor to its end
//	^   a single separator character (anything but a letter/digit/_/-/./%), or URL end
//	*   wildcard: any run of characters, including empty
//
// It is matched by walking literal segments (split on '*') against the URL — no
// regexp, so no ReDoS. Segment count is tiny in real filter lists, so the linear
// backtracking between wildcards is cheap.
type urlFilter struct {
	domainAnchor  bool
	startAnchor   bool
	endAnchor     bool
	caseSensitive bool
	segs          []string // non-empty literal segments, in order (may contain '^')
}

func compileURLFilter(raw string, caseSensitive bool) *urlFilter {
	f := &urlFilter{caseSensitive: caseSensitive}
	s := raw
	switch {
	case strings.HasPrefix(s, "||"):
		f.domainAnchor = true
		s = s[2:]
	case strings.HasPrefix(s, "|"):
		f.startAnchor = true
		s = s[1:]
	}
	if strings.HasSuffix(s, "|") {
		f.endAnchor = true
		s = s[:len(s)-1]
	}
	if !caseSensitive {
		s = strings.ToLower(s)
	}
	parts := strings.Split(s, "*")
	// A leading/trailing empty segment means a '*' sat at that end, which relaxes the
	// corresponding anchor. Drop empties but remember whether the ends were anchored.
	if len(parts) > 0 && parts[0] == "" {
		f.startAnchor = false
		f.domainAnchor = false
	}
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		f.endAnchor = false
	}
	for _, p := range parts {
		if p != "" {
			f.segs = append(f.segs, p)
		}
	}
	return f
}

// match reports whether the (already lowercased when case-insensitive) URL satisfies
// the filter.
func (f *urlFilter) match(rawURL string) bool {
	u := rawURL
	if !f.caseSensitive {
		u = strings.ToLower(u)
	}
	if len(f.segs) == 0 {
		return true // the filter was "*" (or empty) — matches everything
	}
	switch {
	case f.startAnchor:
		return f.tryAt(u, 0)
	case f.domainAnchor:
		for _, st := range domainAnchorPositions(u) {
			if f.tryAt(u, st) {
				return true
			}
		}
		return false
	default:
		for st := 0; st <= len(u); st++ {
			if f.tryAt(u, st) {
				return true
			}
		}
		return false
	}
}

// tryAt attempts to match the whole segment chain with the first segment starting at
// st.
func (f *urlFilter) tryAt(u string, st int) bool {
	end, ok := matchSegAt(u, st, f.segs[0])
	if !ok {
		return false
	}
	return f.matchRest(u, 1, end)
}

// matchRest matches segments[idx:] anywhere at or after pos (they are separated by
// wildcards). The final segment must land on the URL end when the filter is
// end-anchored.
func (f *urlFilter) matchRest(u string, idx, pos int) bool {
	if idx == len(f.segs) {
		return !f.endAnchor || pos == len(u)
	}
	last := idx == len(f.segs)-1
	for q := pos; q <= len(u); q++ {
		end, ok := matchSegAt(u, q, f.segs[idx])
		if !ok {
			continue
		}
		if last {
			if !f.endAnchor || end == len(u) {
				return true
			}
			continue // keep searching for a later occurrence that reaches the end
		}
		if f.matchRest(u, idx+1, end) {
			return true
		}
	}
	return false
}

// matchSegAt matches one literal segment (with '^' separators) at position pos,
// returning the index just past the match. '^' matches one separator byte, or the end
// of the URL (zero-width).
func matchSegAt(u string, pos int, seg string) (int, bool) {
	i := pos
	for k := 0; k < len(seg); k++ {
		c := seg[k]
		if c == '^' {
			if i < len(u) {
				if isSeparatorByte(u[i]) {
					i++
					continue
				}
				return 0, false
			}
			continue // end of URL counts as a separator
		}
		if i >= len(u) || u[i] != c {
			return 0, false
		}
		i++
	}
	return i, true
}

// domainAnchorPositions returns the byte offsets where a (sub)domain begins: the start
// of the authority (just after "scheme://") and each position after a '.' within it.
func domainAnchorPositions(u string) []int {
	hostStart := 0
	if i := strings.Index(u, "://"); i >= 0 {
		hostStart = i + 3
	}
	authEnd := len(u)
	for i := hostStart; i < len(u); i++ {
		switch u[i] {
		case '/', '?', '#':
			authEnd = i
		}
		if authEnd != len(u) {
			break
		}
	}
	pos := []int{hostStart}
	for i := hostStart; i < authEnd; i++ {
		if u[i] == '.' {
			pos = append(pos, i+1)
		}
	}
	return pos
}

// isSeparatorByte reports whether b is a urlFilter "separator": anything but an ASCII
// letter, digit, or one of _ - . %.
func isSeparatorByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return false
	case b == '_' || b == '-' || b == '.' || b == '%':
		return false
	}
	return true
}
