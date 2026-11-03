package sandbox

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A local launch's cgroup limits come from `systemd-run --user --scope`,
// which registers the scope over the user manager's D-Bus and then execs the
// payload itself: the payload inherits systemd-run's environment. The launch
// environment is scrubbed (no XDG_RUNTIME_DIR or DBUS_SESSION_BUS_ADDRESS:
// the bus is a way out of the sandbox), so systemd-run alone could not reach
// the bus. The prefix therefore gives systemd-run the launcher's own bus
// variables and removes them again before the payload starts:
//
//	env XDG_RUNTIME_DIR=… DBUS_SESSION_BUS_ADDRESS=… systemd-run … env -u XDG_RUNTIME_DIR -u DBUS_SESSION_BUS_ADDRESS <payload>
//
// A scope is a unit of its own, outside the launcher's cgroup: stopping the
// launcher's unit (a conductor restart or upgrade, a paseo restart) would no
// longer take its agents with it. Each scope is therefore bound to the unit
// the launcher runs in (BindsTo= and After=), so it is stopped — every
// process in it, setsid'd ones included — when that unit stops.

// busVars are the variables systemd-run needs to reach the user manager.
var busVars = []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

// launcherBusEnv is the launcher's own bus variables (KEY=value).
func launcherBusEnv() []string {
	var out []string
	for _, k := range busVars {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// LauncherUnit is the user-manager unit this process runs in (a .service or
// .scope under user@<uid>.service), or "" when it runs elsewhere (a system
// service, a container, no cgroup v2). A var for tests.
var LauncherUnit = sync.OnceValue(func() string { return unitFromCgroup(readSelfCgroup(), os.Getuid()) })

func readSelfCgroup() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return string(b)
}

// unitFromCgroup reads the unified hierarchy's line of /proc/self/cgroup:
// the innermost .service or .scope below user@<uid>.service.
func unitFromCgroup(cg string, uid int) string {
	mgr := "/user@" + strconv.Itoa(uid) + ".service/"
	sc := bufio.NewScanner(strings.NewReader(cg))
	for sc.Scan() {
		path, ok := strings.CutPrefix(sc.Text(), "0::")
		if !ok {
			continue
		}
		i := strings.Index(path, mgr)
		if i < 0 {
			return ""
		}
		parts := strings.Split(strings.Trim(path[i+len(mgr):], "/"), "/")
		for j := len(parts) - 1; j >= 0; j-- {
			p := parts[j]
			if validUnitName(p) && (strings.HasSuffix(p, ".service") || strings.HasSuffix(p, ".scope")) {
				return p
			}
		}
		return ""
	}
	return ""
}

// validUnitName accepts the characters systemd allows in a unit name.
func validUnitName(n string) bool {
	if n == "" || len(n) > 255 || n[0] == '-' {
		return false
	}
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(":-_.\\@", r):
		default:
			return false
		}
	}
	return true
}

// localScopePrefix is systemdPrefix for a launch on this box: the bus
// variables for systemd-run only, and the scope bound to the launcher's unit.
func (s *Spec) localScopePrefix() []string {
	base := s.systemdPrefix()
	if base == nil {
		return nil
	}
	if u := LauncherUnit(); u != "" {
		base = append(base, "-p", "BindsTo="+u, "-p", "After="+u)
	}
	env := launcherBusEnv()
	if len(env) == 0 {
		return base
	}
	out := append(append([]string{"env"}, env...), base...)
	out = append(out, "env")
	for _, k := range busVars {
		out = append(out, "-u", k)
	}
	return out
}

// ScopeAvailable reports whether a launch's scope prefix works here: it
// runs that very prefix (bus variables, binding) around `true`, with an
// environment as bare as a launch's. Cached for the process.
var ScopeAvailable = sync.OnceValue(func() bool {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	argv := append((&Spec{Pids: 64}).localScopePrefix(), "true")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	return cmd.Run() == nil
})
