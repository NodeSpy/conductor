package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// HeadlessModeNone is the `headless_mode:` value that injects no mode: the
// provider's own default mode applies, as it did before conductor chose one.
const HeadlessModeNone = "none"

// HeadlessMode is a paseo runtime's `headless_mode:` — the provider mode a
// HEADLESS agent launches in (one conductor dispatches and owns, on a step
// with no `mode:` of its own, that nobody drives). Unset, conductor picks each
// known provider's no-prompt mode (see dispatch.noPromptModes); this knob
// tightens that. Two YAML shapes:
//
//	headless_mode: auto                    # one mode id for every provider ("none": inject nothing)
//	headless_mode: { claude: auto, codex: none }   # per provider; unlisted ones keep the default
type HeadlessMode struct {
	// All is the scalar form: one mode id for every provider.
	All string
	// Providers is the map form: provider id → mode id (or "none").
	Providers map[string]string
}

// UnmarshalYAML accepts a scalar mode id or a provider → mode map.
func (h *HeadlessMode) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return fmt.Errorf("headless_mode: %w", err)
		}
		*h = HeadlessMode{All: s}
		return nil
	case yaml.MappingNode:
		var m map[string]string
		if err := n.Decode(&m); err != nil {
			return fmt.Errorf("headless_mode: a per-provider map takes a mode id per provider (e.g. { claude: auto, codex: none }): %w", err)
		}
		*h = HeadlessMode{Providers: m}
		return nil
	}
	return fmt.Errorf("headless_mode: must be a mode id (e.g. auto, or none) or a per-provider map (e.g. { claude: auto })")
}

// MarshalYAML writes the shape it was read from, so a re-marshalled config
// loads back the same.
func (h HeadlessMode) MarshalYAML() (any, error) {
	if h.Providers != nil {
		return h.Providers, nil
	}
	return h.All, nil
}

// validHeadlessModeID reports whether s can be passed as `paseo run --mode`:
// non-empty, one token, not a flag.
func validHeadlessModeID(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\r\n")
}

// validate reports a malformed knob with the runtime it belongs to.
func (h HeadlessMode) validate(runtime string) error {
	if h.Providers == nil {
		if !validHeadlessModeID(h.All) {
			return fmt.Errorf("config: runtime %q: headless_mode: %q is not a mode id — give one (e.g. auto), %q to inject none, or a per-provider map", runtime, h.All, HeadlessModeNone)
		}
		return nil
	}
	if len(h.Providers) == 0 {
		return fmt.Errorf("config: runtime %q: headless_mode: an empty map — name at least one provider, or remove the key", runtime)
	}
	for _, p := range sortedNames(h.Providers) {
		if strings.TrimSpace(p) == "" || p != strings.ToLower(p) {
			return fmt.Errorf("config: runtime %q: headless_mode: provider %q must be a lowercase paseo provider id (claude, codex, gemini, …)", runtime, p)
		}
		if m := h.Providers[p]; !validHeadlessModeID(m) {
			return fmt.Errorf("config: runtime %q: headless_mode.%s: %q is not a mode id — give one (e.g. auto) or %q", runtime, p, m, HeadlessModeNone)
		}
	}
	return nil
}

// PaseoPrimaryBin is the binary of conductor's PRIMARY paseo dispatcher — the
// one shared by command steps, provisioning, the built-in paseo fallback, and
// every local paseo runtime on that binary: the default local paseo runtime's
// `bin:`, else the first (by name) local one's, else the built-in "paseo".
// Runtimes with another bin or a `host:` get dedicated dispatchers.
func (c *Config) PaseoPrimaryBin() string {
	names := sortedNames(c.Runtimes)
	first := ""
	for _, name := range names {
		rt := c.Runtimes[name]
		if rt.BuiltinType() != "paseo" || rt.Host != "" || rt.Bin == "" {
			continue
		}
		if rt.Default {
			return rt.Bin
		}
		if first == "" {
			first = rt.Bin
		}
	}
	if first != "" {
		return first
	}
	return BuiltinPaseoRuntime
}

// SharesPaseoPrimary reports whether a paseo runtime runs on the primary
// dispatcher (local, on the primary bin) rather than a dedicated one.
func (c *Config) SharesPaseoPrimary(rt RuntimeConfig) bool {
	if rt.BuiltinType() != "paseo" || rt.Host != "" {
		return false
	}
	return rt.Bin == "" || rt.Bin == c.PaseoPrimaryBin()
}

// PrimaryHeadlessMode is the headless_mode of the primary paseo dispatcher:
// every runtime sharing it must agree (validateHeadlessModes enforces it), so
// the first one's setting is everyone's. No such runtime → unset (defaults).
func (c *Config) PrimaryHeadlessMode() HeadlessMode {
	names := sortedNames(c.Runtimes)
	for _, name := range names {
		if rt := c.Runtimes[name]; c.SharesPaseoPrimary(rt) && rt.HeadlessMode != nil {
			return *rt.HeadlessMode
		}
	}
	return HeadlessMode{}
}

// validateHeadlessModes checks every `headless_mode:` — a paseo (or runtime
// plugin) option only, well-formed — and that runtimes sharing the primary
// dispatcher agree on it: one dispatcher launches all their agents, so it
// cannot honor two settings.
func (c *Config) validateHeadlessModes() error {
	names := sortedNames(c.Runtimes)
	var shared *HeadlessMode
	sharedBy := ""
	for _, name := range names {
		rt := c.Runtimes[name]
		if rt.HeadlessMode != nil {
			if rt.BuiltinType() != "paseo" && !rt.IsPlugin() {
				return fmt.Errorf("config: runtime %q: `headless_mode:` applies to a paseo runtime only — it picks the paseo provider mode a headless agent launches in", name)
			}
			if err := rt.HeadlessMode.validate(name); err != nil {
				return err
			}
		}
		if !c.SharesPaseoPrimary(rt) {
			continue
		}
		mine := HeadlessMode{}
		if rt.HeadlessMode != nil {
			mine = *rt.HeadlessMode
		}
		if shared != nil && !reflect.DeepEqual(*shared, mine) {
			return fmt.Errorf("config: runtimes %q and %q share conductor's primary paseo dispatcher (local, same bin) but set different headless_mode — set it the same on both, or give one its own bin:", sharedBy, name)
		}
		shared, sharedBy = &mine, name
	}
	return nil
}
