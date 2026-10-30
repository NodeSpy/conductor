package auth

import "testing"

func TestSafeText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain text, ünïcode and 日本語", "plain text, ünïcode and 日本語"},
		{"a\nb\tc\rd", `a\nb\tc\rd`},
		{"\x1b[2K\x1b[1A\x07\x00\x7f", `\x1b[2K\x1b[1A\x07\x00\x7f`},
		{"c1 \u0085 \u009b", `c1 \u0085 \u009b`},
		{"bidi \u202eevil\u202c \u2066x\u2069 \u200f \u061c", `bidi \u202eevil\u202c \u2066x\u2069 \u200f \u061c`},
		{"zw \u200b\u200d\u2060\ufeff sep \u2028\u2029", `zw \u200b\u200d\u2060\ufeff sep \u2028\u2029`},
		{"bad \xff utf8", `bad \xff utf8`},
	} {
		if got := SafeText(c.in); got != c.want {
			t.Errorf("SafeText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := SafeLines("one\x1b\ntwo\u202e"); len(got) != 2 || got[0] != `one\x1b` || got[1] != `two\u202e` {
		t.Errorf("SafeLines: %q", got)
	}
}
