//go:build linux

package agentsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// probeGo writes a fake "go" that behaves like the real `go mod download`
// for our purposes (exits 0, no args checked) but FIRST checks whether
// secret is visible — exiting loudly if so. The path is baked into the
// script text (like fakeGo's record/fail paths elsewhere in this package),
// so no extra env wiring is needed to reach it from inside the jail.
func probeGo(t *testing.T, secret string) (bin string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "go")
	script := "#!/bin/sh\n" +
		"if [ -e " + secret + " ]; then echo 'SECRET VISIBLE' >&2; exit 9; fi\n" +
		"mkdir -p \"$GOMODCACHE\" 2>/dev/null; touch \"$GOMODCACHE/wrote\" || { echo 'cache not writable' >&2; exit 8; }\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestGoPrefetchFillRunsConfined proves the FULL production path (rt2 #11):
// GoPrefetch.Fill wraps `go mod download` through sandbox.WrapLocalCommand
// with Confine, and the download really runs inside the jail — it CANNOT
// read a file planted in a decoy "operator HOME" well outside the
// allow-list, and it CAN write into the module cache it was given.
func TestGoPrefetchFillRunsConfined(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the conductor binary; skipped under -short")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--pid", "--fork",
		"--mount-proc", "--kill-child", "true").Run(); err != nil {
		t.Skipf("root-mapped user namespaces unavailable here: %v", err)
	}

	// Build a real conductor binary — sandbox-net (the in-jail helper) is a
	// subcommand of it.
	bin := filepath.Join(t.TempDir(), "conductor")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}

	// A decoy "operator HOME" with a secret file, under a DIFFERENT temp
	// root than the download's own scratch dir or the cache — never bound
	// into the jail. If the confined download can still see it, the jail
	// failed to hide it by absence.
	decoyHome := t.TempDir()
	secret := filepath.Join(decoyHome, ".netrc")
	if err := os.WriteFile(secret, []byte("machine example.com login op password TOPSECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goBin := probeGo(t, secret)

	cache := filepath.Join(t.TempDir(), "modcache")
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	g := &GoPrefetch{
		Cache: cache,
		Go:    goBin,
		Proxy: "https://proxy.golang.org",
		Env:   []string{"PATH=" + os.Getenv("PATH")},
		Sandbox: sandbox.LocalWrapDeps{
			SelfExe:    func() (string, error) { return bin, nil },
			EgressUnix: pm.UnixEndpoint,
			EgressAddr: pm.Endpoint,
			// The fake go's own directory must be visible (and executable)
			// inside the jail for the re-exec to find it at all — the same
			// allow-list entry a code step's own code temp dir gets.
			ExtraBinds: []sandbox.BindMount{{Path: filepath.Dir(goBin), RO: true}},
		},
	}

	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n\ngo 1.23\n")}}
	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatalf("confined prefetch failed (exit 9 = the decoy secret was VISIBLE, exit 8 = the cache was not writable): %v", err)
	}

	// The probe wrote into GOMODCACHE from inside the jail — the module
	// cache bind is really read-write there.
	if _, err := os.Stat(filepath.Join(cache, "wrote")); err != nil {
		t.Fatalf("the confined download could not write its module cache: %v", err)
	}
	// The marker proves it actually ran (not silently skipped).
	if _, err := os.Stat(filepath.Join(cache, ".conductor-prefetched", m.Digest())); err != nil {
		t.Fatalf("the confined download did not complete: %v", err)
	}
}
