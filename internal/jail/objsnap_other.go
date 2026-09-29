//go:build !unix

package jail

import "fmt"

// snapshotObjects needs openat/O_NOFOLLOW; the jail has no backend here.
func snapshotObjects(ws, dst string) error {
	return fmt.Errorf("snapshot: unsupported on this platform")
}
