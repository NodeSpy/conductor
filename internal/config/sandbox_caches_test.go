package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxCachesDefaultsAndKnobs(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	cfg, err := loadCfg(t, base)
	if err != nil {
		t.Fatal(err)
	}
	var c *SandboxCaches
	// Off unless configured: an upgrade never starts warms, disk use or
	// fetches on a config that did not ask for them.
	if cfg.CachesOn() {
		t.Fatal("caches on without sandbox.caches.enabled")
	}
	for _, y := range []string{"sandbox:\n  caches: {}\n", "sandbox:\n  caches: { max_size: 1GiB }\n", "sandbox:\n  caches: { enabled: false }\n"} {
		if c2, err := loadCfg(t, base+y); err != nil || c2.CachesOn() {
			t.Fatalf("%q: on=%v err=%v", y, c2 != nil && c2.CachesOn(), err)
		}
	}
	if c2, err := loadCfg(t, base+"sandbox:\n  caches: { enabled: true }\n"); err != nil || !c2.CachesOn() {
		t.Fatalf("enabled: true is off (%v)", err)
	}
	if !c.AutodetectOn() || !c.RepoFileOn() || !c.WarmOnMissOn() || c.MaxSizeBytes() != DefaultCachesMax ||
		c.WarmEveryDur() != 0 || c.WarmTimeoutDur() != 20*time.Minute {
		t.Fatal("defaults")
	}
	if cfg.CachesDir() != filepath.Join(filepath.Dir(cfg.Store.StateFile), "repo-caches") {
		t.Fatalf("dir %s", cfg.CachesDir())
	}
	cfg, err = loadCfg(t, base+`sandbox:
  caches:
    dir: /var/cache/conductor-caches
    max_size: 2GiB
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
        env: { DENO_DIR: "{{home}}/.cache/deno" }
        warm: deno cache main.ts
    defaults:
      - recipe: go
    repos:
      acme/web:
        - recipe: npm
          warm: npm ci --ignore-scripts
        - name: go
          disabled: true
`)
	if err != nil {
		t.Fatal(err)
	}
	c = cfg.Sandbox.Caches
	if c.AutodetectOn() || c.RepoFileOn() || c.WarmOnMissOn() || c.MaxSizeBytes() != 2<<30 || c.WarmEveryDur() != 12*time.Hour ||
		c.WarmTimeoutDur() != 5*time.Minute || cfg.CachesDir() != "/var/cache/conductor-caches" || len(c.Repos["acme/web"]) != 2 {
		t.Fatalf("knobs not read: %+v", c)
	}
	off, _ := loadCfg(t, base+"sandbox:\n  caches: { enabled: false }\n")
	if off.CachesOn() {
		t.Fatal("enabled: false")
	}
	if sb, _ := loadCfg(t, base+"sandbox:\n  agents: off\n"); sb.CachesOn() {
		t.Fatal("caches without the sandbox")
	}
	if n, _ := loadCfg(t, base+"sandbox:\n  caches: { max_size: \"off\" }\n"); n.Sandbox.Caches.MaxSizeBytes() != 0 {
		t.Fatal("max_size off")
	}
}

func TestSandboxCachesRefused(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\nsandbox:\n  caches:\n"
	for name, body := range map[string]string{
		"relative dir":     "    dir: rel\n",
		"bad size":         "    max_size: lots\n",
		"bad duration":     "    warm_every: soon\n",
		"too frequent":     "    warm_every: 1m\n",
		"secret path":      "    defaults: [{ name: x, key: k, paths: [~/.ssh] }]\n",
		"outside path":     "    defaults: [{ name: x, key: k, paths: [/etc] }]\n",
		"escaping path":    "    defaults: [{ name: x, key: k, paths: [../x] }]\n",
		"proxy env":        "    defaults: [{ name: x, key: k, paths: [~/x], env: { HTTPS_PROXY: http://evil } }]\n",
		"no key":           "    defaults: [{ name: x, paths: [~/x] }]\n",
		"unknown recipe":   "    defaults: [{ recipe: nope }]\n",
		"twice":            "    defaults: [{ recipe: go }, { recipe: go }]\n",
		"bad repo":         "    repos: { \"not a repo\": [{ recipe: go }] }\n",
		"recipe no detect": "    recipes: [{ name: x, key: k, paths: [~/x] }]\n",
		"recipe ref":       "    recipes: [{ name: x, recipe: go, detect: [a] }]\n",
		"unknown field":    "    defaults: [{ recipe: go, colour: blue }]\n",
	} {
		cfg, err := loadCfg(t, base+body)
		if err == nil {
			err = cfg.Validate()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "cache") && !strings.Contains(err.Error(), "colour") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
