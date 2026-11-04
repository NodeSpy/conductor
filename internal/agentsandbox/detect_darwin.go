//go:build darwin

package agentsandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// listProcs asks ps: every process's parent and marker (its environment,
// -E), then the launch's own processes' argv and binary; lsof tells a
// shell's stdio. macOS has no per-process view of a pty master, so the
// pty-relay rule is Linux's only.
func listProcs(marker string, isRoot func(pid int) bool) func() ([]procInfo, error) {
	needle := markerEnv + "=" + marker
	return func() ([]procInfo, error) {
		out, err := exec.Command("ps", "-Axww", "-E", "-o", "pid=,ppid=,pgid=,command=").Output()
		if err != nil {
			return nil, err
		}
		var all []procInfo
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 3 {
				continue
			}
			pid, err1 := strconv.Atoi(f[0])
			ppid, err2 := strconv.Atoi(f[1])
			pgid, err3 := strconv.Atoi(f[2])
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			all = append(all, procInfo{pid: pid, ppid: ppid, pgid: pgid, marked: strings.Contains(line, needle) || isRoot(pid)})
		}
		launch := launchProcs(all)
		if len(launch) == 0 {
			return all, nil
		}
		pids := make([]string, 0, len(launch))
		for _, p := range launch {
			pids = append(pids, strconv.Itoa(p.pid))
		}
		args := psColumn(pids, "command=")
		comm := psColumn(pids, "comm=")
		rss := psColumn(pids, "rss=")
		inet := lsofInet(pids)
		exes := lsofExe(pids)
		for i := range all {
			p := &all[i]
			a, ok := args[p.pid]
			if !ok {
				continue
			}
			p.args = strings.Fields(a)
			p.exe, p.comm = comm[p.pid], comm[p.pid]
			p.start, _ = procStart(p.pid)
			if kb, err := strconv.ParseUint(strings.TrimSpace(rss[p.pid]), 10, 64); err == nil {
				p.rss = kb << 10
			}
			// ps truncates comm: the binary's own path is lsof's.
			if e := exes[p.pid]; filepath.IsAbs(e) {
				p.exe = e
				p.id, _ = hostBinaries().identify(e)
			}
			for _, fd := range inet[p.pid] {
				p.inetSocket = true
				p.inetFDs = append(p.inetFDs, fd)
				if fd < 3 {
					p.stdio[fd] = "inet"
				}
			}
		}
		return all, nil
	}
}

// psColumn reads one ps column for pids.
func psColumn(pids []string, col string) map[int]string {
	out := map[int]string{}
	b, err := exec.Command("ps", "-ww", "-o", "pid="+","+col, "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			continue
		}
		if pid, err := strconv.Atoi(line[:i]); err == nil {
			out[pid] = strings.TrimSpace(line[i:])
		}
	}
	return out
}

// lsofExe is each of pids' executable: its first txt file, in one lsof.
func lsofExe(pids []string) map[int]string {
	out := map[int]string{}
	b, _ := exec.Command("lsof", "-n", "-P", "-a", "-p", strings.Join(pids, ","), "-d", "txt", "-F", "pn").Output()
	pid := -1
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			if _, ok := out[pid]; !ok {
				out[pid] = line[1:]
			}
		}
	}
	return out
}

// lsofInet lists the fds holding an IP socket of each of pids, in one lsof.
func lsofInet(pids []string) map[int][]int {
	out := map[int][]int{}
	b, _ := exec.Command("lsof", "-n", "-P", "-a", "-p", strings.Join(pids, ","), "-i", "-F", "pf").Output()
	pid := -1
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "f") && pid > 0:
			if fd, err := strconv.Atoi(line[1:]); err == nil {
				out[pid] = append(out[pid], fd)
			}
		}
	}
	return out
}

// signalProc SIGKILLs pid after checking it is still the process the
// watcher saw (its start time). macOS has no pidfd: the window for a
// reused pid is between that check and the kill.
func signalProc(pid int, start uint64) error {
	if !safeToSignal(pid) {
		return errors.New("refusing to signal")
	}
	if st, ok := procStart(pid); !ok || start == 0 || st != start {
		return errors.New("not the process seen")
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// procStart is pid's start time (microseconds since the epoch).
func procStart(pid int) (uint64, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k.Proc.P_pid != int32(pid) {
		return 0, false
	}
	t := k.Proc.P_starttime
	return uint64(t.Sec)*1e6 + uint64(t.Usec), true
}

// ownPgid is this process's group: never one of a launch's.
func ownPgid() int { return syscall.Getpgrp() }

// safeToSignal: never pid 0 or 1, never this process or its parent.
func safeToSignal(pid int) bool {
	return pid > 1 && pid != os.Getpid() && pid != os.Getppid()
}
