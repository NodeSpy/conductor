package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A pack whose manifest still files its members in folders (refused before
// folders were removed; ignored now).
const folderPack = `pack:
  name: tidy
  version: 1.0.0
  description: an older pack
workflows:
  tidy-flow:
    folder: ops/tidy
    steps:
      - log: "tidy"
triggers:
  - name: tidy-now
    folder: ops
    on: manual
    steps:
      - call: tidy-flow
bots:
  helper:
    description: a helper
    folder: anywhere
`

// A config with folder: everywhere conductor ever read one — including
// values that were invalid folder paths, and values that are not strings.
const folderConfig = `
workflows:
  mine:
    folder: work/mine
    steps:
      - log: "hi"
triggers:
  - name: go
    folder: work
    on: manual
    steps:
      - call: mine
  - name: again
    folder: [not, a, path]
    on: manual
    steps:
      - log: "again"
teams:
  platform:
    folder: work/platform
    routing: { fallback: sre }
bots:
  sre:
    team: platform
    folder: "../not a valid folder"
  solo:
    description: on its own
    folder: { nested: true }
packs:
  kit:
    use: ./packs/tidy
    folder: [my, packs]
    workflows:
      tidy-flow:
        folder: ops
    triggers:
      tidy-now:
        - enabled: true
          folder: ops/one
        - enabled: true
          folder: ops/two
          policy: { budget: { max_cost_usd: 1 } }
`

var folderLine = regexp.MustCompile(`(?m)^\s*-?\s*folder:.*\n`)

// stripFolders is the same YAML with every folder: line gone (an array
// item whose first key was folder: keeps its dash on the next key).
func stripFolders(s string) string {
	s = strings.ReplaceAll(s, "        - enabled: true\n          folder: ops/one\n", "        - enabled: true\n")
	s = strings.ReplaceAll(s, "        - enabled: true\n          folder: ops/two\n", "        - enabled: true\n")
	s = strings.ReplaceAll(s, "    workflows:\n      tidy-flow:\n        folder: ops\n", "")
	return folderLine.ReplaceAllString(s, "")
}

func loadFolderVariant(t *testing.T, pack, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	writePackSource(t, dir, "packs/tidy", pack)
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(botsBase+body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveAndLoad(t, p)
	if err != nil {
		t.Fatalf("a config with folder: does not load: %v\n%s", err, body)
	}
	return c
}

// FOLDERS WERE REMOVED: a config that still has folder: anywhere loads,
// warns once per key, and comes out exactly as it would without them.
func TestRemovedFolderIsIgnored(t *testing.T) {
	with := loadFolderVariant(t, folderPack, folderConfig)
	clean := stripFolders(folderConfig)
	if strings.Contains(clean, "folder") || strings.Contains(stripFolders(folderPack), "folder") {
		t.Fatalf("the fixture's stripped variant still has a folder:\n%s", clean)
	}
	without := loadFolderVariant(t, stripFolders(folderPack), clean)
	want := []string{
		"bots.solo.folder: " + FolderRemoved,
		"bots.sre.folder: " + FolderRemoved,
		"packs.kit.folder: " + FolderRemoved,
		"packs.kit.triggers.tidy-now[0].folder: " + FolderRemoved,
		"packs.kit.triggers.tidy-now[1].folder: " + FolderRemoved,
		"packs.kit.workflows.tidy-flow.folder: " + FolderRemoved,
		"teams.platform.folder: " + FolderRemoved,
		"triggers.again.folder: " + FolderRemoved,
		"triggers.go.folder: " + FolderRemoved,
		"workflows.mine.folder: " + FolderRemoved,
		"pack tidy: bots.helper.folder: " + FolderRemoved,
		"pack tidy: triggers.tidy-now.folder: " + FolderRemoved,
		"pack tidy: workflows.tidy-flow.folder: " + FolderRemoved,
	}
	if got := with.RemovedFieldWarnings(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(without.RemovedFieldWarnings()) != 0 {
		t.Fatalf("warnings without folders: %v", without.RemovedFieldWarnings())
	}
	// Ignored: the effective config is the same as without them — bots,
	// teams, workflows, and triggers down to their instances' identities.
	for name, pair := range map[string][2]any{
		"bots": {with.Bots, without.Bots}, "teams": {with.Teams, without.Teams},
		"workflows": {with.Workflows, without.Workflows}, "triggers": {with.Triggers, without.Triggers},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s differ with folder: present", name)
		}
	}
	var names []string
	for _, tr := range with.Triggers {
		names = append(names, tr.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "kit/tidy-now#") {
		t.Fatalf("the armed pack trigger's instances: %v", names)
	}
	// Never written back.
	b, _ := yaml.Marshal(with.Bots["sre"])
	if strings.Contains(string(b), "folder") {
		t.Fatalf("a removed field was marshaled:\n%s", b)
	}
}

// The imports path (a merged multi-file config) warns the same way.
func TestRemovedFolderThroughImports(t *testing.T) {
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	_ = os.WriteFile(filepath.Join(dir, "bots.yaml"), []byte("bots:\n  solo:\n    folder: personal\n"), 0o600)
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(botsBase+"imports: [bots.yaml]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("an imported folder: does not load: %v", err)
	}
	if got := c.RemovedFieldWarnings(); len(got) != 1 || got[0] != "bots.solo.folder: "+FolderRemoved {
		t.Fatalf("imports warnings: %v", got)
	}
}

// RemovedField: decodes from anything, keeps nothing, writes nothing.
func TestRemovedFieldShape(t *testing.T) {
	var v struct {
		Folder RemovedField `yaml:"folder,omitempty"`
		Name   string       `yaml:"name"`
	}
	for _, doc := range []string{"folder: a/b\nname: x\n", "folder: [1, 2]\nname: x\n", "folder: {a: 1}\nname: x\n", "folder:\nname: x\n"} {
		if err := strictUnmarshal([]byte(doc), &v); err != nil || v.Name != "x" {
			t.Errorf("%q: %v", doc, err)
		}
	}
	b, _ := yaml.Marshal(v)
	if string(b) != "name: x\n" {
		t.Fatalf("marshaled: %q", b)
	}
}

// The walk reaches every place folder: was read: a dependency's packs:
// entry, map-form trigger lists, and a manifest's map-form triggers.
func TestRemovedFolderWalk(t *testing.T) {
	if FolderRemoved != "folders were removed; this field is ignored and can be deleted" {
		t.Fatalf("the warning's text: %q", FolderRemoved)
	}
	got := strings.Join(removedFolderWarnings([]byte(`
triggers:
  nightly: { on: manual, folder: x }
packs:
  top:
    triggers:
      t: { folder: y }
    packs:
      dep:
        folder: z
        workflows: { w: { folder: q } }
        packs:
          deeper: { folder: r }
`)), "\n")
	want := strings.Join([]string{
		"packs.top.packs.dep.folder: " + FolderRemoved,
		"packs.top.packs.dep.packs.deeper.folder: " + FolderRemoved,
		"packs.top.packs.dep.workflows.w.folder: " + FolderRemoved,
		"packs.top.triggers.t.folder: " + FolderRemoved,
		"triggers.nightly.folder: " + FolderRemoved,
	}, "\n")
	if got != want {
		t.Fatalf("walk:\n%s\nwant:\n%s", got, want)
	}
	m := strings.Join(manifestFolderWarnings([]byte("triggers:\n  a: { folder: x }\n  b: { on: manual }\n"), "p"), "\n")
	if m != "pack p: triggers.a.folder: "+FolderRemoved {
		t.Fatalf("manifest walk: %q", m)
	}
	if len(removedFolderWarnings([]byte("bots: {}\n"))) != 0 || len(removedFolderWarnings([]byte("::not yaml"))) != 0 {
		t.Fatal("warnings from nothing")
	}
}

// Map-form triggers written as an instance array are keyed by a hash of each
// instance's raw YAML: a folder: in one never changes which instance it is.
func TestRemovedFolderKeepsInstanceIdentity(t *testing.T) {
	body := func(f1, f2 string) string {
		return "triggers:\n  nightly:\n    - on: manual\n" + f1 + "      steps: [ { log: one } ]\n    - on: manual\n" + f2 + "      steps: [ { log: two } ]\n"
	}
	names := func(c *Config) string {
		var out []string
		for _, tr := range c.Triggers {
			out = append(out, tr.Name)
		}
		return strings.Join(out, " ")
	}
	with, err := loadBots(t, body("      folder: a\n", "      folder: [b, c]\n"))
	if err != nil {
		t.Fatal(err)
	}
	without, err := loadBots(t, body("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if names(with) != names(without) || !strings.Contains(names(with), "nightly#") {
		t.Fatalf("instance names with folder: %q, without: %q", names(with), names(without))
	}
	if got := strings.Join(with.RemovedFieldWarnings(), "\n"); got != "triggers.nightly[0].folder: "+FolderRemoved+"\ntriggers.nightly[1].folder: "+FolderRemoved {
		t.Fatalf("instance-array warnings:\n%s", got)
	}
}
