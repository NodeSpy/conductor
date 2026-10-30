// Package safetext classifies the characters that change how text around them
// is shown without being visible themselves: controls, bidi overrides,
// zero-width characters. The dashboard escapes them before printing server-
// or user-supplied text, and config validation refuses them where they can
// never be valid (an SSH public key line).
package safetext

import "unicode/utf8"

// Invisible reports the non-ASCII runes that change how surrounding text is
// shown without being visible: C1 controls (U+0080–U+009F), the bidi
// embeddings, overrides, isolates and marks (U+202A–U+202E, U+2066–U+2069,
// U+200E, U+200F, U+061C), the zero-width characters (U+200B–U+200D, U+2060,
// U+FEFF) and the Unicode line and paragraph separators (U+2028, U+2029).
func Invisible(r rune) bool {
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

// Control reports a C0 control (tab and newline included), DEL, or an
// Invisible rune.
func Control(r rune) bool {
	return r < 0x20 || r == 0x7f || Invisible(r)
}

// FirstControl returns the first Control rune in s and its byte offset, or
// (utf8.RuneError, i) at the first invalid UTF-8 byte, or (0, -1) when s has
// neither.
func FirstControl(s string) (rune, int) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return r, i
		}
		if Control(r) {
			return r, i
		}
		i += size
	}
	return 0, -1
}
