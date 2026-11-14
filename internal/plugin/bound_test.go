//go:build linux || darwin

package plugin

import (
	"os"
	"path/filepath"
	"strings"
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
	if unbind, err := bindLaunch(nil, "p", cmds, sandbox.GrantedWrites); err == nil || !strings.Contains(err.Error(), "since it was checked") {
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
	if unbind, err := bindLaunch(nil, "p", withKeg, sandbox.GrantedWrites); err == nil {
		unbind()
		t.Fatal("bound a program whose keg a launch was let write into after the check")
	}
	sandbox.ResetWriteLedger()
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	unbind, err := bindLaunch(nil, "p", cmds, sandbox.GrantedWrites)
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
	go func() {
		unbind, err := bindLaunch(nil, "p", []jailCommand{{name: "tool", path: tool, real: tool}}, func() []string {
			close(checking)
			<-release
			return nil
		})
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
