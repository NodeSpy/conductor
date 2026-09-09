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
// and the sandbox wiring. It reuses the daemon's egress proxy + mask paths when
// they are set (the `run` path), and falls back to a local proxy for one-shot
// commands so a sandboxed plugin can still be described.
func pluginDeps(sec *secrets.Resolver, audit func(map[string]any)) plugin.Deps {
	exe, _ := os.Executable()
	egressUnix := controller.EgressProxyUnix
	if egressUnix == nil {
		pm := sandbox.NewProxyManager(func(key, hostport string) {
			logf("plugin egress denied: %s -> %s", key, hostport)
		})
		egressUnix = pm.UnixEndpoint
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
		},
	}
}

// loadConnectorPlugins builds the plugin manager for the config and registers
// every connector-kind plugin's type into the connector registry (verify →
// spawn → describe → register). Fail-closed: any load failure returns an error.
// Returns a manager the caller must Close.
func loadConnectorPlugins(cfg *config.Config, sec *secrets.Resolver, audit func(map[string]any)) (*plugin.Manager, error) {
	mgr := plugin.NewManager(cfg.Plugins, cfg.BaseDir(), pluginDeps(sec, audit))
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
		decl, err := mgr.StartAndDescribe(ctx, spec.Name)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		cl, _ := mgr.Client(spec.Name)
		if _, err := connector.RegisterExternalConnector(cl, spec, decl); err != nil {
			rollback()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		registered = append(registered, spec.Provides)
		logf("plugin %s: registered connector type %q (%d verb(s))", spec.Ref(), spec.Provides, len(decl.Verbs))
	}
	return mgr, nil
}

// pluginBootTimeout bounds a single connector plugin's verify+spawn+describe at
// boot, so a stalled plugin can't hang daemon startup indefinitely.
const pluginBootTimeout = 30 * time.Second

// pluginRuntimeControllers verifies each runtime-kind plugin (verify-before-
// execute, fail-closed) and synthesizes it into a ControllerConfig the existing
// controller registry drives as an ACP subprocess — generalizing the shipped
// ACP runtime (#54 §4). A runtime plugin is the highest-stakes plugin (§8.7):
// it EXECUTES agents. Returns entries keyed by the runtime name each provides.
//
// The binary is re-verified on EVERY spawn via the `conductor plugin-exec`
// wrapper (M2 fix), so the sha pin holds for the daemon's whole lifetime, not
// just at boot.
//
// Runtime plugins reuse conductor's ACP controller path and are LESS isolated
// than connector plugins in one remaining way (documented, not stubbed; see
// docs/wiki/Plugins.md): they bypass internal/plugin's supervision (crash-loop
// cap, size-bound, stderr redaction) and rely on ACP's own handling. Their
// environment IS scrubbed, though — ScrubEnv (set below) makes acp.go's
// spawnACP seed the child from sandbox.MinimalEnv() instead of the daemon's
// os.Environ(), so a runtime plugin does not inherit env:-resolved secrets;
// isolation still masks paths and network.
func pluginRuntimeControllers(cfg *config.Config) (map[string]config.ControllerConfig, error) {
	out := map[string]config.ControllerConfig{}
	for name, ref := range cfg.Plugins {
		if ref.Kind != config.PluginKindRuntime {
			continue
		}
		spec := plugin.SpecFromRef(name, ref, cfg.BaseDir())
		if err := plugin.VerifyOnly(spec); err != nil {
			return nil, fmt.Errorf("runtime plugin %s: %w", name, err)
		}
		if spec.Sha256 == "" && spec.AllowUnverified {
			logf("runtime plugin %s: WARNING running UNVERIFIED (no sha256 pin)", name)
		}
		// Route the ACP launch through `conductor plugin-exec`, which re-verifies
		// the binary's sha against the pin on EVERY spawn (the ACP controller
		// re-spawns per session) before exec'ing it — closing the boot-only
		// verification window (M2). The wrapper runs inside the same sandbox the
		// ACP controller applies.
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("runtime plugin %s: cannot resolve conductor binary for re-verify wrapper: %w", name, err)
		}
		wrap := []string{self, "plugin-exec", "--sha", spec.Sha256}
		if spec.AllowUnverified {
			wrap = append(wrap, "--allow-unverified")
		}
		wrap = append(wrap, "--", spec.BinPath)
		wrap = append(wrap, spec.Args...)
		out[spec.Provides] = config.ControllerConfig{
			Transport: "acp",
			Command:   wrap,
			Isolation: ref.Isolation,
			ScrubEnv:  true, // untrusted third-party code: minimal env, no daemon secrets (#54 §8.1)
		}
		logf("plugin %s: registered runtime %q (acp, per-spawn re-verified, env-scrubbed)", spec.Ref(), spec.Provides)
	}
	return out, nil
}

// mergedControllersWithPlugins is cfg.MergedControllers() plus verified runtime
// plugins. A plugin runtime name that collides with a configured runtime/
// controller is refused (external-overrides-bundled is already blocked at
// config validation).
func mergedControllersWithPlugins(cfg *config.Config) (map[string]config.ControllerConfig, error) {
	merged := cfg.MergedControllers()
	prt, err := pluginRuntimeControllers(cfg)
	if err != nil {
		return nil, err
	}
	for name, cc := range prt {
		if _, dup := merged[name]; dup {
			return nil, fmt.Errorf("runtime plugin provides %q which collides with a configured runtime/controller", name)
		}
		merged[name] = cc
	}
	return merged, nil
}

// cmdPluginExec is the hidden re-verify-then-exec wrapper a runtime plugin's
// ACP launch is routed through (see pluginRuntimeControllers). It re-checks the
// binary's SHA-256 against the pin — from a safe path — on every spawn, then
// replaces itself with the plugin via exec. Usage:
//
//	conductor plugin-exec --sha <hex> [--allow-unverified] -- <binPath> [args...]
func cmdPluginExec(args []string) error {
	var sha string
	var allowUnverified bool
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--sha":
			if i+1 >= len(args) {
				return fmt.Errorf("plugin-exec: --sha needs a value")
			}
			sha, i = args[i+1], i+2
		case "--allow-unverified":
			allowUnverified, i = true, i+1
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
		Sha256: sha, AllowUnverified: allowUnverified,
	}); err != nil {
		return err
	}
	// Replace this process with the verified plugin, inheriting stdio+env.
	return syscall.Exec(bin, rest, os.Environ())
}

// cmdPlugin implements `conductor plugin list|show|remove`.
func cmdPlugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: conductor plugin list|show <name>|remove <name>")
	}
	switch args[0] {
	case "list", "ls":
		return cmdPluginList(args[1:])
	case "show":
		return cmdPluginShow(args[1:])
	case "remove", "rm":
		return cmdPluginRemove(args[1:])
	default:
		return fmt.Errorf("unknown plugin subcommand %q (list|show|remove)", args[0])
	}
}

// cmdPluginList shows bundled connectors AND runtimes (tagged bundled) plus the
// external plugins declared in config (tagged external, sha-pinned). It does
// NOT execute any plugin — external entries are shown from config plus a cheap
// verify-before-execute health check (no spawn).
func cmdPluginList(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	fmt.Printf("%-16s %-10s %-9s %-12s %s\n", "NAME", "KIND", "SOURCE", "VERSION", "PROVIDES")

	// Bundled connectors (everything registered that is not external).
	for _, t := range connector.Types() {
		if connector.IsExternalType(t) {
			continue
		}
		fmt.Printf("%-16s %-10s %-9s %-12s %s\n", t, "connector", "bundled", version, t)
	}
	// Bundled runtimes.
	brt := []string{"paseo", "opencode", "agent-deck", "cli"}
	for _, r := range brt {
		fmt.Printf("%-16s %-10s %-9s %-12s %s\n", r, "runtime", "bundled", version, r)
	}
	// External plugins from config.
	names := make([]string, 0, len(cfg.Plugins))
	for n := range cfg.Plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		spec := plugin.SpecFromRef(n, cfg.Plugins[n], cfg.BaseDir())
		health := pluginHealth(spec)
		ver := spec.Version
		if ver == "" {
			ver = "-"
		}
		fmt.Printf("%-16s %-10s %-9s %-12s %s  [%s]\n", n, spec.Kind, "external", ver, spec.Provides, health)
	}
	return nil
}

// pluginHealth runs verify-before-execute (sha + safe perms) WITHOUT executing
// the binary, returning a short status for `plugin list`.
func pluginHealth(spec plugin.Spec) string {
	if err := plugin.VerifyOnly(spec); err != nil {
		return "unverified: " + firstLine(err.Error())
	}
	return "verified"
}

// cmdPluginShow prints one plugin's surface. For a bundled connector type it
// prints the TypeDecl (like `conductor schema`); for an external plugin it
// verifies, spawns, describes, prints the Decl plus the capability/credential
// disclosure, then stops the subprocess.
func cmdPluginShow(args []string) error {
	cfg, rest, err := loadConfigRest(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor plugin show <name>")
	}
	name := rest[0]

	// External plugin, by config key or by the type/runtime it provides?
	if ref, ok := cfg.Plugins[name]; ok {
		return showExternalPlugin(cfg, name, ref)
	}
	for key, ref := range cfg.Plugins {
		if ref.ProvidesName(key) == name {
			return showExternalPlugin(cfg, key, ref)
		}
	}
	// Bundled connector type?
	if decl, ok := connector.TypeDeclFor(name); ok {
		fmt.Printf("plugin %s (bundled connector, version %s)\n", name, version)
		printTypeDecl(decl, nil)
		return nil
	}
	// Bundled runtime?
	for _, r := range []string{"paseo", "opencode", "agent-deck", "cli"} {
		if r == name {
			fmt.Printf("plugin %s (bundled runtime, version %s)\n", name, version)
			fmt.Println("  in-process runtime; select via an agent's runtime: field")
			return nil
		}
	}
	return fmt.Errorf("no plugin, connector type, or runtime named %q (see `conductor plugin list`)", name)
}

func showExternalPlugin(cfg *config.Config, name string, ref config.PluginRef) error {
	spec := plugin.SpecFromRef(name, ref, cfg.BaseDir())
	fmt.Printf("plugin %s (external %s, version %s)\n", name, spec.Kind, orDash(spec.Version))
	fmt.Printf("  source: %s\n", spec.BinPath)
	if spec.Sha256 != "" {
		fmt.Printf("  sha256: %s\n", strings.ToLower(spec.Sha256))
	} else {
		fmt.Printf("  sha256: (unpinned — allow_unverified)\n")
	}
	fmt.Println("\n  DISCLOSURE: this plugin is code the daemon executes out-of-process.")
	if spec.Kind == plugin.KindConnector {
		fmt.Println("  A connector plugin RECEIVES the credentials of every instance you")
		fmt.Println("  configure for its type. Install only plugins you trust.")
	} else {
		fmt.Println("  A runtime plugin EXECUTES your agents (spawns processes, runs tool")
		fmt.Println("  calls). It is the highest-trust plugin — install only ones you trust.")
	}

	// Runtimes are described by the controller side (increment 6); connectors
	// spawn + describe here.
	if spec.Kind != plugin.KindConnector {
		fmt.Println("\n  (runtime Decl introspection: see `conductor plugin list` and runtimes: config)")
		return nil
	}
	sec := secrets.New()
	mgr := plugin.NewManager(map[string]config.PluginRef{name: ref}, cfg.BaseDir(), pluginDeps(sec, func(map[string]any) {}))
	defer mgr.Close()
	decl, err := mgr.StartAndDescribe(context.Background(), name)
	if err != nil {
		return err
	}
	fmt.Printf("\n  declared type: %s", decl.Type)
	if decl.Desc != "" {
		fmt.Printf(" — %s", decl.Desc)
	}
	fmt.Println()
	if caps := decl.Capabilities; len(caps.Egress) > 0 || len(caps.FS) > 0 || caps.Spawns {
		fmt.Println("\n  declared capabilities (granted via isolation:):")
		if len(caps.Egress) > 0 {
			fmt.Printf("    egress: %s\n", strings.Join(caps.Egress, ", "))
		}
		if len(caps.FS) > 0 {
			fmt.Printf("    fs:     %s\n", strings.Join(caps.FS, ", "))
		}
		if caps.Spawns {
			fmt.Println("    spawns: child processes")
		}
	}
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

// cmdPluginRemove: managed remove (fetch/lockfile/cache) is a follow-up; for a
// locally-sourced plugin this reports how to remove it.
func cmdPluginRemove(args []string) error {
	cfg, rest, err := loadConfigRest(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: conductor plugin remove <name>")
	}
	name := rest[0]
	if _, ok := cfg.Plugins[name]; !ok {
		return fmt.Errorf("no external plugin named %q in config", name)
	}
	fmt.Printf("plugin %q is declared in your config's plugins: block.\n", name)
	fmt.Println("Managed remove (fetch/lockfile/cache) is not yet implemented for local")
	fmt.Println("sources — delete its plugins: entry (and any connectors:/runtimes: that")
	fmt.Println("reference its type) and restart the daemon. The running subprocess stops")
	fmt.Println("when the daemon reloads.")
	return nil
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
