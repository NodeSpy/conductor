package agentsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How close a launch came to its caps. A capped Linux launch runs in a
// scope of its own (a named unit): its cgroup's memory.peak and pids.peak
// are read every second while it runs, with the caps (memory.max,
// pids.max) and what the caps did (memory.events oom_kill, pids.events
// max). Where there is no such scope (macOS, no systemd user manager) the
// watcher's own view stands in: the sum of the launch's resident memory and
// tasks at each scan — a sampled maximum, with no cap to compare against.
// At the launch's end one `usage` event carries the peaks; a launch at or
// above usageWarnFrac of a cap is flagged, and one a cap stopped is a
// `limit` event naming the cap.

// usageWarnFrac is the share of a cap from which a launch is flagged.
const usageWarnFrac = 0.8

// usageSampleInterval is how often a scope's cgroup is read.
const usageSampleInterval = time.Second

// cgroupRoot is where the unified cgroup hierarchy is mounted (a var for
// tests).
var cgroupRoot = "/sys/fs/cgroup"

// scopeUnitName is a fresh name for a launch's scope unit.
func scopeUnitName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "conductor-agent-" + hex.EncodeToString(b) + ".scope"
}

// launchUsage is one launch's peaks against its caps.
type launchUsage struct {
	mu sync.Mutex
	// unit is the launch's scope unit ("": none); cgroup its directory,
	// once found.
	unit, cgroup string
	// source: "cgroup" once the scope's files were read, else "processes"
	// once a scan summed the launch's processes ("": nothing seen).
	source                             string
	memPeak, memMax, pidsPeak, pidsMax uint64
	oomKills, pidsRefused              uint64
	// result is the scope unit's systemd result at the end ("oom-kill":
	// the memory cap stopped it).
	result string
}

// sampleCgroup reads the scope's cgroup, found from the launch's root
// processes the first time: only a cgroup that is this launch's own unit
// under the agent slice is read.
func (u *launchUsage) sampleCgroup(pids []int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.unit == "" {
		return
	}
	if u.cgroup == "" {
		for _, pid := range pids {
			if dir := scopeCgroup(pid, u.unit); dir != "" {
				u.cgroup = dir
				break
			}
		}
		if u.cgroup == "" {
			return
		}
	}
	peak, ok := cgroupValue(u.cgroup, "memory.peak")
	if !ok {
		// Before Linux 5.19: the current usage, sampled.
		peak, ok = cgroupValue(u.cgroup, "memory.current")
	}
	if !ok {
		return // the scope is gone
	}
	u.source = "cgroup"
	u.memPeak = max(u.memPeak, peak)
	if pp, ok := cgroupValue(u.cgroup, "pids.peak"); ok {
		u.pidsPeak = max(u.pidsPeak, pp)
	} else if pc, ok := cgroupValue(u.cgroup, "pids.current"); ok {
		u.pidsPeak = max(u.pidsPeak, pc)
	}
	if m, ok := cgroupValue(u.cgroup, "memory.max"); ok {
		u.memMax = m
	}
	if m, ok := cgroupValue(u.cgroup, "pids.max"); ok {
		u.pidsMax = m
	}
	u.oomKills = max(u.oomKills, cgroupKey(u.cgroup, "memory.events", "oom_kill"))
	u.pidsRefused = max(u.pidsRefused, cgroupKey(u.cgroup, "pids.events", "max"))
}

// sampleProcs folds one scan of the launch's processes in, where no
// cgroup is read.
func (u *launchUsage) sampleProcs(procs []procInfo) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.source == "cgroup" || len(procs) == 0 {
		return
	}
	var rss, tasks uint64
	for _, p := range procs {
		rss += p.rss
		t := uint64(p.threads)
		if t == 0 {
			t = 1
		}
		tasks += t
	}
	u.source = "processes"
	u.memPeak = max(u.memPeak, rss)
	u.pidsPeak = max(u.pidsPeak, tasks)
}

// report is the launch's usage event and what its end says about the
// caps: a warning when a cap was nearly reached, and the cap that stopped
// or refused it.
func (u *launchUsage) report(run, identity string) (fields map[string]any, warning string, limits []map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.source == "" {
		return nil, "", nil
	}
	f := map[string]any{"run": run, "memory_peak": u.memPeak, "pids_peak": u.pidsPeak, "source": u.source}
	if identity != "" {
		f["agent"] = identity
	}
	if u.memMax > 0 {
		f["memory_max"] = u.memMax
	}
	if u.pidsMax > 0 {
		f["pids_max"] = u.pidsMax
	}
	var near []string
	if u.memMax > 0 && float64(u.memPeak) >= usageWarnFrac*float64(u.memMax) {
		near = append(near, fmt.Sprintf("memory %s of %s (%.0f%%)", humanBytes(u.memPeak), humanBytes(u.memMax), 100*float64(u.memPeak)/float64(u.memMax)))
	}
	if u.pidsMax > 0 && float64(u.pidsPeak) >= usageWarnFrac*float64(u.pidsMax) {
		near = append(near, fmt.Sprintf("tasks %d of %d (%.0f%%)", u.pidsPeak, u.pidsMax, 100*float64(u.pidsPeak)/float64(u.pidsMax)))
	}
	if len(near) > 0 {
		f["near_cap"] = true
		warning = fmt.Sprintf("launch used %s: raise sandbox.limits for this scope before the cap bites", strings.Join(near, ", "))
		f["warning"] = warning
	}
	if u.oomKills > 0 || u.result == "oom-kill" {
		f["killed_by"] = "memory"
		limits = append(limits, map[string]any{"run": run, "kind": "memory_cap", "action": "killed",
			"reason": fmt.Sprintf("the memory cap (%s) killed the launch: peak %s (sandbox.limits.memory)", humanBytes(u.memMax), humanBytes(u.memPeak))})
	}
	if u.pidsRefused > 0 {
		f["pids_refused"] = u.pidsRefused
		limits = append(limits, map[string]any{"run": run, "kind": "pids_cap", "action": "refused",
			"reason": fmt.Sprintf("the task cap (%d) refused %d forks (sandbox.limits.pids)", u.pidsMax, u.pidsRefused)})
	}
	return f, warning, limits
}

// scopeCgroup is pid's cgroup directory when it is the unit's scope below
// the agent slice ("" otherwise).
func scopeCgroup(pid int, unit string) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	return scopeCgroupPath(string(b), unit)
}

// scopeCgroupPath reads a /proc/<pid>/cgroup: the unified hierarchy's
// path, when its last element is the unit and it lies below the agent
// slice.
func scopeCgroupPath(cg, unit string) string {
	for _, line := range strings.Split(cg, "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		if unit == "" || path != filepath.Clean(path) || filepath.Base(path) != unit || !strings.Contains(path, "/"+AgentSlice+"/") {
			return ""
		}
		return filepath.Join(cgroupRoot, path)
	}
	return ""
}

// cgroupValue reads a one-number cgroup file ("max" is 0: no cap).
func cgroupValue(dir, file string) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, true
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// cgroupKey reads one key of a flat-keyed cgroup file (memory.events).
func cgroupKey(dir, file, key string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+" "); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// scopeResult waits for the launch's scope unit to leave its active
// states and returns its systemd result ("oom-kill": the memory cap stopped
// it); a failed unit is reset so it does not linger (the scope runs without
// --collect so this result can be read). A var for tests.
var scopeResult = func(unit string) string {
	var active, result string
	for i := 0; i < 25; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "-p", "ActiveState", "-p", "Result", "--", unit).Output()
		cancel()
		if err != nil {
			return ""
		}
		active, result = "", ""
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "ActiveState="); ok {
				active = v
			} else if v, ok := strings.CutPrefix(line, "Result="); ok {
				result = v
			}
		}
		if active != "active" && active != "activating" && active != "deactivating" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if active == "failed" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exec.CommandContext(ctx, "systemctl", "--user", "reset-failed", "--", unit).Run()
		cancel()
	}
	return result
}
