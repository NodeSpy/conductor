//go:build linux

package agentsandbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// listProcs reads /proc: every process's parent, start time and marker,
// and — for the launch's own processes — its argv, binary and descriptors.
func listProcs(marker string) func() ([]procInfo, error) {
	needle := []byte("\x00" + markerEnv + "=" + marker + "\x00")
	return func() ([]procInfo, error) {
		ents, err := os.ReadDir("/proc")
		if err != nil {
			return nil, err
		}
		uid := os.Getuid()
		var all []procInfo
		for _, e := range ents {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			p, ok := procStat(pid)
			if !ok {
				continue
			}
			if fi, err := os.Stat("/proc/" + e.Name()); err == nil {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid {
					// Only this user's processes can carry the marker (a
					// launch's run as the operator, mapped).
					if env, err := os.ReadFile("/proc/" + e.Name() + "/environ"); err == nil {
						p.marked = bytes.Contains(append(append([]byte{0}, env...), 0), needle)
					}
				}
			}
			all = append(all, p)
		}
		launch := map[int]bool{}
		for _, p := range launchProcs(all) {
			launch[p.pid] = true
		}
		for i := range all {
			if launch[all[i].pid] {
				enrich(&all[i])
			}
		}
		return all, nil
	}
}

// procStat parses /proc/<pid>/stat: comm, ppid and start time.
func procStat(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	open, close := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || close < open {
		return procInfo{}, false
	}
	f := strings.Fields(string(b[close+1:]))
	// f[0] is the state (field 3): ppid is field 4, starttime field 22.
	if len(f) < 20 {
		return procInfo{}, false
	}
	ppid, _ := strconv.Atoi(f[1])
	start, _ := strconv.ParseUint(f[19], 10, 64)
	return procInfo{pid: pid, ppid: ppid, start: start, comm: string(b[open+1 : close])}, true
}

// enrich reads a launch process's argv, binary and descriptors.
func enrich(p *procInfo) {
	dir := "/proc/" + strconv.Itoa(p.pid)
	if b, err := os.ReadFile(dir + "/cmdline"); err == nil {
		p.args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	}
	p.exe, _ = os.Readlink(dir + "/exe")
	inet := inetInodes(dir)
	fds, err := os.ReadDir(dir + "/fd")
	if err != nil {
		return
	}
	for _, fd := range fds {
		n, err := strconv.Atoi(fd.Name())
		if err != nil {
			continue
		}
		link, err := os.Readlink(dir + "/fd/" + fd.Name())
		if err != nil {
			continue
		}
		kind := "other"
		switch {
		case strings.HasPrefix(link, "socket:["):
			kind = "unix"
			if inet[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
				kind = "inet"
				p.inetSocket = true
			}
		case link == "/dev/ptmx" || link == "/dev/pts/ptmx":
			p.ptyMaster = true
		}
		if n < 3 {
			p.stdio[n] = kind
		}
	}
}

// inetInodes are the socket inodes of the process's network namespace's
// TCP and UDP sockets.
func inetInodes(dir string) map[string]bool {
	out := map[string]bool{}
	for _, t := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile(filepath.Join(dir, "net", t))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 10 {
				continue
			}
			out[f[9]] = true
		}
	}
	return out
}

// signalProc SIGKILLs pid through a pidfd, after checking it is still the
// process the watcher saw (its start time).
func signalProc(pid int, start uint64) error {
	if !safeToSignal(pid) {
		return errors.New("refusing to signal")
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if p, ok := procStat(pid); !ok || p.start != start {
		return errors.New("not the process seen")
	}
	return unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
}

// safeToSignal: never pid 0 or 1 (a process group, everything, init),
// never this process or its parent.
func safeToSignal(pid int) bool {
	return pid > 1 && pid != os.Getpid() && pid != os.Getppid()
}

func init() {
	fifoReaders = func(path string) string {
		ents, err := os.ReadDir("/proc")
		if err != nil {
			return ""
		}
		var who []string
		for _, e := range ents {
			if pid, err := strconv.Atoi(e.Name()); err != nil || pid == os.Getpid() {
				continue // the watcher holds the write end
			}
			fds, err := os.ReadDir("/proc/" + e.Name() + "/fd")
			if err != nil {
				continue
			}
			for _, fd := range fds {
				if l, err := os.Readlink("/proc/" + e.Name() + "/fd/" + fd.Name()); err == nil && l == path {
					comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
					who = append(who, e.Name()+" "+strings.TrimSpace(string(comm)))
					break
				}
			}
		}
		if len(who) == 0 {
			return ""
		}
		return " by pid " + strings.Join(who, ", ")
	}
}
