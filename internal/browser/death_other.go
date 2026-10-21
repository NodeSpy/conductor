//go:build !linux

package browser

import "os/exec"

// setDeathSignal: no parent-death signal here; the browser exits when its
// protocol pipe closes with conductor.
func setDeathSignal(*exec.Cmd) {}
