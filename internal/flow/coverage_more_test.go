package flow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/hosts"
)

func TestFilterMatch(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	r := New(Runner{Cfg: cfg, Conns: reg})

	spec := mustSpec(t, "on: svc.ping\nfilters: { only: \"deploy\" }")
	ok, err := r.FilterMatch(newTrigger("ping", map[string]any{"msg": "deploy started"}), spec)
	if err != nil || !ok {
		t.Fatalf("matching filter: %v %v", ok, err)
	}
	ok, _ = r.FilterMatch(newTrigger("ping", map[string]any{"msg": "other"}), spec)
	if ok {
		t.Fatal("non-matching filter must not match")
	}
	// No filters, and an unknown connector: both pass through.
	if ok, _ := r.FilterMatch(newTrigger("ping", nil), mustSpec(t, "on: svc.ping")); !ok {
		t.Fatal("no filters → true")
	}
	if ok, _ := r.FilterMatch(newTrigger("ping", nil), mustSpec(t, "on: ghost.ping\nfilters: {x: 1}")); !ok {
		t.Fatal("unknown connector → true")
	}
}

// TestCommandStepThroughRun: a type: command step dispatches with Wait and
// its JSON output becomes step outputs (extractOutputs unwrapping).
func TestCommandStepThroughRun(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: `{"output":{"lines":"42"}}`}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: count, type: command, command: [wc, -l] }
  - { id: tell, uses: svc.post, options: { text: "n={{.count.lines}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if got := calls[len(calls)-1].Opts["text"]; got != "n=42" {
		t.Fatalf("command outputs not extracted: %v", got)
	}
	reqs := rig.Agents.requests()
	if len(reqs) != 1 || !reqs[0].Wait || reqs[0].Action.Type != "command" {
		t.Fatalf("command dispatch shape: %+v", reqs)
	}
}

// TestDryRunStubsVerbCodeAndRemoteCommand: DryRun stubs a verb step (declared
// outputs as zero values + stubbed marker), a code step, and a host command.
func TestDryRunStubsVerbCodeAndRemoteCommand(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
hosts:
  box: { host: b.internal }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.DryRun = true
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: v, uses: svc.post, options: { text: hi } }
  - { id: c, run: js, code: "return {}" }
  - { id: r, type: command, command: [make, deploy], host: box }
  - { id: after, uses: svc.post, options: { text: "id={{.v.id}} stub={{.v.stubbed}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("dry run failed: %s", errStr)
	}
	if got := st.count("post"); got != 0 {
		t.Fatalf("dry run must not invoke verbs, got %d", got)
	}
	stubbed := 0
	for _, e := range rig.Store.audits {
		if e["outcome"] == "stubbed" {
			stubbed++
		}
	}
	if stubbed < 2 {
		t.Fatalf("stubbed verb audits: %d", stubbed)
	}
}

func TestResolveListForms(t *testing.T) {
	data := map[string]any{
		"list":  []any{"a", "b"},
		"strs":  []string{"x", "y"},
		"other": 7,
	}
	if items, err := resolveList("{{.list}}", data); err != nil || len(items) != 2 {
		t.Fatalf("[]any: %v %v", items, err)
	}
	if items, err := resolveList("{{.strs}}", data); err != nil || len(items) != 2 || items[0] != "x" {
		t.Fatalf("[]string: %v %v", items, err)
	}
	if _, err := resolveList("{{.missing}}", data); err == nil {
		t.Fatal("missing ref must error")
	}
	// A rendered scalar splits on commas/newlines.
	if items, err := resolveList("a, b\nc", data); err != nil || len(items) != 3 {
		t.Fatalf("split forms: %v %v", items, err)
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := shellJoin([]string{"echo", "it's"}); got != `'echo' 'it'\''s'` {
		t.Fatalf("shellJoin: %s", got)
	}
	if tail("abc", 2) != "…bc" || tail("abc", 5) != "abc" {
		t.Fatal("tail")
	}
	if got := snippet(strings.Repeat("x", 100) + "\n"); !strings.HasSuffix(got, "…") || strings.Contains(got, "\n") {
		t.Fatalf("snippet: %q", got)
	}
	if rt := retryTimeout(&config.RetrySpec{}); rt != 15*time.Minute {
		t.Fatalf("default retry timeout: %v", rt)
	}
	if rt := retryTimeout(&config.RetrySpec{Timeout: config.Duration(time.Minute)}); rt != time.Minute {
		t.Fatalf("explicit retry timeout: %v", rt)
	}
	// RenderOptions is the exported wrapper the notify router uses.
	out, err := RenderOptions(map[string]any{"text": "{{.x}}"}, map[string]any{"x": "v"})
	if err != nil || out["text"] != "v" {
		t.Fatalf("RenderOptions: %v %v", out, err)
	}
	if _, err := renderStringMap(map[string]string{"k": "{{.broken"}, nil); err == nil {
		t.Fatal("bad env template must error")
	}
}

// TestValidateNotifyVia: via routes resolve against the notify scope and the
// connector's verb contract.
func TestValidateNotifyVia(t *testing.T) {
	base := `
connectors:
  svc: { type: fake }
notify:
  via:
    - ROUTE
`
	load := func(route string) error {
		cfg := loadConfig(t, strings.Replace(base, "ROUTE", route, 1))
		return Validate(cfg, buildRegistry(t, cfg))
	}
	if err := load(`{ uses: svc.post, options: { text: "{{.message}}" }, on: [escalate] }`); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}
	cases := []struct{ route, wantErr string }{
		{`{ uses: bad }`, "must be <connector>.<verb>"},
		{`{ uses: ghost.post }`, `unknown connector "ghost"`},
		{`{ uses: svc.zap }`, `no verb "zap"`},
		{`{ uses: svc.post, options: { text: t }, on: [nope] }`, `unknown event "nope"`},
		{`{ uses: svc.post, options: { text: "{{.no_such}}" } }`, "no_such"},
	}
	for _, c := range cases {
		if err := load(c.route); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("route %s: err = %v, want %q", c.route, err, c.wantErr)
		}
	}
}

// TestGrouperRealClock: the default clock debounces and fires (Now/AfterFunc
// on the real clock), and Wait drains.
func TestGrouperRealClock(t *testing.T) {
	fired := make(chan []core.Trigger, 1)
	g := NewGrouper(nil, func(key string, events []core.Trigger) { fired <- events })
	g.Add("k", newTrigger("ping", nil), 10*time.Millisecond, 0)
	g.Add("k", newTrigger("ping", nil), 10*time.Millisecond, 0)
	select {
	case events := <-fired:
		if len(events) != 2 {
			t.Fatalf("batched events: %d", len(events))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grouper never fired on the real clock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	g.Wait(ctx)
}

// TestRemoteCommandStep: a host: command step runs through the SSH client
// seam — env/cwd applied, non-zero exit surfaced as a step error.
func TestRemoteCommandStep(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
hosts:
  box: { host: b.internal, user: ci }
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	var gotScript string
	rig.Runner.Code.SSH = &hosts.Client{Run: func(_ context.Context, argv []string, stdin []byte) (string, string, int, error) {
		gotScript = argv[len(argv)-1]
		return "out-ok", "", 0, nil
	}}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: run, type: command, command: [make, "deploy {{.msg}}"], host: box, env: { CI: "1" } }
  - { id: tell, uses: svc.post, options: { text: "{{.run.stdout}}/{{.run.exit_code}}" } }
`)
	st := newFakeState(t, "svc")
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "prod"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("remote command failed: %s", errStr)
	}
	if !strings.Contains(gotScript, "'deploy prod'") {
		t.Fatalf("templated argv not shell-joined: %q", gotScript)
	}
	calls := st.snapshot()
	if got := calls[len(calls)-1].Opts["text"]; got != "out-ok/0" {
		t.Fatalf("remote outputs: %v", got)
	}

	// Non-zero exit fails the step with the stderr tail.
	rig2 := newTestRunner(t, cfg, reg)
	rig2.Runner.Code.SSH = &hosts.Client{Run: func(context.Context, []string, []byte) (string, string, int, error) {
		return "", "boom detail", 3, nil
	}}
	runTrigger(rig2, newTrigger("ping", map[string]any{"msg": "x"}), mustSpec(t, `
on: svc.ping
steps: [ { id: run, type: command, command: [false], host: box } ]
`))
	failed, errStr := rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "exit 3") || !strings.Contains(errStr, "boom detail") {
		t.Fatalf("remote failure: %v %q", failed, errStr)
	}
}

// TestForEachParallelFanout: parallel: true fans iterations concurrently and
// a failing item fails the step.
func TestForEachParallelFanout(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fan
    for_each: "{{.items}}"
    parallel: true
    uses: svc.post
    options: { text: "{{.item}}" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"items": []any{"a", "b", "c"}}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("parallel fan failed: %s", errStr)
	}
	if got := st.count("post"); got != 3 {
		t.Fatalf("fan calls: %d", got)
	}

	// One failing iteration fails the whole step.
	st2 := newFakeState(t, "svc")
	st2.failIf["post"] = func(opts map[string]any) bool { return opts["text"] == "b" }
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", map[string]any{"items": []any{"a", "b"}}), spec)
	if failed, _ := rig2.workflowFailed(); !failed {
		t.Fatal("failing iteration should fail the fan-out")
	}
}

// TestDeferRetryWhileOutputMatches: the defer-retry loop re-runs while the
// output matches, then completes.
func TestDeferRetryWhileOutputMatches(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	_ = newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	var n int
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		n++
		if n < 3 {
			return dispatch.RunRef{Output: "not ready"}, nil
		}
		return dispatch.RunRef{Output: "ready"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: poll, type: command, command: [check],
      retry: { while_output_matches: "not ready", interval: 1ms, timeout: 5s } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("defer-retry failed: %s", errStr)
	}
	if n != 3 {
		t.Fatalf("defer-retry attempts: %d", n)
	}
}

func TestTemplateRefsControlStructures(t *testing.T) {
	refs, err := templateRefs(`{{if .flag}}{{.a}}{{else}}{{.b}}{{end}}{{with .obj}}{{.inner}}{{end}}{{range .list}}{{.item}}{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(refs, ",")
	for _, want := range []string{"flag", "a", "b", "obj", "list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("refs missing %q: %s", want, got)
		}
	}
	// dot-rebound bodies are not root reads.
	if strings.Contains(got, "inner") || strings.Contains(got, "item") {
		t.Fatalf("rebound refs must be skipped: %s", got)
	}
}

func TestRenderValueShapes(t *testing.T) {
	data := map[string]any{"n": 7, "list": []any{1, 2}}
	// A sole ref keeps the underlying type.
	v, err := renderValue("{{.n}}", data)
	if err != nil || v != 7 {
		t.Fatalf("sole ref type: %v %v", v, err)
	}
	// A missing sole ref renders nil.
	v, _ = renderValue("{{.missing}}", data)
	if v != nil {
		t.Fatalf("missing sole ref: %v", v)
	}
	// Nested maps, []any, and []string all render.
	v, err = renderValue(map[string]any{
		"m": map[string]any{"x": "{{.n}}"},
		"l": []any{"{{.n}}", 5},
		"s": []string{"{{.n}}"},
	}, data)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["m"].(map[string]any)["x"] != 7 || m["l"].([]any)[0] != 7 || m["s"].([]any)[0] != 7 {
		t.Fatalf("nested render: %+v", m)
	}
}
