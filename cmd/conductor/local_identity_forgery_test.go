package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// Finding 7 (LOW): the generic "failed to start" message loadConnectorPlugins
// produces for ANY start/describe failure unconditionally suggested
// `conductor plugin update <name>` — which only ever operates on a REMOTE,
// fetched plugin. A LOCAL development binary (`use: ./bin/widget`) has
// nothing for that command to reinstall; the most common way to actually
// HIT this message for a local ref is the identity-forgery refusal
// (Client.Describe: the plugin's self-reported Type disagreeing with the
// name config.Use derives from the binary's own FILE NAME — see
// config.localUseName), since an operator renaming or copying a local build
// is exactly the everyday local-dev mistake this surfaces.
//
// TestStartFailureHint is the direct, pure-function proof: local never
// mentions plugin update, remote always does.
func TestStartFailureHint(t *testing.T) {
	local := startFailureHint(plugin.Spec{Name: "widget", Local: true})
	if strings.Contains(local, "plugin update") {
		t.Fatalf("a local plugin's hint must never suggest `conductor plugin update`, got: %q", local)
	}
	if !strings.Contains(local, "rename the binary") || !strings.Contains(local, "use:") {
		t.Fatalf("a local plugin's hint must say to rename the binary or repoint use:, got: %q", local)
	}

	remote := startFailureHint(plugin.Spec{Name: "widget", Local: false})
	if !strings.Contains(remote, "conductor plugin update widget") {
		t.Fatalf("a remote plugin's hint must suggest `conductor plugin update <name>`, got: %q", remote)
	}
}

// copyAs copies src to a new file named name in the same directory,
// preserving the executable bit — standing in for "an operator's local
// binary whose FILE NAME is <name>", since config.localUseName derives the
// plugin's registered name from the path's basename, not from anything the
// binary declares about itself.
func copyAs(t *testing.T, src, name string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(filepath.Dir(src), name)
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestLocalIdentityForgeryHintNeverSuggestsPluginUpdate drives the ACTUAL
// end-to-end failure: the real test/plugins/acme-echo fixture (which
// describes itself as Type "acme-echo") is built, then copied to a file
// named "widget" and referenced as a LOCAL plugin — config.localUseName
// derives spec.Provides = "widget" from that file name, which disagrees
// with the plugin's self-reported Type ("acme-echo"), tripping Client.
// Describe's identity-forgery refusal. The resulting disabled reason must
// name the local-specific fix, never `conductor plugin update`.
func TestLocalIdentityForgeryHintNeverSuggestsPluginUpdate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, _ := buildAcmeEchoPing(t, false)
	widgetBin := copyAs(t, bin, "widget")
	t.Cleanup(func() { connector.UnregisterExternalType("widget") })

	doc := "connectors:\n  widget:\n    use: " + widgetBin + "\n"
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig([]string{"--config", cfgPath})
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	mgr, lerr := loadConnectorPlugins(cfg, secrets.New(), func(map[string]any) {}, nil)
	w.Close()
	os.Stderr = old
	logOut, _ := io.ReadAll(r)
	if lerr != nil {
		t.Fatalf("loadConnectorPlugins: %v", lerr)
	}
	t.Cleanup(func() { mgr.Close() })

	if !strings.Contains(string(logOut), "identity forgery") {
		t.Fatalf("expected the identity-forgery refusal to actually fire (test setup), got log:\n%s", logOut)
	}

	// The actionable hint lives in the registered Unavailable reason
	// (DisabledReason, checked below) — the raw daemon log line above only
	// ever prints the underlying describe error, not startFailureHint's
	// suggestion.
	deps := connector.Deps{Secrets: secrets.New(), Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	in, ok := reg.Get("widget")
	if !ok {
		t.Fatal("expected a registered (disabled) instance for widget")
	}
	if !strings.Contains(in.DisabledReason, "identity forgery") {
		t.Fatalf("widget's disabled reason should name the identity-forgery refusal, got: %q", in.DisabledReason)
	}
	if strings.Contains(in.DisabledReason, "plugin update") {
		t.Fatalf("widget's disabled reason must never suggest `conductor plugin update` (a local ref has nothing to reinstall): %q", in.DisabledReason)
	}
	if !strings.Contains(in.DisabledReason, "rename the binary") {
		t.Fatalf("widget's disabled reason must say to rename the binary or repoint use:, got: %q", in.DisabledReason)
	}
}
