package flow

import (
	"context"
	"fmt"
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
				"text":    {Type: connector.TString, Required: true},
				"channel": {Type: connector.TString},
				"as":      {Type: connector.TString},
				"meta":    {Type: connector.TMap},
			},
			Outputs: connector.Schema{"id": {Type: connector.TInt}},
		},
		{
			Name: "ask", Desc: "a fake ask-capable verb", Ask: true,
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
		return out, nil
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
func loadConfig(t *testing.T, y string) *config.Config {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("yaml unmarshal config: %v\n---\n%s", err, y)
	}
	return &cfg
}

// buildRegistry builds a connector.Registry from cfg using a stubbed secrets
// resolver (LookupEnv backed by a plain map, no real env/process access).
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
	var s config.TriggerSpec
	if err := yaml.Unmarshal([]byte(y), &s); err != nil {
		t.Fatalf("yaml unmarshal trigger spec: %v\n---\n%s", err, y)
	}
	return s
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
	mu     sync.Mutex
	audits []map[string]any
	runs   map[string]store.WorkflowRun
	putLog []store.WorkflowRun
	delLog []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{runs: map[string]store.WorkflowRun{}}
}

func (s *fakeStore) Audit(e map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, e)
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
	Profile config.AgentProfile
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

func (a *fakeAgents) background(ctx context.Context, t core.Trigger, stepID string, p config.AgentProfile, ref dispatch.RunRef, handoffConn string) {
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
			Guidance:   func(p config.AgentProfile) string { return "|G|" },
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

// runTrigger runs one trigger (no batch, no checkpoint) to completion.
func runTrigger(rig *testRig, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(context.Background(), emptyRun(), trig, spec, nil, false)
}

// runTriggerCtx is runTrigger with an explicit context (timeout tests).
func runTriggerCtx(ctx context.Context, rig *testRig, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(ctx, emptyRun(), trig, spec, nil, false)
}

// runTriggerBatch runs one trigger with a grouped batch.
func runTriggerBatch(rig *testRig, trig core.Trigger, spec config.TriggerSpec, batch *Batch) {
	rig.Runner.Run(context.Background(), emptyRun(), trig, spec, batch, false)
}

// runTriggerWithRun runs a (possibly checkpointed) WorkflowRun.
func runTriggerWithRun(rig *testRig, run store.WorkflowRun, trig core.Trigger, spec config.TriggerSpec) {
	rig.Runner.Run(context.Background(), run, trig, spec, nil, false)
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
