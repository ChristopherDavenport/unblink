package webext

import (
	"encoding/json"
	"io/fs"
	"strings"
)

// Locales holds an extension's localized messages for chrome.i18n.getMessage and
// __MSG_key__ substitution (used in manifest strings and content-script CSS). Only the
// default locale is loaded, with an "en" fallback — enough for the strings unblink
// acts on; full per-request UI-language negotiation is out of scope (no UI).
type Locales struct {
	messages map[string]localeMessage // keyed by lowercased message name
}

type localeMessage struct {
	message      string
	placeholders map[string]string // lowercased name -> content
}

// loadLocales reads _locales/<defaultLocale>/messages.json, falling back to en.
func loadLocales(fsys fs.FS, defaultLocale string) *Locales {
	l := &Locales{messages: map[string]localeMessage{}}
	for _, loc := range []string{defaultLocale, "en"} {
		if loc != "" && l.loadLocale(fsys, loc) {
			break
		}
	}
	return l
}

func (l *Locales) loadLocale(fsys fs.FS, loc string) bool {
	data, err := fs.ReadFile(fsys, "_locales/"+loc+"/messages.json")
	if err != nil {
		return false
	}
	var raw map[string]struct {
		Message      string `json:"message"`
		Placeholders map[string]struct {
			Content string `json:"content"`
		} `json:"placeholders"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	for k, v := range raw {
		ph := make(map[string]string, len(v.Placeholders))
		for pk, pv := range v.Placeholders {
			ph[strings.ToLower(pk)] = pv.Content
		}
		l.messages[strings.ToLower(k)] = localeMessage{message: v.Message, placeholders: ph}
	}
	return len(raw) > 0
}

// Get returns the message for key (case-insensitive), or "" if absent.
func (l *Locales) Get(key string) string { return l.GetSub(key, nil) }

// GetSub returns the message for key with $1..$9 positional and $name$ placeholder
// substitutions applied. subs are the positional substitution values.
func (l *Locales) GetSub(key string, subs []string) string {
	if l == nil {
		return ""
	}
	m, ok := l.messages[strings.ToLower(key)]
	if !ok {
		return ""
	}
	return expandTokens(m.message, m.placeholders, subs)
}

// Substitute replaces every __MSG_key__ reference in s with its message (used for
// content-script CSS and manifest fields).
func (l *Locales) Substitute(s string) string {
	if l == nil || !strings.Contains(s, "__MSG_") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "__MSG_")
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.Index(s[i+6:], "__")
		if j < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		b.WriteString(l.Get(s[i+6 : i+6+j]))
		s = s[i+6+j+2:]
	}
	return b.String()
}

// expandTokens resolves $$ (literal $), $N (positional subs), and $name$ (placeholder
// content, itself positionally expanded) in a message string.
func expandTokens(s string, placeholders map[string]string, subs []string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' { // $$ -> $
			b.WriteByte('$')
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] >= '1' && s[i+1] <= '9' { // $N
			if idx := int(s[i+1] - '1'); idx < len(subs) {
				b.WriteString(subs[idx])
			}
			i++
			continue
		}
		if end := strings.IndexByte(s[i+1:], '$'); end >= 0 { // $name$
			if content, ok := placeholders[strings.ToLower(s[i+1:i+1+end])]; ok {
				b.WriteString(expandTokens(content, nil, subs))
				i = i + 1 + end
				continue
			}
		}
		b.WriteByte('$')
	}
	return b.String()
}
