package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// transformYAML runs Transform and hands back the migrated document.
func transformYAML(t *testing.T, legacy string) (map[string]any, *Result) {
	t.Helper()
	res, err := Transform([]byte(legacy))
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(res.Output, &doc); err != nil {
		t.Fatalf("parse output: %v\n%s", err, res.Output)
	}
	return doc, res
}

// TestPagerdutyPrecedenceAndUnreachable: the pd rule chain migrates like
// sentry's — later rules exclude earlier matches, a rule after a catch-all
// is unreachable and generates no trigger (noted, never silent) — and the
// connection block carries every transport field.
func TestPagerdutyPrecedenceAndUnreachable(t *testing.T) {
	doc, res := transformYAML(t, `
integrations:
  - type: pagerduty
    name: pd
    listen: ":8097"
    smee_url: https://smee.io/pd
    path: /hooks/pd
    signing_secret: ${PD_SECRET}
    rules:
      - match: { services: [payments], urgencies: [high], event_types: [incident.triggered], priorities: [P1] }
        repo: acme/payments
        actions: { name: page, type: command, command: [true] }
      - match: {}                       # catch-all: everything else lands here
        actions:
          - { name: triage, type: command, command: [true] }
          - { name: log, type: command, command: [true] }
      - match: { services: [ignored] }  # after the catch-all: unreachable
        actions: { name: never, type: command, command: [true] }
`)
	conn := doc["connectors"].(map[string]any)["pd"].(map[string]any)
	for k, want := range map[string]string{
		"listen": ":8097", "smee_url": "https://smee.io/pd",
		"path": "/hooks/pd", "signing_secret": "${PD_SECRET}",
	} {
		if conn[k] != want {
			t.Errorf("conn.%s = %v, want %s", k, conn[k], want)
		}
	}
	trigs := doc["triggers"].([]any)
	if len(trigs) != 3 {
		t.Fatalf("triggers: %d, want 3 (page, triage, log — never is unreachable)", len(trigs))
	}
	// Rule 1: its own match, no exclude, repo pinned.
	first := trigs[0].(map[string]any)
	f := first["filters"].(map[string]any)
	if f["exclude"] != nil || first["repo"] != "acme/payments" {
		t.Fatalf("rule 1: %v", first)
	}
	if u := f["urgencies"].([]any); u[0] != "high" {
		t.Fatalf("rule 1 filters: %v", f)
	}
	// Rule 2 (both actions): excludes rule 1's match to preserve first-match.
	for i := 1; i <= 2; i++ {
		tr := trigs[i].(map[string]any)
		ex, ok := tr["filters"].(map[string]any)["exclude"].([]any)
		if !ok || len(ex) != 1 {
			t.Fatalf("rule 2 trigger %d exclude: %v", i, tr["filters"])
		}
		if ex[0].(map[string]any)["services"].([]any)[0] != "payments" {
			t.Fatalf("rule 2 exclude content: %v", ex)
		}
	}
	joined := strings.Join(res.Summary, "\n")
	if !strings.Contains(joined, "unreachable in legacy") {
		t.Fatalf("unreachable rule must be noted:\n%s", joined)
	}
	if !strings.Contains(joined, "preserve legacy first-match precedence") {
		t.Fatalf("exclusion note missing:\n%s", joined)
	}
}

// TestAutoMigrateGuards: import discovery refuses env-ref and bad-glob
// imports, and a migrated secretful config keeps its file mode.
func TestAutoMigrateGuards(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")

	os.WriteFile(main, []byte("imports: [${CONF_DIR}/x.yaml]\n"), 0o600)
	if _, _, err := AutoMigrate(main, func() error { return nil }, nil); err == nil ||
		!strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("env-ref import: %v", err)
	}

	os.WriteFile(main, []byte("imports: [\"[bad\"]\n"), 0o600)
	if _, _, err := AutoMigrate(main, func() error { return nil }, nil); err == nil ||
		!strings.Contains(err.Error(), "bad import glob") {
		t.Fatalf("bad glob: %v", err)
	}

	if _, _, err := AutoMigrate(filepath.Join(dir, "missing.yaml"), func() error { return nil }, nil); err == nil {
		t.Fatal("missing main config must error")
	}

	// Mode preservation: a 0640 legacy config migrates in place at 0640, and
	// the backup carries the same mode.
	legacy := `
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
`
	os.WriteFile(main, []byte(legacy), 0o640)
	os.Chmod(main, 0o640) // WriteFile is umask-clamped; pin the premise
	logged := 0
	n, _, err := AutoMigrate(main, func() error { return nil }, func(string, ...any) { logged++ })
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if logged == 0 {
		t.Fatal("migration must log")
	}
	for _, p := range []string{main, main + BackupSuffix} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode = %v, want 0640", p, fi.Mode().Perm())
		}
	}

	// The fallback mode for a file that cannot be stat'ed is 0600.
	if m := fileMode(filepath.Join(dir, "nope")); m != 0o600 {
		t.Fatalf("fileMode fallback: %v", m)
	}
}

// TestPrefixBranchSteps: parallel-branch step ids get the variant prefix and
// intra-branch references (both spellings, nested in options) follow.
func TestPrefixBranchSteps(t *testing.T) {
	steps := []config.Step{
		{ID: "fetch", Type: "command", Command: []string{"x"}},
		{ /* no id -> step2 */ Type: "command", Command: []string{"y"}},
		{ID: "post", Uses: "svc.post", Prompt: "see {{.fetch.out}} and {{.steps.fetch.outputs.out}}",
			Options: map[string]any{
				"text":   "{{.fetch.out}}",
				"nested": map[string]any{"deep": []any{"{{.step2.value}}", 7}},
			}},
	}
	got := prefixBranchSteps("v1", steps)
	if got[0].ID != "v1-fetch" || got[1].ID != "v1-step2" || got[2].ID != "v1-post" {
		t.Fatalf("ids: %s %s %s", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[2].Prompt != "see {{.v1-fetch.out}} and {{.steps.v1-fetch.outputs.out}}" {
		t.Fatalf("prompt rewrite: %s", got[2].Prompt)
	}
	if got[2].Options["text"] != "{{.v1-fetch.out}}" {
		t.Fatalf("options rewrite: %v", got[2].Options)
	}
	nested := got[2].Options["nested"].(map[string]any)["deep"].([]any)
	if nested[0] != "{{.v1-step2.value}}" || nested[1] != 7 {
		t.Fatalf("nested rewrite: %v", nested)
	}
}

// TestHandoffWebTunnelAndErrors: the web hand-off carries every tunnel
// field; a name collision with an integration and an empty hand-off block
// both hard-error.
func TestHandoffWebTunnelAndErrors(t *testing.T) {
	doc, _ := transformYAML(t, `
integrations:
  - type: cron
    name: timer
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
handoffs:
  hoff:
    web:
      base_url: https://c.example.com
      listen: ":8099"
      ttl: 45m
      tunnel:
        provider: ngrok
        host: h.example
        mode: http
        ssh_host: bastion
        authtoken: ${NGROK_TOKEN}
        url_pattern: "https://(\\S+)"
        command: [my-tunnel, --up]
        account: true
`)
	hoff := doc["connectors"].(map[string]any)["hoff"].(map[string]any)
	if hoff["type"] != "web" || hoff["base_url"] != "https://c.example.com" || hoff["ttl"] != "45m0s" {
		t.Fatalf("web connector: %v", hoff)
	}
	tun := hoff["tunnel"].(map[string]any)
	for k, want := range map[string]any{
		"provider": "ngrok", "host": "h.example", "mode": "http",
		"ssh_host": "bastion", "authtoken": "${NGROK_TOKEN}",
		"url_pattern": "https://(\\S+)", "account": true,
	} {
		if tun[k] != want {
			t.Errorf("tunnel.%s = %v, want %v", k, tun[k], want)
		}
	}
	if cmd := tun["command"].([]any); len(cmd) != 2 || cmd[0] != "my-tunnel" {
		t.Fatalf("tunnel command: %v", tun["command"])
	}

	// A hand-off named like an integration collides.
	_, err := Transform([]byte(`
integrations:
  - type: cron
    name: hoff
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
handoffs:
  hoff: { web: { listen: ":1" } }
`))
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision: %v", err)
	}

	// None of web/slack/discord set.
	if _, err := handoffConnector("h", config.HandoffConfig{}, map[string]map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "none of web/slack/discord") {
		t.Fatalf("empty handoff: %v", err)
	}
}

// TestPaseoBinWithoutPaseoRuntime: a custom paseo_bin with only non-paseo
// controllers synthesizes a paseo runtime carrying the bin.
func TestPaseoBinWithoutPaseoRuntime(t *testing.T) {
	doc, res := transformYAML(t, `
paseo_bin: /opt/paseo/bin/paseo
controllers:
  deck: { type: agent-deck }
integrations:
  - type: cron
    name: timer
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
`)
	rts := doc["runtimes"].(map[string]any)
	p, ok := rts["paseo"].(map[string]any)
	if !ok || p["bin"] != "/opt/paseo/bin/paseo" {
		t.Fatalf("synthesized paseo runtime: %v", rts)
	}
	if deck := rts["deck"].(map[string]any); deck["type"] != "agent-deck" {
		t.Fatalf("carried controller: %v", rts)
	}
	if !strings.Contains(strings.Join(res.Summary, "\n"), "paseo_bin → runtimes.paseo.bin") {
		t.Fatalf("note missing: %v", res.Summary)
	}

	// With an existing paseo-type controller the bin lands on it instead.
	doc, _ = transformYAML(t, `
paseo_bin: /custom/paseo
controllers:
  main: { type: paseo }
integrations:
  - type: cron
    name: timer
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [x] }
`)
	rts = doc["runtimes"].(map[string]any)
	if m := rts["main"].(map[string]any); m["bin"] != "/custom/paseo" {
		t.Fatalf("patched runtime: %v", rts)
	}
	if _, dup := rts["paseo"]; dup {
		t.Fatalf("no synthetic runtime expected: %v", rts)
	}
}

// TestRSSTransformFeedGuards: a feed without a name refuses; intervals and
// match filters carry.
func TestRSSTransformFeedGuards(t *testing.T) {
	doc, _ := transformYAML(t, `
integrations:
  - type: rss
    name: upstream
    feeds:
      - name: releases
        url: https://example.com/atom
        interval: 30m
        match: "v[0-9]+"
        actions: { name: note, type: command, command: [x] }
`)
	feeds := doc["connectors"].(map[string]any)["upstream"].(map[string]any)["feeds"].(map[string]any)
	rel := feeds["releases"].(map[string]any)
	if rel["url"] != "https://example.com/atom" || rel["interval"] != "30m0s" {
		t.Fatalf("feed: %v", rel)
	}
	tr := doc["triggers"].([]any)[0].(map[string]any)
	if tr["on"] != "upstream.releases" || tr["filters"].(map[string]any)["match"] != "v[0-9]+" {
		t.Fatalf("trigger: %v", tr)
	}

	_, err := Transform([]byte(`
integrations:
  - type: rss
    name: upstream
    feeds:
      - url: https://example.com/atom
        actions: { name: note, type: command, command: [x] }
`))
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Fatalf("unnamed feed: %v", err)
	}
}

// TestTransformRefusals: the remaining hard-error paths name the construct.
func TestTransformRefusals(t *testing.T) {
	cases := []struct{ name, yaml, wantErr string }{
		{"control.enabled false", `
control: { enabled: false }
integrations:
  - type: cron
    name: t
    schedules: [ { name: s, cron: "* * * * *", action: { type: command, command: [x] } } ]`,
			"conductor pause"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Transform([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want %q, got %v", c.wantErr, err)
			}
		})
	}
}

var _ = fmt.Sprintf // keep fmt for future cases
