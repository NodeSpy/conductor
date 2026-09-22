package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pack can reference files it SHIPS via ${pack.dir}, which resolves at
// instantiation to the pack's own vendored directory (absolute) — so a step can
// run `python3 ${pack.dir}/scripts/x.py` with no consumer-supplied path.
func TestPackDirReferenceResolves(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/filekit", `
pack: { name: filekit, version: 1.0.0 }
workflows:
  run:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "python3 ${pack.dir}/scripts/do.py --flag"]
`)
	body := `
connectors:
  timer: { use: cron, schedules: { t: { every: 1h } } }
runtimes:
  claude: { use: cli, tool: claude-code, default: true }
packs:
  fk:
    source: ` + filepath.Join(dir, "src/filekit") + `
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	wf, ok := cfg.Workflows["fk/run"]
	if !ok {
		t.Fatalf("expected fk/run, have %v", workflowKeys(cfg))
	}
	cmd := strings.Join([]string(wf.Steps[0].Command), " ")
	if strings.Contains(cmd, "${pack.dir}") {
		t.Fatalf("${pack.dir} was not substituted: %q", cmd)
	}
	if !strings.Contains(cmd, filepath.Join(".conductor", "packs", "fk")) {
		t.Fatalf("expected the vendored pack dir in the command, got %q", cmd)
	}
	if !strings.Contains(cmd, "/scripts/do.py --flag") {
		t.Fatalf("path suffix should be preserved, got %q", cmd)
	}
}
