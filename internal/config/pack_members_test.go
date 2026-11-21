package config

import (
	"slices"
	"strings"
	"testing"
)

// A pack with a list socket: every github connector it is given is read,
// one is written to by name, and a push from any of them fires the trigger.
const listSocketPack = `
pack:
  name: fanout
  version: 1.0.0
  requires:
    connectors:
      repos: { many: true, min: 2, type: github }
workflows:
  sync:
    steps:
      - { id: read, uses: repos.pr_snapshot, over: true, options: { repo: x/y, pr: 1 } }
      - { id: write, uses: repos.comment, member: "{{.connector}}", options: { repo: x/y, number: 1, body: hi } }
triggers:
  - name: on-pr
    "on": repos.pull_request
    steps:
      - { id: run, workflow: sync }
`

func loadListPack(t *testing.T, manifest, binding string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/fanout", manifest)
	path := writeDoc(t, dir, `
connectors:
  a: { use: github, token: x }
  b: { use: github, token: y }
  c: { use: github, token: z }
packs:
  fan:
    source: ./src/fanout
    connectors: `+binding+`
    triggers: { on-pr: { enabled: true, repos: [x/y] } }
`)
	return resolveAndLoad(t, path)
}

func TestListSocketBindsMembersAndExpandsTrigger(t *testing.T) {
	cfg, err := loadListPack(t, listSocketPack, "{ repos: [b, a] }")
	if err != nil {
		t.Fatal(err)
	}
	wf := cfg.Workflows["fan/sync"]
	read, write := wf.Steps[0], wf.Steps[1]
	if !read.Over || !slices.Equal(read.Members, []string{"b", "a"}) || read.Uses != "b.pr_snapshot" {
		t.Fatalf("over step not bound to the list: %+v", read)
	}
	if write.Member != "{{.connector}}" || !slices.Equal(write.Members, []string{"b", "a"}) || write.Uses != "b.comment" {
		t.Fatalf("member step not bound: %+v", write)
	}
	var srcs []string
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "fan/on-pr") {
			srcs = append(srcs, tr.Sources()...)
		}
	}
	slices.Sort(srcs)
	if !slices.Equal(srcs, []string{"a.pull_request", "b.pull_request"}) {
		t.Fatalf("trigger should fire from every member and only them, got %v", srcs)
	}
}

func TestListSocketErrors(t *testing.T) {
	cases := []struct{ name, manifest, binding, want string }{
		{"never auto-bound", listSocketPack, "{}", "requires a list of connectors"},
		{"below min", listSocketPack, "{ repos: [a] }", "at least 2"},
		{"unknown member", listSocketPack, "{ repos: [a, nope] }", `"nope", which is no connector`},
		{"duplicate", listSocketPack, "{ repos: [a, a] }", "twice"},
		{"wrong type", strings.Replace(listSocketPack, "type: github", "type: gitlab", 1), "{ repos: [a, b] }", "every member must be gitlab"},
		{"list on single socket",
			strings.Replace(listSocketPack, "repos: { many: true, min: 2, type: github }", "repos: \"*\"", 1),
			"{ repos: [a, b] }", "single-connector socket"},
		{"step names list without saying which",
			strings.Replace(listSocketPack, "over: true, ", "", 1), "{ repos: [a, b] }", "say which member"},
		{"pack may not write members",
			strings.Replace(listSocketPack, "over: true,", "over: true, members: [c],", 1), "{ repos: [a, b] }", "sets members:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadListPack(t, c.manifest, c.binding)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestListSocketManifestRoundTrip(t *testing.T) {
	var reqs ConnectorReqs
	if err := strictUnmarshal([]byte("cal: { many: true, min: 2, type: google-calendar, version: '>=1.2.0' }\nsolo: '*'\n"), &reqs); err != nil {
		t.Fatal(err)
	}
	if r := reqs["cal"]; !r.Many || r.Min != 2 || r.Type != "google-calendar" || r.Version != ">=1.2.0" {
		t.Fatalf("decoded %+v", r)
	}
	if err := strictUnmarshal([]byte("x: { min: 2 }\n"), &reqs); err == nil || !strings.Contains(err.Error(), "many: true") {
		t.Fatalf("min without many should fail, got %v", err)
	}
}
