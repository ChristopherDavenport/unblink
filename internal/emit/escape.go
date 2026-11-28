package emit

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
)

// Page text that contains "<", ">" or something shaped like a character
// reference must reach the Markdown as literal text: it must not become raw
// HTML (an agent host that renders Markdown would build the element — an
// attacker-chosen <img> is a zero-click beacon, see DefangImages), and it must
// not be decoded into a different character.
//
// The converter's base plugin does this with HTML entities ("&lt;", "&gt;"),
// which is correct for a renderer but is noise to a model reading the Markdown
// as text — link text "<svelte:window>" arrived as "&lt;svelte:window&gt;" — and
// it leaves a literal "&lt;" in page text indistinguishable from a real "<".
// literalText replaces that with CommonMark backslash escapes, which are just
// as inert to a renderer and read as the character itself:
//
//	<   ->  \<          never starts a tag or an autolink
//	>   ->  >           inert once no "<" can open a tag; "\>" at the start of a
//	                    line, where it would open a block quote
//	&   ->  \&          only when it begins a character reference (&amp; &#39;)
//
// It works by swapping the characters for private sentinels before the base
// plugin's entity replacement sees them (text nodes only: code spans and code
// blocks never pass through text transformers) and swapping them back after all
// other rendering. The sentinels are Unicode noncharacters, which have no
// meaning in interchange; any already in the page text are dropped first so
// page content cannot forge one.
type literalText struct{}

const (
	sentLT  = '﷐'
	sentGT  = '﷑'
	sentAmp = '﷒'
)

// charRef matches the start of a named, decimal or hex character reference.
var charRef = regexp.MustCompile(`&(?:[A-Za-z][A-Za-z0-9]{1,31}|#[0-9]{1,7}|#[xX][0-9A-Fa-f]{1,6});`)

var (
	dropSentinels = strings.NewReplacer(string(sentLT), "", string(sentGT), "", string(sentAmp), "")
	toSentinels   = strings.NewReplacer("<", string(sentLT), ">", string(sentGT))
)

func (literalText) Name() string { return "unblink-literal-text" }

func (literalText) Init(conv *converter.Converter) error {
	conv.Register.TextTransformer(func(_ converter.Context, content string) string {
		return protectText(content)
	}, converter.PriorityEarly-50)
	conv.Register.PostRenderer(func(_ converter.Context, result []byte) []byte {
		return restoreText(result)
	}, converter.PriorityLate+500)
	return nil
}

// protectText swaps the characters literalText owns for sentinels.
func protectText(s string) string {
	if strings.ContainsAny(s, string(sentLT)+string(sentGT)+string(sentAmp)) {
		s = dropSentinels.Replace(s)
	}
	if !strings.ContainsAny(s, "<>&") {
		return s
	}
	s = toSentinels.Replace(s)
	if strings.Contains(s, "&") {
		s = charRef.ReplaceAllStringFunc(s, func(m string) string {
			return string(sentAmp) + m[1:]
		})
	}
	return s
}

// restoreText turns the sentinels into their Markdown-escaped characters.
func restoreText(md []byte) []byte {
	if !bytes.ContainsRune(md, sentLT) && !bytes.ContainsRune(md, sentGT) && !bytes.ContainsRune(md, sentAmp) {
		return md
	}
	var out bytes.Buffer
	out.Grow(len(md) + 16)
	// A ">" opens a block quote when it is the first thing in a block: at the
	// start of a line, or after indentation, list markers ("- ", "12. ") and
	// other quote markers. blockStart tracks that prefix.
	const (
		atStart   = iota // only indentation, quote markers and whole list markers so far
		inNumber         // digits of an ordered-list marker
		wantSpace        // a list marker that needs its space to count
		inContent        // the block's content has begun
	)
	state := atStart
	for _, r := range string(md) {
		switch r {
		case sentLT:
			out.WriteString(`\<`)
		case sentAmp:
			out.WriteString(`\&`)
		case sentGT:
			if state == atStart {
				out.WriteString(`\>`)
			} else {
				out.WriteByte('>')
			}
		default:
			out.WriteRune(r)
		}
		switch {
		case r == '\n':
			state = atStart
		case state == inContent:
		case r == ' ' || r == '\t':
			switch state {
			case wantSpace:
				state = atStart
			case inNumber:
				state = inContent
			}
		case state == atStart && (r == '-' || r == '*' || r == '+'):
			state = wantSpace
		case (state == atStart || state == inNumber) && r >= '0' && r <= '9':
			state = inNumber
		case state == inNumber && (r == '.' || r == ')'):
			state = wantSpace
		case state == atStart && r == '>':
		default:
			state = inContent
		}
	}
	return out.Bytes()
}

// escapeInline makes a plain string (the page title) safe to place in Markdown
// outside the converter, with the same rules as literalText.
func escapeInline(s string) string {
	return string(restoreText([]byte(protectText(s))))
}
