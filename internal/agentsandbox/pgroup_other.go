//go:build !linux && !darwin

package agentsandbox

import (
	"fmt"
	"os"
)

// PgroupCommand is conductor's hidden subcommand that starts a launch in
// a process group of its own (RunPgroup).
const PgroupCommand = "sandbox-pgroup"

// RunPgroup: no process groups to make here.
func RunPgroup(args []string) int {
	fmt.Fprintf(os.Stderr, "conductor %s: not supported on this platform\n", PgroupCommand)
	return 2
}
