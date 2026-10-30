package safetext

import "testing"

func TestFirstControl(t *testing.T) {
	for _, c := range []struct {
		in string
		r  rune
		at int
	}{
		{"ssh-ed25519 AAAA ops@box", 0, -1},
		{"ssh-ed25519 AAAA Zoë Müller", 0, -1},
		{"ssh-ed25519 AAAA x\r\x1b[2K", '\r', 18},
		{"a\tb", '\t', 1},
		{"a\x7f", 0x7f, 1},
		{"a" + string(rune(0x85)), 0x85, 1},
		{"ab" + string(rune(0x202e)) + "c", 0x202e, 2},
		{"a" + string(rune(0x2067)), 0x2067, 1},
		{"a" + string(rune(0x200b)), 0x200b, 1},
		{"a" + string(rune(0xfeff)), 0xfeff, 1},
		{"a" + string(rune(0x2028)), 0x2028, 1},
		{"a\xffb", 0xfffd, 1},
	} {
		if r, at := FirstControl(c.in); r != c.r || at != c.at {
			t.Errorf("FirstControl(%q) = %U at %d, want %U at %d", c.in, r, at, c.r, c.at)
		}
	}
}
