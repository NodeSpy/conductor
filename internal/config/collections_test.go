package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectionsConfig(t *testing.T) {
	ok := "teams:\n  x:\n    collections:\n      postmortems:\n        dir: postmortems\n        fields: { title: { type: string, required: true }, severity: { type: enum, values: [sev1, sev2] } }\n        writers: [operator, proposal]\nbots:\n  a:\n    team: x\n    collections:\n      notes: { dir: notes, free_paths: true, fields: { title: { type: string } }, writers: [operator, bots] }\n"
	c, err := loadTeams(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Teams["x"].Collections["postmortems"].Fields["severity"].Type != "enum" || !c.Bots["a"].Collections["notes"].FreePaths {
		t.Fatalf("collections: %+v %+v", c.Teams["x"].Collections, c.Bots["a"].Collections)
	}
	for name, body := range map[string]string{
		"built-in dir":   "bots:\n  a:\n    collections: { k: { dir: knowledge, fields: { t: { type: string } }, writers: [bots] } }\n",
		"legacy dir":     "bots:\n  a:\n    collections: { k: { dir: episodes/x, fields: { t: { type: string } }, writers: [bots] } }\n",
		"overlap":        "bots:\n  a:\n    collections: { k: { dir: a, fields: { t: { type: string } }, writers: [bots] }, l: { dir: a/b, fields: { t: { type: string } }, writers: [bots] } }\n",
		"no fields":      "bots:\n  a:\n    collections: { k: { dir: k, writers: [bots] } }\n",
		"bad type":       "bots:\n  a:\n    collections: { k: { dir: k, fields: { t: { type: date } }, writers: [bots] } }\n",
		"enum no values": "bots:\n  a:\n    collections: { k: { dir: k, fields: { t: { type: enum } }, writers: [bots] } }\n",
		"values no enum": "bots:\n  a:\n    collections: { k: { dir: k, fields: { t: { type: string, values: [a] } }, writers: [bots] } }\n",
		"no writers":     "bots:\n  a:\n    collections: { k: { dir: k, fields: { t: { type: string } } } }\n",
		"bot proposal":   "bots:\n  a:\n    collections: { k: { dir: k, fields: { t: { type: string } }, writers: [proposal] } }\n",
		"team bots":      "teams:\n  x:\n    collections: { k: { dir: k, fields: { t: { type: string } }, writers: [bots] } }\nbots:\n  a: { team: x }\n",
		"bad dir":        "bots:\n  a:\n    collections: { k: { dir: \"../k\", fields: { t: { type: string } }, writers: [bots] } }\n",
		"bad name":       "bots:\n  a:\n    collections: { K!: { dir: k, fields: { t: { type: string } }, writers: [bots] } }\n",
	} {
		if _, err := loadTeams(t, body); err == nil || !strings.Contains(err.Error(), "collections") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A pack ships a collection definition; a bot or team uses it with from:.
func TestCollectionFrom(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "postmortems.yaml")
	_ = os.WriteFile(def, []byte("dir: postmortems\nfields:\n  title: { type: string, required: true }\nwriters: [operator, bots]\n"), 0o600)
	c, err := loadTeams(t, "bots:\n  a:\n    collections: { postmortems: { from: \""+def+"\" } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if col := c.Bots["a"].Collections["postmortems"]; col.Dir != "postmortems" || col.From != "" || !col.Fields["title"].Required {
		t.Fatalf("from: %+v", col)
	}
	for name, body := range map[string]string{
		"inline too": "bots:\n  a:\n    collections: { postmortems: { from: \"" + def + "\", dir: x } }\n",
		"missing":    "bots:\n  a:\n    collections: { postmortems: { from: /nonexistent/x.yaml } }\n",
	} {
		if _, err := loadTeams(t, body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
