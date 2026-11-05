package config

import (
	"os"
	"strings"
	"testing"
)

// max_runs is read at every scope, and a pack trigger's arm override carries
// it onto the instantiated trigger.
func TestMaxRunsAtEveryScopeAndThroughAPackArm(t *testing.T) {
	path := baseConfigWithReview(t, "")
	b, _ := os.ReadFile(path)
	body := strings.Replace(string(b), "        repos: [acme/app, acme/api]\n",
		"        repos: [acme/app, acme/api]\n        policy: { concurrency: { max_runs: 2 } }\n", 1)
	body = strings.Replace(body, "  gh:\n    use: github\n",
		"  gh:\n    use: github\n    policy: { concurrency: { max_runs: 3 } }\n", 1)
	body += "policy: { concurrency: { max_runs: 4 } }\n"
	os.WriteFile(path, []byte(body), 0o644)
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tr := range cfg.Triggers {
		if strings.Contains(tr.Name, "on_review_request") {
			found = true
			if got := tr.Policy.MaxRuns(); got != 2 {
				t.Errorf("pack trigger max_runs = %d, want the arm's 2", got)
			}
		}
	}
	if !found {
		t.Fatal("pack trigger not instantiated")
	}
	if got := cfg.ConnectorsMap["gh"].Policy.MaxRuns(); got != 3 {
		t.Errorf("connector max_runs = %d, want 3", got)
	}
	if got := cfg.Policy.MaxRuns(); got != 4 {
		t.Errorf("global max_runs = %d, want 4", got)
	}
	if len(cfg.RemovedFieldWarnings()) != 0 {
		t.Errorf("max_runs warned: %v", cfg.RemovedFieldWarnings())
	}
}

func TestMaxRunsNegativeIsRefused(t *testing.T) {
	p := -1
	err := validatePolicyBlock("trigger x policy", &Policy{Concurrency: &Concurrency{MaxRuns: &p}})
	if err == nil || !strings.Contains(err.Error(), "max_runs") {
		t.Fatalf("err = %v, want a max_runs refusal", err)
	}
}

// max_agents / max_agents_per_hour below the global scope are not read: the
// config still loads (a live config that sets them must not stop), with a
// warning naming each one.
func TestScopedMaxAgentsWarnsAndLoads(t *testing.T) {
	path := baseConfigWithReview(t, "")
	b, _ := os.ReadFile(path)
	body := strings.Replace(string(b), "        repos: [acme/app, acme/api]\n",
		"        repos: [acme/app, acme/api]\n        policy: { concurrency: { max_agents: 2 } }\n", 1)
	body = strings.Replace(body, "  gh:\n    use: github\n",
		"  gh:\n    use: github\n    policy: { concurrency: { max_agents_per_hour: 5 } }\n", 1)
	body += "policy: { concurrency: { max_agents: 4, max_agents_per_hour: 9 } }\n"
	os.WriteFile(path, []byte(body), 0o644)
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("a config with scoped max_agents must still load: %v", err)
	}
	w := strings.Join(cfg.RemovedFieldWarnings(), "\n")
	for _, want := range []string{
		"connectors.gh.policy.concurrency.max_agents_per_hour: ignored",
		"on_review_request.policy.concurrency.max_agents: ignored",
		"use concurrency.max_runs",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "\npolicy.concurrency") || strings.HasPrefix(w, "policy.concurrency") {
		t.Errorf("the global max_agents warned:\n%s", w)
	}
	if cfg.AgentCap() != 4 {
		t.Errorf("AgentCap = %d, want the global 4", cfg.AgentCap())
	}
}
