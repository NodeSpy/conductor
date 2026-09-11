package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// `${settings.NAME}` — ONE parameterization mechanism, two places that use it.
//
// A pack declares `settings:` and its manifest is substituted at instantiation
// (pack_instantiate.go). A MAIN CONFIG declares a top-level `settings:` and is
// substituted at load, across every imported file, before the strict decode.
// Both go through the functions here so the syntax, the iteration bound, and
// the unknown-reference rule can't drift apart between them.
//
// It is a TEXT substitution on the config body, deliberately: the value lands
// wherever the reference sits — a scope allowlist entry, a channel, a repo
// glob, a prompt — with no per-field plumbing. That also means it is resolved
// at LOAD time and is the same for every dispatch. The per-event counterpart
// is `{{ }}` templating, which the scope allowlists render at dispatch (see
// internal/flow/resources.go and docs/design/scope-templating.md).
//
// Only the `settings.` prefix is substituted, so a step's shell `${VAR}` and
// the loader's own `${ENV_VAR}` expansion are untouched (both lack the dot).
var settingsRefRE = regexp.MustCompile(`\$\{settings\.([A-Za-z0-9_.-]+)\}`)

// envRefRE matches the `${env.NAME}` form a SETTING VALUE may use to take its
// value from the process environment (conductor.env is loaded into the
// environment by the CLI before the config is read, so a value parked there
// works with no extra wiring). It is deliberately only legal inside a setting
// value: the config body keeps the loader's plain `${NAME}` expansion.
var envRefRE = regexp.MustCompile(`\$\{env\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// maxSettingPasses bounds the resolve/substitute iteration so a setting that
// references itself terminates instead of looping.
const maxSettingPasses = 8

// substituteSettingsBody replaces every `${settings.NAME}` in a config body,
// iterating so a setting whose VALUE itself contains a reference resolves too.
// An unknown name is LEFT IN PLACE — it is caught after decode, by
// unknownSettingRefs, so a comment that merely mentions the syntax doesn't
// fail a load while a real field that needs it does.
func substituteSettingsBody(body []byte, settings map[string]string) []byte {
	if len(settings) == 0 || !strings.Contains(string(body), "${settings.") {
		return body
	}
	sub := body
	for i := 0; i < maxSettingPasses; i++ {
		next := settingsRefRE.ReplaceAllFunc(sub, func(m []byte) []byte {
			name := string(settingsRefRE.FindSubmatch(m)[1])
			if val, ok := settings[name]; ok {
				return []byte(val)
			}
			return m
		})
		if string(next) == string(sub) {
			break
		}
		sub = next
	}
	return sub
}

// unknownSettingRefs lists the `${settings.NAME}` references still present in a
// DECODED body (marshal the decoded struct, so comments are gone) whose name no
// setting declares — the typo, and the "I forgot to declare it" case. Returns
// them sorted and de-duplicated.
func unknownSettingRefs(body []byte, settings map[string]string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, m := range settingsRefRE.FindAllSubmatch(body, -1) {
		name := string(m[1])
		if _, declared := settings[name]; declared || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

// resolveSettingValues turns a declared `settings:` block into the flat
// name→value map the substitutor takes, resolving each value in order:
//
//	${env.NAME}        the process environment (conductor.env included, since
//	                   the CLI loads it into the environment before this runs)
//	${settings.other}  another setting, chained, bounded by maxSettingPasses
//
// An empty result is an ERROR, not an empty string: a setting that silently
// expands to nothing turns `channel: ["${settings.review_channel}"]` into an
// allowlist entry matching nothing, and the operator finds out from a refusal
// at 3am rather than from the load.
func resolveSettingValues(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	names := make([]string, 0, len(raw))
	for k, v := range raw {
		out[k] = v
		names = append(names, k)
	}
	sort.Strings(names) // deterministic error order
	// Environment first: a value is a literal or an ${env.X} pull, never a
	// shell expansion.
	var missingEnv []string
	for _, name := range names {
		var miss []string
		out[name] = envRefRE.ReplaceAllStringFunc(out[name], func(m string) string {
			ev := envRefRE.FindStringSubmatch(m)[1]
			v, ok := os.LookupEnv(ev)
			if !ok {
				miss = append(miss, fmt.Sprintf("%s (settings.%s)", ev, name))
			}
			return v
		})
		missingEnv = append(missingEnv, miss...)
	}
	if len(missingEnv) > 0 {
		return nil, fmt.Errorf("settings: undefined environment variable(s): %s — define them in conductor.env or the environment",
			strings.Join(missingEnv, ", "))
	}
	// Then chaining, so `${settings.a}` inside another setting's value works.
	for i := 0; i < maxSettingPasses; i++ {
		changed := false
		for _, name := range names {
			next := string(substituteSettingsBody([]byte(out[name]), out))
			if next != out[name] {
				out[name], changed = next, true
			}
		}
		if !changed {
			break
		}
	}
	var empty []string
	for _, name := range names {
		if strings.TrimSpace(out[name]) == "" {
			empty = append(empty, name)
		}
	}
	if len(empty) > 0 {
		return nil, fmt.Errorf("settings: %s resolved to an empty value — a setting that expands to nothing silently empties whatever field references it; give it a value, or drop it",
			strings.Join(empty, ", "))
	}
	return out, nil
}

// settingsFromDoc reads a parsed config document's top-level `settings:` block
// into the flat map, without a strict decode (this runs BEFORE the decode the
// substitution feeds).
func settingsFromDoc(doc map[string]any) (map[string]string, error) {
	blk, ok := doc["settings"]
	if !ok || blk == nil {
		return nil, nil
	}
	m, ok := blk.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings: want a map of name -> value, got %T", blk)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		name := strings.TrimSpace(k)
		if name == "" {
			return nil, fmt.Errorf("settings: empty setting name")
		}
		out[name] = stringifySetting(v)
	}
	return out, nil
}

// ExpandSettings resolves a single config document's own `settings:` block and
// substitutes every `${settings.NAME}` in it. It is the whole mechanism for a
// one-file config; Load uses the same pieces across an import graph.
//
// Exported so a caller that builds a Config from a body rather than from disk
// (tests, `conductor config` tooling) gets the same parameterization a real
// load gives, instead of a subtly different config.
func ExpandSettings(body []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	declared, err := settingsFromDoc(doc)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSettingValues(declared)
	if err != nil {
		return nil, err
	}
	return substituteSettingsBody(body, resolved), nil
}

// checkSettingRefs reports `${settings.X}` references that survived into the
// DECODED config and name nothing declared. Called after the decode, on the
// re-marshaled struct, so only real fields count.
func checkSettingRefs(c *Config) error {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil // a config that won't marshal has bigger problems; not ours to report
	}
	missing := unknownSettingRefs(body, c.Settings)
	if len(missing) == 0 {
		return nil
	}
	declared := "none"
	if len(c.Settings) > 0 {
		names := make([]string, 0, len(c.Settings))
		for k := range c.Settings {
			names = append(names, k)
		}
		sort.Strings(names)
		declared = strings.Join(names, ", ")
	}
	return fmt.Errorf("config references undeclared setting(s): %s — add them to the top-level settings: block (declared: %s)",
		strings.Join(missing, ", "), declared)
}
