package connector

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// abiSourcer is a plugin client double implementing the whole source
// surface: it records the start_source request, streams fixed events, and
// answers poll / translate / app_token / target_head.
type abiSourcer struct {
	mu     sync.Mutex
	req    plugin.StartSourceRequest
	events []sdk.SourceEvent
	polls  []sdk.PollRequest
	pollEv []sdk.SourceEvent
	transl []sdk.TranslateRequest
}

// plainSourcer is a source that implements nothing optional.
type plainSourcer struct {
	req    plugin.StartSourceRequest
	events []sdk.SourceEvent
}

func (p *plainSourcer) StartSource(_ context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error {
	p.req = req
	for _, e := range p.events {
		raw, _ := json.Marshal(e)
		emit(raw)
	}
	return nil
}

func (a *abiSourcer) StartSource(_ context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error {
	a.mu.Lock()
	a.req = req
	a.mu.Unlock()
	for _, e := range a.events {
		raw, _ := json.Marshal(e)
		emit(raw)
	}
	return nil
}
func (a *abiSourcer) Poll(_ context.Context, req sdk.PollRequest) ([]sdk.SourceEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polls = append(a.polls, req)
	return a.pollEv, nil
}
func (a *abiSourcer) Validate(_ context.Context, req sdk.ValidateRequest) ([]sdk.Problem, error) {
	if req.Config["secret"] == nil {
		return []sdk.Problem{{Path: "secret", Message: "required"}}, nil
	}
	return nil, nil
}
func (a *abiSourcer) Translate(_ context.Context, req sdk.TranslateRequest) ([]sdk.SourceEvent, error) {
	a.transl = append(a.transl, req)
	return a.pollEv, nil
}
func (a *abiSourcer) AppToken(_ context.Context, _ string, id int64) (string, error) {
	return "tok-" + itoa64(id), nil
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

func abiTrigger(t *testing.T, idx int, on, name, filter string, opts map[string]any) CompiledTrigger {
	t.Helper()
	spec := config.TriggerSpec{On: on, Name: name, Options: opts}
	if filter != "" {
		spec.Filter = mustFilter(t, filter)
	}
	return CompiledTrigger{Index: idx, Spec: spec}
}

func runPSI(t *testing.T, psi *pluginSourceIntegration) []core.Trigger {
	t.Helper()
	var got []core.Trigger
	if err := psi.Start(context.Background(), func(_ context.Context, tr core.Trigger) { got = append(got, tr) }); err != nil {
		t.Fatal(err)
	}
	return got
}

// A plugin is handed its triggers — filters in structural form —
// and a routed event fires exactly the trigger it names: not its siblings on
// the same event, and with no daemon-side re-evaluation of a filter whose
// keys only the plugin understands.
func TestABISourceRoutesToTheNamedTrigger(t *testing.T) {
	a := abiTrigger(t, 0, "gh.self_review", "a", "label_any: [x]", map[string]any{"max_attempts_per_head": 3,
		"flaky_rerun": map[string]any{"enabled": true, "max": 2}})
	b := abiTrigger(t, 1, "gh.self_review", "b", "", nil)
	src := &abiSourcer{events: []sdk.SourceEvent{
		{Event: "self_review", Trigger: a.Ref(), Target: sdk.Target{Repo: "o/r", Number: 7}, Context: map[string]any{"pr": 7}, CatchUp: true},
	}}
	// The event declares a remediation, so the option it names is lowered.
	rem := &sdk.EventSemantics{Remediate: &sdk.RemediateSemantics{Option: "flaky_rerun", Run: "run_id"}}
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github",
		triggers: []CompiledTrigger{a, b}, log: t.Logf, sem: map[string]*sdk.EventSemantics{"self_review": rem}}
	got := runPSI(t, psi)

	if len(src.req.Triggers) != 2 || src.req.Triggers[0].ID != a.Ref() || src.req.Triggers[0].Event != "self_review" ||
		!strings.Contains(string(src.req.Triggers[0].Filter), `"key":"label_any"`) || src.req.Triggers[1].Filter != nil {
		t.Fatalf("triggers not delivered as expected: %+v", src.req.Triggers)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly the named trigger to fire, got %d: %+v", len(got), got)
	}
	g := got[0]
	act, _ := g.Action.(config.Action)
	if g.Variant != "a" || act.FlowRef != a.Ref() || act.MaxAttemptsPerHead != 3 || !g.CatchUp || g.Target.Number != 7 ||
		!act.FlakyRerun.Enabled || act.FlakyRerun.Max != 2 {
		t.Fatalf("routed trigger wrong: variant=%q act=%+v catchup=%v target=%+v", g.Variant, act, g.CatchUp, g.Target)
	}
}

// A routed event cannot reach a trigger on a different event, nor one that
// does not exist, nor another instance's.
func TestABISourceRefusesMisroutedEvents(t *testing.T) {
	a := abiTrigger(t, 0, "gh.new_comment", "a", "", nil)
	src := &abiSourcer{events: []sdk.SourceEvent{
		{Event: "release", Trigger: a.Ref()},                      // wrong event for the trigger
		{Event: "new_comment", Trigger: "9:gh.new_comment"},       // no such trigger
		{Event: "new_comment", Trigger: a.Ref(), Instance: "gh2"}, // another instance
	}}
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github",
		triggers: []CompiledTrigger{a}, log: t.Logf}
	if got := runPSI(t, psi); len(got) != 0 {
		t.Fatalf("misrouted events fired: %+v", got)
	}
}

// A plugin emits only the events it DECLARES, each carrying its declared
// semantics; a terminal event is known by its semantics (closes_target), not
// its name, and fires no trigger of its own. The target claim is the
// plugin's: trust was decided at install, so it is believed — and an event
// that makes no claim stays unassigned.
func TestSourceEventsCarryTheirDeclarations(t *testing.T) {
	nc := abiTrigger(t, 0, "gh.new_comment", "", "", nil)
	rel := abiTrigger(t, 1, "gh.release", "", "", nil)
	closes := &sdk.EventSemantics{ClosesTarget: &sdk.ClosesTargetSemantics{}}
	cursor := &sdk.EventSemantics{Cursor: &sdk.CursorSemantics{ID: "comment_id"}}
	events := []sdk.SourceEvent{
		{Event: "new_comment", Trigger: nc.Ref(), TargetTrusted: true, Target: sdk.Target{Repo: "o/r", Number: 1}},
		{Event: "release", Trigger: rel.Ref(), Target: sdk.Target{Repo: "o/r"}}, // no claim
		{Event: "finished", Target: sdk.Target{Key: "o/r#1", Assigned: true}, Context: map[string]any{"merged": true}},
		{Event: "release", Kind: "failing_checks", Trigger: rel.Ref()}, // a kind it does not declare
		{Event: "teleported"}, // an event it does not declare
	}
	got := runPSI(t, &pluginSourceIntegration{source: &abiSourcer{events: events}, instance: "gh", typ: "github",
		triggers: []CompiledTrigger{nc, rel}, log: t.Logf,
		declared: map[string]bool{"new_comment": true, "release": true, "finished": true},
		sem:      map[string]*sdk.EventSemantics{"new_comment": cursor, "finished": closes}})
	kinds := map[string][]core.Trigger{}
	for _, tr := range got {
		kinds[tr.Kind] = append(kinds[tr.Kind], tr)
	}
	if len(got) != 4 || len(kinds["new_comment"]) != 1 || len(kinds["release"]) != 2 || len(kinds["finished"]) != 1 {
		t.Fatalf("want new_comment, release ×2 (the undeclared kind falls back to its event), finished — got %+v", got)
	}
	if !kinds["new_comment"][0].TargetTrusted || kinds["release"][0].TargetTrusted {
		t.Fatal("the target claim is the plugin's: believed when made, absent when not")
	}
	if kinds["new_comment"][0].Sem != cursor {
		t.Fatal("a trigger must carry its event's declared semantics")
	}
	fin := kinds["finished"][0]
	if !fin.ClosesTarget() || fin.Action != nil || !fin.TargetTrusted || fin.Context["merged"] != true {
		t.Fatalf("a closes_target event is the lifecycle fact: no action, its facts, its claim: %+v", fin)
	}
}

// Every source gets the same contract — there is no older tier driven
// differently. A plugin that implements nothing optional is still sent its
// triggers and still has catch_up honored; it simply cannot be polled or
// forced, and says so.
func TestEverySourceGetsTheSameContract(t *testing.T) {
	tr := abiTrigger(t, 0, "s.alert", "", "", nil)
	src := &plainSourcer{events: []sdk.SourceEvent{{Event: "alert", CatchUp: true}}}
	psi := &pluginSourceIntegration{source: src, instance: "s", typ: "sentry", triggers: []CompiledTrigger{tr}, log: t.Logf}
	got := runPSI(t, psi)
	if len(src.req.Triggers) != 1 || src.req.Triggers[0].ID != tr.Ref() {
		t.Fatalf("every source is sent its triggers: %+v", src.req.Triggers)
	}
	if len(got) != 1 || !got[0].CatchUp {
		t.Fatalf("catch_up is honored for every source: %+v", got)
	}
	if psi.SweepNow() {
		t.Fatal("a source that does not poll cannot be nudged")
	}
	if _, err := psi.Force(context.Background(), "alert", "o/r", 1, func(context.Context, core.Trigger) {}); err == nil {
		t.Fatal("a source that does not poll cannot be forced")
	}
	if err := psi.SweepOnce(context.Background(), func(context.Context, core.Trigger) {}); err == nil {
		t.Fatal("a source that does not poll has no dry run")
	}
}

// poll (now, target, dry run), translate and app_token reach the plugin;
// only events a target poll RETURNS are forced.
func TestSourcePollTranslateAndAppToken(t *testing.T) {
	mc := abiTrigger(t, 0, "gh.merge_conflict", "", "", nil)
	src := &abiSourcer{pollEv: []sdk.SourceEvent{{Event: "merge_conflict", Trigger: mc.Ref(), TargetTrusted: true,
		Target: sdk.Target{Repo: "o/r", Number: 3}}}}
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github",
		triggers: []CompiledTrigger{mc}, declared: map[string]bool{"merge_conflict": true}, log: t.Logf}

	var streamed []core.Trigger
	if err := psi.Start(context.Background(), func(_ context.Context, tr core.Trigger) { streamed = append(streamed, tr) }); err != nil {
		t.Fatal(err)
	}
	if !psi.SweepNow() || len(src.polls) != 1 || src.polls[0].Mode != sdk.PollNow || src.polls[0].Instance != "gh" {
		t.Fatalf("poll now not delivered: %+v", src.polls)
	}
	// Events a poll-now RETURNS are routed through the stream's emit, unforced.
	if len(streamed) != 1 || streamed[0].Force {
		t.Fatalf("poll-now events: %+v", streamed)
	}

	var got []core.Trigger
	n, err := psi.Force(context.Background(), "merge_conflict", "o/r", 3, func(_ context.Context, tr core.Trigger) { got = append(got, tr) })
	if err != nil || n != 1 || len(got) != 1 || !got[0].Force || got[0].Kind != "merge_conflict" {
		t.Fatalf("force: n=%d err=%v got=%+v", n, err, got)
	}
	if p := src.polls[1]; p.Mode != sdk.PollTarget || p.Event != "merge_conflict" || p.Target != "o/r#3" {
		t.Fatalf("target poll request: %+v", p)
	}

	var dry []core.Trigger
	if err := psi.SweepOnce(context.Background(), func(_ context.Context, tr core.Trigger) { dry = append(dry, tr) }); err != nil || len(dry) != 1 || dry[0].Force {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	if src.polls[2].Mode != sdk.PollDryRun {
		t.Fatalf("dry run request: %+v", src.polls[2])
	}

	trs := psi.Translate(context.Background(), "pull_request", []byte(`{}`))
	if tr := src.transl[0]; len(trs) != 1 || tr.Event != "pull_request" || tr.Body != "{}" || len(tr.Triggers) != 1 || tr.Instance != "gh" {
		t.Fatalf("translate: %+v %+v", trs, src.transl)
	}

	if tok, err := psi.AppToken(context.Background(), 42); err != nil || tok != "tok-42" {
		t.Fatalf("app token: %q %v", tok, err)
	}
}

// The dispatch credential policy a github-shaped connection declares is read
// from the daemon's own copy of the config, with the bundled defaults.
func TestIdentitySourceReadsTheConnection(t *testing.T) {
	s := &identitySource{&pluginSourceIntegration{log: t.Logf, config: map[string]any{
		"identity": map[string]any{"write_token": "w-lit"},
		"retry":    map[string]any{"max": 2, "backoff": "5s"},
	}}}
	r, w, c := s.IdentityTokens()
	if r != "app" || w != "w-lit" || c != "self" {
		t.Fatalf("identity: %q %q %q", r, w, c)
	}
	if rp := s.RetryPolicy(); rp.Max != 2 || rp.Backoff.D().Seconds() != 5 {
		t.Fatalf("retry: %+v", rp)
	}
	empty := &identitySource{&pluginSourceIntegration{log: t.Logf, config: map[string]any{}}}
	if r, w, _ := empty.IdentityTokens(); r != "app" || w != "gh_auth" {
		t.Fatalf("defaults: %q %q", r, w)
	}
}

// A plugin declaring a sweep verb has it answered by the daemon — any
// plugin, there is no tier.
func TestSweepVerbIsAnsweredByTheDaemon(t *testing.T) {
	SetSweepHook(func(context.Context) (int, error) { return 4, nil })
	defer SetSweepHook(nil)
	inv := &countInvoker{}
	decl := &TypeDecl{Type: "github", Verbs: []VerbDecl{{Name: "sweep"}}}
	e := &externalImpl{client: inv, decl: decl, log: t.Logf}
	out, err := e.Invoke(context.Background(), "sweep", nil)
	if err != nil || out["nudged"] != 4 || inv.calls != 0 {
		t.Fatalf("sweep: out=%v err=%v forwarded=%d", out, err, inv.calls)
	}
}

type countInvoker struct{ calls int }

func (f *countInvoker) Invoke(context.Context, plugin.InvokeRequest) (map[string]any, error) {
	f.calls++
	return map[string]any{}, nil
}

// A plugin standing in for a bundled type replaces it only while it is
// registered, and the bundled registration comes back.
func TestPluginInPlaceOfBundledIsRestored(t *testing.T) {
	bundled, ok := TypeDeclFor("github")
	if !ok {
		t.Fatal("github is not registered")
	}
	if err := RegisterExternalType(&TypeDecl{Type: "github"}, nil); err == nil {
		t.Fatal("the plain registration must still refuse a bundled type")
	}
	stand := &TypeDecl{Type: "github", Desc: "plugin"}
	if err := RegisterExternalTypeInPlaceOfBundled(stand, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := TypeDeclFor("github"); got != stand || !IsExternalType("github") {
		t.Fatal("the plugin did not take the type")
	}
	if err := RegisterExternalTypeInPlaceOfBundled(&TypeDecl{Type: "github"}, nil); err == nil {
		t.Fatal("a second plugin must still collide")
	}
	UnregisterExternalType("github")
	if got, _ := TypeDeclFor("github"); got != bundled || IsExternalType("github") {
		t.Fatal("the bundled github registration was not restored")
	}
}

// The plugin's own checks run as the source's Validate, naming each problem;
// a plugin with none of its own is valid.
func TestSourceValidateRunsThePluginsChecks(t *testing.T) {
	tr := abiTrigger(t, 0, "gh.release", "", "", nil)
	bad := &pluginSourceIntegration{source: &abiSourcer{}, instance: "gh", config: map[string]any{}, triggers: []CompiledTrigger{tr}, log: t.Logf}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "secret: required") {
		t.Fatalf("plugin's problems not reported: %v", err)
	}
	good := &pluginSourceIntegration{source: &abiSourcer{}, instance: "gh", config: map[string]any{"secret": "s"}, log: t.Logf}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&pluginSourceIntegration{source: &plainSourcer{}, instance: "s"}).Validate(); err != nil {
		t.Fatalf("a plugin without checks is valid: %v", err)
	}
}

// The engine options lower the same way for every source: the attempt
// threshold (either spelling) always, and a remediation's option — whatever
// the event's declaration names it — only on an event that declares one.
func TestEngineOptionsLowerFromTheDeclaration(t *testing.T) {
	opts := map[string]any{"max_attempts_per_revision": 2, "retry_ci": map[string]any{"enabled": true, "max": 4}}
	var act config.Action
	lowerEngineOptions(&act, opts, &sdk.EventSemantics{Remediate: &sdk.RemediateSemantics{Option: "retry_ci", Run: "build"}})
	if act.MaxAttemptsPerHead != 2 || !act.FlakyRerun.Enabled || act.FlakyRerun.Max != 4 {
		t.Fatalf("declared remediation option not lowered: %+v", act)
	}
	var plain config.Action
	lowerEngineOptions(&plain, opts, nil)
	if plain.FlakyRerun.Enabled || plain.MaxAttemptsPerHead != 2 {
		t.Fatalf("an event declaring no remediation got one: %+v", plain)
	}
	// Bundled github: failing_checks declares flaky_rerun.
	if s := githubEventSemantics("failing_checks"); s == nil || s.Remediate == nil || s.Remediate.Option != "flaky_rerun" {
		t.Fatalf("github failing_checks remediation = %+v", s)
	}
}
