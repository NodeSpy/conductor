package agentsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// usageHelperEnv makes the test binary a launch of known size: "<MiB>
// <children>" allocates and touches that much memory, starts that many
// sleeping children and waits to be killed.
const usageHelperEnv = "AGENTSANDBOX_USAGE_HELPER"

func usageHelper(spec string) {
	var mib, kids int
	fmt.Sscan(spec, &mib, &kids)
	buf := make([]byte, mib<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	for i := 0; i < kids; i++ {
		_ = exec.Command("sleep", "60").Start()
	}
	fmt.Println("ready")
	time.Sleep(60 * time.Second)
	_ = buf[len(buf)-1]
}

// fakeScope is a cgroup directory holding the given files.
func fakeScope(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScopeCgroupIsTheLaunchsOwnUnit(t *testing.T) {
	old := cgroupRoot
	cgroupRoot = "/cg"
	t.Cleanup(func() { cgroupRoot = old })
	unit := "conductor-agent-0011223344556677.scope"
	base := "/user.slice/user-1000.slice/user@1000.service/conductor.slice/" + AgentSlice + "/"
	for _, c := range []struct{ cg, unit, want string }{
		{"0::" + base + unit + "\n", unit, "/cg" + base + unit},
		{"12:pids:/x\n0::" + base + unit + "\n", unit, "/cg" + base + unit},
		{"0::" + base + "other.scope\n", unit, ""},                        // another launch's
		{"0::/user.slice/app.slice/" + unit + "\n", unit, ""},             // not under the agent slice
		{"0::" + base + "../" + AgentSlice + "/" + unit + "\n", unit, ""}, // not a clean path
		{"0::" + base + unit + "\n", "", ""},                              // no unit: nothing read
	} {
		if got := scopeCgroupPath(c.cg, c.unit); got != c.want {
			t.Errorf("%q: %q, want %q", c.cg, got, c.want)
		}
	}
}

// The scope's peaks, caps and events become the usage event; a launch at
// 80% of a cap is flagged, and the cap that killed or refused it is named.
func TestUsageReadsPeaksAgainstCaps(t *testing.T) {
	gib := uint64(1 << 30)
	u := &launchUsage{unit: "u.scope", cgroup: fakeScope(t, map[string]string{
		"memory.peak": fmt.Sprint(gib * 9 / 10), "memory.max": fmt.Sprint(gib),
		"pids.peak": "12", "pids.max": "8192",
		"memory.events": "low 0\nhigh 0\nmax 40\noom 1\noom_kill 1\n", "pids.events": "max 0\n",
	})}
	u.sampleCgroup(nil)
	f, warn, limits := u.report("r1", "workflow:review/review-team/review[0]")
	if f["memory_peak"] != gib*9/10 || f["memory_max"] != gib || f["pids_peak"] != uint64(12) || f["pids_max"] != uint64(8192) || f["source"] != "cgroup" || f["agent"] != "workflow:review/review-team/review[0]" {
		t.Fatalf("fields: %v", f)
	}
	if !strings.Contains(warn, "memory 921.6MiB of 1.0GiB (90%)") || strings.Contains(warn, "tasks") || f["near_cap"] != true {
		t.Fatalf("warning: %q", warn)
	}
	if f["killed_by"] != "memory" || len(limits) != 1 || limits[0]["kind"] != "memory_cap" || limits[0]["action"] != "killed" {
		t.Fatalf("memory cap kill: %v %v", f, limits)
	}

	// The scope gone at the end: what was sampled stands; the unit's
	// result names an oom-kill the sampling missed.
	u = &launchUsage{unit: "u.scope", cgroup: fakeScope(t, map[string]string{
		"memory.current": "1048576", "memory.max": "max", "pids.current": "7400", "pids.max": "8192", "pids.events": "max 3\n",
	})}
	u.sampleCgroup(nil)
	os.RemoveAll(u.cgroup)
	u.sampleCgroup(nil)
	u.result = "oom-kill"
	f, warn, limits = u.report("r2", "")
	if f["memory_peak"] != uint64(1048576) || f["memory_max"] != nil || f["pids_peak"] != uint64(7400) {
		t.Fatalf("fields: %v", f)
	}
	if !strings.Contains(warn, "tasks 7400 of 8192 (90%)") || strings.Contains(warn, "memory") {
		t.Fatalf("warning: %q", warn)
	}
	kinds := []string{}
	for _, l := range limits {
		kinds = append(kinds, l["kind"].(string)+"/"+l["action"].(string))
	}
	if strings.Join(kinds, ",") != "memory_cap/killed,pids_cap/refused" || f["pids_refused"] != uint64(3) {
		t.Fatalf("limits: %v", limits)
	}

	// Well under its caps: no warning, no limit.
	u = &launchUsage{unit: "u.scope", cgroup: fakeScope(t, map[string]string{
		"memory.peak": "1000", "memory.max": "100000", "pids.peak": "3", "pids.max": "8192"})}
	u.sampleCgroup(nil)
	if f, warn, limits := u.report("r3", ""); warn != "" || f["near_cap"] != nil || len(limits) != 0 {
		t.Fatalf("quiet launch: %v %q %v", f, warn, limits)
	}
	// Nothing seen: no event.
	if f, _, _ := (&launchUsage{}).report("r4", ""); f != nil {
		t.Fatalf("unseen launch: %v", f)
	}
	// Killed by the memory cap before its cgroup was first read: systemd's
	// result says so, with the cap the scope was given.
	u = &launchUsage{unit: "u.scope", capMemory: "300M", result: "oom-kill"}
	f, _, limits = u.report("r5", "")
	if f == nil || f["source"] != "systemd" || f["killed_by"] != "memory" || len(limits) != 1 || !strings.Contains(limits[0]["reason"].(string), "the memory cap (300M) killed the launch") {
		t.Fatalf("an early kill: %v %v", f, limits)
	}
}
