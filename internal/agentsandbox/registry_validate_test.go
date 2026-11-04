package agentsandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A recorded layout is bound read-write into the next launch, so it is
// re-checked when the registry is read back and when a follow-up dispatch
// reuses it: a tampered record never widens what a launch can write.
func TestRecordedLayoutsAreRevalidated(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	state := filepath.Join(home, ".local", "state", "conductor")
	os.MkdirAll(state, 0o700)
	old := GitTrust
	t.Cleanup(func() { GitTrust = old })
	GitTrust.Home, GitTrust.Forbidden = home, []string{state}

	base := filepath.Join(root, "base")
	git(t, root, "init", "-q", "-b", "main", base)
	git(t, base, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(root, "wt")
	git(t, base, "worktree", "add", "-q", "-b", "fix", wt)
	other := filepath.Join(root, "other")
	git(t, base, "worktree", "add", "-q", "-b", "other", other)
	l, err := ResolveGitLayout(wt)
	if err != nil {
		t.Fatal(err)
	}
	good := Entry{Workspace: wt, Identity: "agent:x", ReadOnly: l.ReadOnly, ReadWrite: l.ReadWrite, Hide: l.Hide,
		GitCommon: l.Common, GitWorktree: l.HeadDir, Push: &PushTarget{Branch: "fix", GitDir: l.GitDir, HeadDir: l.HeadDir}}
	if err := ValidateRecorded(good); err != nil {
		t.Fatalf("a resolved layout is refused: %v", err)
	}
	// Direct mode (common read-write) and a plain clone pass too.
	SharedGitDirect = true
	ld, err := ResolveGitLayout(wt)
	SharedGitDirect = false
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecorded(Entry{Workspace: wt, ReadWrite: ld.ReadWrite}); err != nil {
		t.Fatalf("a direct layout is refused: %v", err)
	}

	bad := map[string]func(e *Entry){
		"home read-write":         func(e *Entry) { e.ReadWrite = append(e.ReadWrite, home) },
		"state read-write":        func(e *Entry) { e.ReadWrite = append(e.ReadWrite, filepath.Join(state, "x")) },
		"non-git read-write":      func(e *Entry) { e.ReadWrite = append(e.ReadWrite, filepath.Join(root, "elsewhere")) },
		"relative read-write":     func(e *Entry) { e.ReadWrite = append(e.ReadWrite, "base/.git") },
		"root read-write":         func(e *Entry) { e.ReadWrite = append(e.ReadWrite, "/") },
		"push into the home":      func(e *Entry) { e.Push.GitDir = home },
		"push not a git dir":      func(e *Entry) { e.Push.GitDir = filepath.Join(root, "wt") },
		"push head into state":    func(e *Entry) { e.Push.HeadDir = state },
		"another worktree's dir":  func(e *Entry) { e.GitWorktree = filepath.Join(l.Common, "worktrees", "other") },
		"worktree without common": func(e *Entry) { e.GitCommon = "" },
		"common is the home":      func(e *Entry) { e.GitCommon = home },
	}
	os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755)
	for name, mut := range bad {
		e := good
		e.ReadWrite = append([]string(nil), good.ReadWrite...)
		p := *good.Push
		e.Push = &p
		mut(&e)
		if err := ValidateRecorded(e); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}

	// Read back from disk: a tampered record is dropped, a good one kept.
	path := filepath.Join(root, ".workspaces.json")
	a := &Registry{}
	if err := a.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	a.Register(good)
	tampered := good
	tampered.Workspace = other
	tampered.ReadWrite = append(append([]string(nil), good.ReadWrite...), home)
	a.Register(tampered)
	b, _ := os.ReadFile(path)
	var list []map[string]any
	if err := json.Unmarshal(b, &list); err != nil || len(list) != 2 || !strings.Contains(string(b), home) {
		t.Fatalf("persisted: %s %v", b, err)
	}
	r := &Registry{}
	if err := r.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup(wt); !ok {
		t.Error("a valid record is dropped on load")
	}
	if e, ok := r.Lookup(other); ok && e.Workspace == canon(other) {
		t.Errorf("a record binding the home read-write survives a reload: %+v", e)
	}
}
