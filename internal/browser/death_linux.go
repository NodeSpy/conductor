//go:build linux

package browser

import (
	"os/exec"
	"syscall"
)

// setDeathSignal kills the browser's sandbox if conductor dies first: a
// browser never outlives the process that answers for it.
func setDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
