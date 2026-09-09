package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildExamplePlugin compiles the reference acme-echo plugin into a temp dir and
// returns its path + SHA-256. Skips if the go toolchain isn't available.
func buildExamplePlugin(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-echo")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// TestExamplePluginRoundTrip drives the REAL subprocess over the real transport.
func TestExamplePluginRoundTrip(t *testing.T) {
	bin, sum := buildExamplePlugin(t)
	spec := Spec{Name: "echo", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: sum, Version: "1.0.0", AllowUnsandboxed: true}
	c := NewClient(spec, Deps{})
	defer c.Close()
	ctx := context.Background()

	decl, err := c.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if decl.Type != "acme-echo" || len(decl.Verbs) != 1 || decl.Verbs[0].Name != "echo" {
		t.Fatalf("unexpected decl: %+v", decl)
	}

	out, err := c.Invoke(ctx, InvokeRequest{
		Instance: "e1", Verb: "echo",
		Options:    map[string]any{"message": "hello"},
		Connection: map[string]any{"token": "s3cr3t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["message"] != "hello" {
		t.Fatalf("echo failed: %+v", out)
	}
	if out["received_token"] != true {
		t.Fatalf("plugin did not receive the credential: %+v", out)
	}
}

// TestExamplePluginShaMismatchRefused proves verify-before-execute against the
// real binary: a wrong pin refuses to launch.
func TestExamplePluginShaMismatchRefused(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	spec := Spec{Name: "echo", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: strings.Repeat("0", 64)}
	c := NewClient(spec, Deps{})
	defer c.Close()
	if err := c.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want sha256 mismatch refusal, got %v", err)
	}
}

// TestExamplePluginHangTimeoutAndRecovery proves supervision against a REAL
// unresponsive subprocess: a hung call returns via the per-call timeout (the
// daemon is not stuck), and a later call restarts the plugin and succeeds.
func TestExamplePluginHangTimeoutAndRecovery(t *testing.T) {
	bin, sum := buildExamplePlugin(t)
	spec := Spec{Name: "echo", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: sum, AllowUnsandboxed: true}
	c := NewClient(spec, Deps{CallTimeout: 300 * time.Millisecond})
	defer c.Close()
	ctx := context.Background()

	// A hung call must return (timeout), not block forever.
	start := time.Now()
	_, err := c.Invoke(ctx, InvokeRequest{Instance: "e1", Verb: "echo",
		Options: map[string]any{"message": "x", "hang": true}})
	if err == nil {
		t.Fatal("hung call should have timed out")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}

	// The daemon survives and recovers: a subsequent normal call restarts the
	// plugin and succeeds.
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "e1", Verb: "echo",
		Options: map[string]any{"message": "recovered"}})
	if err != nil {
		t.Fatalf("recovery call failed: %v", err)
	}
	if out["message"] != "recovered" {
		t.Fatalf("bad recovery output: %+v", out)
	}
}

// TestExamplePluginOversizeRejected proves the untrusted-output size cap: the
// plugin's --oversize mode emits a >8 MiB describe result, which must be
// rejected rather than buffered.
func TestExamplePluginOversizeRejected(t *testing.T) {
	bin, sum := buildExamplePlugin(t)
	spec := Spec{Name: "echo", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: sum, Args: []string{"--oversize"}, AllowUnsandboxed: true}
	c := NewClient(spec, Deps{CallTimeout: 5 * time.Second})
	defer c.Close()
	if _, err := c.Describe(context.Background()); err == nil {
		t.Fatal("want oversized-output rejection")
	}
}
