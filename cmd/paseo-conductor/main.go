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

	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/cron"   // register "cron"
	_ "github.com/NodeSpy/paseo-conductor/internal/integrations/github" // register "github"
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
	// Load secrets from conductor.env (sibling of the config) into the
	// environment before config expansion. This lets launchd — which has no
	// EnvironmentFile — pick up secrets the same way systemd does.
	cfgPath, _ := configPath(args)
	loadEnvFile(filepath.Join(filepath.Dir(cfgPath), "conductor.env"))

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

	disp := dispatch.New(cfg.Dispatch, cfg.DryRun)
	notifier := notify.New(cfg.Notify, userToken, logf)
	eng := engine.New(engine.Options{
		Config: cfg, Store: st, Dispatch: disp, Notifier: notifier,
		Author: gitAuthor(), UserToken: userToken, Log: logf,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Reaper for archive-when-done agents.
	if anyArchive(cfg) {
		r := &dispatch.Reaper{PaseoBin: disp.PaseoBin, Log: logf}
		go r.Run(ctx)
	}

	// Periodic self-update.
	if cfg.Update.Auto {
		go autoUpdateLoop(ctx, cfg.Update)
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

	logf("paseo-conductor %s running (%d integration(s))", version, len(igs))
	if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
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
	disp := dispatch.New(cfg.Dispatch, true) // dry-run
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
	disp := dispatch.New(cfg.Dispatch, true) // dry-run print
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
	var profile config.AgentProfile
	if act.Type == "agent" {
		profile = cfg.Agents[act.Agent]
	}
	req := dispatch.Request{Trigger: t, Action: act, Profile: profile, Author: gitAuthor(), Shadow: true}
	ref, err := disp.Dispatch(context.Background(), req)
	fmt.Printf("• %s %s#%d [%s]\n", t.Kind, t.Target.Repo, t.Target.Number, act.Type)
	if err != nil {
		fmt.Printf("    error: %v\n", err)
		return
	}
	fmt.Printf("    %s\n", strings.Join(ref.Argv, " "))
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
