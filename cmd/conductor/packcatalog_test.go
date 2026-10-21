package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A registry of three packs: workflows only, a bot template only (with its
// collection and seed), and both.
var registryPacks = map[string]string{
	"flows/" + config.PackManifestFile: `pack: { name: flows, version: 1.0.0, description: "review workflows" }
workflows:
  review: { steps: [ { log: "review" } ] }
`,
	"sre-kit/" + config.PackManifestFile: `pack: { name: sre-kit, version: 2.0.0, description: "a first-line SRE bot — workflows not included" }
bots:
  first-line:
    description: First-line SRE
    charter: "Be careful."
    seed: seed
    collections:
      postmortems: { dir: postmortems, fields: { title: { type: string, required: true } }, writers: [operator, bots] }
    connectors:
      store: { use: kv }
    verbs:
      store.get: observe
      store.set: { max: reversible }
`,
	"sre-kit/seed/knowledge/topology.md": "---\ntitle: Topology\nevidence: e\nconfidence: high\n---\nthree replicas\n",
	"mixed/" + config.PackManifestFile: `pack: { name: mixed, version: 1.0.0, description: "a template and a workflow" }
workflows:
  tidy: { steps: [ { log: "tidy" } ] }
bots:
  tidier:
    description: Tidies
    charter: "Tidy."
`,
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// registryRepo writes the registry as a git repository and returns its
// file:// URL (a git registry: its packs are remote, third-party sources).
func registryRepo(t *testing.T) (dir, url string) {
	t.Helper()
	dir = t.TempDir()
	for rel, body := range registryPacks {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "registry")
	return dir, "file://" + dir
}

func catalogConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	config.SetStateDir(filepath.Join(t.TempDir(), "state"))
	t.Cleanup(func() { config.SetStateDir("") })
	p := filepath.Join(dir, "config.yaml")
	cfg := `runtimes:
  paseo: { use: paseo, default: true }
  clibot: { use: cli, tool: claude-code }
operators:
  ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample ops@x" }
connectors:
  state: { use: kv }
` + extra
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func catalogNames(out map[string]any) string {
	var n []string
	for _, x := range out["packs"].([]any) {
		n = append(n, x.(map[string]any)["name"].(string))
	}
	return strings.Join(n, ",")
}

// THE CATALOG: each pack's kinds come from its contents (sre-kit's
// description talks about workflows; it ships none); template-only packs
// are left out unless asked; a git registry's pack installs from its git
// source, never conductor's local clone; a local registry's from its path.
func TestPackCatalog(t *testing.T) {
	_, url := registryRepo(t)
	p := catalogConfig(t, "")
	out, err := packCatalog(p, "", url, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogNames(out); got != "flows,mixed" || out["hidden_template_packs"] != 1 {
		t.Fatalf("default: %s, hidden %v", got, out["hidden_template_packs"])
	}
	out, _ = packCatalog(p, "", url, false, true)
	if got := catalogNames(out); got != "flows,mixed,sre-kit" {
		t.Fatalf("--include-templates: %s", got)
	}
	byName := map[string]map[string]any{}
	for _, x := range out["packs"].([]any) {
		m := x.(map[string]any)
		byName[m["name"].(string)] = m
	}
	kinds := func(n string) string {
		var k []string
		for _, x := range byName[n]["contents"].(map[string]any)["kinds"].([]any) {
			k = append(k, x.(string))
		}
		return strings.Join(k, ",")
	}
	if kinds("flows") != "workflows" || kinds("sre-kit") != "bot_templates,collections,seed" || kinds("mixed") != "bot_templates,workflows" {
		t.Fatalf("kinds: %s | %s | %s", kinds("flows"), kinds("sre-kit"), kinds("mixed"))
	}
	if byName["sre-kit"]["template_only"] != true || byName["mixed"]["template_only"] != false {
		t.Fatalf("template_only: %v %v", byName["sre-kit"]["template_only"], byName["mixed"]["template_only"])
	}
	if inst := byName["sre-kit"]["install"].(string); inst != `packs: { sre-kit: { source: git::`+url+`//sre-kit } }` {
		t.Fatalf("a git registry's install: %s", inst)
	}
	// Search: words, and kinds.
	if out, _ = packCatalog(p, "workflow", url, false, false); catalogNames(out) != "flows,mixed" {
		t.Fatalf("search by kind: %s", catalogNames(out))
	}
	if out, _ = packCatalog(p, "sre", url, false, false); catalogNames(out) != "" || out["hidden_template_packs"] != 1 {
		t.Fatalf("a search matching only a template pack: %s %v", catalogNames(out), out["hidden_template_packs"])
	}
	// A local registry: its packs install from the directory.
	local := t.TempDir()
	for rel, body := range registryPacks {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(local, rel)), 0o755)
		_ = os.WriteFile(filepath.Join(local, rel), []byte(body), 0o644)
	}
	out, _ = packCatalog(p, "flows", local, false, false)
	if inst := out["packs"].([]any)[0].(map[string]any)["install"].(string); inst != "packs: { flows: { use: "+filepath.Join(local, "flows")+" } }" {
		t.Fatalf("a local registry's install: %s", inst)
	}
}

func registryRef(t *testing.T, url, tmpl string) string {
	t.Helper()
	for _, x := range func() []any {
		o, _ := templateList("/nonexistent", "", url, false, false)
		return o["templates"].([]any)
	}() {
		if m := x.(map[string]any); m["template"] == tmpl {
			return m["ref"].(string)
		}
	}
	t.Fatalf("no registry template %s", tmpl)
	return ""
}

// AUTO-INSTALL: a registry template's pack installs in the same change —
// one diff (the packs: entry and the bot), resolved and validated in a
// private copy, nothing in the config directory changing until --write —
// and a third-party source still needs its explicit trust step.
func TestBotNewInstallsItsPack(t *testing.T) {
	_, url := registryRepo(t)
	p := catalogConfig(t, "")
	before, _ := os.ReadFile(p)
	ref := registryRef(t, url, "first-line")
	d := config.TemplateBotDraft{Name: "sre"}
	// The trust step: a third-party git source is refused without it.
	if _, err := planNewBotOpts(p, d, ref, newBotOpts{Registry: url}); err == nil || !strings.Contains(err.Error(), "third-party source, git::"+url+"//sre-kit — trust it first: add it to pack_trust.allow, or re-run with --allow-unlisted") {
		t.Fatalf("without the trust step: %v", err)
	}
	plan, err := planNewBotOpts(p, d, ref, newBotOpts{Registry: url, AllowUnlisted: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || plan.Install == nil || plan.Install.Instance != "sre-kit" || plan.Template != "sre-kit/first-line" {
		t.Fatalf("plan: %+v (%s)", plan, plan.Error)
	}
	for _, want := range []string{"+packs:", "+  sre-kit:", `+    source: "git::` + url + `//sre-kit"`, "+  sre:", "+    use: sre-kit/first-line"} {
		if !strings.Contains(plan.Diff, want) {
			t.Errorf("the diff lacks %q:\n%s", want, plan.Diff)
		}
	}
	// Nothing changed yet: the config, the vendor dir, the lockfile; and
	// the private copy is gone.
	if now, _ := os.ReadFile(p); string(now) != string(before) {
		t.Fatal("planning wrote the config")
	}
	for _, rel := range []string{".conductor", config.LockfileName} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), rel)); err == nil {
			t.Fatalf("planning wrote %s", rel)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(config.StateDir(), "plans")); len(ents) != 0 {
		t.Fatalf("a private copy was left: %d", len(ents))
	}
	// --write: the config, then the pack resolved into the directory.
	if err := writePlan(p, plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if o := cfg.Bots["sre"].Origin; o == nil || o.Pack != "sre-kit" || o.Version != "2.0.0" {
		t.Fatalf("the bot after the write: %+v", cfg.Bots["sre"].Origin)
	}
	lock, _ := config.ReadLockfile(filepath.Dir(p))
	if lock == nil || len(lock.Packs) != 1 || lock.Packs[0].Instance != "sre-kit" {
		t.Fatalf("lockfile: %+v", lock)
	}
	// Installing it again: the instance name is taken.
	if _, err := planNewBotOpts(p, config.TemplateBotDraft{Name: "sre2"}, ref, newBotOpts{Registry: url, AllowUnlisted: true}); err == nil || !strings.Contains(err.Error(), `a pack instance named "sre-kit" is already in packs:`) {
		t.Fatalf("a second install: %v", err)
	}
}

// The trust step can be the allowlist; the allowlist still governs the
// resolver; the validate step still decides — an invalid plan writes
// nothing — and a resolve that fails on --write puts the config back.
func TestBotNewInstallKeepsTrustAndValidate(t *testing.T) {
	_, url := registryRepo(t)
	ref := registryRef(t, url, "first-line")
	// Listed in pack_trust.allow: no flag needed.
	p := catalogConfig(t, "pack_trust:\n  allow: [\""+url+"\"]\n") // the registry's repo
	if plan, err := planNewBotOpts(p, config.TemplateBotDraft{Name: "sre"}, ref, newBotOpts{Registry: url}); err != nil || !plan.Valid {
		t.Fatalf("allowlisted: %+v %v", plan, err)
	}
	// An allowlist that doesn't list it: refused, by the auto-install's
	// step and (with the flag) never past the resolver's own check unless
	// the operator overrides it.
	p2 := catalogConfig(t, "pack_trust:\n  allow: [\"github.com/someone-else/*\"]\n")
	if _, err := planNewBotOpts(p2, config.TemplateBotDraft{Name: "sre"}, ref, newBotOpts{Registry: url}); err == nil || !strings.Contains(err.Error(), "trust it first") {
		t.Fatalf("not allowlisted: %v", err)
	}
	// Validation still decides: an input bound to a connector the config
	// lacks — caught only by validating the whole change, in the copy.
	p3 := catalogConfig(t, "")
	bad, err := planNewBotOpts(p3, config.TemplateBotDraft{Name: "sre", Connectors: map[string]string{"store": "nope"}}, ref, newBotOpts{Registry: url, AllowUnlisted: true})
	if err != nil {
		t.Fatalf("an invalid change should plan (invalid), not fail: %v", err)
	}
	if bad.Valid || bad.Error == "" || bad.Install == nil {
		t.Fatalf("a plan binding a missing connector validated: %+v", bad)
	}
	before, _ := os.ReadFile(p3)
	if werr := writePlan(p3, bad); werr == nil {
		t.Fatal("an invalid plan was written")
	}
	if now, _ := os.ReadFile(p3); string(now) != string(before) {
		t.Fatal("an invalid plan changed the config")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p3), ".conductor")); err == nil {
		t.Fatal("an invalid plan installed its pack")
	}
	// A resolve that fails on --write: the config is put back.
	p4 := catalogConfig(t, "")
	plan, err := planNewBotOpts(p4, config.TemplateBotDraft{Name: "sre"}, ref, newBotOpts{Registry: url, AllowUnlisted: true})
	if err != nil {
		t.Fatal(err)
	}
	before4, _ := os.ReadFile(p4)
	plan.Install.allowUnlisted = false
	// The written config now refuses the source the plan resolved with the
	// flag, which this write no longer has: the resolve fails.
	plan.Config += "pack_trust:\n  allow: [\"github.com/nobody/*\"]\n"
	if err := writePlan(p4, plan); err == nil || !strings.Contains(err.Error(), "your config is as it was") {
		t.Fatalf("a failed resolve on write: %v", err)
	}
	if now, _ := os.ReadFile(p4); string(now) != string(before4) {
		t.Fatal("the config was not put back")
	}
}

// A third-party source is the only one that needs the step: the official
// repo and a local path don't. A git registry over https installs as the
// host/path//pack ref an operator would write (and allowlist).
func TestAutoInstallTrusted(t *testing.T) {
	if k, v := config.RegistrySource("https://github.com/acme/packs.git", "/cache", "sre", false); k != "source" || v != "github.com/acme/packs//sre" {
		t.Fatalf("an https registry's source: %s %s", k, v)
	}
	if k, v := config.RegistrySource("git@github.com:acme/packs", "/cache", "sre", false); v != "git::git@github.com:acme/packs//sre" || k != "source" {
		t.Fatalf("an scp registry's source: %s %s", k, v)
	}
	var none *config.PackTrustConfig
	allow := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}} // an org, as an operator writes it
	for _, c := range []struct {
		key, value string
		t          *config.PackTrustConfig
		flag, want bool
	}{
		{"use", "conductor-packs/sre-bot", none, false, true},
		{"source", "github.com/NodeSpy/conductor-packs//sre-bot", none, false, true},
		{"use", "/home/me/registry/sre-bot", none, false, true},
		{"source", "github.com/acme/packs//sre", none, false, false},
		{"source", "github.com/acme/packs//sre", none, true, true},
		{"source", "github.com/acme/packs//sre", allow, false, true},
		{"source", "github.com/evil/packs//sre", allow, false, false},
		{"source", "git::ssh://git@example.com/acme/packs//sre", none, false, false},
		{"source", "github.com/NodeSpy/conductor-packs-evil//x", none, false, false},
	} {
		if got := c.t.AutoInstallTrusted(c.key, c.value, c.flag); got != c.want {
			t.Errorf("%s %s (flag %v, allowlist %v): %v", c.key, c.value, c.flag, c.t != nil, got)
		}
	}
}
