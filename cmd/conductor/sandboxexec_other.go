//go:build !linux

package main

import "os/exec"

// setParentDeath: no parent-death signal here; the agent's stdio closing
// with this wrapper ends it.
func setParentDeath(*exec.Cmd) {}
