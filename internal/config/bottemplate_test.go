package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const sreKitManifest = `
pack:
  name: sre-kit
  version: 1.0.0
  description: an SRE bot template
  tags: [sre, oncall]
bots:
  first-line:
    description: First-line SRE for the platform
    charter_file: charter.md
    scope: compute and deploys
    seed: seed
    collections:
      postmortems: { from: collections/postmortems.yaml }
    connectors:
      gh: { use: github, desc: "the repos it reads" }
      alerts: { use: github, required: false }
    verbs:
      gh.get_pr: observe
      gh.list_prs: observe
      gh.comment: { max: reversible, why: "notes on the incident PR" }
      gh.merge: propose
      gh.buy_*: { max: commit_money, amount: total_usd }
      alerts.get_issue: observe
    triggers:
      - name: page
        on: gh.issue_opened
        title: "page: {{.title}}"
        urgent: true
      - name: weekly
        on: alerts.schedule
        action: consolidate
    proactive: { every: 4h, checklist: { section: "Proactive checks" }, notify_at: high }
    model: claude-opus-5
    runtime: "a use: cli runtime with claude-code"
`

func writeSREKit(t *testing.T, dir, manifest string) {
	t.Helper()
	p := filepath.Join(dir, "packs", "sre-kit")
	writePackSource(t, dir, "packs/sre-kit", manifest)
	for rel, body := range map[string]string{
		"charter.md":                    "You are a careful SRE.\n\n## Proactive checks\n\n- disks\n",
		"collections/postmortems.yaml":  "dir: postmortems\nfields: { title: { type: string, required: true } }\nwriters: [operator, bots]\n",
		"seed/knowledge/topology.md":    "---\ntitle: Topology\nevidence: e\nconfidence: high\n---\nthree replicas\n",
		"seed/runbooks/restart.md":      "---\ntitle: Restart\nwhen_to_use: hung\nsteps: [restart]\nverification: up\nrollback: none\nowner: sre\n---\n",
		"seed/watch/disk.md":            "---\nwhat: disk\nsignal: df\nthreshold: 90%\nwhy: outages\n---\n",
		"seed/README.md":                "not memory",
		"seed/knowledge/not-a-doc.txt":  "skipped",
		"seed/postmortems/old.md":       "---\ntitle: old\n---\n",
		"seed/knowledge/nested/deep.md": "---\ntitle: x\n---\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(p, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(p, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// loadTemplateCfg writes the pack and a config with body, resolves and loads.
func loadTemplateCfg(t *testing.T, manifest, body string) (*Config, string, error) {
	t.Helper()
	return loadTemplateCfgConn(t, manifest, "", body)
}

// loadTemplateCfgConn is loadTemplateCfg with more connectors.
func loadTemplateCfgConn(t *testing.T, manifest, conns, body string) (*Config, string, error) {
	t.Helper()
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	writeSREKit(t, dir, manifest)
	p := filepath.Join(dir, "config.yaml")
	base := strings.Replace(botsBase, "connectors:\n", "connectors:\n"+conns, 1)
	if err := os.WriteFile(p, []byte(base+"packs:\n  kit: { use: ./packs/sre-kit }\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveAndLoad(t, p)
	return c, p, err
}

func sorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

// A NEW BOT FROM A TEMPLATE takes its behavior and only the verbs its
// operator granted: in practice mode, under the pack instance's placement.
func TestTemplateBotTakesBehaviorNotVerbs(t *testing.T) {
	c, _, err := loadTemplateCfg(t, sreKitManifest, `bots:
  sre:
    use: kit/first-line
    grant: { gh.get_pr: observe, gh.list_prs: observe }
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Bots["sre"]
	if m.Description != "First-line SRE for the platform" || m.Scope != "compute and deploys" || !strings.HasSuffix(m.SeedDir, filepath.Join("packs", "kit", "seed")) {
		t.Fatalf("behavior not applied: %+v", m)
	}
	if strings.Join(sorted(m.Skill.Verbs), ",") != "gh.get_pr,gh.list_prs" || strings.Join(sorted(m.Autonomy.Tiers[TierObserve]), ",") != "gh.get_pr,gh.list_prs" || len(m.Autonomy.Tiers) != 1 {
		t.Fatalf("verbs: %v tiers: %v", m.Skill.Verbs, m.Autonomy.Tiers)
	}
	if !m.Autonomy.ShadowMode() || m.Origin == nil || !m.Origin.Practice || len(m.Origin.Widenings) != 0 {
		t.Fatalf("not in practice mode / widenings: %+v", m.Origin)
	}
	if m.Folder != "packs/kit" {
		t.Fatalf("folder: %q", m.Folder)
	}
	if _, ok := m.Collections["postmortems"]; !ok {
		t.Fatal("the template's collection")
	}
	if !m.Home.Model.Set() || m.Home.Model.Acceptable()[0] != "claude-opus-5" {
		t.Fatal("suggested model")
	}
	ch, err := c.BotCharter("sre")
	if err != nil || !strings.Contains(ch, "careful SRE") {
		t.Fatalf("charter: %q %v", ch, err)
	}
	o := m.Origin
	if o.Ref() != "kit/first-line" || o.Pack != "sre-kit" || o.Version != "1.0.0" || o.Digest == "" || o.Bindings["gh"] != "gh" {
		t.Fatalf("origin: %+v", o)
	}
	if o.Ungranted["gh.comment"] != TierReversible || o.Ungranted["gh.merge"] != TierPropose || len(o.Ungranted) != 4 {
		t.Fatalf("ungranted: %v", o.Ungranted)
	}
	notes := strings.Join(c.BotTemplateNotes(), "\n")
	if !strings.Contains(notes, "bot sre: from template kit/first-line (sre-kit 1.0.0): practice mode; read-only: gh.get_pr, gh.list_prs") || !strings.Contains(notes, "requested, not granted: alerts.get_issue (at most observe)") {
		t.Fatalf("notes:\n%s", notes)
	}
	if o.Bindings["alerts"] != "gh" {
		t.Fatalf("the optional input binds to the one github connector too: %v", o.Bindings)
	}
}

// NOTHING A TEMPLATE REQUESTS IS ACTIVE WITHOUT A GRANT: no grant, no verbs.
func TestTemplateRequestsNeedAGrant(t *testing.T) {
	c, _, err := loadTemplateCfg(t, sreKitManifest, "bots:\n  sre: { use: kit/first-line }\n")
	if err != nil {
		t.Fatal(err)
	}
	m := c.Bots["sre"]
	if m.Skill != nil || len(m.Autonomy.Tiers) != 0 {
		t.Fatalf("an ungranted template bot has verbs: %v %v", m.Skill, m.Autonomy.Tiers)
	}
}

// A GRANT IS BOUNDED BY THE TEMPLATE: a tier looser than its maximum is cut
// to it (and said); an unrequested verb or unknown tier is an error; a money
// verb carries its amount.
func TestTemplateGrantBounds(t *testing.T) {
	c, _, err := loadTemplateCfg(t, sreKitManifest, `bots:
  sre:
    use: kit/first-line
    grant: { gh.comment: observe, gh.merge: reversible, gh.buy_*: propose, gh.get_pr: commit_money, gh.list_prs: never }
    autonomy: { spend_cap: { per_action: 10, per_period: 20 } }
`)
	if err != nil {
		t.Fatal(err)
	}
	tiers := c.Bots["sre"].Autonomy.Tiers
	want := map[string]string{"gh.comment": TierReversible, "gh.merge": TierPropose, "gh.buy_*": TierCommitMoney, "gh.get_pr": TierObserve, "gh.list_prs": TierNever}
	for v, tier := range want {
		found := false
		for _, x := range tiers[tier] {
			found = found || x == v
		}
		if !found {
			t.Errorf("%s not at %s: %v", v, tier, tiers)
		}
	}
	for tier, vs := range tiers {
		for _, v := range vs {
			if want[v] != tier {
				t.Errorf("%s also at %s", v, tier)
			}
		}
	}
	if c.Bots["sre"].Autonomy.Amounts["gh.buy_*"] != "total_usd" {
		t.Fatal("money verb without its amount")
	}
	notes := strings.Join(c.BotTemplateNotes(), "\n")
	for _, n := range []string{"grant gh.comment: observe is not within", "grant gh.merge: reversible is not within", "grant gh.get_pr: commit_money is not within", "grant gh.buy_*: propose is not within"} {
		if !strings.Contains(notes, n) {
			t.Errorf("no note %q", n)
		}
	}
	if !strings.Contains(notes, "WIDENING: gh.buy_* at commit_money; gh.comment at reversible; gh.merge at propose") {
		t.Fatalf("widenings: %s", notes)
	}
	for body, want := range map[string]string{
		"grant: { gh.delete_repo: observe }":     "grant: gh.delete_repo: not requested by template kit/first-line",
		"grant: { gh.get_pr: sometimes }":        `grant: gh.get_pr: tier "sometimes" is not one of`,
		"connectors: { nope: gh }":               "connectors.nope: not an input of template kit/first-line",
		"connectors: { gh: elsewhere }":          `connectors.gh: "elsewhere" is not one of your connectors`,
		"use: kit/second":                        `pack instance kit has no template "second"`,
		"use: other/first-line":                  `no pack instance "other" in packs:`,
		"use: first-line":                        "a template is named <pack instance>/<template>",
		"use: kit/first-line\n    team: nowhere": "",
	} {
		b := "bots:\n  sre:\n    use: kit/first-line\n    " + body + "\n"
		if strings.HasPrefix(body, "use:") {
			b = "bots:\n  sre:\n    " + body + "\n"
		}
		_, _, err := loadTemplateCfg(t, sreKitManifest, b)
		if want == "" {
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s:\n  %v\n  want %q", body, err, want)
		}
	}
	if _, _, err := loadTemplateCfg(t, sreKitManifest, "bots:\n  plain: { grant: { gh.get_pr: observe } }\n"); err == nil || !strings.Contains(err.Error(), "set use: <pack instance>/<template>") {
		t.Fatalf("a grant without a template: %v", err)
	}
}

// What the operator widens is said: own verbs, practice mode off, promotion.
func TestTemplateWideningsAreNamed(t *testing.T) {
	c, _, err := loadTemplateCfg(t, sreKitManifest, `bots:
  sre:
    use: kit/first-line
    grant: { gh.comment: reversible }
    skill: { verbs: [gh.close_issue] }
    autonomy: { shadow: false, promote: [gh.comment] }
`)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Bots["sre"].Origin.Widenings, "; ")
	for _, want := range []string{"gh.comment at reversible", "own verb gh.close_issue", "practice mode off", "promoted to run unapproved: gh.comment"} {
		if !strings.Contains(w, want) {
			t.Errorf("widenings lack %q: %s", want, w)
		}
	}
	if !strings.Contains(strings.Join(c.BotTemplateNotes(), "\n"), "PRACTICE MODE OFF") {
		t.Fatal("practice mode off is not loud")
	}
}

// Bindings: one connector of the type binds itself; two are the operator's
// choice; the wrong type, or none for a required input, is an error.
func TestTemplateInputBinding(t *testing.T) {
	two := sreKitManifest
	base := "  gh2: { use: github, token: y, repos: [\"acme/*\"] }\n"
	if _, _, err := loadTemplateCfgConn(t, two, base, "bots:\n  sre: { use: kit/first-line }\n"); err == nil || !strings.Contains(err.Error(), "connectors.alerts: ambiguous — you have 2 github connectors (gh, gh2)") {
		t.Fatalf("ambiguous: %v", err)
	}
	c, _, err := loadTemplateCfgConn(t, two, base, "bots:\n  sre: { use: kit/first-line, connectors: { gh: gh2, alerts: gh }, grant: { gh.get_pr: observe, alerts.get_issue: observe } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if v := strings.Join(sorted(c.Bots["sre"].Skill.Verbs), ","); v != "gh.get_issue,gh2.get_pr" {
		t.Fatalf("bound verbs: %s", v)
	}
	noGH := strings.Replace(sreKitManifest, "gh: { use: github, desc: \"the repos it reads\" }", "gh: { use: rss }", 1)
	if _, _, err := loadTemplateCfg(t, noGH, "bots:\n  sre: { use: kit/first-line }\n"); err == nil || !strings.Contains(err.Error(), "needs a rss connector and you have none") {
		t.Fatalf("a required input with no connector: %v", err)
	}
	if _, _, err := loadTemplateCfg(t, noGH, "bots:\n  sre: { use: kit/first-line, connectors: { gh: gh } }\n"); err == nil || !strings.Contains(err.Error(), `"gh" is a github connector; template kit/first-line needs a rss one here`) {
		t.Fatalf("the wrong type: %v", err)
	}
}

// Team ceilings apply on top of a template bot like any other.
func TestTemplateBotUnderTeamCeilings(t *testing.T) {
	_, _, err := loadTemplateCfg(t, sreKitManifest, `teams:
  platform: { routing: { fallback: sre }, autonomy: { ceiling: observe } }
bots:
  sre:
    use: kit/first-line
    team: platform
    grant: { gh.comment: reversible }
    autonomy: { promote: [gh.comment] }
`)
	if err == nil || !strings.Contains(err.Error(), "ceiling observe") {
		t.Fatalf("a template bot past its team's ceiling: %v", err)
	}
	c, _, err := loadTemplateCfg(t, sreKitManifest, "teams:\n  platform: { routing: { fallback: sre }, folder: work/p }\nbots:\n  sre: { use: kit/first-line, team: platform }\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Bots["sre"].Folder != "" {
		t.Fatalf("a team bot got the pack's folder: %q", c.Bots["sre"].Folder)
	}
}

// A TEMPLATE CANNOT SET what is the operator's: every forbidden key fails
// the pack, by name.
func TestTemplateForbiddenKeys(t *testing.T) {
	values := map[string]string{"operators": "[ops]", "approvers": "[ops]", "folder": "x/y", "team": "platform", "secrets": "{ a: b }",
		"skill": "{ verbs: [gh.merge] }", "grant": "{ gh.merge: observe }", "autonomy": "{ shadow: false }", "isolation": "{ sandbox: optional }",
		"browser": "{ logins: [x.example.com] }", "network": "[x:443]", "egress": "[x:443]", "memory_repo": "/tmp/m", "seed_dir": "/tmp/s",
		"memory": "{ index_budget: 60k }", "peers": "[other]", "terraform": "{ plan_connector: x }", "home": "{ mode: bypass }",
		"worker": "{ runtime: x }", "disabled": "true", "use": "kit/other", "charter_digest": "abc"}
	// The list is fixed here, not read from the map: dropping a key from
	// forbiddenTemplateKeys must fail this test.
	if len(values) != len(forbiddenTemplateKeys) {
		t.Fatalf("forbiddenTemplateKeys has %d keys, the test expects %d", len(forbiddenTemplateKeys), len(values))
	}
	for k, v := range values {
		man := strings.Replace(sreKitManifest, "    scope: compute and deploys\n", "    scope: compute and deploys\n    "+k+": "+v+"\n", 1)
		_, _, err := loadTemplateCfg(t, man, "")
		if err == nil || !strings.Contains(err.Error(), "a bot template cannot set "+k+" (") {
			t.Errorf("%s slipped through: %v", k, err)
		}
	}
	for frag, want := range map[string]string{
		"    proactive: { every: 4h, checklist: { section: \"Proactive checks\" }, notify_at: high }": "",
		"    proactive: { every: 4h, budget: { max_runs_per_day: 99 } }":                              "cannot set budget (a proactive budget spends the operator's money",
		"    verbs:\n      gh.*: observe":                                                             "never every verb of a connector",
		"    verbs:\n      pagerduty.ack: observe":                                                    `"pagerduty" is not one of the template's connector inputs`,
		"    verbs:\n      gh.close: never":                                                           `maximum tier "never" is not one of observe`,
		"    verbs:\n      gh.buy: commit_money":                                                      "a commit_money verb names its amount option",
		"    charter_file: ../outside.md":                                                             "leaves the pack",
		"    seed: /etc":                                                                              "must be a path inside the pack",
		"    connectors:\n      gh: my-gh-connector":                                                  "is not a version constraint — a template declares the connector it needs",
		"    triggers:\n      - { name: x, on: slack.message }":                                       "on: is <input>.<event>",
		"    credentials: { secrets_via: env }":                                                       "field credentials not found",
	} {
		man := sreKitManifest
		switch {
		case strings.HasPrefix(frag, "    verbs:"):
			man = strings.Replace(man, "    verbs:\n", frag+"\n", 1)
		case strings.HasPrefix(frag, "    connectors:"):
			man = strings.Replace(man, "    connectors:\n      gh: { use: github, desc: \"the repos it reads\" }\n", frag+"\n", 1)
		case strings.HasPrefix(frag, "    triggers:"):
			man = strings.Replace(man, "    triggers:\n", frag+"\n", 1)
		case strings.HasPrefix(frag, "    proactive:"):
			man = strings.Replace(man, "    proactive: { every: 4h, checklist: { section: \"Proactive checks\" }, notify_at: high }", frag, 1)
		case strings.HasPrefix(frag, "    charter_file:"):
			man = strings.Replace(man, "    charter_file: charter.md", frag, 1)
		case strings.HasPrefix(frag, "    seed:"):
			man = strings.Replace(man, "    seed: seed", frag, 1)
		case strings.HasPrefix(frag, "    credentials:"):
			// a key the forbidden list does not name: strict decoding is the backstop
			man = strings.Replace(man, "    scope: compute and deploys\n", "    scope: compute and deploys\n"+frag+"\n", 1)
		}
		_, _, err := loadTemplateCfg(t, man, "")
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", frag, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s:\n  %v\n  want %q", frag, err, want)
		}
	}
}

// `conductor bot new` / `grant`: spliced into the text, the rest of the file
// untouched, and the result loads.
func TestTemplateConfigEdits(t *testing.T) {
	c, p, err := loadTemplateCfg(t, sreKitManifest, "# my bots\nbots:\n  other: # keep me\n    description: \"x\"\n    worker: { runtime: claude-cli }\n# trailing comment\n")
	if err != nil {
		t.Fatal(err)
	}
	lt, err := c.Template("kit/first-line")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BotEntryYAML(TemplateBotDraft{Name: "sre", Template: lt, Grants: map[string]string{"gh.merge": TierReversible}}); err == nil || !strings.Contains(err.Error(), "looser than template kit/first-line allows — at most propose") {
		t.Fatalf("a grant past the maximum at write time: %v", err)
	}
	entry, err := BotEntryYAML(TemplateBotDraft{Name: "sre", Template: lt, Grants: map[string]string{"gh.comment": TierReversible}, Proactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry, "gh.get_pr: observe") || !strings.Contains(entry, "gh.comment: reversible") || strings.Contains(entry, "gh.merge") {
		t.Fatalf("entry:\n%s", entry)
	}
	// An optional input left unbound: its read-only requests are not granted.
	ub, _ := BotEntryYAML(TemplateBotDraft{Name: "sre", Template: lt, Unbound: []string{"alerts"}})
	if strings.Contains(ub, "alerts.get_issue") || !strings.Contains(ub, "gh.get_pr: observe") {
		t.Fatalf("unbound input's grants:\n%s", ub)
	}
	if ro := ReadOnlyGrants(lt.T); len(ro) != 3 || ro["gh.comment"] != "" {
		t.Fatalf("read-only grants: %v", ro)
	}
	src, _ := os.ReadFile(p)
	out, err := InsertTopLevel(src, "bots", entry)
	if err != nil {
		t.Fatal(err)
	}
	trig, skipped, err := TemplateTriggersYAML("sre", lt, map[string]string{"gh": "gh"}, nil)
	if err != nil || !strings.Contains(trig, "name: sre-page") || !strings.Contains(trig, "enabled: false") || strings.Contains(trig, "sre-weekly") || len(skipped) != 1 {
		t.Fatalf("triggers (alerts unbound → weekly skipped):\n%s %v %v", trig, skipped, err)
	}
	if again, skipped, _ := TemplateTriggersYAML("sre2", lt, map[string]string{"gh": "gh"}, map[string]string{"gh.issue_opened": "sre-page"}); again != "" || !strings.Contains(skipped[0], "gh.issue_opened already drives trigger sre-page") {
		t.Fatalf("a taken event: %q %v", again, skipped)
	}
	if out, err = InsertTopLevel(out, "triggers", trig); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"# my bots", "other: # keep me", "# trailing comment"} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("lost %q", keep)
		}
	}
	if !strings.HasPrefix(string(out), string(src[:len(src)-len("# trailing comment\n")])) {
		t.Errorf("the file before the insertion changed:\n%s", out)
	}
	_ = os.WriteFile(p, out, 0o600)
	c2, err := Load(p)
	if err != nil {
		t.Fatalf("edited config:\n%s\n%v", out, err)
	}
	if c2.Bots["sre"].Origin == nil || c2.Bots["other"].Description != "x" {
		t.Fatal("bots after the edit")
	}
	found := false
	for _, tr := range c2.Triggers {
		if tr.Name == "sre-page" && tr.Enabled != nil && !*tr.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatal("the suggested trigger is not there, disarmed")
	}
	// A grant on the existing entry.
	out2, err := SetBotGrants(out, "sre", map[string]string{"gh.merge": TierPropose})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, out2, 0o600)
	c3, err := Load(p)
	if err != nil {
		t.Fatalf("after grant:\n%s\n%v", out2, err)
	}
	if !strings.Contains(strings.Join(c3.Bots["sre"].Origin.Widenings, ";"), "gh.merge at propose") || c3.Bots["other"].Description != "x" {
		t.Fatalf("grant: %+v", c3.Bots["sre"].Origin)
	}
	if _, err := SetBotGrants(out, "ghost", map[string]string{"gh.merge": TierPropose}); err == nil {
		t.Fatal("a grant on no bot")
	}
	// No bots: section at all: appended.
	o, err := InsertTopLevel([]byte("store: {}\n"), "bots", "x: {}\n")
	if err != nil || string(o) != "store: {}\n\nbots:\n  x: {}\n" {
		t.Fatalf("append: %q %v", o, err)
	}
	o, err = InsertTopLevel([]byte("bots: {}\nstore: {}\n"), "bots", "x: {}\n")
	if err != nil || string(o) != "bots:\n  x: {}\nstore: {}\n" {
		t.Fatalf("empty flow: %q %v", o, err)
	}
	if _, err := InsertTopLevel([]byte("bots: { a: {} }\n"), "bots", "x: {}\n"); err == nil {
		t.Fatal("a non-empty flow section")
	}
}

// The catalog: installed templates with their bots, a registry's, search.
func TestTemplateCatalog(t *testing.T) {
	c, p, err := loadTemplateCfg(t, sreKitManifest, "bots:\n  sre: { use: kit/first-line }\n")
	if err != nil {
		t.Fatal(err)
	}
	in := c.InstalledTemplates(false)
	if len(in) != 1 || in[0].Ref != "kit/first-line" || in[0].ReadOnly != 3 || in[0].Above != 3 || strings.Join(in[0].UsedBy, ",") != "sre" || len(in[0].Seed) != 5 {
		t.Fatalf("installed: %+v", in)
	}
	reg := filepath.Join(filepath.Dir(p), "packs")
	ts, problems := RegistryTemplates(reg, true)
	if len(problems) != 0 || len(ts) != 1 || ts[0].Ref != "conductor-packs/sre-kit/first-line" || !strings.Contains(ts[0].Install, "use: conductor-packs/sre-kit") {
		t.Fatalf("registry: %+v %v", ts, problems)
	}
	if len(SearchTemplates(ts, "oncall")) != 1 || len(SearchTemplates(ts, "sre merge")) != 1 || len(SearchTemplates(ts, "billing")) != 0 {
		t.Fatal("search")
	}
	if d, _, err := RegistryDir(reg); err != nil || d != reg {
		t.Fatalf("a local registry: %s %v", d, err)
	}
	if _, _, err := RegistryDir("git://example.com/x"); err == nil {
		t.Fatal("a plaintext registry")
	}
}

// What the operator sets wins over the template's: description, a
// collection of the same name.
func TestTemplateOperatorWins(t *testing.T) {
	c, _, err := loadTemplateCfg(t, sreKitManifest, `bots:
  sre:
    use: kit/first-line
    description: "my own words"
    collections:
      postmortems: { dir: reviews, writers: [operator], fields: { title: { type: string, required: true } } }
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Bots["sre"]
	if m.Description != "my own words" || m.Collections["postmortems"].Dir != "reviews" || m.Scope != "compute and deploys" {
		t.Fatalf("operator's settings lost: %+v", m)
	}
}
