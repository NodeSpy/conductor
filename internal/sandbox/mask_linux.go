//go:build linux

package sandbox

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// maskPath hides one daemon path inside this mount namespace (#36 iso-review
// H7): a directory is overmounted with an empty read-only tmpfs, a file with
// a bind of /dev/null. The mount namespace was created private (unshare), so
// nothing leaks to the host — and user-namespace root is enough to mount
// tmpfs/binds. A path that doesn't exist is already hidden.
func maskPath(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := unix.Mount("none", path, "tmpfs", unix.MS_RDONLY, "size=4k,mode=0500"); err != nil {
			return fmt.Errorf("tmpfs over dir: %w", err)
		}
		return nil
	}
	if err := unix.Mount("/dev/null", path, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind /dev/null over file: %w", err)
	}
	return nil
}
