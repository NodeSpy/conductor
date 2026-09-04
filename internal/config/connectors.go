// New-schema (connectors-model) configuration types.
//
// The connectors model replaces the per-integration `integrations:` list with
// four cooperating blocks — `connectors:` (external services: sources +
// verbs), `runtimes:`/`agents:` (where work runs and named profiles),
// `triggers:` (the on/filters/steps/hooks grammar), and optional `hosts:`,
// `workflows:`, `policy:`, `secrets:`. Both schemas coexist: a file may carry
// either (or, during migration, both); Config.HasConnectors reports which
// world a load is in. Structural validation lives here; semantic validation
// against each connector's published event/verb schemas lives in
// internal/connector (which can see the schemas).
package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConnectorRef is one entry in the `connectors:` map: the common header plus
// the raw node so the concrete connector type can decode its own connection
// fields (tokens, app creds, schedules, feeds, …).
type ConnectorRef struct {
	Type    string `yaml:"type,omitempty"`
	Enabled *bool  `yaml:"enabled,omitempty"`
	// Options are the connector's default verb options; each `uses:` call's
	// options merge over these (the call wins). Identity (`as:`) lives here.
	Options map[string]any `yaml:"options,omitempty"`
	// Policy is the connector-scoped policy block (ignore/rate_limits/backoff/
	// pause_label live here; quiet_hours/concurrency may override the global).
	Policy *Policy `yaml:"policy,omitempty"`
	raw    yaml.Node
}

// UnmarshalYAML captures the header fields and retains the raw node.
func (r *ConnectorRef) UnmarshalYAML(n *yaml.Node) error {
	type hdr struct {
		Type    string         `yaml:"type,omitempty"`
		Enabled *bool          `yaml:"enabled,omitempty"`
		Options map[string]any `yaml:"options,omitempty"`
		Policy  *Policy        `yaml:"policy,omitempty"`
	}
	var h hdr
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Type, r.Enabled, r.Options, r.Policy, r.raw = h.Type, h.Enabled, h.Options, h.Policy, *n
	return nil
}

// Decode unmarshals the raw connector node into a type-specific struct.
func (r ConnectorRef) Decode(v any) error { return r.raw.Decode(v) }

// IsEnabled reports whether the connector is enabled (default true).
func (r ConnectorRef) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// RuntimeConfig is one entry in the `runtimes:` map — where agents run
// (today's controllers, renamed, plus launch config that used to be global).
type RuntimeConfig struct {
	// Type is a built-in runtime kind: paseo | agent-deck | opencode | cli.
	// Mutually exclusive with Agent.
	Type string `yaml:"type,omitempty"`
	// Agent names an agent runtime driven over a transport (gemini, opencode,
	// …). Mutually exclusive with Type; implies transport acp unless overridden.
	Agent string `yaml:"agent,omitempty"`
	// Transport is how conductor talks to the runtime: acp | native | cli.
	Transport string `yaml:"transport,omitempty"`
	// SessionModel hints session persistence: native | resumable | oneshot.
	SessionModel string `yaml:"session_model,omitempty"`
	// Default flags the fleet-default runtime (at most one).
	Default bool `yaml:"default,omitempty"`
	// Bin is the runtime's binary (paseo/agent-deck); replaces the global
	// paseo_bin.
	Bin string `yaml:"bin,omitempty"`
	// Tool and Command are the bare-CLI recipe for transport: cli.
	Tool    string   `yaml:"tool,omitempty"`
	Command []string `yaml:"command,omitempty"`
	// Host names a `hosts:` entry; the runtime's subprocesses run there over
	// SSH (all its agents launch on that box).
	Host string `yaml:"host,omitempty"`
}

// Controller converts a runtime entry to the legacy controller shape the
// controller registry consumes, carrying Bin and Host through.
func (r RuntimeConfig) Controller() ControllerConfig {
	return ControllerConfig{
		Type: r.Type, Agent: r.Agent, Transport: r.Transport,
		SessionModel: r.SessionModel, Default: r.Default,
		Tool: r.Tool, Command: r.Command,
		Bin: r.Bin, Host: r.Host,
	}
}

// HostConfig is one entry in the `hosts:` map — a named SSH target referenced
// by `host:` on connectors, runtimes, agents, and code steps.
type HostConfig struct {
	Host string `yaml:"host,omitempty"` // hostname or address (required)
	User string `yaml:"user,omitempty"`
	Port int    `yaml:"port,omitempty"`
	// Key is the private-key path (empty = ssh defaults / agent).
	Key string `yaml:"key,omitempty"`
	// KnownHosts is a known_hosts file override (empty = ssh defaults).
	KnownHosts string `yaml:"known_hosts,omitempty"`
	// Cwd is the default remote working directory.
	Cwd string `yaml:"cwd,omitempty"`
	// Env is exported into remote commands.
	Env map[string]string `yaml:"env,omitempty"`
}

// WorkflowDef is one entry in the `workflows:` map — a named, parameterized
// step list invoked from triggers (or other workflows) via `workflow:`.
type WorkflowDef struct {
	Inputs  map[string]InputSpec `yaml:"inputs,omitempty"`
	Outputs map[string]string    `yaml:"outputs,omitempty"` // name -> template over internal step outputs
	Steps   []Step               `yaml:"steps,omitempty"`
}

// InputSpec declares one workflow input.
type InputSpec struct {
	Type     string `yaml:"type,omitempty"` // string | integer | number | boolean | list | map | any
	Required bool   `yaml:"required,omitempty"`
	Default  any    `yaml:"default,omitempty"`
}

// TriggerSpec is one entry in the `triggers:` list: on/filters/steps/hooks
// plus optional grouping, policy, and source-side options.
type TriggerSpec struct {
	// On selects the inbound event: <connector>.<event>.
	On string `yaml:"on,omitempty"`
	// Name is an optional variant name (distinguishes dedup/attempt state when
	// several triggers listen to the same event; mirrors legacy action names).
	Name    string `yaml:"name,omitempty"`
	Enabled *bool  `yaml:"enabled,omitempty"`
	// Filters gate whether the trigger fires; legal keys come from the event's
	// filter schema. All AND-ed.
	Filters map[string]any `yaml:"filters,omitempty"`
	// Group batches a burst of related events into one run (debounce).
	Group *GroupSpec `yaml:"group,omitempty"`
	Steps []Step     `yaml:"steps,omitempty"`
	Hooks []Hook     `yaml:"hooks,omitempty"`
	// Policy is the trigger-scoped policy block (most specific — wins).
	Policy *Policy `yaml:"policy,omitempty"`
	// Options are source-side per-trigger options validated against the
	// event's option schema (e.g. github flaky_rerun / stuck_after).
	Options map[string]any `yaml:"options,omitempty"`
	// Repo pins the checkout repo for synthetic sources (sentry/pagerduty/
	// webhook/rss); empty keeps the scratch/no-checkout behavior.
	Repo string `yaml:"repo,omitempty"`
	// Shadow previews this trigger's work without dispatching.
	Shadow *bool `yaml:"shadow,omitempty"`
}

// IsEnabled reports whether the trigger is enabled (default true).
func (t TriggerSpec) IsEnabled() bool { return t.Enabled == nil || *t.Enabled }

// Connector returns the connector name from On ("gh.new_comment" -> "gh").
func (t TriggerSpec) Connector() string {
	c, _, _ := strings.Cut(t.On, ".")
	return c
}

// Event returns the event name from On ("gh.new_comment" -> "new_comment").
func (t TriggerSpec) Event() string {
	_, e, _ := strings.Cut(t.On, ".")
	return e
}

// GroupSpec batches events: key groups them, window debounces.
type GroupSpec struct {
	// Key is the grouping expression (templated). Default: the event's own
	// dedup id — every event is its own run.
	Key string `yaml:"key,omitempty"`
	// Window is the debounce window: it resets on each new event and the batch
	// fires when the group goes quiet. Default 15s.
	Window Duration `yaml:"window,omitempty"`
	// MaxWait caps how long a busy group can defer firing (default 4×window).
	MaxWait Duration `yaml:"max_wait,omitempty"`
}

// Step is one entry in a `steps:` list (and the body of hooks' action units).
// Exactly one of the step forms must be set: `type: agent`, `type: command`,
// `run:` (code), `uses:` (verb), or `use:` (workflow call).
type Step struct {
	ID string `yaml:"id,omitempty"`
	If string `yaml:"if,omitempty"`
	// Type is agent | command for the do-work forms ("" for uses/run/use).
	Type string `yaml:"type,omitempty"`

	// agent form
	Agent           string         `yaml:"agent,omitempty"`
	Prompt          string         `yaml:"prompt,omitempty"`
	Checkout        string         `yaml:"checkout,omitempty"`
	OutputSchema    map[string]any `yaml:"output_schema,omitempty"`
	Background      bool           `yaml:"background,omitempty"`
	Handoff         string         `yaml:"handoff,omitempty"` // ask-capable connector for a background review
	RerequestReview bool           `yaml:"rerequest_review,omitempty"`

	// command form (also carries workdir/env for agent/code forms)
	Command []string          `yaml:"command,omitempty"`
	WorkDir string            `yaml:"workdir,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`

	// code form: run: js | go-embed | risor | lua | go | sh | bash | ruby | node | …
	Run  string   `yaml:"run,omitempty"`
	Code string   `yaml:"code,omitempty"`
	Args []string `yaml:"args,omitempty"`
	// Host names a `hosts:` entry to run a host-interpreter code step, or a
	// command step, on a remote box. SSH is a one-off inline target.
	Host string      `yaml:"host,omitempty"`
	SSH  *HostConfig `yaml:"ssh,omitempty"`

	// verb form
	Uses    string         `yaml:"uses,omitempty"`
	Options map[string]any `yaml:"options,omitempty"`

	// workflow-call form. Workflow names a reusable workflow (defined inline
	// or in any imported file), or is a bare file path when that file defines
	// exactly one workflow; Import names the file a `workflow: <name>` lives
	// in without a section-level import. File forms are materialized into
	// cfg.Workflows at load (see resolveWorkflowFiles).
	Workflow string         `yaml:"workflow,omitempty"`
	Import   string         `yaml:"import,omitempty"`
	With     map[string]any `yaml:"with,omitempty"`

	// control flow
	ForEach         string        `yaml:"for_each,omitempty"` // template resolving to a list; {{.item}} in scope
	Parallel        *ParallelSpec `yaml:"parallel,omitempty"`
	Retry           *RetrySpec    `yaml:"retry,omitempty"`
	Timeout         Duration      `yaml:"timeout,omitempty"`
	ContinueOnError bool          `yaml:"continue_on_error,omitempty"`

	// step-level hooks, scoped to this step
	Hooks []Hook `yaml:"hooks,omitempty"`

	Backend string `yaml:"backend,omitempty"` // dispatch backend override (carried from legacy)
	Shadow  *bool  `yaml:"shadow,omitempty"`
}

// Form returns the step's form keyword: "agent", "command", "code", "verb",
// "workflow", "parallel", or "" when indeterminate.
func (s Step) Form() string {
	switch {
	case s.Uses != "":
		return "verb"
	case s.Workflow != "":
		return "workflow"
	case s.Run != "":
		return "code"
	case s.Type == "agent" || (s.Type == "" && s.Agent != ""):
		return "agent"
	case s.Type == "command" || (s.Type == "" && len(s.Command) > 0):
		return "command"
	case s.Parallel != nil && len(s.Parallel.Branches) > 0:
		return "parallel"
	}
	return ""
}

// ParallelSpec is either `parallel: true` (run for_each iterations
// concurrently) or `parallel: [ [steps…], [steps…] ]` (concurrent branches
// joined before the next step).
type ParallelSpec struct {
	Concurrent bool
	Branches   [][]Step
}

// UnmarshalYAML accepts a bool or a sequence of step lists.
func (p *ParallelSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil {
			return fmt.Errorf("parallel: must be a bool or a list of branches: %w", err)
		}
		p.Concurrent = b
		return nil
	}
	var branches [][]Step
	if err := n.Decode(&branches); err != nil {
		return fmt.Errorf("parallel: must be a bool or a list of branches: %w", err)
	}
	p.Branches = branches
	return nil
}

// MarshalYAML renders the same two shapes back out.
func (p ParallelSpec) MarshalYAML() (any, error) {
	if len(p.Branches) > 0 {
		return p.Branches, nil
	}
	return p.Concurrent, nil
}

// RetrySpec unifies the two retry behaviors a step may declare: re-run on
// error (max/backoff) and re-run while the output still signals "not ready"
// (while_output_matches/interval/timeout — the legacy StepRetry semantics).
type RetrySpec struct {
	Max     int      `yaml:"max,omitempty"`
	Backoff Duration `yaml:"backoff,omitempty"`

	WhileOutputMatches string   `yaml:"while_output_matches,omitempty"`
	Interval           Duration `yaml:"interval,omitempty"`
	Timeout            Duration `yaml:"timeout,omitempty"`
}

// StepRetry converts the defer-retry half to the legacy shape (nil if unused).
func (r *RetrySpec) StepRetry() *StepRetry {
	if r == nil || r.WhileOutputMatches == "" {
		return nil
	}
	return &StepRetry{WhileOutputMatches: r.WhileOutputMatches, Interval: r.Interval, Timeout: r.Timeout}
}

// Hook is one lifecycle action unit: `{ at, uses, options, if, id }`,
// anchored to a workflow (trigger-level `hooks:`) or to a single step
// (step-level `hooks:`).
type Hook struct {
	At      string         `yaml:"at,omitempty"` // start | done | fail
	ID      string         `yaml:"id,omitempty"`
	If      string         `yaml:"if,omitempty"`
	Uses    string         `yaml:"uses,omitempty"`
	Options map[string]any `yaml:"options,omitempty"`
}

// Policy is the cross-cutting control block, valid at three scopes — global,
// connector, trigger — with the most specific setting winning per key.
type Policy struct {
	QuietHours  *QuietHours  `yaml:"quiet_hours,omitempty"`
	Concurrency *Concurrency `yaml:"concurrency,omitempty"`
	// Connector-scoped connection properties (valid globally as defaults too).
	Ignore     *Ignore     `yaml:"ignore,omitempty"`
	RateLimits *RateLimits `yaml:"rate_limits,omitempty"`
	Backoff    *Backoff    `yaml:"backoff,omitempty"`
	// PauseLabel is a github label that parks a target; connector default or a
	// per-trigger hold label.
	PauseLabel *string `yaml:"pause_label,omitempty"`
	// ReplyToBots gates the conversational reply back to a bot author
	// (decline_only | off | full, default decline_only). Substantive work —
	// fixes, thread resolution, labels — always runs; only the reply is gated.
	ReplyToBots *string `yaml:"reply_to_bots,omitempty"`
	// Shadow previews instead of dispatching (legacy control.shadow).
	// There is deliberately no `enabled` here: the global kill switch is the
	// runtime `conductor pause`, and per-connector/per-trigger `enabled:` are
	// their own top-level fields.
	Shadow *bool `yaml:"shadow,omitempty"`
	// MaxAttemptsPerHead is the soft attempt threshold before backoff.
	MaxAttemptsPerHead *int `yaml:"max_attempts_per_head,omitempty"`
}

// reply_to_bots modes: gate the conversational reply to a bot author.
const (
	// ReplyToBotsDeclineOnly (default): the agent is instructed to reply only
	// with a concrete reason for not applying a suggestion — no pleasantries.
	ReplyToBotsDeclineOnly = "decline_only"
	// ReplyToBotsOff: comment/reply verbs back to a bot-authored trigger are
	// skipped structurally by the flow runner.
	ReplyToBotsOff = "off"
	// ReplyToBotsFull: no gating.
	ReplyToBotsFull = "full"
)

// ReplyToBotsMode returns the resolved reply_to_bots mode (default
// decline_only).
func (p Policy) ReplyToBotsMode() string {
	if p.ReplyToBots != nil {
		return *p.ReplyToBots
	}
	return ReplyToBotsDeclineOnly
}

// validatePolicyBlock checks a policy block's enum fields at any scope.
func validatePolicyBlock(where string, p *Policy) error {
	if p == nil || p.ReplyToBots == nil {
		return nil
	}
	switch *p.ReplyToBots {
	case ReplyToBotsDeclineOnly, ReplyToBotsOff, ReplyToBotsFull:
		return nil
	}
	return fmt.Errorf("config: %s: reply_to_bots must be decline_only|off|full, got %q", where, *p.ReplyToBots)
}

// QuietHours defers (hold) or drops work inside a local-time window.
type QuietHours struct {
	TZ   string `yaml:"tz,omitempty"`
	From string `yaml:"from,omitempty"` // "22:00"
	To   string `yaml:"to,omitempty"`   // "07:00"
	// Hold defers work until the window ends (true) or drops it (false).
	// Default true. An override scope may set hold: false to un-quiet.
	Hold *bool `yaml:"hold,omitempty"`
}

// Concurrency bounds total agent load.
type Concurrency struct {
	MaxAgents        *int `yaml:"max_agents,omitempty"`
	MaxAgentsPerHour *int `yaml:"max_agents_per_hour,omitempty"`
}

// Ignore lists authors whose activity never triggers work.
type Ignore struct {
	Users []string `yaml:"users,omitempty"`
}

// RateLimits caps a connector's outbound verb calls.
type RateLimits struct {
	PerMinute int `yaml:"per_minute,omitempty"`
}

// Backoff tunes the retry cadence past the soft attempt threshold.
type Backoff struct {
	Base Duration `yaml:"base,omitempty"`
	Max  Duration `yaml:"max,omitempty"`
}

// MergePolicy overlays scopes most-specific-last: each non-nil field of a
// later (more specific) policy replaces the earlier one's. nil scopes are
// skipped; the result is never nil.
func MergePolicy(scopes ...*Policy) Policy {
	var out Policy
	for _, p := range scopes {
		if p == nil {
			continue
		}
		if p.QuietHours != nil {
			if out.QuietHours == nil {
				q := *p.QuietHours
				out.QuietHours = &q
			} else {
				// Field-level overlay so a trigger can set just `hold: false`.
				q := *out.QuietHours
				if p.QuietHours.TZ != "" {
					q.TZ = p.QuietHours.TZ
				}
				if p.QuietHours.From != "" {
					q.From = p.QuietHours.From
				}
				if p.QuietHours.To != "" {
					q.To = p.QuietHours.To
				}
				if p.QuietHours.Hold != nil {
					q.Hold = p.QuietHours.Hold
				}
				out.QuietHours = &q
			}
		}
		if p.Concurrency != nil {
			if out.Concurrency == nil {
				out.Concurrency = &Concurrency{}
			}
			if p.Concurrency.MaxAgents != nil {
				out.Concurrency.MaxAgents = p.Concurrency.MaxAgents
			}
			if p.Concurrency.MaxAgentsPerHour != nil {
				out.Concurrency.MaxAgentsPerHour = p.Concurrency.MaxAgentsPerHour
			}
		}
		if p.Ignore != nil {
			out.Ignore = p.Ignore
		}
		if p.RateLimits != nil {
			out.RateLimits = p.RateLimits
		}
		if p.Backoff != nil {
			out.Backoff = p.Backoff
		}
		if p.PauseLabel != nil {
			out.PauseLabel = p.PauseLabel
		}
		if p.ReplyToBots != nil {
			out.ReplyToBots = p.ReplyToBots
		}
		if p.Shadow != nil {
			out.Shadow = p.Shadow
		}
		if p.MaxAttemptsPerHead != nil {
			out.MaxAttemptsPerHead = p.MaxAttemptsPerHead
		}
	}
	return out
}

// HasConnectors reports whether the config is (at least partly) on the new
// schema.
func (c *Config) HasConnectors() bool {
	return len(c.ConnectorsMap) > 0 || len(c.Triggers) > 0
}

// validateConnectors structurally checks the new-schema blocks. Semantic
// validation against each connector's published schemas happens in
// internal/connector once instances are built.
func (c *Config) validateConnectors() error {
	if err := validatePolicyBlock("policy", c.Policy); err != nil {
		return err
	}
	for name, ref := range c.ConnectorsMap {
		if name == "" {
			return fmt.Errorf("config: connectors: empty connector name")
		}
		if ref.Type == "" {
			return fmt.Errorf("config: connector %q: missing type", name)
		}
		if err := validatePolicyBlock("connector "+name+" policy", ref.Policy); err != nil {
			return err
		}
	}
	for name, rt := range c.Runtimes {
		if name == "" {
			return fmt.Errorf("config: runtimes: empty runtime name")
		}
		if (rt.Type == "") == (rt.Agent == "") {
			return fmt.Errorf("config: runtime %q: set exactly one of `type` or `agent`", name)
		}
		if err := c.checkRemoteHostSupport("runtime", name, rt.Host, rt.Type, rt.Agent, rt.Controller().EffectiveTransport()); err != nil {
			return err
		}
	}
	if err := c.validateRuntimeDefaults(); err != nil {
		return err
	}
	for name, h := range c.Hosts {
		if name == "" {
			return fmt.Errorf("config: hosts: empty host name")
		}
		if h.Host == "" {
			return fmt.Errorf("config: host %q: missing host address", name)
		}
	}
	for i, t := range c.Triggers {
		where := fmt.Sprintf("triggers[%d]", i)
		if t.On == "" {
			return fmt.Errorf("config: %s: missing `on:`", where)
		}
		conn, event, ok := strings.Cut(t.On, ".")
		if !ok || conn == "" || event == "" {
			return fmt.Errorf("config: %s: `on: %s` must be <connector>.<event>", where, t.On)
		}
		if _, okc := c.ConnectorsMap[conn]; !okc {
			return fmt.Errorf("config: %s: unknown connector %q in `on: %s` (defined: %s)", where, conn, t.On, c.connectorNames())
		}
		if len(t.Steps) == 0 {
			return fmt.Errorf("config: %s (on: %s): no steps", where, t.On)
		}
		if err := validatePolicyBlock(where+" policy", t.Policy); err != nil {
			return err
		}
		if err := validateSteps(where, t.Steps, c); err != nil {
			return err
		}
		if err := validateHooks(where, t.Hooks); err != nil {
			return err
		}
	}
	for name, wf := range c.Workflows {
		if name == "" {
			return fmt.Errorf("config: workflows: empty workflow name")
		}
		where := "workflow " + name
		if len(wf.Steps) == 0 {
			return fmt.Errorf("config: %s: no steps", where)
		}
		for in, spec := range wf.Inputs {
			switch spec.Type {
			case "", "string", "integer", "number", "boolean", "list", "map", "any":
			default:
				return fmt.Errorf("config: %s: input %q: unknown type %q", where, in, spec.Type)
			}
		}
		if err := validateSteps(where, wf.Steps, c); err != nil {
			return err
		}
	}
	return nil
}

// validateRuntimeDefaults enforces at most one default across runtimes and
// legacy controllers combined (they share the registry).
func (c *Config) validateRuntimeDefaults() error {
	defaults := 0
	for _, rt := range c.Runtimes {
		if rt.Default {
			defaults++
		}
	}
	for _, cc := range c.Controllers {
		if cc.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("config: at most one runtime may set `default: true` (%d do)", defaults)
	}
	for name := range c.Runtimes {
		if _, dup := c.Controllers[name]; dup {
			return fmt.Errorf("config: %q is defined under both runtimes: and controllers:", name)
		}
	}
	return nil
}

// validateSteps structurally checks a step list (recursing into parallel
// branches): unique non-empty forms, known hook phases, host references.
func validateSteps(where string, steps []Step, c *Config) error {
	seen := map[string]bool{}
	for i, s := range steps {
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("step%d", i+1)
		}
		w := where + " step " + id
		if seen[id] {
			return fmt.Errorf("config: %s: duplicate step id %q", where, id)
		}
		seen[id] = true
		if err := validateStep(w, s, c); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(w string, s Step, c *Config) error {
	forms := 0
	for _, set := range []bool{
		s.Uses != "", s.Workflow != "", s.Run != "",
		s.Type == "agent" || (s.Type == "" && s.Agent != "" && s.Uses == "" && s.Workflow == ""),
		s.Type == "command" || (s.Type == "" && len(s.Command) > 0),
	} {
		if set {
			forms++
		}
	}
	if s.Parallel != nil && len(s.Parallel.Branches) > 0 {
		if forms > 0 {
			return fmt.Errorf("config: %s: parallel branches cannot be combined with another step form", w)
		}
		for bi, branch := range s.Parallel.Branches {
			if err := validateSteps(fmt.Sprintf("%s branch %d", w, bi+1), branch, c); err != nil {
				return err
			}
		}
		return validateHooks(w, s.Hooks)
	}
	if forms == 0 {
		return fmt.Errorf("config: %s: set one of `type: agent`, `type: command`, `run:`, `uses:`, or `workflow:`", w)
	}
	if forms > 1 {
		return fmt.Errorf("config: %s: step forms are mutually exclusive (set exactly one of type/run/uses/workflow)", w)
	}
	if s.Uses != "" {
		conn, verb, ok := strings.Cut(s.Uses, ".")
		if !ok || conn == "" || verb == "" {
			return fmt.Errorf("config: %s: `uses: %s` must be <connector>.<verb>", w, s.Uses)
		}
	}
	if s.Run != "" && strings.TrimSpace(s.Code) == "" {
		return fmt.Errorf("config: %s: `run: %s` needs `code:`", w, s.Run)
	}
	if s.Host != "" && s.SSH != nil {
		return fmt.Errorf("config: %s: set `host:` or inline `ssh:`, not both", w)
	}
	if s.Host != "" && c != nil {
		if _, ok := c.Hosts[s.Host]; !ok {
			return fmt.Errorf("config: %s: unknown host %q (defined: %s)", w, s.Host, c.hostNames())
		}
	}
	if s.SSH != nil && s.SSH.Host == "" {
		return fmt.Errorf("config: %s: inline ssh: needs `host:` (the address)", w)
	}
	switch s.Run {
	case "js", "go-embed", "risor", "lua":
		if s.Host != "" || s.SSH != nil {
			return fmt.Errorf("config: %s: `run: %s` executes inside conductor's own process and is local-only — use a host interpreter (e.g. `run: node`/`run: sh`) for remote code", w, s.Run)
		}
	}
	if s.Import != "" && s.Workflow == "" {
		return fmt.Errorf("config: %s: a step-level `import:` needs `workflow: <name>` naming the workflow in that file", w)
	}
	if s.Workflow != "" && c != nil {
		if _, ok := c.Workflows[s.Workflow]; !ok {
			return fmt.Errorf("config: %s: unknown workflow %q (defined: %s)", w, s.Workflow, c.workflowNames())
		}
	}
	return validateHooks(w, s.Hooks)
}

// validateHooks checks hook phases and that each hook is a verb action unit.
func validateHooks(where string, hooks []Hook) error {
	for i, h := range hooks {
		w := fmt.Sprintf("%s hooks[%d]", where, i)
		switch h.At {
		case "start", "done", "fail":
		default:
			return fmt.Errorf("config: %s: `at:` must be start|done|fail, got %q", w, h.At)
		}
		if h.Uses == "" {
			return fmt.Errorf("config: %s: hooks are verb action units — set `uses: <connector>.<verb>`", w)
		}
		if conn, verb, ok := strings.Cut(h.Uses, "."); !ok || conn == "" || verb == "" {
			return fmt.Errorf("config: %s: `uses: %s` must be <connector>.<verb>", w, h.Uses)
		}
	}
	return nil
}

func (c *Config) connectorNames() string { return sortedKeys(c.ConnectorsMap) }
func (c *Config) hostNames() string      { return sortedKeys(c.Hosts) }
func (c *Config) workflowNames() string  { return sortedKeys(c.Workflows) }

func sortedKeys[M ~map[string]V, V any](m M) string {
	if len(m) == 0 {
		return "none"
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
