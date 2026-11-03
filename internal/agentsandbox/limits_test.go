package agentsandbox

import (
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

func withLimits(t *testing.T, ok bool) {
	t.Helper()
	old := limitsAvailable
	limitsAvailable = func(string) bool { return ok }
	limitsWarned = sync.Once{}
	unit := sandbox.LauncherUnit
	sandbox.LauncherUnit = func() string { return "" }
	t.Cleanup(func() { limitsAvailable = old; limitsWarned = sync.Once{}; sandbox.LauncherUnit = unit })
}

func TestWrapRunsAgentsUnderDefaultLimits(t *testing.T) {
	withPlatform(t, "linux")
	withLimits(t, true)
	// The bus variables' own wrapping is sandbox's TestScopePrefixCarriesTheBusOnlyToSystemdRun.
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	f := newPolicyFixture(t)
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	j := strings.Join(l.Argv, " ")
	want := "systemd-run --user --scope --quiet --collect --slice=" + AgentSlice + " -p MemoryMax=50% -p TasksMax=8192 unshare "
	if !strings.HasPrefix(j, want) {
		t.Fatalf("argv:\n%s\nwant prefix %s", j, want)
	}
	f.pol.Limits = LimitSpec{Memory: "4G", Pids: 100}
	l, _ = Wrap(f.pol, []string{"claude"}, f.deps)
	if j := strings.Join(l.Argv, " "); !strings.Contains(j, "MemoryMax=4G -p TasksMax=100") {
		t.Fatalf("operator's limits: %s", j)
	}
	f.pol.Limits = LimitSpec{Off: true}
	l, _ = Wrap(f.pol, []string{"claude"}, f.deps)
	if l.Argv[0] != "unshare" {
		t.Fatalf("limits off: %q", l.Argv[0])
	}
}

func TestLimitsWithoutAUserManager(t *testing.T) {
	withPlatform(t, "linux")
	withLimits(t, false)
	f := newPolicyFixture(t)
	var events []string
	f.deps.Event = func(k string, _ map[string]any) { events = append(events, k) }
	for i := 0; i < 2; i++ {
		l, err := Wrap(f.pol, []string{"claude"}, f.deps)
		if err != nil || l.Argv[0] != "unshare" {
			t.Fatalf("warn-and-run: %v %v", err, l)
		}
		l.Cleanup()
	}
	if strings.Join(events, ",") != "limits_unavailable" {
		t.Fatalf("warned once: %v", events)
	}
	f.pol.Limits.Required = true
	if _, err := Wrap(f.pol, []string{"claude"}, f.deps); err == nil || !strings.Contains(err.Error(), "sandbox.limits.required") {
		t.Fatalf("required: %v", err)
	}
	// macOS has no cgroups: nothing to require there.
	withPlatform(t, "darwin")
	if _, err := Wrap(f.pol, []string{"claude"}, f.deps); err != nil {
		t.Fatalf("darwin: %v", err)
	}
}

func TestDiskGuard(t *testing.T) {
	withPlatform(t, "linux")
	withLimits(t, false)
	f := newPolicyFixture(t)
	f.pol.Limits = LimitSpec{DiskFreeBytes: 1 << 62}
	if _, err := Wrap(f.pol, []string{"claude"}, f.deps); err == nil || !strings.Contains(err.Error(), "disk guard") {
		t.Fatalf("a launch on a full disk: %v", err)
	}
	f.pol.Limits = LimitSpec{DiskFreeBytes: 1 << 62, DiskOff: true}
	if l, err := Wrap(f.pol, []string{"claude"}, f.deps); err != nil {
		t.Fatalf("guard off: %v", err)
	} else {
		l.Cleanup()
	}
	if (LimitSpec{}).diskMinimum(1000) != 50 || (LimitSpec{DiskFreeFrac: 0.1, DiskFreeBytes: 7}).diskMinimum(1000) != 100 ||
		(LimitSpec{DiskFreeBytes: 500}).diskMinimum(1000) != 500 {
		t.Fatal("minimum")
	}
	// A running launch whose disk fills is stopped — even with detection off.
	var killed []int
	var events []map[string]any
	low := ""
	d := &detector{off: true, run: "r", reported: map[string]bool{},
		list:    func() ([]procInfo, error) { return []procInfo{{pid: 70, ppid: 1, marked: true, start: 1}}, nil },
		signal:  func(pid int, _ uint64) error { killed = append(killed, pid); return nil },
		event:   func(_ string, f map[string]any) { events = append(events, f) },
		diskLow: func() string { return low }}
	d.checkDisk()
	if len(killed) != 0 {
		t.Fatal("killed with room to spare")
	}
	low = "/ws has 1B free"
	d.checkDisk()
	d.checkDisk()
	if len(killed) != 1 || len(events) != 1 || events[0]["kind"] != "disk_low" {
		t.Fatalf("killed %v events %v", killed, events)
	}
	_ = sandbox.Spec{}
}
