package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// The "fake" connector type: a minimal in-memory connector registered once at
// package init so every test in this package can build configs referencing
// `type: fake` connectors without touching any real integration.
//
// Event: ping (filters: only; context: msg). Verbs: post (options: text
// required, channel, as, meta; outputs: id), ask (Ask: true; outputs:
// action/text/ref), fail (always errors), slow (sleeps a configurable
// duration, for timeout tests).
// ---------------------------------------------------------------------------

var fakeDecl = &connector.TypeDecl{
	Type: "fake",
	Desc: "Fake: an in-memory connector type for flow package tests.",
	Events: []connector.EventDecl{
		{
			Name:    "ping",
			Desc:    "a synthetic test event",
			Filters: connector.Schema{"only": {Type: connector.TString}},
			Context: connector.Schema{"msg": {Type: connector.TString}},
		},
	},
	Filter: fakeFilter,
	Verbs: []connector.VerbDecl{
		{
			Name: "post",
			Desc: "records an invocation and returns a canned id",
			Options: connector.Schema{
				"text": {Type: connector.TString, Required: true},
				// Scope-tagged destination options, one per dimension the
				// real connectors use, so this package's tests exercise the
				// generic walk rather than any one connector's spelling.
				"channel": {Type: connector.TString, Scope: "channel"},
				"as":      {Type: connector.TString},
				"meta":    {Type: connector.TMap},
				"repo":    {Type: connector.TString, Scope: "repo"},  // a target selector (like gh verbs)
				"store":   {Type: connector.TString, Scope: "store"}, // a store selector (like kv verbs)
			},
			Outputs: connector.Schema{"id": {Type: connector.TInt}},
		},
		{
			// Carries a Usage hint: the card and the MCP tool description
			// must both prefer it over Desc (design §A).
			Name: "ask", Desc: "a fake ask-capable verb",
			Usage: "ask a human and wait for their answer", Ask: true,
			Options: connector.Schema{"prompt": {Type: connector.TString, Required: true}},
			Outputs: connector.Schema{
				"action": {Type: connector.TString},
				"text":   {Type: connector.TString},
				"ref":    {Type: connector.TString},
			},
		},
		{
			Name:    "fail",
			Desc:    "always errors",
			Options: connector.Schema{},
			Outputs: connector.Schema{},
		},
		{
			Name:    "slow",
			Desc:    "sleeps a test-configured duration before returning",
			Options: connector.Schema{},
			Outputs: connector.Schema{"done": {Type: connector.TBool}},
		},
		{
			Name: "download", Desc: "returns raw bytes as a declared binary output (#36 §21)",
			Options:   connector.Schema{"url": {Type: connector.TString}},
			Outputs:   connector.Schema{"body": {Type: connector.TAny}},
			BinaryOut: []string{"body"},
		},
		{
			Name: "upload", Desc: "accepts a blob handle on a declared binary input (#36 §21)",
			Options:  connector.Schema{"file": {Type: connector.TAny, Required: true}},
			Outputs:  connector.Schema{"ok": {Type: connector.TBool}},
			BinaryIn: []string{"file"},
		},
	},
}

func init() { connector.RegisterType(fakeDecl, newFakeImpl) }

// fakeFilter matches a trigger's `filters: {only: <prefix>}` against the
// event's published `msg` context — only == ctx.msg's prefix.
func fakeFilter(event string, filters, trigCtx map[string]any) (bool, error) {
	only, _ := filters["only"].(string)
	if only == "" {
		return true, nil
	}
	msg, _ := trigCtx["msg"].(string)
	return len(msg) >= len(only) && msg[:len(only)] == only, nil
}

// fakeCall is one recorded Invoke.
type fakeCall struct {
	Verb string
	Opts map[string]any
}

// fakeState is the per-connector-instance-name mutable test control block: it
// records every invocation and lets a test script canned outputs, induced
// failures, and artificial latency. Looked up by connector instance name at
// Invoke time (not cached at construction) so a test can configure it either
// before or after building the registry, as long as it's set before Run.
type fakeState struct {
	mu sync.Mutex

	calls []fakeCall

	// failTimes[verb] > 0 makes the next that-many invocations of verb fail,
	// decrementing on each call.
	failTimes map[string]int
	// failIf[verb], when set, is consulted on every invocation of verb; a
	// true result fails that call (independent of failTimes).
	failIf map[string]func(opts map[string]any) bool
	// outputs[verb] overrides the default canned outputs for verb.
	outputs map[string]map[string]any
	// slowMS[verb] sleeps that long (bounded by ctx) before returning.
	slowMS map[string]time.Duration
}

var (
	fakeStatesMu sync.Mutex
	fakeStates   = map[string]*fakeState{}
)

func newFakeStateEmpty() *fakeState {
	return &fakeState{
		failTimes: map[string]int{},
		failIf:    map[string]func(map[string]any) bool{},
		outputs:   map[string]map[string]any{},
		slowMS:    map[string]time.Duration{},
	}
}

// getOrCreateFakeState returns the state for a connector instance name,
// creating an empty one on first use (default canned-outputs behavior).
func getOrCreateFakeState(name string) *fakeState {
	fakeStatesMu.Lock()
	defer fakeStatesMu.Unlock()
	st, ok := fakeStates[name]
	if !ok {
		st = newFakeStateEmpty()
		fakeStates[name] = st
	}
	return st
}

// newFakeState resets (or creates) the state for name and arranges for it to
// be removed when the test ends — call this AFTER building the registry
// (which may lazily create a default entry) and BEFORE calling Run.
func newFakeState(t *testing.T, name string) *fakeState {
	t.Helper()
	st := newFakeStateEmpty()
	fakeStatesMu.Lock()
	fakeStates[name] = st
	fakeStatesMu.Unlock()
	t.Cleanup(func() {
		fakeStatesMu.Lock()
		delete(fakeStates, name)
		fakeStatesMu.Unlock()
	})
	return st
}

func (s *fakeState) snapshot() []fakeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *fakeState) count(verb string) int {
	n := 0
	for _, c := range s.snapshot() {
		if c.Verb == verb {
			n++
		}
	}
	return n
}

type fakeImpl struct{ name string }

func newFakeImpl(name string, ref config.ConnectorRef, deps connector.Deps) (connector.Impl, error) {
	return &fakeImpl{name: name}, nil
}

func (f *fakeImpl) Validate() error          { return nil }
func (f *fakeImpl) DeclaredEvents() []string { return nil }
func (f *fakeImpl) Source(triggers []connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

func (f *fakeImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	st := getOrCreateFakeState(f.name)
	st.mu.Lock()
	st.calls = append(st.calls, fakeCall{Verb: verb, Opts: opts})
	remaining := st.failTimes[verb]
	if remaining > 0 {
		st.failTimes[verb] = remaining - 1
	}
	pred := st.failIf[verb]
	slow := st.slowMS[verb]
	out, hasOut := st.outputs[verb]
	st.mu.Unlock()

	if verb == "fail" {
		return nil, fmt.Errorf("fake: verb %q always fails", verb)
	}
	if pred != nil && pred(opts) {
		return nil, fmt.Errorf("fake: verb %q failed (failIf)", verb)
	}
	if remaining > 0 {
		return nil, fmt.Errorf("fake: verb %q flaky failure (%d remaining)", verb, remaining)
	}
	if slow > 0 {
		t := time.NewTimer(slow)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if hasOut {
		// Return a fresh copy: a real connector mints a new output map per call,
		// and the runner mutates it in place (binary-out → handle). Sharing the
		// stored instance would let one invocation corrupt the canned output for
		// the next (e.g. a []byte body rewritten to a handle across re-triggers).
		cp := make(map[string]any, len(out))
		for k, v := range out {
			cp[k] = v
		}
		return cp, nil
	}
	switch verb {
	case "post":
		return map[string]any{"id": 1}, nil
	case "ask":
		return map[string]any{"action": "approve", "text": "ok", "ref": "ref-1"}, nil
	case "slow":
		return map[string]any{"done": true}, nil
	}
	return map[string]any{}, nil
}

// ---------------------------------------------------------------------------
// Config / registry construction helpers
// ---------------------------------------------------------------------------

// loadConfig parses a YAML document directly into a config.Config (no file
// I/O, no import/env expansion — just the structural shape flow needs).
// lastConfigYAML is the document loadConfig most recently parsed. mustSpec
// prepends it so a trigger spec written in a test can merge an anchor the
// config document defines — anchors are FILE-local, and a rig that parses
// the two halves separately would otherwise be unable to express what a
// real single-file config can. No test here runs in parallel.
var lastConfigYAML string

// commonAnchors is a fallback anchor preamble for the trigger specs in this
// package. Anchors are FILE-local, and the rig parses a config document and
// a trigger spec through separate helpers — so a spec that merges `<<:
// *fixer` needs the definition in scope. A config document that defines its
// own overrides these, since it is spliced in after.
const commonAnchors = `x-rig:
  critic: &critic { type: agent, name: critic, model: m }
  deployer: &deployer { type: agent, name: deployer, model: x }
  fixer: &fixer { type: agent, name: fixer, model: m }
  opted: &opted { type: agent }
  planner: &planner { type: agent, name: planner, model: x }
  reviewer: &reviewer { type: agent, name: reviewer, model: m }
`

func loadConfig(t *testing.T, y string) *config.Config {
	t.Helper()
	lastConfigYAML = y
	// Resolve anchors the way config.Load does, with the rig preamble in
	// scope so a config document may merge one it did not define itself.
	if flat, err := config.ResolveAliasBytes([]byte(commonAnchors + "\n" + y)); err == nil {
		y = string(flat)
	}
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("yaml unmarshal config: %v\n---\n%s", err, y)
	}
	// Mirror config.Load: multi-source on: lists expand, then `extends:`
	// resolves (including a step reaching a `steps:` template) before
	// anything runs.
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatalf("normalize triggers: %v\n---\n%s", err, y)
	}
	if err := cfg.ResolveExtends(); err != nil {
		t.Fatalf("resolve extends: %v\n---\n%s", err, y)
	}
	return &cfg
}

// buildRegistry builds a connector.Registry from cfg using a stubbed secrets
// resolver (LookupEnv backed by a plain map, no real env/process access).
// loadConfigViaLoader writes the document to disk and loads it through the
// REAL config.Load, so load-time machinery the lightweight helper skips —
// `${settings.X}` substitution above all — is what the test sees.
func loadConfigViaLoader(t *testing.T, y string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "conductor.yaml")
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("config.Load: %v\n---\n%s", err, y)
	}
	return cfg
}

func buildRegistry(t *testing.T, cfg *config.Config) *connector.Registry {
	t.Helper()
	reg, err := connector.Build(cfg, connector.Deps{Secrets: testSecrets(nil), Config: cfg})
	if err != nil {
		t.Fatalf("connector.Build: %v", err)
	}
	return reg
}

// testSecrets returns a secrets.Resolver whose LookupEnv is stubbed against
// env (never touches the real process environment).
func testSecrets(env map[string]string) *secrets.Resolver {
	r := secrets.New()
	r.LookupEnv = func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	return r
}

// mustSpec parses one `triggers:`-list-entry-shaped YAML document into a
// config.TriggerSpec.
func mustSpec(t *testing.T, y string) config.TriggerSpec {
	t.Helper()
	// Parse the spec in the same DOCUMENT as the config that set the scene,
	// so `<<: *fixer` resolves the way it would in a real config file. The
	// spec is a one-entry `triggers:` list inside that document.
	doc := commonAnchors + "\n" + lastConfigYAML + "\ntriggers:\n" + indentYAML("  ", "- "+strings.TrimPrefix(strings.TrimSpace(y), "- "))
	// …and resolve the anchors first, exactly as config.Load does: a custom
	// UnmarshalYAML re-encodes the node it is handed, so an alias pointing
	// outside that node cannot be read directly.
	flat, rerr := config.ResolveAliasBytes([]byte(doc))
	if rerr != nil {
		flat = []byte(doc)
	}
	var whole struct {
		Triggers []config.TriggerSpec `yaml:"triggers"`
	}
	if err := yaml.Unmarshal(flat, &whole); err != nil || len(whole.Triggers) != 1 {
		// Fall back to the spec alone — a test that set no config, or one
		// whose spec is not list-shaped.
		var s config.TriggerSpec
		if err2 := yaml.Unmarshal([]byte(y), &s); err2 != nil {
			t.Fatalf("yaml unmarshal trigger spec: %v\n---\n%s", err2, y)
		}
		return s
	}
	return whole.Triggers[0]
}

// indentYAML re-indents a block, leaving the first line's own "- " marker in
// place so a mapping becomes one sequence entry.
func indentYAML(pad, y string) string {
	lines := strings.Split(y, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if i == 0 {
			lines[i] = pad + l
			continue
		}
		lines[i] = pad + "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// newTrigger builds a minimal core.Trigger for a test: a repo/number target
// plus the given context map.
func newTrigger(kind string, ctx map[string]any) core.Trigger {
	return core.Trigger{
		Source: "fake", Instance: "fake", Kind: kind,
		Target:  core.Target{Repo: "o/r", Number: 7},
		Title:   "test trigger",
		Context: ctx,
	}
}

// ---------------------------------------------------------------------------
// Fake Store
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu      sync.Mutex
	audits  []map[string]any
	runs    map[string]store.WorkflowRun
	putLog  []store.WorkflowRun
	history []store.RunHistory
	delLog  []string
	plans   map[string]store.PlanRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{runs: map[string]store.WorkflowRun{}, plans: map[string]store.PlanRecord{}}
}

func (s *fakeStore) PutPlan(rec store.PlanRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[rec.RunID+"\x00"+rec.StepID] = rec
	return nil
}

func (s *fakeStore) GetPlan(runID, stepID string) (store.PlanRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.plans[runID+"\x00"+stepID]
	return rec, ok
}

func (s *fakeStore) DeletePlan(runID, stepID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, runID+"\x00"+stepID)
	return nil
}

func (s *fakeStore) Audit(e map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, e)
}

func (s *fakeStore) PutHistory(rec store.RunHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, rec)
	return nil
}

// lastHistory returns the most recently persisted history record.
func (s *fakeStore) lastHistory() (store.RunHistory, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) == 0 {
		return store.RunHistory{}, false
	}
	return s.history[len(s.history)-1], true
}

// allHistory returns a snapshot of every persisted history write, in the order
// the store received them.
func (s *fakeStore) allHistory() []store.RunHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.RunHistory, len(s.history))
	copy(out, s.history)
	return out
}

func (s *fakeStore) PutRun(r store.WorkflowRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[r.ID] = r
	s.putLog = append(s.putLog, r)
	return nil
}

func (s *fakeStore) DeleteRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
	s.delLog = append(s.delLog, id)
	return nil
}

func (s *fakeStore) auditsWithEvent(event string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, e := range s.audits {
		if e["event"] == event {
			out = append(out, e)
		}
	}
	return out
}

func (s *fakeStore) lastPut(id string) (store.WorkflowRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last store.WorkflowRun
	found := false
	for _, r := range s.putLog {
		if r.ID == id {
			last = r
			found = true
		}
	}
	return last, found
}

// ---------------------------------------------------------------------------
// Fake Notifier
// ---------------------------------------------------------------------------

type notifyEvent struct {
	Event string
	Msg   string
	T     core.Trigger
}

type fakeNotifier struct {
	mu     sync.Mutex
	events []notifyEvent
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{} }

func (n *fakeNotifier) Emit(ctx context.Context, event string, t core.Trigger, msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, notifyEvent{Event: event, Msg: msg, T: t})
}

func (n *fakeNotifier) snapshot() []notifyEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]notifyEvent, len(n.events))
	copy(out, n.events)
	return out
}

// ---------------------------------------------------------------------------
// Fake AgentServices (Dispatch / Background / Archive)
// ---------------------------------------------------------------------------

type backgroundCall struct {
	StepID  string
	Handoff string
	Profile config.Step
	Ref     dispatch.RunRef
}

type fakeAgents struct {
	mu   sync.Mutex
	reqs []dispatch.Request

	// dispatchFunc, when set, overrides the default canned dispatch response.
	dispatchFunc func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error)

	backgroundCalls []backgroundCall
	archived        []string
}

func newFakeAgents() *fakeAgents { return &fakeAgents{} }

func (a *fakeAgents) dispatch(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, req)
	fn := a.dispatchFunc
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return dispatch.RunRef{Output: "{}"}, nil
}

func (a *fakeAgents) background(ctx context.Context, t core.Trigger, stepID, agentName string, p config.Step, ref dispatch.RunRef, handoffConn string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.backgroundCalls = append(a.backgroundCalls, backgroundCall{StepID: stepID, Handoff: handoffConn, Profile: p, Ref: ref})
}

func (a *fakeAgents) archive(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.archived = append(a.archived, id)
}

func (a *fakeAgents) requests() []dispatch.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]dispatch.Request, len(a.reqs))
	copy(out, a.reqs)
	return out
}

// ---------------------------------------------------------------------------
// Runner construction
// ---------------------------------------------------------------------------

type testRig struct {
	Runner   *Runner
	Store    *fakeStore
	Notifier *fakeNotifier
	Agents   *fakeAgents
}

// newTestRunner builds a Runner wired to fresh fakes for one test.
func newTestRunner(t *testing.T, cfg *config.Config, reg *connector.Registry) *testRig {
	t.Helper()
	st := newFakeStore()
	notif := newFakeNotifier()
	ag := newFakeAgents()
	r := New(Runner{
		Cfg:   cfg,
		Conns: reg,
		Agents: AgentServices{
			Dispatch:   ag.dispatch,
			Tokens:     func(t core.Trigger) dispatch.Tokens { return dispatch.Tokens{} },
			Guidance:   func(agentName string, p config.Step, pol config.Policy) string { return "|G|" },
			Background: ag.background,
			Archive:    ag.archive,
		},
		Secrets:    secrets.New(),
		SecretVals: map[string]string{"tok": "s3kr1t-value"},
		Store:      st,
		Notif:      notif,
	})
	return &testRig{Runner: r, Store: st, Notifier: notif, Agents: ag}
}

// fastSleep makes retry backoff/interval waits instant (still ctx-aware).
func fastSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Run helpers. Runner.Run has no return value — its outcome is observed
// through the fake Store's audit log ("workflow_failed" on error) and the
// fake Notifier's emitted events ("complete" / "escalate").
// ---------------------------------------------------------------------------

func emptyRun() store.WorkflowRun {
	return store.WorkflowRun{Outputs: map[string]map[string]any{}}
}

// The rig resolves the trigger's index the way production does rather
// than assuming 0 — that assumption WAS the bug (C1), so a test rig that
// hardcodes it cannot catch a regression.

// runTrigger runs one trigger (no batch, no checkpoint) to completion.
func runTrigger(rig *testRig, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(context.Background(), emptyRun(), trig, spec, rig.Runner.IndexOf(spec), nil, false)
}

// runTriggerCtx is runTrigger with an explicit context (timeout tests).
func runTriggerCtx(ctx context.Context, rig *testRig, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(ctx, emptyRun(), trig, spec, rig.Runner.IndexOf(spec), nil, false)
}

// runTriggerBatch runs one trigger with a grouped batch.
func runTriggerBatch(rig *testRig, trig core.Trigger, spec config.TriggerSpec, batch *Batch) {
	rig.Runner.Run(context.Background(), emptyRun(), trig, spec, rig.Runner.IndexOf(spec), batch, false)
}

// runTriggerWithRun runs a (possibly checkpointed) WorkflowRun.
func runTriggerWithRun(rig *testRig, run store.WorkflowRun, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(context.Background(), run, trig, spec, rig.Runner.IndexOf(spec), nil, false)
}

// workflowFailed reports whether the run just executed logged a
// "workflow_failed" audit event, and its error string if so.
func (rig *testRig) workflowFailed() (bool, string) {
	entries := rig.Store.auditsWithEvent("workflow_failed")
	if len(entries) == 0 {
		return false, ""
	}
	last := entries[len(entries)-1]
	errStr, _ := last["error"].(string)
	return true, errStr
}
