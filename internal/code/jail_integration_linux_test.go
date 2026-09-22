//go:build linux

package code

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestCodeStepJailedEndToEnd proves the FULL production path a confined pack
// code step takes: the Executor's execCLILocal builds the isolation deps
// (Confine + the code/ctx binds), sandbox.WrapLocalCommand renders the
// root-mapped unshare + `conductor sandbox-net --bind …`, and the real
// conductor binary builds the pivot_root jail before running the step. The step
// must (a) succeed, (b) SEE its bound workdir, and (c) NOT see a host path that
// was never bound — the confinement the whole feature exists to give pack code.
func TestCodeStepJailedEndToEnd(t *testing.T) {
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
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}

	// Build a real conductor binary — sandbox-net (the in-jail helper) is a
	// subcommand of it.
	bin := filepath.Join(t.TempDir(), "conductor")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/cmd/conductor")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build conductor: %v\n%s", err, out)
	}

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "marker"), []byte("in-workdir"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret under a DIFFERENT temp root, never bound — the jail's fresh /tmp
	// (and the allow-list) must hide it.
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "creds")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := &Executor{Sandbox: sandbox.LocalWrapDeps{
		SelfExe: func() (string, error) { return bin, nil },
	}}
	// namespace + deny, mirroring the pack default; workdir is bound
	// automatically, the secret dir is not.
	spec := Spec{
		Run:     "cli",
		Command: []string{"bash", "-c", `test -f "$MARKER" || { echo "no marker" >&2; exit 3; }; if [ -e "$SECRET" ]; then echo "SECRET VISIBLE" >&2; exit 4; fi; echo '{"ok": true}'`},
		Env:     map[string]string{"MARKER": filepath.Join(work, "marker"), "SECRET": secret},
		WorkDir: work,
		Isolation: &config.IsolationConfig{
			Mode:    "namespace",
			Network: &config.IsolationNetwork{Deny: true},
		},
	}
	out, err := e.Exec(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("jailed code step failed (exit 3=no workdir, 4=secret leaked): %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("unexpected outputs: %#v", out)
	}
}
