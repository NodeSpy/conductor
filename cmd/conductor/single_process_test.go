package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildSingleProcessEcho compiles test/plugins/acme-echo with its
// singleProcess build flag set — the single_process capability's fixture
// (pkg/plugin/wire.go Capabilities.SingleProcess), proving loadConnectorPlugins'
// cold-start promotion (cmd/conductor/plugins.go, Manager.PromoteSharedProcess)
// end to end: a plugin that declares it must get the shared-process path
// EVEN THOUGH the config below sets shared_process: nowhere, the same way
// the real tailscale exposure plugin needs its funnel lease refcount to be.
func buildSingleProcessEcho(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-echo")
	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.singleProcess=true",
		"-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build single_process acme-echo: %v\n%s", err, out)
	}
	return bin
}

// captureLogf redirects os.Stderr (what logf writes to) for the duration of
// fn and returns everything logged.
func captureLogf(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestLoadConnectorPluginsPromotesSingleProcessPlugin is the single_process
// capability's end-to-end proof at the cmd/conductor layer: TWO configured
// instances of a plugin that declares Capabilities.SingleProcess, with NO
// shared_process: anywhere in the config, must both be registered
// successfully and served by the SAME underlying *plugin.Client — the host
// treats the capability exactly like shared_process: true, regardless of
// what the operator wrote.
func TestLoadConnectorPluginsPromotesSingleProcessPlugin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildSingleProcessEcho(t)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	doc := fmt.Sprintf("connectors:\n  a:\n    use: %s\n  b:\n    use: %s\n", bin, bin)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}

	sec := secrets.New()
	var mgr *plugin.Manager
	logged := captureLogf(t, func() {
		mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	})
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	if !strings.Contains(logged, "acme-echo") || !strings.Contains(logged, "single_process") {
		t.Fatalf("expected a log line announcing the single_process promotion, got:\n%s", logged)
	}

	key := "connectors/acme-echo"
	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if !spec.SharedProcess {
		t.Fatal("a single_process plugin's Spec must be promoted to SharedProcess = true")
	}

	factory := mgr.InstanceClientFactory(key)
	ca, err := factory("a")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := factory("b")
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatal("both configured instances must resolve to the SAME *Client once the plugin is promoted to single_process")
	}

	// And both instances build cleanly through the real connector registry —
	// "both instances served", not just "the factory agrees".
	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		in, ok := reg.Get(n)
		if !ok || in.DisabledReason != "" {
			t.Fatalf("instance %s did not build cleanly: ok=%v disabled=%q", n, ok, in.DisabledReason)
		}
		out, err := in.Invoke(context.Background(), "echo", map[string]any{"message": "hi-" + n})
		if err != nil {
			t.Fatalf("instance %s: invoke: %v", n, err)
		}
		if out["message"] != "hi-"+n {
			t.Fatalf("instance %s: echo mismatch: %+v", n, out)
		}
	}
}

// TestPermissionLinesShowsSingleProcessReason is permissionLines'
// (cmd/conductor/plugins.go) display half: a plugin whose Spec is already
// promoted (SharedProcess = true via its Manifest.SingleProcess, exactly
// what SpecFromRef/PromoteSharedProcess would have produced), with no
// shared_process: in config, must show the union grant on one "grant (one
// shared process)" line AND a line explaining WHY — the plugin's own
// declaration, not the operator's config — so the operator understands why
// grants are unioned instead of shown per instance.
func TestPermissionLinesShowsSingleProcessReason(t *testing.T) {
	ref := config.PluginRef{
		Name:    "tailscale",
		Use:     config.Use{Kind: config.UseKindConnector, Name: "tailscale"},
		Network: []string{"controlplane.tailscale.com:443"},
		Instances: map[string]config.ConnectorGrant{
			"ts": {Network: []string{"controlplane.tailscale.com:443"}},
		},
	}
	spec := plugin.Spec{
		Name: "tailscale", Kind: plugin.KindConnector, Provides: "tailscale",
		SharedProcess: true,
		Manifest:      plugin.Manifest{SingleProcess: true},
	}
	lines := permissionLines(ref, spec)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "grant (one shared process):") {
		t.Fatalf("expected the shared-process grant line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "single_process") {
		t.Fatalf("expected a line explaining the single_process override, got:\n%s", joined)
	}
	if !strings.Contains(joined, "controlplane.tailscale.com:443") {
		t.Fatalf("expected the union network on the grant line, got:\n%s", joined)
	}

	// Without the capability (and no shared_process: either), the ordinary
	// per-instance display stands, with NO single_process line at all.
	plainSpec := plugin.Spec{Name: "tailscale", Kind: plugin.KindConnector, Provides: "tailscale"}
	plainLines := strings.Join(permissionLines(ref, plainSpec), "\n")
	if strings.Contains(plainLines, "single_process") {
		t.Fatalf("a plugin that does not declare single_process must show no such line:\n%s", plainLines)
	}
	if !strings.Contains(plainLines, "instance") {
		t.Fatalf("a plugin with no shared process at all must show per-instance lines, got:\n%s", plainLines)
	}
}
