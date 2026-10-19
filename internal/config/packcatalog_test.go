package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CONTENT KINDS come from what a manifest contains — never from what a pack
// says about itself — and only a pack shipping bot templates and nothing
// else (their collections and seeds included) is template-only.
func TestManifestContents(t *testing.T) {
	for _, c := range []struct {
		man          string
		kinds        string
		templateOnly bool
	}{
		{"pack: { name: a, version: 1.0.0, description: 'workflows, triggers, checks and bots' }\n", "", false},
		{"pack: { name: a, version: 1.0.0 }\nworkflows: { w: { steps: [ { log: x } ] } }\n", "workflows", false},
		{"pack: { name: a, version: 1.0.0 }\nbots:\n  t: { description: d, charter: c }\n", "bot_templates", true},
		{"pack: { name: a, version: 1.0.0 }\nbots:\n  t: { description: d, charter: c, seed: s, collections: { p: { dir: p, fields: { title: { type: string } } } } }\n", "bot_templates,collections,seed", true},
		{"pack: { name: a, version: 1.0.0 }\nbots:\n  t: { description: d, charter: c }\nworkflows: { w: { steps: [ { log: x } ] } }\n", "bot_templates,workflows", false},
		{"pack: { name: a, version: 1.0.0 }\nbots:\n  t: { description: d, charter: c }\nchecks: { c: { log: x } }\n", "bot_templates,checks", false},
		{"pack: { name: a, version: 1.0.0 }\ntriggers:\n  - { name: x, on: manual, steps: [ { log: x } ] }\n", "triggers", false},
	} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, PackManifestFile), []byte(c.man), 0o644)
		man, err := loadPackManifest(dir)
		if err != nil {
			t.Fatalf("%q: %v", c.man, err)
		}
		pc := ManifestContents(man)
		if strings.Join(pc.Kinds, ",") != c.kinds || pc.TemplateOnly() != c.templateOnly {
			t.Errorf("%q: kinds %v template-only %v, want %q %v", c.man, pc.Kinds, pc.TemplateOnly(), c.kinds, c.templateOnly)
		}
	}
	ps := []PackSummary{{Name: "flows", Contents: PackContents{Kinds: []string{"workflows"}}}, {Name: "kit", TemplateOnly: true, Description: "sre"}}
	if kept, hidden := FilterPacks(ps, "", false); len(kept) != 1 || hidden != 1 {
		t.Fatalf("filter: %v %d", kept, hidden)
	}
	if kept, hidden := FilterPacks(ps, "sre", true); len(kept) != 1 || kept[0].Name != "kit" || hidden != 0 {
		t.Fatalf("filter with templates: %v %d", kept, hidden)
	}
}

// IS AN UPDATE AVAILABLE: against the official registry as conductor last
// fetched it (never a fetch of its own); unknown for a pack from anywhere
// else, or before the registry has been fetched.
func TestCheckTemplateUpdate(t *testing.T) {
	SetStateDir(t.TempDir())
	t.Cleanup(func() { SetStateDir("") })
	o := &BotOrigin{Pack: "sre-bot", Version: "2.0.0", Source: PacksNamespaceAlias + "/sre-bot"}
	if u := CheckTemplateUpdate(o, "sre"); u.Known || !strings.Contains(u.Why, "not been fetched") {
		t.Fatalf("no registry yet: %+v", u)
	}
	h := sha256.Sum256([]byte(DefaultRegistry))
	dir := filepath.Join(StateDir(), "registry", hex.EncodeToString(h[:6]))
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".git", "FETCH_HEAD"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "sre-bot"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sre-bot", PackManifestFile), []byte("pack: { name: sre-bot, version: 2.1.0 }\n"), 0o644)
	u := CheckTemplateUpdate(o, "sre")
	if !u.Known || !u.Available || u.Latest != "2.1.0" || !strings.Contains(u.How, "conductor pack update sre") || u.Checked == "" {
		t.Fatalf("a newer one: %+v", u)
	}
	if _, err := time.Parse(time.RFC3339, u.Checked); err != nil {
		t.Fatalf("checked: %v", err)
	}
	o.Version = "2.1.0"
	if u := CheckTemplateUpdate(o, "sre"); !u.Known || u.Available {
		t.Fatalf("current: %+v", u)
	}
	o.Version = "3.0.0"
	if u := CheckTemplateUpdate(o, "sre"); u.Available {
		t.Fatalf("newer than the registry: %+v", u)
	}
	// The official repo by its source form too.
	o.Version, o.Source = "1.0.0", "github.com/NodeSpy/conductor-packs//sre-bot"
	if u := CheckTemplateUpdate(o, "sre"); !u.Available {
		t.Fatalf("by source: %+v", u)
	}
	if u := CheckTemplateUpdate(&BotOrigin{Pack: "x", Version: "1.0.0", Source: "github.com/acme/packs//x"}, "x"); u.Known || !strings.Contains(u.Why, "not from the official registry") {
		t.Fatalf("a third-party pack: %+v", u)
	}
	if u := CheckTemplateUpdate(&BotOrigin{Pack: "gone", Version: "1.0.0", Source: PacksNamespaceAlias + "/gone"}, "x"); u.Known {
		t.Fatalf("a pack the registry lacks: %+v", u)
	}
}
