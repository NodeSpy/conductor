package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// A path the config granted (an fs: path, a plugin's write) stays recorded
// while it exists, after the config stops granting it: what launches left
// there stays. The daemon logs it, naming it, when it was recorded, the
// ledger, and how to clear it; once the directory is gone it is forgotten.
// A path is recorded as the config's or a run's (both: a run's); an earlier
// ledger's bare lines load as a run's.
func TestConfiguredWritesStayWhileTheyExist(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	dir := t.TempDir()
	mk := func(n string) string {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fsDir, kept, run, both, legacy := mk("fs"), mk("kept"), mk("run"), mk("both"), mk("legacy")
	ledger := filepath.Join(dir, "ledger")
	if err := os.WriteFile(ledger, []byte(legacy+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	SetConfiguredWrites([]string{fsDir, kept, both, legacy})
	if err := AdmitWrites(fsDir, kept, run, both); err != nil {
		t.Fatal(err)
	}
	// A run's launch is let write it too (the config no longer names it then).
	SetConfiguredWrites(nil)
	if err := AdmitWrites(both); err != nil {
		t.Fatal(err)
	}
	if src := WriteSource(fsDir); !strings.Contains(src, "the config let a sandboxed launch write "+fsDir) || !strings.Contains(src, ledger) {
		t.Fatalf("source %q", src)
	}
	if src := WriteSource(run); !strings.Contains(src, "a run's sandboxed launch") {
		t.Fatalf("source %q", src)
	}
	// The daemon restarts with fs: dropped from the config, and kept still in it.
	ResetWriteLedger()
	if err := UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	SetConfiguredWrites([]string{kept})
	var logs []string
	ReportUnconfigured(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	got := GrantedWrites()
	for _, want := range []string{fsDir, kept, run, legacy, both} {
		if !slices.Contains(got, want) {
			t.Fatalf("%s forgotten: %v", want, got)
		}
	}
	if len(logs) != 1 || !strings.Contains(logs[0], fsDir) || !strings.Contains(logs[0], ledger) || !strings.Contains(logs[0], "still counted") || !strings.Contains(logs[0], "remove the directory") {
		t.Fatalf("logs %q", logs)
	}
	if src := WriteSource(fsDir); !strings.Contains(src, "delete its line from "+ledger) {
		t.Fatalf("source %q", src)
	}
	// Removed, it is forgotten at the next start.
	if err := os.RemoveAll(fsDir); err != nil {
		t.Fatal(err)
	}
	ResetWriteLedger()
	if err := UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	if got := GrantedWrites(); slices.Contains(got, fsDir) || !slices.Contains(got, kept) {
		t.Fatalf("reloaded %v", got)
	}
}

// Paths are resolved before any lock is taken: one whose resolution hangs
// (a network mount) holds up the launch that names it, and no other
// launch, nor Exclusive.
func TestAHungResolveHoldsUpOnlyItsLaunch(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	if err := UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	slow, other := filepath.Join(t.TempDir(), "nfs"), t.TempDir()
	release := make(chan struct{})
	t.Cleanup(func() { ResolveWrite = func(p string) []string { return []string{filepath.Clean(p)} } })
	ResolveWrite = func(p string) []string {
		if p == slow {
			<-release
		}
		return []string{filepath.Clean(p)}
	}
	hung := make(chan error, 1)
	go func() { hung <- AdmitWrites(slow) }()
	time.Sleep(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		if err := AdmitWrites(other); err != nil {
			done <- err
			return
		}
		done <- Exclusive(func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an unrelated launch waited on a hung resolve")
	}
	close(release)
	if err := <-hung; err != nil {
		t.Fatal(err)
	}
	if got := GrantedWrites(); !slices.Contains(got, slow) || !slices.Contains(got, other) {
		t.Fatalf("recorded %v", got)
	}
}

// Nothing is admitted while Exclusive runs: AdmitWrites waits for it.
func TestExclusiveHoldsAdmission(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	if err := UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	p := t.TempDir()
	in, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = Exclusive(func() error { close(in); <-release; return nil })
	}()
	<-in
	go func() { _ = AdmitWrites(p); close(done) }()
	select {
	case <-done:
		t.Fatal("a launch was admitted while Exclusive ran")
	case <-time.After(100 * time.Millisecond):
	}
	if slices.Contains(GrantedWrites(), p) {
		t.Fatal("recorded while Exclusive ran")
	}
	close(release)
	<-done
	if !slices.Contains(GrantedWrites(), p) {
		t.Fatal("not recorded once Exclusive returned")
	}
}

// A prune forgets what it found gone, unless it was granted again since it
// looked: that launch may have made it by now. And a hook that runs when a
// path is added (re-deriving PATH) runs outside the admission lock.
func TestPruneKeepsWhatWasGrantedMeanwhile(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	if err := UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	was := maxGranted
	t.Cleanup(func() { maxGranted, afterPruneStat = was, func() {} })
	maxGranted = 2
	dir := t.TempDir()
	again, gone := filepath.Join(dir, "again"), filepath.Join(dir, "gone")
	recordWrites(again, gone, filepath.Join(dir, "third"))
	afterPruneStat = func() {
		afterPruneStat = func() {}
		addWrites(resolveWrites([]string{again})) // granted again while the prune looked
	}
	recordWrites(t.TempDir()) // over maxGranted: prunes
	got := GrantedWrites()
	if !slices.Contains(got, again) || slices.Contains(got, gone) {
		t.Fatalf("recorded %v: want %s kept and %s forgotten", got, again, gone)
	}
}

var hookHold atomic.Pointer[chan struct{}]

func TestHooksRunOutsideTheAdmissionLock(t *testing.T) {
	t.Cleanup(ResetWriteLedger)
	if err := UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	in := make(chan struct{}, 1)
	hold := make(chan struct{})
	hookHold.Store(&hold)
	t.Cleanup(func() { hookHold.Store(nil); close(hold) })
	OnGrantedWrites(func() {
		if h := hookHold.Load(); h != nil {
			select {
			case in <- struct{}{}:
			default:
			}
			<-*h
		}
	})
	p := t.TempDir()
	go func() { _ = AdmitWrites(p) }()
	<-in
	done := make(chan error, 1)
	go func() { done <- Exclusive(func() error { return nil }) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hook held the admission lock")
	}
}
