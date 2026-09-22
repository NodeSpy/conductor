//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Exit codes carried from the re-exec'd jail probe back to the parent test.
const (
	jailProbeConfined = 0  // workdir visible, unbound secret gone, escape denied — good
	jailProbeLeaked   = 7  // an unbound host path was still visible — jail FAILED
	jailProbeNoWork   = 8  // the bound workdir was NOT visible — jail broke the launch
	jailProbeEscaped  = 9  // the payload remounted a read-only base dir — drop FAILED
	jailEnvCannot     = 42 // this environment can't build the jail at all — skip
)

const jailSecret = "SECRET-OUTSIDE-THE-ALLOWLIST-DO-NOT-LEAK"

// TestJailConfinesInSandbox proves the pivot_root fs-jail end to end via the
// production RunEnter: after it builds an allow-list jail and drops privilege,
// the untrusted payload it execs (a) SEES the bound workdir, (b) does NOT see a
// host path that was never bound (hidden by absence, the whole point — no
// overmount, so no EPERM), and (c) cannot remount a read-only base dir to climb
// back out. It re-execs its own binary in two roles: the OUTER role runs
// RunEnter; the innermost PROBE checks the three properties and encodes the
// verdict in its exit code.
func TestJailConfinesInSandbox(t *testing.T) {
	switch os.Getenv("JAIL_ROLE") {
	case "probe":
		os.Exit(runJailProbe())
	case "outer":
		os.Exit(runJailOuter())
	}

	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH — the namespace jail is Linux+util-linux only")
	}
	// The jail needs a root-mapped user namespace to hold CAP_SYS_ADMIN over the
	// mount ns; probe that it runs here (locked-down CI kernels forbid it) and
	// skip cleanly rather than fail.
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces (unavailable in this environment): %v", err)
	}

	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "marker"), []byte("in-workdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The secret sits under the same temp root but is NEVER bound — the jail's
	// fresh /tmp must hide it purely by absence.
	secret := filepath.Join(dir, "secret.conf")
	if err := os.WriteFile(secret, []byte(jailSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The OUTER unshare mirrors WrapLocal's namespace jail invocation exactly:
	// root-mapped userns + pid + fork + mount-proc.
	argv := []string{"unshare", "--user", "--map-root-user", "--pid", "--fork",
		"--mount-proc", "--kill-child", "--",
		os.Args[0], "-test.run=^TestJailConfinesInSandbox$", "-test.v"}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"JAIL_ROLE=outer",
		"JAIL_WORK="+work,
		"JAIL_MARKER="+filepath.Join(work, "marker"),
		"JAIL_SECRET="+secret,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Skipf("outer namespace launch failed (environment cannot build the jail): %v\n%s", err, out)
	}
	switch code {
	case jailProbeConfined:
		// success
	case jailEnvCannot:
		t.Skip("environment cannot build the pivot_root jail — skipping")
	case jailProbeLeaked:
		t.Fatalf("payload saw an UNBOUND host path — the jail did not hide it by absence\n%s", out)
	case jailProbeNoWork:
		t.Fatalf("payload could not see the bound workdir — the jail broke the launch\n%s", out)
	case jailProbeEscaped:
		t.Fatalf("payload remounted a read-only base dir — the privilege drop is NOT holding\n%s", out)
	default:
		t.Fatalf("jail probe returned unexpected exit code %d\n%s", code, out)
	}
}

// runJailOuter runs the production path: RunEnter builds the allow-list jail
// (workdir + the test-binary dir, so the re-exec'd probe can be found) and
// execs the probe through the nested-userns privilege drop.
func runJailOuter() int {
	work := os.Getenv("JAIL_WORK")
	selfDir := filepath.Dir(os.Args[0])
	os.Setenv("JAIL_ROLE", "probe")
	return RunEnter(EnterOpts{
		Binds: []BindMount{
			{Path: work},
			{Path: selfDir, RO: true},
		},
		Argv: []string{os.Args[0], "-test.run=^TestJailConfinesInSandbox$", "-test.v"},
	})
}

// runJailProbe is the innermost payload, running inside the jail after the
// privilege drop. It checks the three confinement properties.
func runJailProbe() int {
	// (a) the bound workdir must be visible.
	if b, err := os.ReadFile(os.Getenv("JAIL_MARKER")); err != nil || !strings.Contains(string(b), "in-workdir") {
		return jailProbeNoWork
	}
	// (b) the unbound secret must be gone (hidden by absence).
	if _, err := os.Stat(os.Getenv("JAIL_SECRET")); err == nil {
		return jailProbeLeaked
	}
	// (c) escape attempt: remounting a read-only base dir rw must be refused
	// (the payload holds no CAP_SYS_ADMIN over the jail's mount ns).
	if err := unix.Mount("", "/usr", "", unix.MS_BIND|unix.MS_REMOUNT, ""); err == nil {
		return jailProbeEscaped
	}
	return jailProbeConfined
}
