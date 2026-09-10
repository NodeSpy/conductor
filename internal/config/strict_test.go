package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadDoc(t *testing.T, doc string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

const strictBase = `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
`

// Regression: unknown config keys were silently dropped (no KnownFields), so
// a typo'd known_hosts/filters/approvers setting simply didn't apply — with
// no error and no trace. Every typo below must be a named load error.
func TestStrictYAMLUnknownKeysAreNamedErrors(t *testing.T) {
	cases := []struct{ name, doc, wantKey string }{
		{"top level", strictBase + `
notifyy: { push: true }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`, "notifyy"},
		{"trigger level (custom unmarshaler)", strictBase + `
triggers:
  - on: timer.tick
    filtres: { branch: main }
    steps: [{ id: t, type: command, command: ["true"] }]
`, "filtres"},
		{"hosts entry", strictBase + `
hosts:
  build: { host: build01, user: ci, known_hostss: /tmp/kh }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`, "known_hostss"},
		{"step level", strictBase + `
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"], approve: true }]
`, "approve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadDoc(t, tc.doc)
			if err == nil {
				t.Fatalf("typo'd key %q loaded without error", tc.wantKey)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("error must name the unknown key %q, got: %v", tc.wantKey, err)
			}
		})
	}
}

// An unknown key inside an imported section file is a named error too.
func TestStrictYAMLImportedFileUnknownKey(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": strictBase + `
triggers:
  - imports: [triggers/*.yaml]
`,
		"triggers/tick.yaml": `
- on: timer.tick
  grupo: { key: "{{.repo}}" }
  steps: [{ id: t, type: command, command: ["true"] }]
`,
	})
	_, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "grupo") {
		t.Fatalf("typo'd key in an imported trigger file must be a named error, got: %v", err)
	}
}
