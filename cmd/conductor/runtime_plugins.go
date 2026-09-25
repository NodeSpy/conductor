package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/decider"
	"github.com/NodeSpy/conductor/internal/dispatch"
	agentmodels "github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// runtimePluginBackend is a runtime plugin that speaks Backend-RPC (the paseo
// plugin dialect): its live *plugin.Client wrapped as a dispatch.Backend, ready
// to drive a dedicated Dispatcher/Reaper for that runtime name.
type runtimePluginBackend struct {
	Name    string
	Backend dispatch.Backend
	Bin     string // the runtime's bin: (paseo_bin passed to the plugin), for the dispatcher's own PaseoBin display
}

// loadRuntimePlugins starts and describes every runtimes:-kind plugin, then
// classifies each by dialect. A plugin whose Describe() declares the full
// Backend-RPC verb set (SpeaksBackendRPC) is kept RUNNING and returned wrapped
// as a dispatch.Backend — the caller gives it a dedicated Dispatcher + Reaper
// via reg.OverridePaseo, so it drives paseo dispatch instead of the builtin
// cliBackend. A plugin that does NOT (an ACP-dialect runtime) has its probe
// client closed here and is left for pluginRuntimeControllers to wrap as an ACP
// subprocess — the two paths are disjoint, so a name is claimed by exactly one.
//
// Fail-closed, same posture as loadConnectorPlugins/loadEnginePlugins: a start,
// describe, or kind mismatch stops boot rather than degrading (a bad plugin is
// an operator/security condition, not a transient).
func loadRuntimePlugins(mgr *plugin.Manager, cfg *config.Config, retry config.Retry, sec *secrets.Resolver) (map[string]runtimePluginBackend, decider.Set, error) {
	return loadRuntimePluginsFor(mgr, cfg, retry, sec, true)
}

// loadDecisionRuntimes is loadRuntimePlugins for one-shot mode: it adopts the
// DECISION runtimes (they need nothing long-lived — a client and its
// connection) and leaves every other runtime plugin exactly as one-shot mode
// always has (Backend-RPC runtimes unwired, ACP runtimes to the controller
// path). Without it a decision runtime would fall through to the ACP path
// and agent resolution would not know to skip it.
func loadDecisionRuntimes(mgr *plugin.Manager, cfg *config.Config, sec *secrets.Resolver) (decider.Set, error) {
	_, deciders, err := loadRuntimePluginsFor(mgr, cfg, config.Retry{}, sec, false)
	return deciders, err
}

func loadRuntimePluginsFor(mgr *plugin.Manager, cfg *config.Config, retry config.Retry, sec *secrets.Resolver, adoptBackends bool) (map[string]runtimePluginBackend, decider.Set, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginBootTimeout)
	defer cancel()

	out := map[string]runtimePluginBackend{}
	deciders := decider.Set{}
	for _, spec := range mgr.RuntimeSpecs() {
		decl, err := mgr.StartAndDescribe(ctx, spec.Key())
		if err != nil {
			return nil, nil, fmt.Errorf("runtime plugin %s: %w", spec.Name, err)
		}
		// KIND ENFORCEMENT at the point of use, as for connectors/engines: the
		// running binary must still describe itself as a runtime. A connector or
		// engine accepted here would be driven with agent-launch verbs it never
		// agreed to.
		if decl.Kind != "" && decl.Kind != plugin.KindRuntime {
			return nil, nil, fmt.Errorf("runtime plugin %s: referenced under runtimes: but it describes itself as %s — refusing", spec.Name, decl.Kind)
		}
		// A DECISION runtime (it declares decision protocols and serves
		// decide) answers decide: steps only: keep its client running, hand it
		// its resolved connection, and register its roster for fleets. It is
		// neither a dispatcher nor an ACP session, so it takes neither path
		// below.
		if decider.IsDecisionRuntime(decl) {
			rt := cfg.Runtimes[spec.Name]
			cl, ok := mgr.Client(spec.Key())
			if !ok {
				return nil, nil, fmt.Errorf("runtime plugin %s: no client after describe (internal)", spec.Name)
			}
			conn, err := resolveRuntimeConnection(ctx, rt.Connection, sec)
			if err != nil {
				return nil, nil, fmt.Errorf("runtime plugin %s: connection: %w", spec.Name, err)
			}
			d := decider.New(spec.Name, decl.Protocols, cl, conn)
			deciders[spec.Name] = d
			registerDeciderRoster(spec.Name, rt, d)
			logf("plugin %s: registered decision runtime %q (%s); permissions: %s",
				spec.Ref(), spec.Name, strings.Join(decl.Protocols, ", "), spec.EffectiveManifest().Summary())
			continue
		}
		if !adoptBackends || !dispatch.SpeaksBackendRPC(decl) {
			// ACP-dialect runtime plugin: the real session is a fresh
			// `conductor plugin-exec` subprocess spawned per session, NOT this
			// probe client — close it and let pluginRuntimeControllers wrap it.
			if cl, ok := mgr.Client(spec.Key()); ok {
				_ = cl.Close()
			}
			continue
		}
		rt := cfg.Runtimes[spec.Name]
		if rt.Host != "" {
			// A Backend-RPC plugin runs as a local-stdio subprocess of this
			// daemon; there is no remoting story for its plugin.Client yet.
			return nil, nil, fmt.Errorf("runtime plugin %s: host: is not yet supported for a Backend-RPC runtime plugin (remove host:, or use a builtin paseo runtime with host:)", spec.Name)
		}
		cl, ok := mgr.Client(spec.Key())
		if !ok {
			return nil, nil, fmt.Errorf("runtime plugin %s: no client after describe (internal)", spec.Name)
		}
		conn := map[string]any{}
		if rt.Bin != "" {
			conn["paseo_bin"] = rt.Bin
		}
		backend := dispatch.NewRPCBackend(cl, spec.Name, conn, retry.Attempts(), retry.BackoffDur())
		out[spec.Name] = runtimePluginBackend{Name: spec.Name, Backend: backend, Bin: rt.Bin}
		logf("plugin %s: registered runtime %q (backend-rpc); permissions: %s",
			spec.Ref(), spec.Name, spec.EffectiveManifest().Summary())
	}
	return out, deciders, nil
}

// resolveRuntimeConnection resolves a runtime's connection: block — each
// secret reference (env:, a vault ref, op://…) replaced by its value and
// tracked for redaction, exactly as a connector's connection fields are.
func resolveRuntimeConnection(ctx context.Context, raw map[string]any, sec *secrets.Resolver) (map[string]any, error) {
	conn := make(map[string]any, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok || sec == nil || !secrets.IsRef(s) {
			conn[k] = v
			continue
		}
		val, err := sec.Resolve(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", k, err)
		}
		sec.Track(val)
		conn[k] = val
	}
	return conn, nil
}

// registerDeciderRoster makes a decision runtime's models discoverable: its
// implementation name gets a lister that asks the plugin's models verb, so a
// fleet naming `jev-*` resolves against what the runtime actually offers.
func registerDeciderRoster(name string, rt config.RuntimeConfig, d *decider.Runtime) {
	impl := name
	if u, err := rt.Resolved(); err == nil && u.Name != "" {
		impl = u.Name
	}
	agentmodels.Register(impl, func(r agentmodels.Runtime, _ *agentmodels.Catalog) agentmodels.Lister {
		return d
	})
}

// runtimePluginManager returns the *plugin.Manager loadRuntimePlugins should
// use. In the common case it reuses stack.Plugins (which already built a Client
// for every runtimes/* ref and is Closed by the daemon's `defer stack.Close()`).
// A config with no connectors:/triggers: block makes buildFlowStack return a nil
// stack, yet cfg.PluginRefs() can still reference a plugin runtime — so build a
// standalone Manager there and return its own closer.
func runtimePluginManager(stack *flowStack, cfg *config.Config, sec *secrets.Resolver, audit func(map[string]any)) (*plugin.Manager, func()) {
	if stack != nil && stack.Plugins != nil {
		return stack.Plugins, func() {}
	}
	m := pluginManagerFor(cfg, sec, audit)
	return m, func() { _ = m.Close() }
}
