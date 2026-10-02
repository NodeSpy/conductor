package connector

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/handoff"
)

var webDecl = &TypeDecl{
	Type: "web",
	Desc: "Web: a draft link served on conductor's own HTTP listener (approve/revise/discard + a text box); no source events.",
	Connection: Schema{
		"base_url": {Type: TString, Desc: "public origin draft links point at, e.g. https://conductor.example.com"},
		"listen":   {Type: TString, Desc: "inbound HTTP address draft pages are served on (default 127.0.0.1:8099 — loopback only)"},
		"ttl":      {Type: TDuration, Desc: "how long a presented draft's link stays valid (default 30m)"},
		"expose":   {Type: TString, Desc: "an exposure connector (one declaring an exposes verb: lan, tunnel, or a plugin) that gives each draft a public URL, instead of a fixed base_url"},
	},
	Verbs: []VerbDecl{
		{
			Name: "ask", Desc: "present a question/draft and wait for the reply", Ask: true,
			Options: askOptionBase(),
			Outputs: askOutputs(),
		},
	},
}

func init() { RegisterType(webDecl, newWebImpl) }

// webConn mirrors config.HandoffWeb — the connectors-model connection schema
// for the web hand-off channel.
type webConn struct {
	BaseURL string          `yaml:"base_url"`
	Listen  string          `yaml:"listen"`
	TTL     config.Duration `yaml:"ttl"`
	Expose  string          `yaml:"expose"`
}

type webImpl struct {
	name string
	conn webConn
	deps Deps

	ch *handoff.WebChannel
}

func newWebImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn webConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode web connection: %w", name, err)
	}
	logf := deps.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ch := handoff.NewWebChannel(conn.BaseURL, conn.TTL.D(), logf)
	if conn.Expose != "" {
		// Each draft gets its URL from the named exposure connector's
		// exposes verb, resolved when the draft is presented.
		ch.SetTunnel(exposureTunnel{lookup: deps.Lookup, name: conn.Expose, from: name}, webListenDefault(conn.Listen))
	}
	return &webImpl{name: name, conn: conn, deps: deps, ch: ch}, nil
}

// webListenDefault mirrors handoff/registry.go's webListen default:
// loopback-only unless the config binds wider explicitly (draft pages carry
// approve/deny actions and are reached through the tunnel, not the LAN).
func webListenDefault(listen string) string {
	if listen != "" {
		return listen
	}
	return "127.0.0.1:8099"
}

func (w *webImpl) Validate() error {
	if w.conn.BaseURL == "" && w.conn.Expose == "" {
		return fmt.Errorf("connector %q: set base_url or expose to present links", w.name)
	}
	return nil
}

// ExposeTarget names the exposure connector this one uses (checked once the
// registry is built).
func (w *webImpl) ExposeTarget() string { return w.conn.Expose }

// exposeUser is a connector that names an exposure connector.
type exposeUser interface{ ExposeTarget() string }

// checkExposures disables any instance whose `expose:` names a connector
// that does not exist or cannot expose — a config mistake surfaced at boot
// and by `conductor validate`, not at the first hand-off.
func (r *Registry) checkExposures(log func(string, ...any)) {
	for _, name := range r.order {
		in := r.byName[name]
		eu, ok := in.Impl.(exposeUser)
		if !ok || eu.ExposeTarget() == "" || in.DisabledReason != "" {
			continue
		}
		if _, _, err := exposureVerb(r.Get, eu.ExposeTarget()); err != nil {
			in.DisabledReason = err.Error()
			if log != nil {
				log("connector %q disabled: %v", name, err)
			}
		}
	}
}

func (w *webImpl) DeclaredEvents() []string { return nil }

// Channel exposes the underlying web hand-off channel so main wiring mounts
// it on the inbound HTTP listener at Listen().
func (w *webImpl) Channel() *handoff.WebChannel { return w.ch }

// Listen returns the inbound HTTP address draft pages are served on.
func (w *webImpl) Listen() string { return webListenDefault(w.conn.Listen) }

// AskChannel implements AskChanneler so a background step's `handoff: web`
// review rides the same channel as this connector's own ask verb.
func (w *webImpl) AskChannel(opts map[string]any) (handoff.Channel, error) { return w.ch, nil }

func (w *webImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("connector %q (web) has no source events", w.name)
}

func (w *webImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "ask" {
		return nil, fmt.Errorf("web: unknown verb %q", verb)
	}
	return runAsk(ctx, w.ch, opts)
}
