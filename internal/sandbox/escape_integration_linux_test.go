//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Red-team escape battery for the Linux namespace+pivot_root jail. It drives
// the PRODUCTION RunEnter (the same path WrapLocal builds for every agent) and
// then, inside the dropped-privilege payload, attempts every filesystem- and
// process-level escape in the brief: remount/umount/pivot of the jail mounts,
// the nested user+mount namespace "locked mount" bypass, host /proc and host
// process visibility, device exposure, writing to read-only base dirs, and
// mountinfo source leaks. Each check records a verdict; any escape fails the
// test loudly.
//
// It reuses the role-reexec shape of TestJailConfinesInSandbox: the test binary
// re-execs itself as the OUTER (unshare → RunEnter) and innermost PROBE.

const escSecret = "ESCAPE-BATTERY-SECRET-MUST-NOT-LEAK"

// escCheck is one escape attempt's verdict.
type escCheck struct {
	Name    string `json:"name"`
	Escaped bool   `json:"escaped"` // true = the sandbox was defeated
	Detail  string `json:"detail"`
}

func TestJailEscapeBattery(t *testing.T) {
	switch os.Getenv("ESC_ROLE") {
	case "probe":
		os.Exit(runEscProbe())
	case "outer":
		os.Exit(runEscOuter())
	}

	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH — the namespace jail is Linux+util-linux only")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}

	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unbound secret under the same temp root: the jail must hide it by absence.
	secret := filepath.Join(dir, "secret.conf")
	if err := os.WriteFile(secret, []byte(escSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verdict := filepath.Join(work, "verdict.json")

	argv := []string{"unshare", "--user", "--map-root-user", "--pid", "--fork",
		"--mount-proc", "--kill-child", "--net", "--",
		os.Args[0], "-test.run=^TestJailEscapeBattery$", "-test.v"}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"ESC_ROLE=outer",
		"ESC_WORK="+work,
		"ESC_SECRET="+secret,
		"ESC_VERDICT="+verdict,
		"ESC_HOSTHOME="+os.Getenv("HOME"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == jailEnvCannot {
			t.Skipf("environment cannot build the jail:\n%s", out)
		}
		t.Fatalf("outer launch failed: %v\n%s", err, out)
	}

	b, rerr := os.ReadFile(verdict)
	if rerr != nil {
		t.Fatalf("no verdict file (%v); outer output:\n%s", rerr, out)
	}
	var checks []escCheck
	if err := json.Unmarshal(b, &checks); err != nil {
		t.Fatalf("bad verdict JSON (%v): %s", err, b)
	}
	if len(checks) == 0 {
		t.Fatalf("probe recorded no checks; output:\n%s", out)
	}
	var escaped []string
	for _, c := range checks {
		t.Logf("escape check %-28s escaped=%-5v %s", c.Name, c.Escaped, c.Detail)
		if c.Escaped {
			escaped = append(escaped, c.Name+" ("+c.Detail+")")
		}
	}
	if len(escaped) > 0 {
		t.Fatalf("SANDBOX ESCAPE(S): %s", strings.Join(escaped, "; "))
	}
}

// runEscOuter runs the production RunEnter with an agent-shaped allow-list
// (workdir rw + the test-binary dir ro so the probe can re-exec).
func runEscOuter() int {
	work := os.Getenv("ESC_WORK")
	selfDir := filepath.Dir(os.Args[0])
	os.Setenv("ESC_ROLE", "probe")
	// The re-exec'd probe runs this package's TestMain (testhome), which needs a
	// writable temp dir; point it at the bound, writable workdir so the probe
	// works wherever /tmp handling differs (host vs container).
	os.Setenv("TMPDIR", work)
	return RunEnter(EnterOpts{
		Binds: []BindMount{
			{Path: work},
			{Path: selfDir, RO: true},
		},
		Chdir: work,
		Argv:  []string{os.Args[0], "-test.run=^TestJailEscapeBattery$", "-test.v"},
	})
}

// runEscProbe is the innermost payload: it has dropped privilege and lives in
// the jail. It runs every escape attempt and writes the verdicts.
func runEscProbe() int {
	var checks []escCheck
	add := func(name string, escaped bool, detail string) {
		checks = append(checks, escCheck{Name: name, Escaped: escaped, Detail: detail})
	}

	// (0) sanity: workdir visible.
	if _, err := os.Stat("verdict.json"); err != nil && !os.IsNotExist(err) {
		add("workdir_visible", true, "cannot stat in workdir: "+err.Error())
	}

	// (1) the unbound secret must be gone (hidden by absence).
	sp := os.Getenv("ESC_SECRET")
	if b, err := os.ReadFile(sp); err == nil {
		add("unbound_secret_absent", true, "readable at "+sp+": "+strings.TrimSpace(string(b)))
	} else {
		add("unbound_secret_absent", false, "absent ("+sp+")")
	}

	// (2) remount a read-only base dir rw.
	if err := unix.Mount("", "/usr", "", unix.MS_BIND|unix.MS_REMOUNT, ""); err == nil {
		add("remount_usr_rw", true, "remount succeeded")
	} else {
		add("remount_usr_rw", false, err.Error())
	}

	// (3) umount a base dir (to reveal anything underneath / detach the jail).
	if err := unix.Unmount("/usr", unix.MNT_DETACH); err == nil {
		add("umount_usr", true, "umount succeeded")
	} else {
		add("umount_usr", false, err.Error())
	}

	// (4) the detached old root must be gone.
	if _, err := os.Stat("/.oldroot"); err == nil {
		add("oldroot_present", true, "/.oldroot exists")
	} else {
		add("oldroot_present", false, "absent")
	}

	// (5) write to a read-only base dir.
	if err := os.WriteFile("/usr/esc-evil", []byte("x"), 0o644); err == nil {
		_ = os.Remove("/usr/esc-evil")
		add("write_usr_ro", true, "wrote /usr/esc-evil")
	} else {
		add("write_usr_ro", false, err.Error())
	}
	if err := os.WriteFile("/etc/esc-evil", []byte("x"), 0o644); err == nil {
		_ = os.Remove("/etc/esc-evil")
		add("write_etc_ro", true, "wrote /etc/esc-evil")
	} else {
		add("write_etc_ro", false, err.Error())
	}

	// (6) pivot_root back out: needs CAP_SYS_ADMIN over the jail's mount ns,
	// which the dropped payload does not hold.
	_ = os.MkdirAll("/tmp/esc-pivot", 0o755)
	if err := unix.PivotRoot("/", "/tmp/esc-pivot"); err == nil {
		add("pivot_root_out", true, "pivot_root succeeded")
	} else {
		add("pivot_root_out", false, err.Error())
	}

	// (7) nested user+mount namespace "locked mount" bypass: create a fresh
	// mount ns we own and try to umount/remount the inherited jail mounts.
	add("nested_userns_umount", nestedUnshareEscapes("umount"), "see detail")
	checks[len(checks)-1].Detail = nestedDetail
	add("nested_userns_remount", nestedUnshareEscapes("remount"), nestedDetail)

	// (8) host process / pid-namespace leak: /proc must show only sandbox pids,
	// and no host process's cmdline/environ.
	esc, det := procLeak()
	add("proc_host_pid_leak", esc, det)

	// (9) /proc/1 must be our own namespace init, not the host's.
	esc, det = proc1Leak()
	add("proc1_host_leak", esc, det)

	// (10) device exposure: no block/mem/kernel devices.
	esc, det = deviceExposure()
	add("device_exposure", esc, det)

	// (11) mountinfo source leak: no mount source references a host path
	// outside the allow-list (notably the operator's real home).
	esc, det = mountinfoLeak()
	add("mountinfo_host_leak", esc, det)

	// (12) can we still reach any network in the empty netns? (no interfaces
	// beyond lo; an outbound dial must fail.)
	esc, det = networkReachable()
	add("network_reachable", esc, det)

	b, _ := json.MarshalIndent(checks, "", "  ")
	if err := os.WriteFile(os.Getenv("ESC_VERDICT"), b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "probe: write verdict:", err)
		return 1
	}
	return 0
}

var nestedDetail string

// nestedUnshareEscapes runs the bound `unshare` to make a user+mount namespace
// the payload owns, then tries to umount or remount the jail's read-only /usr.
// If it can, the "locked mount" protection failed and the jail can be peeled
// open — an escape. It records the raw output in nestedDetail.
func nestedUnshareEscapes(op string) bool {
	script := `umount /usr 2>&1; echo "umount_rc=$?"`
	if op == "remount" {
		script = `mount -o remount,rw /usr 2>&1; echo "remount_rc=$?"; (echo x > /usr/esc-nested && echo WROTE_USR && rm -f /usr/esc-nested) 2>&1`
	}
	cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	nestedDetail = strings.TrimSpace(strings.ReplaceAll(string(out), "\n", " | "))
	if err != nil {
		nestedDetail += " | err=" + err.Error()
	}
	// Escaped only if the mutation actually took: rc==0 for the op, or a write
	// landed on the read-only base.
	if op == "umount" {
		return strings.Contains(nestedDetail, "umount_rc=0")
	}
	return strings.Contains(nestedDetail, "WROTE_USR")
}

func procLeak() (bool, string) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false, "cannot read /proc: " + err.Error()
	}
	hostHome := os.Getenv("ESC_HOSTHOME")
	var pids []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, n)
		}
	}
	// In a fresh pid namespace the sandbox's own PID 1 is `unshare`/`sandbox-net`
	// and the probe is a low pid. A large set, or any process whose cmdline
	// names a host-only path, is a leak.
	for _, p := range pids {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p))
		if err != nil {
			continue
		}
		cl := strings.ReplaceAll(string(b), "\x00", " ")
		if hostHome != "" && strings.Contains(cl, hostHome) {
			return true, fmt.Sprintf("pid %d cmdline references host home: %s", p, strings.TrimSpace(cl))
		}
	}
	if len(pids) > 50 {
		return true, fmt.Sprintf("%d processes visible (pid namespace leak?)", len(pids))
	}
	return false, fmt.Sprintf("%d pids visible (sandbox-only)", len(pids))
}

func proc1Leak() (bool, string) {
	// /proc/1/environ of the host init/daemon would carry host secrets. In our
	// pid ns, pid 1 is our own and its environ is the sandbox's.
	b, err := os.ReadFile("/proc/1/environ")
	if err != nil {
		return false, "proc/1/environ unreadable: " + err.Error()
	}
	env := strings.ReplaceAll(string(b), "\x00", " ")
	for _, bad := range []string{"ANTHROPIC_API_KEY", "GH_TOKEN", "GITHUB_TOKEN", "AWS_", "CONDUCTOR_SKILL_TOKEN=", "SSH_AUTH_SOCK"} {
		if strings.Contains(env, bad) {
			return true, "proc/1/environ carries host credential marker: " + bad
		}
	}
	return false, "proc/1 is the sandbox's own init"
}

func deviceExposure() (bool, string) {
	for _, d := range []string{"/dev/sda", "/dev/nvme0n1", "/dev/mem", "/dev/kmem", "/dev/kmsg", "/dev/sda1", "/dev/vda", "/dev/mapper/control"} {
		if f, err := os.OpenFile(d, os.O_RDONLY, 0); err == nil {
			f.Close()
			return true, "opened host device " + d
		}
	}
	return false, "no block/mem/kernel devices"
}

func mountinfoLeak() (bool, string) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, "mountinfo unreadable: " + err.Error()
	}
	hostHome := os.Getenv("ESC_HOSTHOME")
	for _, line := range strings.Split(string(b), "\n") {
		if hostHome != "" && hostHome != "/" && strings.Contains(line, hostHome) {
			return true, "mountinfo references host home: " + line
		}
		for _, bad := range []string{"/.ssh", "/.aws", "/.config/gh", "agent-state", "/.gnupg"} {
			if strings.Contains(line, bad) {
				return true, "mountinfo references a secret path: " + line
			}
		}
	}
	return false, "no host-secret mount sources"
}

func networkReachable() (bool, string) {
	// The jail is launched with --net: an empty netns. There is nothing to
	// reach. (The relay/proxy are wired separately and not present here.)
	ifaces, _ := os.ReadDir("/sys/class/net")
	var names []string
	for _, i := range ifaces {
		names = append(names, i.Name())
	}
	for _, n := range names {
		if n != "lo" {
			return true, "unexpected interface in netns: " + n
		}
	}
	return false, "empty netns (ifaces: " + strings.Join(names, ",") + ")"
}
