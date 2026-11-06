package config

import (
	"os"
	"strings"
	"testing"
)

// `network: []` and `allow_env: []` on a connector now grant none, where an
// older conductor read them as everything the plugin declares: the config
// loads, with a warning naming each one. Unset, or a list with entries,
// warns nothing.
func TestEmptyNarrowingWarnsAtLoad(t *testing.T) {
	path := baseConfigWithReview(t, "")
	b, _ := os.ReadFile(path)
	body := strings.Replace(string(b), "  gh:\n    use: github\n", "  gh:\n    use: github\n    network: []\n    allow_env: []\n", 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(cfg.RemovedFieldWarnings(), "\n")
	for _, want := range []string{"connectors.gh.network: [] grants no network at all", "connectors.gh.allow_env: [] passes no variables at all", "remove the key"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}

	body = strings.Replace(body, "    network: []\n    allow_env: []\n", "    allow_env: [GH_HOST]\n", 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err = resolveAndLoad(t, path); err != nil {
		t.Fatal(err)
	}
	if w := cfg.RemovedFieldWarnings(); len(w) != 0 {
		t.Errorf("warned: %v", w)
	}
}
