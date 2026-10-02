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

// abiSourcer is a ConnectorABI plugin client double: it records the
// start_source request and streams fixed events, and answers the extension
// requests.
type abiSourcer struct {
	mu      sync.Mutex
	req     plugin.StartSourceRequest
	events  []sdk.SourceEvent
	nudged  []string
	forced  []sdk.ForceRequest
	forceEv []sdk.SourceEvent
	heads   []sdk.Target
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
func (a *abiSourcer) Nudge(_ context.Context, instance string) (bool, error) {
	a.nudged = append(a.nudged, instance)
	return true, nil
}
func (a *abiSourcer) Force(_ context.Context, req sdk.ForceRequest) ([]sdk.SourceEvent, error) {
	a.forced = append(a.forced, req)
	return a.forceEv, nil
}
func (a *abiSourcer) AppToken(_ context.Context, _ string, id int64) (string, error) {
	return "tok-" + itoa64(id), nil
}
func (a *abiSourcer) TargetHead(_ context.Context, _ string, t sdk.Target) (sdk.TargetHeadResult, error) {
	a.heads = append(a.heads, t)
	return sdk.TargetHeadResult{SHA: "head1", State: "open"}, nil
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

// A ConnectorABI plugin is handed its triggers — filters in structural form —
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
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github", abi: sdk.ConnectorABI,
		triggers: []CompiledTrigger{a, b}, log: t.Logf}
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
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github", abi: sdk.ConnectorABI,
		triggers: []CompiledTrigger{a}, log: t.Logf}
	if got := runPSI(t, psi); len(got) != 0 {
		t.Fatalf("misrouted events fired: %+v", got)
	}
}

// TRUST. Without trusted_source a plugin's events are untrusted input, as
// they always were: a target-trust claim is ignored and an engine-interpreted
// kind is dropped. With it, both are believed — but only for an event the
// plugin declares (and _closed, from a ConnectorABI source).
func TestABISourceTrustIsTheOperatorsGrant(t *testing.T) {
	nc := abiTrigger(t, 0, "gh.new_comment", "", "", nil)
	rel := abiTrigger(t, 1, "gh.release", "", "", nil)
	events := []sdk.SourceEvent{
		{Event: "new_comment", Trigger: nc.Ref(), TargetTrusted: true, Target: sdk.Target{Repo: "o/r", Number: 1}},
		{Event: "release", Trigger: rel.Ref(), TargetTrusted: true, Target: sdk.Target{Repo: "o/r"}},
		{Event: "_closed", Kind: "_closed", TargetTrusted: true, Target: sdk.Target{Repo: "o/r", Number: 1}, Context: map[string]any{"merged": true}},
		{Event: "release", Kind: "failing_checks", Trigger: rel.Ref()}, // claims a kind it does not declare
	}
	declared := map[string]bool{"new_comment": true, "release": true}

	untrusted := runPSI(t, &pluginSourceIntegration{source: &abiSourcer{events: events}, instance: "gh", typ: "github",
		abi: sdk.ConnectorABI, triggers: []CompiledTrigger{nc, rel}, declared: declared, log: t.Logf})
	// new_comment and _closed are engine-interpreted: dropped. release fires
	// twice (its own event, and the failing_checks claim falling back to it),
	// neither trusted.
	if len(untrusted) != 2 {
		t.Fatalf("untrusted: want the two release events only, got %+v", untrusted)
	}
	for _, tr := range untrusted {
		if tr.Kind != "release" || tr.TargetTrusted {
			t.Fatalf("untrusted source produced %q trusted=%v", tr.Kind, tr.TargetTrusted)
		}
	}

	trusted := runPSI(t, &pluginSourceIntegration{source: &abiSourcer{events: events}, instance: "gh", typ: "github",
		abi: sdk.ConnectorABI, trusted: true, triggers: []CompiledTrigger{nc, rel}, declared: declared, log: t.Logf})
	kinds := map[string]core.Trigger{}
	for _, tr := range trusted {
		kinds[tr.Kind] = tr
	}
	if len(trusted) != 3 || kinds["new_comment"].Kind == "" || kinds["release"].Kind == "" || kinds[core.KindClosed].Kind == "" {
		t.Fatalf("trusted: want new_comment, release, _closed — got %+v", trusted)
	}
	if !kinds["new_comment"].TargetTrusted || !kinds[core.KindClosed].TargetTrusted {
		t.Fatal("trusted source's target claims were not believed")
	}
	if kinds[core.KindClosed].Action != nil || kinds[core.KindClosed].Context["merged"] != true {
		t.Fatalf("_closed must carry its facts and no action: %+v", kinds[core.KindClosed])
	}
	// failing_checks was not declared: even a trusted source may not claim it.
	if _, ok := kinds["failing_checks"]; ok {
		t.Fatal("a trusted source emitted an engine-interpreted kind it does not declare")
	}
}

// A plain (pre-extension) plugin is driven exactly as before: no triggers on
// start_source, routing fields ignored, catch_up and target claims ignored.
func TestLegacySourceIgnoresExtensionFields(t *testing.T) {
	tr := abiTrigger(t, 0, "s.alert", "", "", nil)
	src := &abiSourcer{events: []sdk.SourceEvent{
		{Event: "alert", Trigger: "nonsense", CatchUp: true, TargetTrusted: true},
	}}
	psi := &pluginSourceIntegration{source: src, instance: "s", typ: "sentry", trusted: false,
		triggers: []CompiledTrigger{tr}, log: t.Logf}
	got := runPSI(t, psi)
	if len(src.req.Triggers) != 0 {
		t.Fatal("a plugin that did not ask for triggers was sent them")
	}
	if len(got) != 1 || got[0].CatchUp || got[0].TargetTrusted {
		t.Fatalf("legacy source behaviour changed: %+v", got)
	}
	if psi.SweepNow() {
		t.Fatal("a legacy source has no nudge")
	}
	if _, err := psi.Force(context.Background(), "alert", "o/r", 1, func(context.Context, core.Trigger) {}); err == nil {
		t.Fatal("a legacy source has no force")
	}
}

// nudge / force / app_token reach the plugin; forced events are routed like
// any other and marked Force.
func TestABISourceExtensionCalls(t *testing.T) {
	mc := abiTrigger(t, 0, "gh.merge_conflict", "", "", nil)
	src := &abiSourcer{forceEv: []sdk.SourceEvent{{Event: "merge_conflict", Trigger: mc.Ref(), TargetTrusted: true,
		Target: sdk.Target{Repo: "o/r", Number: 3}}}}
	psi := &pluginSourceIntegration{source: src, instance: "gh", typ: "github", abi: sdk.ConnectorABI, trusted: true,
		triggers: []CompiledTrigger{mc}, declared: map[string]bool{"merge_conflict": true}, log: t.Logf}
	if !psi.SweepNow() || len(src.nudged) != 1 || src.nudged[0] != "gh" {
		t.Fatalf("nudge not delivered: %v", src.nudged)
	}
	var got []core.Trigger
	n, err := psi.Force(context.Background(), "merge_conflict", "o/r", 3, func(_ context.Context, tr core.Trigger) { got = append(got, tr) })
	if err != nil || n != 1 || len(got) != 1 || !got[0].Force || got[0].Kind != "merge_conflict" {
		t.Fatalf("force: n=%d err=%v got=%+v", n, err, got)
	}
	if src.forced[0].Kind != "merge_conflict" || src.forced[0].Repo != "o/r" || src.forced[0].Number != 3 {
		t.Fatalf("force request: %+v", src.forced[0])
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

// Only a ConnectorABI plugin declaring a sweep verb has it answered by the
// daemon; anything else is forwarded as it always was.
func TestSweepVerbIsAnsweredByTheDaemon(t *testing.T) {
	SetSweepHook(func(context.Context) (int, error) { return 4, nil })
	defer SetSweepHook(nil)
	inv := &countInvoker{}
	decl := &TypeDecl{Type: "github", Verbs: []VerbDecl{{Name: "sweep"}}}
	e := &externalImpl{client: inv, decl: decl, abi: sdk.ConnectorABI, log: t.Logf}
	out, err := e.Invoke(context.Background(), "sweep", nil)
	if err != nil || out["nudged"] != 4 || inv.calls != 0 {
		t.Fatalf("ABI sweep: out=%v err=%v forwarded=%d", out, err, inv.calls)
	}
	old := &externalImpl{client: inv, decl: decl, abi: 0, log: t.Logf}
	if _, err := old.Invoke(context.Background(), "sweep", nil); err != nil || inv.calls != 1 {
		t.Fatalf("a pre-extension plugin's sweep verb must be forwarded: err=%v calls=%d", err, inv.calls)
	}
}

type countInvoker struct{ calls int }

func (f *countInvoker) Invoke(context.Context, plugin.InvokeRequest) (map[string]any, error) {
	f.calls++
	return map[string]any{}, nil
}

// A head read goes to the plugin only for a trusted target this instance emitted.
func TestABITargetHeadOnlyForOwnTrustedTargets(t *testing.T) {
	src := &abiSourcer{}
	e := &externalImpl{source: src, instance: "gh", abi: sdk.ConnectorABI, decl: &TypeDecl{}}
	own := core.Trigger{Instance: "gh", TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 5}}
	if h, err := e.TargetHead(context.Background(), own); err != nil || h.SHA != "head1" || h.State != "open" {
		t.Fatalf("own trusted target: %+v %v", h, err)
	}
	for _, tr := range []core.Trigger{
		{Instance: "gh", Target: core.Target{Repo: "o/r", Number: 5}},                         // untrusted
		{Instance: "other", TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 5}}, // not ours
		{Instance: "gh", TargetTrusted: true, Target: core.Target{Repo: "o/r"}},               // no number
	} {
		if h, _ := e.TargetHead(context.Background(), tr); h.SHA != "" {
			t.Fatalf("read a head it should not have: %+v for %+v", h, tr)
		}
	}
	if len(src.heads) != 1 {
		t.Fatalf("plugin asked %d times, want 1", len(src.heads))
	}
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
