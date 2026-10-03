package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
)

// `validate <path>` must validate THAT file, not silently fall back to the
// default config (the old footgun). We assert on WHICH path load was attempted.
func TestValidateHonorsAPositionalConfigPath(t *testing.T) {
	miss := filepath.Join(t.TempDir(), "only-here.yaml") // does not exist
	err := cmdValidate([]string{miss})
	if err == nil || !strings.Contains(err.Error(), miss) {
		t.Fatalf("validate <path> must load THAT path (err should name %q), got: %v", miss, err)
	}
}

func TestValidateRejectsAmbiguousConfig(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	os.WriteFile(a, []byte("connectors: {}\n"), 0644)
	err := cmdValidate([]string{"--config", a, filepath.Join(dir, "b.yaml")})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("--config + a bare positional must be refused as ambiguous, got: %v", err)
	}
}

func TestValidateRejectsExtraPositionals(t *testing.T) {
	err := cmdValidate([]string{"a.yaml", "b.yaml"})
	if err == nil || !strings.Contains(err.Error(), "single config path") {
		t.Fatalf("two positional paths must be refused, got: %v", err)
	}
}

// fakeReleaseAPI stubs plugin.ReleaseAPI for the fetchability check. Download
// always errors — checkPluginFetchability must never call it (it is
// read-only, no install).
type fakeReleaseAPI struct {
	tags []string
	err  error
}

func (f fakeReleaseAPI) ListTags(plugin.RemoteSource) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tags, nil
}

func (f fakeReleaseAPI) Download(plugin.RemoteSource, string, string, string) (string, error) {
	return "", errors.New("validate must never download a plugin")
}

// TestValidateReportsPluginFetchability: plugin-contract.md §5.2 step 1 / Q12
// / Q13 promise that `conductor validate` reports, for a referenced plugin
// it does not have installed, whether it can actually be fetched from this
// machine. A plugin that can't be fetched must be a WARNING (Q12: the daemon
// still boots with just that connector disabled), never a validate failure.
func TestValidateReportsPluginFetchability(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty install state: nothing "installed"
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	doc := "connectors:\n  forge:\n    use: acme/conductor-plugins/connectors/widget@~>1.0\n"
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	oldAPI := validateReleaseAPI
	t.Cleanup(func() { validateReleaseAPI = oldAPI })

	t.Run("fetchable", func(t *testing.T) {
		validateReleaseAPI = fakeReleaseAPI{tags: []string{"connectors/widget/v1.2.0"}}
		out, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath}) })
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if !strings.Contains(out, "fetchable:") || !strings.Contains(out, "v1.2.0") {
			t.Fatalf("want a fetchable report naming the resolved tag, got:\n%s", out)
		}
	})

	t.Run("not fetchable is a warning, not a failure", func(t *testing.T) {
		validateReleaseAPI = fakeReleaseAPI{err: errors.New("network unreachable")}
		out, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath}) })
		if err != nil {
			t.Fatalf("an unfetchable plugin must not fail validate (Q12: boot still proceeds degraded): %v", err)
		}
		if !strings.Contains(out, "warning:") || !strings.Contains(out, "not fetchable") {
			t.Fatalf("want a not-fetchable warning, got:\n%s", out)
		}
		if !strings.Contains(out, "ok:") {
			t.Fatalf("validate must still report ok overall despite the warning, got:\n%s", out)
		}
	})

	t.Run("--require-plugins makes it a failure", func(t *testing.T) {
		validateReleaseAPI = fakeReleaseAPI{err: errors.New("network unreachable")}
		if _, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath, "--require-plugins"}) }); err == nil ||
			!strings.Contains(err.Error(), "neither installed nor fetchable") {
			t.Fatalf("want a failure naming the missing plugin, got %v", err)
		}
		validateReleaseAPI = fakeReleaseAPI{tags: []string{"connectors/widget/v1.2.0"}}
		if _, err := captureStdout(t, func() error { return cmdValidate([]string{cfgPath, "--require-plugins"}) }); err != nil {
			t.Fatalf("a fetchable plugin passes --require-plugins: %v", err)
		}
	})
}

// An installed plugin satisfies --require-plugins only at a version the
// config's pin accepts: a pin raised past the installed build must be
// fetchable, or the check fails.
func TestRequirePluginsChecksTheInstalledVersionAgainstThePin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st := plugin.LoadInstallState(plugin.InstallDir())
	st.Put(plugin.Installed{Key: "connectors/widget", Kind: "connector", Name: "widget",
		Use: "acme/conductor-plugins/connectors/widget", Resolved: "connectors/widget/v1.0.0"})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	oldAPI := validateReleaseAPI
	t.Cleanup(func() { validateReleaseAPI = oldAPI })
	validateReleaseAPI = fakeReleaseAPI{err: errors.New("network unreachable")}
	write := func(pin string) string {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("connectors:\n  forge:\n    use: acme/conductor-plugins/connectors/widget"+pin+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := captureStdout(t, func() error { return cmdValidate([]string{write("@~>1.0"), "--require-plugins"}) }); err != nil {
		t.Fatalf("the installed v1.0.0 satisfies ~>1.0: %v", err)
	}
	if _, err := captureStdout(t, func() error { return cmdValidate([]string{write("@>=1.2"), "--require-plugins"}) }); err == nil {
		t.Fatal("a pin past the installed build, with nothing fetchable, passed --require-plugins")
	}
}
