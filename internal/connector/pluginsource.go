package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// pluginsource.go wires a SOURCE plugin (one that streams events, #59) into the
// engine. The plugin owns the transport (listener/poll/HMAC) and emits raw
// events; the DAEMON owns action resolution — it lowers each matched trigger to
// a config.Action. This keeps Trigger.Action (a Go value the engine asserts) on
// the daemon side, out of the wire.
//
// Every source gets the same contract (docs/design/plugin-contract.md): it is
// handed its instance config AND its triggers. A plugin may route each event
// to the one trigger it evaluated it for (its own match keys), or name none
// and let the daemon match it against every trigger on `on:` with the
// generic evaluator. It may implement plugin.poll (catch-up now, one forced
// target, a dry run) and plugin.translate (replay, once).

// pluginSourcer is the streaming capability of *plugin.Client the source
// integration needs (Invoke lives on the verb path).
type pluginSourcer interface {
	StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error
}

// pluginStreamer is StartSource supervised across plugin restarts
// (*plugin.Client.StreamSource). A sourcer without it is started once.
type pluginStreamer interface {
	StreamSource(ctx context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error
}

// pluginSourceExt is the optional source surface of *plugin.Client.
type pluginSourceExt interface {
	Poll(ctx context.Context, req sdk.PollRequest) ([]sdk.SourceEvent, error)
	Translate(ctx context.Context, req sdk.TranslateRequest) ([]sdk.SourceEvent, error)
	Validate(ctx context.Context, req sdk.ValidateRequest) ([]sdk.Problem, error)
	AppToken(ctx context.Context, instance string, installationID int64) (string, error)
}

// pluginEvent is the wire shape a source plugin emits (one plugin.event) —
// the SDK's own type, so the schema has one definition.
type pluginEvent = sdk.SourceEvent

// pluginSourceIntegration adapts a source plugin to core.Integration so it wires
// into the engine exactly like a bundled source (main.go: ig.Start(ctx, eng.Emit)).
type pluginSourceIntegration struct {
	source   pluginSourcer
	instance string // connector instance name (matches the `on:` prefix)
	typ      string // connector type, for Trigger.Source
	config   map[string]any
	triggers []CompiledTrigger
	log      func(string, ...any)

	// declared are the event names the plugin declares: the only events it
	// may emit.
	declared map[string]bool
	// sem are the declared events' semantics, attached to every trigger the
	// event fires (the engine reads them, never the event's name).
	sem map[string]*sdk.EventSemantics
	// dynamic is the plugin's dynamic event (names from its config, e.g. a
	// cron schedule): any name is one of its events, with its semantics.
	dynamic *EventDecl

	// listeners are the connection-level `listeners` semantic this
	// connector's type declares (listeners.go): for each, the config's
	// listen/expose/url_to field paths. nil for a type that declares none.
	listeners []sdk.Listener
	// lookup resolves another configured connector instance by name, for
	// opening a declared listener's named exposure. nil when none of
	// listeners names an expose field (never used in that case).
	lookup func(string) (*Instance, bool)

	mu   sync.Mutex
	emit core.EmitFunc // the running stream's emit, for events a poll returns
	ctx  context.Context
}

func (p *pluginSourceIntegration) Name() string { return p.instance }

// Validate runs the plugin's own config and trigger checks (plugin.validate)
// — at boot and under `conductor validate`, which both build the stack. A
// plugin with no checks of its own (method-not-found) is valid.
func (p *pluginSourceIntegration) Validate() error {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return nil
	}
	ctx, cancel := extReq()
	defer cancel()
	problems, err := ext.Validate(ctx, sdk.ValidateRequest{Instance: p.instance, Config: p.config, Triggers: p.wireTriggers()})
	if err == plugin.ErrNotSupported {
		return nil
	}
	if err != nil {
		return fmt.Errorf("connector %q: validate: %w", p.instance, err)
	}
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, 0, len(problems))
	for _, pr := range problems {
		if pr.Path != "" {
			lines = append(lines, pr.Path+": "+pr.Message)
		} else {
			lines = append(lines, pr.Message)
		}
	}
	return fmt.Errorf("connector %q:\n  %s", p.instance, strings.Join(lines, "\n  "))
}

// Start opens the plugin's event stream and, for each event, emits a Trigger for
// every configured trigger whose `on:` and filters match (or, for a routed
// event, the one trigger it names). Runs until ctx is cancelled (which tears
// down the plugin subprocess and ends the stream).
func (p *pluginSourceIntegration) Start(ctx context.Context, emit core.EmitFunc) error {
	cfg, release, err := p.openListeners(ctx)
	if err != nil {
		return err
	}
	defer release()
	req := plugin.StartSourceRequest{Instance: p.instance, Config: cfg, Triggers: p.wireTriggers()}
	p.mu.Lock()
	p.emit, p.ctx = emit, ctx
	p.mu.Unlock()
	sink := func(raw json.RawMessage) {
		var ev pluginEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			p.log("plugin source %s: dropping malformed event: %v", p.instance, err)
			return
		}
		for _, t := range p.triggersFor(ev, false) {
			emit(ctx, t)
		}
	}
	if st, ok := p.source.(pluginStreamer); ok {
		return st.StreamSource(ctx, req, sink)
	}
	return p.source.StartSource(ctx, req, sink)
}

// wireTriggers is the instance's triggers as a plugin receives them: identity, event, options, and the filter in its structural form. What
// a trigger DOES stays here.
func (p *pluginSourceIntegration) wireTriggers() []sdk.SourceTrigger {
	out := make([]sdk.SourceTrigger, 0, len(p.triggers))
	for _, t := range p.triggers {
		st := sdk.SourceTrigger{
			ID: t.Ref(), Name: t.Spec.Name, Event: t.Spec.Event(),
			Enabled: t.Spec.Enabled, Options: t.Spec.Options,
		}
		if t.Spec.Filter != nil {
			if b, err := json.Marshal(t.Spec.Filter.Kit()); err == nil {
				st.Filter = b
			} else {
				p.log("plugin source %s: trigger %s: filter not sendable (%v) — the trigger will not fire", p.instance, t.Ref(), err)
				st.Enabled = new(bool)
			}
		}
		out = append(out, st)
	}
	return out
}

// triggersFor turns one wire event into the Triggers it fires.
func (p *pluginSourceIntegration) triggersFor(ev pluginEvent, force bool) []core.Trigger {
	if ev.Instance != "" && ev.Instance != p.instance {
		return nil // another instance's event on a shared plugin process
	}
	kind, ok := p.kindFor(ev)
	if !ok {
		return nil
	}
	// The plugin's claim that the platform assigned this target — on the
	// wire, or by the event's declaration (target.assigned: a bool, or the
	// name of a bool fact). Trust in a plugin was decided once, at install
	// (plugin_trust); after that its word is taken like any installed
	// plugin's.
	facts := core.Trigger{Context: ev.Context, Target: coreTarget(ev.Target)}.Facts()
	assigned := ev.Target.Assigned || ev.TargetTrusted || declaredAssigned(p.semFor(kind), facts)
	if r := p.semFor(kind); r != nil && r.ConversationReply != nil && assigned {
		// A reply in a conversation the plugin opened: delivered to the
		// engine's inbox first; one nobody is waiting on is an ordinary event.
		// Only a delivery the platform vouches for may answer an ask — the
		// same assigned bit every other trust decision reads.
		cr := r.ConversationReply
		a, _ := core.LookupFact(facts, cr.Author)
		tx, _ := core.LookupFact(facts, cr.Text)
		author, _ := a.(string)
		text, _ := tx.(string)
		if handoff.Conversations.DeliverReply(p.instance, core.RenderFacts(cr.ID, facts), author, text) {
			return nil
		}
	}
	sem := p.semFor(kind)
	if sem != nil && sem.ClosesTarget != nil && ev.Trigger == "" {
		// A terminal event: the lifecycle fact about a target. It fires no
		// trigger of its own (the engine settles the target on it) — a
		// trigger explicitly `on:` it may still take it, by routing.
		return []core.Trigger{{
			Source: p.typ, Instance: p.instance, Kind: kind,
			Target: coreTarget(ev.Target), Title: ev.Title, Context: ev.Context,
			TargetTrusted: assigned, Sem: sem,
		}}
	}
	if ev.Trigger != "" {
		t, ok := p.routedTrigger(ev)
		if !ok {
			return nil
		}
		return []core.Trigger{p.trigger(t, kind, ev, assigned, force)}
	}
	var out []core.Trigger
	on := p.instance + "." + ev.Event
	for _, t := range p.triggers {
		if t.Spec.On != on {
			continue
		}
		// A plain plugin source's `filter:` runs here rather than in the flow
		// runner: the runner's pass is keyed on the connector TYPE's
		// declaration, and a plugin's event keys come from its own manifest.
		// filterMatch is the match-key body either way, so the two paths agree
		// on what a key means.
		if keep, err := t.Spec.Filter.Eval(ev.Context, pluginFilterMatch); err != nil {
			p.log("plugin source %s: trigger %q: filter not evaluated (%v) — not firing", p.instance, t.Spec.Name, err)
			continue
		} else if !keep {
			continue
		}
		out = append(out, p.trigger(t, kind, ev, assigned, force))
	}
	return out
}

// kindFor decides the kind an event is emitted as, or refuses it: a plugin
// may emit only events it DECLARES (when it declares any — a plugin that
// declares none is a plain source whose events are taken by name). There are
// no reserved names: what the engine does with an event comes from its
// declared semantics, never its name.
func (p *pluginSourceIntegration) kindFor(ev pluginEvent) (string, bool) {
	kind := ev.Kind
	if kind == "" {
		kind = ev.Event
	}
	if len(p.declared) == 0 || p.declared[kind] {
		return kind, true
	}
	if p.dynamic != nil && (ev.Kind == "" || ev.Kind == ev.Event) {
		return kind, true // a config-named event; the plugin validated its names
	}
	if ev.Kind != "" && ev.Kind != ev.Event && p.declared[ev.Event] {
		p.log("plugin source %s: event kind %q is not one the plugin declares; using its declared event %q", p.instance, ev.Kind, ev.Event)
		return ev.Event, true
	}
	p.log("plugin source %s: dropping event %q — not an event the plugin declares", p.instance, kind)
	return "", false
}

// routedTrigger finds the one trigger a routed event names. It must be a
// trigger of THIS instance, on the event the plugin says it is: a plugin
// cannot fire a trigger written for a different event by naming its id.
func (p *pluginSourceIntegration) routedTrigger(ev pluginEvent) (CompiledTrigger, bool) {
	for _, t := range p.triggers {
		if t.Ref() != ev.Trigger {
			continue
		}
		if t.Spec.On != p.instance+"."+ev.Event {
			p.log("plugin source %s: event %q routed to trigger %s, which is on %s — dropped", p.instance, ev.Event, ev.Trigger, t.Spec.On)
			return CompiledTrigger{}, false
		}
		return t, true
	}
	p.log("plugin source %s: event %q routed to unknown trigger %q — dropped", p.instance, ev.Event, ev.Trigger)
	return CompiledTrigger{}, false
}

// trigger builds the core.Trigger one matched trigger fires.
func (p *pluginSourceIntegration) trigger(t CompiledTrigger, kind string, ev pluginEvent, assigned, force bool) core.Trigger {
	return core.Trigger{
		// TargetTrusted is the plugin's claim that the platform assigned the
		// target (a signature-verified delivery, a read with its own
		// credentials) rather than the sender choosing it.
		TargetTrusted: assigned,
		Source:        p.typ,
		Instance:      p.instance,
		Kind:          kind,
		Variant:       t.Spec.Name,
		Target:        coreTarget(ev.Target),
		Title:         ev.Title,
		Context:       ev.Context,
		Dedup:         ev.Dedup,
		Labels:        ev.Labels,
		CatchUp:       ev.CatchUp,
		Force:         force,
		Action:        pluginAction(t, p.semFor(kind)),
		Sem:           p.semFor(kind),
	}
}

// pluginAction is the config.Action a plugin-sourced trigger runs: the
// generic lowering plus the trigger options the ENGINE interprets, which mean
// the same thing whichever connector the trigger is on.
func pluginAction(t CompiledTrigger, sem *sdk.EventSemantics) config.Action {
	act := lowerAction(t)
	lowerEngineOptions(&act, t.Spec.Options, sem)
	return act
}

// extReq is a bounded context for one source-extension call.
func extReq() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// SweepNow asks the source to poll now (SIGUSR1, `conductor sweep --now`,
// the sweep verb). false when the plugin does not poll.
func (p *pluginSourceIntegration) SweepNow() bool {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return false
	}
	ctx, cancel := extReq()
	defer cancel()
	evs, err := ext.Poll(ctx, sdk.PollRequest{Instance: p.instance, Mode: sdk.PollNow})
	if err == plugin.ErrNotSupported {
		return false
	}
	if err != nil {
		p.log("plugin source %s: poll: %v", p.instance, err)
		return false
	}
	p.routeReturned(evs)
	return true
}

// routeReturned routes events a poll RETURNED (rather than streamed) into
// the engine, through the same emit the stream uses.
func (p *pluginSourceIntegration) routeReturned(evs []sdk.SourceEvent) {
	p.mu.Lock()
	emit, ctx := p.emit, p.ctx
	p.mu.Unlock()
	if emit == nil {
		return
	}
	for _, ev := range evs {
		for _, t := range p.triggersFor(ev, false) {
			emit(ctx, t)
		}
	}
}

// SweepOnce is the dry-run preview (`conductor sweep`): the events a poll
// would emit, handed to emit, with nothing dispatched by the plugin itself.
func (p *pluginSourceIntegration) SweepOnce(ctx context.Context, emit core.EmitFunc) error {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return fmt.Errorf("plugin source %s does not poll", p.instance)
	}
	evs, err := ext.Poll(ctx, sdk.PollRequest{Instance: p.instance, Mode: sdk.PollDryRun})
	if err == plugin.ErrNotSupported {
		return fmt.Errorf("plugin source %s does not poll", p.instance)
	}
	if err != nil {
		return err
	}
	for _, ev := range evs {
		for _, t := range p.triggersFor(ev, false) {
			emit(ctx, t)
		}
	}
	return nil
}

// Force implements core.Forcer: the plugin returns one target's events for
// kind (plugin.poll, mode target), and the daemon routes them and marks them
// forced. Only RETURNED events are ever forced.
func (p *pluginSourceIntegration) Force(ctx context.Context, kind, repo string, number int, emit core.EmitFunc) (int, error) {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return 0, fmt.Errorf("plugin source %s does not support force", p.instance)
	}
	target := repo
	if number != 0 {
		target = fmt.Sprintf("%s#%d", repo, number)
	}
	evs, err := ext.Poll(ctx, sdk.PollRequest{Instance: p.instance, Mode: sdk.PollTarget, Event: kind, Target: target})
	if err == plugin.ErrNotSupported {
		return 0, fmt.Errorf("plugin source %s does not support force", p.instance)
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range evs {
		for _, t := range p.triggersFor(ev, true) {
			emit(ctx, t)
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no %q trigger fired for %s", kind, target)
	}
	return n, nil
}

// Translate decodes one delivery into the triggers it fires (replay, once),
// through the plugin's plugin.translate. A plugin that does not translate
// yields none.
func (p *pluginSourceIntegration) Translate(ctx context.Context, event string, body []byte) []core.Trigger {
	trs, err := p.TranslateDelivery(ctx, event, nil, body)
	if err != nil {
		p.log("plugin source %s: translate: %v", p.instance, err)
	}
	return trs
}

// TranslateDelivery is Translate with the delivery's headers.
func (p *pluginSourceIntegration) TranslateDelivery(ctx context.Context, event string, headers map[string]string, body []byte) ([]core.Trigger, error) {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return nil, fmt.Errorf("plugin source %s does not translate deliveries", p.instance)
	}
	evs, err := ext.Translate(ctx, sdk.TranslateRequest{Instance: p.instance, Config: p.config, Triggers: p.wireTriggers(),
		Event: event, Headers: headers, Body: string(body)})
	if err == plugin.ErrNotSupported {
		return nil, fmt.Errorf("plugin source %s does not translate deliveries", p.instance)
	}
	if err != nil {
		return nil, err
	}
	var out []core.Trigger
	for _, ev := range evs {
		out = append(out, p.triggersFor(ev, false)...)
	}
	return out, nil
}

// AppToken implements the engine's resume-time App token re-mint for a
// source.
func (p *pluginSourceIntegration) AppToken(ctx context.Context, instID int64) (string, error) {
	ext, ok := p.source.(pluginSourceExt)
	if !ok {
		return "", fmt.Errorf("plugin source %s cannot mint app tokens", p.instance)
	}
	return ext.AppToken(ctx, p.instance, instID)
}

// pluginFilterMatch evaluates one match key of a plugin source's `filter:`.
// The grammar handles the boolean structure; this is just filterMatch's
// single-key case, so an AND of keys means exactly what the whole map used to.
func pluginFilterMatch(key string, val any, ctx map[string]any) (bool, error) {
	return filterMatch(map[string]any{key: val}, ctx), nil
}

// filterMatch is the generic, connector-agnostic filter evaluator for plugin
// sources: every filter key must match the same key in the event context, where
// a filter LIST matches if the context value is one of its entries (case-
// insensitive) and a filter SCALAR matches on equality. An absent context key
// fails the filter. This covers the common source-filter shapes (sentry
// projects/levels/environments, pagerduty urgency/service). Connector-specific
// filter semantics beyond this belong in the plugin (which owns the payload).
func filterMatch(filters map[string]any, ctx map[string]any) bool {
	for k, want := range filters {
		got, ok := ctx[k]
		if !ok {
			return false
		}
		if !valueMatches(want, got) {
			return false
		}
	}
	return true
}

func valueMatches(want, got any) bool {
	gs := fmt.Sprintf("%v", got)
	switch w := want.(type) {
	case []any:
		for _, e := range w {
			if eqFold(fmt.Sprintf("%v", e), gs) {
				return true
			}
		}
		return false
	case []string:
		for _, e := range w {
			if eqFold(e, gs) {
				return true
			}
		}
		return false
	default:
		return eqFold(fmt.Sprintf("%v", want), gs)
	}
}

func eqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// coreTarget is the engine's target for a wire target (the legacy fields;
// the generic key/assigned form is read by the source adapter).
func coreTarget(t sdk.Target) core.Target {
	return core.Target{Repo: t.Repo, Owner: t.Owner, Name: t.Name, PR: t.PR, Issue: t.Issue, Number: t.Number,
		HeadSHA: t.HeadSHA, BaseRef: t.BaseRef, HTMLURL: t.HTMLURL, Project: t.Project}
}

// wireTarget is coreTarget's inverse.
func wireTarget(t core.Target) sdk.Target {
	return sdk.Target{Repo: t.Repo, Owner: t.Owner, Name: t.Name, PR: t.PR, Issue: t.Issue, Number: t.Number,
		HeadSHA: t.HeadSHA, BaseRef: t.BaseRef, HTMLURL: t.HTMLURL, Project: t.Project}
}

// declaredAssigned reads an event's declared target.assigned: true, or the
// name of a fact that is true.
func declaredAssigned(sem *sdk.EventSemantics, facts map[string]any) bool {
	if sem == nil || sem.Target == nil || len(sem.Target.Assigned) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(sem.Target.Assigned, &b) == nil {
		return b
	}
	var name string
	if json.Unmarshal(sem.Target.Assigned, &name) != nil || name == "" {
		return false
	}
	v, _ := core.LookupFact(facts, name)
	t, _ := v.(bool)
	return t
}

// semFor is the declared semantics of event kind: its own declaration, else
// the dynamic event's for a config-named one.
func (p *pluginSourceIntegration) semFor(kind string) *sdk.EventSemantics {
	if s, ok := p.sem[kind]; ok {
		return s
	}
	if p.dynamic != nil {
		return p.dynamic.Semantics
	}
	return nil
}
