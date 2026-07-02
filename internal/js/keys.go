package js

import "strings"

// keyInfo is the identity of a keyboard key across the four fields handlers
// read: the printable/named `key`, the physical `code`, the legacy numeric
// `keyCode`/`which`, and whether the key produces a character.
type keyInfo struct {
	key       string // KeyboardEvent.key (e.g. "Enter", "a", "ArrowDown")
	code      string // KeyboardEvent.code (e.g. "Enter", "KeyA", "ArrowDown")
	keyCode   int    // legacy keyCode/which
	printable bool   // does pressing it insert a character?
	char      string // the inserted character (printable keys only)
}

// namedKeys maps the special (non-character) key names to their code + legacy
// keyCode. Lowercased on lookup so "enter"/"Enter"/"ENTER" all resolve.
var namedKeys = map[string]struct {
	code    string
	keyCode int
}{
	"enter":      {"Enter", 13},
	"tab":        {"Tab", 9},
	"escape":     {"Escape", 27},
	"esc":        {"Escape", 27},
	"backspace":  {"Backspace", 8},
	"delete":     {"Delete", 46},
	"del":        {"Delete", 46},
	"arrowup":    {"ArrowUp", 38},
	"arrowdown":  {"ArrowDown", 40},
	"arrowleft":  {"ArrowLeft", 37},
	"arrowright": {"ArrowRight", 39},
	"up":         {"ArrowUp", 38},
	"down":       {"ArrowDown", 40},
	"left":       {"ArrowLeft", 37},
	"right":      {"ArrowRight", 39},
	"home":       {"Home", 36},
	"end":        {"End", 35},
	"pageup":     {"PageUp", 33},
	"pagedown":   {"PageDown", 34},
	"insert":     {"Insert", 45},
	"shift":      {"ShiftLeft", 16},
	"control":    {"ControlLeft", 17},
	"ctrl":       {"ControlLeft", 17},
	"alt":        {"AltLeft", 18},
	"meta":       {"MetaLeft", 91},
}

// resolveKey maps an interact `key` string to the full keyInfo. A single
// printable character resolves to its char + physical code; a named key ("Enter",
// "ArrowDown") resolves via namedKeys; an empty key defaults to Enter (the common
// "press Enter to submit/search" gesture). Multi-rune non-named strings fall back
// to a keyless-but-typed key so a handler at least sees a keydown.
func resolveKey(k string) keyInfo {
	if k == "" {
		k = "Enter"
	}
	if len(k) == 1 {
		return charKey(k)
	}
	if nk, ok := namedKeys[strings.ToLower(k)]; ok {
		return keyInfo{key: named(k), code: nk.code, keyCode: nk.keyCode}
	}
	// " " arrives as the literal space when a caller passes "Space"/"Spacebar".
	switch strings.ToLower(k) {
	case "space", "spacebar":
		return keyInfo{key: " ", code: "Space", keyCode: 32, printable: true, char: " "}
	}
	// Unknown multi-char key: treat as a named key with no legacy code so a
	// keydown at least fires with the given key string.
	return keyInfo{key: k}
}

// named returns the canonical KeyboardEvent.key for a named key: browsers use
// TitleCase ("Enter", "ArrowDown"), so normalize common aliases.
func named(k string) string {
	switch strings.ToLower(k) {
	case "enter":
		return "Enter"
	case "tab":
		return "Tab"
	case "escape", "esc":
		return "Escape"
	case "backspace":
		return "Backspace"
	case "delete", "del":
		return "Delete"
	case "arrowup", "up":
		return "ArrowUp"
	case "arrowdown", "down":
		return "ArrowDown"
	case "arrowleft", "left":
		return "ArrowLeft"
	case "arrowright", "right":
		return "ArrowRight"
	case "home":
		return "Home"
	case "end":
		return "End"
	case "pageup":
		return "PageUp"
	case "pagedown":
		return "PageDown"
	case "insert":
		return "Insert"
	case "shift":
		return "Shift"
	case "control", "ctrl":
		return "Control"
	case "alt":
		return "Alt"
	case "meta":
		return "Meta"
	}
	return k
}

// charKey builds the keyInfo for a single printable character: uppercase-ASCII
// keyCode (browsers report the uppercase code regardless of shift) and a
// KeyX/DigitN physical code where one applies.
func charKey(ch string) keyInfo {
	r := []rune(ch)[0]
	info := keyInfo{key: ch, printable: true, char: ch}
	switch {
	case r >= 'a' && r <= 'z':
		info.code = "Key" + strings.ToUpper(ch)
		info.keyCode = int(r) - ('a' - 'A')
	case r >= 'A' && r <= 'Z':
		info.code = "Key" + ch
		info.keyCode = int(r)
	case r >= '0' && r <= '9':
		info.code = "Digit" + ch
		info.keyCode = int(r)
	case r == ' ':
		info.code = "Space"
		info.keyCode = 32
	default:
		// Punctuation: no reliable physical code; the uppercase rune value is a
		// good-enough legacy keyCode for the handlers that read it.
		info.keyCode = int(r)
	}
	return info
}
