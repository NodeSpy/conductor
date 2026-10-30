package auth

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/safetext"
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
		case safetext.Invisible(r):
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
		if safetext.Control(r) {
			return true
		}
	}
	return false
}
