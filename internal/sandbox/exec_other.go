//go:build !linux

package sandbox

import "os/exec"

// execChild on non-Linux is a plain exec: namespace masking (and thus the
// nested-userns privilege drop that protects it) is Linux-only — RunEnter only
// carries masks when launched inside a Linux user namespace.
func execChild(argv []string, dropMountPriv bool) *exec.Cmd {
	_ = dropMountPriv
	return exec.Command(argv[0], argv[1:]...)
}
