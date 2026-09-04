package main

import (
	"fmt"
	"sort"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/hosts"
)

// paseoRuntimeDef is one paseo-type runtimes:/controllers: entry's launch
// surface: which binary, and (optionally) which SSH host it runs on.
type paseoRuntimeDef struct {
	Name    string
	Bin     string
	Host    string
	Default bool
}

// paseoRuntimeDefs lists every paseo-type entry across runtimes: and legacy
// controllers:, sorted by name for determinism.
func paseoRuntimeDefs(cfg *config.Config) []paseoRuntimeDef {
	var out []paseoRuntimeDef
	for name, rt := range cfg.Runtimes {
		if rt.Type == "paseo" {
			out = append(out, paseoRuntimeDef{Name: name, Bin: rt.Bin, Host: rt.Host, Default: rt.Default})
		}
	}
	for name, cc := range cfg.Controllers {
		if cc.Type == "paseo" {
			out = append(out, paseoRuntimeDef{Name: name, Bin: cc.Bin, Host: cc.Host, Default: cc.Default})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolvePaseoBin picks the PRIMARY dispatcher's binary — the one shared by
// command steps, provisioning for non-paseo controllers, and the built-in
// paseo fallback. It must be local: the default local paseo runtime's bin,
// else the first local one's, else the top-level paseo_bin. Runtimes with a
// different bin or a host: get their OWN dispatchers (see
// buildPaseoOverrides), so nothing errors on plurality anymore.
func resolvePaseoBin(cfg *config.Config) (string, error) {
	var first string
	for _, def := range paseoRuntimeDefs(cfg) {
		if def.Host != "" || def.Bin == "" {
			continue
		}
		if def.Default {
			return def.Bin, nil
		}
		if first == "" {
			first = def.Bin
		}
	}
	if first != "" {
		return first, nil
	}
	return cfg.PaseoBin, nil
}

// buildPaseoOverrides builds a dedicated dispatcher for every paseo runtime
// that differs from the primary — its own bin:, or a host: whose paseo CLI
// runs over SSH. The registry rebinds those runtimes to these dispatchers, so
// `runtime: gpu-paseo` on an agent profile launches (and is inspected,
// queued to, archived) on that box.
func buildPaseoOverrides(cfg *config.Config, primaryBin string, retry config.Retry, dryRun bool) (map[string]*dispatch.Dispatcher, error) {
	out := map[string]*dispatch.Dispatcher{}
	for _, def := range paseoRuntimeDefs(cfg) {
		bin := def.Bin
		if bin == "" {
			bin = primaryBin
		}
		if def.Host == "" && bin == primaryBin {
			continue // the shared primary dispatcher covers it
		}
		d := dispatch.New(bin, retry, dryRun)
		if def.Host != "" {
			hc, ok := cfg.Hosts[def.Host]
			if !ok {
				return nil, fmt.Errorf("runtime %q: unknown host %q (defined: %s)", def.Name, def.Host, sortedHostNames(cfg.Hosts))
			}
			d.Remote = &hosts.Target{Name: def.Host, Cfg: hc}
		}
		out[def.Name] = d
	}
	return out, nil
}

// sortedHostNames lists defined hosts: names for error messages.
func sortedHostNames(hostMap map[string]config.HostConfig) string {
	if len(hostMap) == 0 {
		return "none"
	}
	names := make([]string, 0, len(hostMap))
	for n := range hostMap {
		names = append(names, n)
	}
	sort.Strings(names)
	return joinComma(names)
}

func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
