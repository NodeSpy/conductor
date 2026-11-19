//go:build linux

package keycache

import "golang.org/x/sys/unix"

// syncDir flushes the filesystem dir is on: an overlay's upper layer is
// mounted volatile (never synced), so a version built from one is synced
// once it is published.
// syncFS is syncDir (tests replace it: a busy filesystem takes seconds).
var syncFS = syncDir

func syncDir(dir string) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	_ = unix.Syncfs(fd)
	unix.Close(fd)
}
