package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// HostJail is whether this host can jail plugins at all, and if not, why
// and what fixes it. A plugin jail needs a non-root daemon and unprivileged
// user namespaces that may mount (Linux), or sandbox-exec (macOS). Inside a
// default Docker container, as root, or on a kernel or AppArmor policy that
// restricts user namespaces, it cannot be built, whatever a plugin declares.
type HostJail struct {
	OK     bool
	Reason string // why not, in plain words ("" when OK)
	Fix    string // what makes it possible ("" when OK)
}

// Refusal is the error a plugin installed after the jail gets on a host
// that cannot jail it.
func (h HostJail) Refusal(name string) error {
	return fmt.Errorf("plugin %s: cannot jail it on this host: %s. It was installed after the plugin jail, so it does not run unjailed by default — %s; or set trust: full on its entry to run it unjailed", name, h.Reason, h.Fix)
}

// Warning is what the log and the notification say on a host that cannot
// jail plugins: the ones installed before the jail run as they did then.
func (h HostJail) Warning() string {
	return fmt.Sprintf("WARNING: this host cannot jail plugins: %s. Plugins installed before the plugin jail keep running UNJAILED, as they did before it; a plugin installed since is refused. To jail them: %s. To run a plugin unjailed on purpose (and silence this), set trust: full on its entry", h.Reason, h.Fix)
}

var hostJail struct {
	sync.Mutex
	probe func() HostJail
	done  bool
	h     HostJail
}

// SetHostJailProbe installs the probe HostJailStatus runs, once, on first
// use. Without one (library callers, tests) the host is taken to be able
// to jail plugins, which is what every launch assumed before the probe.
func SetHostJailProbe(probe func() HostJail) {
	hostJail.Lock()
	defer hostJail.Unlock()
	hostJail.probe, hostJail.done = probe, false
}

// SetHostJailForTests pins the answer.
func SetHostJailForTests(h HostJail) (restore func()) {
	hostJail.Lock()
	probe, done, prev := hostJail.probe, hostJail.done, hostJail.h
	hostJail.probe, hostJail.done, hostJail.h = nil, true, h
	hostJail.Unlock()
	return func() {
		hostJail.Lock()
		hostJail.probe, hostJail.done, hostJail.h = probe, done, prev
		hostJail.Unlock()
	}
}

// HostJailStatus is this host's answer (probed once).
func HostJailStatus() HostJail {
	hostJail.Lock()
	defer hostJail.Unlock()
	if !hostJail.done {
		hostJail.h = HostJail{OK: true}
		if hostJail.probe != nil {
			hostJail.h = hostJail.probe()
		}
		hostJail.done = true
	}
	return hostJail.h
}

// probeEnv are the probe's inputs, replaceable in tests.
type probeEnv struct {
	goos     string
	euid     int
	lookPath func(string) (string, error)
	// run runs argv and returns its combined output.
	run      func(argv []string) ([]byte, error)
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}

// ProbeHostJail builds a real jail, the way a plugin launch does, around a
// no-op: conductor's own binary (self) as `sandbox-net --noop`. It answers
// exactly "can a plugin be jailed here", and when not, names the cause.
func ProbeHostJail(self string) HostJail {
	return probeHostJail(self, probeEnv{
		goos:     spawnGOOS,
		euid:     os.Geteuid(),
		lookPath: spawnLookPath,
		run: func(argv []string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // conductor's own binary and fixed wrapper args
			c.Env = sandbox.MinimalEnv()
			return c.CombinedOutput()
		},
		readFile: os.ReadFile,
		stat:     os.Stat,
	})
}

func probeHostJail(self string, e probeEnv) HostJail {
	switch e.goos {
	case "darwin":
		if _, err := e.lookPath("sandbox-exec"); err != nil {
			return HostJail{Reason: "sandbox-exec (macOS Seatbelt) is not available", Fix: "run conductor on a macOS with /usr/bin/sandbox-exec"}
		}
		if out, err := e.run([]string{"sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true"}); err != nil {
			return HostJail{Reason: "sandbox-exec does not run here (" + lastLine(out, err) + ")", Fix: "run conductor outside a sandbox that denies sandbox-exec"}
		}
		return HostJail{OK: true}
	case "linux":
	default:
		return HostJail{Reason: "there is no plugin jail on " + e.goos, Fix: "run conductor on Linux or macOS"}
	}
	if e.euid == 0 {
		return HostJail{
			Reason: "conductor runs as root, and a user namespace maps root to root, so a jail would be no boundary",
			Fix:    "run conductor as a non-root user (in a container: a USER other than root, with user namespaces allowed)",
		}
	}
	if _, err := e.lookPath("unshare"); err != nil {
		return HostJail{Reason: "unshare (util-linux) is not installed", Fix: "install util-linux"}
	}
	if self == "" {
		return HostJail{Reason: "conductor's own binary (the jail's entry) is unknown", Fix: "run conductor from its installed binary"}
	}
	spec := &sandbox.Spec{Mode: "namespace", Deny: true}
	argv, err := spec.WrapLocal([]string{self, "sandbox-net", "--noop"}, "", nil, &sandbox.NetForward{
		Self:  self,
		Base:  sandbox.BaseMinimal,
		Binds: []sandbox.BindMount{{Path: filepath.Dir(self), RO: true}},
	})
	if err != nil {
		return HostJail{Reason: "the jail cannot be set up: " + err.Error(), Fix: "report this as a bug"}
	}
	out, err := e.run(argv)
	if err == nil {
		return HostJail{OK: true}
	}
	return diagnoseUserns(lastLine(out, err), e)
}

// diagnoseUserns names why a namespaced jail failed: the usual causes, in
// the order a host has them.
func diagnoseUserns(detail string, e probeEnv) HostJail {
	sysctl := func(p string) string {
		b, err := e.readFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	switch {
	case sysctl("/proc/sys/kernel/apparmor_restrict_unprivileged_userns") == "1":
		return HostJail{
			Reason: "AppArmor restricts unprivileged user namespaces (kernel.apparmor_restrict_unprivileged_userns=1, the Ubuntu 23.10+ default)",
			Fix:    "set kernel.apparmor_restrict_unprivileged_userns=0 (sysctl, persisted in /etc/sysctl.d), or give conductor an AppArmor profile that allows userns",
		}
	case sysctl("/proc/sys/kernel/unprivileged_userns_clone") == "0":
		return HostJail{
			Reason: "unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone=0)",
			Fix:    "set kernel.unprivileged_userns_clone=1 (sysctl, persisted in /etc/sysctl.d)",
		}
	case sysctl("/proc/sys/user/max_user_namespaces") == "0":
		return HostJail{
			Reason: "user namespaces are disabled (user.max_user_namespaces=0)",
			Fix:    "set user.max_user_namespaces to a positive number (sysctl, persisted in /etc/sysctl.d)",
		}
	case inContainer(e):
		return HostJail{
			Reason: "it runs in a container that does not allow user namespaces (" + detail + ")",
			Fix:    "run the container with user namespaces allowed (e.g. docker run --security-opt seccomp=unconfined --security-opt apparmor=unconfined, or a seccomp profile allowing unshare/clone with CLONE_NEWUSER, and an unmasked /proc)",
		}
	}
	return HostJail{
		Reason: "a jail could not be built (" + detail + ")",
		Fix:    "allow unprivileged user namespaces that may mount (check sysctls, seccomp and AppArmor)",
	}
}

func inContainer(e probeEnv) bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := e.stat(p); err == nil {
			return true
		}
	}
	b, _ := e.readFile("/proc/1/cgroup")
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") || strings.Contains(s, "libpod")
}

// lastLine is the last non-empty line of a failed command's output, or its
// error.
func lastLine(out []byte, err error) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if l := strings.TrimSpace(string(lines[len(lines)-1])); l != "" {
		return l
	}
	return err.Error()
}
