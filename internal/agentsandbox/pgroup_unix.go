//go:build linux || darwin

package agentsandbox

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// PgroupCommand is conductor's hidden subcommand that starts a launch in
// a process group of its own (RunPgroup).
const PgroupCommand = "sandbox-pgroup"

// RunPgroup is `conductor sandbox-pgroup -- ARGV...`: it makes itself the
// leader of a new process group and execs ARGV in its place (same pid).
// macOS has no pid namespace: a process an agent leaves behind
// (`nohup … &`) is reparented to launchd, and a platform binary's
// environment (the launch's marker) is hidden from ps. A launch whose
// processes share a group of their own is still found by that group at
// its end (detector.killAll), before its cache is published. Run from a
// terminal (an interactive launch) the group is left alone: a process
// group that is not the terminal's foreground one is stopped when it
// reads it.
func RunPgroup(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: conductor %s -- COMMAND [ARG...]\n", PgroupCommand)
		return 2
	}
	if !isTerminal(0) {
		_ = unix.Setpgid(0, 0)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor %s: %v\n", PgroupCommand, err)
		return 127
	}
	err = unix.Exec(path, args, os.Environ())
	fmt.Fprintf(os.Stderr, "conductor %s: %s: %v\n", PgroupCommand, path, err)
	return 127
}
