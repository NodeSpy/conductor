//go:build linux

package skill

import (
	"os"
	"strconv"
	"strings"
)

// procAncestor reads /proc/<pid>/stat for pid's own start time (field 22,
// clock ticks since boot) and its parent pid (field 4) — the two primitives
// BoundRoot's ancestry walk needs. ok=false when pid is not a live process
// (it exited, or never existed) or /proc cannot be read.
//
// Mirrors internal/memory/peer_linux.go's procStartTime (same hardened
// parse: fields are read from AFTER the last ')', so a process name
// containing ")123 456" cannot shift field indices) rather than importing
// it — internal/skill stays free of internal/memory's own dependency
// surface, the same reason skill.Peer duplicates memory.Peer instead of
// sharing it.
func procAncestor(pid int) (ppid int, start uint64, ok bool) {
	if pid <= 0 {
		return 0, 0, false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is stat field 3 (state); field 4 (ppid) is fields[1]; field
	// 22 (starttime) is fields[19].
	if len(fields) < 20 {
		return 0, 0, false
	}
	pp, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	st, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return pp, st, true
}
