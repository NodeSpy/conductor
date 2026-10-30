//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// setParentDeath kills the sandbox if this wrapper dies first (paseo killed
// it): an agent never outlives the process that answers for it.
func setParentDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
