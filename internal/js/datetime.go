package js

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Time zones for Intl.DateTimeFormat and Date's locale methods. goja's Date
// reads the process zone (time.Local), so the engine's default zone is that
// one, reported by its IANA name; an explicit timeZone option is resolved
// against the system zone database.

var (
	zoneCache     sync.Map // requested name → zoneEntry
	localZoneOnce sync.Once
	localZone     string
	offsetZoneRE  = regexp.MustCompile(`^([+-])(\d\d)(?::?(\d\d))?$`)
)

type zoneEntry struct {
	loc  *time.Location
	name string
}

// maxZoneNameLen bounds a page-supplied zone name before it reaches the zone
// database lookup (the longest IANA name is under 40 bytes).
const maxZoneNameLen = 64

// localZoneName is the IANA name of the process zone: $TZ, else the
// /etc/localtime link, else UTC or a fixed-offset name.
func localZoneName() string {
	localZoneOnce.Do(func() {
		if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
			if _, err := time.LoadLocation(tz); err == nil {
				localZone = tz
				return
			}
		}
		if link, err := os.Readlink("/etc/localtime"); err == nil {
			if i := strings.Index(link, "zoneinfo/"); i >= 0 {
				name := link[i+len("zoneinfo/"):]
				if _, err := time.LoadLocation(name); err == nil {
					localZone = name
					return
				}
			}
		}
		_, off := time.Now().Zone()
		switch {
		case off == 0:
			localZone = "UTC"
		case off%3600 == 0:
			localZone = fmt.Sprintf("Etc/GMT%+d", -off/3600) // POSIX sign: Etc/GMT+8 is UTC-8
		default:
			localZone = "UTC"
		}
	})
	return localZone
}

// resolveZone maps a time zone identifier to a location and its canonical
// name. "" is the process zone. Identifiers match case-insensitively, as
// ECMA-402 requires; ±hh:mm offsets are accepted. nil means unknown.
func resolveZone(name string) (*time.Location, string) {
	if name == "" {
		return time.Local, localZoneName()
	}
	if len(name) > maxZoneNameLen {
		return nil, ""
	}
	if e, ok := zoneCache.Load(name); ok {
		z := e.(zoneEntry)
		return z.loc, z.name
	}
	loc, canon := lookupZone(name)
	zoneCache.Store(name, zoneEntry{loc, canon})
	return loc, canon
}

func lookupZone(name string) (*time.Location, string) {
	switch strings.ToUpper(name) {
	case "UTC", "GMT", "ETC/UTC", "ETC/GMT", "Z":
		return time.UTC, "UTC"
	}
	if m := offsetZoneRE.FindStringSubmatch(name); m != nil {
		var h, mi int
		_, _ = fmt.Sscanf(m[2], "%d", &h)
		if m[3] != "" {
			_, _ = fmt.Sscanf(m[3], "%d", &mi)
		}
		if h > 23 || mi > 59 {
			return nil, ""
		}
		secs := h*3600 + mi*60
		if m[1] == "-" {
			secs = -secs
		}
		canon := fmt.Sprintf("%s%02d:%02d", m[1], h, mi)
		return time.FixedZone(canon, secs), canon
	}
	if strings.ContainsAny(name, `\ .`) || strings.HasPrefix(name, "/") {
		return nil, ""
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc, name
	}
	// Case-insensitive match: America/New_York from america/new_york.
	parts := strings.Split(strings.ToLower(name), "/")
	for i, p := range parts {
		segs := strings.Split(p, "_")
		for j, s := range segs {
			if s != "" {
				segs[j] = strings.ToUpper(s[:1]) + s[1:]
			}
		}
		parts[i] = strings.Join(segs, "_")
	}
	canon := strings.Join(parts, "/")
	if loc, err := time.LoadLocation(canon); err == nil {
		return loc, canon
	}
	return nil, ""
}

// zoneAbbr is the short zone name shown for timeZoneName: the database's
// abbreviation ("PST"), or a GMT offset where the database has none.
func zoneAbbr(abbr string, offSecs int) string {
	if abbr != "" && abbr[0] != '+' && abbr[0] != '-' {
		return abbr
	}
	return gmtOffset(offSecs, false)
}

// gmtOffset formats an offset as "GMT-8" / "GMT+5:30" (short) or
// "GMT-08:00" (long); zero is "GMT".
func gmtOffset(offSecs int, long bool) string {
	if offSecs == 0 {
		return "GMT"
	}
	sign := "+"
	if offSecs < 0 {
		sign, offSecs = "-", -offSecs
	}
	h, m := offSecs/3600, offSecs%3600/60
	if long {
		return fmt.Sprintf("GMT%s%02d:%02d", sign, h, m)
	}
	if m == 0 {
		return fmt.Sprintf("GMT%s%d", sign, h)
	}
	return fmt.Sprintf("GMT%s%d:%02d", sign, h, m)
}

// installZoneInfo registers the prelude's zone lookup:
// __unblinkZoneInfo(epochMs, zone) → [offsetMinutes, abbreviation, canonical
// name, shortOffset, longOffset], or null for an unknown zone. The Intl
// section captures it and removes the global.
func (b *bridge) installZoneInfo() {
	vm := b.vm
	_ = vm.Set("__unblinkZoneInfo", func(call goja.FunctionCall) goja.Value {
		ms := call.Argument(0).ToFloat()
		name := ""
		if a := call.Argument(1); !goja.IsUndefined(a) && !goja.IsNull(a) {
			name = a.String()
		}
		loc, canon := resolveZone(name)
		if loc == nil || ms != ms || ms > 8.64e15 || ms < -8.64e15 {
			return goja.Null()
		}
		abbr, off := time.UnixMilli(int64(ms)).In(loc).Zone()
		return vm.ToValue([]any{off / 60, zoneAbbr(abbr, off), canon, gmtOffset(off, false), gmtOffset(off, true)})
	})
}
