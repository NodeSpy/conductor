//go:build linux || darwin

package keycache

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// stripXattrs removes every extended attribute p (never followed) carries
// that this process may remove. A published copy holds plain files: an
// attribute an agent set (a user.* note, a macOS one a clone carried over)
// is neither screened nor wanted.
func stripXattrs(p string) {
	n, err := unix.Llistxattr(p, nil)
	if err != nil || n <= 0 {
		return
	}
	buf := make([]byte, n)
	if n, err = unix.Llistxattr(p, buf); err != nil {
		return
	}
	for _, name := range bytes.Split(buf[:n], []byte{0}) {
		if len(name) > 0 {
			_ = unix.Lremovexattr(p, string(name))
		}
	}
}
