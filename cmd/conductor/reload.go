package main

import (
	"context"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
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
//   - a source connector or a client that won't drain (Client.Reload error).
//
// Two passes: validate every moved plugin is reloadable first, then apply — so a
// mixed batch never does partial in-place swaps and then restarts anyway.
func reloadMoved(cfg *config.Config, mgr *plugin.Manager, moved []plugin.Resolution) bool {
	if mgr == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginReloadTimeout)
	defer cancel()

	refs := cfg.PluginRefs()
	state := plugin.LoadInstallState(plugin.InstallDir())
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
		ref, ok := refs[r.Key]
		if !ok {
			logf("reload: %q is not a live plugin (pack or removed ref) — restarting to apply", r.Key)
			return false
		}
		if _, ok := mgr.Client(r.Key); !ok {
			logf("reload: %s has no live client (ACP runtime?) — restarting to apply", r.Name)
			return false
		}
		oldDecl, ok := mgr.Decl(r.Key)
		if !ok {
			logf("reload: %s has no recorded boot surface — restarting to apply", r.Name)
			return false
		}
		inst, ok := state.Get(r.Key)
		if !ok {
			logf("reload: %s not in install state after reconcile — restarting to apply", r.Name)
			return false
		}
		newSpec := plugin.SpecFromRef(ref, cfg.BaseDir(), inst, ok)
		newDecl, err := describe(ctx, newSpec)
		if err != nil {
			logf("reload: describing new %s build failed (%v) — restarting to apply", r.Name, err)
			return false
		}
		if !plugin.SameReloadSurface(oldDecl, newDecl) {
			logf("reload: %s interface changed (verbs/abi/kind/permissions) — restarting to apply", r.Name)
			return false
		}
		todo = append(todo, pending{r.Key, r.Name, newSpec})
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
