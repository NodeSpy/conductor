//go:build !linux

package controller

import "syscall"

// warmDeathSignal: no parent-death signal here; the warm's own time limit
// bounds it.
func warmDeathSignal(*syscall.SysProcAttr) (unlock func()) { return func() {} }
