package agentsandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What a launch packed into its private packed-refs is written back as
// loose refs — never a ref git would not name, never through a symlink the
// agent planted in the refs dir, never over a ref the repository has.
func TestReconcilePrivateRefs(t *testing.T) {
	common, priv := t.TempDir(), t.TempDir()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	os.MkdirAll(filepath.Join(common, "refs", "heads"), 0o755)
	os.WriteFile(filepath.Join(common, "packed-refs"), []byte("# pack-refs with: peeled\n"+a+" refs/heads/same\n"), 0o644)
	os.WriteFile(filepath.Join(common, "refs", "heads", "loose"), []byte(a+"\n"), 0o644)
	elsewhere := t.TempDir()
	os.Symlink(elsewhere, filepath.Join(common, "refs", "tags"))
	os.WriteFile(filepath.Join(priv, "packed-refs"), []byte(strings.Join([]string{
		"# pack-refs with: peeled fully-peeled sorted",
		b + " refs/heads/work",       // packed by the agent: restored
		b + " refs/heads/team/fix-1", // a new directory: restored
		a + " refs/heads/same",       // the repository agrees
		b + " refs/heads/loose",      // a loose ref wins
		b + " refs/tags/v1",          // through the planted symlink: refused
		b + " refs/heads/../../config",
		b + " refs/heads/.hidden",
		b + " refs/heads/x.lock",
		b + " HEAD",
		"zz refs/heads/bad-id",
		"^" + a,
	}, "\n")+"\n"), 0o644)
	got := reconcilePrivateRefs(priv, common)
	slices.Sort(got)
	if strings.Join(got, ",") != "refs/heads/team/fix-1,refs/heads/work" {
		t.Fatalf("restored %v", got)
	}
	for ref, want := range map[string]string{"refs/heads/work": b, "refs/heads/team/fix-1": b, "refs/heads/loose": a} {
		if g, _ := os.ReadFile(filepath.Join(common, ref)); strings.TrimSpace(string(g)) != want {
			t.Errorf("%s = %q", ref, g)
		}
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatal("a ref was written through a symlink")
	}
	if _, err := os.Stat(filepath.Join(common, "refs", "heads", "bad-id")); err == nil {
		t.Fatal("a ref with a bad id was written")
	}
}

// The private view holds its own config (with conductor's settings after the
// operator's), packed-refs and HEAD, and symlinks (macOS) or mount points
// (Linux) for the real subtrees; it never follows a symlinked file.
func TestPreparePrivateCommon(t *testing.T) {
	common := t.TempDir()
	for _, d := range []string{"objects", "refs", "logs", "hooks", "info", "worktrees/wt"} {
		os.MkdirAll(filepath.Join(common, d), 0o755)
	}
	os.WriteFile(filepath.Join(common, "config"), []byte("[core]\n\tbare = false\n"), 0o644)
	os.WriteFile(filepath.Join(common, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	priv := filepath.Join(t.TempDir(), "p")
	wt := filepath.Join(common, "worktrees", "wt")
	if err := preparePrivateCommon(priv, common, wt, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(priv, "config"))
	if !strings.HasPrefix(string(cfg), "[core]") || !strings.Contains(string(cfg), "writeCommitGraph = false") {
		t.Fatalf("config %q", cfg)
	}
	if l, _ := os.Readlink(filepath.Join(priv, "refs")); l != filepath.Join(common, "refs") {
		t.Fatalf("refs -> %q", l)
	}
	if l, _ := os.Readlink(filepath.Join(priv, "worktrees", "wt")); l != wt {
		t.Fatalf("worktree -> %q", l)
	}
	if err := preparePrivateCommon(priv, common, wt, false); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(priv, "refs")); err != nil || !fi.IsDir() {
		t.Fatal("no mount point for refs")
	}
	os.Remove(filepath.Join(common, "HEAD"))
	os.Symlink("/etc/passwd", filepath.Join(common, "HEAD"))
	if err := preparePrivateCommon(priv, common, wt, false); err == nil {
		t.Fatal("a symlinked HEAD was copied")
	}
}
