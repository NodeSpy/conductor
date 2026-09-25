package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/engine"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/gitwt"
	"github.com/NodeSpy/conductor/internal/hosts"
	agentmodels "github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// ONE-SHOT MODE (docs/design/conductor-in-github-actions.md, "the keystone").
//
// `conductor once <trigger>` takes ONE event, matches it to ONE named trigger,
// executes that trigger's steps FOR REAL, in-process, synchronously, to
// completion, and exits with the outcome. It is the entry point an ephemeral
// runner — a GitHub Actions job — uses: the job already holds the event that
// fired it (`$GITHUB_EVENT_PATH`), a checkout, and a write token, so all that
// was missing was a way into the pipeline that does not route through a
// running daemon's control socket.
//
// It is `replay` made real, minus the daemon:
//
//	replay                          once
//	-------------------------------------------------------------------
//	DryRun=true everywhere          DryRun=false — steps actually run
//	every trigger the event yields  ONE named trigger (design O1)
//	no store / no notifier          an EPHEMERAL store + a live notifier
//	no engine                       engine.New wires the runner's
//	                                AgentServices (runtime resolution,
//	                                tokens, guidance) — but Engine.Run is
//	                                NEVER called
//	prints "would …"                streams real step/gate results
//	always exit 0                   exit code = outcome (design O3)
//
// And it is `run` (the daemon) minus every background loop. NOTHING here
// starts: the control socket, the sweep, the auto-update loop, the digest
// loop, the reaper(s), the git-worktree orphan sweep, the webhook/smee
// watchers, the memory IPC socket, the callable HTTP service, the Discord
// gateways, the inbound listener, or the engine's own event loop. A runner
// processes one event and dies; a loop that outlived the run would either
// hang the job or be killed mid-flight.
//
// paseo is a config ERROR here, not a fallback (design non-goal): its agents
// are a separate daemon's children and that daemon is not in the runner. So
// is an interactive hand-off, which has no human to hand off to in CI — see
// onceUnsupportedSteps.

// Exit codes. 0 and 1 keep their existing meanings across the CLI (success;
// "error: …" on stderr); 3 is one-shot's own: conductor ran fine, the WORK
// did not. A job can therefore tell "the trigger rejected the change" from
// "conductor could not start".
const (
	onceExitOK      = 0 // the run passed, or the event did not match
	onceExitOutcome = 3 // a fail-on outcome fired (step error / gate rejection)
)

// The fail-on outcome categories (design O3). `--fail-on` takes a
// comma-separated subset; the default is every category there is, which is
// also the least surprising reading of "the job failed".
const (
	failOnStepError  = "step-error"
	failOnGateReject = "gate-reject"
)

var onceFailOnCategories = []string{failOnStepError, failOnGateReject}

// exitError carries a specific process exit status up to main, which would
// otherwise map every returned error onto 1. Its message is printed by the
// caller only when non-empty — a run that already streamed its failure to the
// job log does not need an "error:" line repeating it.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// onceOptions are `conductor once`'s own flags, after configPath has taken
// --config/--state-dir.
type onceOptions struct {
	trigger      string
	eventPath    string   // raw event body (an Actions $GITHUB_EVENT_PATH payload)
	eventName    string   // the webhook event name ($GITHUB_EVENT_NAME)
	fixturePath  string   // a replay-style {"event":…,"body":…} fixture
	failOn       []string // outcome categories that make the job fail
	requireMatch bool     // a non-matching event is an ERROR, not a pass
	// durableState is set when the operator pointed state somewhere on
	// purpose (--state-dir, or a config that sets store.state_file). Without
	// it one-shot mode runs on a throwaway state dir (design O4).
	durableState bool
	// stdout/stderr are injectable so the tests can read the job log without
	// racing on the process-wide os.Stdout.
	stdout, stderr func(string)
}

func onceUsage() {
	fmt.Fprint(os.Stderr, `conductor once — run ONE event through ONE trigger, for real, then exit

usage:
  conductor once <trigger> [flags]

Loads the config, takes a single event, matches it to the named trigger, and
executes that trigger's steps in this process to completion. No daemon, no
background loops (no sweep, no webhook watcher, no auto-update, no reaper, no
control socket). Built for an ephemeral runner — a GitHub Actions job.

flags:
  --event PATH        the raw event body (default $GITHUB_EVENT_PATH)
  --event-name NAME   the event name, e.g. pull_request (default $GITHUB_EVENT_NAME)
  --fixture PATH      a replay fixture {"event": "...", "body": {...}} instead
                      of --event/--event-name (for local testing)
  --config PATH       config file (default ~/.config/conductor/config.yaml)
  --state-dir PATH    keep state here instead of a throwaway directory
  --fail-on LIST      comma-separated outcomes that fail the job
                      (default step-error,gate-reject; "none" never fails)
  --require-match     exit non-zero when the event does not match the trigger
                      (default: a non-match is a PASS — the job fired, the
                      trigger's predicate simply did not hold)

exit codes:
  0  the run succeeded, or the event did not match the trigger
  1  conductor could not run (bad flags, bad config, unsupported step)
  3  the run produced a --fail-on outcome

Agent credentials (ANTHROPIC_API_KEY, …) and the write token
(identity.write_token — GITHUB_TOKEN in Actions) come from the environment.
The paseo runtime is NOT available in one-shot mode; use cli runtimes, engine
plugins, verbs, and commands.
`)
}

// cmdOnce is the `once` subcommand entry point.
func cmdOnce(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			onceUsage()
			return nil
		}
	}
	opts, rest, err := parseOnceFlags(args)
	if err != nil {
		return err
	}
	cfg, rest, err := loadConfig(rest)
	if err != nil {
		return err
	}
	switch len(rest) {
	case 0:
		onceUsage()
		return fmt.Errorf("once: name the trigger to run")
	case 1:
		opts.trigger = rest[0]
	default:
		return fmt.Errorf("once takes ONE trigger name; got %d positional arguments: %s", len(rest), strings.Join(rest, " "))
	}
	// An operator who pointed store.state_file somewhere meant it; otherwise
	// one-shot state is throwaway (design O4).
	if !opts.durableState && cfg.Store.StateFile != filepath.Join(config.StateDir(), "state.json") {
		opts.durableState = true
	}
	return runOnce(context.Background(), cfg, opts)
}

// parseOnceFlags consumes `once`'s own flags and passes everything else
// (--config, --state-dir, the trigger name) through to loadConfig.
func parseOnceFlags(args []string) (onceOptions, []string, error) {
	o := onceOptions{
		eventPath: os.Getenv("GITHUB_EVENT_PATH"),
		eventName: os.Getenv("GITHUB_EVENT_NAME"),
		failOn:    append([]string(nil), onceFailOnCategories...),
	}
	var rest []string
	need := func(i int, flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("once: %s needs a value", flag)
		}
		return args[i+1], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "--event":
			o.eventPath, err = need(i, "--event")
			i++
		case "--event-name":
			o.eventName, err = need(i, "--event-name")
			i++
		case "--fixture":
			o.fixturePath, err = need(i, "--fixture")
			i++
		case "--fail-on":
			var v string
			if v, err = need(i, "--fail-on"); err == nil {
				o.failOn, err = parseFailOn(v)
			}
			i++
		case "--require-match":
			o.requireMatch = true
		case "--state-dir":
			// configPath consumes this; note it so the state stays durable.
			o.durableState = true
			rest = append(rest, args[i])
		default:
			rest = append(rest, args[i])
		}
		if err != nil {
			return o, nil, err
		}
	}
	return o, rest, nil
}

// parseFailOn validates a --fail-on list. "none" is the explicit empty set —
// report everything, fail on nothing — for a job that wants the run's output
// without gating on it.
func parseFailOn(v string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		switch part {
		case "":
			continue
		case "none":
			return nil, nil
		case failOnStepError, failOnGateReject:
			out = append(out, part)
		default:
			return nil, fmt.Errorf("once: unknown --fail-on category %q (known: %s, or none)",
				part, strings.Join(onceFailOnCategories, ", "))
		}
	}
	return out, nil
}

// onceEvent is the event a one-shot run processes: the webhook event NAME and
// the raw body, exactly the pair every source integration's Translate takes.
type onceEvent struct {
	Name string
	Body json.RawMessage
}

// readOnceEvent resolves the event from --fixture, or from --event/--event-name
// (which default to the two variables GitHub Actions sets for every job).
//
// An Actions payload is the raw event JSON and arrives over no wire, so there
// is no HMAC to verify: it is a trusted local file the runner was handed, not
// an inbound webhook. That is the whole difference from the daemon's intake.
func readOnceEvent(o onceOptions) (onceEvent, error) {
	if o.fixturePath != "" {
		raw, err := os.ReadFile(o.fixturePath)
		if err != nil {
			return onceEvent{}, err
		}
		var fx struct {
			Event string          `json:"event"`
			Body  json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(raw, &fx); err != nil {
			return onceEvent{}, fmt.Errorf("fixture must be {\"event\":..., \"body\":{...}}: %w", err)
		}
		if fx.Event == "" {
			return onceEvent{}, fmt.Errorf("fixture %s: no \"event\" name", o.fixturePath)
		}
		name := fx.Event
		if o.eventName != "" && o.fixtureNameOverridden() {
			name = o.eventName
		}
		return onceEvent{Name: name, Body: fx.Body}, nil
	}
	if o.eventPath == "" {
		return onceEvent{}, fmt.Errorf("once: no event — pass --event PATH (or --fixture PATH); in GitHub Actions $GITHUB_EVENT_PATH is set for you")
	}
	if o.eventName == "" {
		return onceEvent{}, fmt.Errorf("once: no event name — pass --event-name NAME (e.g. pull_request); in GitHub Actions $GITHUB_EVENT_NAME is set for you")
	}
	body, err := os.ReadFile(o.eventPath)
	if err != nil {
		return onceEvent{}, err
	}
	if !json.Valid(body) {
		return onceEvent{}, fmt.Errorf("event %s: not valid JSON", o.eventPath)
	}
	return onceEvent{Name: o.eventName, Body: body}, nil
}

// fixtureNameOverridden reports whether an explicit --event-name should win
// over the fixture's own. It cannot, today: the fixture names its event and a
// stray $GITHUB_EVENT_NAME in the environment must not silently retarget it.
func (o onceOptions) fixtureNameOverridden() bool { return false }

// runOnce is the one-shot pipeline. Split from cmdOnce so tests drive it with
// a config and options in hand rather than through argv.
func runOnce(ctx context.Context, cfg *config.Config, o onceOptions) error {
	out, errOut := o.writers()

	ev, err := readOnceEvent(o)
	if err != nil {
		return err
	}
	spec, tidx, err := onceTriggerByName(cfg, o.trigger)
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

	// Ephemeral state (design O4). A runner is thrown away, so dedup /
	// attempts / backoff / history live in a temp directory that goes with it
	// — unless the operator pointed state somewhere durable on purpose. ctx
	// stores (`stores:`) are untouched either way: one pointed at a remote
	// backend is exactly how a stateful CI workflow is meant to work.
	if !o.durableState {
		dir, err := os.MkdirTemp("", "conductor-once-*")
		if err != nil {
			return fmt.Errorf("once: state dir: %w", err)
		}
		defer os.RemoveAll(dir)
		cfg.Store.StateFile = filepath.Join(dir, "state.json")
		cfg.Store.AuditLog = filepath.Join(dir, "audit.jsonl")
		// The override is process-global (install state, the model catalog, the
		// blob store all read it), so restore it on the way out rather than
		// leaving a deleted directory pinned behind us. Irrelevant to the CLI,
		// which runs one command and exits; it matters to anything that calls
		// runOnce more than once in a process.
		prev := config.StateDir()
		config.SetStateDir(dir)
		defer config.SetStateDir(prev)
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

	// A live notifier, so a configured escalate/needs_input route still
	// reaches a human from CI. NOT SetPublisher: the lifecycle→conductor.*
	// source feeds triggers through an engine loop that one-shot mode never
	// runs, so publishing would queue events nothing drains.
	notifier := notify.New(cfg.Notify, logf, st.Audit)

	// The real stack: DryRun false. Steps — agents, engines, verbs, commands —
	// actually execute.
	stack, err := buildFlowStack(cfg, st, notifier, false)
	if err != nil {
		return err
	}
	defer stack.Close()
	if stack == nil {
		return fmt.Errorf("once: this config has no `connectors:` block — one-shot mode runs connectors-model triggers (see `conductor config migrate`)")
	}
	if stack.Secrets != nil {
		notifier.SetSecrets(stack.Secrets)
		setLogRedactor(stack.Secrets)
		st.SetAuditRedactor(stack.Secrets.Redact)
	}
	notifyStackFailures(stack, notifier)

	// What this trigger CANNOT do in a runner: paseo, and anything that waits
	// on a human. Checked before a single step executes, so the job fails at
	// second one with a config-shaped message instead of hanging or half-
	// running.
	if err := onceUnsupportedSteps(cfg, stack.Registry, spec); err != nil {
		return err
	}

	igs, retry, writeTok, readTok := resolveDispatchIdentity(igs, stack)
	paseoBin, err := resolvePaseoBin(cfg)
	if err != nil {
		return err
	}
	disp := dispatch.New(paseoBin, retry, false) // NOT dry-run
	disp.AdoptOpenWorkspaces = cfg.AdoptOpenWorkspaces
	endpoint := resolvePaseoEndpoint(cfg)
	disp.Home, disp.Server = endpoint.Home(), endpoint.Server()
	if stack.Secrets != nil {
		disp.Secrets = stack.Secrets
		dispatch.SetScrubber(stack.Secrets)
	}

	// Runtime plumbing, same wiring the daemon does — minus everything that
	// would keep running after the event is processed.
	controller.HostArgvPrefix = func(name string) ([]string, error) {
		hc, ok := cfg.Hosts[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q (defined: %s)", name, sortedHostNames(cfg.Hosts))
		}
		return (&hosts.Client{}).ArgvPrefix(hosts.Target{Name: name, Cfg: hc}), nil
	}
	controller.HostDial = func(ctx context.Context, name, addr string) (net.Conn, error) {
		hc, ok := cfg.Hosts[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q (defined: %s)", name, sortedHostNames(cfg.Hosts))
		}
		return (&hosts.Client{}).DialVia(ctx, hosts.Target{Name: name, Cfg: hc}, addr)
	}
	// Isolation network policy is enforced in CI exactly as on a box: the
	// manager starts a proxy lazily, on the first dispatch that needs one, and
	// Close tears down whatever it started when the run ends.
	egress := sandbox.NewProxyManager(func(key, hostport string) {
		logf("sandbox: egress denied: %s (not in allowlist)", hostport)
		st.Audit(map[string]any{"event": "egress_denied", "target": hostport})
	})
	defer egress.Close()
	controller.EgressProxyFor = egress.Endpoint
	controller.EgressProxyUnix = egress.UnixEndpoint
	stateDir := filepath.Dir(cfg.Store.StateFile)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("state dir %s: %w", stateDir, err)
	}
	cfgFile, _ := configPath(nil)
	controller.DaemonMaskPaths = []string{stateDir, filepath.Dir(cfgFile)}

	// One-shot mode doesn't wire Backend-RPC runtime plugins (no long-lived
	// dispatcher/reaper here); ACP runtime plugins still resolve as before.
	// Decision runtimes ARE wired: decide: steps reach them natively, and
	// agent resolution must know to skip them.
	rtMgr, closeRtMgr := runtimePluginManager(stack, cfg, secrets.New(), st.Audit)
	defer closeRtMgr()
	rtSecrets := secrets.New()
	if stack != nil && stack.Secrets != nil {
		rtSecrets = stack.Secrets
	}
	deciders, err := loadDecisionRuntimes(rtMgr, cfg, rtSecrets)
	if err != nil {
		return err
	}
	mergedControllers, err := mergedControllersWithPlugins(cfg, nil, deciders)
	if err != nil {
		return err
	}
	// The cli runtime provisions its own git worktree under <state>/worktrees.
	// gitProv.Run — the orphan-reaper loop — is deliberately NOT started: the
	// state dir is thrown away with the job, and a sweeping goroutine would
	// outlive the run it was meant to serve.
	gitProv := gitwt.New(stateDir)
	gitProv.Log = logf
	reg := controller.NewRegistry(mergedControllers, cfg.DefaultRuntimeName(), disp, disp,
		controller.WithCLIProvisioner(gitProv))

	// engine.New wires the flow runner's AgentServices (runtime resolution,
	// identity tokens, guidance, model selection, budgets). It starts NOTHING
	// — Engine.Run, which owns the trigger queue, the GC loop and the affinity
	// sweep, is never called. This is the whole reason one-shot mode can reuse
	// the daemon's dispatch semantics without becoming a daemon.
	eng := engine.New(engine.Options{
		Config: cfg, Store: st, Dispatch: disp, Controllers: reg,
		Notifier: notifier, Author: gitAuthor(), UserToken: writeTok, ReadToken: readTok,
		Log: logf, RefreshAppToken: refreshAppToken(igs), PausePath: pausePath(cfg),
		Flow: stack.Runner, Connectors: stack.Registry, Secrets: stack.Secrets,
	})
	eng.SetModelResolver(agentmodels.NewResolver(cfg, agentmodels.NewCatalog(config.StateDir())))
	eng.SetDeciders(deciders)

	// Find the event's trigger THROUGH the configured source, so the connector's
	// own normalization (github's event→kind mapping, target extraction, author
	// facts) runs on the Actions payload exactly as it does on a delivery.
	t, ok, err := onceTranslate(ctx, stack, ev, tidx)
	if err != nil {
		return err
	}
	if ok {
		match, ferr := stack.Runner.FilterMatch(t, spec)
		if ferr != nil {
			return fmt.Errorf("once: %s: filter: %w", o.trigger, ferr)
		}
		ok = match
	}
	if !ok {
		out(fmt.Sprintf("no match: %s did not fire trigger %q", ev.Name, o.trigger))
		if o.requireMatch {
			return &exitError{code: onceExitOutcome,
				msg: fmt.Sprintf("once: event %s did not match trigger %q (--require-match)", ev.Name, o.trigger)}
		}
		return nil
	}

	// Stream the run to the job log and collect the outcome from the same
	// event hub `conductor watch` tails — what a job sees is exactly what the
	// run record persists.
	rec := newOnceRecorder(stack.Events, out)
	defer rec.stop()

	out(fmt.Sprintf("running trigger %q: %s %s (%d step(s))", o.trigger, t.Kind, onceTargetLabel(t), len(spec.Steps)))
	run := store.WorkflowRun{
		ID:       "once:" + t.Kind + ":" + t.Key(),
		Source:   t.Source,
		Instance: t.Instance,
		Kind:     t.Kind,
		Repo:     t.Target.Repo,
		Number:   t.Target.Number,
		Outputs:  map[string]map[string]any{},
	}
	stack.Runner.Run(ctx, run, t, spec, tidx, nil, false)
	rec.stop()

	res := rec.result()
	for _, line := range res.summary() {
		out(line)
	}
	fired := res.firedCategories(o.failOn)
	if len(fired) == 0 {
		return nil
	}
	errOut(fmt.Sprintf("once: trigger %q failed (%s)", o.trigger, strings.Join(fired, ", ")))
	return &exitError{code: onceExitOutcome}
}

// writers resolves the job-log sinks (stdout for the run narrative, stderr for
// the failure line), defaulting to the process's own.
func (o onceOptions) writers() (out, errOut func(string)) {
	out, errOut = o.stdout, o.stderr
	if out == nil {
		out = func(s string) { fmt.Fprintln(os.Stdout, s) }
	}
	if errOut == nil {
		errOut = func(s string) { fmt.Fprintln(os.Stderr, s) }
	}
	return
}

// onceTargetLabel renders a trigger's target for the job log ("owner/repo#12",
// or the repo alone for a target with no number).
func onceTargetLabel(t core.Trigger) string {
	if t.Target.Repo == "" {
		return "(no target)"
	}
	if t.Target.Number == 0 {
		return t.Target.Repo
	}
	return fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number)
}

// onceTriggerByName resolves the `<trigger>` argument to its spec and its
// position in `triggers:` — the index is half of an unnamed trigger's identity
// scope, so it travels with the spec everywhere (see flow.Runner.Run).
//
// Design O1: the trigger is EXPLICIT. Matching the event against every trigger
// the way the daemon does would make one job fire several workflows, which is
// surprising in a step whose success is the job's.
func onceTriggerByName(cfg *config.Config, name string) (config.TriggerSpec, int, error) {
	if name == "" {
		return config.TriggerSpec{}, 0, fmt.Errorf("once: name the trigger to run")
	}
	for i, spec := range cfg.Triggers {
		if spec.Name != name {
			continue
		}
		if !spec.IsEnabled() {
			return config.TriggerSpec{}, 0, fmt.Errorf("once: trigger %q is `enabled: false`", name)
		}
		if spec.Manual() {
			return config.TriggerSpec{}, 0, fmt.Errorf("once: trigger %q is a manual trigger (`on: manual`) — one-shot mode processes an EVENT through a source trigger; fire a manual one with `conductor run %s` against a running daemon", name, name)
		}
		return spec, i, nil
	}
	return config.TriggerSpec{}, 0, fmt.Errorf("once: no trigger named %q (named triggers: %s)", name, namedTriggerList(cfg))
}

// namedTriggerList lists the triggers `once` can address, for the not-found
// error. Unnamed list-form triggers are unaddressable by design — `name:` is
// how a trigger becomes a CLI target.
func namedTriggerList(cfg *config.Config) string {
	var names []string
	for _, spec := range cfg.Triggers {
		if spec.Name != "" && !spec.Manual() {
			names = append(names, spec.Name)
		}
	}
	if len(names) == 0 {
		return "none — give the trigger you want to run a `name:`"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// onceTranslate feeds the event through the lowered source integrations and
// returns the trigger destined for tidx. This is replay's path verbatim: the
// connector normalizes the payload, lowers its configured triggers, and stamps
// each resulting trigger with the FlowRef that names the spec it belongs to.
// An event that produces several triggers for the SAME spec (a batch) takes
// the first — one job, one run.
func onceTranslate(ctx context.Context, stack *flowStack, ev onceEvent, tidx int) (core.Trigger, bool, error) {
	type translator interface {
		Translate(context.Context, string, []byte) []core.Trigger
	}
	for _, ig := range stack.Integrations {
		tr, ok := ig.(translator)
		if !ok {
			continue
		}
		for _, t := range tr.Translate(ctx, ev.Name, ev.Body) {
			act, _ := t.Action.(config.Action)
			if act.FlowRef == "" {
				continue
			}
			_, idx, ok := stack.Runner.SpecFor(act.FlowRef)
			if !ok || idx != tidx {
				continue
			}
			return t, true, nil
		}
	}
	return core.Trigger{}, false, nil
}

// onceUnsupportedSteps refuses, BEFORE anything executes, the steps a runner
// cannot honestly run. Both refusals are deliberate errors rather than silent
// degradations — a step that quietly did something else would report a green
// job for work that never happened.
//
//   - paseo (design non-goal). A paseo-runtime agent is a child of the paseo
//     daemon, which is not in the runner and would not survive the job if it
//     were. cli runtimes, engine plugins, verbs and commands all run headless.
//   - anything that waits for a human. A `background: true` hand-off step and
//     an `ask`-class verb both block on a reply that cannot arrive in CI. The
//     hand-off machinery has no CI-shaped decision to degrade TO — auto-approving
//     a review nobody read is worse than failing — so one-shot mode fails fast
//     and names the step, instead of a job that hangs until its timeout.
func onceUnsupportedSteps(cfg *config.Config, reg *connector.Registry, spec config.TriggerSpec) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	walkOnceSteps(cfg, spec, func(where string, s config.Step) {
		if firstErr != nil {
			return
		}
		if s.Background {
			fail(fmt.Errorf("config: %s: `background: true` hands an agent off for a human to drive, and there is no human in a one-shot run — remove it, or run this trigger on the daemon", where))
			return
		}
		if s.Uses != "" && reg != nil {
			connName, verb, _ := strings.Cut(s.Uses, ".")
			if in, ok := reg.Get(connName); ok && in.Decl != nil {
				if vd, found := in.Decl.Verb(verb); found && vd.Ask {
					fail(fmt.Errorf("config: %s: `uses: %s` presents to a human and blocks for their answer — there is no human in a one-shot run; remove it, or run this trigger on the daemon", where, s.Uses))
					return
				}
			}
		}
		if s.Form() != "agent" && s.Form() != "team" {
			return
		}
		if rn := onceRuntimeOf(cfg, s); rn == config.BuiltinPaseoRuntime {
			fail(fmt.Errorf("config: %s: runtime %q needs the paseo daemon, which a one-shot run does not have — use a cli/acp runtime, an engine plugin, a verb or a command in `conductor once`", where, rn))
		}
	})
	return firstErr
}

// onceRuntimeOf resolves the runtime name a step dispatches on, by the same
// ladder every other resolution uses (explicit → fleet default → the built-in
// paseo), and collapses a paseo-TYPE named runtime onto "paseo" so
// `runtime: gpu-paseo` is refused for the same reason the bare default is.
func onceRuntimeOf(cfg *config.Config, s config.Step) string {
	rn := s.Runtime
	if rn == "" {
		rn = cfg.DefaultRuntimeName()
	}
	if rn == "" {
		return config.BuiltinPaseoRuntime
	}
	if cc, ok := cfg.MergedControllers()[rn]; ok && cc.Type == config.BuiltinPaseoRuntime {
		return config.BuiltinPaseoRuntime
	}
	return rn
}

// walkOnceSteps visits every step reachable from one trigger — its own steps,
// parallel branches, compensations, and the steps of any workflow it calls
// (transitively, each workflow expanded once). It is the per-trigger analogue
// of config.WalkSteps, which walks the WHOLE config: one-shot mode refuses a
// step only when the trigger it was asked to run can actually reach it.
func walkOnceSteps(cfg *config.Config, spec config.TriggerSpec, fn func(where string, s config.Step)) {
	seenWorkflow := map[string]bool{}
	var walkList func(where string, steps []config.Step)
	var walk func(where string, s config.Step)
	walk = func(where string, s config.Step) {
		fn(where, s)
		if s.Parallel != nil {
			for bi := range s.Parallel.Branches {
				walkList(fmt.Sprintf("%s branch[%d]", where, bi), s.Parallel.Branches[bi])
			}
		}
		if s.Compensate != nil {
			walk(where+" compensate", *s.Compensate)
		}
		if s.Workflow != "" && !seenWorkflow[s.Workflow] {
			seenWorkflow[s.Workflow] = true
			if wf, ok := cfg.Workflows[s.Workflow]; ok {
				walkList("workflow "+s.Workflow, wf.Steps)
			}
		}
	}
	walkList = func(where string, steps []config.Step) {
		for i := range steps {
			id := steps[i].ID
			if id == "" {
				id = fmt.Sprintf("step%d", i+1)
			}
			walk(where+" "+id, steps[i])
		}
	}
	label := "trigger " + spec.On
	if spec.Name != "" {
		label = "trigger " + spec.Name
	}
	walkList(label, spec.Steps)
}

// onceRecorder subscribes to the flow event hub, streams the run to the job
// log, and remembers what it saw so the exit code can be derived from it. The
// hub is the same surface `conductor watch` tails, so one-shot's job log and a
// daemon operator's live tail show the same run.
type onceRecorder struct {
	cancel func()
	done   chan struct{}

	mu       sync.Mutex
	status   string // the run's terminal status: ok | failed | "" (never finished)
	detail   string
	stepFail []string // "<step>: <error>"
	gateFail []string // "<step>: <checks>" — gate rounds that escalated
	once     sync.Once
}

func newOnceRecorder(hub *flow.EventHub, out func(string)) *onceRecorder {
	ch, cancel := hub.Subscribe("")
	r := &onceRecorder{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for ev := range ch {
			r.record(ev)
			if line := onceEventLine(ev); line != "" {
				out(line)
			}
		}
	}()
	return r
}

func (r *onceRecorder) record(ev flow.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Type {
	case "step_done":
		if ev.Status == "failed" {
			r.stepFail = append(r.stepFail, joinDetail(ev.Step, ev.Detail))
		}
	case "gate":
		// "escalated" is the gate's DISCARD verdict — the change was not
		// promoted. A plain "fail" round is followed by a revise and is not
		// yet an outcome.
		if ev.Status == "escalated" {
			r.gateFail = append(r.gateFail, joinDetail(ev.Step, ev.Detail))
		}
	case "run_done":
		r.status, r.detail = ev.Status, ev.Detail
	}
}

// stop unsubscribes and waits for the streaming goroutine, so every event the
// run published has reached the job log before the exit code is computed.
func (r *onceRecorder) stop() {
	r.once.Do(func() {
		r.cancel()
		<-r.done
	})
}

func (r *onceRecorder) result() onceResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return onceResult{status: r.status, detail: r.detail,
		stepFail: append([]string(nil), r.stepFail...),
		gateFail: append([]string(nil), r.gateFail...)}
}

// onceResult is what the run produced, in the shape the exit code needs.
type onceResult struct {
	status   string
	detail   string
	stepFail []string
	gateFail []string
}

// firedCategories returns the requested fail-on categories this result
// actually triggered, in the order they are documented.
//
// A run that ended "failed" with no step event attributed to it still counts
// as a step error: the failure happened somewhere in the steps (a hook, a
// checkpoint, a workflow-level error), and treating an unattributed failure as
// a pass is the one mistake a CI gate must not make.
func (res onceResult) firedCategories(failOn []string) []string {
	want := map[string]bool{}
	for _, c := range failOn {
		want[c] = true
	}
	var fired []string
	if want[failOnStepError] && (len(res.stepFail) > 0 || (res.status == "failed" && len(res.gateFail) == 0)) {
		fired = append(fired, failOnStepError)
	}
	if want[failOnGateReject] && len(res.gateFail) > 0 {
		fired = append(fired, failOnGateReject)
	}
	return fired
}

// summary renders the closing lines of the job log.
func (res onceResult) summary() []string {
	var out []string
	for _, f := range res.gateFail {
		out = append(out, "gate rejected "+f)
	}
	for _, f := range res.stepFail {
		out = append(out, "step failed "+f)
	}
	switch res.status {
	case "ok":
		out = append(out, "outcome: ok")
	case "":
		out = append(out, "outcome: unknown (the run published no result)")
	default:
		out = append(out, "outcome: "+joinDetail(res.status, res.detail))
	}
	return out
}

// onceEventLine renders one run event as a job-log line ("" to skip). Step
// starts and gate rounds are shown because a CI log is read after the fact and
// the sequence is what makes a failure legible; run_started/run_done are left
// to the framing lines runOnce prints itself.
func onceEventLine(ev flow.RunEvent) string {
	switch ev.Type {
	case "step_started":
		return "  · " + ev.Step
	case "step_done":
		line := fmt.Sprintf("  %s %s (%s)", onceMark(ev.Status), ev.Step, ev.Status)
		if ev.DurationMS > 0 {
			line = fmt.Sprintf("%s %dms", line, ev.DurationMS)
		}
		if ev.Detail != "" {
			line += ": " + ev.Detail
		}
		return line
	case "gate":
		return fmt.Sprintf("  %s gate %s: %s", onceMark(ev.Status), ev.Step, joinDetail(ev.Status, ev.Detail))
	}
	return ""
}

func onceMark(status string) string {
	switch status {
	case "ok", "pass":
		return "✓"
	case "skipped", "revise":
		return "–"
	default:
		return "✗"
	}
}

func joinDetail(head, detail string) string {
	if detail == "" {
		return head
	}
	return head + ": " + detail
}
