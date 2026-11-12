package config

import (
	"strings"
	"testing"
	"time"
)

const progressBase = `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
`

// `at: progress` reports a run: a trigger's hook, never a step's; `every:`
// paces progress hooks only, and never below the floor.
func TestProgressHookPlacement(t *testing.T) {
	cfg, err := loadDoc(t, progressBase+`    hooks:
      - { at: progress, every: 1m, uses: timer.tick, options: {} }
      - { at: progress, every: 2s, uses: timer.tick, options: {} }
      - { at: progress, uses: timer.tick, options: {} }
    steps: [{ id: t, type: command, command: ["true"], progress: "checking" }]
`)
	if err != nil {
		t.Fatalf("a trigger-level progress hook: %v", err)
	}
	hs := cfg.Triggers[0].Hooks
	if hs[0].ProgressEvery() != time.Minute || hs[1].ProgressEvery() != MinProgressEvery || hs[2].ProgressEvery() != DefaultProgressEvery {
		t.Fatalf("pacing = %v %v %v", hs[0].ProgressEvery(), hs[1].ProgressEvery(), hs[2].ProgressEvery())
	}
	if cfg.Triggers[0].Steps[0].Progress != "checking" {
		t.Fatalf("step progress = %q", cfg.Triggers[0].Steps[0].Progress)
	}
	for name, doc := range map[string]string{
		"step-level progress": `    steps:
      - id: t
        type: command
        command: ["true"]
        hooks: [{ at: progress, uses: timer.tick, options: {} }]
`,
		"every on a start hook": `    hooks: [{ at: start, every: 1m, uses: timer.tick, options: {} }]
    steps: [{ id: t, type: command, command: ["true"] }]
`,
	} {
		if _, err := loadDoc(t, progressBase+doc); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "progress") {
			t.Errorf("%s: error does not say why: %v", name, err)
		}
	}
}

func TestDashboardPublicURL(t *testing.T) {
	if _, err := loadDoc(t, dashBase+"dashboard: { enabled: true, public_url: \"https://dash.example:7788\" }\n"); err != nil {
		t.Fatalf("a good public_url: %v", err)
	}
	for _, bad := range []string{"dash.example", "ftp://dash.example", "https://dash.example/#/runs"} {
		if _, err := loadDoc(t, dashBase+"dashboard: { enabled: true, public_url: \""+bad+"\" }\n"); err == nil {
			t.Errorf("public_url %q accepted", bad)
		}
	}
}
