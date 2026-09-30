//go:build linux

package sandbox

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	// The payload sees the daemon's REAL uid/gid, not the root-in-userns the
	// jail was built as: inside `unshare --map-root-user` os.Getuid() is 0,
	// and a tool that checks for root (claude-code refuses
	// --dangerously-skip-permissions as root) or looks itself up with
	// getpwuid must see the operator. Mapping the real uid onto our own
	// (userns-root) uid is the single mapping an unprivileged process may
	// write, and the child still holds no capability over the jail's mount
	// namespace. Outside a remapped namespace (--map-current-user) both
	// sides are simply the current ids.
	uid, gid := os.Getuid(), os.Getgid()
	outerUID, outerGID := uid, gid
	if h, ok := hostID("/proc/self/uid_map", uid); ok {
		uid = h
	}
	if h, ok := hostID("/proc/self/gid_map", gid); ok {
		gid = h
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Unshareflags: syscall.CLONE_NEWUSER,
		UidMappings:  []syscall.SysProcIDMap{{ContainerID: uid, HostID: outerUID, Size: 1}},
		GidMappings:  []syscall.SysProcIDMap{{ContainerID: gid, HostID: outerGID, Size: 1}},
		// GidMappingsEnableSetgroups stays false: writing gid_map for a nested
		// userns unprivileged requires setgroups=deny, which the zero value
		// selects (the runtime writes "deny" before the map).
	}
	return cmd
}

// hostID reads a /proc/self/{uid,gid}_map and returns the id in the PARENT
// namespace that id (in ours) maps to — the daemon's real uid when we are
// root-in-userns. ok=false when the map is the identity or unreadable.
func hostID(mapFile string, id int) (int, bool) {
	f, err := os.Open(mapFile)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 3 {
			continue
		}
		inside, e1 := strconv.Atoi(fs[0])
		outside, e2 := strconv.Atoi(fs[1])
		n, e3 := strconv.Atoi(fs[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if id >= inside && id < inside+n {
			h := outside + (id - inside)
			// The initial namespace's map is the identity over the whole
			// range; only a remap is interesting.
			if h == id {
				return 0, false
			}
			return h, true
		}
	}
	return 0, false
}
