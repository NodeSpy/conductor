package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// cmdInit resolves everything declared in `packs:` (fetch sources, recurse
// dependencies), writes the sha-pinned lockfile, and prints a Terraform-init /
// plan-style preview of what the packs add. This is the only network step; the
// daemon then loads offline from the vendored packs + lockfile.
func cmdInit(args []string) error {
	path, rest := configPath(args)
	allowUnlisted := false
	for _, a := range rest {
		if a == "--allow-unlisted" {
			allowUnlisted = true
		}
	}
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))

	resolve := config.ResolvePacks
	if allowUnlisted {
		resolve = config.ResolvePacksAllowingUnlisted
	}
	lock, err := resolve(path)
	if err != nil {
		return err
	}
	// Resolve remote plugins (release-asset fetch) in the same step.
	nPlugins, err := resolvePluginsForInit(path, allowUnlisted)
	if err != nil {
		return err
	}
	if len(lock.Packs) == 0 && nPlugins == 0 {
		fmt.Println("no packs: or remote plugins: block — nothing to initialize")
		return nil
	}
	if len(lock.Packs) > 0 {
		fmt.Printf("resolved %d pack(s) into %s\n", len(lock.Packs), config.LockfileName)
		for _, e := range lock.Packs {
			fmt.Printf("  %-24s %s@%s (%s)\n", e.Instance, e.Name, orNone(e.Version), e.Resolved)
		}
		fmt.Println()
	}
	// Load the config so the instantiated effect can be previewed.
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("resolved, but loading the config failed: %w", err)
	}
	if len(lock.Packs) > 0 {
		printPackPlan(cfg)
	}
	fmt.Println("\nnext: arm a pack trigger (enabled + repos) in your config, then `conductor validate`")
	return nil
}

// resolvePluginsForInit reconciles local install state against every plugin the
// config references (`use:` that did not resolve to a builtin): fetches what is
// missing, re-resolves what is not pinned, records each permission manifest, and
// prints a line per plugin. Returns the count it touched.
//
// This is the app-extension "declare it and it is there" step: the operator
// wrote `use: sentry` and ran `conductor init`; everything else is conductor's
// job.
func resolvePluginsForInit(path string, allowUnlisted bool) (int, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return 0, err
	}
	results, err := reconcilePlugins(cfg, plugin.Options{AllowUnlisted: allowUnlisted, Log: logf})
	if err != nil {
		return 0, err
	}
	if len(results) > 0 {
		if perr := printResolutions(results); perr != nil {
			return len(results), perr
		}
		fmt.Println()
	}
	return len(results), nil
}

// shortSha abbreviates a hex sha for a preview line.
func shortSha(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
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
	case "add":
		return cmdPackAdd(args)
	case "remove", "rm":
		return cmdPackRemove(args)
	case "update":
		return cmdPackUpdate(args)
	default:
		return fmt.Errorf("unknown pack subcommand %q (list|plan|lint|show|add|remove|update)", rest[0])
	}
}

// cmdPackAdd fetches a pack source and prints an install review plus a
// ready-to-paste `packs:` block — WITHOUT mutating the config (§14). The
// operator pastes the block, binds/arms it, then runs `conductor init`.
func cmdPackAdd(args []string) error {
	path, rest := configPath(args)
	// rest[0] is "add"; the source follows.
	pos := positional(rest)
	if len(pos) < 2 {
		return fmt.Errorf("usage: conductor pack add <source>")
	}
	source := pos[1]
	man, err := config.FetchPackForReview(source, filepath.Dir(path))
	if err != nil {
		return err
	}
	m := man.Pack
	fmt.Printf("%s v%s — %s\n", m.Name, m.Version, m.Description)
	fmt.Println("\ninstall review:")
	man.WalkPackSteps(func(where string, st *config.Step) {
		if s := st.Skill; s != nil && (len(s.Verbs) > 0 || len(s.AllowSecrets) > 0) {
			fmt.Printf("  step %s", where)
			if len(s.Verbs) > 0 {
				fmt.Printf("  !! skill: %s", strings.Join(s.Verbs, ", "))
			}
			if len(s.AllowSecrets) > 0 {
				fmt.Printf("  !! secrets: %s", strings.Join(s.AllowSecrets, ", "))
			}
			fmt.Println()
		}
	})
	for _, tr := range man.Triggers {
		fmt.Printf("  ships trigger %q on:%s (disarmed — you arm it)\n", tr.Name, tr.On)
	}
	// Ready-to-paste block.
	fmt.Println("\nadd to your config's packs: block, then `conductor init`:")
	inst := m.Name
	fmt.Printf("  %s:\n    source: %s\n", inst, source)
	if m.Version != "" {
		fmt.Printf("    version: %s\n", m.Version)
	}
	for _, c := range m.Requires.Connectors.Names() {
		fmt.Printf("    connectors: { %s: <your-connector> }\n", c)
	}
	for _, s := range m.Requires.Stores {
		fmt.Printf("    stores: { %s: <your-store> }\n", s)
	}
	for name := range m.Requires.Secrets {
		fmt.Printf("    secrets: { %s: <your-secret-or-vault-ref> }\n", name)
	}
	for _, tr := range man.Triggers {
		fmt.Printf("    triggers: { %s: { enabled: true, repos: [your-org/repo] } }\n", tr.Name)
	}
	return nil
}

// cmdPackRemove clears a pack instance's vendored tree and lockfile entries.
func cmdPackRemove(args []string) error {
	path, rest := configPath(args)
	pos := positional(rest)
	if len(pos) < 2 {
		return fmt.Errorf("usage: conductor pack remove <instance>")
	}
	inst := pos[1]
	dir := filepath.Dir(path)
	vendor := config.PackVendorDir(dir)
	if err := os.RemoveAll(filepath.Join(vendor, inst)); err != nil {
		return err
	}
	if lock, _ := config.ReadLockfile(dir); lock != nil {
		kept := lock.Packs[:0]
		removed := 0
		for _, e := range lock.Packs {
			if e.Instance == inst || strings.HasPrefix(e.Instance, inst+"/") {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		lock.Packs = kept
		if err := config.WriteLockfileTo(dir, lock); err != nil {
			return err
		}
		fmt.Printf("removed %d lockfile entr(ies) and the vendored tree for %q\n", removed, inst)
	}
	fmt.Printf("note: also remove the `packs.%s:` block from your config so it is not re-fetched\n", inst)
	return nil
}

// cmdPackUpdate re-resolves the packs: block and prints the lockfile diff.
func cmdPackUpdate(args []string) error {
	path, rest := configPath(args)
	allowUnlisted := false
	for _, a := range rest {
		if a == "--allow-unlisted" {
			allowUnlisted = true
		}
	}
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))
	old, _ := config.ReadLockfile(filepath.Dir(path))
	resolve := config.ResolvePacks
	if allowUnlisted {
		resolve = config.ResolvePacksAllowingUnlisted
	}
	next, err := resolve(path)
	if err != nil {
		return err
	}
	printLockDiff(old, next)
	return nil
}

func printLockDiff(old, next *config.Lockfile) {
	oldBy := map[string]config.LockEntry{}
	if old != nil {
		for _, e := range old.Packs {
			oldBy[e.Instance] = e
		}
	}
	changes := 0
	for _, e := range next.Packs {
		prev, existed := oldBy[e.Instance]
		switch {
		case !existed:
			fmt.Printf("  + %s %s@%s (%s)\n", e.Instance, e.Name, orNone(e.Version), e.Resolved)
			changes++
		case prev.Resolved != e.Resolved || prev.Digest != e.Digest:
			fmt.Printf("  ~ %s %s: %s -> %s\n", e.Instance, e.Name, short(prev.Resolved), short(e.Resolved))
			changes++
		}
		delete(oldBy, e.Instance)
	}
	for inst, e := range oldBy {
		fmt.Printf("  - %s %s (removed)\n", inst, e.Name)
		changes++
	}
	if changes == 0 {
		fmt.Println("packs are up to date (no changes)")
	} else {
		fmt.Printf("%d change(s) written to %s\n", changes, config.LockfileName)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func sortedAgentNames(m map[string]config.Step) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
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

		// A pack's steps live in its workflows now, so its grants are
		// reported per addressable step rather than per registry entry.
		var agents, workflows, checks []string
		for name := range cfg.Workflows {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			wf := cfg.Workflows[name]
			for i := range wf.Steps {
				p := wf.Steps[i]
				if p.Skill == nil {
					continue
				}
				grant := ""
				if len(p.Skill.Verbs) > 0 {
					grant += "  !! grants skill: " + strings.Join(p.Skill.Verbs, ", ")
				}
				if len(p.Skill.AllowSecrets) > 0 {
					grant += "  !! may read secrets: " + strings.Join(p.Skill.AllowSecrets, ", ")
				}
				if grant != "" {
					agents = append(agents, name+"/"+config.StepSlot(p, i)+grant)
				}
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
	for _, n := range m.Requires.Connectors.Names() {
		c := m.Requires.Connectors[n]
		note := ""
		if !c.Required {
			note = "  (optional — its triggers go dormant if unbound)"
		}
		if c.Version != "" && c.Version != config.AnyVersion {
			fmt.Printf("  connector %s: %s%s\n", n, c.Version, note)
		} else {
			fmt.Printf("  connector %s%s\n", n, note)
		}
	}
	if len(m.Requires.Stores) > 0 {
		fmt.Printf("  stores: %s\n", strings.Join(m.Requires.Stores, ", "))
	}
	for name, s := range m.Requires.Secrets {
		fmt.Printf("  secret %s: %s\n", name, s.Desc)
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
	if len(man.Exports.Workflows) > 0 || len(man.Exports.Steps) > 0 {
		fmt.Println("\nexports (public, reference by qualified name):")
		for _, w := range man.Exports.Workflows {
			fmt.Printf("  workflow: <instance>/%s\n", w)
		}
	}
	fmt.Println("\nexample:")
	fmt.Printf("  packs:\n    %s:\n      source: <source>\n", m.Name)
	if names := m.Requires.Connectors.Names(); len(names) > 0 {
		fmt.Printf("      connectors: { %s: <your-connector> }\n", names[0])
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
