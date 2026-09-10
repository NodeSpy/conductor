package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// pluginDeps assembles the shared plugin.Deps: the redactor, the audit sink,
// and the sandbox wiring. The sandbox wiring is only consumed when a connector
// carries an OPTIONAL isolation: block — the default path is the permission
// manifest, not OS confinement — but the egress proxy is also what enforces a
// plugin's declared network, so it is wired either way.
func pluginDeps(sec *secrets.Resolver, audit func(map[string]any)) plugin.Deps {
	exe, _ := os.Executable()
	egressUnix, egressAddr := controller.EgressProxyUnix, controller.EgressProxyFor
	if egressUnix == nil || egressAddr == nil {
		pm := sandbox.NewProxyManager(func(key, hostport string) {
			logf("plugin egress denied: %s -> %s", key, hostport)
		})
		egressUnix, egressAddr = pm.UnixEndpoint, pm.Endpoint
	}
	masks := controller.DaemonMaskPaths
	if len(masks) == 0 {
		masks = []string{config.StateDir(), configDir()}
	}
	return plugin.Deps{
		Log:    logf,
		Redact: sec.Redact,
		Audit:  audit,
		Sandbox: plugin.SandboxDeps{
			Self:       exe,
			MaskPaths:  masks,
			EgressUnix: egressUnix,
			EgressAddr: egressAddr,
		},
	}
}

// pluginManagerFor builds the plugin manager for a loaded config, joining the
// config's DERIVED plugin set (non-builtin `use:` references) with local install
// state. It touches no network: install state is read offline.
func pluginManagerFor(cfg *config.Config, sec *secrets.Resolver, audit func(map[string]any)) *plugin.Manager {
	state := plugin.LoadInstallState(plugin.InstallDir())
	return plugin.NewManager(cfg.PluginRefs(), cfg.BaseDir(), state, pluginDeps(sec, audit))
}

// loadConnectorPlugins builds the plugin manager for the config and registers
// every connector-kind plugin's type into the connector registry (verify →
// spawn → describe → register). Fail-closed: any load failure returns an error.
// Returns a manager the caller must Close.
func loadConnectorPlugins(cfg *config.Config, sec *secrets.Resolver, audit func(map[string]any)) (*plugin.Manager, error) {
	mgr := pluginManagerFor(cfg, sec, audit)
	// Bound the boot phase: verify+spawn+describe must not hang forever (a
	// stalled binary read or sandbox preflight) with no deadline.
	ctx, cancel := context.WithTimeout(context.Background(), pluginBootTimeout)
	defer cancel()
	var registered []string
	rollback := func() {
		for _, t := range registered {
			connector.UnregisterExternalType(t)
		}
		mgr.Close()
	}
	for _, spec := range mgr.ConnectorSpecs() {
		decl, err := mgr.StartAndDescribe(ctx, spec.Key())
		if err != nil {
			rollback()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		// KIND ENFORCEMENT at the point of use: whatever install state recorded,
		// the running binary must still describe itself as a connector. A
		// runtime EXECUTES agents — accepting one here would silently escalate
		// what the operator agreed to.
		if decl.Kind != "" && decl.Kind != plugin.KindConnector {
			rollback()
			return nil, fmt.Errorf("plugin %s: declared under connectors: but it describes itself as a %s — a %s cannot be wired as a connector", spec.Name, decl.Kind, decl.Kind)
		}
		// CAN'T-EXCEED-DECLARATION: every instance's `network:` must be covered
		// by what the plugin says it needs. A config that widens a plugin's
		// declared egress is a config error, not a silent grant.
		for cname, cref := range cfg.ConnectorsMap {
			if cref.TypeName() != spec.Name || len(cref.Network) == 0 {
				continue
			}
			if err := plugin.CheckNetworkWithinManifest(cname, decl.Capabilities.Egress, cref.Network); err != nil {
				rollback()
				return nil, err
			}
		}
		cl, _ := mgr.Client(spec.Key())
		if _, err := connector.RegisterExternalConnector(cl, spec, decl); err != nil {
			rollback()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		registered = append(registered, spec.Provides)
		logf("plugin %s: registered connector type %q (%d verb(s)); permissions: %s",
			spec.Ref(), spec.Provides, len(decl.Verbs), spec.EffectiveManifest().Summary())
	}
	return mgr, nil
}

// pluginBootTimeout bounds a single connector plugin's verify+spawn+describe at
// boot, so a stalled plugin can't hang daemon startup indefinitely.
const pluginBootTimeout = 30 * time.Second

// pluginRuntimeControllers verifies each runtime-kind plugin (verify-before-
// execute, fail-closed) and synthesizes it into a ControllerConfig the existing
// controller registry drives as an ACP subprocess — generalizing the shipped
// ACP runtime (#54 §4). A runtime plugin is the highest-stakes plugin: it
// EXECUTES agents, which is exactly the privilege the operator already granted
// conductor, so it is NOT wrapped in a heavy sandbox by default.
//
// The binary is re-verified on EVERY spawn via the `conductor plugin-exec`
// wrapper, so the recorded sha holds for the daemon's whole lifetime, not just
// at boot.
//
// Their environment IS scrubbed — ScrubEnv (set below) makes acp.go's spawnACP
// seed the child from sandbox.MinimalEnv() instead of the daemon's os.Environ(),
// so a runtime plugin does not inherit env:-resolved secrets.
func pluginRuntimeControllers(cfg *config.Config) (map[string]config.ControllerConfig, error) {
	out := map[string]config.ControllerConfig{}
	state := plugin.LoadInstallState(plugin.InstallDir())
	refs := cfg.PluginRefs()
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref := refs[key]
		if ref.Kind() != config.PluginKindRuntime {
			continue
		}
		inst, ok := state.Get(key)
		spec := plugin.SpecFromRef(ref, cfg.BaseDir(), inst, ok)
		if !spec.Installed() {
			return nil, spec.NotInstalledError()
		}
		if err := plugin.VerifyOnly(spec); err != nil {
			return nil, fmt.Errorf("runtime plugin %s: %w", ref.Name, err)
		}
		// Route the ACP launch through `conductor plugin-exec`, which re-verifies
		// the binary's sha on EVERY spawn (the ACP controller re-spawns per
		// session) before exec'ing it — closing the boot-only verification window.
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("runtime plugin %s: cannot resolve conductor binary for re-verify wrapper: %w", ref.Name, err)
		}
		wrap := []string{self, "plugin-exec", "--sha", spec.Sha256}
		if spec.Local {
			wrap = append(wrap, "--local")
		}
		wrap = append(wrap, "--", spec.BinPath)
		out[ref.Name] = config.ControllerConfig{
			Transport: "acp",
			Command:   wrap,
			Isolation: ref.Isolation,
			ScrubEnv:  true, // third-party code: minimal env, no daemon secrets (#54 §8.1)
		}
		logf("plugin %s: registered runtime %q (acp, per-spawn re-verified, env-scrubbed); permissions: %s",
			spec.Ref(), ref.Name, spec.EffectiveManifest().Summary())
	}
	return out, nil
}

// mergedControllersWithPlugins is cfg.MergedControllers() plus verified runtime
// plugins. A plugin runtime's ControllerConfig REPLACES the placeholder
// MergedControllers derived from its runtimes: entry (which carries no builtin
// type, because the implementation is the plugin binary).
func mergedControllersWithPlugins(cfg *config.Config) (map[string]config.ControllerConfig, error) {
	merged := cfg.MergedControllers()
	prt, err := pluginRuntimeControllers(cfg)
	if err != nil {
		return nil, err
	}
	for name, cc := range prt {
		// Carry through the fields the runtimes: entry contributed (host,
		// session model, default flag) — the plugin supplies the launch recipe,
		// not the placement.
		if prev, ok := merged[name]; ok {
			cc.SessionModel, cc.Default, cc.Host = prev.SessionModel, prev.Default, prev.Host
			if cc.Isolation == nil {
				cc.Isolation = prev.Isolation
			}
		}
		merged[name] = cc
	}
	return merged, nil
}

// cmdPluginExec is the hidden re-verify-then-exec wrapper a runtime plugin's
// ACP launch is routed through (see pluginRuntimeControllers). It re-checks the
// binary's SHA-256 against the recorded value — from a safe path — on every
// spawn, then replaces itself with the plugin via exec. Usage:
//
//	conductor plugin-exec --sha <hex> [--local] -- <binPath>
func cmdPluginExec(args []string) error {
	var sha string
	var local bool
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--sha":
			if i+1 >= len(args) {
				return fmt.Errorf("plugin-exec: --sha needs a value")
			}
			sha, i = args[i+1], i+2
		case "--local":
			local, i = true, i+1
		case "--":
			i++
			goto rest
		default:
			return fmt.Errorf("plugin-exec: unexpected arg %q", args[i])
		}
	}
rest:
	rest := args[i:]
	if len(rest) == 0 {
		return fmt.Errorf("plugin-exec: missing -- <binPath>")
	}
	bin := rest[0]
	// Re-verify (sha + safe perms) BEFORE exec — the per-spawn TOCTOU close.
	if err := plugin.VerifyOnly(plugin.Spec{
		Name: "runtime", Kind: plugin.KindRuntime, BinPath: bin,
		Sha256: sha, Local: local,
	}); err != nil {
		return err
	}
	// Replace this process with the verified plugin, inheriting stdio+env.
	return syscall.Exec(bin, rest, os.Environ())
}

// cmdPlugin implements `conductor plugin list|show|add|update|remove`.
func cmdPlugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: conductor plugin list|show <name>|add <ref>|update [name]|remove <name>")
	}
	switch args[0] {
	case "list", "ls":
		return cmdPluginList(args[1:])
	case "show":
		return cmdPluginShow(args[1:])
	case "add":
		return cmdPluginAdd(args[1:])
	case "remove", "rm":
		return cmdPluginRemove(args[1:])
	case "update":
		return cmdPluginUpdate(args[1:])
	default:
		return fmt.Errorf("unknown plugin subcommand %q (list|show|add|update|remove)", args[0])
	}
}

// cmdPluginList shows every connector and runtime available to this config —
// bundled and plugin-backed alike — with the ORIGIN each resolved from, so
// "where does this come from?" is answerable at a glance. It executes nothing.
func cmdPluginList(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	caps := false
	for _, a := range rest {
		if a == "--caps" || a == "--permissions" {
			caps = true
		}
	}
	state := plugin.LoadInstallState(plugin.InstallDir())

	fmt.Printf("%-18s %-10s %-9s %-14s %s\n", "NAME", "KIND", "ORIGIN", "VERSION", "STATUS")
	for _, t := range connector.Types() {
		if connector.IsExternalType(t) {
			continue
		}
		fmt.Printf("%-18s %-10s %-9s %-14s %s\n", t, "connector", "builtin", version, "bundled")
	}
	for _, r := range config.BuiltinNames(config.UseKindRuntime) {
		fmt.Printf("%-18s %-10s %-9s %-14s %s\n", r, "runtime", "builtin", version, "bundled")
	}

	refs := cfg.PluginRefs()
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref := refs[key]
		inst, ok := state.Get(key)
		spec := plugin.SpecFromRef(ref, cfg.BaseDir(), inst, ok)
		ver := inst.Resolved
		if spec.Local {
			ver = "local"
		} else if ver == "" {
			ver = "-"
		}
		fmt.Printf("%-18s %-10s %-9s %-14s %s\n", ref.Name, ref.Kind(), ref.Use.Origin, ver, pluginStatus(spec))
		if caps {
			fmt.Printf("%-18s   use: %s\n", "", ref.Use.String())
			fmt.Printf("%-18s   permissions: %s\n", "", spec.EffectiveManifest().Summary())
		}
	}
	return nil
}

// pluginStatus reports a plugin's readiness WITHOUT executing it: is it
// installed, and does the binary still verify against its recorded sha?
func pluginStatus(spec plugin.Spec) string {
	if !spec.Installed() {
		return "not installed — run `conductor init`"
	}
	if err := plugin.VerifyOnly(spec); err != nil {
		return "unverified: " + firstLine(err.Error())
	}
	if spec.Local {
		return "ok (local build)"
	}
	return "ok"
}

// cmdPluginUpdate re-resolves plugins against their `use:` references,
// re-installing any that moved. With no argument it updates everything not
// pinned to an exact version; with a name it updates just that one. Each change
// is printed with the sha it moved from, so a surprise change is visible.
func cmdPluginUpdate(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	opts := plugin.Options{Log: logf}
	var only []string
	for _, a := range positional(rest) {
		only = append(only, a)
	}
	for _, a := range rest {
		switch a {
		case "--allow-unlisted":
			opts.AllowUnlisted = true
		case "--force":
			opts.Force = true
		}
	}
	if len(only) > 1 {
		return fmt.Errorf("usage: conductor plugin update [name]")
	}
	if len(only) == 1 {
		opts.Only = only[0]
	}
	results, err := reconcilePlugins(cfg, opts)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("no plugins referenced — nothing to update")
	}
	return printResolutions(results)
}

// cmdPluginAdd is sugar for the whole add flow: resolve the reference, install
// it, SHOW THE PERMISSION MANIFEST the operator is accepting, and print the
// config stub to paste. It deliberately prints the stub rather than editing the
// config: the config is the operator's file, and a tool that silently rewrites
// it is a tool you stop trusting.
func cmdPluginAdd(args []string) error {
	cfg, rest, err := loadConfigRest(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor plugin add <ref> [--runtime] [--as <name>]")
	}
	kind := config.UseKindConnector
	instance := ""
	allowUnlisted := false
	for i, a := range args {
		switch a {
		case "--runtime":
			kind = config.UseKindRuntime
		case "--allow-unlisted":
			allowUnlisted = true
		case "--as":
			if i+1 < len(args) {
				instance = args[i+1]
			}
		}
	}
	ref := rest[0]
	u, err := config.ParseUse(kind, ref)
	if err != nil {
		return err
	}
	if u.IsBuiltin() {
		fmt.Printf("%s is a builtin %s — nothing to install.\n\n", u.Name, kind)
		fmt.Printf("%s:\n  %s: { use: %s }\n", kind.Block(), orDefault(instance, u.Name), u.Name)
		return nil
	}
	if instance == "" {
		instance = u.Name
	}

	pr := config.PluginRef{Name: u.Name, Instance: instance, Use: u}
	state := plugin.LoadInstallState(plugin.InstallDir())
	results, err := plugin.Reconcile(
		map[string]config.PluginRef{u.InstallKey(): pr}, state, cfg.PluginTrust,
		plugin.GHReleaseAPI{},
		plugin.Options{AllowUnlisted: allowUnlisted, Describe: describeForInstall(cfg), Log: logf},
	)
	if err != nil {
		return err
	}
	if err := printResolutions(results); err != nil {
		return err
	}
	inst, ok := state.Get(u.InstallKey())
	if !ok {
		return fmt.Errorf("plugin %s was not installed", u.Name)
	}
	fmt.Printf("\n%s declares these permissions:\n  %s\n", u.Name, inst.Manifest.Summary())
	fmt.Println("\nAdd it to your config:")
	fmt.Printf("\n%s:\n  %s:\n    use: %s\n", kind.Block(), instance, ref)
	if kind == config.UseKindConnector && len(inst.Manifest.Egress) > 0 {
		fmt.Printf("    network: [%s]\n", strings.Join(inst.Manifest.Egress, ", "))
	}
	return nil
}

// cmdPluginShow prints one implementation's surface. For a builtin it prints the
// TypeDecl (like `conductor schema`); for a plugin it verifies, spawns,
// describes, prints the Decl plus the permission manifest, then stops the
// subprocess.
func cmdPluginShow(args []string) error {
	cfg, rest, err := loadConfigRest(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor plugin show <name>")
	}
	name := rest[0]

	refs := cfg.PluginRefs()
	for _, key := range []string{"connectors/" + name, "runtimes/" + name} {
		if ref, ok := refs[key]; ok {
			return showPlugin(cfg, ref)
		}
	}
	if decl, ok := connector.TypeDeclFor(name); ok {
		fmt.Printf("%s (builtin connector, conductor %s)\n", name, version)
		printTypeDecl(decl, nil)
		return nil
	}
	if config.BuiltinRuntime(name) {
		fmt.Printf("%s (builtin runtime, conductor %s)\n", name, version)
		fmt.Println("  in-binary runtime; select it with `use:` under runtimes: and an agent's runtime: field")
		return nil
	}
	return fmt.Errorf("no plugin, connector type, or runtime named %q (see `conductor plugin list`)", name)
}

func showPlugin(cfg *config.Config, ref config.PluginRef) error {
	state := plugin.LoadInstallState(plugin.InstallDir())
	inst, ok := state.Get(ref.Key())
	spec := plugin.SpecFromRef(ref, cfg.BaseDir(), inst, ok)

	fmt.Printf("%s (%s plugin)\n", ref.Name, ref.Kind())
	fmt.Printf("  use:    %s\n", ref.Use.String())
	fmt.Printf("  origin: %s", ref.Use.Origin)
	if src := ref.Source(); src != "" {
		fmt.Printf(" (%s)", src)
	}
	fmt.Println()
	if spec.Local {
		fmt.Printf("  binary: %s (local build — no release sha to verify against)\n", spec.BinPath)
	} else if spec.Installed() {
		fmt.Printf("  binary: %s\n", spec.BinPath)
		fmt.Printf("  build:  %s (sha %s)\n", inst.Resolved, strings.ToLower(inst.Sha256))
	} else {
		fmt.Printf("  binary: NOT INSTALLED — run `conductor init`\n")
	}
	fmt.Printf("\n  PERMISSIONS (declared by the plugin, enforced by conductor):\n    %s\n", spec.EffectiveManifest().Summary())
	if len(ref.Network) > 0 {
		fmt.Printf("    narrowed by this config's network: %s\n", strings.Join(ref.Network, ", "))
	}
	if ref.Isolation != nil {
		fmt.Printf("    plus OPT-IN OS isolation: mode %s\n", ref.Isolation.Mode)
	}
	fmt.Println("\n  DISCLOSURE: this plugin is code the daemon executes out-of-process.")
	if ref.Kind() == config.PluginKindConnector {
		fmt.Println("  A connector plugin RECEIVES the credentials of every instance you")
		fmt.Println("  configure for it. Install only plugins you trust.")
	} else {
		fmt.Println("  A runtime plugin EXECUTES your agents (spawns processes, runs tool")
		fmt.Println("  calls). It is the highest-trust plugin — install only ones you trust.")
	}
	if ref.Kind() != config.PluginKindConnector || !spec.Installed() {
		return nil
	}

	sec := secrets.New()
	mgr := plugin.NewManager(map[string]config.PluginRef{ref.Key(): ref}, cfg.BaseDir(), state, pluginDeps(sec, func(map[string]any) {}))
	defer mgr.Close()
	decl, err := mgr.StartAndDescribe(context.Background(), ref.Key())
	if err != nil {
		return err
	}
	fmt.Printf("\n  declared type: %s", decl.Type)
	if decl.Desc != "" {
		fmt.Printf(" — %s", decl.Desc)
	}
	fmt.Println()
	if len(decl.Connection) > 0 {
		fmt.Println("\n  connection:")
		for _, k := range sortedKeys(decl.Connection) {
			f := decl.Connection[k]
			req := ""
			if f.Required {
				req = " (required)"
			}
			fmt.Printf("    %s: %s%s %s\n", k, f.Type, req, f.Desc)
		}
	}
	if len(decl.Verbs) > 0 {
		fmt.Println("\n  verbs:")
		for _, v := range decl.Verbs {
			fmt.Printf("    %s — %s\n", v.Name, v.Desc)
		}
	}
	return nil
}

// cmdPluginRemove drops a plugin from local install state and deletes its
// installed binary. It does NOT touch the config: the reference is the
// declaration, so removing it there is the operator's edit to make.
func cmdPluginRemove(args []string) error {
	cfg, rest, err := loadConfigRest(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor plugin remove <name>")
	}
	name := rest[0]
	state := plugin.LoadInstallState(plugin.InstallDir())
	removed := false
	for _, key := range []string{"connectors/" + name, "runtimes/" + name} {
		if state.Delete(key) {
			removed = true
			if err := os.RemoveAll(plugin.BinDirFor(state.Dir(), key)); err != nil {
				return err
			}
			fmt.Printf("removed %s from install state and deleted its binary\n", key)
		}
	}
	if !removed {
		return fmt.Errorf("no installed plugin named %q (see `conductor plugin list`)", name)
	}
	if err := state.Save(); err != nil {
		return err
	}
	for key, ref := range cfg.PluginRefs() {
		if ref.Name == name {
			fmt.Printf("\nNOTE: your config still references it (%s: %s, use: %s).\n", ref.Use.Kind.Block(), ref.Instance, ref.Use.String())
			fmt.Println("Delete that entry too, or the next `conductor init` reinstalls it.")
			_ = key
		}
	}
	return nil
}

// reconcilePlugins brings install state in line with the config's referenced
// plugins. It is the single path `init`, `plugin update`, and the boot gap-fill
// share.
func reconcilePlugins(cfg *config.Config, opts plugin.Options) ([]plugin.Resolution, error) {
	if opts.Describe == nil {
		opts.Describe = describeForInstall(cfg)
	}
	state := plugin.LoadInstallState(plugin.InstallDir())
	return plugin.Reconcile(cfg.PluginRefs(), state, cfg.PluginTrust, plugin.GHReleaseAPI{}, opts)
}

// describeForInstall spawns a freshly-installed plugin ONCE to record its
// permission manifest and declared kind. This spawn happens before any manifest
// exists, so it is confined to nothing — which is safe because describe is a
// pure self-description that needs neither network nor child processes.
func describeForInstall(cfg *config.Config) plugin.DescribeFunc {
	return func(ctx context.Context, spec plugin.Spec) (*plugin.Decl, error) {
		sec := secrets.New()
		deps := pluginDeps(sec, func(map[string]any) {})
		cl := plugin.NewClient(spec, deps)
		defer cl.Close()
		if err := cl.Start(ctx); err != nil {
			return nil, err
		}
		return cl.Describe(ctx)
	}
}

// printResolutions renders one reconcile pass and returns the first failure, so
// a failed install is an exit code and not just a line of output.
func printResolutions(results []plugin.Resolution) error {
	var firstErr error
	for _, r := range results {
		switch r.Action {
		case plugin.ActionFailed:
			fmt.Printf("  %-20s %-11s %v\n", r.Name, r.Action, r.Err)
			if firstErr == nil {
				firstErr = r.Err
			}
		case plugin.ActionUpdated:
			fmt.Printf("  %-20s %-11s %s (sha %s -> %s)\n", r.Name, r.Action, r.Tag, shortSha(r.PrevSha), shortSha(r.Sha))
		case plugin.ActionLocal:
			fmt.Printf("  %-20s %-11s %s\n", r.Name, r.Action, r.Path)
		default:
			fmt.Printf("  %-20s %-11s %s (%s)\n", r.Name, r.Action, r.Tag, shortSha(r.Sha))
		}
	}
	return firstErr
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func sortedKeys(s plugin.Schema) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadConfigRest is loadConfig but also returns the non-flag positional args.
func loadConfigRest(args []string) (*config.Config, []string, error) {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return nil, nil, err
	}
	return cfg, positional(rest), nil
}
