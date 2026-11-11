package configmigrate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/configedit"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const githubOld = `# conductor config
connectors:
  # the forge
  gh:
    use: github
    token: ${GH_TOKEN}
    webhook:
      smee_url: ${GH_SMEE_URL}   # the App posts here
      secret: ${GH_WEBHOOK_SECRET}
    sweep: { enabled: false }

  # notifications
  chat:
    use: slack
    bot_token: ${SLACK_BOT_TOKEN}

triggers:
  - on: gh.new_comment
    name: answer
    steps:
      - uses: chat.post
`

const githubNew = `# conductor config
connectors:
  # the forge
  gh:
    use: github
    token: ${GH_TOKEN}
    webhook:
      secret: ${GH_WEBHOOK_SECRET}
      expose: gh-smee
    sweep: { enabled: false }

  # notifications
  chat:
    use: slack
    bot_token: ${SLACK_BOT_TOKEN}
  gh-smee:
    use: smee
    channel: ${GH_SMEE_URL}

triggers:
  - on: gh.new_comment
    name: answer
    steps:
      - uses: chat.post
`

func TestGithubSmeeURLBecomesASmeeConnector(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", githubOld)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, main); got != githubNew {
		t.Fatalf("rewrite:\n%s\nwant:\n%s", got, githubNew)
	}
	if len(res.Files) != 1 || res.Files[0] != main || res.Backups[0] != main+".pre-smee" {
		t.Fatalf("result = %+v", res)
	}
	if got := read(t, main+".pre-smee"); got != githubOld {
		t.Fatalf("backup holds:\n%s", got)
	}
	if len(res.Uses) != 1 || res.Uses[0] != "smee" {
		t.Fatalf("uses = %v", res.Uses)
	}
	if fi, _ := os.Stat(main); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want the original 0640", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(main + ".pre-smee"); fi.Mode().Perm() != 0o640 {
		t.Fatalf("backup mode = %v, want the original 0640", fi.Mode().Perm())
	}
}

// A second run over its own output is a no-op, and a re-run does not touch
// the first backup.
func TestIdempotent(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", githubOld)
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	once := read(t, main)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 || read(t, main) != once {
		t.Fatalf("second run rewrote: %+v\n%s", res, read(t, main))
	}
	plan, err := Compute(main, Options{})
	if err != nil || plan.Changed() {
		t.Fatalf("plan over migrated output: changed=%v err=%v", plan != nil && plan.Changed(), err)
	}
	if got := read(t, main+".pre-smee"); got != githubOld {
		t.Fatal("the first backup was overwritten")
	}
}

// Secret references are carried over as written, never expanded — even
// with the variable set in the environment.
func TestSecretRefsVerbatim(t *testing.T) {
	t.Setenv("GH_SMEE_URL", "https://relay.example/expanded")
	t.Setenv("GH_WEBHOOK_SECRET", "expanded-secret")
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `connectors:
  gh:
    use: github
    webhook:
      smee_url: "${GH_SMEE_URL}"
      secret: ${settings.webhook_secret}
`)
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, main)
	for _, want := range []string{"channel: ${GH_SMEE_URL}", "secret: ${settings.webhook_secret}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "expanded") {
		t.Fatalf("a value was expanded:\n%s", got)
	}
}

// The bundled webhook connector: expose on the connector itself, no listen
// (conductor picks the local address at load), the relay path left at its
// default.
func TestBundledWebhookSmee(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `connectors:
  hooks:
    use: webhook
    smee_url: https://relay.example/ch1
    sources:
      alarms: { path: /alarms, match: '{{if .body.alarm}}true{{end}}' }
`)
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	want := `connectors:
  hooks:
    use: webhook
    sources:
      alarms: { path: /alarms, match: '{{if .body.alarm}}true{{end}}' }
    expose: hooks-smee
  hooks-smee:
    use: smee
    channel: https://relay.example/ch1
    ` + trustComment + `
    trust: full
    isolate: true
`
	if got := read(t, main); got != want {
		t.Fatalf("rewrite:\n%s\nwant:\n%s", got, want)
	}
	// A github listener is a plugin's: its smee connector stays jailed.
	gh := write(t, dir, "gh.yaml", "connectors:\n  gh:\n    use: github\n    webhook:\n      smee_url: https://relay.example/ch2\n")
	if _, err := Apply(gh, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, gh); strings.Contains(got, "trust") || strings.Contains(got, "isolate") {
		t.Fatalf("the github shape's smee connector was unjailed:\n%s", got)
	}
}

// A flow-style webhook connector is rewritten whole; its smee connector is
// unjailed all the same, with the reason above it.
func TestBundledWebhookSmeeFlowStyle(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", "connectors:\n  hooks: { use: webhook, smee_url: https://relay.example/ch1, sources: { a: { path: /a } } }\n")
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, main), trustComment) {
		t.Errorf("no reason given:\n%s", read(t, main))
	}
	doc, err := configedit.Parse([]byte(read(t, main)))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"use": "smee", "channel": "https://relay.example/ch1", "trust": "full", "isolate": true} {
		if got, _ := doc.Get([]string{"connectors", "hooks-smee", k}); got != want {
			t.Errorf("hooks-smee.%s = %v, want %v", k, got, want)
		}
	}
}

// An existing listen address and path are kept (the relay posts to them,
// and the listener stays reachable there); a listener with neither gets
// neither written — conductor picks its local address at load.
func TestListenKeptNoneWritten(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `connectors:
  direct:
    use: github
    webhook:
      listen: 0.0.0.0:9000
      path: /gh
      smee_url: https://relay.example/a
  other:
    use: github
    webhook:
      smee_url: https://relay.example/b
  web:
    use: webhook
    listen: 127.0.0.1:8787
    sources: { s: { path: /s } }
`)
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, main)
	for _, want := range []string{
		"listen: 0.0.0.0:9000\n      path: /gh\n      expose: direct-smee",
		"other:\n    use: github\n    webhook:\n      expose: other-smee\n",
		"direct-smee:\n    use: smee\n    channel: https://relay.example/a",
		"other-smee:\n    use: smee\n    channel: https://relay.example/b",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "listen:") != 2 || strings.Count(got, "path: /gh") != 1 || strings.Contains(got, "/webhook") {
		t.Fatalf("a listen or path was written:\n%s", got)
	}
}

// A config an earlier release of this rule rewrote, with the listen and path
// it wrote then, is left exactly as it is: nothing to rewrite, no backup.
func TestEarlierRewriteLeftAsItIs(t *testing.T) {
	dir := t.TempDir()
	body := `connectors:
  gh:
    use: github
    webhook:
      secret: ${GH_WEBHOOK_SECRET}
      listen: 127.0.0.1:8787
      path: /webhook
      expose: gh-smee
  gh-smee:
    use: smee
    channel: https://relay.example/a
  hooks:
    use: webhook
    sources: { s: { path: /s } }
    listen: 127.0.0.1:8788
    expose: hooks-smee
  hooks-smee:
    use: smee
    channel: https://relay.example/b
    trust: full
    isolate: true
`
	main := write(t, dir, "conductor.yaml", body)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, main) != body || len(res.Files) != 0 {
		t.Fatalf("an earlier rewrite was touched (files %v):\n%s", res.Files, read(t, main))
	}
	if _, err := os.Stat(main + ".pre-smee"); !os.IsNotExist(err) {
		t.Fatal("a backup was written for nothing")
	}
}

// A smee connector already pinned to the same channel is reused, and a
// taken name gets a suffix.
func TestReuseAndNameClash(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `connectors:
  a:
    use: github
    webhook: { listen: "127.0.0.1:9001" }
  a-smee:
    use: ntfy
  b:
    use: github
    webhook:
      smee_url: https://relay.example/shared
  relay:
    use: smee
    channel: https://relay.example/shared
  c:
    use: github
    webhook:
      smee_url: https://relay.example/own
  c-smee:
    use: ntfy
`)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, main)
	if !strings.Contains(got, "expose: relay") {
		t.Fatalf("b should reuse relay:\n%s", got)
	}
	if !strings.Contains(got, "c-smee2:\n    use: smee\n    channel: https://relay.example/own") || !strings.Contains(got, "expose: c-smee2") {
		t.Fatalf("c should get c-smee2:\n%s", got)
	}
	if strings.Count(got, "use: smee") != 2 {
		t.Fatalf("want exactly two smee connectors:\n%s", got)
	}
	if len(res.Notes) != 2 {
		t.Fatalf("notes = %v", res.Notes)
	}
}

// smee_url beside an expose is ambiguous: left alone, with a note.
func TestSmeeURLWithExposeLeftAlone(t *testing.T) {
	dir := t.TempDir()
	body := `connectors:
  gh:
    use: github
    webhook:
      listen: 127.0.0.1:9000
      expose: tunnel1
      smee_url: https://relay.example/a
`
	main := write(t, dir, "conductor.yaml", body)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, main) != body || len(res.Files) != 0 {
		t.Fatal("a connector with both smee_url and expose must be left as it is")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "pick one by hand") {
		t.Fatalf("notes = %v", res.Notes)
	}
}

// A flow-style webhook block can't take in-place edits; the block is
// rewritten whole (still only that entry).
func TestFlowStyleWebhook(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `connectors:
  gh:
    use: github # keep me
    webhook: { smee_url: "${GH_SMEE_URL}", secret: "${S}" }
  # trailing comment
`)
	if _, err := Apply(main, Options{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, main)
	for _, want := range []string{"use: github # keep me", "# trailing comment", "secret: ${S}", "expose: gh-smee", "channel: ${GH_SMEE_URL}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "smee_url") || strings.Contains(got, "listen") {
		t.Fatalf("smee_url left, or a listen written:\n%s", got)
	}
}

// Connectors in imported files: a top-level import, a connectors.imports
// section file of bare entries, and a connector whose body is its own file.
func TestImportedFiles(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", `imports: [base.yaml]
connectors:
  imports: [conf.d/*.yaml]
  body:
    import: ./body.yaml
`)
	base := write(t, dir, "base.yaml", `connectors:
  top:
    use: github
    webhook: { listen: "127.0.0.1:9100" }
`)
	write(t, dir, "base.yaml", read(t, base)+`  top2:
    use: github
    webhook:
      smee_url: https://relay.example/top2
`)
	sect := write(t, dir, "conf.d/hooks.yaml", `hooks:
  use: webhook
  smee_url: https://relay.example/hooks
  sources: { s: { path: /s } }
`)
	body := write(t, dir, "body.yaml", `use: github
webhook:
  smee_url: https://relay.example/body
`)
	res, err := Apply(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 4 {
		t.Fatalf("rewrote %v, want all four files", res.Files)
	}
	if got := read(t, base); !strings.Contains(got, "top2-smee:\n    use: smee") || !strings.Contains(got, "expose: top2-smee") {
		t.Fatalf("base.yaml:\n%s", got)
	}
	if got := read(t, sect); !strings.Contains(got, "\nhooks-smee:\n  use: smee\n  channel: https://relay.example/hooks") || !strings.Contains(got, "expose: hooks-smee") {
		t.Fatalf("section file:\n%s", got)
	}
	if got := read(t, body); !strings.Contains(got, "expose: body-smee") || strings.Contains(got, "use: smee") {
		t.Fatalf("body file:\n%s", got)
	}
	if got := read(t, main); !strings.Contains(got, "body-smee:\n    use: smee\n    channel: https://relay.example/body") {
		t.Fatalf("main (holds the body file's sibling):\n%s", got)
	}
	// Nothing but the listen that was already there.
	all := read(t, base) + read(t, sect) + read(t, body)
	if strings.Count(all, "listen:") != 1 {
		t.Fatalf("a listen was written:\n%s", all)
	}
}

// Ensure runs before anything is written; its failure writes nothing.
func TestEnsureFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", githubOld)
	var asked []string
	_, err := Apply(main, Options{Ensure: func(uses []string, _ map[string][]string) error {
		asked = uses
		return errors.New("offline")
	}})
	if err == nil || !strings.Contains(err.Error(), "nothing was rewritten") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(asked, ",") != "smee" {
		t.Fatalf("ensure asked for %v", asked)
	}
	if read(t, main) != githubOld {
		t.Fatal("the config was rewritten despite the failed install")
	}
	if _, err := os.Stat(main + ".pre-smee"); !os.IsNotExist(err) {
		t.Fatal("a backup was written despite the failed install")
	}
}

// A main-file-only rewrite is validated on a copy before the live file is
// touched; a refusal there writes nothing.
func TestValidateBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", githubOld)
	var seen []string
	_, err := Apply(main, Options{Check: func(p string) (map[string]string, error) {
		if p == main {
			return nil, nil // the baseline
		}
		seen = append(seen, p)
		if read(t, main) != githubOld {
			t.Fatal("the live file changed before validation")
		}
		if !strings.Contains(read(t, p), "expose: gh-smee") {
			t.Fatal("validated something other than the candidate")
		}
		return nil, errors.New("refused")
	}})
	if err == nil || !strings.Contains(err.Error(), "nothing was rewritten") {
		t.Fatalf("err = %v", err)
	}
	if len(seen) != 1 || seen[0] == main || filepath.Dir(seen[0]) != dir {
		t.Fatalf("validated %v, want one copy beside the main file", seen)
	}
	if read(t, main) != githubOld {
		t.Fatal("rewritten despite the refusal")
	}
	if _, err := os.Stat(seen[0]); !os.IsNotExist(err) {
		t.Fatal("the validation copy was left behind")
	}
}

// When imports change too, the swapped tree is validated and a refusal
// restores every file.
func TestValidateAfterSwapRestoresEveryFile(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", "imports: [more.yaml]\n"+githubOld)
	more := write(t, dir, "more.yaml", `connectors:
  gh2:
    use: github
    webhook:
      smee_url: https://relay.example/two
`)
	origMain, origMore := read(t, main), read(t, more)
	calls := 0
	_, err := Apply(main, Options{Check: func(p string) (map[string]string, error) {
		calls++
		if p != main {
			t.Fatalf("validated %s, want the live main file", p)
		}
		if calls == 1 {
			return nil, nil // the baseline
		}
		if !strings.Contains(read(t, more), "expose: gh2-smee") {
			t.Fatal("validated before the swap")
		}
		return nil, errors.New("refused")
	}})
	if err == nil || !strings.Contains(err.Error(), "originals restored") {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || read(t, main) != origMain || read(t, more) != origMore {
		t.Fatal("not every file was restored")
	}
}

func TestNothingToDo(t *testing.T) {
	dir := t.TempDir()
	body := "connectors:\n  gh:\n    use: github\n    webhook: { listen: \"127.0.0.1:9000\" }\n"
	main := write(t, dir, "conductor.yaml", body)
	res, err := Apply(main, Options{Ensure: func([]string, map[string][]string) error {
		t.Fatal("nothing to install")
		return nil
	}})
	if err != nil || len(res.Files) != 0 || read(t, main) != body {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(main + ".pre-smee"); !os.IsNotExist(err) {
		t.Fatal("a backup was written for nothing")
	}
}

// A rewrite that leaves a touched connector disabled is refused, unless that
// connector was already disabled before it.
func TestCheckRefusesANewlyDisabledConnector(t *testing.T) {
	for _, c := range []struct {
		name    string
		before  map[string]string
		after   map[string]string
		refused bool
	}{
		{"the new smee connector disabled", nil, map[string]string{"gh-smee": "plugin missing"}, true},
		{"the migrated connector newly disabled", nil, map[string]string{"gh": "expose target disabled"}, true},
		{"already disabled before", map[string]string{"gh": "no credential"}, map[string]string{"gh": "no credential"}, false},
		{"an untouched connector disabled", nil, map[string]string{"chat": "bad token"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			main := write(t, dir, "conductor.yaml", githubOld)
			_, err := Apply(main, Options{Check: func(p string) (map[string]string, error) {
				if p == main {
					return c.before, nil
				}
				return c.after, nil
			}})
			if c.refused != (err != nil) {
				t.Fatalf("err = %v, want refused=%v", err, c.refused)
			}
			if c.refused && read(t, main) != githubOld {
				t.Fatal("rewritten despite the refusal")
			}
			if !c.refused && read(t, main) != githubNew {
				t.Fatal("not rewritten")
			}
		})
	}
}

const bothOld = `connectors:
  gh:
    use: github
    webhook:
      smee_url: ${GH_SMEE_URL}
      secret: ${GH_WEBHOOK_SECRET}
  hooks:
    use: webhook
    smee_url: ${HOOKS_SMEE_URL}
`

// The github rewrite wires smee to a jailed listener, so it asks for smee
// granted expose_local and its channel; when that can't be had, only that
// rewrite is held back (github's own smee_url keeps working), with a
// warning, and the bundled webhook connector — whose relay runs unjailed —
// is migrated anyway.
func TestGrantHoldsBackOnlyTheJailedRewrite(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", bothOld)
	var asked []Grant
	res, err := Apply(main, Options{Grant: func(g Grant) ([]string, error) {
		asked = append(asked, g)
		return nil, errors.New("no smee release declaring expose_local is available yet")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0].Use != "smee" || !asked[0].ExposeLocal || !slices.Equal(asked[0].Egress, []string{"config:channel"}) {
		t.Fatalf("asked for %+v", asked)
	}
	got := read(t, main)
	if !strings.Contains(got, "smee_url: ${GH_SMEE_URL}") || strings.Contains(got, "gh-smee") {
		t.Fatalf("the github connector was not held back:\n%s", got)
	}
	if strings.Contains(got, "smee_url: ${HOOKS_SMEE_URL}") || !strings.Contains(got, "hooks-smee:") {
		t.Fatalf("the bundled webhook connector was not migrated:\n%s", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no smee release declaring expose_local") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
	if !slices.ContainsFunc(res.Notes, func(n string) bool { return strings.HasPrefix(n, "gh.webhook: left on smee_url for now") }) {
		t.Fatalf("notes = %q", res.Notes)
	}

	// A second run, with the grant available now, finishes the job; what
	// the grant did is a note of the run.
	res, err = Apply(main, Options{Grant: func(Grant) ([]string, error) {
		return []string{"approved smee v1.2.0"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, main); strings.Contains(got, "smee_url:") || !strings.Contains(got, "expose: gh-smee") {
		t.Fatalf("not finished:\n%s", got)
	}
	if len(res.Warnings) != 0 || !slices.Contains(res.Notes, "approved smee v1.2.0") {
		t.Fatalf("warnings = %q, notes = %q", res.Warnings, res.Notes)
	}
}

// Only a jailed relay asks for a grant: the bundled webhook connector's is
// unjailed.
func TestBundledWebhookAsksNoGrant(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "conductor.yaml", "connectors:\n  hooks:\n    use: webhook\n    smee_url: ${HOOKS_SMEE_URL}\n")
	plan, err := Compute(main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Grants) != 0 {
		t.Fatalf("grants = %+v", plan.Grants)
	}
}
