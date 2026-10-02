package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// pluginsource.go wires a SOURCE plugin (one that streams events, #59) into the
// engine. The plugin owns the transport (listener/poll/HMAC) and emits raw
// events; the DAEMON owns action resolution — it lowers each matched trigger to
// a config.Action. This keeps Trigger.Action (a Go value the engine asserts) on
// the daemon side, out of the wire.
//
// Two shapes of plugin arrive here:
//
//   - a plain source (every plugin before the source extension): it is handed
//     its instance config, and the daemon matches each event against the
//     triggers on `on:` with the generic evaluator. It is UNTRUSTED input.
//   - a ConnectorABI source (sdk.ConnectorABI, pkg/plugin/source.go): it is
//     ALSO handed the instance's triggers, evaluates them itself — its own
//     match keys, identity gates, sweep — and routes each event to exactly the
//     trigger it fired for. It can be nudged, forced, asked for an App token
//     and a target's head. It is still untrusted unless the operator marked
//     the connector trusted_source (see trustedKind and the design doc).

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

// pluginSourceExt is the ConnectorABI request surface of *plugin.Client.
type pluginSourceExt interface {
	Nudge(ctx context.Context, instance string) (bool, error)
	Force(ctx context.Context, req sdk.ForceRequest) ([]sdk.SourceEvent, error)
	AppToken(ctx context.Context, instance string, installationID int64) (string, error)
	TargetHead(ctx context.Context, instance string, t sdk.Target) (sdk.TargetHeadResult, error)
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

	// abi is the plugin's connector ABI (sdk.ConnectorABI and up speaks the
	// source extension); 0 for every plugin before it.
	abi int
	// trusted is the operator's trusted_source grant for this instance.
	trusted bool
	// declared are the event names the plugin declares: the only
	// engine-interpreted kinds a trusted source may emit.
	declared map[string]bool

	hintOnce sync.Once
}

// sourceABI reports whether this plugin speaks the source extension.
func (p *pluginSourceIntegration) sourceABI() bool { return p.abi >= sdk.ConnectorABI }

func (p *pluginSourceIntegration) Name() string    { return p.instance }
func (p *pluginSourceIntegration) Validate() error { return nil }

// Start opens the plugin's event stream and, for each event, emits a Trigger for
// every configured trigger whose `on:` and filters match (or, for a routed
// event, the one trigger it names). Runs until ctx is cancelled (which tears
// down the plugin subprocess and ends the stream).
func (p *pluginSourceIntegration) Start(ctx context.Context, emit core.EmitFunc) error {
	req := plugin.StartSourceRequest{Instance: p.instance, Config: p.config}
	if p.sourceABI() {
		req.Triggers = p.wireTriggers()
	}
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

// wireTriggers is the instance's triggers as a ConnectorABI plugin receives
// them: identity, event, options, and the filter in its structural form. What
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
	trusted := p.trusted && ev.TargetTrusted
	if kind == core.KindClosed {
		// The lifecycle fact about a target, not an event any trigger is on:
		// emitted once, with no action, exactly as the bundled github source
		// does. Only a trusted source reaches here (kindFor).
		return []core.Trigger{{
			Source: p.typ, Instance: p.instance, Kind: kind,
			Target: coreTarget(ev.Target), Title: ev.Title, Context: ev.Context,
			TargetTrusted: trusted,
		}}
	}
	if p.sourceABI() && ev.Trigger != "" {
		t, ok := p.routedTrigger(ev)
		if !ok {
			return nil
		}
		return []core.Trigger{p.trigger(t, kind, ev, trusted, force)}
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
		out = append(out, p.trigger(t, kind, ev, trusted, force))
	}
	return out
}

// kindFor decides the kind an event is emitted as, or refuses it.
//
// A plugin may name its own event's KIND, and nothing else. A source plugin
// emitting `_closed` or `failing_checks` claims a fact the ENGINE acts on —
// consuming a target's engagements, settling its outcome, re-running its CI
// with the operator's token (round-13). Those come from sources that read a
// verified platform payload. A plugin is believed to be one only when the
// operator said so (trusted_source), and even then only for an event it
// DECLARES — or `_closed` from a ConnectorABI source, which is the lifecycle
// fact every such engine-interpreted kind is about.
func (p *pluginSourceIntegration) kindFor(ev pluginEvent) (string, bool) {
	kind := ev.Kind
	if kind == "" {
		kind = ev.Event
	}
	if !core.ReservedKind(kind) {
		return kind, true
	}
	if p.trusted && (p.declared[kind] || (kind == core.KindClosed && p.sourceABI())) {
		return kind, true
	}
	if !p.trusted {
		// The untrusted rule, as it always was: a reserved kind falls back to
		// the declared event's own name, and a declared name that is itself
		// reserved is dropped.
		if ev.Kind != "" && ev.Kind != ev.Event {
			p.log("plugin source %s: refusing event kind %q — a plugin may not emit a kind the engine interprets; using its declared event %q",
				p.instance, kind, ev.Event)
		}
		kind = ev.Event
		if !core.ReservedKind(kind) {
			return kind, true
		}
		p.hintOnce.Do(func() {
			p.log("plugin source %s: dropping %q — its kind is one the engine interprets, and this connector is not marked trusted_source "+
				"(set trusted_source: true on it if you vouch that its events come from verified platform deliveries; see docs/design/plugin-source-abi.md)",
				p.instance, kind)
		})
		return "", false
	}
	p.log("plugin source %s: dropping event kind %q — reserved for the engine and not an event this plugin declares", p.instance, kind)
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
func (p *pluginSourceIntegration) trigger(t CompiledTrigger, kind string, ev pluginEvent, trusted, force bool) core.Trigger {
	return core.Trigger{
		// TargetTrusted is the plugin's CLAIM, believed only under the
		// operator's trusted_source grant (round-8 #3). The target arrives on
		// the wire from a third-party plugin, which built it from whatever
		// payload it was handed — the same provenance as a webhook body. A
		// plugin-sourced dispatch the operator has not vouched for gets no
		// implicit own-repo trust; an operator scoping one lists the repos.
		TargetTrusted: trusted,
		Source:        p.typ,
		Instance:      p.instance,
		Kind:          kind,
		Variant:       t.Spec.Name,
		Target:        coreTarget(ev.Target),
		Title:         ev.Title,
		Context:       ev.Context,
		Dedup:         ev.Dedup,
		Labels:        ev.Labels,
		CatchUp:       ev.CatchUp && p.sourceABI(),
		Force:         force,
		Action:        pluginAction(t),
	}
}

// pluginAction is the config.Action a plugin-sourced trigger runs: the
// generic lowering plus the trigger options the ENGINE interprets, which mean
// the same thing whichever connector the trigger is on.
func pluginAction(t CompiledTrigger) config.Action {
	act := lowerAction(t)
	lowerEngineOptions(&act, t.Spec.Options)
	return act
}

// extReq is a bounded context for one source-extension call.
func extReq() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// SweepNow nudges a ConnectorABI source's catch-up sweep (SIGUSR1,
// `conductor sweep --now`, the sweep verb). false when the plugin has no
// sweep or does not speak the extension.
func (p *pluginSourceIntegration) SweepNow() bool {
	ext, ok := p.source.(pluginSourceExt)
	if !ok || !p.sourceABI() {
		return false
	}
	ctx, cancel := extReq()
	defer cancel()
	nudged, err := ext.Nudge(ctx, p.instance)
	if err != nil && err != plugin.ErrNotSupported {
		p.log("plugin source %s: nudge: %v", p.instance, err)
	}
	return nudged
}

// Force implements core.Forcer for a ConnectorABI source: the plugin builds
// the events, the daemon routes them and marks them forced.
func (p *pluginSourceIntegration) Force(ctx context.Context, kind, repo string, number int, emit core.EmitFunc) (int, error) {
	ext, ok := p.source.(pluginSourceExt)
	if !ok || !p.sourceABI() {
		return 0, fmt.Errorf("plugin source %s does not support force", p.instance)
	}
	evs, err := ext.Force(ctx, sdk.ForceRequest{Instance: p.instance, Kind: kind, Repo: repo, Number: number})
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
		return 0, fmt.Errorf("no %q trigger fired for %s#%d", kind, repo, number)
	}
	return n, nil
}

// AppToken implements the engine's resume-time App token re-mint for a
// ConnectorABI source.
func (p *pluginSourceIntegration) AppToken(ctx context.Context, instID int64) (string, error) {
	ext, ok := p.source.(pluginSourceExt)
	if !ok || !p.sourceABI() {
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
