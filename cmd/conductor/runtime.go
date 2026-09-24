package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/paseover"
)

// paseoRuntimeDef is one paseo-type runtimes:/controllers: entry's launch
// surface: which binary, which daemon home, and (optionally) which SSH host it
// runs on.
type paseoRuntimeDef struct {
	Name    string
	Bin     string
	Home    string
	Server  string
	Host    string
	Default bool
}

// paseoRuntimeDefs lists every paseo-type entry across runtimes: and legacy
// controllers:, sorted by name for determinism.
func paseoRuntimeDefs(cfg *config.Config) []paseoRuntimeDef {
	var out []paseoRuntimeDef
	for name, rt := range cfg.Runtimes {
		if rt.BuiltinType() == "paseo" {
			out = append(out, paseoRuntimeDef{Name: name, Bin: rt.Bin, Home: rt.Home, Server: rt.Server, Host: rt.Host, Default: rt.Default})
		}
	}
	for name, cc := range cfg.Controllers {
		if cc.Type == "paseo" {
			out = append(out, paseoRuntimeDef{Name: name, Bin: cc.Bin, Home: cc.Home, Server: cc.Server, Host: cc.Host, Default: cc.Default})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// paseoEndpointFor resolves WHICH DAEMON a paseo runtime's CLI talks to — the
// full ladder lives in internal/paseover so dispatch and model discovery reach
// the same one from the same inputs (they did not, and that was the #145 bug).
func paseoEndpointFor(def paseoRuntimeDef) paseover.Endpoint {
	return paseover.Resolve(paseover.Target{
		Server: def.Server,
		Home:   def.Home,
		Local:  def.Host == "",
	})
}

// resolvePaseoEndpoint picks the PRIMARY dispatcher's daemon, mirroring
// resolvePaseoBin: the default local paseo runtime's, else the first local
// one's, else the ladder's own answer for a box with no paseo runtime declared.
func resolvePaseoEndpoint(cfg *config.Config) paseover.Endpoint {
	var first paseover.Endpoint
	seenFirst := false
	for _, def := range paseoRuntimeDefs(cfg) {
		if def.Host != "" {
			continue
		}
		if def.Default {
			return paseoEndpointFor(def)
		}
		if !seenFirst {
			first = paseoEndpointFor(def)
			seenFirst = true
		}
	}
	if seenFirst && len(first.Args) > 0 {
		return first
	}
	return paseover.Resolve(paseover.Target{Local: true})
}

// expandTilde expands a leading ~ or ~/ to the user's home dir.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
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
		ep := paseoEndpointFor(def)
		d.Home, d.Server = ep.Home(), ep.Server()
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
