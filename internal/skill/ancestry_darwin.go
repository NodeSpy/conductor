//go:build darwin

package skill

import "golang.org/x/sys/unix"

// procAncestor reads pid's parent pid and its own start time via
// sysctl(KERN_PROC_PID) (unix.SysctlKinfoProc) — the macOS counterpart to
// the Linux /proc/<pid>/stat read. Start time has second granularity here
// (the kernel's p_starttime timeval, combined with its microseconds for a
// little extra precision); still enough to catch a reused pid landing on a
// different process a second or more apart, and BindRoot/processInTree
// already treat a zero start time as "unverifiable" rather than a match.
// ok=false when pid is not a live process or the sysctl fails.
func procAncestor(pid int) (ppid int, start uint64, ok bool) {
	if pid <= 0 {
		return 0, 0, false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0, 0, false
	}
	st := uint64(kp.Proc.P_starttime.Sec)*1e6 + uint64(kp.Proc.P_starttime.Usec)
	return int(kp.Eproc.Ppid), st, true
}
