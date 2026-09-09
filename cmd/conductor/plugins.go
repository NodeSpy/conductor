package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

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
	ctx := context.Background()
	for _, spec := range mgr.ConnectorSpecs() {
		decl, err := mgr.StartAndDescribe(ctx, spec.Name)
		if err != nil {
			mgr.Close()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		cl, _ := mgr.Client(spec.Name)
		if _, err := connector.RegisterExternalConnector(cl, spec, decl); err != nil {
			mgr.Close()
			return nil, fmt.Errorf("plugin %s: %w", spec.Name, err)
		}
		logf("plugin %s: registered connector type %q (%d verb(s))", spec.Ref(), spec.Provides, len(decl.Verbs))
	}
	return mgr, nil
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
