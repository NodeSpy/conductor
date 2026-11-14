package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every launch the sandbox wraps records the host paths it lets the launch
// write: a jail's writable binds (not read-only ones, a tmpfs, or a link)
// and its /tmp, a Seatbelt profile's writes, an agent's read-write paths, a
// container's workdir and fs:. With a ledger they persist: reloaded, what
// still exists is kept and what is gone is dropped. Paths inside a skipped
// dir, and anything while no ledger is in use, are not recorded.
func TestWrapsRecordWhatTheyLetWrite(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	dir := t.TempDir()
	mk := func(n string) string {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rw, src, ro, skipped, agentRW, plugW, ctrDir, ctrFS := mk("rw"), mk("src"), mk("ro"), mk("state/x"), mk("agent"), mk("plugin"), mk("ctr"), mk("ctrfs")
	gone := filepath.Join(dir, "gone")

	(&Spec{Mode: "container", Image: "img", FS: []string{ctrFS}}).WrapLocal([]string{"true"}, ctrDir, nil, nil)
	if len(GrantedWrites()) != 0 {
		t.Fatalf("recorded with no ledger in use: %v", GrantedWrites())
	}
	ledger := filepath.Join(dir, "ledger")
	if err := UseWriteLedger(ledger, filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}
	was := CheckGOOS
	t.Cleanup(func() { CheckGOOS = was })
	CheckGOOS = "linux"
	nf := &NetForward{Self: "/bin/true", Binds: []BindMount{{Path: rw}, {Path: "/jail/p", Src: src}, {Path: ro, RO: true}, {Path: "/t", Tmpfs: true}, {Path: "/l", Link: "/x"}, {Path: skipped}, {Path: gone}}}
	if _, err := (&Spec{Mode: "namespace"}).WrapLocal([]string{"true"}, "", nil, nf); err != nil {
		t.Fatal(err)
	}
	CheckGOOS = "darwin"
	if _, err := (&Spec{Mode: "namespace"}).WrapLocal([]string{"true"}, "", nil, &NetForward{Agent: &AgentProfile{ReadWrite: []string{agentRW}}}); err != nil {
		t.Fatal(err)
	}
	WrapSeatbeltPlugin([]string{"true"}, &PluginProfile{Write: []string{plugW}})
	if _, err := (&Spec{Mode: "container", Image: "img", FS: []string{ctrFS}}).WrapLocal([]string{"true"}, ctrDir, nil, nil); err != nil {
		t.Fatal(err)
	}
	got := GrantedWrites()
	for _, want := range []string{rw, src, agentRW, plugW, ctrDir, ctrFS, gone} {
		if !slices.Contains(got, want) {
			t.Errorf("%s not recorded: %v", want, got)
		}
	}
	for _, not := range []string{ro, "/jail/p", "/t", "/l", skipped} {
		if slices.Contains(got, not) {
			t.Errorf("%s recorded: %v", not, got)
		}
	}
	// A restart: what still exists is kept, what is gone dropped.
	ResetWriteLedger()
	if err := UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	got = GrantedWrites()
	if !slices.Contains(got, rw) || !slices.Contains(got, ctrFS) || slices.Contains(got, gone) {
		t.Fatalf("reloaded %v, want what exists and not %s", got, gone)
	}
}
