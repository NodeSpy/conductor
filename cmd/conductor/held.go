package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// heldNotifier tells the user a plugin update waits for their approval
// because it changes what the plugin may do; cmdRun wires it to the daemon's
// notify path.
var (
	heldNotifier func(msg string)
	heldMu       sync.Mutex
)

func setHeldNotifier(n func(msg string)) {
	heldMu.Lock()
	heldNotifier = n
	heldMu.Unlock()
}

// announceHeld sends each held update the user has not been told about once,
// and records in install state that it was sent, so neither the next update
// pass nor a restart sends it again (a newer held release is sent anew). It
// stays listed (`plugin list`, `status`, the dashboard) until approved or
// declined. It
// runs after every reconcile pass and at boot; with no notifier (a CLI
// command) it does nothing, and the daemon sends it at its next pass.
func announceHeld() {
	heldMu.Lock()
	notify := heldNotifier
	heldMu.Unlock()
	dir := plugin.InstallDir()
	if notify == nil || dir == "" {
		return
	}
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	lock, err := plugin.LockInstallState(dir)
	if err != nil {
		logf("plugins: cannot announce held plugins: %v", err)
		return
	}
	defer lock.Unlock()
	state := plugin.LoadInstallState(dir)
	sent := false
	for _, p := range state.PendingUpdates() {
		if !p.Waiting() || p.Announced {
			continue
		}
		notify(p.Message())
		state.MarkAnnounced(p)
		sent = true
	}
	if !sent {
		return
	}
	if err := state.Save(); err != nil {
		logf("plugins: could not record that a held plugin was announced (it may be announced again): %v", err)
	}
}

// dashboardPluginHeld is the updates waiting for approval, for the
// dashboard's Plugins screen. It reads install state without locking it, as `plugin
// list` does: Save replaces the file atomically.
func dashboardPluginHeld() []api.PluginHeld {
	var out []api.PluginHeld
	for _, p := range plugin.LoadInstallState(plugin.InstallDir()).PendingUpdates() {
		if !p.Waiting() {
			continue
		}
		out = append(out, api.PluginHeld{Name: p.Name, Running: p.Replaces, Version: p.Resolved,
			Fingerprint: plugin.Fingerprint(p.Manifest), Adds: p.Widens, Removes: p.Removes})
	}
	return out
}

// dashboardApprovePlugin approves, from the dashboard, the held update of
// the plugin named name whose declaration is fingerprint: what `conductor
// plugin approve <name>@<fingerprint>` does (the held build replaces the
// running one).
func dashboardApprovePlugin(name, fingerprint string) (string, error) {
	return decideHeld(name, func(state *plugin.InstallState) ([]plugin.Pending, error) {
		return state.Approve(name + "@" + fingerprint)
	}, func(p plugin.Pending) string {
		logApproved(p, "the dashboard")
		return fmt.Sprintf("approved %s %s, replacing %s", p.Name, p.Resolved, p.Replaces)
	})
}

// dashboardDeclinePlugin declines, from the dashboard, the held update of the
// plugin named name whose declaration is fingerprint: what `conductor plugin
// decline <name>` does (the running build stays).
func dashboardDeclinePlugin(name, fingerprint string) (string, error) {
	return decideHeld(name, func(state *plugin.InstallState) ([]plugin.Pending, error) {
		if p, ok := heldNamed(state, name); !ok || plugin.Fingerprint(p.Manifest) != fingerprint {
			return nil, fmt.Errorf("plugin %s: what waits for approval has changed since you looked", name)
		}
		return state.Decline(name)
	}, func(p plugin.Pending) string {
		logDeclined(p, "the dashboard")
		return fmt.Sprintf("kept %s %s; declined %s", p.Name, p.Replaces, p.Resolved)
	})
}

// heldNamed is the update waiting for the plugin named name.
func heldNamed(state *plugin.InstallState, name string) (plugin.Pending, bool) {
	for _, p := range state.PendingUpdates() {
		if (p.Name == name || p.Key == name) && p.Waiting() {
			return p, true
		}
	}
	return plugin.Pending{}, false
}

// decideHeld applies one decision to install state under the same
// locks a reconcile pass takes, and saves it.
func decideHeld(name string, decide func(*plugin.InstallState) ([]plugin.Pending, error), say func(plugin.Pending) string) (string, error) {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	lock, err := plugin.LockInstallState(plugin.InstallDir())
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	state := plugin.LoadInstallState(plugin.InstallDir())
	done, err := decide(state)
	if err != nil {
		return "", err
	}
	if err := state.Save(); err != nil {
		return "", err
	}
	var what []string
	for _, p := range done {
		what = append(what, say(p))
	}
	return strings.Join(what, "; "), nil
}
