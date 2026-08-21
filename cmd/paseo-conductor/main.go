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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/engine"
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

func cmdValidate(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		return err
	}
	for _, ig := range igs {
		if err := ig.Validate(); err != nil {
			return err
		}
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
	for _, ig := range igs {
		if err := ig.Validate(); err != nil {
			return err
		}
	}

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
	notifier := notify.New(cfg.Notify, logf)
	// Shared "never reap" set for interactive hand-off agents: the engine registers
	// a background step's agent at launch; the reaper skips anything in it.
	hold := dispatch.NewHoldSet()
	eng := engine.New(engine.Options{
		Config: cfg, Store: st, Dispatch: disp, Notifier: notifier,
		Author: gitAuthor(), UserToken: writeTok, ReadToken: readTok, Log: logf,
		RefreshAppToken: refreshAppToken(igs), Hold: hold,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Reaper for archive-when-done agents. It shares the hand-off hold-set so it
	// never archives an agent the engine handed off for you to drive.
	if anyArchive(cfg) {
		r := &dispatch.Reaper{PaseoBin: disp.PaseoBin, Log: logf, Held: hold}
		go r.Run(ctx)
	}

	// Periodic self-update. `stop` lets it trigger a graceful shutdown so the
	// service manager relaunches into the new binary.
	if cfg.Update.Auto {
		go autoUpdateLoop(ctx, cfg.Update, stop)
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
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
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
