package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// engineConns is the one connector every case needs only because Validate
// refuses a config that wires nothing at all.
const engineConns = `
connectors:
  gh: { use: github }
`

// loadYAML writes a whole config and loads it exactly as the daemon does, so
// these tests see the strict decode, the `call:` fold and validation
// together rather than one of them in isolation.
func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "conductor.yaml")
	if err := os.WriteFile(p, []byte(engineConns+body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		return nil, err
	}
	return c, c.Validate()
}

// engineWF is the preamble every single-step case shares: one workflow
// named `w` whose first step is the one under test.
const engineWF = `
workflows:
  w:
    steps:
`

// `use: cli` with a `command:` parses as a CODE step on the cli engine — not
// as the legacy `type: command` dispatch, which the same key used to imply.
func TestStepUseCLIWithCommand(t *testing.T) {
	c, err := loadYAML(t, engineWF+`      - { id: build, use: cli, command: [make, -C, ./svc, release] }
`)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Workflows["w"].Steps[0]
	if s.Form() != "code" {
		t.Fatalf("form = %q, want code", s.Form())
	}
	sel, class := s.StepEngine()
	if sel != "cli" || class != EngineCLI {
		t.Fatalf("engine = %q/%s, want cli/%s", sel, class, EngineCLI)
	}
	if got := []string(s.Command); !reflect.DeepEqual(got, []string{"make", "-C", "./svc", "release"}) {
		t.Fatalf("command = %v", got)
	}
}

// The string form of `command:` is word-split, quotes honored, no expansion.
func TestCommandStringForm(t *testing.T) {
	c, err := loadYAML(t, engineWF+`      - { id: greet, use: cli, command: "echo 'hello  world' $HOME" }
`)
	if err != nil {
		t.Fatal(err)
	}
	got := []string(c.Workflows["w"].Steps[0].Command)
	if !reflect.DeepEqual(got, []string{"echo", "hello  world", "$HOME"}) {
		t.Fatalf("command = %#v", got)
	}
}

func TestCommandStringUnterminatedQuote(t *testing.T) {
	_, err := loadYAML(t, engineWF+`      - { id: x, use: cli, command: "echo 'oops" }
`)
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("err = %v", err)
	}
}

// `use: cli` + `code:` is the host-interpreter shape: the code becomes a
// file and its path is appended to the argv.
func TestStepUseCLIWithCodeParses(t *testing.T) {
	c, err := loadYAML(t, engineWF+`      - id: shape
        use: cli
        command: [bash, -c]
        code: "echo '{\"ok\": true}'"
`)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Workflows["w"].Steps[0]
	if _, class := s.StepEngine(); class != EngineCLI {
		t.Fatalf("class = %s", class)
	}
	if s.Code == "" || len(s.Command) != 2 {
		t.Fatalf("step = %+v", s)
	}
}

func TestStepUseCLINeedsCommand(t *testing.T) {
	_, err := loadYAML(t, engineWF+`      - { id: x, use: cli, code: "echo hi" }
`)
	if err == nil || !strings.Contains(err.Error(), "`use: cli` needs `command:`") {
		t.Fatalf("err = %v", err)
	}
}

// `run:` is unchanged in every respect: the engines it named still resolve
// to the same classes, and its historic permissiveness (any unknown name is
// a host interpreter) is intact.
func TestRunAliasUnchanged(t *testing.T) {
	for _, tc := range []struct {
		run   string
		class EngineClass
	}{
		{"js", EngineInProcess},
		{"go-embed", EngineInProcess},
		{"risor", EngineInProcess},
		{"lua", EngineInProcess},
		{"bash", EngineHost},
		{"go", EngineHost},
		{"python3", EngineHost},
		{"/opt/venv/bin/python", EngineHost},
		{"some-in-house-interpreter", EngineHost},
	} {
		c, err := loadYAML(t, engineWF+`      - { id: x, run: `+tc.run+`, code: "x" }
`)
		if err != nil {
			t.Fatalf("run: %s: %v", tc.run, err)
		}
		s := c.Workflows["w"].Steps[0]
		if s.Form() != "code" {
			t.Errorf("run: %s: form = %q", tc.run, s.Form())
		}
		sel, class := s.StepEngine()
		if sel != tc.run || class != tc.class {
			t.Errorf("run: %s: engine = %q/%s, want %s", tc.run, sel, class, tc.class)
		}
	}
}

// `use:` selects the same engines `run:` did.
func TestUseSelectsSameEnginesAsRun(t *testing.T) {
	for _, tc := range []struct {
		use   string
		class EngineClass
	}{
		{"js", EngineInProcess},
		{"lua", EngineInProcess},
		{"bash", EngineHost},
		{"python3", EngineHost},
		{"./venv/bin/python", EngineHost},
	} {
		c, err := loadYAML(t, engineWF+`      - { id: x, use: "`+tc.use+`", code: "x" }
`)
		if err != nil {
			t.Fatalf("use: %s: %v", tc.use, err)
		}
		sel, class := c.Workflows["w"].Steps[0].StepEngine()
		if sel != tc.use || class != tc.class {
			t.Errorf("use: %s: engine = %q/%s, want %s", tc.use, sel, class, tc.class)
		}
	}
}

// A `use:` naming something conductor cannot run says so, names the
// builtins, and points at `call:` — which is the thing the author probably
// wanted if the name is a workflow.
func TestStepUseUnknownEngineErrors(t *testing.T) {
	_, err := loadYAML(t, engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err == nil {
		t.Fatal("an unknown engine must not load")
	}
	for _, want := range []string{"names no engine conductor can run", "cli, go-embed, js, lua, risor", "`call:`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// The specific case the migration exists for: an unmigrated step `use:` that
// names a workflow now reads as an engine, and the error says exactly that.
func TestStepUseNamingAWorkflowPointsAtCall(t *testing.T) {
	_, err := loadYAML(t, `
workflows:
  review-flow:
    steps:
      - { id: a, uses: gh.comment, options: { body: hi } }
  w:
    steps:
      - { use: review-flow }
`)
	if err == nil {
		t.Fatal("an unmigrated workflow use: must not load")
	}
	if !strings.Contains(err.Error(), "is a workflow — write `call: review-flow`") {
		t.Fatalf("err = %v", err)
	}
}

// `call:` is the workflow call; `workflow:` remains the older spelling of
// the same field and both land in Step.Workflow.
func TestCallAndWorkflowAreOneField(t *testing.T) {
	for _, key := range []string{"call", "workflow"} {
		c, err := loadYAML(t, `
workflows:
  review-flow:
    steps:
      - { id: a, uses: gh.comment, options: { body: hi } }
  w:
    steps:
      - { `+key+`: review-flow, with: { pr: 1 } }
`)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		s := c.Workflows["w"].Steps[0]
		if s.Workflow != "review-flow" || s.Form() != "workflow" {
			t.Fatalf("%s: workflow = %q form = %q", key, s.Workflow, s.Form())
		}
		if s.Call != "" {
			t.Fatalf("%s: Call must be folded away, got %q", key, s.Call)
		}
	}
}

func TestCallAndWorkflowConflict(t *testing.T) {
	_, err := loadYAML(t, `
workflows:
  a: { steps: [ { id: s, uses: gh.comment, options: {} } ] }
  b: { steps: [ { id: s, uses: gh.comment, options: {} } ] }
  w: { steps: [ { call: a, workflow: b } ] }
`)
	if err == nil || !strings.Contains(err.Error(), "they are the same field") {
		t.Fatalf("err = %v", err)
	}
}

func TestUseAndRunTogetherRejected(t *testing.T) {
	_, err := loadYAML(t, engineWF+`      - { id: x, use: js, run: js, code: "x" }
`)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
}

// An in-process engine is local-only under either spelling.
func TestInProcessEngineRejectsHost(t *testing.T) {
	for _, key := range []string{"use", "run"} {
		_, err := loadYAML(t, `
hosts:
  build-box: { host: build01.example.com }
`+engineWF+`      - { id: x, `+key+`: js, code: "x", host: build-box }
`)
		if err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
}

// `command:` belongs to the cli engine; on any other it is a mistake worth
// naming rather than a silently ignored key.
func TestCommandOnNonCLIEngineRejected(t *testing.T) {
	_, err := loadYAML(t, engineWF+`      - { id: x, use: js, code: "x", command: [make] }
`)
	if err == nil || !strings.Contains(err.Error(), "the `cli` engine's argv") {
		t.Fatalf("err = %v", err)
	}
}

// `type: command` is untouched: the same config that worked before still
// parses as the command form.
func TestTypeCommandStillWorks(t *testing.T) {
	for _, body := range []string{
		`      - { id: x, type: command, command: [make, test] }`,
		`      - { id: x, command: [make, test] }`,
	} {
		c, err := loadYAML(t, engineWF+body+"\n")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		s := c.Workflows["w"].Steps[0]
		if s.Form() != "command" {
			t.Fatalf("%s: form = %q, want command", body, s.Form())
		}
		if _, class := s.StepEngine(); class != EngineNone {
			t.Fatalf("%s: a command step selects no engine, got %s", body, class)
		}
	}
}

func TestSplitArgv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"make", []string{"make"}},
		{"  make   test  ", []string{"make", "test"}},
		{`echo "a b" c`, []string{"echo", "a b", "c"}},
		{`echo 'a "b"'`, []string{"echo", `a "b"`}},
		{`echo a\ b`, []string{"echo", "a b"}},
		{`echo ""`, []string{"echo", ""}},
	} {
		got, err := splitArgv(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q -> %#v, want %#v", tc.in, got, tc.want)
		}
	}
	if _, err := splitArgv(`echo "oops`); err == nil {
		t.Error("an unterminated quote must be an error")
	}
}
