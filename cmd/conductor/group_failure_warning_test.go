package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// Finding 4 (LOW): ExplodeRefs' second return value, a []plugin.GroupFailure
// naming every plugin whose configured instances it could NOT safely split
// into process groups (the "must never happen" isolation-fold invariant
// failing anyway — see internal/plugin/group.go's narrowRef), was silently
// discarded ("_") by every READ-ONLY command that builds its own throwaway
// stack: `conductor validate`'s fetchability check, `plugin list`, `plugin
// show`, and the pending-plugin retry log. Only the LIVE daemon path
// (pluginManagerForStack) actually surfaced it. A failure there means the
// affected plugin silently vanishes from these reports with no explanation
// — exactly what finding 6 (cmd/conductor/plugins.go's documented
// mitigation) was supposed to prevent everywhere, not just at boot.
//
// groupRef's own isolation pre-split makes a REAL GroupFailure unreachable
// through ordinary config input (see internal/plugin's own
// TestExplodeRefsPropagatesGroupFailure, which documents the same
// limitation) — so these tests inject one through the explodeRefs package
// var (plugins.go), the same kind of test seam already used for
// validateReleaseAPI, to drive each call site's actual production code path
// with a synthetic failure and prove it is surfaced, not dropped.

// stubExplodeRefsWithFailure returns a stand-in for plugin.ExplodeRefs that
// ignores its input and reports exactly one synthetic GroupFailure — a
// deterministic, injectable substitute for the real, practically
// untriggerable case.
func stubExplodeRefsWithFailure(name, reason string) func(map[string]config.PluginRef, string, *plugin.InstallState) (map[string]config.PluginRef, []plugin.GroupFailure) {
	return func(map[string]config.PluginRef, string, *plugin.InstallState) (map[string]config.PluginRef, []plugin.GroupFailure) {
		return map[string]config.PluginRef{}, []plugin.GroupFailure{{Key: "connectors/" + name, Name: name, Reason: reason}}
	}
}

func withStubExplodeRefs(t *testing.T, name, reason string) {
	t.Helper()
	old := explodeRefs
	explodeRefs = stubExplodeRefsWithFailure(name, reason)
	t.Cleanup(func() { explodeRefs = old })
}

func TestValidateReportsGroupFailureNotSilence(t *testing.T) {
	withStubExplodeRefs(t, "f4validatewarn", "isolation blocks that do not combine")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("connectors:\n  forge:\n    use: acme/conductor-plugins/connectors/f4validatewarn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath}) })
	if err != nil {
		t.Fatalf("a GroupFailure alone must not fail validate (same degrade-don't-crash posture as every other plugin problem here): %v", err)
	}
	if !strings.Contains(out, "f4validatewarn") || !strings.Contains(out, "isolation blocks that do not combine") {
		t.Fatalf("validate must name the plugin and the reason for a GroupFailure, got:\n%s", out)
	}
}

func TestValidateRequirePluginsFailsOnGroupFailure(t *testing.T) {
	withStubExplodeRefs(t, "f4validatereq", "isolation blocks that do not combine")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("connectors:\n  forge:\n    use: acme/conductor-plugins/connectors/f4validatereq\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath, "--require-plugins"}) }); err == nil {
		t.Fatal("--require-plugins must fail when a plugin could not be safely grouped, not silently pass")
	}
}

func TestCmdPluginListWarnsOnGroupFailure(t *testing.T) {
	withStubExplodeRefs(t, "f4listwarn", "isolation blocks that do not combine")
	args := writeCfg(t, "connectors:\n  forge: { use: acme/plugins/f4listwarn }\n")

	out, err := captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatalf("plugin list: %v", err)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "f4listwarn") || !strings.Contains(out, "isolation blocks that do not combine") {
		t.Fatalf("plugin list must warn, naming the plugin and the reason, got:\n%s", out)
	}
}

func TestCmdPluginShowReportsGroupFailureNotGenericNotFound(t *testing.T) {
	withStubExplodeRefs(t, "f4showwarn", "isolation blocks that do not combine")
	args := writeCfg(t, "connectors:\n  forge: { use: acme/plugins/f4showwarn }\n")

	_, err := captureStdout(t, func() error { return cmdPluginShow(append(append([]string(nil), args...), "f4showwarn")) })
	if err == nil {
		t.Fatal("expected an error for a plugin ExplodeRefs could not group")
	}
	if strings.Contains(err.Error(), "no plugin, connector type, or runtime named") {
		t.Fatalf("plugin show must report the GroupFailure's own reason, not the generic not-found error that silently discards it: %v", err)
	}
	if !strings.Contains(err.Error(), "f4showwarn") || !strings.Contains(err.Error(), "isolation blocks that do not combine") {
		t.Fatalf("plugin show's error must name the plugin and the reason, got: %v", err)
	}
}

func TestPendingPluginsStillMissingLogsGroupFailure(t *testing.T) {
	withStubExplodeRefs(t, "f4pendingwarn", "isolation blocks that do not combine")
	cfg, _, err := loadConfig(writeCfg(t, "connectors:\n  forge: { use: acme/plugins/f4pendingwarn }\n"))
	if err != nil {
		t.Fatal(err)
	}
	state := plugin.LoadInstallState(t.TempDir())

	old := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	pendingPluginsStillMissing(cfg, state)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "f4pendingwarn") || !strings.Contains(string(out), "isolation blocks that do not combine") {
		t.Fatalf("the pending-plugin retry log must name the plugin and the reason for a GroupFailure, got:\n%s", out)
	}
}
