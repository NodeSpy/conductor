package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// FOLDERS WERE REMOVED, and a config that still files things in folders —
// the example config with folder: added everywhere it was read — loads and
// passes the full validation, with warnings and nothing else.
func TestValidateWithRemovedFolders(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(raw), "~/.config/conductor/github-app.pem", writeTempRSAKey(t), 1)
	for _, r := range []struct{ old, new string }{
		{"\n  platform-sre:\n", "\n  platform-sre:\n    folder: work/platform\n"},
		{"\n  platform:\n", "\n  platform:\n    folder: \"../not/valid\"\n"},
		{"\ntriggers:\n", "\ntriggers:\n  - name: filed\n    folder: [a, b]\n    on: manual\n    steps: [ { log: \"filed\" } ]\n"},
	} {
		if !strings.Contains(doc, r.old) {
			t.Fatalf("the example config no longer has %q", r.old)
		}
		doc = strings.Replace(doc, r.old, r.new, 1)
	}
	for _, v := range []string{"GH_WEBHOOK_SECRET", "GH_SMEE_URL", "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "CW_SECRET", "GH_PAT", "CONDUCTOR_INVOKE_TOKEN", "CONDUCTOR_INVOKE_HMAC"} {
		t.Setenv(v, "dummy-"+v)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("a config with folder: does not load: %v", err)
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a config with folder: fails validation: %v", err)
	}
	stack.Close()
	if got := len(cfg.RemovedFieldWarnings()); got != 3 {
		t.Fatalf("warnings: %v", cfg.RemovedFieldWarnings())
	}
}

// `conductor validate` on a config with folder: succeeds and prints the
// warnings.
func TestValidateCommandWarnsOnFolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := "runtimes:\n  paseo: { use: paseo, default: true }\nworkflows:\n  w:\n    folder: work\n    steps: [ { log: hi } ]\ntriggers:\n  - name: go\n    folder: work\n    on: manual\n    steps: [ { call: w } ]\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdValidate([]string{"--config", path}) })
	if err != nil {
		t.Fatalf("validate failed on a removed field: %v\n%s", err, out)
	}
	for _, want := range []string{"warning: workflows.w.folder: " + config.FolderRemoved, "warning: triggers.go.folder: " + config.FolderRemoved, "ok:"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output lacks %q:\n%s", want, out)
		}
	}
}
