package plugin

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// conductorListenPorts lists the TCP ports this conductor process listens
// on (lsof, which macOS ships).
func conductorListenPorts() (map[int]bool, error) {
	out, err := exec.Command("/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(os.Getpid()), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	ports := map[int]bool{}
	if err != nil {
		// lsof exits 1 when nothing matched.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) == 0 {
			return ports, nil
		}
		return nil, err
	}
	for _, l := range strings.Split(string(out), "\n") {
		n, ok := strings.CutPrefix(l, "n")
		if !ok {
			continue
		}
		if i := strings.LastIndex(n, ":"); i >= 0 {
			if p, err := strconv.Atoi(n[i+1:]); err == nil {
				ports[p] = true
			}
		}
	}
	return ports, nil
}
