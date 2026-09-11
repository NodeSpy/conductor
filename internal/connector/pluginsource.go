package connector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// pluginsource.go wires a SOURCE plugin (one that streams events, #59) into the
// engine. The plugin owns the transport (listener/poll/HMAC) and emits raw
// events; the DAEMON owns action resolution — it matches each event against the
// configured triggers and lowers the matched trigger to a config.Action. This
// keeps Trigger.Action (a Go value the engine asserts) on the daemon side, out
// of the wire, and keeps the plugin a dumb, reusable event source.

// pluginSourcer is the streaming capability of *plugin.Client the source
// integration needs (Invoke lives on the verb path).
type pluginSourcer interface {
	StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error
}

// pluginEvent is the wire shape a source plugin emits (one plugin.event). It is
// the connector-agnostic normalized event: an event name, optional target/title,
// a context map (for filter matching + prompt templating), a dedup signature,
// and extra labels. Everything a kit/plugin can produce without knowing the
// operator's actions.
type pluginEvent struct {
	Event   string            `json:"event"`
	Kind    string            `json:"kind,omitempty"`
	Title   string            `json:"title,omitempty"`
	Target  core.Target       `json:"target,omitempty"`
	Context map[string]any    `json:"context,omitempty"`
	Dedup   string            `json:"dedup,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// pluginSourceIntegration adapts a source plugin to core.Integration so it wires
// into the engine exactly like a bundled source (main.go: ig.Start(ctx, eng.Emit)).
type pluginSourceIntegration struct {
	source   pluginSourcer
	instance string // connector instance name (matches the `on:` prefix)
	typ      string // connector type, for Trigger.Source
	config   map[string]any
	triggers []CompiledTrigger
	log      func(string, ...any)
}

func (p *pluginSourceIntegration) Name() string    { return p.instance }
func (p *pluginSourceIntegration) Validate() error { return nil }

// Start opens the plugin's event stream and, for each event, emits a Trigger for
// every configured trigger whose `on:` and filters match. Runs until ctx is
// cancelled (which tears down the plugin subprocess and ends the stream).
func (p *pluginSourceIntegration) Start(ctx context.Context, emit core.EmitFunc) error {
	req := plugin.StartSourceRequest{Instance: p.instance, Config: p.config}
	return p.source.StartSource(ctx, req, func(raw json.RawMessage) {
		var ev pluginEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			p.log("plugin source %s: dropping malformed event: %v", p.instance, err)
			return
		}
		kind := ev.Kind
		if kind == "" {
			kind = ev.Event
		}
		on := p.instance + "." + ev.Event
		for _, t := range p.triggers {
			if t.Spec.On != on {
				continue
			}
			if !filterMatch(t.Spec.Filters, ev.Context) {
				continue
			}
			emit(ctx, core.Trigger{
				// TargetTrusted stays FALSE, deliberately (round-8 #3). The
				// target arrives on the wire from a third-party plugin, which
				// built it from whatever payload it was handed — the same
				// provenance as a webhook body, and not conductor's code. A
				// plugin-sourced dispatch therefore gets no implicit own-repo
				// trust; an operator scoping one lists the repos.
				Source:   p.typ,
				Instance: p.instance,
				Kind:     kind,
				Variant:  t.Spec.Name,
				Target:   ev.Target,
				Title:    ev.Title,
				Context:  ev.Context,
				Dedup:    ev.Dedup,
				Labels:   ev.Labels,
				Action:   lowerAction(t),
			})
		}
	})
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
