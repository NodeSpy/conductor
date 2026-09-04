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
		"listen":   {Type: TString, Desc: "inbound HTTP address draft pages are served on (default :8099)"},
		"ttl":      {Type: TDuration, Desc: "how long a presented draft's link stays valid (default 30m)"},
		"tunnel":   {Type: TMap, Desc: "pluggable per-draft tunnel: provider, host, mode, ssh_host, authtoken, url_pattern, command"},
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
	BaseURL string              `yaml:"base_url"`
	Listen  string              `yaml:"listen"`
	TTL     config.Duration     `yaml:"ttl"`
	Tunnel  config.TunnelConfig `yaml:"tunnel"`
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
	// Unlike handoff/registry.go's buildChannel (which logs and silently
	// falls back to no tunnel on a bad tunnel config), a construction failure
	// here is a hard build error — the connectors-model's registry turns that
	// into this connector's DisabledReason rather than degrading the channel
	// quietly.
	t, err := handoff.NewTunnel(conn.Tunnel, conn.BaseURL, logf)
	if err != nil {
		return nil, fmt.Errorf("tunnel: %w", err)
	}
	ch.SetTunnel(t, webListenDefault(conn.Listen))
	return &webImpl{name: name, conn: conn, deps: deps, ch: ch}, nil
}

// webListenDefault mirrors handoff/registry.go's webListen default.
func webListenDefault(listen string) string {
	if listen != "" {
		return listen
	}
	return ":8099"
}

func (w *webImpl) Validate() error {
	if w.conn.BaseURL == "" && w.conn.Tunnel.Provider == "" {
		return fmt.Errorf("connector %q: set base_url and/or tunnel to present links", w.name)
	}
	return nil
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
