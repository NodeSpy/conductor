package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// resolvePaseoBin picks the paseo binary the shared dispatcher runs: a
// paseo-type runtime's (or legacy controller's) bin: wins over the top-level
// paseo_bin. One conductor drives one paseo binary — two runtimes naming
// DIFFERENT bins is a config error, not a silent pick.
func resolvePaseoBin(cfg *config.Config) (string, error) {
	bins := map[string]string{} // bin -> runtime name
	add := func(name, typ, bin string) {
		if typ == "paseo" && bin != "" {
			bins[bin] = name
		}
	}
	for name, rt := range cfg.Runtimes {
		add(name, rt.Type, rt.Bin)
	}
	for name, cc := range cfg.Controllers {
		add(name, cc.Type, cc.Bin)
	}
	switch len(bins) {
	case 0:
		return cfg.PaseoBin, nil
	case 1:
		for bin := range bins {
			return bin, nil
		}
	}
	names := make([]string, 0, len(bins))
	for bin, name := range bins {
		names = append(names, fmt.Sprintf("%s (bin: %s)", name, bin))
	}
	sort.Strings(names)
	return "", fmt.Errorf("config: multiple paseo runtimes with different bin: values are not supported — one paseo binary per conductor (got %s)", strings.Join(names, ", "))
}

// sortedHostNames lists defined hosts: names for error messages.
func sortedHostNames(hosts map[string]config.HostConfig) string {
	if len(hosts) == 0 {
		return "none"
	}
	names := make([]string, 0, len(hosts))
	for n := range hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
