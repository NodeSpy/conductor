package plugin

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// conductorListenPorts lists the TCP ports this conductor process listens
// on: its sockets (/proc/self/fd) in the LISTEN state (/proc/self/net/tcp*).
func conductorListenPorts() (map[int]bool, error) {
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		l, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err != nil {
			continue
		}
		if ino, ok := strings.CutPrefix(l, "socket:["); ok {
			inodes[strings.TrimSuffix(ino, "]")] = true
		}
	}
	ports := map[int]bool{}
	for _, f := range []string{"/proc/self/net/tcp", "/proc/self/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
			fs := strings.Fields(sc.Text())
			if len(fs) < 10 || fs[3] != "0A" || !inodes[fs[9]] {
				continue
			}
			_, hexPort, ok := strings.Cut(fs[1], ":")
			if !ok {
				continue
			}
			if p, err := strconv.ParseInt(hexPort, 16, 32); err == nil {
				ports[int(p)] = true
			}
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return ports, nil
}
