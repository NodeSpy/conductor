// Package procexec replaces the running process image with another program
// and keeps the parent-death signal its parent set.
//
// The kernel keeps that signal per thread, and a thread that isn't the one
// the process started on never has it. Go may run any goroutine, the main one
// included, on any thread, so a bare syscall.Exec can drop it: a daemon run
// under `unshare --fork --kill-child`, or a child its parent started with
// Pdeathsig, would then outlive the parent after its first re-exec.
package procexec

import "syscall"

// Exec is syscall.Exec, run on a locked thread that carries the parent-death
// signal the process started with. It returns only on failure.
func Exec(path string, argv, env []string) error {
	unlock := armThread()
	err := syscall.Exec(path, argv, env)
	unlock()
	return err
}
