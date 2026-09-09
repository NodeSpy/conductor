package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// cmdInit resolves everything declared in `packs:` (fetch sources, recurse
// dependencies), writes the sha-pinned lockfile, and prints a Terraform-init /
// plan-style preview of what the packs add. This is the only network step; the
// daemon then loads offline from the vendored packs + lockfile.
func cmdInit(args []string) error {
	path, _ := configPath(args)
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))

	lock, err := config.ResolvePacks(path)
	if err != nil {
		return err
	}
	if len(lock.Packs) == 0 {
		fmt.Println("no packs: block — nothing to initialize")
		return nil
	}
	fmt.Printf("resolved %d pack(s) into %s\n", len(lock.Packs), config.LockfileName)
	for _, e := range lock.Packs {
		fmt.Printf("  %-24s %s@%s (%s)\n", e.Instance, e.Name, orNone(e.Version), e.Resolved)
	}
	fmt.Println()
	// Load the config so the instantiated effect can be previewed.
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("packs resolved, but loading the instantiated config failed: %w", err)
	}
	printPackPlan(cfg)
	fmt.Println("\nnext: arm a pack trigger (enabled + repos) in your config, then `conductor validate`")
	return nil
}

// cmdPack is the author/operator surface: list | plan | lint | show.
func cmdPack(args []string) error {
	rest := positional(args)
	if len(rest) == 0 {
		return fmt.Errorf("usage: conductor pack <list|plan|lint|show> [args]")
	}
	switch rest[0] {
	case "list":
		return cmdPackList(args)
	case "plan":
		return cmdPackPlan(args)
	case "lint":
		return cmdPackLint(rest[1:])
	case "show":
		return cmdPackShow(rest[1:])
	default:
		return fmt.Errorf("unknown pack subcommand %q (list|plan|lint|show)", rest[0])
	}
}

// cmdPackList lists the configured pack instances and their lock status.
func cmdPackList(args []string) error {
	path, _ := configPath(args)
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	if len(cfg.Packs) == 0 {
		fmt.Println("no packs configured")
		return nil
	}
	lock, _ := config.ReadLockfile(filepath.Dir(path))
	locked := map[string]config.LockEntry{}
	if lock != nil {
		for _, e := range lock.Packs {
			locked[e.Instance] = e
		}
	}
	names := make([]string, 0, len(cfg.Packs))
	for n := range cfg.Packs {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("pack instances:")
	for _, n := range names {
		inst := cfg.Packs[n]
		status := "NOT INITIALIZED — run `conductor init`"
		if e, ok := locked[n]; ok {
			status = fmt.Sprintf("%s (%s)", e.Name, e.Resolved)
		}
		fmt.Printf("  %-20s %s  [%s]\n", n, inst.Source, status)
	}
	return nil
}

// cmdPackPlan previews the effect of the configured packs on the effective
// config: the namespaced agents/workflows/checks added, skill grants (loudly),
// and each trigger's armed/disarmed state and repo scope.
func cmdPackPlan(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	if len(cfg.Packs) == 0 {
		fmt.Println("no packs configured")
		return nil
	}
	printPackPlan(cfg)
	return nil
}

// printPackPlan renders the pack-contributed surface of an already-loaded config.
func printPackPlan(cfg *config.Config) {
	instances := make([]string, 0, len(cfg.Packs))
	for n := range cfg.Packs {
		instances = append(instances, n)
	}
	sort.Strings(instances)

	for _, ns := range instances {
		fmt.Printf("pack %q adds:\n", ns)
		prefix := ns + "/"

		var agents, workflows, checks []string
		for name := range cfg.Agents {
			if strings.HasPrefix(name, prefix) {
				grant := ""
				if p := cfg.Agents[name]; p.Skill != nil {
					if len(p.Skill.Verbs) > 0 {
						grant += "  !! grants skill: " + strings.Join(p.Skill.Verbs, ", ")
					}
					if len(p.Skill.AllowSecrets) > 0 {
						grant += "  !! may read secrets: " + strings.Join(p.Skill.AllowSecrets, ", ")
					}
				}
				agents = append(agents, name+grant)
			}
		}
		for name := range cfg.Workflows {
			if strings.HasPrefix(name, prefix) {
				workflows = append(workflows, name)
			}
		}
		for name := range cfg.Checks {
			if strings.HasPrefix(name, prefix) {
				checks = append(checks, name)
			}
		}
		sort.Strings(agents)
		sort.Strings(workflows)
		sort.Strings(checks)
		printList("agents", agents)
		printList("workflows", workflows)
		printList("checks", checks)

		fmt.Println("  triggers:")
		for _, tr := range cfg.Triggers {
			if !strings.HasPrefix(tr.Name, prefix) {
				continue
			}
			armed := "DISARMED (inert)"
			if tr.Enabled != nil && *tr.Enabled {
				repos := "no repos — matches nothing"
				if r, ok := tr.Filters["repos"]; ok {
					repos = "repos: " + fmt.Sprintf("%v", r)
				}
				armed = "ARMED — " + repos
			}
			fmt.Printf("    %-28s on:%s  [%s]\n", tr.Name, tr.On, armed)
		}
	}
	for _, w := range cfg.PackWarnings() {
		fmt.Printf("warning: %s\n", w)
	}
}

// cmdPackLint validates a pack at a local path is well-formed (§18).
func cmdPackLint(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: conductor pack lint <pack-dir>")
	}
	dir := args[0]
	man, err := config.LoadPackManifestForLint(dir)
	if err != nil {
		return err
	}
	problems := config.LintPackManifest(man)
	if len(problems) == 0 {
		fmt.Printf("ok: pack %q v%s is well-formed\n", man.Pack.Name, man.Pack.Version)
		return nil
	}
	for _, p := range problems {
		fmt.Printf("  - %s\n", p)
	}
	return fmt.Errorf("%d lint problem(s)", len(problems))
}

// cmdPackShow renders a pack's docs: metadata, requires, settings, presets,
// exports, and an example arming snippet (§23).
func cmdPackShow(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: conductor pack show <pack-dir>")
	}
	man, err := config.LoadPackManifestForLint(args[0])
	if err != nil {
		return err
	}
	m := man.Pack
	fmt.Printf("%s v%s\n", m.Name, m.Version)
	if m.Description != "" {
		fmt.Printf("  %s\n", m.Description)
	}
	if len(m.Tags) > 0 {
		fmt.Printf("  tags: %s\n", strings.Join(m.Tags, ", "))
	}
	if m.License != "" {
		fmt.Printf("  license: %s\n", m.License)
	}
	if m.Homepage != "" {
		fmt.Printf("  homepage: %s\n", m.Homepage)
	}
	if m.Deprecated != "" {
		fmt.Printf("  DEPRECATED: %s\n", m.Deprecated)
	}
	fmt.Println("\nrequires (you bind these):")
	if m.Requires.Conductor != "" {
		fmt.Printf("  conductor: %s\n", m.Requires.Conductor)
	}
	if len(m.Requires.Connectors) > 0 {
		fmt.Printf("  connectors: %s\n", strings.Join(m.Requires.Connectors, ", "))
	}
	if len(m.Requires.Stores) > 0 {
		fmt.Printf("  stores: %s\n", strings.Join(m.Requires.Stores, ", "))
	}
	for name, s := range m.Requires.Secrets {
		fmt.Printf("  secret %s: %s\n", name, s.Desc)
	}
	for role, r := range m.Requires.Roles {
		if len(r.Skill) > 0 {
			fmt.Printf("  role %s (needs skill: %s)\n", role, strings.Join(r.Skill, ", "))
		} else {
			fmt.Printf("  role %s\n", role)
		}
	}
	if len(man.Settings) > 0 {
		fmt.Println("\nsettings (override in the instance block):")
		names := make([]string, 0, len(man.Settings))
		for n := range man.Settings {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := man.Settings[n]
			fmt.Printf("  %-16s %v  %s\n", n, s.Default, s.Desc)
		}
	}
	if len(man.Presets) > 0 {
		names := make([]string, 0, len(man.Presets))
		for n := range man.Presets {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("\npresets: %s\n", strings.Join(names, ", "))
	}
	if len(man.Exports.Workflows) > 0 || len(man.Exports.Agents) > 0 {
		fmt.Println("\nexports (public, reference by qualified name):")
		for _, w := range man.Exports.Workflows {
			fmt.Printf("  workflow: <instance>/%s\n", w)
		}
	}
	fmt.Println("\nexample:")
	fmt.Printf("  packs:\n    %s:\n      source: <source>\n", m.Name)
	if len(m.Requires.Connectors) > 0 {
		fmt.Printf("      connectors: { %s: <your-connector> }\n", m.Requires.Connectors[0])
	}
	return nil
}

func printList(label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Printf("  %s:\n", label)
	for _, it := range items {
		fmt.Printf("    %s\n", it)
	}
}

func orNone(s string) string {
	if s == "" {
		return "unpinned"
	}
	return s
}
