//go:build linux || darwin

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Ported from the review, inverted: a launch admitted to write a dir after
// a plugin's commands were checked there, but before the plugin registered
// what it binds, fails the plugin's launch; registered first, the other
// launch is refused instead. Check and registration are one step against
// admission (sandbox.Exclusive).
func TestWriteAdmittedBetweenCheckAndBindIsCaught(t *testing.T) {
	home, sd := jailTestDirs(t)
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sandbox.ResetWriteLedger)
	x := filepath.Join(home, "tools")
	if err := os.MkdirAll(x, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", x+":/usr/bin:/bin")
	sd.PeerWritable = sandbox.GrantedWrites
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}
	cmds, err := findTool(t, s, sd) // checked: nothing may write x yet
	if err != nil || len(cmds) == 0 {
		t.Fatal(cmds, err)
	}
	if err := sandbox.AdmitWrites(x); err != nil { // a launch in between
		t.Fatalf("nothing binds x yet: %v", err)
	}
	if unbind, err := bindLaunch(nil, "p", cmds, nil); err == nil || !strings.Contains(err.Error(), "since it was checked") {
		if unbind != nil {
			unbind()
		}
		t.Fatalf("bound a program a launch was let write after the check: %v", err)
	}
	// Inside a directory the program loads from (a keg), too.
	sandbox.ResetWriteLedger()
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	keg := filepath.Join(home, "keg")
	if err := sandbox.AdmitWrites(filepath.Join(keg, "lib")); err != nil {
		t.Fatal(err)
	}
	withKeg := []jailCommand{{name: "tool", path: cmds[0].path, real: cmds[0].real, reads: []string{keg}}}
	if unbind, err := bindLaunch(nil, "p", withKeg, nil); err == nil {
		unbind()
		t.Fatal("bound a program whose keg a launch was let write into after the check")
	}
	sandbox.ResetWriteLedger()
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	unbind, err := bindLaunch(nil, "p", cmds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AdmitWrites(x); err == nil {
		t.Fatal("admitted to write what a running jail binds")
	}
	unbind()
	if err := sandbox.AdmitWrites(x); err != nil {
		t.Fatalf("once the jail is gone: %v", err)
	}
}

// A Client's registration outlives the process: while the plugin is
// restarting or parked crash-looping, nothing is admitted over what it
// runs, so its relaunch is not refused. Its next launch replaces it; Close
// releases it.
func TestBoundStaysWhileThePluginRestarts(t *testing.T) {
	a, b := fakeTool(t, "a/bin/tool"), fakeTool(t, "b/bin/tool")
	c := NewClient(Spec{Name: "p"}, Deps{})
	if c.deps.Sandbox.owner != c {
		t.Fatal("a Client's launches are not its own")
	}
	unbind, err := bindLaunch(c, "p", []jailCommand{{name: "tool", path: a, real: a}}, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	unbind() // the process exited
	if err := sandbox.AdmitWrites(filepath.Dir(a)); err == nil {
		t.Fatal("admitted over a restarting plugin's program")
	}
	// Relaunched with another program: the new one is what it binds.
	if _, err := bindLaunch(c, "p", []jailCommand{{name: "tool", path: b, real: b}}, func() []string { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AdmitWrites(filepath.Dir(a)); err != nil {
		t.Fatalf("the old program is no longer run: %v", err)
	}
	if err := sandbox.AdmitWrites(filepath.Dir(b)); err == nil {
		t.Fatal("admitted over the relaunched program")
	}
	c.Close()
	if err := sandbox.AdmitWrites(filepath.Dir(b)); err != nil {
		t.Fatalf("once the plugin is closed: %v", err)
	}
}

// No launch is admitted while a jail checks and registers what it binds: an
// admission that arrives meanwhile waits, then sees the registration.
func TestAdmissionWaitsForTheBind(t *testing.T) {
	tool := fakeTool(t, "x/bin/tool")
	checking, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	t.Cleanup(func() { bindHook = func() {} })
	bindHook = func() {
		close(checking)
		<-release
	}
	go func() {
		unbind, err := bindLaunch(nil, "p", []jailCommand{{name: "tool", path: tool, real: tool}}, nil)
		if err == nil {
			t.Cleanup(unbind)
		}
		done <- err
	}()
	<-checking
	admitted := make(chan error, 1)
	go func() { admitted <- sandbox.AdmitWrites(filepath.Dir(tool)) }()
	select {
	case err := <-admitted:
		t.Fatalf("a launch was admitted while the jail checked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err == nil {
		t.Fatal("admitted over what the jail registered")
	}
}

// A jail's bind step compares strings only: a peer path whose resolution
// hangs (a network mount) holds up that launch, never another launch's
// bind or admission.
func TestAHungResolveDoesNotHoldUpOtherBinds(t *testing.T) {
	a, b := fakeTool(t, "a/bin/tool"), fakeTool(t, "b/bin/tool")
	hang := filepath.Join(t.TempDir(), "nfs")
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	hung := make(chan error, 1)
	go func() {
		_, err := bindLaunch(nil, "slow", []jailCommand{{name: "tool", path: a, real: a}}, func() []string {
			// Never asked under the bind step's lock.
			free := make(chan struct{})
			go func() { _ = sandbox.Exclusive(func() error { return nil }); close(free) }()
			select {
			case <-free:
			case <-time.After(2 * time.Second):
				t.Error("the peers' paths are resolved holding the bind step's lock")
				return nil
			}
			<-release // resolving what the peers may write hangs
			return []string{hang}
		})
		hung <- err
	}()
	done := make(chan error, 1)
	go func() {
		unbind, err := bindLaunch(nil, "p", []jailCommand{{name: "tool", path: b, real: b}}, nil)
		if err == nil {
			unbind()
			err = sandbox.AdmitWrites(t.TempDir())
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another launch waited on a hung resolve")
	}
	once.Do(func() { close(release) })
	if err := <-hung; err != nil {
		t.Fatal(err)
	}
}

// Ported from the review, inverted: narrowing a code step's fs: from ~/go
// to ~/go/pkg/mod does not make what it planted in ~/go/bin while it was
// let write trusted. ~/go stays recorded while it exists: the program is
// refused and ~/go/bin stays off conductor's PATH, and the daemon's log
// says why and how to clear it.
func TestNarrowedConfigKeepsWhatWasPlanted(t *testing.T) {
	home, sd := jailTestDirs(t)
	dir := t.TempDir()
	ledger := filepath.Join(dir, "sandbox-writes")
	t.Cleanup(sandbox.ResetWriteLedger)
	if err := sandbox.UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	gohome := filepath.Join(home, "go")
	gobin := filepath.Join(gohome, "bin")
	if err := os.MkdirAll(filepath.Join(gohome, "pkg", "mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox.SetConfiguredWrites([]string{gohome})
	if _, err := (&sandbox.Spec{Mode: "container", Image: "img", FS: []string{gohome}}).WrapLocal([]string{"true"}, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// What the step planted while it was let write ~/go.
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gobin, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The daemon restarts with fs: narrowed to ~/go/pkg/mod.
	sandbox.ResetWriteLedger()
	if err := sandbox.UseWriteLedger(ledger); err != nil {
		t.Fatal(err)
	}
	sandbox.SetConfiguredWrites([]string{filepath.Join(gohome, "pkg", "mod")})
	var logs []string
	sandbox.ReportUnconfigured(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if len(logs) != 1 || !strings.Contains(logs[0], gohome) || !strings.Contains(logs[0], ledger) {
		t.Fatalf("logs %q", logs)
	}
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}, Commands: map[string]string{"tool": filepath.Join(gobin, "tool")}}
	if cmds, err := findTool(t, s, sd); err == nil || !strings.Contains(err.Error(), "the config let a sandboxed launch write "+gohome) || !strings.Contains(err.Error(), ledger) {
		t.Fatalf("commands %+v, err %v; want the planted program refused, naming where that came from", cmds, err)
	}
	if kept, _ := guardedPATH(gobin+":/usr/bin", sandbox.GrantedWrites()); kept != "/usr/bin" {
		t.Fatalf("PATH %q: ~/go/bin back on conductor's PATH", kept)
	}
}

// A write is compared in its resolved form: one through a symlink to where
// a running jail's program is is refused like the real path.
func TestAWriteThroughASymlinkIsResolved(t *testing.T) {
	tool := fakeTool(t, "real/bin/tool")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(filepath.Dir(tool)), link); err != nil {
		t.Fatal(err)
	}
	unbind, err := bindLaunch(nil, "p", []jailCommand{{name: "tool", path: tool, real: tool}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unbind()
	if err := sandbox.AdmitWrites(link); err == nil {
		t.Fatalf("admitted to write %s, a symlink to where plugin p's program is", link)
	}
}
