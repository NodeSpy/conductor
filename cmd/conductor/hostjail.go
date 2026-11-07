package main

import (
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// hostJailBootWarning probes, at boot, whether this host can jail plugins.
// When it cannot and a configured plugin is not trust: full, it logs the
// reason and the fix and returns the same text for the notification: the
// plugins installed before the jail keep running unjailed, the ones since
// are refused. "" when the host can, or every plugin is trust: full (the
// operator acknowledged it).
func hostJailBootWarning(cfg *config.Config) string {
	h := plugin.HostJailStatus()
	if h.OK {
		return ""
	}
	for _, ref := range cfg.PluginRefs() {
		trusted := ref.TrustFull
		for _, g := range ref.Instances {
			trusted = trusted && g.TrustFull
		}
		if !trusted {
			w := "plugins: " + h.Warning()
			logf("%s", w)
			return w
		}
	}
	return ""
}
