package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
)

// buildEcho compiles the reference acme-echo plugin for a real-wire test.
func buildEcho(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-echo")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(bin)
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// TestExternalInvokeRejectsBadOutputOverRealWire proves §8.2 end-to-end: a real
// subprocess returning an undeclared/mistyped field is rejected by schema
// validation in the connector RPC-proxy — not just when a Go map is fed to the
// validator directly.
func TestExternalInvokeRejectsBadOutputOverRealWire(t *testing.T) {
	bin, sum := buildEcho(t)
	cl := plugin.NewClient(plugin.Spec{
		Name: "echo", Kind: plugin.KindConnector, Provides: "acme-echo", BinPath: bin, Sha256: sum,
		AllowUnsandboxed: true,
	}, plugin.Deps{})
	defer cl.Close()

	decl, err := cl.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e := &externalImpl{client: cl, instance: "e1", decl: mapDecl(decl), pluginRef: "echo@1"}

	// Well-formed call passes.
	if _, err := e.Invoke(context.Background(), "echo", map[string]any{"message": "ok"}); err != nil {
		t.Fatalf("valid invoke failed: %v", err)
	}
	// badoutput → the plugin returns an undeclared "surprise" field; schema
	// validation (missing required "message", unknown key) must reject it.
	if _, err := e.Invoke(context.Background(), "echo", map[string]any{"message": "x", "badoutput": true}); err == nil {
		t.Fatal("malformed real-wire output should have been rejected")
	} else if !strings.Contains(err.Error(), "output") {
		t.Fatalf("want schema-validation error, got %v", err)
	}
}
