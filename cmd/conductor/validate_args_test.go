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
}
