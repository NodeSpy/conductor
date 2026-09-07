//go:build linux

package sandbox

import (
	"golang.org/x/sys/unix"
)

// loopbackUp brings lo up inside the sandbox's fresh network namespace. The
// process owns the netns through its user namespace, so plain SIOCSIFFLAGS
// works unprivileged. Idempotent: an already-up lo (container mode) is left
// alone.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	flags := ifr.Uint16()
	if flags&unix.IFF_UP != 0 {
		return nil
	}
	ifr.SetUint16(flags | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}
