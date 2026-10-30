package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceSymlinkCarveoutRefused is the regression for the red-team
// finding that a symlinked path component let a browser write (download /
// screenshot) land inside .git or a read-only/hidden carve-out: checkRel
// inspected the literal path, but the opener then followed an in-workspace
// symlink. The opener now refuses every symlinked component, so the literal
// .git / carve-out checks hold, and os.Root's escape protection is kept.
func TestWorkspaceSymlinkCarveoutRefused(t *testing.T) {
	ws := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(ws, ".git"), 0o755))
	ro := filepath.Join(ws, "ro") // a read-only / hidden carve-out
	must(os.MkdirAll(ro, 0o755))
	deny := []string{ro}
	must(os.Symlink(".git", filepath.Join(ws, "glink")))
	must(os.Symlink("ro", filepath.Join(ws, "rlink")))

	// A write through a symlinked component must be refused, and nothing may
	// land in the carve-outs.
	for _, rel := range []string{"glink/pwned.txt", "rlink/pwned.txt"} {
		if name, err := writeWorkspace(ws, rel, deny, strings.NewReader("x"), 1024); err == nil {
			t.Errorf("writeWorkspace(%q) must be refused, landed at %q", rel, name)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, ".git", "pwned.txt")); err == nil {
		t.Error("a write landed inside .git via a symlinked component")
	}
	if _, err := os.Lstat(filepath.Join(ro, "pwned.txt")); err == nil {
		t.Error("a write landed inside a read-only carve-out via a symlinked component")
	}

	// A legitimate write (no symlink) still works.
	if name, err := writeWorkspace(ws, filepath.Join(OutDir, "ok.txt"), deny, strings.NewReader("hi"), 1024); err != nil || name == "" {
		t.Errorf("a plain write must succeed: name=%q err=%v", name, err)
	}

	// An upload through a symlink to an absolute path outside the workspace is
	// refused (os.Root's escape protection, now also as a symlinked component).
	outside := filepath.Join(t.TempDir(), "secret")
	must(os.WriteFile(outside, []byte("TOP-SECRET"), 0o600))
	must(os.Symlink(outside, filepath.Join(ws, "esc")))
	dst := filepath.Join(t.TempDir(), "copied")
	if _, err := readWorkspace(ws, "esc", deny, 1<<20, dst); err == nil {
		t.Error("an upload via a symlink to outside the workspace must be refused")
	}
}
