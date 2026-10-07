package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginShowProbesLocalManifest is finding 7 (MEDIUM): SpecFromRef never
// resolves a Manifest for a LOCAL plugin (`use: ./path`) — there is no
// install-state record to source one from the way a remote plugin's
// Reconcile-time Describe leaves behind — so `plugin show` and
// `plugin list --caps` always read "no declared capabilities" for a local
// dev build, even one that genuinely declares real capabilities. Using the
// SAME single_process fixture TestLoadConnectorPluginsSharesSingleProcessPlugin
// ByDefault drives (test/plugins/acme-echo built with
// -X main.singleProcess=true, which declares Capabilities.Env and
// Capabilities.SingleProcess), `plugin show` must now probe the local
// binary and list both.
func TestPluginShowProbesLocalManifest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildSingleProcessEcho(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  echoer:\n    use: " + bin + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return cmdPluginShow([]string{"--config", path, "acme-echo"}) })
	if err != nil {
		t.Fatalf("plugin show acme-echo: %v\n%s", err, out)
	}
	if strings.Contains(out, "no declared capabilities") {
		t.Fatalf("plugin show must probe a local build's real capabilities, not read them as empty, got:\n%s", out)
	}
	if !strings.Contains(out, "single_process") {
		t.Fatalf("plugin show must list single_process for this fixture, got:\n%s", out)
	}
	if !strings.Contains(out, "ACME_ECHO_VAR_A") || !strings.Contains(out, "ACME_ECHO_VAR_B") {
		t.Fatalf("plugin show must list the fixture's declared env capability, got:\n%s", out)
	}
}

// TestPluginListCapsProbesLocalManifest is the `plugin list --caps` half of
// finding 7: the SAME gap, with the SAME fixture, through the other
// command the review flagged.
func TestPluginListCapsProbesLocalManifest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildSingleProcessEcho(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  echoer:\n    use: " + bin + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return cmdPluginList([]string{"--config", path, "--caps"}) })
	if err != nil {
		t.Fatalf("plugin list --caps: %v\n%s", err, out)
	}
	if strings.Contains(out, "no declared capabilities") {
		t.Fatalf("plugin list --caps must probe a local build's real capabilities, not read them as empty, got:\n%s", out)
	}
	if !strings.Contains(out, "single_process") {
		t.Fatalf("plugin list --caps must list single_process for this fixture, got:\n%s", out)
	}

	// A bare `plugin list` (no --caps) must still spawn nothing — no new
	// cost for the common case.
	bare, err := captureStdout(t, func() error { return cmdPluginList([]string{"--config", path}) })
	if err != nil {
		t.Fatalf("plugin list: %v\n%s", err, bare)
	}
	if strings.Contains(bare, "single_process") || strings.Contains(bare, "capabilities") {
		t.Fatalf("a bare `plugin list` must never probe or print capabilities, got:\n%s", bare)
	}
}
