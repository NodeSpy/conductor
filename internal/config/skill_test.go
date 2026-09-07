package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadSkillCfg(t *testing.T, y string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// The skill: block (#36 §12 increment 5): parses, validates its enum and its
// allow_secrets ("<vault>/<key>" against vaults:), and is OFF by default.
func TestSkillPolicyConfig(t *testing.T) {
	base := `
connectors:
  gh: { type: github }
vaults:
  house: { type: file, dir: /run/secrets }
agents:
  deployer:
    model: x
    skill:
      secrets_via: %VIA%
      allow_secrets: [%ALLOW%]
      verbs: [gh.comment]
triggers:
  - on: gh.pr_opened
    steps: [ { id: a, type: agent, agent: deployer, prompt: p } ]
`
	load := func(via, allow string) (*Config, error) {
		y := strings.Replace(strings.Replace(base, "%VIA%", via, 1), "%ALLOW%", allow, 1)
		cfg, err := loadSkillCfg(t, y)
		if err == nil {
			err = cfg.Validate()
		}
		return cfg, err
	}

	cfg, err := load("broker", "house/deploy_key")
	if err != nil {
		t.Fatalf("valid skill block: %v", err)
	}
	sk := cfg.Agents["deployer"].Skill
	if sk == nil || sk.SecretsVia != "broker" || sk.AllowSecrets[0] != "house/deploy_key" || sk.Verbs[0] != "gh.comment" {
		t.Fatalf("skill block: %+v", sk)
	}
	if !cfg.SkillEnabled() {
		t.Fatal("SkillEnabled must report the opted-in profile")
	}

	// A bad secrets_via is a validation error.
	if _, err := load("prompt", "house/deploy_key"); err == nil || !strings.Contains(err.Error(), "secrets_via must be broker|env|none") {
		t.Fatalf("bad secrets_via: %v", err)
	}

	// allow_secrets must name a defined vault…
	if _, err := load("broker", "ghost/key"); err == nil || !strings.Contains(err.Error(), "unknown vault \"ghost\"") {
		t.Fatalf("unknown vault: %v", err)
	}
	// …and a bare name (no vault/key form) is rejected on this model.
	if _, err := load("broker", "deploy_key"); err == nil || !strings.Contains(err.Error(), `"<vault>/<key>"`) {
		t.Fatalf("bare name: %v", err)
	}

	// Default off: no skill: block anywhere → SkillEnabled false.
	off, err := loadSkillCfg(t, `
connectors:
  gh: { type: github }
agents:
  fixer: { model: x }
triggers:
  - on: gh.pr_opened
    steps: [ { id: a, type: agent, agent: fixer, prompt: p } ]
`)
	if err != nil {
		t.Fatal(err)
	}
	if off.SkillEnabled() {
		t.Fatal("SkillEnabled must be false with no skill: block")
	}
	if off.Agents["fixer"].Skill != nil {
		t.Fatal("skill must default to nil")
	}
}
