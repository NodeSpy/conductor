package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// testJailDeps wires SandboxDeps for a REALLY JAILED plugin launch: conductor's
// own binary (the jail's entry — SandboxDeps.Self) built ONCE for the whole
// test binary run, and a live egress proxy. Every test in this package that
// exercises buildCommand/buildCommandIn for a non-TrustFull Spec needs this
// (or an equivalent), because buildJailed refuses to run at all without a
// wired jail entry: "conductor's own binary (the jail's entry) is not wired".
//
// Mirrors TestDeclaredJailLinux's own preflight: skip (not fail) when this
// host cannot build the jail, so these tests degrade gracefully on a box
// without unprivileged user namespaces rather than failing the whole suite.
// Linux only — macOS's Seatbelt jail (buildSeatbelt) does not need Self at
// all, so a plugin test that wants to exercise that path builds its own
// SandboxDeps with EgressAddr instead (see seatbelt_spawn_test.go).
func testJailDeps(t *testing.T) SandboxDeps {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("testJailDeps is linux-only")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}
	testConductorOnce.Do(func() {
		dir, err := os.MkdirTemp("", "conductor-jailtest-bin")
		if err != nil {
			testConductorErr = err
			return
		}
		testConductorPath = filepath.Join(dir, "conductor")
		out, err := exec.Command("go", "build", "-o", testConductorPath, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput()
		if err != nil {
			testConductorErr = fmt.Errorf("build conductor: %w\n%s", err, out)
		}
	})
	if testConductorErr != nil {
		t.Fatalf("build conductor (jail entry): %v", testConductorErr)
	}
	pm := sandbox.NewProxyManager(nil)
	t.Cleanup(pm.Close)
	return SandboxDeps{
		Self:        testConductorPath,
		StagingRoot: t.TempDir(),
		EgressUnix:  pm.UnixEndpoint,
		EgressAddr:  pm.Endpoint,
	}
}

var (
	testConductorOnce sync.Once
	testConductorPath string
	testConductorErr  error
)
