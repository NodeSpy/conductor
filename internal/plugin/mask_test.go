package plugin

import (
	"path/filepath"
	"testing"
)

func TestMasksExcludingBinaryDir(t *testing.T) {
	state := filepath.FromSlash("/home/u/.local/state/conductor")
	cfg := filepath.FromSlash("/home/u/.config/conductor")
	bin := filepath.FromSlash("/home/u/.local/state/conductor/plugins/engines/js/conductor-js")

	got := masksExcludingBinaryDir([]string{state, cfg}, bin)

	// state is an ancestor of the binary dir → dropped; cfg is unrelated → kept.
	if len(got) != 1 || got[0] != cfg {
		t.Fatalf("expected only the config dir kept, got %v", got)
	}
}

func TestMasksExcludingBinaryDirEmptyBin(t *testing.T) {
	masks := []string{"/a", "/b"}
	got := masksExcludingBinaryDir(masks, "")
	if len(got) != 2 {
		t.Fatalf("a not-installed spec (empty binPath) leaves masks unchanged, got %v", got)
	}
}

func TestMasksExcludingBinaryDirNoSiblingFalsePositive(t *testing.T) {
	// A mask that only shares a path PREFIX string (not a real ancestor) must be
	// kept: /home/u/state-old is not an ancestor of /home/u/state/....
	bin := filepath.FromSlash("/home/u/state/plugins/js/bin")
	sibling := filepath.FromSlash("/home/u/state-old")
	got := masksExcludingBinaryDir([]string{sibling}, bin)
	if len(got) != 1 || got[0] != sibling {
		t.Fatalf("a prefix-only lookalike must be kept, got %v", got)
	}
}
