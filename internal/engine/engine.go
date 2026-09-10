// Package engine consumes Triggers from integrations and drives them through
// dedup, attempt caps, kill switch, shadow mode, dispatch, and notifications.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// *store.Store persists the broker's PR→session map; assert it here (engine
// imports both) so a drift in the SessionStore contract fails this build.
var _ controller.SessionStore = (*store.Store)(nil)

// Dispatcher runs a resolved request against a backend. *dispatch.Dispatcher
// satisfies it; tests inject fakes.
type Dispatcher interface {
	Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error)
	// WaitForAgent blocks until a launched background agent goes idle (or the
	// timeout fires), so a concurrency slot frees only when its work is done.
	WaitForAgent(ctx context.Context, id string, timeout time.Duration)
	// HasLiveAgent reports whether any non-archived conductor agent is already
	// working or parked for this PR+kind (gates re-dispatch of live-gated kinds).
	HasLiveAgent(ctx context.Context, prKey, kind string) bool
	// Archive soft-deletes a finished agent immediately, so a non-interactive step's
	// agent (e.g. assess) doesn't linger in paseo until the reaper's next tick.
	Archive(ctx context.Context, agentID string) error
}

// Notifier emits notifications. *notify.Notifier satisfies it.
type Notifier interface {
	Emit(context.Context, string, core.Trigger, string)
}

// Store is the persistence surface the engine needs. *store.Store satisfies it.
type Store interface {
	GC() (int, error)
	Touch(key string)
	Delete(key string) error
	LastSignature(key, kind string) string
	Attempts(key, kind, head string) int
	RetryReady(key, kind, head string, soft int, base time.Duration, factor int, max time.Duration) (bool, time.Duration)
	Record(key, kind, sig, head string) error
	RecordAttempt(key, kind, head string) error
	LastCommentID(key, kind string) int64
	AdvanceCommentID(key, kind string, id int64) error
	Audit(entry map[string]any)
	// Workflow-run persistence, so multi-step workflows resume across restarts.
	PutRun(r store.WorkflowRun) error
	DeleteRun(id string) error
	PendingRuns() []store.WorkflowRun
	// Execution history (#36 §20): the recorded run a user-driven retry
	// rehydrates. The verified read checks the record's HMAC — retry must
	// never trust a record modified on disk (#36 review M8).
	GetHistory(id string) (store.RunHistory, bool)
	GetHistoryVerified(id string) (store.RunHistory, error)
	// Outcome-learning state (#36 §18): engagements awaiting a terminal
	// signal, and the per-agent counters behind guidance tuning.
	RecordEngagement(repo string, number int, e store.Engagement)
	TakeEngagements(repo string, number int) []store.Engagement
	PeekEngagements(repo string, number int) []store.Engagement
	MarkCIFailure(repo string, number int, head string) bool
	BumpOutcome(key, outcome string)
	OutcomeStats(key string) map[string]int
}

// Engine is the central work loop.
type Engine struct {
	cfg         *config.Config
	store       Store
	disp        Dispatcher
	controllers *controller.Registry // resolves which controller runs each agent
	broker      *controller.Broker   // owns one live session per PR (interactive hand-off); nil = disabled
	handoffs    *handoff.Registry    // resolves a step's hand-off channel by name; nil = paseo-native hand-off
	notif       Notifier
	author      dispatch.Author
	userTok     func() (string, error)
	readTok     func() (string, error) // read-token override (nil = use the per-trigger App token)
	rerun       func(context.Context, core.Trigger, int64) error
	runStatus   func(context.Context, core.Trigger, int64) (string, error) // workflow run status (completed|in_progress|queued|…)
	refreshTok  func(core.Trigger) (string, error)                         // re-mint the App token on resume
	log         func(string, ...any)
	hold        *dispatch.HoldSet    // agent ids handed off to the user; the reaper never touches these
	affinity    *controller.Affinity // keyed live sessions (session:); nil = every dispatch fresh
	pausePath   string               // control file; present = paused (toggled by pause/resume, no restart)
	ch          chan core.Trigger
	secrets     *secrets.Resolver // redacts argv/errors/output tails on audit + log surfaces
	sem         chan struct{}     // concurrent-agent cap; nil = unlimited
	groupWarn   sync.Map          // FlowRefs whose group key already failed once (log once, not per event)
	baseCtx     context.Context   // the Run loop's ctx; ties ctx-less entry points (batch flush) to shutdown

	// flow runs connectors-model triggers (actions carrying a FlowRef);
	// grouper batches their grouped events. nil when the config has no
	// connectors — the legacy path is then the only path.
	flow       *flow.Runner
	grouper    *flow.Grouper
	connectors *connector.Registry

	budgetMu  sync.Mutex  // guards agentDisp (rolling agent-dispatch timestamps)
	agentDisp []time.Time // agent-dispatch times in the last hour (runaway budget)
	spendMu   sync.Mutex  // makes budget check + reservation one atomic step (#36 review H7)

	// meter is the rolling-window token/$ spend ledger behind the hard
	// budget caps (#36 §14) — in-memory like agentDisp; the audit's
	// agent_usage rows are the durable record.
	meter *cost.Meter
	// modelResolver walks the fleet ladder (design §2.3) per dispatch. nil
	// = no model layer: every dispatch bare-launches.
	modelResolver *models.Resolver
}

// overAgentBudget prunes agent-dispatch timestamps older than an hour and reports
// whether the last hour already has max dispatches.
func (e *Engine) overAgentBudget(max int) bool {
	cut := time.Now().Add(-time.Hour)
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	kept := e.agentDisp[:0]
	for _, t := range e.agentDisp {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	e.agentDisp = kept
	return len(e.agentDisp) >= max
}

// recordAgentDispatch stamps an agent dispatch into the rolling budget window.
func (e *Engine) recordAgentDispatch() {
	e.budgetMu.Lock()
	e.agentDisp = append(e.agentDisp, time.Now())
	e.budgetMu.Unlock()
}

// Options configure an Engine.
type Options struct {
	Config   *config.Config
	Store    Store
	Dispatch Dispatcher
	// Controllers resolves which controller runs each agent (explicit
	// `controller:` → default:true → built-in paseo). nil builds a registry from
	// Config.Controllers with Dispatch as the built-in paseo runner — so with no
	// `controllers:` block every agent dispatches through paseo, unchanged.
	Controllers *controller.Registry
	// Broker owns one live agent session per PR so an interactive hand-off survives
	// a conductor restart and follow-ups funnel to the live session instead of a
	// duplicate agent. nil disables it — the interactive hand-off then stays
	// paseo-native (you drive the agent in paseo, as before).
	Broker *controller.Broker
	// Handoffs resolves the portable human↔agent channel an interactive review is
	// presented on (a step's `handoff:` name → the handoffs: entry flagged
	// default:true → the sole configured entry). nil, or resolving to nil, → the
	// review hand-off keeps today's behavior (notify you to open the live agent
	// in paseo).
	Handoffs  *handoff.Registry
	Notifier  Notifier
	Author    dispatch.Author
	UserToken func() (string, error)
	// ReadToken, if set, overrides the token used for API reads (GH_TOKEN) instead
	// of the per-trigger App installation token — for identity.read_token != "app".
	ReadToken func() (string, error)
	// Rerun, if set, overrides the flaky-CI rerun step (tests inject a spy). A
	// returned error means the rerun was NOT requested; the attempt isn't counted.
	Rerun func(context.Context, core.Trigger, int64) error
	// RunStatus, if set, overrides the workflow-run status lookup the flaky-CI step
	// uses to wait for a run to finish before rerunning it (tests inject a stub).
	RunStatus func(context.Context, core.Trigger, int64) (string, error)
	// RefreshAppToken re-mints the App installation token for a persisted trigger
	// on resume (the persisted one is expired). Given the trigger's instance +
	// installation_id. nil disables workflow resume.
	RefreshAppToken func(core.Trigger) (string, error)
	// Hold is the shared "never reap" set for interactive hand-off agents; the
	// engine registers a background step's agent id here at launch and the reaper
	// skips it. nil disables the explicit hold (falls back to label/marker signals).
	Hold *dispatch.HoldSet
	// Affinity is the session-affinity registry (agents whose profile carries a
	// session: block get one live session per rendered key, shared across
	// triggers). nil disables affinity — every dispatch stays fresh.
	Affinity *controller.Affinity
	// Secrets redacts tracked secret values from the engine's audit entries
	// and output-tail log lines (nil = passthrough; legacy configs).
	Secrets *secrets.Resolver
	// PausePath is a control file whose presence pauses dispatch (toggled by the
	// pause/resume commands without a restart). Empty disables the runtime pause.
	PausePath string
	Log       func(string, ...any)
	// Flow runs connectors-model triggers; nil when the config has none. The
	// engine fills in its AgentServices (runtime resolution, tokens, guidance,
	// background hand-off) after construction.
	Flow *flow.Runner
	// Connectors is the built connector registry (ask-capable hand-off
	// resolution). nil without a connectors: block.
	Connectors *connector.Registry
}

// New builds an Engine.
func New(o Options) *Engine {
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	reg := o.Controllers
	if reg == nil {
		// Build the controller set from config, with the passed dispatcher as the
		// built-in paseo runner (and follow-up sender when it supports one). No
		// `controllers:` block → resolution always yields paseo, unchanged.
		var sender controller.Sender
		if s, ok := o.Dispatch.(controller.Sender); ok {
			sender = s
		}
		reg = controller.NewRegistry(o.Config.MergedControllers(), o.Config.DefaultRuntimeName(), o.Dispatch, sender)
	}
	e := &Engine{
		cfg: o.Config, store: o.Store, disp: o.Dispatch, controllers: reg, notif: o.Notifier,
		broker: o.Broker, handoffs: o.Handoffs,
		author: o.Author, userTok: o.UserToken, readTok: o.ReadToken, log: log,
		hold:      o.Hold,
		affinity:  o.Affinity,
		pausePath: o.PausePath,
		secrets:   o.Secrets,
		ch:        make(chan core.Trigger, 256),
		meter:     cost.NewMeter(),
	}
	if cap := o.Config.AgentCap(); cap > 0 {
		e.sem = make(chan struct{}, cap)
	}
	e.rerun = o.Rerun
	if e.rerun == nil {
		e.rerun = e.rerunFailed
	}
	e.runStatus = o.RunStatus
	if e.runStatus == nil {
		e.runStatus = e.workflowRunStatus
	}
	e.refreshTok = o.RefreshAppToken
	e.connectors = o.Connectors
	if o.Flow != nil {
		e.flow = o.Flow
		// The runner's agent/command steps dispatch through the engine's own
		// runtime resolution, tokens, and hand-off machinery.
		e.flow.Agents = e.flowAgentServices()
		e.grouper = flow.NewGrouper(nil, e.runBatch)
	}
	return e
}

// Emit enqueues a trigger for processing (non-blocking; drops if the queue is
// saturated, logging so it's visible).
func (e *Engine) Emit(ctx context.Context, t core.Trigger) {
	select {
	case e.ch <- t:
	default:
		e.log("%s queue full, dropping", tag(t))
	}
}

// Run processes triggers until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	e.baseCtx = ctx // grouper batch flushes (no ctx of their own) tie to shutdown
	if _, err := e.store.GC(); err != nil {
		e.log("engine: initial GC: %v", err)
	}
	go e.gcLoop(ctx)
	if e.affinity != nil {
		go e.affinity.Run(ctx, time.Minute) // idle_ttl / max_lifetime sweep
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-e.ch:
			e.process(ctx, t)
		}
	}
}

func (e *Engine) gcLoop(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := e.store.GC(); err == nil && n > 0 {
				e.log("engine: GC evicted %d records", n)
			}
		}
	}
}

// agentGuidance returns the house tone/format guidance appended to a dispatched
// agent's prompt. Guidance is *additive* (a stack) and entirely config-driven —
// conductor injects NO tone of its own. Layer 0 is the policy-resolved baseline
// (global → connector → trigger, stacked by MergePolicy), or the unfolded
// top-level agent_guidance (a config that never ran applyDefaults, e.g. a test).
// The step's own parts — carrying any extends: ancestor's, prepended during
// resolveExtends — stack on top as separate blocks. Nothing configured → no
// guidance block at all. A profile `guidance: { replace: … }` drops layer 0 and
// every inherited part; an explicit "" renders nothing (a deliberate disable).
func (e *Engine) agentGuidance(profile config.Step, pol config.Policy) string {
	spec := profile.Guidance
	replace := spec != nil && spec.Replace

	var parts []string
	if !replace {
		switch {
		case pol.Guidance != nil:
			parts = append(parts, pol.Guidance.Parts...)
		case e.cfg.AgentGuidance != nil:
			parts = append(parts, *e.cfg.AgentGuidance)
		}
	}
	if spec != nil {
		parts = append(parts, spec.Parts...)
	}

	var b strings.Builder
	for _, p := range parts {
		b.WriteString(wrapGuidance(p)) // empty parts render nothing
	}
	// The skill blurb (#36 §12) rides the same append path, opted in by the
	// profile's skill: block. The whole guidance is redactor-filtered — an
	// injected prompt section must never carry a tracked secret value.
	return e.redact(b.String() + e.skillGuidance(profile))
}

// skillGuidance tells a skill-enabled agent what its conductor tools are and
// how to use them: verbs first (the credential never enters the session),
// the broker only as a last resort. "" for profiles without skill: — and for
// profiles on a runtime that cannot carry the MCP tools (#123): promising an
// agent tools it doesn't have just makes it fail; `conductor validate` warns
// the operator instead.
func (e *Engine) skillGuidance(profile config.Step) string {
	sk := profile.Skill
	if sk == nil {
		return ""
	}
	_, mode := e.cfg.SkillDelivery(profile)
	switch mode {
	case config.SkillModeMCP:
		return wrapGuidance(e.skillGuidanceMCP(sk))
	case config.SkillModeCLI:
		return wrapGuidance(e.skillGuidanceCLI(sk))
	default:
		return "" // SkillModeNone: the surface can't reach this agent
	}
}

// skillGuidanceMCP is the blurb for runtimes that carry the tool server as MCP
// tools (ACP/opencode). Layer 0 only: the granted verbs arrive as native tool
// schemas (SkillVerbCatalog, rendered from the same registry entries the CLI
// card uses), so repeating them as prompt text would be redundant tokens.
func (e *Engine) skillGuidanceMCP(sk *config.SkillPolicy) string {
	var b strings.Builder
	if len(sk.Verbs) > 0 {
		b.WriteString(flow.CapabilityPreamble(false))
	} else {
		b.WriteString("Conductor tools are available on this session.")
	}
	if sk.SecretsVia == "broker" && len(sk.AllowSecrets) > 0 {
		b.WriteString(fmt.Sprintf(" If a raw tool you must run itself needs a credential, request it via secret_issue/secret_redeem (allowed: %s) — grants are single-use, expire in about a minute, and every step is audited. Use the value immediately for the one action that needs it; never echo it, store it, or write it to disk.",
			strings.Join(sk.AllowSecrets, ", ")))
	}
	b.WriteString(" Values that render as «secret:…» are opaque handles — pass them through unchanged; they only resolve inside conductor.")
	return b.String()
}

// skillGuidanceCLI is the blurb for local runtimes with no MCP surface
// (paseo, agent-deck, cli): the agent shells the `conductor` CLI.
//
// Layer 0 (the generated mechanics preamble) plus Layer 1 (the CAPABILITY
// CARD — the granted verbs with their options and call form, rendered from
// the verb registry). The card is what lets a workflow prompt drop to intent
// instead of hand-coding `conductor call <verb> --<opt> '<json>'`, and it
// updates itself when a verb's signature changes.
//
// The card is scoped to exactly the grant and comes from the same resolution
// as `conductor discover` and enforcement, so it can never promise a verb the
// daemon would refuse. With no flow runner wired (no connectors), there is no
// registry to render and the agent gets the discovery command instead.
func (e *Engine) skillGuidanceCLI(sk *config.SkillPolicy) string {
	var b strings.Builder
	b.WriteString("Conductor is available on this machine via the `conductor` CLI (endpoint + a scoped token are already in your environment). ")
	b.WriteString(flow.CapabilityPreamble(true))
	b.WriteString(" `conductor memory recall|remember` is your shared memory.")
	if card := e.capabilityCard(sk); card != "" {
		b.WriteString("\n\n")
		b.WriteString(card)
	}
	if sk.SecretsVia == "broker" && len(sk.AllowSecrets) > 0 {
		b.WriteString(fmt.Sprintf(" If a raw tool you run yourself genuinely needs a credential, `conductor secret <name>` mints a single-use, ~1-minute, audited value (allowed: %s) — use it immediately for that one action; never echo, store, or write it to disk.",
			strings.Join(sk.AllowSecrets, ", ")))
	}
	return b.String()
}

// capabilityCard renders the granted verbs for a CLI-transport agent ("" when
// the grant admits nothing, or when no registry is wired).
func (e *Engine) capabilityCard(sk *config.SkillPolicy) string {
	if e.flow == nil || sk == nil || len(sk.Verbs) == 0 {
		return ""
	}
	return e.flow.CapabilityCard(sk.Verbs)
}

// dispatchAgent routes one request through session affinity when the profile
// keeps keyed sessions (a follow-up to the bound live session, or a fresh
// spawn that binds), and falls through to the plain runner otherwise —
// including for session: profiles on runtimes without session persistence
// (one-shot/cli), which stay fresh-per-event and lean on shared memory.
func (e *Engine) dispatchAgent(ctx context.Context, runner Dispatcher, req dispatch.Request) (dispatch.RunRef, error) {
	if e.affinity != nil {
		if ref, handled, err := e.affinity.Dispatch(ctx, runner, req); handled {
			return ref, err
		}
	}
	return runner.Dispatch(ctx, req)
}

// affinityOwns reports whether an agent id is a bound keyed session — the
// archive paths must not tear a shared session down after one step.
func (e *Engine) affinityOwns(agentID string) bool { return e.affinity.Owns(agentID) }

// memoryPrompt renders the opt-in shared-memory section for a dispatched
// agent — the same append path as agentGuidance. A profile without
// `memory:` (or with memory unconfigured) gets "" — no token cost.
func (e *Engine) memoryPrompt(identity string, step config.Step, t core.Trigger, workflow string) string {
	sel := step.Memory
	if sel == nil || !sel.Enabled {
		return ""
	}
	m := memory.Active()
	if m == nil {
		return ""
	}
	// The engine supplies the run's context keys as a CONVENTION; the memory
	// core never interprets them (design §2).
	keys := memory.ContextKeys(t.Target.Repo, workflow, identity)
	f := memory.Filter{
		Scopes: memory.ExpandScopeRefs(sel.Scopes, t.Target.Repo, workflow, identity),
		Tags:   sel.Tags, Limit: sel.Limit,
	}
	return m.PromptSection(f, keys)
}

// harvestMemory applies the memory output contract to a finished agent's
// output (see memory.HarvestOutput): best-effort, audited, never a failure.
func (e *Engine) harvestMemory(t core.Trigger, agent, runID, output string, sel *config.MemorySelector) {
	m := memory.Active()
	if m == nil || strings.TrimSpace(output) == "" {
		return
	}
	// Writing is opt-in per STEP, exactly like reading (memoryPrompt): only a
	// step whose memory: is enabled may harvest its output into the shared
	// store. Otherwise any dispatched agent — including one an untrusted
	// event steered — could poison shared memory through its output contract
	// without the operator ever granting it memory access (#57 M8).
	if sel == nil || !sel.Enabled {
		return
	}
	src := memory.Source{Step: agent, Run: runID, Trigger: t.Kind, Repo: t.Target.Repo}
	entries, err := m.HarvestOutput(output, src)
	if err != nil {
		e.log("%s memory output contract: %v", tag(t), err)
		e.store.Audit(map[string]any{"event": "memory_remember", "via": "output", "outcome": "failed",
			"repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind, "agent": agent, "error": err.Error()})
		return
	}
	for _, en := range entries {
		e.store.Audit(map[string]any{"event": "memory_remember", "via": "output", "outcome": "ok",
			"repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind, "agent": agent,
			"id": en.ID, "scope": en.Scope})
	}
}

// retryWhileDeferred re-runs a step while its output still signals "not ready"
// (matches rp.WhileOutputMatches) — e.g. critique deferring on pending CI — polling
// every rp.RetryInterval() up to rp.RetryTimeout(), then giving up (the sweep is the
// backstop). Returns the last RunRef. Only meaningful for a step that exited cleanly
// but reported it isn't done; a hard error is handled by the caller.
func (e *Engine) retryWhileDeferred(ctx context.Context, req dispatch.Request, ref dispatch.RunRef, rp *config.StepRetry) dispatch.RunRef {
	if rp == nil || rp.WhileOutputMatches == "" {
		return ref
	}
	re, err := regexp.Compile(rp.WhileOutputMatches)
	if err != nil {
		e.log("%s step retry: bad while_output_matches %q: %v — not retrying", tag(req.Trigger), rp.WhileOutputMatches, err)
		return ref
	}
	if !re.MatchString(ref.Output) {
		return ref // already ready
	}
	interval, timeout := rp.RetryInterval(), rp.RetryTimeout()
	deadline := time.Now().Add(timeout)
	e.log("%s step deferred (matches %q) — releasing its slot and retrying every %s for up to %s", tag(req.Trigger), rp.WhileOutputMatches, interval, timeout)
	for {
		if time.Now().After(deadline) {
			e.log("%s step still deferred after %s — giving up (sweep will retry)", tag(req.Trigger), timeout)
			return ref // still holding the slot
		}
		// Free the concurrency slot while we idle so a deferred step (just waiting on
		// CI) doesn't tie up capacity, then queue for one again before re-running. On
		// ctx cancel (shutdown) we return without re-acquiring — the workflow goroutine
		// is being torn down, so the slot accounting no longer matters.
		e.release()
		select {
		case <-ctx.Done():
			return ref
		case <-time.After(interval):
		}
		if !e.acquire(ctx) {
			return ref // cancelled while queued (shutdown)
		}
		r2, err := e.disp.Dispatch(ctx, req)
		if err != nil {
			return r2 // surface the error to the caller's normal handling (slot held)
		}
		ref = r2
		if !re.MatchString(ref.Output) {
			e.log("%s step retry cleared — ready", tag(req.Trigger))
			return ref // slot held
		}
	}
}

// wrapGuidance renders guidance text as its own prompt block, or "" if empty.
func wrapGuidance(t string) string {
	if t = strings.TrimSpace(t); t == "" {
		return ""
	}
	return "\n\n---\n" + t
}

// isPaused reports whether the runtime pause control file is present.
func (e *Engine) isPaused() bool {
	if e.pausePath == "" {
		return false
	}
	_, err := os.Stat(e.pausePath)
	return err == nil
}

// triggerHasLabel reports whether the object's labels (stamped into Context by the
// integration where available) include label, case-insensitively.
func triggerHasLabel(t core.Trigger, label string) bool {
	raw, ok := t.Context["labels"]
	if !ok {
		return false
	}
	var labels []string
	switch v := raw.(type) {
	case []string:
		labels = v
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				labels = append(labels, s)
			}
		}
	}
	for _, l := range labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

func (e *Engine) process(ctx context.Context, t core.Trigger) {
	key := t.Key()

	// Session-affinity end_on: an eviction event (pr_closed/merged) ends the
	// matching keyed session before any gate can drop the trigger.
	e.affinity.ObserveEvent(ctx, t)

	// The outcome loop (#36 §18) reads merge/close/revert/CI facts off the
	// trigger before any gate can drop it.
	e.observeOutcomeSignals(ctx, t)

	// Terminal state: drop dedup record, no dispatch.
	if t.Kind == core.KindClosed {
		_ = e.store.Delete(key)
		e.log("%s closed; dropped state", tag(t))
		return
	}

	// Kill switch (config) + runtime pause (a control file toggled by `pause`/
	// `resume` without a restart).
	if !e.cfg.Control.IsEnabled() {
		return
	}
	if e.isPaused() {
		e.log("%s skipped — conductor is paused", tag(t))
		return
	}
	// Per-PR/issue opt-out: a label on the object (e.g. `conductor:off`) parks it.
	if pl := e.cfg.Control.PauseLabel; pl != "" && triggerHasLabel(t, pl) {
		e.log("%s skipped — carries pause label %q", tag(t), pl)
		return
	}
	e.store.Touch(key)

	act, ok := t.Action.(config.Action)
	if !ok || !act.IsEnabled() {
		return
	}
	head := t.Target.HeadSHA

	// Comment high-water mark: a new_comment for a comment id at or below the mark
	// was already handled (webhook or a prior sweep) — skip it. This is what lets the
	// sweep re-list recent comments to recover missed ones without re-dispatching
	// old ones (the single-slot dedup can't distinguish them). The mark advances on
	// a successful new_comment dispatch below. It's kept per comment kind: issue
	// (conversation) and review (inline) comments are separate id sequences.
	//
	// Connectors-model triggers additionally key the mark PER VARIANT: several
	// independent triggers may listen to the same comment event (one grouped,
	// one not), and the first to advance a shared mark would starve its
	// siblings of the very same comment. Legacy state is untouched (legacy
	// actions keep the bare kind key, honoring existing state.json).
	if !t.Force && t.Kind == "new_comment" {
		if id := commentID(t); id > 0 && id <= e.store.LastCommentID(key, commentMarkKind(t)) {
			return
		}
	}
	if t.Force {
		e.log("%s forced — bypassing dedup/liveness/backoff gates", tag(t))
	}

	// Dedup/attempt state is keyed per action variant so two variants of a kind on
	// the same PR/head don't collide. An unnamed (single) action keeps the bare
	// `kind` key, so existing state.json is honored with no migration.
	dkind := t.Kind
	if t.Variant != "" {
		dkind = t.Kind + "#" + t.Variant
	}

	// Flaky-CI: rerun the failed run once before spawning a fix agent. run_id is 0
	// for a non-Actions check (nothing to rerun) — straight to the fixer.
	if runID := toInt64(t.Context["run_id"]); t.Kind == "failing_checks" && act.FlakyRerun.Enabled && runID > 0 {
		// One failed job cancels its siblings, so failing check_run events land while
		// the run is still finishing — GitHub refuses to rerun a run in progress, and
		// the same holds for a stale failure event arriving after we've already kicked
		// off the rerun. Either way: wait; the run's completion re-triggers us.
		if status, err := e.runStatus(ctx, t, runID); err == nil && status != "completed" {
			e.log("%s run %d still %s — waiting for it to finish", tag(t), runID, status)
			return
		}
		maxRerun := act.FlakyRerun.Max
		if maxRerun <= 0 {
			maxRerun = 1
		}
		if e.store.Attempts(key, "failing_checks_rerun", head) < maxRerun {
			if err := e.rerun(ctx, t, runID); err != nil {
				// Not requested, so don't burn the attempt; fall through to the fixer.
				e.log("%s flaky rerun run %d: %v — dispatching the fixer instead", tag(t), runID, err)
			} else {
				_ = e.store.Record(key, "failing_checks_rerun", head, head)
				e.store.Audit(map[string]any{"event": "flaky_rerun", "repo": t.Target.Repo,
					"number": t.Target.Number, "run_id": runID})
				return // wait for the rerun; a fresh failure will re-trigger
			}
		}
	}

	// Gate. Most kinds dedup on the acted signature (act once per state). But a
	// review's "done" is external — you submitted, so you're no longer a requested
	// reviewer — not "we launched something once." For those, gate on whether a
	// conductor agent for this PR is already working/parked instead of a permanent
	// dedup flag, so a still-pending review keeps coming back until you do it.
	liveGate := livenessGated(t.Kind)
	if t.Force {
		// Forced: skip the dedup / liveness gates entirely and dispatch below.
	} else if liveGate {
		// A review workflow shouldn't re-run while its agent is parked for you.
		// Single-action fixers instead fall through to dispatch, which queues new
		// work to the agent already on this PR (or spawns one) — see paseo.go.
		if len(act.Steps) > 0 && e.disp.HasLiveAgent(ctx, key, t.Kind) {
			// Exception: a review re-request on a NEW head. If we've never dispatched
			// review_requested at the current head, the parked agent is reviewing stale
			// code — re-engage on the new head instead of being blocked indefinitely by
			// it. A same-head re-request (or a duplicate webhook delivery) has a recorded
			// attempt at this head, so it's suppressed here: no double-fire, no re-review
			// of identical code.
			if t.Kind == "review_requested" && head != "" && e.store.Attempts(key, dkind, head) == 0 {
				e.log("%s re-engaging — review re-requested on a new head %s (agent parked on older code)", tag(t), short(head))
			} else {
				e.log("%s skipped — an agent is already working/parked for it", tag(t))
				return
			}
		}
		// A live-gated kind never records "done" on dispatch — the sweep re-derives
		// reality (still dirty? threads unresolved? review pending?) each run, so
		// culled/failed work isn't abandoned. The backoff below bounds the retries.
	} else if t.Dedup != "" && e.store.LastSignature(key, dkind) == t.Dedup {
		return
	}

	// Past the soft threshold (max_attempts_per_head), gate retries behind a GROWING
	// backoff instead of a hard cap — a struggling (pr,kind,head) keeps getting
	// periodic retries with widening gaps (10m→30m→…→24h) rather than being abandoned
	// forever. Escalate once, when it first crosses the threshold. The cadence
	// and threshold come from the trigger's merged policy (scoped for flow
	// triggers, global otherwise); the constants are the defaults.
	pol := e.retryPolicyFor(act)
	soft := act.MaxAttemptsPerHead
	if soft == 0 && pol.MaxAttemptsPerHead != nil {
		soft = *pol.MaxAttemptsPerHead
	}
	if soft == 0 && t.Kind != "new_comment" {
		soft = defaultMaxAttempts
	}
	base, max := retryBackoffBase, retryBackoffMax
	if pol.Backoff != nil {
		if d := pol.Backoff.Base.D(); d > 0 {
			base = d
		}
		if d := pol.Backoff.Max.D(); d > 0 {
			max = d
		}
	}
	if soft > 0 && !t.Force {
		if n := e.store.Attempts(key, dkind, head); n >= soft {
			if ready, wait := e.store.RetryReady(key, dkind, head, soft, base, retryBackoffFactor, max); !ready {
				e.log("%s in backoff — %d attempts at %s, next retry in ~%s",
					tag(t), n, short(head), wait.Round(time.Minute))
				return
			}
			if n == soft { // first time past the threshold and now eligible — say so, once
				// notif.Emit audits the escalate for status/report (no separate row here).
				e.notif.Emit(ctx, notify.EventEscalate, t,
					fmt.Sprintf("still failing after %d tries at %s — backing off, will keep retrying periodically", soft, short(head)))
			}
		}
	}

	// A connectors-model trigger: the generic gates above (kill switch,
	// pause, comment high-water mark, dedup, backoff) have all applied; the
	// flow runner owns steps/hooks/verbs from here.
	if act.FlowRef != "" {
		if e.flow == nil {
			e.log("%s flow trigger but no flow runner wired — dropping", tag(t))
			return
		}
		e.processFlow(ctx, t, act, key, dkind, head)
		return
	}

	// Resolve the step's behavior, tokens, shadow. A legacy (integrations:)
	// Action carries no behavior fields of its own, so `agent:` is where it
	// points at some: a STEP REFERENCE (`<workflow>/<step-id>`) naming a
	// step in the operator's own workflows. Anything else is a plain
	// attribution label and the dispatch runs on the policy baseline alone
	// — deny-by-default, so no Action inherits memory access or a skill
	// grant nobody wrote down. Either way `agent:` supplies the IDENTITY,
	// so a box's memory/session/outcome keys are unchanged.
	profile, identity := e.actionProfile(act.Agent)
	if act.Backend != "" {
		profile.Runtime = act.Backend
	}
	model := e.resolveModel(ctx, profile)
	if act.Type == "agent" {
		if act.Prompt != "" {
			act.Prompt += dispatch.WriteWrapperGuidance
			act.Prompt += e.agentGuidance(profile, e.retryPolicyFor(act))
			act.Prompt += e.memoryPrompt(identity, profile, t, "")
			if act.RerequestReview {
				act.Prompt += dispatch.RerequestReviewGuidance
			}
			// NOTE: no HoldGuidance here. A top-level single-action agent is an
			// autonomous fixer (new_comment/changes_requested/merge_conflict/
			// failing_checks/issue_matched) — it should make the best decision and
			// finish, not pose courtesy questions. Interactive "ask me" behavior is
			// reserved for review hand-offs (steps.go → HandoffGuidance on background
			// steps). A fixer that can't proceed just stops; live-gated kinds re-derive
			// via the sweep.
		}
	}
	appTok, _ := t.Context["app_token"].(string)
	if e.readTok != nil { // identity.read_token override → reads use it, not the App token
		if tok, err := e.readTok(); err == nil && tok != "" {
			appTok = tok
		}
	}
	userTok := ""
	if e.userTok != nil {
		userTok, _ = e.userTok()
	}
	shadow := e.cfg.Control.Shadow || (act.Shadow != nil && *act.Shadow)

	// Multi-step workflow: record now (so it doesn't re-fire), take a slot as
	// backpressure, and run the steps in their own goroutine (releasing the slot
	// when the foreground steps finish) so the engine loop isn't blocked by them.
	if len(act.Steps) > 0 {
		if !shadow {
			if liveGate {
				// Live-gated kinds don't consume the permanent dedup (the sweep re-derives
				// them), but we DO mark the head we dispatched at, so a same-head re-request
				// or duplicate delivery is suppressed while a genuinely new head re-engages
				// (see the live-gate exception above).
				_ = e.store.RecordAttempt(key, dkind, head)
			} else { // shadow previews never record
				_ = e.store.Record(key, dkind, t.Dedup, head)
			}
		}
		e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
		e.log("%s workflow (%d steps%s)", tag(t), len(act.Steps), shadowNote(shadow))
		if !shadow && !e.acquire(ctx) {
			return
		}
		run := e.newRun(t, act, shadow)
		go func() {
			defer e.recoverDispatch(ctx, t, run, "workflow dispatch")
			if !shadow {
				defer e.release()
			}
			e.runSteps(ctx, run, t, act, appTok, userTok, shadow)
		}()
		return
	}

	req := dispatch.Request{
		Trigger: t, Action: act, Step: profile, Identity: identity, Model: model,
		Tokens: dispatch.Tokens{App: appTok, User: userTok},
		Author: e.author, Shadow: shadow, CatchUp: t.CatchUp,
	}

	// Resolve which controller runs this agent before taking a slot. Commands
	// aren't controller-selected (they're a local subprocess). For today's configs
	// this is the paseo dispatcher and `run` == e.disp — no behavior change.
	run := Dispatcher(e.disp)
	if act.Type == "agent" {
		r, rerr := e.runnerFor(profile)
		if rerr != nil {
			e.log("%s no runnable controller: %v", tag(t), rerr)
			e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("no runnable controller for agent %q: %v", act.Agent, rerr))
			if !shadow {
				_ = e.store.RecordAttempt(key, dkind, head)
			}
			return
		}
		run = r
	}

	// spendRes is the budget reservation an admitted agent dispatch holds
	// until its usage settles (or the dispatch never charges — cancelled).
	var spendRes *cost.Reservation
	// Coding agents are heavy and contend on a shared repo. Acquire a slot first
	// (this blocks the loop as backpressure when the cap is full), then hold it in
	// the background until the launched agent goes idle — so the cap bounds the
	// number of *running* agents. Commands (gh merge/update-branch, critique) are
	// cheap and ungated. Checks/record/notify stay synchronous either way.
	if act.Type == "agent" && !shadow {
		// Runaway guard: cap agent dispatches per rolling hour. Over budget → shed
		// (record an attempt so live-gated/backoff kinds re-run once the window frees;
		// commands stay ungated). Protects the box from a webhook flood or a sweep
		// misfire spinning up unbounded agents.
		if max := e.cfg.AgentsPerHour(); max > 0 && e.overAgentBudget(max) {
			e.log("%s agent budget reached (%d/hr) — shedding, will retry later", tag(t), max)
			_ = e.store.RecordAttempt(key, dkind, head) // so it isn't silently forgotten
			return
		}
		// Spend budget (#36 §14): same shed semantics as the count budget —
		// record the attempt, retry when the rolling window frees, notify.
		// An admitted dispatch RESERVES its estimated spend (settled or
		// cancelled below).
		res, berr := e.checkSpendBudget(e.runtimeOf(profile), nil, "", cost.Estimate(model, act.Prompt, ""))
		if berr != nil {
			e.shedForBudget(ctx, t, berr, shadow)
			return
		}
		spendRes = res
		if !e.acquire(ctx) {
			e.meter.Cancel(spendRes)
			return
		}
		e.recordAgentDispatch()
	}

	e.notif.Emit(ctx, notify.EventDispatch, t, act.Type)
	if act.Type == "command" {
		e.log("%s running (%s)", tag(t), actionDesc(act))
	}
	start := time.Now()
	ref, err := e.dispatchAgent(ctx, run, req)
	took := time.Since(start).Round(time.Second)
	if act.Type == "command" && err == nil && !ref.Skipped {
		tail := ""
		if tl := tailOutput(ref.Output); tl != "" {
			tail = "\n" + e.redact(tl)
		}
		e.log("%s command done (%s) in %s%s", tag(t), ref.Backend, took, tail)
	}
	e.auditDispatch(t, ref, err)
	// Cost accounting (#36 §14): charge the run's usage (runtime-reported
	// where the output carries it, else an approximate estimate) to the
	// budget scopes and the audit, settling the dispatch's reservation.
	// Output-less (background) runs meter the prompt side now; their output
	// lands in later accounting as approximate. Non-charging outcomes
	// (skip/queue/error) cancel the reservation instead.
	if act.Type == "agent" && err == nil && !ref.Skipped && !ref.Shadowed && !ref.Queued {
		e.recordUsage(t, identity, e.runtimeOf(profile), act.ID, "", "", spendRes, cost.FromRun(model, act.Prompt, ref.Output))
	} else {
		e.meter.Cancel(spendRes)
	}
	gated := act.Type == "agent" && !shadow

	// A catch-up whose PR already has a working agent did nothing — don't record it
	// (it isn't an attempt) and free the slot.
	if ref.Skipped {
		e.log("%s %s", tag(t), e.redact(ref.Output))
		if gated {
			e.release()
		}
		return
	}
	// A dispatch killed by our own shutdown (ctx cancelled / `paseo run` SIGTERM'd)
	// isn't a real attempt — don't count it toward backoff or escalate; the sweep
	// re-derives it cleanly on restart.
	if err != nil && interruptedByShutdown(ctx, err) {
		e.log("%s interrupted by shutdown — not counting an attempt", tag(t))
		if gated {
			e.release()
		}
		return
	}
	if !shadow {
		switch {
		case liveGate:
			// Never mark "done" on dispatch — the sweep re-derives completion. Just
			// count the attempt; backoff bounds retries of an unfixable state.
			_ = e.store.RecordAttempt(key, dkind, head)
		case err != nil:
			// A failed dispatch: count the try but don't consume the dedup signature,
			// so it retries next time instead of being suppressed forever.
			_ = e.store.RecordAttempt(key, dkind, head)
		default:
			_ = e.store.Record(key, dkind, t.Dedup, head)
		}
	}

	if err != nil {
		if tl := tailOutput(ref.Output); tl != "" {
			e.log("%s command output (tail):\n%s", tag(t), e.redact(tl))
		}
		e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("dispatch failed: %s", e.redact(err.Error())))
		if gated {
			e.release()
		}
		return
	}
	// A comment was handled (fresh agent, queued, or adopted) — raise the high-water
	// mark so the sweep's re-listing of recent comments won't re-dispatch this one.
	if t.Kind == "new_comment" {
		if id := commentID(t); id > 0 {
			_ = e.store.AdvanceCommentID(key, commentKind(t), id)
		}
	}

	// A finished agent's captured output may carry the memory output contract.
	// Queued/adopted work has no final output here; shadow previews never write.
	// Single-action dispatches have no WorkflowRun record; the agent id is the
	// run identity that provenance (Source.Run) can trace back.
	if act.Type == "agent" && !shadow && !ref.Queued {
		e.harvestMemory(t, act.Agent, ref.AgentID, ref.Output, profile.Memory)
	}

	if ref.Queued {
		// Work was handed to an agent already on the PR — no new agent, no slot to
		// hold; it'll drain the queue on its own.
		if ref.Adopted {
			e.log("%s adopted your open workspace agent %s", tag(t), ref.AgentID)
		} else {
			e.log("%s queued to agent %s", tag(t), ref.AgentID)
		}
		if gated {
			e.release()
		}
		return
	}
	e.notif.Emit(ctx, notify.EventComplete, t, ref.Backend)
	if gated && !ref.Shadowed && ref.AgentID != "" {
		go func() {
			defer e.recoverDispatch(ctx, t, store.WorkflowRun{}, "agent wait")
			defer e.release()
			run.WaitForAgent(ctx, ref.AgentID, agentWaitTimeout(profile))
		}()
	} else if gated {
		e.release()
	}
}

// newRun builds (and, unless shadow, persists) a WorkflowRun so a multi-step
// workflow can resume across a restart. The persisted trigger has its tokens
// stripped (they're re-minted on resume) and its Action detached (stored raw).
func (e *Engine) newRun(t core.Trigger, act config.Action, shadow bool) store.WorkflowRun {
	run := store.WorkflowRun{
		ID:       t.Kind + ":" + t.Key(),
		Source:   t.Source,
		Instance: t.Instance,
		Kind:     t.Kind,
		Repo:     t.Target.Repo,
		Number:   t.Target.Number,
		Outputs:  map[string]map[string]any{},
	}
	tp := t
	tp.Action = nil
	tp.Context = sanitizeContext(t.Context)
	run.Trigger, _ = json.Marshal(tp)
	run.Action, _ = json.Marshal(act)
	if shadow || e.store == nil {
		run.ID = "" // shadow previews aren't tracked/persisted
		return run
	}
	_ = e.store.PutRun(run)
	return run
}

// saveRun persists an updated run (no-op for untracked/shadow runs).
func (e *Engine) saveRun(run store.WorkflowRun) {
	if run.ID != "" {
		_ = e.store.PutRun(run)
	}
}

// finishRun removes a completed/failed run from persistence.
func (e *Engine) finishRun(run store.WorkflowRun) {
	if run.ID != "" {
		_ = e.store.DeleteRun(run.ID)
	}
}

// recoverDispatch is the top-frame panic guard for a per-event dispatch
// goroutine. A panic inside runSteps/flow.Run/handoff.Review must not take the
// whole daemon down and every other in-flight run with it: recover here, log
// the stack, escalate to the operator, and clear the persisted run so it is
// recorded failed rather than left dangling as in-flight (which ResumeWorkflows
// would otherwise re-drive on the next start, straight back into the same
// panic). The goroutine's own deferred e.release() still runs — recover only
// swallows the panic, it does not skip the other defers — so the concurrency
// slot and any budget reservation are returned normally. run may be a zero
// value (no persisted run in scope, e.g. the WaitForAgent/review goroutines);
// finishRun is then a no-op.
func (e *Engine) recoverDispatch(ctx context.Context, t core.Trigger, run store.WorkflowRun, what string) {
	r := recover()
	if r == nil {
		return
	}
	e.log("%s PANIC in %s: %v\n%s", tag(t), what, r, debug.Stack())
	e.store.Audit(map[string]any{"event": "panic_recovered", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "run": run.ID, "where": what})
	e.notif.Emit(ctx, notify.EventEscalate, t,
		fmt.Sprintf("internal error in %s — run recorded failed: %v", what, r))
	e.finishRun(run)
}

// sanitizeContext copies a trigger context minus secrets (re-minted on resume).
func sanitizeContext(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if k == "app_token" || k == "gh_token" {
			continue
		}
		out[k] = v
	}
	return out
}

// ResumeWorkflows re-runs any workflow that was in-flight when the conductor last
// stopped. Prior steps' outputs are restored; the interrupted step re-runs
// (at-least-once). Tokens are re-minted. Disabled if RefreshAppToken is unset.
func (e *Engine) ResumeWorkflows(ctx context.Context) {
	if e.refreshTok == nil {
		return
	}
	for _, r := range e.store.PendingRuns() {
		var t core.Trigger
		var act config.Action
		if json.Unmarshal(r.Trigger, &t) != nil || json.Unmarshal(r.Action, &act) != nil {
			e.log("engine: resume %s: unreadable, dropping", r.ID)
			_ = e.store.DeleteRun(r.ID)
			continue
		}
		// A flow run resumes through the flow runner (its own checkpoint
		// model); token re-minting below still applies first when possible.
		if act.FlowRef != "" {
			if e.flow == nil {
				e.log("engine: resume %s: flow run but no flow runner — leaving for next start", r.ID)
				continue
			}
			if t.Context != nil {
				if appTok, err := e.refreshTok(t); err == nil && appTok != "" {
					t.Context["app_token"] = appTok
				}
			}
			e.resumeFlowRun(ctx, r, t, act)
			continue
		}
		t.Action = act
		appTok, err := e.refreshTok(t)
		if err != nil {
			e.log("engine: resume %s: app token: %v (leaving for next start)", r.ID, err)
			continue
		}
		if e.readTok != nil { // identity.read_token override
			if tok, terr := e.readTok(); terr == nil && tok != "" {
				appTok = tok
			}
		}
		userTok := ""
		if e.userTok != nil {
			userTok, _ = e.userTok()
		}
		if t.Context == nil {
			t.Context = map[string]any{}
		}
		t.Context["app_token"] = appTok
		run := r
		if run.Outputs == nil {
			run.Outputs = map[string]map[string]any{}
		}
		e.log("%s resuming workflow from step %d", tag(t), r.StepIndex)
		e.store.Audit(map[string]any{"event": "resume", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "step_index": r.StepIndex})
		if !e.acquire(ctx) {
			return
		}
		go func() {
			defer e.recoverDispatch(ctx, t, run, "workflow resume")
			defer e.release()
			e.runSteps(ctx, run, t, act, appTok, userTok, false)
		}()
	}
}

// Backoff schedule for retries past max_attempts_per_head: 10m, 30m, 90m, … ×3
// each step, capped at 24h. Never a permanent give-up — a struggling PR just
// retries with widening gaps (longer elapsed ⇒ less likely recoverable).
const (
	retryBackoffBase   = 10 * time.Minute
	retryBackoffFactor = 3
	retryBackoffMax    = 24 * time.Hour
	// defaultMaxAttempts is the soft threshold (retries before backoff begins) when
	// an action doesn't set max_attempts_per_head. new_comment is exempt — distinct
	// comments share a kind@head attempt key, so a cap there would throttle real work.
	defaultMaxAttempts = 3
)

// tag is a stable log prefix tying a line to its integration + target + kind, so
// all work for one PR/issue is greppable: engine[<instance> <repo>#<num> <kind>#<variant>].
func tag(t core.Trigger) string {
	id := t.Target.Repo
	if t.Target.PR > 0 {
		id = fmt.Sprintf("%s#%d", id, t.Target.PR)
	} else if t.Target.Issue > 0 {
		id = fmt.Sprintf("%s#%d", id, t.Target.Issue)
	}
	return fmt.Sprintf("engine[%s %s %s]", t.Instance, id, t.Kind+variantSuffix(t.Variant))
}

// variantSuffix renders "#name" for logs when a trigger is a named variant.
func variantSuffix(v string) string {
	if v == "" {
		return ""
	}
	return "#" + v
}

// interruptedByShutdown reports whether a dispatch error is just the daemon going
// down (ctx cancelled, or the `paseo run` child killed by our SIGTERM) rather than
// a real failure — so we don't burn an attempt or escalate on a clean shutdown.
func interruptedByShutdown(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "signal: terminated") ||
		strings.Contains(s, "signal: killed") ||
		strings.Contains(s, "context canceled")
}

// livenessGated reports whether a kind's completion is EXTERNAL state the sweep
// re-derives each run (review still pending? PR still dirty? threads still
// unresolved?) rather than a one-shot "we dispatched once" flag. For these we
// never record "done" on dispatch — a culled/failed/incomplete agent would
// otherwise mark the work done and it'd be abandoned. Instead we gate on whether
// an agent is already working/parked for it, and let the sweep retry until the
// underlying condition clears. new_comment stays dedup-gated (keyed per comment
// id — each distinct comment must be handled, not collapsed to "an agent ran").
func livenessGated(kind string) bool {
	switch kind {
	case "review_requested", "merge_conflict", "changes_requested":
		return true
	}
	return false
}

// runnerFor resolves the controller that runs an agent (from the profile's
// `controller:`, then the default:true controller, then the built-in paseo) and
// returns its dispatch surface. An error means the controller is unknown or its
// transport isn't runnable in this build — the caller escalates rather than
// silently dispatching through the wrong runtime. For today's configs (no
// `controllers:` block) this always resolves to the paseo dispatcher.
func (e *Engine) runnerFor(profile config.Step) (Dispatcher, error) {
	// RuntimeName() honors the connectors-model `runtime:` field (falling back
	// to the legacy `controller:`). Reading `.Controller` directly here left a
	// `runtime:`-only profile resolving to the default runtime on first
	// dispatch, even though the session-affinity path already used
	// RuntimeName() — so a plugin/ACP runtime selected via `runtime:` was
	// silently skipped until a follow-up turn (#54).
	run, err := e.controllers.RunnerFor(profile.Runtime)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// actionProfile resolves a legacy Action's `agent:` to the step it points at
// and that step's identity. Deny-by-default: a reference that names nothing
// yields an empty step, so an Action can never inherit memory access or a
// skill grant the operator did not write down somewhere addressable — and
// the raw string stays the identity, so nothing about its history moves.
//
// When the reference DOES resolve, the identity is the referenced step's,
// which is what the session sweep computes when it walks the config looking
// for an `end_on:` to honor. Two different answers there would bind a
// session under one key and try to evict it under another.
func (e *Engine) actionProfile(ref string) (config.Step, string) {
	if e.cfg == nil || ref == "" {
		return config.Step{}, ref
	}
	s, identity, err := e.cfg.FindStepIdentity(ref)
	if err != nil {
		return config.Step{}, ref
	}
	return *s, identity
}

// controllerFor resolves the controller (not just its runner) that owns an
// agent's sessions — the review hand-off needs it to open/resume a broker session
// (NewSession/ResumeSession), not only the dispatch runner.
func (e *Engine) controllerFor(profile config.Step) (controller.Controller, error) {
	return e.controllers.Resolve(profile.Runtime)
}

// acquire takes a concurrency slot, blocking until one is free (backpressure).
// Returns false if the context is cancelled first. No-op (true) when uncapped.
func (e *Engine) acquire(ctx context.Context) bool {
	if e.sem == nil {
		return true
	}
	select {
	case e.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns a concurrency slot.
func (e *Engine) release() {
	if e.sem != nil {
		<-e.sem
	}
}

// redact scrubs tracked secret values ("" resolver = passthrough).
func (e *Engine) redact(v string) string {
	if e.secrets == nil {
		return v
	}
	return e.secrets.Redact(v)
}

// redactArgv scrubs an argv copy for audit.
func (e *Engine) redactArgv(argv []string) []string {
	if e.secrets == nil || len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = e.secrets.Redact(a)
	}
	return out
}

// auditDispatch writes the dispatch audit entry and logs the outcome.
func (e *Engine) auditDispatch(t core.Trigger, ref dispatch.RunRef, err error) {
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "failed"
	case ref.Skipped:
		outcome = "skipped"
	case ref.Adopted:
		outcome = "adopted"
	case ref.Queued:
		outcome = "queued"
	case ref.Shadowed:
		outcome = "shadow"
	}
	entry := map[string]any{
		"event": "dispatch", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "backend": ref.Backend, "argv": e.redactArgv(ref.Argv),
		"shadow": ref.Shadowed, "agent_id": ref.AgentID, "outcome": outcome,
	}
	if err != nil {
		entry["error"] = e.redact(err.Error())
		e.log("%s dispatch failed: %s", tag(t), e.redact(err.Error()))
	} else {
		e.log("%s dispatched (backend=%s shadow=%v)", tag(t), ref.Backend, ref.Shadowed)
	}
	e.store.Audit(entry)
	if core.CompletionHook != nil {
		core.CompletionHook(t, outcome)
	}
}

// agentWaitTimeout bounds how long a slot is held waiting for an agent to idle,
// so a stuck agent eventually frees its slot. Derived from the profile's
// wait_timeout (plus grace), else a one-hour backstop.
func agentWaitTimeout(p config.Step) time.Duration {
	if d := p.WaitTimeout.D(); d > 0 {
		return d + 5*time.Minute
	}
	return time.Hour
}

// rerunFailed re-runs the failed jobs of a workflow run, as you. Returns the gh
// error (with its output) when the rerun could not be requested.
func (e *Engine) rerunFailed(ctx context.Context, t core.Trigger, runID int64) error {
	c := exec.CommandContext(ctx, "gh", "run", "rerun", fmt.Sprintf("%d", runID),
		"--failed", "--repo", t.Target.Repo)
	c.Env = append(os.Environ(), "GH_TOKEN="+e.userToken())
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	e.log("%s flaky rerun triggered (run %d)", tag(t), runID)
	return nil
}

// workflowRunStatus reads a workflow run's status (queued|in_progress|completed|…), as you.
func (e *Engine) workflowRunStatus(ctx context.Context, t core.Trigger, runID int64) (string, error) {
	c := exec.CommandContext(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/runs/%d", t.Target.Repo, runID),
		"--jq", ".status")
	c.Env = append(os.Environ(), "GH_TOKEN="+e.userToken())
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// userToken returns your GitHub token ("" when unavailable).
func (e *Engine) userToken() string {
	if e.userTok == nil {
		return ""
	}
	tok, _ := e.userTok()
	return tok
}

func toInt64(v any) int64 {
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

// commentID reads a new_comment trigger's source comment id from Context (0 if
// absent — e.g. an older trigger without the field, which then can't be gated).
func commentID(t core.Trigger) int64 {
	if t.Context == nil {
		return 0
	}
	return toInt64(t.Context["comment_id"])
}

// commentMarkKind returns the high-water-mark key for a comment trigger:
// the comment kind, suffixed per variant for connectors-model triggers so
// sibling triggers on the same event keep independent marks.
func commentMarkKind(t core.Trigger) string {
	ck := commentKind(t)
	if act, ok := t.Action.(config.Action); ok && act.FlowRef != "" && t.Variant != "" {
		ck += "#" + t.Variant
	}
	return ck
}

// commentKind reads a new_comment trigger's comment kind (store.CommentKindIssue /
// store.CommentKindReview) from Context, selecting which high-water mark applies.
// Absent (an older trigger) → issue, matching the pre-per-kind single mark.
func commentKind(t core.Trigger) string {
	if t.Context != nil {
		if k, _ := t.Context["comment_kind"].(string); k != "" {
			return k
		}
	}
	return store.CommentKindIssue
}

func shadowNote(shadow bool) string {
	if shadow {
		return ", shadow"
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
