package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"encoding/json"

	"github.com/NodeSpy/conductor/internal/config"
)

const demoKit = `pack:
  name: demo-kit
  version: 1.0.0
  description: a demo bot template
  tags: [demo]
bots:
  keeper:
    description: Keeps an eye on the state store
    charter: "Keep it tidy."
    connectors:
      store: { use: kv }
      schedule: { use: cron, required: false }
    verbs:
      store.get: observe
      store.contains: observe
      store.set: { max: reversible }
      store.delete: propose
    triggers:
      - { name: weekly, on: schedule.weekly, action: consolidate }
`

func demoConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "packs", "demo-kit"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "packs", "demo-kit", config.PackManifestFile), []byte(demoKit), 0o600)
	p := filepath.Join(dir, "config.yaml")
	cfg := `# my config
runtimes:
  paseo: { use: paseo, default: true }
  clibot: { use: cli, tool: claude-code }
operators:
  ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample ops@x" }
connectors:
  state: { use: kv }
  timer: { use: cron, schedules: { weekly: { cron: "0 9 * * 1" } } }
packs:
  kit: { use: ./packs/demo-kit }
`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// `conductor bot new`: planned on a copy, validated, a diff; nothing
// written until asked; a second bot from the same template skips the
// trigger whose schedule is taken; the copy never lingers.
func TestBotNewPlan(t *testing.T) {
	p := demoConfig(t)
	before, _ := os.ReadFile(p)
	plan, err := planNewBot(p, config.TemplateBotDraft{Name: "keeper"}, "kit/keeper")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || plan.Widening || plan.Review == nil || !plan.Review.Practice || strings.Join(plan.Review.ReadOnly, ",") != "state.contains,state.get" || len(plan.Review.Widenings) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(plan.Diff, "+    use: kit/keeper") || !strings.Contains(plan.Diff, "+  - name: keeper-weekly") || !strings.Contains(plan.Diff, "+    enabled: false") {
		t.Fatalf("diff:\n%s", plan.Diff)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("a plan wrote the config")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".conductor-plan-*")); len(left) != 0 {
		t.Fatalf("plan copies left behind: %v", left)
	}
	if err := writeConfig(p, plan.Config); err != nil {
		t.Fatal(err)
	}
	// A second bot: widened at instantiation, its schedule trigger skipped.
	plan2, err := planNewBot(p, config.TemplateBotDraft{Name: "keeper-2", Grants: map[string]string{"store.set": "reversible"}}, "kit/keeper")
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Valid || !plan2.Widening || !strings.Contains(strings.Join(plan2.Review.Widenings, ";"), "state.set at reversible") ||
		strings.Contains(plan2.Diff, "keeper-2-weekly") || !strings.Contains(strings.Join(plan2.Notes, "\n"), "timer.weekly already drives trigger keeper-weekly") {
		t.Fatalf("second plan: %+v\n%s", plan2, plan2.Diff)
	}
	for _, bad := range []struct {
		d    config.TemplateBotDraft
		ref  string
		want string
	}{
		{config.TemplateBotDraft{Name: "keeper"}, "kit/keeper", `a bot named "keeper" exists`},
		{config.TemplateBotDraft{Name: "k3", Grants: map[string]string{"store.delete": "reversible"}}, "kit/keeper", "looser than template kit/keeper allows — at most propose"},
		{config.TemplateBotDraft{Name: "k3", Grants: map[string]string{"store.drop": "never"}}, "kit/keeper", "does not request it"},
		{config.TemplateBotDraft{Name: "k3"}, "conductor-packs/demo-kit/keeper", "not an installed template"},
		{config.TemplateBotDraft{Name: "k3", Connectors: map[string]string{"nope": "state"}}, "kit/keeper", "not an input of template"},
		{config.TemplateBotDraft{Name: "Bad Name"}, "kit/keeper", "lowercase letters"},
	} {
		if _, err := planNewBot(p, bad.d, bad.ref); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%+v %s: %v (want %q)", bad.d, bad.ref, err, bad.want)
		}
	}
	// `conductor bot grant`: a widening on a copy.
	g, err := planGrant(p, "keeper", map[string]string{"store.delete": "propose"})
	if err != nil || !g.Valid || !g.Widening || !strings.Contains(g.Diff, "+      store.delete: propose") {
		t.Fatalf("grant plan: %+v %v", g, err)
	}
	if _, err := planGrant(p, "keeper", map[string]string{"store.delete": "observe"}); err == nil {
		t.Fatal("a grant past the maximum")
	}
}

// The dashboard's ops on the control socket: catalog, one template, a plan.
func TestTemplateControlOps(t *testing.T) {
	p := demoConfig(t)
	old := botConfigPath
	botConfigPath = p
	defer func() { botConfigPath, planFull = old, true }()
	call := func(sub string, args map[string]any) controlResponse {
		a, b := net.Pipe()
		go templateControl(a, controlRequest{Cmd: "bot", Sub: sub, Args: args})
		var resp controlResponse
		if err := json.NewDecoder(b).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		b.Close()
		return resp
	}
	r := call("templates", map[string]any{"installed": "true"})
	ts, _ := r.Result["templates"].([]any)
	if r.Error != "" || len(ts) != 1 || ts[0].(map[string]any)["ref"] != "kit/keeper" {
		t.Fatalf("templates: %+v", r)
	}
	r = call("template", map[string]any{"ref": "kit/keeper", "registry": filepath.Join(filepath.Dir(p), "packs")})
	if r.Error != "" || r.Result["charter"] != "Keep it tidy." {
		t.Fatalf("template: %+v", r)
	}
	r = call("template_plan", map[string]any{"name": "keeper", "template": "kit/keeper", "grants": map[string]any{"store.set": "reversible"}})
	if r.Error != "" || r.Result["valid"] != true || r.Result["widening"] != true || !strings.Contains(r.Result["diff"].(string), "store.set: reversible") {
		t.Fatalf("plan: %+v", r)
	}
	if r := call("grant_plan", map[string]any{"bot": "ghost", "grants": map[string]any{"store.set": "observe"}}); r.Error == "" {
		t.Fatal("a grant plan for no bot")
	}
}
