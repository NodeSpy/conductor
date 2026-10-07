package main

import (
	"context"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// pluginReloadTimeout bounds one reloadMoved pass (probe-describe + drain + swap
// across all moved plugins).
const pluginReloadTimeout = 90 * time.Second

// reloadMoved applies each moved plugin's new build IN PLACE (Client.Reload — no
// daemon restart) when its Decl surface is unchanged from what the daemon's
// registrations were built against. It returns true only if it handled ALL of
// them; anything it can't reload makes it return false so the caller restarts
// the daemon (which re-applies everything):
//
//   - a moved item that isn't a live plugin (a pack, or a removed ref);
//   - a plugin with no live *Client (an ACP-dialect runtime — its process is a
//     per-session spawn, not a swappable client);
//   - a changed Decl (verbs/ABI/kind) or widened permissions (SameReloadSurface);
//   - a live connector instance whose PER-INSTANCE declaration (Q6,
//     plugin-contract.md §1.4) would change under the new build — the
//     type-level SameReloadSurface check above can never see this for a
//     rest/graphql-shaped plugin, whose type decl has no verbs or events at
//     all, so every instance-specific verb/event lives only in what
//     plugin.describe {instance} answers;
//   - a source connector or a client that won't drain (Client.Reload error).
//
// reg is the connector registry built from this same plugin set (nil when the
// config has no connectors: block) — it is where InstancesUsingPlugin finds
// the live instances to re-check.
//
// Two passes: validate every moved plugin is reloadable first, then apply — so a
// mixed batch never does partial in-place swaps and then restarts anyway.
func reloadMoved(cfg *config.Config, mgr *plugin.Manager, reg *connector.Registry, moved []plugin.Resolution) bool {
	if mgr == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginReloadTimeout)
	defer cancel()

	// Side-by-side versions: refs is exploded into one entry per
	// resolved-version GROUP, keyed the same way Manager and the connector
	// registry are (Spec.GroupKey) — reloading the group a moved Resolution
	// names must never touch a SIBLING group of the same plugin name still
	// pinned to a different version (docs/wiki/Plugins.md "Side-by-side
	// versions").
	state := plugin.LoadInstallState(plugin.InstallDir())
	refs, _ := plugin.ExplodeRefs(cfg.PluginRefs(), cfg.BaseDir(), state) // see pluginManagerForStack for the handled case
	describe := describeForInstall(cfg)

	type pending struct {
		key, name string
		newSpec   plugin.Spec
	}
	var todo []pending
	for _, r := range moved {
		if !r.Changed() {
			continue
		}
		ref, ok := refs[r.GroupKey]
		if !ok {
			logf("reload: %q is not a live plugin (pack or removed ref) — restarting to apply", r.GroupKey)
			return false
		}
		if !mgr.HasLiveClient(r.GroupKey) {
			logf("reload: %s has no live client (ACP runtime?) — restarting to apply", r.Name)
			return false
		}
		oldDecl, ok := mgr.Decl(r.GroupKey)
		if !ok {
			logf("reload: %s has no recorded boot surface — restarting to apply", r.Name)
			return false
		}
		inst, ok := state.GetVersion(r.Key, r.Tag, r.Source)
		if !ok {
			logf("reload: %s not in install state after reconcile — restarting to apply", r.Name)
			return false
		}
		newSpec := plugin.SpecFromRef(ref, cfg.BaseDir(), inst, ok)
		newSpec.GroupKey = r.GroupKey
		newDecl, err := describe(ctx, newSpec)
		if err != nil {
			logf("reload: describing new %s build failed (%v) — restarting to apply", r.Name, err)
			return false
		}
		if !plugin.SameReloadSurface(oldDecl, newDecl) {
			logf("reload: %s interface changed (verbs/abi/kind/permissions) — restarting to apply", r.Name)
			return false
		}
		// Q6: the type-level check above says nothing about a per-instance
		// declaration — a rest/graphql-shaped plugin's type decl carries no
		// verbs or events at all, so it ALWAYS passes SameReloadSurface no
		// matter what an instance's own declared verbs/events do. Re-describe
		// every live instance that had one against the new build, and refuse
		// the in-place swap (fall back to restart) unless each is still the
		// same.
		if needCheck := instancesWithQ6Decl(connector.InstancesUsingPlugin(reg, r.GroupKey)); len(needCheck) > 0 {
			newInstDecls, err := describeInstancesForInstall(ctx, newSpec, needCheck)
			if err != nil {
				logf("reload: describing %s per-instance build failed (%v) — restarting to apply", r.Name, err)
				return false
			}
			for _, in := range needCheck {
				nd := newInstDecls[in.Instance]
				if nd == nil || !plugin.SameReloadSurface(in.Decl, nd) {
					logf("reload: %s instance %q per-instance declaration changed — restarting to apply", r.Name, in.Instance)
					return false
				}
			}
		}
		todo = append(todo, pending{r.GroupKey, r.Name, newSpec})
	}
	for _, p := range todo {
		if err := mgr.Reload(p.key, p.newSpec); err != nil {
			logf("reload: %s in-place swap failed (%v) — restarting to apply", p.name, err)
			return false
		}
		logf("plugin %s: hot-reloaded in place — no restart", p.name)
	}
	return true
}

// instancesWithQ6Decl filters to the instances that actually have a
// per-instance Decl to re-check — one whose plugin never implements/answers
// plugin.describe {instance} has nothing instance-specific the type-level
// SameReloadSurface check didn't already cover.
func instancesWithQ6Decl(insts []connector.PluginInstanceDecl) []connector.PluginInstanceDecl {
	var out []connector.PluginInstanceDecl
	for _, in := range insts {
		if in.Decl != nil {
			out = append(out, in)
		}
	}
	return out
}

// describeInstancesForInstall re-describes every given LIVE instance (Q6,
// plugin-contract.md §1.4) against the plugin's NEW build — each in its OWN
// process, spawned with exactly that instance's own grant (plugin.InstanceSpec:
// the same Network/AllowSecrets/AllowEnv/Isolation Manager.InstanceClient
// would build that instance's real process from), closed the moment it
// answers.
//
// This is finding 1: instances is built from connector.InstancesUsingPlugin,
// whose Connection field is ei.conn — each instance's RESOLVED connection,
// holding real secret values. A single shared process asked to describe every
// instance in turn (as this once did) would hand every sibling's connection,
// secrets included, to that one process — exactly the cross-instance
// exposure per-instance processes (docs/wiki/Plugins.md "Multi-instance
// isolation") exist to prevent, even though the type-level Decl surface this
// re-probes never calls a verb or needs a secret itself. One throwaway
// process per instance, each confined to only that one instance's own grant,
// closes that gap.
//
// The returned map holds a nil entry for an instance the new build no longer
// answers plugin.describe {instance} for at all — reloadMoved treats that
// drop the same as any other declaration change: unsafe to swap in place.
func describeInstancesForInstall(ctx context.Context, spec plugin.Spec, instances []connector.PluginInstanceDecl) (map[string]*plugin.Decl, error) {
	sec := secrets.New()
	deps := pluginDeps(sec, func(map[string]any) {}, nil)
	out := make(map[string]*plugin.Decl, len(instances))
	for _, in := range instances {
		d, supported, err := describeOneInstanceForInstall(ctx, spec, in, deps)
		if err != nil {
			return nil, fmt.Errorf("instance %s: %w", in.Instance, err)
		}
		if supported {
			out[in.Instance] = d
		} else {
			out[in.Instance] = nil
		}
	}
	return out, nil
}

// describeOneInstanceForInstall spawns ONE instance's own process — confined
// to exactly that instance's grant (plugin.InstanceSpec), never a sibling's —
// describes it, and closes it before returning.
func describeOneInstanceForInstall(ctx context.Context, spec plugin.Spec, in connector.PluginInstanceDecl, deps plugin.Deps) (*plugin.Decl, bool, error) {
	cl := plugin.NewClient(plugin.InstanceSpec(spec, in.Instance), deps)
	defer cl.Close()
	if err := cl.Start(ctx); err != nil {
		return nil, false, err
	}
	return cl.DescribeInstance(ctx, in.Instance, in.Connection)
}
