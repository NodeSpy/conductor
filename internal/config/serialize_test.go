package config

import (
	"strings"
	"testing"
)

const serializePack = `
pack:
  name: review
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
triggers:
  - name: deploy
    on: github.push
    serialize: { key: "{{.repo}}" }
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: b } }]
`

func serializeOf(t *testing.T, arm string) *SerializeSpec {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", serializePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      deploy: `+arm+`
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "review/deploy") {
			return tr.Serialize
		}
	}
	t.Fatal("no review/deploy trigger")
	return nil
}

// A pack trigger's serialize: key ships with it, and the operator's arm
// replaces it, or turns it off with an empty key.
func TestPackSerializeKeyIsOverridable(t *testing.T) {
	if s := serializeOf(t, `{ enabled: true, repos: [o/r] }`); s == nil || s.Key != "{{.repo}}" {
		t.Fatalf("shipped key: %+v", s)
	}
	if s := serializeOf(t, `{ enabled: true, repos: [o/r], serialize: { key: "all" } }`); s == nil || s.Key != "all" {
		t.Fatalf("overridden key: %+v", s)
	}
	if s := serializeOf(t, `{ enabled: true, repos: [o/r], serialize: { key: "" } }`); s == nil || s.Key != "" {
		t.Fatalf("turned off: %+v", s)
	}
}
