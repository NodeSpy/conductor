package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cacheBase = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"

func TestSandboxCachesDefaultsAndKnobs(t *testing.T) {
	cfg, err := loadCfg(t, cacheBase)
	if err != nil {
		t.Fatal(err)
	}
	// Off unless configured: an upgrade never uses disk a config did not
	// ask for.
	if cfg.CachesOn() {
		t.Fatal("caches on without sandbox.caches.enabled")
	}
	for _, y := range []string{"sandbox:\n  caches: {}\n", "sandbox:\n  caches: { max_size: 1GiB }\n", "sandbox:\n  caches: { enabled: false }\n"} {
		if c2, err := loadCfg(t, cacheBase+y); err != nil || c2.CachesOn() {
			t.Fatalf("%q: on=%v err=%v", y, c2 != nil && c2.CachesOn(), err)
		}
	}
	if c2, err := loadCfg(t, cacheBase+"sandbox:\n  caches: { enabled: true }\n"); err != nil || !c2.CachesOn() {
		t.Fatalf("enabled: true is off (%v)", err)
	}
	var c *SandboxCaches
	if c.MaxSizeBytes() != DefaultCachesMax || c.LearnMinSize() != DefaultCacheLearnMinSize || c.LearnMinRuns() != DefaultCacheLearnMinRuns {
		t.Fatal("defaults")
	}
	if cfg.CachesDir() != filepath.Join(filepath.Dir(cfg.Store.StateFile), "repo-caches") {
		t.Fatalf("dir %s", cfg.CachesDir())
	}
	cfg, err = loadCfg(t, cacheBase+`sandbox:
  caches:
    enabled: true
    dir: /var/cache/conductor-caches
    max_size: 2GiB
    learn: { min_size: 10MiB, min_runs: 3 }
`)
	if err != nil {
		t.Fatal(err)
	}
	c = cfg.Sandbox.Caches
	if c.MaxSizeBytes() != 2<<30 || c.LearnMinSize() != 10<<20 || c.LearnMinRuns() != 3 || cfg.CachesDir() != "/var/cache/conductor-caches" {
		t.Fatalf("knobs not read: %+v", c)
	}
	if sb, _ := loadCfg(t, cacheBase+"sandbox:\n  agents: off\n  caches: { enabled: true }\n"); sb.CachesOn() {
		t.Fatal("caches without the sandbox")
	}
	if n, _ := loadCfg(t, cacheBase+"sandbox:\n  caches: { max_size: \"off\" }\n"); n.Sandbox.Caches.MaxSizeBytes() != 0 {
		t.Fatal("max_size off")
	}
}

func TestSandboxCachesRefused(t *testing.T) {
	for name, body := range map[string]string{
		"relative dir":  "sandbox:\n  caches: { dir: rel }\n",
		"bad size":      "sandbox:\n  caches: { max_size: lots }\n",
		"bad min_size":  "sandbox:\n  caches: { learn: { min_size: big } }\n",
		"zero min_size": "sandbox:\n  caches: { learn: { min_size: \"0\" } }\n",
		"neg min_runs":  "sandbox:\n  caches: { learn: { min_runs: -1 } }\n",
		"learn typo":    "sandbox:\n  caches: { learn: { min_run: 2 } }\n",
		"no key":        "workflows:\n  w:\n    steps: [{ id: a, type: agent, prompt: x, cache: { restore_from: [x] } }]\n",
		"blank key":     "workflows:\n  w:\n    steps: [{ id: a, type: agent, prompt: x, cache: { key: \"  \" } }]\n",
		"bad template":  "workflows:\n  w:\n    steps: [{ id: a, type: agent, prompt: x, cache: { key: \"{{.repo\" } }]\n",
		"bad fallback":  "workflows:\n  w:\n    cache: { key: k, restore_from: [\"{{\"] }\n    steps: [{ id: a, type: agent, prompt: x }]\n",
		"too many":      "workflows:\n  w:\n    steps: [{ id: a, type: agent, prompt: x, cache: { key: k, restore_from: [a, b, c, d, e, f, g, h, i] } }]\n",
		"unknown field": "workflows:\n  w:\n    steps: [{ id: a, type: agent, prompt: x, cache: { key: k, paths: [~/.cache] } }]\n",
	} {
		cfg, err := loadCfg(t, cacheBase+body)
		if err == nil {
			err = cfg.Validate()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "cache") && !strings.Contains(err.Error(), "min_run") && !strings.Contains(err.Error(), "paths") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A step's cache: and a workflow's default load, teach: with them.
func TestStepCacheLoads(t *testing.T) {
	cfg, err := loadCfg(t, cacheBase+`workflows:
  warm:
    cache: { key: " {{.repo}} " }
    steps: [{ id: w, type: agent, prompt: warm }]
  teacher:
    steps: [{ id: t, type: agent, prompt: t, cache: { key: "{{.repo}}", teach: true } }]
  fix:
    steps:
      - id: f
        type: agent
        prompt: fix
        cache: { key: "pr-{{.repo}}-{{.pr}}", restore_from: ["{{.repo}}"] }
`)
	if err != nil {
		t.Fatal(err)
	}
	if wf := cfg.Workflows["warm"]; wf.Cache == nil || CacheTemplate(wf.Cache.Key) != "{{.repo}}" {
		t.Fatalf("workflow cache %+v", wf.Cache)
	}
	if s := cfg.Workflows["fix"].Steps[0]; s.Cache == nil || len(s.Cache.RestoreFrom) != 1 {
		t.Fatalf("step cache %+v", s.Cache)
	}
	if s := cfg.Workflows["teacher"].Steps[0]; s.Cache == nil || !s.Cache.Teach {
		t.Fatalf("teach: not loaded %+v", s.Cache)
	}
	if s := cfg.Workflows["fix"].Steps[0]; s.Cache.Teach || cfg.Workflows["warm"].Cache.Teach {
		t.Fatal("teach: set where it was not declared")
	}
}

// The recipe caches' fields are gone: a config that still has them loads,
// they are ignored, and each gets a one-line warning — never a crash (the
// fleet auto-updates onto configs written for the old schema).
func TestRemovedCacheFieldsWarnNotCrash(t *testing.T) {
	cfg, err := loadCfg(t, cacheBase+`sandbox:
  caches:
    enabled: true
    autodetect: false
    repo_file: false
    warm_on_miss: false
    warm_every: 12h
    warm_timeout: 5m
    recipes:
      - name: deno
        detect: [deno.lock]
        key: 'deno-{{hash "deno.lock"}}'
        paths: [~/.cache/deno]
    defaults:
      - recipe: go
    repos:
      acme/web:
        - recipe: npm
          warm: npm ci --ignore-scripts
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.CachesOn() {
		t.Fatal("the removed fields turned caches off")
	}
	got := strings.Join(cfg.RemovedFieldWarnings(), "\n")
	for _, k := range removedCacheFields {
		if !strings.Contains(got, "sandbox.caches."+k+": "+CachesRemoved) {
			t.Errorf("no warning for %s:\n%s", k, got)
		}
	}
	if n := strings.Count(got, "\n") + 1; n != len(removedCacheFields) {
		t.Errorf("%d warnings, want %d:\n%s", n, len(removedCacheFields), got)
	}
	if w, _ := loadCfg(t, cacheBase+"sandbox:\n  caches: { enabled: true }\n"); len(w.RemovedFieldWarnings()) != 0 {
		t.Fatalf("warnings from a current config: %v", w.RemovedFieldWarnings())
	}
}

// The shipped example loads whole, with no warning.
func TestConfigExampleLoads(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"GH_WEBHOOK_SECRET", "GH_SMEE_URL", "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "CW_SECRET", "CONDUCTOR_INVOKE_TOKEN", "CONDUCTOR_INVOKE_HMAC"} {
		t.Setenv(v, "x")
	}
	cfg, err := loadCfg(t, string(raw))
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
	if w := cfg.RemovedFieldWarnings(); len(w) != 0 {
		t.Fatalf("config.example.yaml warns: %v", w)
	}
}

// The cache store holds launches' overlay layers, which mount(2) takes as
// paths: a config whose fs: grant shows it (or a directory above it) to
// launches — cached or not — is refused.
func TestCachesDirUnderAnFSGrantIsRefused(t *testing.T) {
	const base = cacheBase + "sandbox:\n  caches: { enabled: true, dir: /srv/caches/store }\n"
	if _, err := loadCfg(t, base+"engines: { e: { isolation: { mode: namespace, fs: [/srv/media] } } }\n"); err != nil {
		t.Fatalf("an unrelated fs: grant: %v", err)
	}
	for _, grant := range []string{"/srv/caches", "/srv/caches/store", "/srv/caches/store/.layers"} {
		_, err := loadCfg(t, base+"engines: { e: { isolation: { mode: namespace, fs: ["+grant+"] } } }\n")
		if err == nil || !strings.Contains(err.Error(), "sandbox.caches.dir") {
			t.Errorf("fs: [%s]: %v", grant, err)
		}
	}
	off := strings.Replace(base, "enabled: true", "enabled: false", 1)
	if _, err := loadCfg(t, off+"engines: { e: { isolation: { mode: namespace, fs: [/srv/caches] } } }\n"); err != nil {
		t.Fatalf("caches off: %v", err)
	}
}
