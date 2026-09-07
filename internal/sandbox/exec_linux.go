//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// execChild builds the command that runs the sandboxed payload. When masks are
// in force (dropMountPriv), the payload is re-exec'd into a nested, unprivileged
// user namespace via clone(CLONE_NEWUSER) with an identity uid/gid mapping (the
// only mapping the daemon's own uid may write without privilege). The child then
// holds capabilities ONLY over that new user namespace — none over the outer
// user namespace that owns the mount namespace where the masks live — so a
// `umount`/`MNT_DETACH` of a mask is refused (EPERM), and mounts inherited into
// the less-privileged namespace are kernel-locked, so it cannot make a fresh
// mount namespace and umount there either. The network namespace is untouched,
// so the in-sandbox forwarder's loopback address stays reachable.
//
// Without masks (privileged: true, or a namespace launch with nothing to hide)
// there is nothing to protect, and container-mode payloads run RunEnter inside
// an engine whose seccomp profile commonly blocks unprivileged clone/unshare —
// so the hop is applied only when masks are present.
func execChild(argv []string, dropMountPriv bool) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	if !dropMountPriv {
		return cmd
	}
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Unshareflags: syscall.CLONE_NEWUSER,
		UidMappings:  []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:  []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		// GidMappingsEnableSetgroups stays false: writing gid_map for a nested
		// userns unprivileged requires setgroups=deny, which the zero value
		// selects (the runtime writes "deny" before the map).
	}
	return cmd
}
