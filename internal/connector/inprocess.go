package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/builtins/cron"
	"github.com/NodeSpy/conductor/internal/builtins/exposure"
	"github.com/NodeSpy/conductor/internal/builtins/graphql"
	"github.com/NodeSpy/conductor/internal/builtins/rest"
	"github.com/NodeSpy/conductor/internal/builtins/rss"
	"github.com/NodeSpy/conductor/internal/builtins/webhook"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// In-process builtins (plugin-contract.md §1.10): a builtin connector written
// as a pkg/plugin Handler and served over an in-memory pipe through the same
// client, transport and adapter (externalImpl) as a spawned plugin. The
// engine cannot tell it from one — it gets no private path.

// InProcessState backs host.state for in-process builtins (set by the daemon;
// nil answers host.state with an error).
var InProcessState *plugin.StateStore

// inprocessTunnel is the tunnel builtin's handler (one per daemon, like a
// spawned plugin's process).
var inprocessTunnel = exposure.NewTunnel()

func init() {
	RegisterInProcessConnector(cron.Cron{})
	RegisterInProcessConnector(rss.New())
	RegisterInProcessConnector(webhook.New())
	RegisterInProcessConnector(rest.New())
	RegisterInProcessConnector(graphql.New())
	RegisterInProcessConnector(exposure.LAN{})
	RegisterInProcessConnector(inprocessTunnel)
}

// RegisterInProcessConnector registers h as the builtin connector type it
// describes. Its declarations are checked as a plugin's are; a builtin that
// fails them is a programming error.
func RegisterInProcessConnector(h sdk.Handler) {
	d := h.Describe()
	if p := sdk.ValidateSemantics(d); len(p) > 0 {
		panic(fmt.Sprintf("builtin %s: declarations do not hang together: %v", d.Type, p))
	}
	spec := plugin.Spec{Name: d.Type, Kind: plugin.KindConnector, Provides: d.Type, InProcess: h}
	td := mapDecl(&d)
	var (
		once sync.Once
		cl   *plugin.Client
		cerr error
	)
	client := func(deps Deps) (*plugin.Client, error) {
		once.Do(func() {
			log := deps.Log
			if log == nil {
				log = func(string, ...any) {}
			}
			cl = plugin.NewClient(spec, plugin.Deps{Log: log, State: InProcessState, Auth: HostAuthProvider})
			// Over the wire, like any plugin: must-understand applies.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, cerr = cl.Describe(ctx)
		})
		return cl, cerr
	}
	RegisterType(td, func(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
		c, err := client(deps)
		if err != nil {
			return nil, fmt.Errorf("builtin %s: %w", d.Type, err)
		}
		conn, refs, err := resolveConnection(ref, deps.Secrets, nil)
		if err != nil {
			return nil, err
		}
		trackDeclaredSecrets(conn, td.Connection, deps.Secrets)
		log := deps.Log
		if log == nil {
			log = func(string, ...any) {}
		}
		// Builtins get exactly the same generic contract treatment a spawned
		// plugin does (RegisterExternalConnector): a declared `auth:` block
		// may be managed OAuth2 (the daemon owns the token exchange and
		// injects the bearer) or passed through for the builtin's own static
		// schemes, and a per-instance declaration (Q6) — rest/graphql/webhook
		// get no special case the engine or this registration path knows
		// about.
		au, err := buildManagedAuth(name, d.Auth, ref, deps)
		if err != nil {
			return nil, fmt.Errorf("builtin %s: %w", d.Type, err)
		}
		registerAuth(name, au)
		if err := enrichConnection(conn, ref, deps, nil); err != nil {
			return nil, fmt.Errorf("builtin %s: %w", d.Type, err)
		}
		effDecl := td
		id, err := resolveInstanceDecl(c, name, conn)
		if err != nil {
			return nil, fmt.Errorf("builtin %s: %w", d.Type, err)
		}
		if id != nil {
			effDecl = id
		}
		return &externalImpl{
			client: c, source: c, instance: name, decl: effDecl, conn: conn, auth: au, secretRefs: refs,
			pluginRef: "builtin:" + d.Type, pluginType: d.Type, audit: deps.Audit, log: log,
			lookup: deps.Lookup,
		}, nil
	})
}
