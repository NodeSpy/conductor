package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
