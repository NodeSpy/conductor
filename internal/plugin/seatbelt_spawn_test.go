package plugin

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// On macOS a namespace-isolated plugin is confined by Seatbelt (sandbox-exec),
// which is an ALLOW-LIST — so buildCommand must allow-list the plugin's OWN
// binary dir (the Linux Masks deny-list has no Seatbelt analog and would leave
// the plugin unable to read/exec itself). Exercised from a Linux host via the
// injectable spawnGOOS / spawnLookPath + sandbox.CheckGOOS.
func TestBuildCommandDarwinSeatbeltJailsPlugin(t *testing.T) {
	oldG, oldL, oldC := spawnGOOS, spawnLookPath, sandbox.CheckGOOS
	spawnGOOS = "darwin"
	spawnLookPath = func(n string) (string, error) { return "/usr/bin/" + n, nil }
	sandbox.CheckGOOS = "darwin"
	t.Cleanup(func() { spawnGOOS, spawnLookPath, sandbox.CheckGOOS = oldG, oldL, oldC })

	dir := t.TempDir()
	bin := writeBin(t, dir, "plugin", []byte("x"), 0o755)
	s := Spec{
		Name: "p", Kind: KindConnector, BinPath: bin, Local: true,
		Isolation:          &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}},
		IsolationDefaulted: true,
	}
	cmd, cleanup, sandboxed, err := buildCommand(s, SandboxDeps{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !sandboxed {
		t.Fatal("a namespace plugin on darwin must be Seatbelt-confined, not degraded to manifest-only")
	}
	if len(cmd.Args) < 4 || cmd.Args[0] != "sandbox-exec" || cmd.Args[1] != "-p" {
		t.Fatalf("expected sandbox-exec -p <profile> …, got %v", cmd.Args)
	}
	profile := cmd.Args[2]
	wantDir, err := filepath.EvalSymlinks(filepath.Dir(bin))
	if err != nil {
		wantDir = filepath.Dir(bin)
	}
	if !strings.Contains(profile, `(subpath "`+wantDir+`")`) {
		t.Fatalf("profile must allow-list the plugin binary dir %q:\n%s", wantDir, profile)
	}
	if !strings.Contains(profile, "(deny network*)") {
		t.Fatalf("deny default must cut the network:\n%s", profile)
	}
	if cmd.Args[len(cmd.Args)-1] != bin {
		t.Fatalf("wrapped argv must exec the plugin binary %q: %v", bin, cmd.Args)
	}
}
