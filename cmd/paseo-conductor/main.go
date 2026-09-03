// Command paseo-conductor is an event-driven agent orchestrator for a local
// Paseo daemon. It receives GitHub App webhooks (via smee), matches them to
// rules, and dispatches coding agents / commands.
//
// Subcommands: run | validate | replay <event.json> | sweep | version.
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/controller"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/engine"
	"github.com/NodeSpy/paseo-conductor/internal/handoff"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
	"github.com/NodeSpy/paseo-conductor/internal/store"

	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/cron"      // register "cron"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/github"    // register "github"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/pagerduty" // register "pagerduty"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/rss"       // register "rss"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/sentry"    // register "sentry"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/slack"     // register "slack"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/webhook"   // register "webhook"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
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
	case "pause":
		err = cmdPause(args, true)
	case "resume":
		err = cmdPause(args, false)
	case "update":
		err = cmdUpdate(args)
	case "service":
		err = cmdService(args)
	case "version", "-v", "--version":
		fmt.Println("paseo-conductor", version)
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
	fmt.Fprint(os.Stderr, `paseo-conductor — event-driven agent orchestration for your Paseo daemon

usage:
  paseo-conductor run [--config PATH]         start the daemon
  paseo-conductor validate [--config PATH]    load & validate config, then exit
  paseo-conductor replay <event.json> [--config PATH]  run a saved webhook through the pipeline (dry-run)
  paseo-conductor sweep [--config PATH]       one catch-up sweep (dry-run print)
  paseo-conductor sweep --now [--config PATH] signal the running daemon to sweep now
  paseo-conductor force <kind> <owner/repo>#<n>  force an action for a target now (via the running daemon)
  paseo-conductor status [--config PATH]      snapshot: live agents, in-flight workflows, stuck/attention
  paseo-conductor report [--days N]           activity summary: dispatches by kind/outcome + attention
  paseo-conductor pause | resume              stop / resume dispatch at runtime (no restart)
  paseo-conductor update [--force] [--tag vX]  self-update to the latest release (uses gh)
  paseo-conductor service install|sync|uninstall  manage the background service unit
  paseo-conductor version
`)
}

// configPath extracts --config from args (default ~/.config/paseo-conductor/config.yaml).
func configPath(args []string) (string, []string) {
	def := expandHome("~/.config/paseo-conductor/config.yaml")
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
	fmt.Printf("ok: %d integration(s) configured (%d enabled), %d agent profile(s)\n",
		len(cfg.Integrations), len(igs), len(cfg.Agents))
	return nil
}

func cmdRun(args []string) error {
	// loadConfig loads the sibling conductor.env first, so ${...} refs resolve
	// (this is also how launchd — which has no EnvironmentFile — gets secrets).
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
	// Wire the engine's dispatch-completion seam to every configured slack
	// instance's on_done/on_fail handling (see core.SetCompletionHook and
	// slack.Integration.HandleCompletion). A no-op when no slack integration is
	// configured. Sibling seam to slack.SetReplyHook (hand-off thread replies).
	wireSlackCompletion(igs)

	st, err := store.Open(store.Options{
		StatePath:    cfg.Store.StateFile,
		AuditPath:    cfg.Store.AuditLog,
		TTL:          cfg.Store.StateTTL.D(),
		MaxPRs:       cfg.Store.MaxTrackedPRs,
		AuditMaxSize: cfg.Store.AuditMaxSize.Bytes(),
	})
	if err != nil {
		return err
	}
	defer st.Close()

	retry, writeTok, readTok := dispatchTuning(igs)
	disp := dispatch.New(cfg.PaseoBin, retry, cfg.DryRun)
	disp.AdoptOpenWorkspaces = cfg.AdoptOpenWorkspaces
	preflightPATH(disp.PaseoBin)
	notifier := notify.New(cfg.Notify, logf, st.Audit)
	// Controller registry (paseo is the built-in default) + the session broker that
	// owns one live session per PR — so an interactive hand-off survives a restart
	// and follow-ups funnel to the live session instead of a duplicate agent. Built
	// here so the broker and the engine share one registry. With no `controllers:`
	// block this resolves to paseo everywhere, unchanged.
	var paseoSender controller.Sender = disp
	reg := controller.NewRegistry(cfg.Controllers, cfg.DefaultControllerName(), disp, paseoSender)
	broker := controller.NewBroker(reg, st, logf)
	// Hand-off registry: resolves the named `handoffs:` map (config.Load already
	// folded a legacy singular `handoff:` block into Handoffs["default"], so this
	// is the only path needed here). Empty map → Resolve always yields nil → the
	// review hand-off keeps today's paseo-native behavior.
	handoffs := handoff.NewRegistry(cfg.Handoffs, cfg.DefaultHandoffName(), logf)
	// Shared "never reap" set for interactive hand-off agents: the engine registers
	// a background step's agent at launch; the reaper skips anything in it.
	hold := dispatch.NewHoldSet(filepath.Join(filepath.Dir(cfg.Store.StateFile), "holds.json"))
	eng := engine.New(engine.Options{
		Config: cfg, Store: st, Dispatch: disp, Controllers: reg, Broker: broker, Handoffs: handoffs,
		Notifier: notifier, Author: gitAuthor(), UserToken: writeTok, ReadToken: readTok, Log: logf,
		RefreshAppToken: refreshAppToken(igs), Hold: hold, PausePath: pausePath(cfg),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Serve each configured web hand-off's draft pages on the shared inbound
	// listener (once ctx exists to govern its shutdown). No-op when no web
	// hand-off is configured.
	for _, we := range handoffs.WebEntries() {
		inbound.Register(ctx, we.Listen, "/handoff", we.Chan, logf)
		logf("handoff %s: web draft pages on %s/handoff", we.Name, we.Listen)
	}

	// Write a pidfile so the `sweep` CLI can signal us; clean it up on exit.
	pidFile := pidPath(cfg)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		logf("could not write pidfile %s: %v", pidFile, err)
	} else {
		defer os.Remove(pidFile)
	}

	// SIGUSR1 → run a catch-up sweep now (and reset the adaptive cadence). Lets
	// `paseo-conductor sweep` force a sweep without waiting out the backoff.
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

	// Control socket: lets `force` inject a specific action for a target into this
	// running engine (parameters a signal can't carry).
	go serveControl(ctx, controlSockPath(cfg), igs, eng.Emit, logf)

	// Reaper for archive-when-done agents. It shares the hand-off hold-set so it
	// never archives an agent the engine handed off for you to drive.
	if anyArchive(cfg) {
		r := &dispatch.Reaper{PaseoBin: disp.PaseoBin, Log: logf, Held: hold}
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

	// Periodic self-update. `stop` lets it trigger a graceful shutdown so the
	// service manager relaunches into the new binary.
	if cfg.Update.Auto {
		go autoUpdateLoop(ctx, cfg.Update, stop)
	}

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

	logf("paseo-conductor %s running (%d integration(s))", version, len(igs))
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
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "paseo-conductor.pid")
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
}

type controlResponse struct {
	OK         bool   `json:"ok"`
	Dispatched int    `json:"dispatched,omitempty"`
	Msg        string `json:"msg,omitempty"`
	Error      string `json:"error,omitempty"`
}

// serveControl runs the daemon's unix control socket until ctx is cancelled.
func serveControl(ctx context.Context, path string, igs []core.Integration, emit core.EmitFunc, log func(string, ...any)) {
	_ = os.Remove(path) // clear a stale socket from a prior run
	l, err := net.Listen("unix", path)
	if err != nil {
		log("control socket %s: %v", path, err)
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
		go handleControlConn(ctx, conn, igs, emit, log)
	}
}

func handleControlConn(ctx context.Context, conn net.Conn, igs []core.Integration, emit core.EmitFunc, log func(string, ...any)) {
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
	default:
		writeControlResp(conn, controlResponse{Error: "unknown control command: " + req.Cmd})
	}
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
//	paseo-conductor force <kind> <owner/repo>#<number> [--integration NAME]
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
		return fmt.Errorf("usage: paseo-conductor force <kind> <owner/repo>#<number> [--integration NAME]")
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
	fmt.Printf("signaled paseo-conductor (pid %d) to run a sweep now\n", pid)
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
	var profile config.AgentProfile
	if act.Type == "agent" {
		profile = cfg.Agents[act.Agent]
	}
	req := dispatch.Request{Trigger: t, Action: act, Profile: profile, Author: gitAuthor(), Shadow: true, Wait: !act.Background}
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
	for _, p := range cfg.Agents {
		if p.ArchiveWhenDone {
			return true
		}
	}
	return false
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
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

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
