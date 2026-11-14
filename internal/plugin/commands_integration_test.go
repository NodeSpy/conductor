//go:build linux || darwin

package plugin

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestHomebrewCommandRunsJailed runs a command Homebrew linked onto PATH
// (git, or CONDUCTOR_TEST_BREW_COMMAND) through the real plugin launch: it
// is found from what Homebrew recorded installing, and runs in the jail
// with the libraries of what it was installed with. Skipped where PATH has no such command (CI's Linux
// runners; a Mac whose git is Apple's).
func TestHomebrewCommandRunsJailed(t *testing.T) {
	name := os.Getenv("CONDUCTOR_TEST_BREW_COMMAND")
	if name == "" {
		name = "git"
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			t.Skip("sandbox-exec not on PATH")
		}
	} else if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skip("requires unprivileged root-mapped user namespaces:", err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	jailHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { jailHome = os.UserHomeDir })
	sd := SandboxDeps{StagingRoot: filepath.Join(dir, "staging")}
	m := Manifest{Commands: []string{name}, Env: []string{"GIT_CONFIG_NOSYSTEM"}}
	g, err := pluginGrant(Spec{Name: "probe", Manifest: m})
	if err != nil {
		t.Fatal(err)
	}
	found, err := findCommands(Spec{Name: "probe"}, newGrantChecks(sd, g), []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].source != fromHomebrew {
		t.Skipf("%s is not a Homebrew command here (%+v)", name, found)
	}
	t.Logf("%s: %s, reads %v", name, found[0].real, found[0].reads)

	t.Setenv("HOME", home)
	// git's system config is the prefix's etc/gitconfig, which the plugin
	// does not declare: a plugin running git says so (as it would).
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	key := "out=" + name + " --version"
	got := runProbePlugin(t, dir, sd, m, key)[key]
	if !strings.HasPrefix(got, "ok:") {
		t.Fatalf("%s in the jail: %q", key, got)
	}
	t.Logf("jailed %s: %s", key, got)
}

// runProbePlugin launches the jail probe as plugin "probe" with manifest m
// through the real jailed launch, and returns what it reported for keys.
func runProbePlugin(t *testing.T, dir string, sd SandboxDeps, m Manifest, keys ...string) map[string]string {
	t.Helper()
	conductor := filepath.Join(dir, "conductor")
	if out, err := exec.Command("go", "build", "-o", conductor, "github.com/NodeSpy/conductor/cmd/conductor").CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}
	binDir := filepath.Join(dir, "plugins", "probe", "v1")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(binDir, "jailprobe")
	if out, err := exec.Command("go", "build", "-o", probe, "./testdata/jailprobe").CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	pm := sandbox.NewProxyManager(nil)
	t.Cleanup(pm.Close)
	sd.Self, sd.EgressUnix, sd.EgressAddr = conductor, pm.UnixEndpoint, pm.Endpoint
	s := Spec{Name: "probe", Kind: KindConnector, BinPath: probe, Manifest: m, Args: keys}
	cmd, cleanup, sandboxed, err := buildCommand(s, sd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !sandboxed {
		t.Fatal("the launch is not jailed")
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), "\t"); ok && slices.Contains(keys, k) {
				got[k] = v
			}
			if sc.Text() == "READY" {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("probe did not finish; stderr:\n%s", stderr.String())
	}
	stdin.Close()
	_ = cmd.Wait()
	if len(got) != len(keys) {
		t.Fatalf("probe reported %v for %v; stderr:\n%s", got, keys, stderr.String())
	}
	return got
}
