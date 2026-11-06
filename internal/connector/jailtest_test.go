package connector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// testJailDeps builds conductor's own binary once per test binary (the
// jail's entry, SandboxDeps.Self) and wires a real egress proxy, so a
// *plugin.Manager/Client built with the returned Deps launches its
// subprocess really jailed — the same way the daemon does — rather than
// failing with "cannot jail it" or running unconfined. Confinement/grant
// tests (multi-instance isolation, per-instance env/egress) need this; tests
// whose subject is wire mechanics instead use Spec.TrustFull.
func testJailDeps(t *testing.T) plugin.Deps {
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
		dir, err := os.MkdirTemp("", "conductor-connector-jailtest-bin")
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
	return plugin.Deps{Sandbox: plugin.SandboxDeps{
		Self:        testConductorPath,
		StagingRoot: t.TempDir(),
		EgressUnix:  pm.UnixEndpoint,
		EgressAddr:  pm.Endpoint,
	}}
}

var (
	testConductorOnce sync.Once
	testConductorPath string
	testConductorErr  error
)
