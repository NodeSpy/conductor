package config

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// LoadPackManifestForLint reads and strict-decodes a pack manifest from a local
// directory WITHOUT settings substitution — so `conductor pack lint`/`show` see
// the authored form (including ${settings.NAME} placeholders).
func LoadPackManifestForLint(dir string) (*PackManifest, error) {
	return loadPackManifest(dir)
}

// LintPackDir is a convenience for tests/tools: load + lint a pack directory.
func LintPackDir(dir string) ([]string, error) {
	man, err := LoadPackManifestForLint(dir)
	if err != nil {
		return nil, err
	}
	return LintPackManifest(man), nil
}

// LintPackManifest checks a pack is well-formed (§18) and returns a list of
// human-readable problems (empty when clean). It validates: identity present,
// no bind-only sections shipped, requires.conductor declared, exports
// resolve, skill grants stay inside requires.connectors, triggers are named,
// and every ${settings.NAME} reference is a declared setting.
func LintPackManifest(man *PackManifest) []string {
	var problems []string

	if man.Pack.Name == "" {
		problems = append(problems, "pack.name is required")
	}
	if man.Pack.Version == "" {
		problems = append(problems, "pack.version is required")
	}
	if man.Pack.Requires.Conductor == "" {
		problems = append(problems, "requires.conductor is required (the auto-updating fleet needs a daemon-version constraint)")
	}
	if err := man.checkNoEnvironment(); err != nil {
		problems = append(problems, err.Error())
	}

	// Exports must resolve to defined workflows/agents.
	for _, w := range man.Exports.Workflows {
		if _, ok := man.Workflows[w]; !ok {
			problems = append(problems, fmt.Sprintf("exports.workflows: %q names no workflow defined by the pack", w))
		}
	}
	for _, a := range man.Exports.Steps {
		if _, ok := man.Steps[a]; !ok {
			problems = append(problems, fmt.Sprintf("exports.agents: %q names no agent defined by the pack", a))
		}
	}

	// requires.roles should correspond to a bundled agent (so it can be
	// defaulted / overridden / bound).
	for role := range man.Pack.Requires.Roles {
		if _, ok := man.Steps[role]; !ok {
			problems = append(problems, fmt.Sprintf("requires.roles: %q has no bundled agent of that name", role))
		}
	}

	// Shipped triggers must be named so a consumer can arm them.
	for i, tr := range man.Triggers {
		if tr.Name == "" {
			problems = append(problems, fmt.Sprintf("triggers[%d]: a shipped trigger must have a name (so it can be armed)", i))
		}
	}

	// A pack may only grant its agents access to connectors it DECLARED
	// (§C): requires.connectors is the capability boundary, not just a list
	// of sockets.
	problems = append(problems, lintPackSkillGrants(man)...)

	problems = append(problems, lintSettingsRefs(man)...)
	sort.Strings(problems)
	return problems
}

// lintSettingsRefs reports any ${settings.NAME} in the manifest body that is not
// a declared setting. Placeholders live in string fields (prompts, options), so
// they survive a re-marshal of the decoded manifest.
func lintSettingsRefs(man *PackManifest) []string {
	body, err := yaml.Marshal(man)
	if err != nil {
		return nil
	}
	var problems []string
	seen := map[string]bool{}
	for _, m := range settingsRefRE.FindAllSubmatch(body, -1) {
		name := string(m[1])
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := man.Settings[name]; !ok {
			problems = append(problems, fmt.Sprintf("references ${settings.%s} but does not declare it under settings:", name))
		}
	}
	return problems
}
