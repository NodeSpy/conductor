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

// Exit codes carried from the re-exec'd probe back to the parent test.
const (
	maskProbeMasked   = 0  // the mask held — the file read back empty (protected)
	maskProbeRevealed = 7  // the payload umounted the mask and read the secret (HOLE)
	maskEnvCannotMask = 42 // this environment can't apply the mask at all — skip
)

const maskSecret = "SECRET-DAEMON-CONFIG-DO-NOT-LEAK"

// TestMaskSurvivesUmountInSandbox proves Fix #1 (#36 iso-review round 2, item 1):
// after RunEnter overmounts a daemon file, the untrusted payload it execs must
// NOT be able to `umount` that mask and read the file underneath. The masks are
// mounts in the outer user namespace's mount namespace, which would otherwise
// still grant the payload CAP_SYS_ADMIN over them; RunEnter re-execs the payload
// through a nested user namespace that owns none of those, so umount is refused.
//
// The test re-execs its own binary in three roles:
//
//   - the innermost PROBE tries to umount the mask and read the file, encoding
//     the result in its exit code;
//   - the SAFE outer role runs the real RunEnter (mask + nested-userns exec of
//     the probe) — the probe must come back "masked";
//   - the UNSAFE outer role masks then PLAIN-execs the probe (no nested userns,
//     the pre-fix behaviour) — the probe must come back "revealed", proving the
//     test actually exercises the hole and would catch a regression that drops
//     the nesting.
func TestMaskSurvivesUmountInSandbox(t *testing.T) {
	switch {
	case os.Getenv("MASK_PROBE") != "":
		os.Exit(runMaskProbe(os.Getenv("MASK_PROBE")))
	case os.Getenv("MASK_OUTER_SAFE") != "":
		os.Exit(runMaskOuterSafe(os.Getenv("MASK_OUTER_SAFE")))
	case os.Getenv("MASK_OUTER_UNSAFE") != "":
		os.Exit(runMaskOuterUnsafe(os.Getenv("MASK_OUTER_UNSAFE")))
	}

	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH — namespace masking is Linux+util-linux only")
	}

	// Capability gate: the OUTER unshare every scenario relies on must actually
	// run here. GitHub-hosted runners (and other locked-down kernels) forbid
	// `unshare --user --mount`, which surfaces as a generic non-zero exit from
	// the outer unshare — BEFORE the re-exec'd binary runs, so the inner
	// maskCapable() probe never gets the chance to signal maskEnvCannotMask and
	// the scenario comes back as a bare exit 1. Probe the exact namespace up
	// front and skip cleanly (never a false green) rather than failing. On a
	// box that DOES support it, `true` exits 0 and the real test runs.
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged user namespaces; unavailable in this environment "+
			"(e.g. restricted CI runner): %v", err)
	}

	// SAFE: RunEnter's nested-userns hop must keep the mask intact.
	safe := runMaskScenario(t, "MASK_OUTER_SAFE")
	switch safe {
	case maskEnvCannotMask:
		t.Skip("unprivileged user+mount namespaces unavailable here — cannot exercise masking")
	case maskProbeMasked:
		// expected — the mask survived the payload's umount attempt
	case maskProbeRevealed:
		t.Fatal("payload umounted the mask and read the daemon file — Fix #1 privilege drop is NOT holding")
	default:
		t.Fatalf("safe scenario returned unexpected exit code %d", safe)
	}

	// UNSAFE control: without the nested-userns hop the SAME probe DOES reveal
	// the file — if this ever comes back "masked", the test is no longer
	// exercising the hole and the safe result above is meaningless.
	unsafe := runMaskScenario(t, "MASK_OUTER_UNSAFE")
	switch unsafe {
	case maskEnvCannotMask:
		t.Skip("unprivileged user+mount namespaces unavailable here — cannot exercise masking")
	case maskProbeRevealed:
		// expected — plain exec inherits CAP_SYS_ADMIN and umounts the mask
	case maskProbeMasked:
		t.Fatal("control (plain exec, no nested userns) did NOT reveal the file — the test no longer exercises the umount hole")
	default:
		t.Fatalf("unsafe scenario returned unexpected exit code %d", unsafe)
	}
}

// runMaskScenario re-execs the test binary inside an outer user+mount namespace
// (where the mask can be applied) with the given role env var pointing at a
// fresh secret file, and returns the propagated exit code.
func runMaskScenario(t *testing.T, roleEnv string) int {
	t.Helper()
	dir := t.TempDir()
	secret := dir + "/daemon-secret.conf"
	if err := os.WriteFile(secret, []byte(maskSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// --map-root-user gives the outer namespace the mount privilege maskPath
	// needs regardless of the host uid; --mount gives it a private mount ns to
	// apply the overmount into. The nested-userns drop under test is uid-agnostic.
	argv := []string{"unshare", "--user", "--map-root-user", "--mount",
		os.Args[0], "-test.run=^TestMaskSurvivesUmountInSandbox$", "-test.v"}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), roleEnv+"="+secret)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		t.Logf("scenario %s launch error: %v\n%s", roleEnv, err, out)
		return maskEnvCannotMask
	}
	return 0
}

// runMaskProbe is the innermost payload: try every umount form, then read the
// file. A non-empty read that carries the secret means the mask was defeated.
func runMaskProbe(path string) int {
	_ = unix.Unmount(path, 0)
	_ = unix.Unmount(path, unix.MNT_DETACH)
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), maskSecret) {
		return maskProbeRevealed
	}
	return maskProbeMasked
}

// runMaskOuterSafe runs the production path: RunEnter masks the file and execs
// the probe through the nested-userns privilege drop.
func runMaskOuterSafe(secret string) int {
	// RunEnter collapses a mask-EPERM environment into a generic exit 1; probe
	// the capability first so the parent can SKIP (not fail) where mounts are
	// unavailable, distinct from a real "the mask leaked" failure.
	if !maskCapable(filepath.Dir(secret)) {
		return maskEnvCannotMask
	}
	os.Unsetenv("MASK_OUTER_SAFE")
	os.Setenv("MASK_PROBE", secret)
	return RunEnter(EnterOpts{
		Masks: []string{secret},
		Argv:  []string{os.Args[0], "-test.run=^TestMaskSurvivesUmountInSandbox$", "-test.v"},
	})
}

// maskCapable reports whether an overmount can be applied in this namespace
// (some CI/sandbox kernels forbid even unprivileged-userns mounts).
func maskCapable(dir string) bool {
	probe := filepath.Join(dir, ".maskprobe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return false
	}
	defer os.Remove(probe)
	if err := unix.Mount("/dev/null", probe, "", unix.MS_BIND, ""); err != nil {
		return false
	}
	_ = unix.Unmount(probe, 0)
	return true
}

// runMaskOuterUnsafe is the pre-fix control: mask, then PLAIN exec the probe
// (no nested userns), so it inherits CAP_SYS_ADMIN over the mount namespace.
func runMaskOuterUnsafe(secret string) int {
	if err := maskPath(secret); err != nil {
		return maskEnvCannotMask
	}
	os.Unsetenv("MASK_OUTER_UNSAFE")
	os.Setenv("MASK_PROBE", secret)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMaskSurvivesUmountInSandbox$", "-test.v")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return maskEnvCannotMask
	}
	return 0
}
