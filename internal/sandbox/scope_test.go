package sandbox

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func TestUnitFromCgroup(t *testing.T) {
	for _, c := range []struct{ cg, want string }{
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/conductor.service\n", "conductor.service"},
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-paseo-4f2.scope\n", "app-paseo-4f2.scope"},
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/conductor.service/sub\n", "conductor.service"},
		// Another uid's manager, the system manager, a session scope outside
		// the user manager, cgroup v1: no unit to bind to.
		{"0::/user.slice/user-1001.slice/user@1001.service/app.slice/x.service\n", ""},
		{"0::/system.slice/conductor.service\n", ""},
		{"0::/user.slice/user-1000.slice/session-3.scope\n", ""},
		{"12:pids:/user.slice\n1:name=systemd:/user.slice/user@1000.service/x.service\n", ""},
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/bad;name.service\n", ""},
	} {
		if got := unitFromCgroup(c.cg, 1000); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cg, got, c.want)
		}
	}
}

// The scope prefix hands systemd-run the launcher's bus variables, strips
// them before the payload, and binds the scope to the launcher's unit.
func TestScopePrefixCarriesTheBusOnlyToSystemdRun(t *testing.T) {
	withBackend(t, "linux")
	old := LauncherUnit
	t.Cleanup(func() { LauncherUnit = old })
	LauncherUnit = func() string { return "conductor.service" }
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	s := FromConfig(&config.IsolationConfig{Mode: "namespace", Limits: &config.IsolationLimits{Pids: 128}})
	argv, err := s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	want := "env XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus systemd-run --user --scope --quiet --collect -p TasksMax=128 -p BindsTo=conductor.service -p After=conductor.service env -u XDG_RUNTIME_DIR -u DBUS_SESSION_BUS_ADDRESS unshare "
	if !strings.HasPrefix(got, want) {
		t.Fatalf("scope prefix:\n got %q\nwant prefix %q", got, want)
	}

	// No bus in the launcher's env, no unit: the bare prefix.
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	LauncherUnit = func() string { return "" }
	argv, _ = s.WrapLocal([]string{"tool"}, "/wt", nil, nil)
	if got := strings.Join(argv, " "); !strings.HasPrefix(got, "systemd-run --user --scope --quiet --collect -p TasksMax=128 unshare ") {
		t.Fatalf("bare scope prefix: %q", got)
	}

	// A remote command never carries this box's bus or unit.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	LauncherUnit = func() string { return "conductor.service" }
	r, err := s.WrapRemote("tool")
	if err != nil || strings.Contains(r, "XDG_RUNTIME_DIR") || strings.Contains(r, "BindsTo") {
		t.Fatalf("remote: %q %v", r, err)
	}
}
