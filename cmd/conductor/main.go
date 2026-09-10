// Command conductor is an event-driven agent orchestrator for a local
// Paseo daemon: connectors turn service events (GitHub App webhooks over
// smee, Slack, cron, sentry, …) into triggers whose steps dispatch coding
// agents, verbs, code, and commands.
//
// Subcommands: run | validate | replay | sweep | force | status | report |
// runs | watch | pause | resume | update | service | connectors | connector |
// schema | secrets | vault | unlock | config | mcp | workflows | version.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/callable"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/engine"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/inbound"
	"github.com/NodeSpy/conductor/internal/integrations/slack" // registers "slack"; also feeds hand-off replies (see wireSlackHandoffInbox)
	"github.com/NodeSpy/conductor/internal/memory"
	agentmodels "github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/vaults"

	_ "github.com/NodeSpy/conductor/internal/integrations/cron"    // register "cron"
	_ "github.com/NodeSpy/conductor/internal/integrations/github"  // register "github"
	_ "github.com/NodeSpy/conductor/internal/integrations/rss"     // register "rss"
	_ "github.com/NodeSpy/conductor/internal/integrations/webhook" // register "webhook"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	// Let pack requires.conductor constraints check against the running
	// version, and requires.connectors constraints against each installed
	// plugin connector's resolved release (a builtin connector's version IS
	// the daemon version — see config.resolvedConnectorVersion).
	config.SetRuntimeVersion(version)
	publishConnectorVersions()
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "sandbox-net":
		// Internal: the in-sandbox forwarder for enforced egress (#36 §15).
		// Launched by conductor itself inside a namespace/container; not a
		// user-facing command.
		os.Exit(runSandboxNet(args))
	case "run":
		err = cmdRun(args)
	case "validate":
		err = cmdValidate(args)
	case "replay":
		err = cmdReplay(args)
	case "sweep":
		err = cmdSweep(args)
	case "force":
		err = cmdForce(args)
	case "status":
		err = cmdStatus(args)
	case "report":
		err = cmdReport(args)
	case "runs":
		err = cmdRuns(args)
	case "watch":
		err = cmdWatch(args)
	case "pause":
		err = cmdPause(args, true)
	case "resume":
		err = cmdPause(args, false)
	case "update":
		err = cmdUpdate(args)
	case "service":
		err = cmdService(args)
	case "connectors":
		err = cmdConnectors(args)
	case "connector":
		err = cmdConnectorAuth(args)
	case "schema":
		err = cmdSchema(args)
	case "secrets":
		err = cmdSecrets(args)
	case "vault":
		err = cmdVault(args)
	case "unlock":
		err = cmdUnlock(args)
	case "config":
		err = cmdConfig(args)
	case "mcp":
		err = cmdMCP(args)
	case "plugin", "plugins":
		err = cmdPlugin(args)
	case "plugin-exec": // hidden: re-verify-then-exec wrapper for runtime plugins
		err = cmdPluginExec(args)
	case "discover":
		err = cmdDiscover(args)
	case "call":
		err = cmdCall(args)
	case "memory":
		err = cmdSkillMemory(args)
	case "secret":
		err = cmdSecret(args)
	case "workflows":
		err = cmdWorkflows(args)
	case "init":
		err = cmdInit(args)
	case "pack":
		err = cmdPack(args)
	case "version", "-v", "--version":
		fmt.Println("conductor", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `conductor — event-driven agent orchestration for your Paseo daemon

usage:
  conductor run [--config PATH]         start the daemon
  conductor run <name> [--input k=v ...] [--json '{…}']  fire a manual trigger via the running daemon
  conductor validate [--config PATH]    load & validate config, then exit
  conductor replay <event.json> [--config PATH]  run a saved webhook through the pipeline (dry-run)
  conductor sweep [--config PATH]       one catch-up sweep (dry-run print)
  conductor sweep --now [--config PATH] signal the running daemon to sweep now
  conductor force <kind> <owner/repo>#<n>  force an action for a target now (via the running daemon)
  conductor status [--config PATH]      snapshot: live agents, in-flight workflows, stuck/attention
  conductor report [--days N]           activity summary: dispatches by kind/outcome + attention + spend
  conductor runs [<id>] [--limit N]     recorded executions: list, or one run's step detail
  conductor runs retry <id> [--from <step>] [--force-replay]  re-run a recorded execution (recorded inputs pinned)
  conductor watch [<run-id>] [--json]   tail the live run event stream (steps, gates, outcomes)
  conductor pause | resume              stop / resume dispatch at runtime (no restart)
  conductor update [--force] [--tag vX]  self-update to the latest release (uses gh)
  conductor service install|sync|uninstall  manage the background service unit
  conductor connectors ls               list configured connectors: state, events, verbs
  conductor plugin list                 list plugins: bundled connectors/runtimes + external
  conductor plugin show <name>          a plugin's surface (Decl + capability/credential disclosure)
  conductor plugin remove <name>        how to remove an external plugin from your plugins: block
  conductor schema <connector>          print a connector's event/filter/verb/option schemas
  conductor secrets check               resolve every secret reference and report
  conductor connector auth ls           each oauth2 connector's login state + token expiry
  conductor connector auth <name> [--revoke]  oauth2 login (or clear stored tokens)
  conductor vault <name> init|add|get|ls|rm  manage a named vaults: entry
  conductor unlock                      seed the default vault key for non-interactive restarts
  conductor config migrate [--dry-run]  transform a legacy config to the connectors schema
  conductor mcp memory --socket <path>  stdio MCP server: memory + skill broker (agent-facing)
  conductor mcp callable --token <name> [--config PATH]  stdio MCP server: invoke callable workflows (external MCP clients)
  conductor workflows ls|review|rm      manage saved (agent-promoted) workflows
  conductor init [--config PATH] [--allow-unlisted]  fetch the packs: block, write the lockfile, preview
  conductor pack list|plan              list configured packs; preview what they add
  conductor pack add <source>           fetch a pack, show its install review + a ready-to-paste block
  conductor pack lint|show <pack-dir>   validate / render a pack (author + install-review tooling)
  conductor pack remove <instance>      clear a pack's vendored tree + lockfile entries
  conductor pack update | update --packs  re-resolve the packs: block and diff the lockfile
  conductor version
`)
}

// configPath extracts --config from args (default configDir()/config.yaml —
// ~/.config/conductor).
func configPath(args []string) (string, []string) {
	def := filepath.Join(configDir(), "config.yaml")
	rest := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			def = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return def, rest
}

func loadConfig(args []string) (*config.Config, []string, error) {
	path, rest := configPath(args)
	// Load secrets from the sibling conductor.env so ${...} refs resolve for
	// every subcommand (validate/replay/sweep/run) — matching how the daemon
	// loads them at runtime, without needing to export anything by hand.
	loadEnvFile(filepath.Join(filepath.Dir(path), "conductor.env"))
	cfg, err := config.Load(path)
	return cfg, rest, err
}

// resolveBootConfig runs the daemon's boot config pipeline: migrate-on-boot,
// then load, and on ANY load failure holds degraded until the config becomes
// loadable rather than returning an error that exits cmdRun into a
// service-manager crash-loop (H1). The failure that must hold is not only a
// failed migration — a connectors-schema config the strict loader rejects (a
// stray key, an env ref that didn't resolve) reaches here with migrateWarning
// == "", just as much a restart-loop trap. Returns the loaded config and any
// escalate warning to surface. This is the testable boot seam: the gate lives
// here, not inline in cmdRun.
func resolveBootConfig(args []string) (*config.Config, string, error) {
	migrateWarning := autoMigrateOnBoot(args)
	// loadConfig loads the sibling conductor.env first, so ${...} refs resolve
	// (this is also how launchd — which has no EnvironmentFile — gets secrets).
	cfg, _, err := loadConfig(args)
	if err != nil {
		cfg, migrateWarning, err = holdDegradedUntilLoadable(args, migrateWarning, err)
	}
	return cfg, migrateWarning, err
}

// buildIntegrations instantiates every configured integration via the registry.
func buildIntegrations(cfg *config.Config) ([]core.Integration, error) {
	var out []core.Integration
	for _, ref := range cfg.Integrations {
		if !ref.IsEnabled() {
			continue
		}
		ig, err := core.Build(ref.Type, ref.Name, ref.Decode)
		if err != nil {
			return nil, err
		}
		out = append(out, ig)
	}
	return out, nil
}

// actionLister is implemented by an integration that can enumerate its configured
// actions, so checks spanning the integration's sub-config and the top-level
// config (agent profile references) can run up front rather than at dispatch.
type actionLister interface {
	Actions() []config.ActionRef
}

// validateAll runs each integration's own Validate, then the cross-config
// checks: every `agent:` an action or workflow step names must be a profile
// defined under `agents:` — otherwise the engine dispatches an empty profile and
// paseo fails with MISSING_PROVIDER only once a live trigger reaches that step.
func validateAll(cfg *config.Config, igs []core.Integration) error {
	var refs []config.ActionRef
	for _, ig := range igs {
		if err := ig.Validate(); err != nil {
			return err
		}
		if l, ok := ig.(actionLister); ok {
			refs = append(refs, l.Actions()...)
		}
	}
	return cfg.CheckAgentRefs(refs)
}

func cmdValidate(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	if err := validateAll(cfg, igs); err != nil {
		return err
	}
	// The connectors-model semantic pass: schemas, verbs, position-scoped
	// references, workflow inputs/outputs. No-op for legacy-only configs.
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		return err
	}
	defer stack.Close()
	// Deprecation + skill + isolation lint: warnings, never failures.
	for _, w := range flow.DeprecationWarnings(cfg) {
		fmt.Printf("warning: %s\n", w)
	}
	for _, w := range flow.IsolationWarnings(cfg) {
		fmt.Printf("warning: %s\n", w)
	}
	if stack != nil {
		for _, w := range flow.SkillWarnings(cfg, stack.Registry) {
			fmt.Printf("warning: %s\n", w)
		}
	}
	if stack != nil {
		fmt.Printf("ok: %d connector(s), %d trigger(s), %d workflow(s), %d agent profile(s)",
			len(cfg.ConnectorsMap), len(cfg.Triggers), len(cfg.Workflows), len(cfg.Steps))
		if len(cfg.Integrations) > 0 {
			fmt.Printf(" — plus %d legacy integration(s)", len(cfg.Integrations))
		}
		fmt.Println()
		return nil
	}
	fmt.Printf("ok: %d integration(s) configured (%d enabled), %d agent profile(s)\n",
		len(cfg.Integrations), len(igs), len(cfg.Steps))
	return nil
}

func cmdRun(args []string) error {
	// `conductor run <name>` fires a manual trigger through the running
	// daemon; the bare form starts the daemon itself.
	if hasPositional(args) {
		return cmdRunTrigger(args)
	}
	// Automatic in-place migration: a legacy config is transformed to the
	// connectors schema (backed up, validated, swapped) BEFORE the strict
	// runtime load; on any failure the daemon keeps running on the legacy
	// config and notifies.
	cfg, migrateWarning, err := resolveBootConfig(args)
	if err != nil {
		return err
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	if err := validateAll(cfg, igs); err != nil {
		return err
	}

	st, err := store.Open(store.Options{
		StatePath:      cfg.Store.StateFile,
		AuditPath:      cfg.Store.AuditLog,
		TTL:            cfg.Store.StateTTL.D(),
		MaxPRs:         cfg.Store.MaxTrackedPRs,
		AuditMaxSize:   cfg.Store.AuditMaxSize.Bytes(),
		HistoryMaxAge:  cfg.Store.HistoryRetention.D(),
		HistoryMaxRuns: cfg.Store.HistoryMaxRuns,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	retry, writeTok, readTok := dispatchTuning(igs)
	paseoBin, err := resolvePaseoBin(cfg)
	if err != nil {
		return err
	}
	disp := dispatch.New(paseoBin, retry, cfg.DryRun)
	disp.AdoptOpenWorkspaces = cfg.AdoptOpenWorkspaces
	preflightPATH(disp.PaseoBin)
	notifier := notify.New(cfg.Notify, logf, st.Audit)
	// Every lifecycle event feeds the conductor.* source (ordinary triggers
	// alert on them); the source's loop guard keeps a notification
	// workflow's own events from re-feeding.
	notifier.SetPublisher(connector.EmitLifecycle)
	// Controller registry (paseo is the built-in default) + the session broker that
	// owns one live session per PR — so an interactive hand-off survives a restart
	// and follow-ups funnel to the live session instead of a duplicate agent. Built
	// here so the broker and the engine share one registry. With no `controllers:`
	// or `runtimes:` block this resolves to paseo everywhere, unchanged.
	//
	// HostArgvPrefix is wired here (once, before any controller launches) so
	// cli/acp/agent-deck controllers configured with `host:` can resolve the
	// ssh argv prefix for a named `hosts:` entry.
	controller.HostArgvPrefix = func(name string) ([]string, error) {
		hc, ok := cfg.Hosts[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q (defined: %s)", name, sortedHostNames(cfg.Hosts))
		}
		return (&hosts.Client{}).ArgvPrefix(hosts.Target{Name: name, Cfg: hc}), nil
	}
	// The egress proxy manager (#36 §15): one loopback proxy per distinct
	// allowlist, enforcing isolation network policy for the runtimes conductor
	// launches; agent-authored dispatches with no explicit policy route
	// through its deny-all proxy. Denials are logged and audited.
	egress := sandbox.NewProxyManager(func(key, hostport string) {
		logf("sandbox: egress denied: %s (not in allowlist)", hostport)
		st.Audit(map[string]any{"event": "egress_denied", "target": hostport})
	})
	defer egress.Close()
	controller.EgressProxyFor = egress.Endpoint
	controller.EgressProxyUnix = egress.UnixEndpoint
	// Namespace-mode sandboxes share the daemon's uid — by default the
	// daemon's own state and config disappear from their mount view
	// (#36 iso-review H7); isolation `privileged: true` is the opt-out.
	cfgFile, _ := configPath(args)
	stateDir := config.StateDir()
	if cfg.Store.StateFile != "" {
		stateDir = filepath.Dir(cfg.Store.StateFile)
	}
	// The state dir holds the control socket, state.json, the audit log, the
	// pid file, and per-run history — all daemon-private. Owner-only (0700) so
	// no other local user can read the audit trail or reach the sockets inside
	// it; tighten an already-existing dir too, since MkdirAll won't.
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("state dir %s: %w", stateDir, err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("state dir %s perms: %w", stateDir, err)
	}
	controller.DaemonMaskPaths = []string{stateDir, filepath.Dir(cfgFile)}
	// HostDial is the ssh -W stdio forward remote opencode servers are reached
	// through (they bind the remote 127.0.0.1; no port opens anywhere).
	controller.HostDial = func(ctx context.Context, name, addr string) (net.Conn, error) {
		hc, ok := cfg.Hosts[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q (defined: %s)", name, sortedHostNames(cfg.Hosts))
		}
		return (&hosts.Client{}).DialVia(ctx, hosts.Target{Name: name, Cfg: hc}, addr)
	}
	var paseoSender controller.Sender = disp
	// External runtime plugins (#54) are verified (fail-closed) and merged into
	// the controller set as sandboxed ACP subprocesses, selectable via a
	// profile's runtime:.
	mergedControllers, err := mergedControllersWithPlugins(cfg)
	if err != nil {
		return err
	}
	reg := controller.NewRegistry(mergedControllers, cfg.DefaultRuntimeName(), disp, paseoSender)
	// Paseo runtimes with their own bin: — or a host:, whose paseo CLI runs
	// over SSH — get dedicated dispatchers; the registry rebinds them so an
	// agent's `runtime:` selection launches on the right box.
	paseoOverrides, err := buildPaseoOverrides(cfg, paseoBin, retry, cfg.DryRun)
	if err != nil {
		return err
	}
	for name, pd := range paseoOverrides {
		reg.OverridePaseo(name, pd, pd)
		where := "bin " + pd.PaseoBin
		if pd.Remote != nil {
			where += " on host " + pd.Remote.Name
		}
		logf("runtime %s: dedicated paseo dispatcher (%s)", name, where)
	}
	broker := controller.NewBroker(reg, st, logf)
	// Hand-off registry: resolves the named `handoffs:` map (config.Load already
	// folded a legacy singular `handoff:` block into Handoffs["default"], so this
	// is the only path needed here). Empty map → Resolve always yields nil → the
	// review hand-off keeps today's paseo-native behavior.
	handoffs := handoff.NewRegistry(cfg.Handoffs, cfg.DefaultHandoffName(), logf)

	// The connectors-model stack: secret resolution, the connector registry,
	// the flow runner, and the lowered source integrations. nil when the
	// config has no connectors: block — everything below then behaves exactly
	// as before.
	stack, err := buildFlowStack(cfg, st, notifier, cfg.DryRun)
	if err != nil {
		return err
	}
	defer stack.Close() // stop plugin subprocesses on daemon shutdown (#54)
	if stack != nil {
		igs = append(igs, stack.Integrations...)
	}
	// A legacy config (no connectors: block) can still carry a memory:
	// section — buildFlowStack didn't run, so wire it here.
	if stack == nil {
		if err := configureMemory(cfg, nil); err != nil {
			return err
		}
	}
	// Redaction reaches every outbound surface once the resolver exists: the
	// notifier's webhooks/via routes, the shared logf choke point, and the
	// audit writer's value backstop.
	if stack != nil {
		notifier.SetSecrets(stack.Secrets)
		setLogRedactor(stack.Secrets)
		st.SetAuditRedactor(stack.Secrets.Redact)
	}
	notifyStackFailures(stack, notifier)
	if migrateWarning != "" {
		notifier.Emit(context.Background(), notify.EventEscalate, core.Trigger{Source: "config", Kind: "migration"}, migrateWarning)
	}
	// notify.via routes deliver through connector verbs — wire the router when
	// a connectors: block exists (via with no connectors logs a warning).
	if stack != nil {
		notifier.SetRouter(func(ctx context.Context, r config.NotifyRoute, data map[string]any) error {
			connName, verb, _ := strings.Cut(r.Uses, ".")
			in, ok := stack.Registry.Get(connName)
			if !ok {
				return fmt.Errorf("unknown connector %q", connName)
			}
			merged := connector.MergeOptions(in.DefaultOptions, r.Options)
			rendered, err := flow.RenderOptions(merged, data)
			if err != nil {
				return err
			}
			_, err = in.InvokeFinal(ctx, verb, rendered)
			return err
		})
	}
	// Wire the engine's dispatch-completion seam to every configured slack
	// instance's on_done/on_fail handling (see core.SetCompletionHook and
	// slack.Integration.HandleCompletion). A no-op when no slack integration is
	// configured. Sibling seam to slack.SetReplyHook (hand-off thread replies).
	wireSlackCompletion(igs)
	// Discord hand-off gateway(s): unlike Slack this needs no separate
	// `integrations:` entry — conductor runs the bot gateway itself, one
	// goroutine per distinct configured bot_token (entries sharing a token
	// share a connection). A no-op when no `discord:` hand-off is configured.
	// Governed by ctx below so it shuts down with the daemon; started after ctx
	// exists, alongside the web hand-off listeners.
	// Shared "never reap" set for interactive hand-off agents: the engine registers
	// a background step's agent at launch; the reaper skips anything in it.
	hold := dispatch.NewHoldSet(filepath.Join(filepath.Dir(cfg.Store.StateFile), "holds.json"))
	// Session affinity: agents whose profile carries a session: block get one
	// live session per rendered key, shared across triggers, persisted in
	// affinity.json, resumed after restart, held from the reaper while bound.
	affinity := controller.NewAffinity(reg, st, cfg, hold.Add, hold.Remove, logf)
	engOpts := engine.Options{
		Config: cfg, Store: st, Dispatch: disp, Controllers: reg, Broker: broker, Handoffs: handoffs,
		Notifier: notifier, Author: gitAuthor(), UserToken: writeTok, ReadToken: readTok, Log: logf,
		RefreshAppToken: refreshAppToken(igs), Hold: hold, Affinity: affinity, PausePath: pausePath(cfg),
	}
	if stack != nil {
		engOpts.Flow = stack.Runner
		engOpts.Connectors = stack.Registry
		engOpts.Secrets = stack.Secrets
		disp.Secrets = stack.Secrets
		// Tracked secrets never render into an external runtime's
		// prompt/env scope through step outputs (#122 R3).
		dispatch.SetScrubber(stack.Secrets)
	}
	// Cost accounting (#36 §14): install the config's model→$ overrides
	// before anything estimates a run's spend.
	if cfg.Pricing != nil {
		models := make(map[string]cost.ModelPrice, len(cfg.Pricing.Models))
		for pat, p := range cfg.Pricing.Models {
			models[pat] = cost.ModelPrice{Input: p.Input, Output: p.Output}
		}
		var def *cost.ModelPrice
		if d := cfg.Pricing.Default; d != nil {
			def = &cost.ModelPrice{Input: d.Input, Output: d.Output}
		}
		cost.SetPricing(models, def)
	}
	eng := engine.New(engOpts)
	// Model selection (docs/design/runtimes-models-packs.md §2.3): the
	// resolver owns the fleet ladder and the discovered rosters. Discovery
	// is lazy and degrade-safe — a box that cannot enumerate simply bare
	// launches — so wiring it costs nothing at boot.
	eng.SetModelResolver(agentmodels.NewResolver(cfg, agentmodels.NewCatalog(config.StateDir())))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The live agent-tool socket: with a memory: section configured — or any
	// profile opted into the conductor skill (#36 §12) — serve the tool
	// socket beside the state file and publish the launch command runtimes
	// with live-tool injection (ACP mcpServers) attach to agent sessions. A
	// socket failure only loses the LIVE surface — the verbs, output
	// contract, and injection still work — so log, don't die.
	if mgr := memory.Active(); mgr != nil || cfg.SkillEnabled() {
		sock := filepath.Join(filepath.Dir(cfg.Store.StateFile), "memory.sock")
		if l, lerr := memory.ListenSocket(sock); lerr != nil {
			logf("memory: %v — live agent tools disabled (verbs/output contract unaffected)", lerr)
		} else {
			go memory.ServeIPC(ctx, l, mgr, st.Audit, logf)
			if exe, eerr := os.Executable(); eerr == nil {
				argv := []string{exe, "mcp", "memory", "--socket", sock}
				if mgr == nil {
					// Skill-only socket: hide the memory tools the daemon
					// can't serve.
					argv = append(argv, "--no-memory")
				}
				memory.SetToolCommand(argv)
				logf("memory: live agent tools on %s", sock)
			}
			// The agent-driven-workflow live surface rides the same socket:
			// run_step executes one guarded step through the flow runner,
			// workflow_list serves the choosable catalog.
			var ops memory.LiveOps
			if stack != nil {
				ops.RunStep = stack.Runner.RunLiveStep
				ops.ListWorkflows = stack.Runner.WorkflowCatalog
				logf("memory: live run_step/workflow_list tools enabled")
			}
			// The secret broker (#36 §12): built only when a profile opts in
			// via skill:, authorized by the session tokens the dispatch path
			// mints (skill.Active), never by client-asserted identity.
			if cfg.SkillEnabled() {
				// A broker name is a vault entry ("<vault>/<key>", read at
				// issue time through the vaults registry, which taints the
				// value for redaction); a bare name falls back to the
				// retired named-secrets block.
				lookup := func(name string) (string, bool) {
					if vault, key, ok := strings.Cut(name, "/"); ok {
						v, err := vaults.Read(ctx, vault, key)
						return v, err == nil && v != ""
					}
					if stack != nil {
						v, ok := stack.SecretVals[name]
						return v, ok && v != ""
					}
					return "", false
				}
				sb := skill.NewBroker(lookup, st.Audit)
				skill.SetActive(sb)
				go sb.SweepLoop(ctx, 30*time.Second)
				// Peer credentials come off each socket connection (kernel
				// SO_PEERCRED) — the broker binds sessions to the claiming
				// process and refuses a token from any other.
				asPeer := func(p memory.Peer) skill.Peer {
					return skill.Peer{PID: p.PID, StartTime: p.StartTime, UID: p.UID, Valid: p.Valid}
				}
				ops.ClaimToken = func(claim string, peer memory.Peer) (string, error) {
					return sb.ClaimSession(claim, asPeer(peer))
				}
				ops.IssueSecret = func(token, name string, peer memory.Peer) (string, time.Time, error) {
					return sb.Issue(token, name, asPeer(peer))
				}
				ops.RedeemSecret = func(token, grant string, peer memory.Peer) (string, error) {
					return sb.Redeem(token, grant, asPeer(peer))
				}
				// Identify resolves a session token to its dispatch provenance
				// so the CLI/remote memory + run_step ops bind their Source to
				// the token's real identity, never a spoofable body Source.
				ops.Identify = func(token string, peer memory.Peer) (memory.Source, int, bool) {
					id, err := sb.Authorize(token, asPeer(peer))
					if err != nil {
						return memory.Source{}, 0, false
					}
					return memory.Source{Step: id.Agent, Repo: id.Repo, Trigger: id.Trigger}, id.Number, true
				}
				// The verb-tool surface: catalog + execution, both bound to
				// the token's real dispatch identity and its skill.verbs.
				if stack != nil {
					runner := stack.Runner
					ops.SkillVerbs = func(token string, peer memory.Peer) ([]map[string]any, error) {
						id, err := sb.Authorize(token, asPeer(peer))
						if err != nil {
							return nil, err
						}
						return runner.SkillVerbCatalog(id.Policy.Verbs), nil
					}
					ops.RunVerb = func(vctx context.Context, token, uses string, options map[string]any, peer memory.Peer) (map[string]any, error) {
						id, err := sb.Authorize(token, asPeer(peer))
						if err != nil {
							return nil, err
						}
						// The per-session call cap (skill.max_calls) charges
						// BEFORE dispatch — a capped session runs nothing.
						if err := sb.ChargeVerbCall(token, asPeer(peer)); err != nil {
							return nil, err
						}
						return runner.RunSkillVerb(vctx, flow.SkillIdentity{
							Agent: id.Agent, Repo: id.Repo, Trigger: id.Trigger,
							Number: id.Number, Verbs: id.Policy.Verbs,
						}, uses, options)
					}
				}
				logf("skill: secret broker + verb tools enabled (per-profile skill: policy)")
			}
			memory.SetLiveOps(ops)

			// Remote (host:) skill agents reach this socket through a per-host
			// SSH reverse tunnel the dispatch path opens on demand and this
			// manager supervises for the daemon's lifetime — internal wiring
			// over the same SSH trust that launches paseo there, nothing public.
			dispatch.InitSkillTunnels(ctx, logf)
		}
	}

	// Mount connector ask surfaces (web pages, discord gateways) and fan
	// Socket Mode replies into every slack inbox — legacy handoffs included.
	wireConnectorSurfaces(ctx, stack, handoffs, cfg)

	// Serve each configured web hand-off's draft pages on the shared inbound
	// listener (once ctx exists to govern its shutdown). No-op when no web
	// hand-off is configured.
	for _, we := range handoffs.WebEntries() {
		inbound.Register(ctx, we.Listen, "/handoff", we.Chan, logf)
		logf("handoff %s: web draft pages on %s/handoff", we.Name, we.Listen)
	}

	// Start one Discord gateway per distinct bot token configured across
	// `discord:` hand-off entries. No-op when none are configured
	// (DiscordBotTokens is empty).
	for _, tok := range handoffs.DiscordBotTokens() {
		tok := tok
		go handoff.RunDiscordGateway(ctx, tok, handoffs.DiscordInbox(), logf)
	}
	if n := len(handoffs.DiscordBotTokens()); n > 0 {
		logf("handoff: %d discord bot gateway(s) starting", n)
	}

	// Write a pidfile so the `sweep` CLI can signal us; clean it up on exit.
	pidFile := pidPath(cfg)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		logf("could not write pidfile %s: %v", pidFile, err)
	} else {
		defer os.Remove(pidFile)
	}

	// SIGUSR1 → run a catch-up sweep now (and reset the adaptive cadence). Lets
	// `conductor sweep` force a sweep without waiting out the backoff.
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-usr1:
				n := 0
				for _, ig := range igs {
					if sn, ok := ig.(sweepNower); ok && sn.SweepNow() {
						n++
					}
				}
				logf("manual sweep requested (SIGUSR1) — nudged %d integration(s)", n)
			}
		}
	}()

	// Control socket: lets `force` inject a specific action for a target, and
	// `run` fire a manual trigger, into this running engine (parameters a
	// signal can't carry).
	var events *flow.EventHub
	if stack != nil {
		events = stack.Events
	}
	// Gate for MCP-callable control-socket runs (#36 §13 review, item 2): the
	// same callable opt-in + per-token scope the HTTP surface enforces, re-checked
	// at dispatch time. Local `conductor run` is unaffected (it sends no token).
	callableNames := cfg.CallableTriggerNames()
	callableTokens := map[string]config.CallableToken{}
	for _, tok := range cfg.Callable.Tokens {
		callableTokens[tok.Name] = tok
	}
	ctlGate := &callableGate{
		tokens:   callableTokens,
		callable: func(name string) bool { return callableNames[name] },
		audit:    st.Audit,
	}
	go serveControl(ctx, controlSockPath(cfg), igs, eng.Emit, manualTriggersByName(cfg), eng.RetryRunByID, events, ctlGate, logf)

	// Callable service (#36 §13): the authenticated inbound invoke surface. Off
	// unless a `callable:` block is configured; mounts on the shared inbound
	// listener (may reuse a webhook/sentry `listen:`). It dispatches agents, so
	// it flows through the same manual → policy/quiet-hours/budget/audit path as
	// `conductor run`; the entry point changes, the containment does not.
	if cfg.Callable.Enabled() {
		manual := manualTriggersByName(cfg)
		callableSet := cfg.CallableTriggerNames()
		histDir := historyDirPath(cfg)
		callable.New(callable.Deps{
			Cfg:      cfg.Callable,
			Callable: func(name string) bool { return callableSet[name] },
			Invoke: func(ctx context.Context, name string, input map[string]any, histID string) error {
				_, err := runManualTrigger(ctx, manual, controlRequest{Name: name, Inputs: input, HistoryID: histID}, eng.Emit)
				return err
			},
			ReadRun: func(id string) (store.RunHistory, bool) {
				rec, err := store.ReadHistory(histDir, id)
				return rec, err == nil
			},
			Audit: st.Audit,
			Log:   logf,
		}).Register(ctx)
	}

	// The conductor.* verbs act on THIS daemon — wire them to the existing
	// update/pause/restart/run machinery. Restarting ops fire AFTER the verb
	// returns (a short grace) so the calling step checkpoints first and the
	// workflow resumes past it on the new process.
	restartSoon := func() {
		time.AfterFunc(2*time.Second, func() { applyUpdate(stop) })
	}
	connector.SetConductorOps(&connector.ConductorOps{
		Update: func(context.Context) (bool, string, error) {
			updated, tag, err := doUpdate(false, "")
			if err != nil {
				return false, "", err
			}
			if !updated {
				return false, version, nil
			}
			if _, serr := syncServiceUnit(false); serr != nil {
				logf("conductor.update: service unit sync failed: %v", serr)
			}
			logf("conductor.update: installed %s (was %s) — restarting", tag, version)
			restartSoon()
			return true, tag, nil
		},
		Pause:   func() error { return setPaused(cfg, true) },
		Resume:  func() error { return setPaused(cfg, false) },
		Restart: func() error { restartSoon(); return nil },
		Reload:  func() error { restartSoon(); return nil }, // config loads at boot
		Run: func(ctx context.Context, name string, inputs map[string]any) (string, error) {
			return runManualTrigger(ctx, manualTriggersByName(cfg), controlRequest{Name: name, Inputs: inputs}, eng.Emit)
		},
	})
	defer connector.SetConductorOps(nil)
	// gh.sweep: the same nudge the SIGUSR1 handler runs.
	connector.SetSweepHook(func(context.Context) (int, error) {
		n := 0
		for _, ig := range igs {
			if sn, ok := ig.(sweepNower); ok && sn.SweepNow() {
				n++
			}
		}
		logf("sweep requested (gh.sweep verb) — nudged %d integration(s)", n)
		return n, nil
	})
	defer connector.SetSweepHook(nil)

	// Reaper for archive-when-done agents. It shares the hand-off hold-set so it
	// never archives an agent the engine handed off for you to drive.
	if anyArchive(cfg) {
		// One reaper per paseo dispatch surface: the primary, plus each
		// dedicated (own-bin / remote) runtime — their agents live where their
		// paseo does.
		reapers := []*dispatch.Reaper{{PaseoBin: disp.PaseoBin, Log: logf, Held: hold}}
		for _, pd := range paseoOverrides {
			reapers = append(reapers, &dispatch.Reaper{PaseoBin: pd.PaseoBin, Remote: pd.Remote, Log: logf, Held: hold})
		}
		for _, r := range reapers {
			// Testability hook (test/e2e/): shrink the reaper cadence/grace so the
			// hermetic harness can observe archive-when-done without a multi-minute
			// wait. Unset — the production case — leaves the reaper's own defaults (1m
			// interval, 3m startup grace) untouched.
			if d := envDuration("PC_REAPER_INTERVAL"); d > 0 {
				r.Interval = d
			}
			if d := envDuration("PC_REAPER_MIN_AGE"); d > 0 {
				r.MinAge = d
			}
			go r.Run(ctx)
		}
	}

	// Periodic self-update. `stop` lets it trigger a graceful shutdown so the
	// service manager relaunches into the new binary.
	if cfg.Update.Auto {
		go autoUpdateLoop(ctx, cfg.Update, cfgFile, notifier, stop)
	}
	// conductor.updated fires on the first boot of a new release.
	go emitUpdatedOnBoot(cfg, notifier)

	// Periodic activity digest (opt-in via notify.digest).
	if cfg.Notify.Digest.D() > 0 {
		go digestLoop(ctx, cfg, notifier)
	}

	// Start integrations.
	for _, ig := range igs {
		ig := ig
		go func() {
			if err := ig.Start(ctx, eng.Emit); err != nil && ctx.Err() == nil {
				logf("integration %s stopped: %v", ig.Name(), err)
			}
		}()
	}

	// Resume any workflow that was mid-flight when we last stopped.
	go eng.ResumeWorkflows(ctx)

	logf("conductor %s running (%d integration(s))", version, len(igs))
	if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// appTokener is implemented by integrations that can mint an App installation
// token (the github integration), used to re-mint on workflow resume.
type appTokener interface {
	AppToken(context.Context, int64) (string, error)
}

// refreshAppToken builds the engine's token-refresh provider: given a persisted
// trigger, find its integration and re-mint the App token from installation_id.
func refreshAppToken(igs []core.Integration) func(core.Trigger) (string, error) {
	return func(t core.Trigger) (string, error) {
		for _, ig := range igs {
			if ig.Name() != t.Instance {
				continue
			}
			at, ok := ig.(appTokener)
			if !ok {
				return "", fmt.Errorf("integration %q cannot mint app tokens", t.Instance)
			}
			instID := toInt64Any(t.Context["installation_id"])
			if instID == 0 {
				return "", fmt.Errorf("resume %s: no installation_id", t.Key())
			}
			return at.AppToken(context.Background(), instID)
		}
		return "", fmt.Errorf("no integration named %q", t.Instance)
	}
}

// completionHandler is implemented by an integration that wants to hear a
// dispatch's final outcome for triggers it emitted (the slack integration, for
// on_done/on_fail feedback).
type completionHandler interface {
	HandleCompletion(t core.Trigger, outcome string)
}

// wireSlackCompletion installs the single global core.CompletionHook, routing
// each call to whichever configured integration instance emitted the trigger
// (matched by name). A no-op when no configured integration implements
// completionHandler.
func wireSlackCompletion(igs []core.Integration) {
	core.SetCompletionHook(func(t core.Trigger, outcome string) {
		for _, ig := range igs {
			if ig.Name() != t.Instance {
				continue
			}
			if ch, ok := ig.(completionHandler); ok {
				ch.HandleCompletion(t, outcome)
			}
			return
		}
	})
}

// wireSlackHandoffInbox connects any configured `slack:` hand-off entries'
// shared Inbox to the slack integration's reply hook, so a Socket Mode
// "message" event (thread reply or DM) resolves the pending Await instead of
// being treated as ordinary chatter. A no-op when no slack hand-off is
// configured. If one is configured but no enabled `slack` integration exists
// in `integrations:`, nothing will ever call the hook — warn loudly at
// startup rather than leaving the hand-off silently stuck waiting for a
// reply that can never arrive.
func wireSlackHandoffInbox(cfg *config.Config, handoffs *handoff.Registry) {
	inbox := handoffs.SlackInbox()
	if inbox == nil {
		return
	}
	slack.SetReplyHook(func(channel, threadTS, user, text string) bool {
		return inbox.DeliverFrom(channel, threadTS, user, text)
	})
	if !anySlackIntegration(cfg) {
		logf("handoff: a slack hand-off is configured but no enabled `slack` integration is present in integrations: — replies will never be captured (add one, see README Hand-offs)")
	}
}

// anySlackIntegration reports whether integrations: configures at least one
// enabled slack instance (the Socket Mode connection that must be running for
// slack hand-off replies to be captured).
func anySlackIntegration(cfg *config.Config) bool {
	for _, ig := range cfg.Integrations {
		if ig.Type == "slack" && ig.IsEnabled() {
			return true
		}
	}
	return false
}

// dispatchTuner is implemented by an integration that carries dispatch-level
// credential + retry policy (the github integration). The first one found tunes
// the shared Dispatcher and the engine's read/write token resolvers.
type dispatchTuner interface {
	RetryPolicy() config.Retry
	IdentityTokens() (read, write, commitAuthor string)
}

// dispatchTuning derives the shared dispatch settings from the first integration
// that provides them. Token keyword resolution (values are already ${ENV}-expanded):
// write "gh_auth" (default) → `gh auth token`, else a literal token (a PAT); read
// "app" (default) → nil so reads use the per-trigger App token, "gh_auth" → `gh
// auth token`, else a literal token.
func dispatchTuning(igs []core.Integration) (retry config.Retry, write, read func() (string, error)) {
	write = userToken // default: `gh auth token`
	for _, ig := range igs {
		t, ok := ig.(dispatchTuner)
		if !ok {
			continue
		}
		retry = t.RetryPolicy()
		rd, wr, _ := t.IdentityTokens()
		if wr != "" && wr != "gh_auth" {
			lit := wr
			write = func() (string, error) { return lit, nil }
		}
		switch {
		case rd == "" || rd == "app":
			read = nil
		case rd == "gh_auth":
			read = userToken
		default:
			lit := rd
			read = func() (string, error) { return lit, nil }
		}
		break
	}
	return
}

// preflightPATH warns loudly if the tools dispatch needs aren't on PATH — a
// missing `paseo`/`gh` otherwise fails every dispatch silently (the common
// systemd --user "minimal PATH" trap). Non-fatal: the daemon still runs.
func preflightPATH(paseoBin string) {
	for _, bin := range []string{paseoBin, "gh"} {
		if _, err := exec.LookPath(bin); err != nil {
			logf("WARNING: %q not found on PATH — dispatches will fail until it's resolvable "+
				"(PATH=%s). If running as a service, reinstall/update the unit so PATH includes "+
				"~/.local/bin, or set PATH in conductor.env.", bin, os.Getenv("PATH"))
		}
	}
}

// toInt64Any coerces a JSON-decoded number (float64/int/int64) to int64.
func toInt64Any(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func cmdReplay(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	if len(rest) < 1 {
		return fmt.Errorf("replay: need a fixture path")
	}
	raw, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	var fx struct {
		Event string          `json:"event"`
		Body  json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		return fmt.Errorf("fixture must be {\"event\":..., \"body\":{...}}: %w", err)
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	type translator interface {
		Translate(context.Context, string, []byte) []core.Trigger
	}
	disp := dispatch.New(cfg.PaseoBin, config.Retry{}, true) // dry-run
	found := 0
	for _, ig := range igs {
		tr, ok := ig.(translator)
		if !ok {
			continue
		}
		for _, t := range tr.Translate(context.Background(), fx.Event, fx.Body) {
			found++
			printTrigger(cfg, disp, t)
		}
	}

	// Connectors-model triggers: translate through the lowered sources, then
	// run each matching trigger through the flow runner with DryRun stubbing
	// every verb/code/agent dispatch ("would invoke …").
	if cfg.HasConnectors() {
		stack, err := buildFlowStack(cfg, nil, nil, true) // DryRun
		if err != nil {
			return err
		}
		defer stack.Close()
		stack.Runner.Agents = flow.AgentServices{Dispatch: disp.Dispatch}
		for _, ig := range stack.Integrations {
			tr, ok := ig.(translator)
			if !ok {
				continue
			}
			for _, t := range tr.Translate(context.Background(), fx.Event, fx.Body) {
				act, _ := t.Action.(config.Action)
				if act.FlowRef == "" {
					found++
					printTrigger(cfg, disp, t)
					continue
				}
				spec, ok := stack.Runner.SpecFor(act.FlowRef)
				if !ok {
					continue
				}
				if match, err := stack.Runner.FilterMatch(t, spec); err != nil {
					logf("replay: filter error: %v", err)
					continue
				} else if !match {
					continue
				}
				found++
				fmt.Printf("• %s %s#%d [workflow: %d steps] (dry-run)\n",
					t.Kind, t.Target.Repo, t.Target.Number, len(spec.Steps))
				stack.Runner.Run(context.Background(),
					store.WorkflowRun{Outputs: map[string]map[string]any{}}, t, spec, nil, true)
			}
		}
	}
	if found == 0 {
		fmt.Println("no triggers produced (no matching rule/action, or kind needs live REST)")
	}
	return nil
}

func cmdSweep(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	// `sweep --now` signals the RUNNING daemon to run a catch-up sweep immediately
	// (bypassing the adaptive backoff). Without --now, sweep is a dry-run preview
	// that prints what a sweep would emit, in this process.
	for _, a := range rest {
		if a == "--now" {
			return signalSweepNow(cfg)
		}
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	type sweeper interface {
		SweepOnce(context.Context, core.EmitFunc) error
	}
	disp := dispatch.New(cfg.PaseoBin, config.Retry{}, true) // dry-run print
	ctx := context.Background()
	for _, ig := range igs {
		sw, ok := ig.(sweeper)
		if !ok {
			continue
		}
		emit := func(_ context.Context, t core.Trigger) { printTrigger(cfg, disp, t) }
		if err := sw.SweepOnce(ctx, emit); err != nil {
			return err
		}
	}
	return nil
}

// sweepNower is implemented by an integration whose running sweep can be triggered
// on demand (the github integration). Used by the SIGUSR1 handler in run().
type sweepNower interface{ SweepNow() bool }

// pidPath is the daemon's pidfile (a sibling of the state file), written by `run`
// and read by `sweep --now` to signal the running process.
func pidPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "conductor.pid")
}

// controlSockPath is the daemon's control socket (a sibling of the state file),
// used by `force` to inject a specific action for a target into the running engine.
func controlSockPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "control.sock")
}

type controlRequest struct {
	Cmd         string `json:"cmd"`
	Integration string `json:"integration,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Number      int    `json:"number,omitempty"`
	// run: the manual trigger's name and its CLI-provided inputs.
	Name   string         `json:"name,omitempty"`
	Inputs map[string]any `json:"inputs,omitempty"`
	// HistoryID, when set, pins the run's §20 history record id so the caller
	// can read the run back by an id it already returned (the §13 invoke
	// surface). Empty keeps the engine's own id assignment.
	HistoryID string `json:"history_id,omitempty"`
	// Callable marks a `run` that arrived via the MCP callable face (#36 §13
	// review, item 2). It flips the daemon from the ungated local-`conductor
	// run` path onto the same model the HTTP surface enforces: the trigger's
	// callable opt-in and the presenting token's scope are re-checked at
	// dispatch time and the invoke is audited. Caller is the token identity.
	// A same-user process reaching this socket already has ungated `run`; the
	// gate exists so the legitimate MCP face is held to the token model, not as
	// a new boundary against a local attacker.
	Callable bool   `json:"callable,omitempty"`
	Caller   string `json:"caller,omitempty"`
	// retry: the recorded run to re-run and the step to resume from
	// ("" = the recorded failed step, else the beginning); ForceReplay
	// permits re-running steps the record says already succeeded. (#36 §20)
	// watch: RunID filters the live event stream ("" = every run). (#36 §17)
	RunID       string `json:"run_id,omitempty"`
	FromStep    string `json:"from_step,omitempty"`
	ForceReplay bool   `json:"force_replay,omitempty"`
}

type controlResponse struct {
	OK         bool   `json:"ok"`
	Dispatched int    `json:"dispatched,omitempty"`
	Msg        string `json:"msg,omitempty"`
	Error      string `json:"error,omitempty"`
}

// callableGate authorizes + audits a control-socket `run` that the MCP callable
// face marked req.Callable (#36 §13 review, item 2). The MCP face is a separate
// process that reaches the daemon over the same-user control socket; without
// this gate it would dispatch straight to runManualTrigger, bypassing the token
// scope, the run-time callable opt-in re-check, and the callable_invoke audit
// the HTTP surface enforces. Only req.Callable runs are gated — local
// `conductor run` (req.Callable false) stays ungated.
type callableGate struct {
	tokens   map[string]config.CallableToken // caller identity → its workflow scope
	callable func(string) bool               // run-time `callable: true` opt-in re-check
	audit    func(map[string]any)
}

// authorize re-checks an MCP-originated run at dispatch time: the trigger must
// still be callable and the presenting token still scoped to it. Returns nil if
// the run may proceed. Deny-by-default: an unknown token or a missing gate
// refuses.
func (g *callableGate) authorize(caller, name string) error {
	if g == nil {
		return fmt.Errorf("callable invoke not available")
	}
	if !g.callable(name) {
		return fmt.Errorf("workflow %q is not callable", name)
	}
	tok, ok := g.tokens[caller]
	if !ok {
		return fmt.Errorf("unknown callable token %q", caller)
	}
	if !tok.Allows(name) {
		return fmt.Errorf("token %q is not scoped to invoke %q", caller, name)
	}
	return nil
}

// serveControl runs the daemon's unix control socket until ctx is cancelled.
func serveControl(ctx context.Context, path string, igs []core.Integration, emit core.EmitFunc, manual map[string]connector.CompiledTrigger, retry retryFunc, events *flow.EventHub, gate *callableGate, log func(string, ...any)) {
	_ = os.Remove(path) // clear a stale socket from a prior run
	l, err := net.Listen("unix", path)
	if err != nil {
		log("control socket %s: %v", path, err)
		return
	}
	// Same-user only, like the egress proxy (proxy.go) and memory IPC
	// (memory/ipc.go) sockets: this socket serves `retry --force-replay`
	// (replays side effects) and `watch` (every run's live events), so any
	// local process reaching it is a privilege boundary. Fail closed if the
	// mode can't be tightened rather than serve a world-reachable socket.
	if err := os.Chmod(path, 0o600); err != nil {
		log("control socket %s perms: %v", path, err)
		l.Close()
		os.Remove(path)
		return
	}
	go func() { <-ctx.Done(); l.Close(); os.Remove(path) }()
	log("control socket listening at %s", path)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go handleControlConn(ctx, conn, igs, emit, manual, retry, events, gate, log)
	}
}

// retryFunc re-runs a recorded execution from a step (#36 §20) — the
// engine's RetryRunByID.
type retryFunc func(ctx context.Context, runID, fromStep string, forceReplay bool) (string, error)

func handleControlConn(ctx context.Context, conn net.Conn, igs []core.Integration, emit core.EmitFunc, manual map[string]connector.CompiledTrigger, retry retryFunc, events *flow.EventHub, gate *callableGate, log func(string, ...any)) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var req controlRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeControlResp(conn, controlResponse{Error: "bad request: " + err.Error()})
		return
	}
	switch req.Cmd {
	case "force":
		n, err := forceOnIntegrations(ctx, igs, req, emit)
		if err != nil {
			log("force %s %s#%d failed: %v", req.Kind, req.Repo, req.Number, err)
			writeControlResp(conn, controlResponse{Error: err.Error()})
			return
		}
		log("forced %d %s trigger(s) for %s#%d (via control socket)", n, req.Kind, req.Repo, req.Number)
		writeControlResp(conn, controlResponse{OK: true, Dispatched: n,
			Msg: fmt.Sprintf("forced %d %s trigger(s) for %s#%d", n, req.Kind, req.Repo, req.Number)})
	case "run":
		// A run marked req.Callable arrived via the MCP callable face: hold it to
		// the same token model as the HTTP surface (#36 §13 review, item 2) —
		// re-check the callable opt-in + the token's scope at dispatch time, so
		// removing `callable:` or narrowing the token revokes reachability even
		// while the MCP process stays up. Local `conductor run` is req.Callable
		// false and stays ungated.
		if req.Callable {
			if err := gate.authorize(req.Caller, req.Name); err != nil {
				log("callable run %q refused for %q: %v", req.Name, req.Caller, err)
				writeControlResp(conn, controlResponse{Error: err.Error()})
				return
			}
		}
		msg, err := runManualTrigger(ctx, manual, req, emit)
		if err != nil {
			log("run %q failed: %v", req.Name, err)
			writeControlResp(conn, controlResponse{Error: err.Error()})
			return
		}
		if req.Callable && gate != nil {
			gate.audit(map[string]any{
				"event": "callable_invoke", "caller": req.Caller, "workflow": req.Name, "run_id": req.HistoryID,
			})
		}
		log("manual trigger %q dispatched (via control socket)", req.Name)
		writeControlResp(conn, controlResponse{OK: true, Dispatched: 1, Msg: msg})
	case "watch":
		if events == nil {
			writeControlResp(conn, controlResponse{Error: "live events need the connectors model (no flow runner configured)"})
			return
		}
		streamRunEvents(ctx, conn, events, req.RunID, log)
	case "retry":
		if retry == nil {
			writeControlResp(conn, controlResponse{Error: "retry is not available (no flow runner configured)"})
			return
		}
		msg, err := retry(ctx, req.RunID, req.FromStep, req.ForceReplay)
		if err != nil {
			log("retry %q failed: %v", req.RunID, err)
			writeControlResp(conn, controlResponse{Error: err.Error()})
			return
		}
		log("retry %q dispatched (via control socket)", req.RunID)
		writeControlResp(conn, controlResponse{OK: true, Dispatched: 1, Msg: msg})
	default:
		writeControlResp(conn, controlResponse{Error: "unknown control command: " + req.Cmd})
	}
}

// runManualTrigger fires an `on: manual` trigger by name: the CLI inputs land
// in the trigger context (top-level and under `inputs`) and the run flows
// through the same policy/quiet-hours/audit path as any other firing.
func runManualTrigger(ctx context.Context, manual map[string]connector.CompiledTrigger, req controlRequest, emit core.EmitFunc) (string, error) {
	ct, ok := manual[req.Name]
	if !ok {
		names := make([]string, 0, len(manual))
		for n := range manual {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "", fmt.Errorf("no manual trigger named %q (no trigger declares `on: manual`)", req.Name)
		}
		return "", fmt.Errorf("no manual trigger named %q (manual triggers: %s)", req.Name, strings.Join(names, ", "))
	}
	inputs := req.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	trigCtx := map[string]any{"inputs": inputs}
	for k, v := range inputs {
		if k != "inputs" {
			trigCtx[k] = v
		}
	}
	act := config.Action{Name: ct.Spec.Name, Enabled: ct.Spec.Enabled, Shadow: ct.Spec.Shadow, FlowRef: ct.Ref()}
	act.TargetRepo = ct.Spec.Repo
	act = inbound.ForceNoCheckout(act)
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	emit(ctx, core.Trigger{
		Source:    "manual",
		Instance:  "manual",
		Kind:      "manual",
		Variant:   ct.Spec.Name,
		Target:    inbound.SyntheticTarget("manual:"+ct.Spec.Name, runID),
		Title:     "manual run: " + ct.Spec.Name,
		Context:   trigCtx,
		Force:     true, // bypass dedup gates — an on-demand run always runs
		Action:    act,
		HistoryID: req.HistoryID, // "" unless a caller (the §13 invoke surface) pinned it
	})
	return fmt.Sprintf("dispatched manual trigger %q", req.Name), nil
}

// manualTriggersByName indexes the `on: manual` triggers for `conductor run`
// (names are load-validated to exist and be unique).
func manualTriggersByName(cfg *config.Config) map[string]connector.CompiledTrigger {
	m := map[string]connector.CompiledTrigger{}
	for i, spec := range cfg.Triggers {
		if spec.Manual() && spec.Name != "" {
			m[spec.Name] = connector.CompiledTrigger{Index: i, Spec: spec}
		}
	}
	return m
}

func writeControlResp(conn net.Conn, resp controlResponse) {
	_ = json.NewEncoder(conn).Encode(resp)
}

// forceOnIntegrations runs Force on the matching integration(s). With an explicit
// integration name only that one; otherwise it tries each Forcer and returns the
// first success (so a repo is routed to whichever integration configures it).
func forceOnIntegrations(ctx context.Context, igs []core.Integration, req controlRequest, emit core.EmitFunc) (int, error) {
	if req.Kind == "" || req.Repo == "" || req.Number == 0 {
		return 0, fmt.Errorf("force needs kind, repo and number")
	}
	matched := false
	var errs []string
	for _, ig := range igs {
		if req.Integration != "" && ig.Name() != req.Integration {
			continue
		}
		f, ok := ig.(core.Forcer)
		if !ok {
			continue
		}
		matched = true
		n, err := f.Force(ctx, req.Kind, req.Repo, req.Number, emit)
		if err != nil {
			errs = append(errs, ig.Name()+": "+err.Error())
			continue
		}
		return n, nil // first integration that handled it wins
	}
	if !matched {
		return 0, fmt.Errorf("no integration supports force (integration=%q)", req.Integration)
	}
	return 0, fmt.Errorf("force failed: %s", strings.Join(errs, "; "))
}

// cmdForce sends a force request to the running daemon over its control socket:
//
//	conductor force <kind> <owner/repo>#<number> [--integration NAME]
func cmdForce(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	var integration string
	var pos []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--integration" && i+1 < len(rest) {
			integration = rest[i+1]
			i++
			continue
		}
		pos = append(pos, rest[i])
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: conductor force <kind> <owner/repo>#<number> [--integration NAME]")
	}
	repo, number, err := parsePRRef(pos[1])
	if err != nil {
		return err
	}
	resp, err := sendControl(cfg, controlRequest{Cmd: "force", Integration: integration,
		Kind: pos[0], Repo: repo, Number: number})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println(resp.Msg)
	return nil
}

// hasPositional reports whether args carry a non-flag token, skipping the
// value of every value-taking flag `conductor run` understands.
func hasPositional(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "--input", "--json":
			i++ // skip the flag's value
		default:
			if !strings.HasPrefix(args[i], "--") {
				return true
			}
		}
	}
	return false
}

// cmdRunTrigger fires a manual trigger through the running daemon:
//
//	conductor run <name> [--input k=v ...] [--json '{…}']
//
// --json supplies structured inputs; --input k=v string entries overlay them.
func cmdRunTrigger(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	var name string
	inputs := map[string]any{}
	kvs := map[string]any{}
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "--input":
			if i+1 >= len(rest) {
				return fmt.Errorf("--input wants key=value")
			}
			k, v, ok := strings.Cut(rest[i+1], "=")
			if !ok || k == "" {
				return fmt.Errorf("--input wants key=value, got %q", rest[i+1])
			}
			kvs[k] = v
			i++
		case rest[i] == "--json":
			if i+1 >= len(rest) {
				return fmt.Errorf("--json wants a JSON object")
			}
			if err := json.Unmarshal([]byte(rest[i+1]), &inputs); err != nil {
				return fmt.Errorf("--json: %w", err)
			}
			i++
		case strings.HasPrefix(rest[i], "--"):
			return fmt.Errorf("unknown flag %q (usage: conductor run <name> [--input k=v ...] [--json '{…}'])", rest[i])
		case name == "":
			name = rest[i]
		default:
			return fmt.Errorf("unexpected argument %q — one trigger name only", rest[i])
		}
	}
	if name == "" {
		return fmt.Errorf("usage: conductor run <name> [--input k=v ...] [--json '{…}']")
	}
	for k, v := range kvs {
		inputs[k] = v
	}
	// `conductor run <id>` (#36 §20): an argument that isn't a configured
	// manual trigger but names a RECORDED execution shows its detail — a
	// configured trigger name always wins, and inspection works with the
	// daemon down.
	if _, isTrigger := manualTriggersByName(cfg)[name]; !isTrigger && len(inputs) == 0 {
		if _, err := store.ReadHistory(historyDirPath(cfg), name); err == nil {
			return printRunDetail(historyDirPath(cfg), name)
		}
	}
	resp, err := sendControl(cfg, controlRequest{Cmd: "run", Name: name, Inputs: inputs})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println(resp.Msg)
	return nil
}

// parsePRRef splits "owner/name#N" into ("owner/name", N).
func parsePRRef(s string) (repo string, number int, err error) {
	i := strings.LastIndex(s, "#")
	if i <= 0 {
		return "", 0, fmt.Errorf("target must be owner/repo#N, got %q", s)
	}
	repo = s[:i]
	number, err = strconv.Atoi(s[i+1:])
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("bad number in %q (want owner/repo#N)", s)
	}
	return repo, number, nil
}

// sendControl dials the daemon control socket and does one request/response.
func sendControl(cfg *config.Config, req controlRequest) (controlResponse, error) {
	p := controlSockPath(cfg)
	conn, err := net.Dial("unix", p)
	if err != nil {
		return controlResponse{}, fmt.Errorf("connect to daemon control socket %s — is it running? %w", p, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return controlResponse{}, err
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return controlResponse{}, err
	}
	return resp, nil
}

// signalSweepNow tells the running daemon to run a catch-up sweep immediately by
// sending it SIGUSR1 (pid read from the pidfile).
func signalSweepNow(cfg *config.Config) error {
	p := pidPath(cfg)
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("read pidfile %s — is the daemon running? %w", p, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("bad pidfile %s: %w", p, err)
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal daemon (pid %d): %w", pid, err)
	}
	fmt.Printf("signaled conductor (pid %d) to run a sweep now\n", pid)
	return nil
}

// printTrigger renders what a trigger would dispatch (dry-run).
func printTrigger(cfg *config.Config, disp *dispatch.Dispatcher, t core.Trigger) {
	act, _ := t.Action.(config.Action)

	if len(act.Steps) > 0 {
		fmt.Printf("• %s %s#%d [workflow: %d steps]\n", t.Kind, t.Target.Repo, t.Target.Number, len(act.Steps))
		for i, step := range act.Steps {
			id := step.ID
			if id == "" {
				id = fmt.Sprintf("step%d", i+1)
			}
			if step.If != "" {
				fmt.Printf("    - %s (if: %s) [%s]\n", id, step.If, step.Type)
			} else {
				fmt.Printf("    - %s [%s]\n", id, step.Type)
			}
			printOneDispatch(cfg, disp, t, step, "        ")
		}
		return
	}

	fmt.Printf("• %s %s#%d [%s]\n", t.Kind, t.Target.Repo, t.Target.Number, act.Type)
	printOneDispatch(cfg, disp, t, act, "    ")
}

func printOneDispatch(cfg *config.Config, disp *dispatch.Dispatcher, t core.Trigger, act config.Action, indent string) {
	req := dispatch.Request{Trigger: t, Action: act, Identity: act.Agent,
		Author: gitAuthor(), Shadow: true, Wait: !act.Background}
	ref, err := disp.Dispatch(context.Background(), req)
	if err != nil {
		fmt.Printf("%serror: %v\n", indent, err)
		return
	}
	fmt.Printf("%s%s\n", indent, strings.Join(ref.Argv, " "))
}

// userToken returns your `gh auth token`, memoized.
var (
	tokOnce sync.Once
	tokVal  string
	tokErr  error
)

func userToken() (string, error) {
	tokOnce.Do(func() {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			tokErr = fmt.Errorf("gh auth token: %w", err)
			return
		}
		tokVal = strings.TrimSpace(string(out))
	})
	return tokVal, tokErr
}

// gitAuthor reads your git identity for commit attribution.
func gitAuthor() dispatch.Author {
	name := gitConfig("user.name")
	email := gitConfig("user.email")
	return dispatch.Author{Name: name, Email: email}
}

func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// envDuration parses a duration from an env var, returning 0 when unset or
// invalid. Used only by the test/e2e/ reaper-cadence testability hook.
func envDuration(key string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

func anyArchive(cfg *config.Config) bool {
	found := false
	cfg.WalkSteps(func(_ config.IdentityScope, _ int, s *config.Step) {
		if s.ArchiveWhenDone {
			found = true
		}
	})
	return found
}

// logRedact scrubs tracked secret values from every journal line. logf is
// the ONE logger handed to engine/flow/dispatch/notify/affinity/memory/
// handoffs/control, so redacting here covers all of them at a single choke
// point; it's atomic because subsystems log from many goroutines while main
// wires the resolver once at boot.
var logRedact atomic.Pointer[secrets.Resolver]

// setLogRedactor wires the secrets resolver into the shared logger.
func setLogRedactor(r *secrets.Resolver) { logRedact.Store(r) }

func logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	if r := logRedact.Load(); r != nil {
		line = r.Redact(line)
	}
	fmt.Fprintln(os.Stderr, line)
}

// loadEnvFile loads simple KEY=VALUE lines from path into the environment
// (best-effort; missing file is fine). Existing env vars are not overridden.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}
