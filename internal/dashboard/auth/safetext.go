package auth

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SafeText makes server- or user-supplied text safe to print on one line of a
// terminal or a page: every C0 and C1 control (newline and tab included), DEL,
// the bidi controls, the zero-width characters and the line and paragraph
// separators become visible escapes (\n, \x1b, \u202e), and invalid UTF-8
// becomes \xNN. Nothing else changes, so plain text reads the same.
func SafeText(s string) string {
	if !needsEscape(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unsafeRune(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// SafeLines splits multi-line text on newlines and makes each line safe.
func SafeLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = SafeText(l)
	}
	return lines
}

func needsEscape(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unsafeRune(r) {
			return true
		}
	}
	return false
}

// unsafeRune reports the non-ASCII runes that change how surrounding text is
// shown without being visible themselves: C1 controls, bidi embeddings,
// overrides, isolates and marks, zero-width characters and the Unicode line
// and paragraph separators.
func unsafeRune(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff:
		return true
	case r == 0x2028, r == 0x2029:
		return true
	}
	return false
}
