package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

const folderPack = `
pack:
  name: kit
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
workflows:
  tidy:
    description: tidy up
    steps: [ { log: "tidy" } ]
  sweep:
    steps: [ { log: "sweep" } ]
triggers:
  - on: manual
    name: nightly
    steps: [ { log: "nightly" } ]
  - on: manual
    name: weekly
    steps: [ { log: "weekly" } ]
`

func loadFolderPack(t *testing.T, manifest, instance string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", manifest)
	return resolveAndLoad(t, writeDoc(t, dir, "packs:\n  kit:\n    source: ./src/kit\n"+instance))
}

func trig(c *Config, name string) TriggerSpec {
	for _, t := range c.Triggers {
		if t.Name == name {
			return t
		}
	}
	return TriggerSpec{}
}

func TestWorkflowAndTriggerFolders(t *testing.T) {
	c, err := loadBots(t, "workflows:\n  w1: { folder: \" work / ops \", steps: [ { log: x } ] }\n  w2: { steps: [ { log: x } ] }\ntriggers:\n  - { on: manual, name: t1, folder: personal/home, steps: [ { log: x } ] }\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkflowFolder("w1") != "work/ops" || c.WorkflowFolder("w2") != "" || c.TriggerFolder(trig(c, "t1")) != "personal/home" {
		t.Fatalf("folders: %q %q %q", c.WorkflowFolder("w1"), c.WorkflowFolder("w2"), c.TriggerFolder(trig(c, "t1")))
	}
	for name, body := range map[string]string{
		"workflow ..":   "workflows:\n  w: { folder: \"a/../b\", steps: [ { log: x } ] }\n",
		"workflow //":   "workflows:\n  w: { folder: \"a//b\", steps: [ { log: x } ] }\n",
		"trigger space": "triggers:\n  - { on: manual, name: t, folder: \"a b\", steps: [ { log: x } ] }\n",
		"trigger deep":  "triggers:\n  - { on: manual, name: t, folder: \"a/b/c/d/e/f/g/h/i\", steps: [ { log: x } ] }\n",
	} {
		if _, err := loadBots(t, body); err == nil || !strings.Contains(err.Error(), "folder") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A bot's synthesized workers are listed with the bot (or its team).
	c, err = loadTeams(t, "teams:\n  x: { folder: work/platform }\nbots:\n  a: { team: x }\n  s: { folder: personal/home }\n")
	if err != nil {
		t.Fatal(err)
	}
	if f := c.TriggerFolder(trig(c, BotWorkerTrigger("a"))); f != "work/platform" {
		t.Errorf("a team bot's worker: %q", f)
	}
	if f := c.TriggerFolder(trig(c, BotHandoffTrigger("s"))); f != "personal/home" {
		t.Errorf("a solo bot's hand-off: %q", f)
	}
}

// A pack cannot file its own workflows or triggers — at load, and in lint.
func TestPackCannotSetFolder(t *testing.T) {
	for name, man := range map[string]string{
		"workflow": strings.Replace(folderPack, "    description: tidy up\n", "    description: tidy up\n    folder: theirs\n", 1),
		"trigger":  strings.Replace(folderPack, "    name: nightly\n", "    name: nightly\n    folder: theirs\n", 1),
	} {
		_, err := loadFolderPack(t, man, "")
		if err == nil || !strings.Contains(err.Error(), "a pack cannot file its own") || !strings.Contains(err.Error(), "packs.<instance>.") {
			t.Errorf("%s: %v", name, err)
		}
		dir := t.TempDir()
		writePackSource(t, dir, "p", man)
		if _, err := LintPackDir(dir + "/p"); err == nil || !strings.Contains(err.Error(), "a pack cannot file its own") {
			t.Errorf("%s lint: %v", name, err)
		}
	}
	if _, err := loadFolderPack(t, folderPack, ""); err != nil {
		t.Fatalf("the clean pack: %v", err)
	}
}

// OVERRIDE PLACEMENT: per-name wins over the instance's folder, which wins
// over the default (packs/<instance>).
func TestPackFolderOverrides(t *testing.T) {
	c, err := loadFolderPack(t, folderPack, "")
	if err != nil {
		t.Fatal(err)
	}
	if f := c.WorkflowFolder("kit/tidy"); f != "packs/kit" {
		t.Fatalf("default workflow folder: %q (workflows: %v)", f, keys(c.Workflows))
	}
	if f := c.TriggerFolder(trig(c, "kit/nightly")); f != "packs/kit" {
		t.Fatalf("default trigger folder: %q", f)
	}
	c, err = loadFolderPack(t, folderPack, "    folder: work/kit\n    workflows: { tidy: { folder: work/kit/tidy } }\n    triggers: { nightly: { folder: ops/nightly } }\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][3]string{
		{"workflow tidy", c.WorkflowFolder("kit/tidy"), "work/kit/tidy"},
		{"workflow sweep", c.WorkflowFolder("kit/sweep"), "work/kit"},
		{"trigger nightly", c.TriggerFolder(trig(c, "kit/nightly")), "ops/nightly"},
		{"trigger weekly", c.TriggerFolder(trig(c, "kit/weekly")), "work/kit"},
	} {
		if tc[1] != tc[2] {
			t.Errorf("%s: %q, want %q", tc[0], tc[1], tc[2])
		}
	}
	// "*" files every trigger; a named arm wins over it.
	c, err = loadFolderPack(t, folderPack, "    triggers:\n      \"*\": { folder: star }\n      nightly: { folder: named }\n")
	if err != nil {
		t.Fatal(err)
	}
	if n, w := c.TriggerFolder(trig(c, "kit/nightly")), c.TriggerFolder(trig(c, "kit/weekly")); n != "named" || w != "star" {
		t.Errorf("star vs named: nightly %q, weekly %q", n, w)
	}
	// Two armed instances, each filed on its own.
	c, err = loadFolderPack(t, folderPack, "    triggers:\n      nightly:\n        - { enabled: true, options: { a: 1 }, folder: one }\n        - { enabled: true, options: { a: 2 }, folder: two }\n")
	if err != nil {
		t.Fatal(err)
	}
	var fs []string
	for _, tr := range c.Triggers {
		if strings.HasPrefix(tr.Name, "kit/nightly") {
			fs = append(fs, c.TriggerFolder(tr))
		}
	}
	sort.Strings(fs)
	if strings.Join(fs, ",") != "one,two" {
		t.Errorf("per-instance folders: %v", fs)
	}
	for name, inst := range map[string]string{
		"unknown workflow": "    workflows: { nope: { folder: x } }\n",
		"bad folder":       "    folder: \"../x\"\n",
		"bad arm folder":   "    triggers: { nightly: { folder: \"a//b\" } }\n",
	} {
		if _, err := loadFolderPack(t, folderPack, inst); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// NO BEHAVIOR: moving workflows and triggers — operator, pack and fanned-out
// instances — between folders changes nothing else about them: steps,
// gating, scope, policy, isolation, and the instance identity that keys
// their state.
func TestEntityFoldersChangeNothing(t *testing.T) {
	const base = `
workflows:
  w1: { %W% description: a workflow, steps: [ { log: x } ] }
triggers:
  review:
    on: gh.pull_request
    %T%
    filter: { repo: [acme/web] }
    policy: { budget: { max_tokens: 1000 } }
    steps: [ { type: agent, prompt: hi } ]
  fan:
    - { on: manual, %I% options: { a: 1 }, steps: [ { log: x } ] }
    - { on: manual, options: { a: 2 }, steps: [ { log: x } ] }
`
	render := func(w, tr, in string) string {
		r := strings.NewReplacer("%W%", w, "%T%", tr, "%I%", in)
		return r.Replace(base)
	}
	b, err := loadBots(t, render("", "", ""))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, f := range [][3]string{
		{"folder: work/ops,", "folder: personal/home", "folder: a/b,"},
		{"folder: x,", "folder: x", ""},
	} {
		c, err := loadBots(t, render(f[0], f[1], f[2]))
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Triggers) != len(b.Triggers) {
			t.Fatalf("%v: %d triggers, want %d", f, len(c.Triggers), len(b.Triggers))
		}
		for i := range c.Triggers {
			got, want := c.Triggers[i], b.Triggers[i]
			got.Folder, want.Folder = "", ""
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%v: trigger %s changed:\n got %+v\nwant %+v", f, want.Name, got, want)
			}
		}
		for n := range b.Workflows {
			got, want := c.Workflows[n], b.Workflows[n]
			got.Folder, want.Folder = "", ""
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%v: workflow %s changed", f, n)
			}
		}
	}
	// A pack's armed instances keep their identities when filed elsewhere.
	inst := "    triggers:\n      nightly:\n        - { enabled: true, options: { a: 1 }%F% }\n        - { enabled: true, options: { a: 2 } }\n"
	p0, err := loadFolderPack(t, folderPack, strings.Replace(inst, "%F%", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	p1, err := loadFolderPack(t, folderPack, strings.Replace(inst, "%F%", ", folder: work/nightly", 1)+"    folder: work/kit\n    workflows: { tidy: { folder: z } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(p0.Triggers) != len(p1.Triggers) {
		t.Fatalf("pack triggers: %d vs %d", len(p0.Triggers), len(p1.Triggers))
	}
	for i := range p0.Triggers {
		got, want := p1.Triggers[i], p0.Triggers[i]
		if got.Name != want.Name {
			t.Errorf("an instance's identity moved with its folder: %s vs %s", got.Name, want.Name)
		}
		got.Folder, want.Folder = "", ""
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pack trigger %s changed:\n got %+v\nwant %+v", want.Name, got, want)
		}
	}
	for n := range p0.Workflows {
		got, want := p1.Workflows[n], p0.Workflows[n]
		got.Folder, want.Folder = "", ""
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pack workflow %s changed", n)
		}
	}
}
