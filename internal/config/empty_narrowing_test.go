package config

import (
	"os"
	"strings"
	"testing"
)

// `network: []` on a connector narrows nothing, as before the plugin jail:
// the config loads, with a warning naming it. `allow_env: []` passes none,
// as it always did, and warns nothing; nor does unset, or a list with
// entries.
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
	if strings.Contains(w, "allow_env") {
		t.Errorf("allow_env: [] warned: %s", w)
	}
	for _, want := range []string{"connectors.gh.network: [] does not narrow the plugin", "as before the plugin jail", "remove the key"} {
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
