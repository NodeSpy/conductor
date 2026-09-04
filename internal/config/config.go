// Package config loads and validates conductor's YAML configuration.
//
// The top level is integration-agnostic: control/notify/agents/dispatch/store
// plus a list of raw integration entries. Each integration decodes its own
// sub-config (see internal/integrations/*). Action and AgentProfile are shared
// here because both the integration (which maps events→actions) and the
// dispatch package (which executes them) need them.
package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole config file.
type Config struct {
	// Imports lists other YAML files (paths or globs, relative to this file's
	// directory) to merge in, so the config can be split across files. Maps merge
	// recursively, lists (e.g. integrations) concatenate, and this file's own keys
	// win over imported ones. Processed at load time; empty here after loading.
	Imports []string `yaml:"imports"`

	Integrations []IntegrationRef `yaml:"integrations"`

	// ConnectorsMap, Runtimes, Hosts, Workflows, Triggers, Policy, and
	// SecretRefs are the connectors-model schema (see connectors.go). They
	// coexist with the legacy blocks: a config may carry either schema (or,
	// mid-migration, both).
	ConnectorsMap map[string]ConnectorRef  `yaml:"connectors"`
	Runtimes      map[string]RuntimeConfig `yaml:"runtimes"`
	Hosts         map[string]HostConfig    `yaml:"hosts"`
	Workflows     map[string]WorkflowDef   `yaml:"workflows"`
	Triggers      []TriggerSpec            `yaml:"triggers"`
	Policy        *Policy                  `yaml:"policy"`
	// SecretRefs is the named `secrets:` block: name -> secret reference
	// (env:/op://…), readable in templates as {{.secrets.<name>}}.
	SecretRefs map[string]string `yaml:"secrets"`

	Control Control `yaml:"control"`
	Notify  Notify  `yaml:"notify"`
	// Handoff is the LEGACY singular hand-off block (a web-link page on the inbound
	// listener). Deprecated in favor of `handoffs:` (below); still parsed for
	// back-compat and folded into Handoffs["default"] by applyDefaults when
	// `handoffs:` is empty. New configs should use `handoffs:` directly.
	Handoff Handoff `yaml:"handoff"`
	// Handoffs is an OPTIONAL named map of interactive-review hand-off channels
	// (web-link, Slack, Discord) a `background: true` workflow step can present its
	// draft on. Entirely optional: with no `handoffs:` block (and no legacy
	// `handoff:` block), an interactive review stays paseo-native (you drive the
	// agent in paseo), unchanged. See HandoffConfig and internal/handoff.
	Handoffs map[string]HandoffConfig `yaml:"handoffs"`
	Agents   map[string]AgentProfile  `yaml:"agents"`
	// Controllers is an OPTIONAL map of named agent runtimes conductor can
	// dispatch through (paseo, an ACP agent, opencode, …). Entirely optional: with
	// no `controllers:` block, every agent uses the built-in paseo controller and
	// behavior is unchanged. See ControllerConfig and internal/controller.
	Controllers map[string]ControllerConfig `yaml:"controllers"`
	PaseoBin    string                      `yaml:"paseo_bin"` // path to the paseo CLI (default "paseo")
	Store       Store                       `yaml:"store"`
	Update      Update                      `yaml:"update"`
	DryRun      bool                        `yaml:"dry_run"`
	// AdoptOpenWorkspaces routes PR feedback (new_comment/changes_requested) to an
	// agent whose checkout is already on the PR's head branch — e.g. a workspace you
	// opened by hand — instead of spawning a fresh worktree. Opt-in.
	AdoptOpenWorkspaces bool `yaml:"adopt_open_workspaces"`

	// AgentGuidance is free text appended to every dispatched agent prompt (after
	// the identity/write wrapper) — house rules for tone/format, e.g. "keep replies
	// short and human". Unset (nil) → a built-in concise/human-tone default; set to
	// "" → no guidance; set to your own text → replaces the default.
	AgentGuidance *string `yaml:"agent_guidance"`
}

// Update configures periodic self-update checks.
type Update struct {
	Auto     bool     `yaml:"auto"`     // check for and install new releases periodically
	Interval Duration `yaml:"interval"` // how often to check (default 10m; checks are cheap conditional requests)
	Apply    *bool    `yaml:"apply"`    // re-exec into the new binary after updating (default true)
}

// ShouldApply reports whether to re-exec after a successful update (default true).
func (u Update) ShouldApply() bool { return u.Apply == nil || *u.Apply }

// IntegrationRef is one entry in the `integrations:` list. It captures the
// common header and retains the raw node so the concrete integration can decode
// its own fields.
type IntegrationRef struct {
	Type    string    `yaml:"type"`
	Name    string    `yaml:"name"`
	Enabled *bool     `yaml:"enabled"`
	raw     yaml.Node `yaml:"-"`
}

// UnmarshalYAML captures the header fields and the raw node.
func (r *IntegrationRef) UnmarshalYAML(n *yaml.Node) error {
	type hdr struct {
		Type    string `yaml:"type"`
		Name    string `yaml:"name"`
		Enabled *bool  `yaml:"enabled"`
	}
	var h hdr
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Type, r.Name, r.Enabled, r.raw = h.Type, h.Name, h.Enabled, *n
	return nil
}

// Decode unmarshals the raw integration node into a type-specific struct.
func (r IntegrationRef) Decode(v any) error { return r.raw.Decode(v) }

// IsEnabled reports whether the instance is enabled (default true).
func (r IntegrationRef) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Control is the kill switch + shadow settings.
type Control struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PauseLabel string `yaml:"pause_label"` // e.g. "conductor:off"
	Shadow     bool   `yaml:"shadow"`      // global shadow mode
	// MaxConcurrentAgents caps how many conductor coding agents run at once
	// (protects the machine and avoids git-lock contention on a shared repo when
	// a sweep fans out). Absent → default 3; explicit 0 (or negative) → unlimited.
	MaxConcurrentAgents *int `yaml:"max_concurrent_agents"`
	// MaxAgentsPerHour caps agent dispatches in a rolling hour (runaway guard — a
	// webhook flood or sweep misfire can't spin up unbounded agents). 0 = unlimited.
	MaxAgentsPerHour int `yaml:"max_agents_per_hour"`
}

// AgentsPerHour returns the rolling-hour agent-dispatch cap (0 = unlimited).
func (c Control) AgentsPerHour() int { return c.MaxAgentsPerHour }

// IsEnabled reports the master on/off (default true).
func (c Control) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// AgentCap returns the concurrent-agent cap: default 3 when unset, or the
// configured value (<=0 means unlimited).
func (c Control) AgentCap() int {
	if c.MaxConcurrentAgents == nil {
		return 3
	}
	return *c.MaxConcurrentAgents
}

// Handoff is the legacy singular hand-off block. Deprecated — see Config.Handoff.
type Handoff struct {
	Web HandoffWeb `yaml:"web"` // web-link channel served on the inbound HTTP listener
}

// HandoffConfig is one entry in the `handoffs:` named map (mirrors
// ControllerConfig/`controllers:`). Exactly one of Web/Slack/Discord must be set;
// Default flags the entry a step's `handoff:` resolves to when it names none
// explicitly. See internal/handoff.Registry for resolution order.
type HandoffConfig struct {
	// Web configures a web-link draft page served on conductor's inbound HTTP
	// listener. Mutually exclusive with Slack/Discord.
	Web *HandoffWeb `yaml:"web"`
	// Slack configures a Slack DM/thread hand-off (see internal/handoff.SlackChannel).
	// Posting the draft uses BotToken; capturing the reply also needs a `slack:`
	// integration (Socket Mode) configured and running — see the Hand-offs section
	// of README.md.
	Slack *HandoffChat `yaml:"slack"`
	// Discord configures a Discord DM/thread hand-off (see
	// internal/handoff.DiscordChannel). Posting the draft and capturing the
	// reply both go through a Discord bot gateway conductor runs itself (see
	// internal/handoff.RunDiscordGateway) — no separate integration needed, and
	// no tunnel/URL.
	Discord *HandoffChat `yaml:"discord"`
	// Default flags this hand-off as the fleet default, used when a step sets no
	// explicit `handoff:`. At most one entry may set it.
	Default bool `yaml:"default"`
}

// HandoffWeb configures the web-link hand-off channel: a draft page (approve /
// revise / discard + a text box) served on conductor's inbound HTTP listener.
type HandoffWeb struct {
	// BaseURL is the public origin the draft links point at (e.g.
	// https://conductor.example.com). Empty disables the web channel.
	BaseURL string `yaml:"base_url"`
	// Listen is the inbound HTTP address the draft pages are served on (e.g.
	// :8099). Shared with other inbound integrations on the same address; defaults
	// to :8099 when a BaseURL is set but no address is given.
	Listen string `yaml:"listen"`
	// TTL is how long a presented draft's link stays valid before the server-side
	// pending entry expires (default 30m when unset).
	TTL Duration `yaml:"ttl"`
	// Tunnel configures a per-hand-off ephemeral public URL (cloudflared, ngrok,
	// tailscale, ssh, …), opened fresh for each draft instead of using a fixed
	// BaseURL. Unset (or provider: static/"") keeps today's BaseURL-as-is
	// behavior.
	Tunnel TunnelConfig `yaml:"tunnel"`
}

// TunnelConfig is the schema for a pluggable tunnel that gives the web hand-off
// channel a fresh public URL per draft, instead of a persistent `base_url` you
// host yourself. See handoff.NewTunnel for the provider implementations
// (lan/static/cloudflared/ngrok/tailscale/ssh/localxpose/command).
type TunnelConfig struct {
	Provider   string   `yaml:"provider"`
	Host       string   `yaml:"host"`
	Mode       string   `yaml:"mode"`
	SSHHost    string   `yaml:"ssh_host"`
	Authtoken  string   `yaml:"authtoken"`
	URLPattern string   `yaml:"url_pattern"`
	Command    []string `yaml:"command"`
	Account    bool     `yaml:"account"`
}

// HandoffChat is the schema for a chat-based (Slack/Discord) hand-off channel:
// post the draft to a DM or a thread, capture the reply. Shared by
// HandoffConfig.Slack and HandoffConfig.Discord.
type HandoffChat struct {
	To      string `yaml:"to"`      // dm | thread
	Channel string `yaml:"channel"` // to: thread — channel to post in (required)
	// User is the target user id for a `to: dm` channel (a Slack user id, e.g.
	// U0123ABCD, or a Discord user id — required for dm). There is no
	// GitHub->Slack/Discord identity mapping, so this is never
	// inferred/defaulted; you must look up the id yourself (Slack profile ->
	// "Copy member ID"; Discord: enable Developer Mode, right-click the user ->
	// "Copy User ID").
	User string `yaml:"user"`
	// BotToken authenticates posting (and, for Discord, the gateway connection
	// that captures replies). Slack: a bot token, xoxb-… (chat:write, +im:write
	// for to: dm). Discord: a bot token from the Discord developer portal; the
	// bot needs the privileged MESSAGE CONTENT intent enabled and must be
	// invited to the server/channel (or share a DM with `user`).
	BotToken string `yaml:"bot_token"`
}

// Notify configures notifications. All channels are private to you (the daemon
// journal today; a push endpoint later) — the conductor never comments on PRs.
type Notify struct {
	Push              bool            `yaml:"push"`
	On                []string        `yaml:"on"`                  // subset of: dispatch, complete, escalate, needs_input
	SlackWebhookURL   string          `yaml:"slack_webhook_url"`   // optional Slack incoming-webhook URL to post enabled events to
	DiscordWebhookURL string          `yaml:"discord_webhook_url"` // optional Discord incoming-webhook URL to post enabled events to
	Ntfy              NotifyNtfy      `yaml:"ntfy"`                // optional ntfy.sh (or self-hosted) topic to publish to
	Pushover          NotifyPushover  `yaml:"pushover"`            // optional Pushover application/user to notify
	Notifiarr         NotifyNotifiarr `yaml:"notifiarr"`           // optional Notifiarr passthrough integration
	Digest            Duration        `yaml:"digest"`              // periodic activity summary (e.g. 24h); 0 = off
}

// NotifyNtfy configures publishing to an ntfy (https://ntfy.sh or self-hosted)
// topic. Server defaults to https://ntfy.sh when unset.
type NotifyNtfy struct {
	Server string `yaml:"server"`
	Topic  string `yaml:"topic"`
}

// NotifyPushover configures posting to the Pushover message API.
type NotifyPushover struct {
	Token string `yaml:"token"` // application token
	User  string `yaml:"user"`  // user/group key
}

// NotifyNotifiarr configures posting to a Notifiarr passthrough integration,
// which relays to Discord on Notifiarr's side.
type NotifyNotifiarr struct {
	APIKey    string `yaml:"api_key"`
	ChannelID string `yaml:"channel_id"` // optional: Discord channel ID override
}

// Wants reports whether the given notify event is enabled.
func (n Notify) Wants(event string) bool {
	for _, e := range n.On {
		if e == event {
			return true
		}
	}
	return false
}

// Retry controls re-attempts of a `paseo run` that fails with a transient error
// (a git lock/timeout while creating the worktree — common under a sweep
// fan-out). Only transient failures are retried; real errors surface at once.
type Retry struct {
	Max     int      `yaml:"max"`     // extra attempts (default 3); <=0 disables
	Backoff Duration `yaml:"backoff"` // delay between attempts (default 10s)
}

// Attempts returns the configured retry count, defaulting to 3 when unset.
func (r Retry) Attempts() int {
	if r.Max == 0 {
		return 3
	}
	if r.Max < 0 {
		return 0
	}
	return r.Max
}

// BackoffDur returns the configured backoff, defaulting to 10s when unset.
func (r Retry) BackoffDur() time.Duration {
	if d := r.Backoff.D(); d > 0 {
		return d
	}
	return 10 * time.Second
}

// Store configures the dedup state + audit log.
type Store struct {
	StateFile     string   `yaml:"state_file"`
	AuditLog      string   `yaml:"audit_log"`
	StateTTL      Duration `yaml:"state_ttl"`
	MaxTrackedPRs int      `yaml:"max_tracked_prs"`
	AuditMaxSize  ByteSize `yaml:"audit_max_size"`
}

// ControllerConfig is one entry in the optional top-level `controllers:` block —
// a named agent runtime conductor can dispatch through. All fields are optional;
// the block itself is optional. With no `controllers:` block every agent uses the
// built-in paseo controller (no migration, no behavior change).
type ControllerConfig struct {
	// Type is a built-in controller kind (M1 ships "paseo"). Mutually exclusive
	// with Agent. Reserved kinds parse and validate but aren't runnable until their
	// milestone lands.
	Type string `yaml:"type"`
	// Agent names an agent runtime driven over a transport (gemini, opencode, …).
	// Mutually exclusive with Type; implies transport acp unless overridden.
	Agent string `yaml:"agent"`
	// Transport is how conductor talks to the runtime: acp | native | cli. Empty
	// defaults to acp for an agent runtime, else native (see EffectiveTransport).
	// Ergonomic only — it never changes the global default controller.
	Transport string `yaml:"transport"`
	// SessionModel hints how the runtime keeps a session across turns: native |
	// resumable | oneshot. Optional; the controller usually reports its own.
	SessionModel string `yaml:"session_model"`
	// Default flags this controller as the fleet default, used when an agent sets
	// no explicit `controller:`. At most one controller may set it.
	Default bool `yaml:"default"`
	// Tool and Command are a bare-CLI recipe for a non-ACP tool (transport: cli).
	// Reserved for the cli-controller milestone.
	Tool    string   `yaml:"tool"`
	Command []string `yaml:"command"`
	// Bin is the runtime binary (paseo/agent-deck); for agent-deck it wins over
	// the command/tool/"agent-deck" bin resolution. For paseo, exactly one
	// distinct Bin may be set across all paseo runtimes/controllers combined
	// (see cmd/paseo-conductor's resolvePaseoBin).
	Bin string `yaml:"bin"`
	// Host names a `hosts:` entry; this controller's subprocess launches run
	// there over SSH instead of locally. Only cli/acp/agent-deck controllers
	// support it — see checkRemoteHostSupport.
	Host string `yaml:"host"`
}

// EffectiveTransport returns the controller's transport, defaulting to acp for an
// agent runtime and native for a built-in type when unset. Ergonomic only — it
// never changes the global default controller (that's the `default:` flag).
func (c ControllerConfig) EffectiveTransport() string {
	if c.Transport != "" {
		return c.Transport
	}
	if c.Agent != "" {
		return "acp"
	}
	return "native"
}

// DefaultControllerName returns the name of the controller flagged default:true,
// or "" when none is (resolution then falls back to the built-in paseo).
func (c *Config) DefaultControllerName() string {
	for name, cc := range c.Controllers {
		if cc.Default {
			return name
		}
	}
	return ""
}

// MergedControllers unions the connectors-model `runtimes:` block into the
// legacy `controllers:` shape the controller registry consumes (each
// RuntimeConfig converted via its Controller() method). Both schemas name the
// same registry, and validateRuntimeDefaults already rejects a name defined
// under both — so this is a plain union, no precedence to resolve.
func (c *Config) MergedControllers() map[string]ControllerConfig {
	merged := make(map[string]ControllerConfig, len(c.Controllers)+len(c.Runtimes))
	for name, cc := range c.Controllers {
		merged[name] = cc
	}
	for name, rt := range c.Runtimes {
		merged[name] = rt.Controller()
	}
	return merged
}

// DefaultRuntimeName returns the name of the runtime or controller flagged
// default:true (runtimes: checked first), or "" when none is (resolution
// then falls back to the built-in paseo). At most one across both maps may
// set it — see validateRuntimeDefaults.
func (c *Config) DefaultRuntimeName() string {
	for name, rt := range c.Runtimes {
		if rt.Default {
			return name
		}
	}
	return c.DefaultControllerName()
}

// DefaultHandoffName returns the name of the `handoffs:` entry flagged
// default:true, or "" when none is (resolution then falls back to the sole
// configured entry, or no hand-off channel at all — see internal/handoff.Registry).
func (c *Config) DefaultHandoffName() string {
	for name, hc := range c.Handoffs {
		if hc.Default {
			return name
		}
	}
	return ""
}

// AgentProfile is a reusable named agent config referenced by agent actions.
type AgentProfile struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Thinking string `yaml:"thinking"`
	Mode     string `yaml:"mode"`
	// Controller names the controller (from top-level `controllers:`) that runs
	// this agent. Empty falls through to the controller flagged default:true, then
	// to the built-in paseo controller. See internal/controller resolution order.
	Controller string `yaml:"controller"`
	// Runtime is the connectors-model name for Controller (a `runtimes:` entry).
	// When both are set, Runtime wins; use RuntimeName.
	Runtime string `yaml:"runtime"`
	// Host pins this agent's runtime invocations to a named `hosts:` SSH
	// target, overriding (or standing in for) the runtime's own `host:`.
	Host            string            `yaml:"host"`
	Workspace       string            `yaml:"workspace"` // local | worktree
	WaitTimeout     Duration          `yaml:"wait_timeout"`
	ArchiveWhenDone bool              `yaml:"archive_when_done"`
	Labels          map[string]string `yaml:"labels"`
	// Guidance overrides the top-level agent_guidance for THIS agent (house tone/
	// format rules appended to its prompt). Unset (nil) → fall through to the
	// top-level agent_guidance (then the built-in default); "" → none; text → that.
	Guidance *string `yaml:"guidance"`
}

// RuntimeName returns the runtime/controller the profile selects (runtime
// wins over the legacy controller key; "" = the default).
func (p AgentProfile) RuntimeName() string {
	if p.Runtime != "" {
		return p.Runtime
	}
	return p.Controller
}

// Action is one (source,kind)→action mapping. Type is "agent" or "command".
type Action struct {
	Name     string            `yaml:"name"` // variant name when a kind has multiple actions; "" = the sole/unnamed action
	Type     string            `yaml:"type"`
	Enabled  *bool             `yaml:"enabled"` // default true
	Backend  string            `yaml:"backend"` // override default backend for the type
	Shadow   *bool             `yaml:"shadow"`  // per-action shadow override
	Checkout string            `yaml:"checkout"`
	WorkDir  string            `yaml:"workdir"` // working directory (command: cwd; agent: paseo --cwd)
	Env      map[string]string `yaml:"env"`

	// agent-type fields
	Agent  string `yaml:"agent"` // agent profile name
	Prompt string `yaml:"prompt"`
	// RerequestReview: after the agent addresses feedback and pushes, re-request
	// review from the reviewer(s) who requested changes (closes the review loop).
	RerequestReview bool `yaml:"rerequest_review"`
	// Exclude skips PRs matching these criteria (e.g. release PRs).
	Exclude Exclude `yaml:"exclude"`

	// command-type fields
	Command []string `yaml:"command"`
	// Retry re-runs this step while its output signals it isn't ready yet (e.g.
	// critique deferring on pending CI) — so the workflow doesn't complete a step
	// that isn't actually done. Mainly for command steps.
	Retry *StepRetry `yaml:"retry"`

	// workflow (multi-step) fields. When Steps is non-empty the action runs as
	// an ordered workflow; each step is itself an Action plus ID/If/OutputSchema.
	Steps        []Action       `yaml:"steps"`
	ID           string         `yaml:"id"`            // step id (for steps.<id>.outputs.*)
	If           string         `yaml:"if"`            // step condition (see internal/expr)
	OutputSchema map[string]any `yaml:"output_schema"` // agent step: JSON schema for structured output
	Background   bool           `yaml:"background"`    // workflow step: dispatch `paseo run --background` and don't
	//                                                    wait/capture — launch a live agent to drive interactively
	// Handoff names a `handoffs:` entry (see Config.Handoffs) a background step
	// presents its interactive review draft on. Empty resolves to the entry
	// flagged default:true, then the sole configured entry, then no hand-off
	// channel (paseo-native). Only meaningful on a background step.
	Handoff string `yaml:"handoff"`

	// gating actors (live on the check they gate)
	Reviewer Actors `yaml:"reviewer"` // review_requested: whose requested review triggers it
	Assignee Actors `yaml:"assignee"` // issue_assigned: whose assignment triggers it

	// FlowRef ties a lowered connectors-model trigger back to its
	// config.Triggers spec ("<index>:<on>"). Set programmatically by the
	// lowering in internal/connector — never from user YAML — and carried
	// through JSON persistence so an in-flight run resumes onto its spec.
	// The x_-prefixed YAML names are internal: the lowering round-trips these
	// structs through YAML, so they need tags, but they are not part of the
	// user-facing schema.
	FlowRef string `yaml:"x_flow_ref,omitempty" json:"FlowRef,omitempty"`
	// Repos / ExcludeRepos are per-variant repo gates (globs), also set by the
	// connectors-model lowering: a trigger's `filters.repos` becomes a variant
	// that only fires for matching repos. Legacy configs use rule-level
	// `match.repos` instead and never set these.
	Repos        []string `yaml:"x_repos,omitempty" json:"Repos,omitempty"`
	ExcludeRepos []string `yaml:"x_exclude_repos,omitempty" json:"ExcludeRepos,omitempty"`
	// TargetRepo pins the trigger's checkout repo per variant (a lowered
	// trigger's repo: on sentry/pagerduty, whose legacy Repo was rule-level).
	TargetRepo string `yaml:"x_target_repo,omitempty" json:"TargetRepo,omitempty"`

	// kind-specific options
	MaxAttemptsPerHead int            `yaml:"max_attempts_per_head"`
	IgnoreChecks       []string       `yaml:"ignore_checks"`
	FlakyRerun         FlakyRerun     `yaml:"flaky_rerun"`
	StuckAfter         Duration       `yaml:"stuck_after"`   // stuck_checks: a check running longer than this is "stuck" (default 30m)
	PollInterval       Duration       `yaml:"poll_interval"` // stuck_checks: how often the dedicated poller checks (default 15m)
	FromUsers          []string       `yaml:"from_users"`    // new_comment: only these commenters trigger (empty = any)
	IgnoreUsers        []string       `yaml:"ignore_users"`  // new_comment: never trigger on these commenters (e.g. CI report bots)
	LabelsAny          []string       `yaml:"labels_any"`    // issue matches if it has ANY of these labels
	LabelsAll          []string       `yaml:"labels_all"`    // ...and ALL of these labels
	Authors            []string       `yaml:"authors"`       // ...and was opened by one of these logins
	SoleAssignee       bool           `yaml:"sole_assignee"` // ...and you are the ONLY assignee
	RequireLabel       string         `yaml:"require_label"`
	IncludePrereleases bool           `yaml:"include_prereleases"` // release: also fire on prereleases (default: skip them)
	Method             string         `yaml:"method"`
	Gates              map[string]any `yaml:"gates"`
	Project            map[string]any `yaml:"project"`
}

// IsEnabled reports whether the action is enabled (default true).
func (a Action) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

// StuckAfterDur returns the stuck-check threshold, defaulting to 30m.
func (a Action) StuckAfterDur() time.Duration {
	if d := a.StuckAfter.D(); d > 0 {
		return d
	}
	return 30 * time.Minute
}

// PollIntervalDur returns the stuck_checks poll cadence, defaulting to 15m.
func (a Action) PollIntervalDur() time.Duration {
	if d := a.PollInterval.D(); d > 0 {
		return d
	}
	return 15 * time.Minute
}

// StepRetry re-runs a workflow step while its output still matches WhileOutputMatches
// (a regexp) — the "not ready yet" signal, e.g. critique's "status: retry" when it's
// waiting on CI. The step re-runs every Interval (default 1m) until the output stops
// matching or Timeout (default 15m) elapses, at which point conductor gives up on the
// retry (the sweep remains the backstop). Only applies to non-background steps.
type StepRetry struct {
	WhileOutputMatches string   `yaml:"while_output_matches"`
	Interval           Duration `yaml:"interval"`
	Timeout            Duration `yaml:"timeout"`
}

// RetryInterval returns the poll interval, defaulting to 1m.
func (r StepRetry) RetryInterval() time.Duration {
	if d := r.Interval.D(); d > 0 {
		return d
	}
	return time.Minute
}

// RetryTimeout returns the give-up budget, defaulting to 15m.
func (r StepRetry) RetryTimeout() time.Duration {
	if d := r.Timeout.D(); d > 0 {
		return d
	}
	return 15 * time.Minute
}

// ActionSet is one-or-more actions for a kind. In YAML it accepts either a single
// mapping (`merge_conflict: { agent: opus }` → one unnamed action, the common case)
// or a sequence of named variants (`issue_matched: [ {name: a, …}, {name: b, …} ]`).
// This keeps every existing single-object config valid while enabling variants.
type ActionSet []Action

// UnmarshalYAML accepts a single mapping or a sequence of actions.
func (s *ActionSet) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var list []Action
		if err := node.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	}
	var one Action
	if err := node.Decode(&one); err != nil {
		return err
	}
	*s = ActionSet{one}
	return nil
}

// Refs labels each action in the set with where it lives, for CheckAgentRefs.
// Named variants get a `[name]` suffix so a bad reference in one variant of a
// kind is distinguishable from its siblings.
func (s ActionSet) Refs(where string) []ActionRef {
	refs := make([]ActionRef, 0, len(s))
	for _, a := range s {
		w := where
		if a.Name != "" {
			w += "[" + a.Name + "]"
		}
		refs = append(refs, ActionRef{Where: w, Action: a})
	}
	return refs
}

// Exclude filters out PRs an action shouldn't act on (e.g. release PRs), by head
// branch glob, label, or a case-insensitive title substring.
type Exclude struct {
	Branches []string `yaml:"branches"` // head-branch globs, e.g. "release/*"
	Labels   []string `yaml:"labels"`   // PR labels (case-insensitive)
	Title    []string `yaml:"title"`    // case-insensitive substrings of the PR title
}

// Empty reports whether no exclusion is configured.
func (e Exclude) Empty() bool {
	return len(e.Branches) == 0 && len(e.Labels) == 0 && len(e.Title) == 0
}

// Matches reports whether a PR (head branch, title, labels) hits any exclusion.
func (e Exclude) Matches(branch, title string, labels []string) bool {
	for _, p := range e.Branches {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	for _, want := range e.Labels {
		for _, l := range labels {
			if strings.EqualFold(want, l) {
				return true
			}
		}
	}
	lt := strings.ToLower(title)
	for _, s := range e.Title {
		if s != "" && strings.Contains(lt, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// FlakyRerun controls one-shot re-runs of failed checks before dispatching.
type FlakyRerun struct {
	Enabled bool `yaml:"enabled"`
	Max     int  `yaml:"max"`
}

// Actors is a set of GitHub logins/teams (used for reviewer/assignee matching).
type Actors struct {
	Logins []string `yaml:"logins"`
	Teams  []string `yaml:"teams"`
}

// HasLogin reports whether login is in the set (case-insensitive).
func (a Actors) HasLogin(login string) bool {
	for _, l := range a.Logins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// Load reads, expands, defaults, and validates the config at path. If the file
// declares `imports:`, the listed files are deep-merged first (see loadMerged);
// otherwise the file is parsed directly, exactly as before.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded, err := expandEnv(path, raw)
	if err != nil {
		return nil, err
	}

	// Cheap probe: no `imports:` → parse the single file directly (unchanged path,
	// no map round-trip). Only pay the merge machinery when imports are used.
	var probe struct {
		Imports []string `yaml:"imports"`
	}
	if err := yaml.Unmarshal(expanded, &probe); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	var c Config
	if len(probe.Imports) == 0 {
		if err := yaml.Unmarshal(expanded, &c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	} else {
		merged, err := loadMerged(path, map[string]bool{})
		if err != nil {
			return nil, err
		}
		out, err := yaml.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merge imports: %w", err)
		}
		// Re-parse the merged document so the custom unmarshalers (IntegrationRef,
		// ActionSet, Duration, …) still run over each node.
		if err := yaml.Unmarshal(out, &c); err != nil {
			return nil, fmt.Errorf("parse merged config: %w", err)
		}
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// loadMerged reads the file at path (env-expanded) as a generic map, then
// deep-merges any files it lists under `imports:` — resolved relative to this
// file's directory, globs allowed. Imported files are merged in listed order and
// the importing file's own keys overlay them, so: scalars/maps in the importer
// win, lists concatenate (imported entries first), and each file is included at
// most once (cycles and diamond imports are de-duped, not errors).
func loadMerged(p string, loaded map[string]bool) (map[string]any, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	if loaded[abs] {
		return map[string]any{}, nil // already included elsewhere
	}
	loaded[abs] = true

	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", p, err)
	}
	expanded, err := expandEnv(p, raw)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(expanded, &m); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", p, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	imports := toStrings(m["imports"])
	delete(m, "imports")

	merged := map[string]any{}
	dir := filepath.Dir(p)
	for _, imp := range imports {
		pat := imp
		if !filepath.IsAbs(pat) {
			pat = filepath.Join(dir, pat)
		}
		matches, err := filepath.Glob(pat)
		if err != nil {
			return nil, fmt.Errorf("%s: bad import glob %q: %w", p, imp, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: import %q matched no files", p, imp)
		}
		sort.Strings(matches)
		for _, f := range matches {
			sub, err := loadMerged(f, loaded)
			if err != nil {
				return nil, err
			}
			merged = mergeMaps(merged, sub)
		}
	}
	return mergeMaps(merged, m), nil
}

// toStrings coerces a YAML scalar or sequence of strings into []string.
func toStrings(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}

// mergeMaps deep-merges src into dst: nested maps merge recursively, lists
// concatenate (dst's entries first), and any other value in src overwrites dst.
func mergeMaps(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, sv := range src {
		if dv, ok := dst[k]; ok {
			if dm, ok1 := dv.(map[string]any); ok1 {
				if sm, ok2 := sv.(map[string]any); ok2 {
					dst[k] = mergeMaps(dm, sm)
					continue
				}
			}
			if dl, ok1 := dv.([]any); ok1 {
				if sl, ok2 := sv.([]any); ok2 {
					dst[k] = append(append([]any{}, dl...), sl...)
					continue
				}
			}
		}
		dst[k] = sv
	}
	return dst
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR} (brace form only) with the environment value, so a
// bare "$" in prompts is left untouched. Referencing a variable that is not set
// at all is an error — silently expanding it to "" would turn a missing
// conductor.env into a confusing downstream failure (e.g. "webhook_secret
// required") instead of naming the variable. A variable that is set but empty
// (KEY= in conductor.env) is deliberate and expands to "".
//
// Expansion runs per line on the code portion only: a ${VAR} inside a YAML
// comment is left verbatim and never reported as missing, so the example config's
// explanatory comments (which mention ${ENV}/${GH_PAT}) don't fail to load.
func expandEnv(path string, b []byte) ([]byte, error) {
	var missing []string
	seen := map[string]bool{}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		code, comment := splitYAMLComment(line)
		code = envRe.ReplaceAllStringFunc(code, func(m string) string {
			name := envRe.FindStringSubmatch(m)[1]
			v, ok := os.LookupEnv(name)
			if !ok && !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			return v
		})
		lines[i] = code + comment
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config %s references undefined environment variable(s): %s (define them in %s or the environment)",
			path, strings.Join(missing, ", "), filepath.Join(filepath.Dir(path), "conductor.env"))
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// splitYAMLComment splits a line into its code and trailing `#…` comment. A `#`
// starts a comment only at line start or when preceded by whitespace and not
// inside a quoted scalar — so `${VAR}` in an explanatory comment is ignored,
// while a value like `{{.repo}}#{{.pr}}` (its `#` not whitespace-preceded) and a
// quoted `"a # b"` stay code.
func splitYAMLComment(line string) (code, comment string) {
	var inS, inD bool
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i], line[i:]
		}
	}
	return line, ""
}

func (c *Config) applyDefaults() {
	if c.PaseoBin == "" {
		c.PaseoBin = "paseo"
	}
	if c.Store.StateFile == "" {
		c.Store.StateFile = filepath.Join(StateDir(), "state.json")
	}
	if c.Store.AuditLog == "" {
		c.Store.AuditLog = filepath.Join(StateDir(), "audit.jsonl")
	}
	c.Store.StateFile = expandHome(c.Store.StateFile)
	c.Store.AuditLog = expandHome(c.Store.AuditLog)
	if c.Store.StateTTL == 0 {
		c.Store.StateTTL = Duration(30 * 24 * time.Hour)
	}
	if c.Store.MaxTrackedPRs == 0 {
		c.Store.MaxTrackedPRs = 5000
	}
	if c.Store.AuditMaxSize == 0 {
		c.Store.AuditMaxSize = 50 * 1024 * 1024
	}
	if len(c.Notify.On) == 0 {
		c.Notify.On = []string{"escalate"}
	}
	if c.Update.Auto && c.Update.Interval == 0 {
		c.Update.Interval = Duration(10 * time.Minute)
	}
	c.applyHandoffCompat()
}

// applyHandoffCompat folds the LEGACY singular `handoff: { web: … }` block into
// `handoffs: { default: { web: …, default: true } }` when the new named map is
// empty, so a config still on the old field keeps loading and resolving exactly
// as before (a step naming no explicit `handoff:` gets this synthesized default
// entry). A no-op once `handoffs:` is set — the new map always wins. `handoff:`
// shipped with no known users on it yet, but the shim is cheap to keep.
func (c *Config) applyHandoffCompat() {
	if len(c.Handoffs) > 0 {
		return
	}
	if c.Handoff.Web.BaseURL == "" && c.Handoff.Web.Listen == "" {
		return
	}
	web := c.Handoff.Web
	c.Handoffs = map[string]HandoffConfig{
		"default": {Web: &web, Default: true},
	}
}

// Validate checks required fields and cross-field consistency.
func (c *Config) Validate() error {
	if len(c.Integrations) == 0 && !c.HasConnectors() {
		return fmt.Errorf("config: no integrations or connectors configured")
	}
	if err := c.validateConnectors(); err != nil {
		return err
	}
	names := map[string]bool{}
	for i, ig := range c.Integrations {
		if ig.Type == "" {
			return fmt.Errorf("config: integrations[%d]: missing type", i)
		}
		if ig.Name == "" {
			return fmt.Errorf("config: integrations[%d]: missing name", i)
		}
		if names[ig.Name] {
			return fmt.Errorf("config: duplicate integration name %q", ig.Name)
		}
		names[ig.Name] = true
	}
	if err := c.validateControllers(); err != nil {
		return err
	}
	if err := c.validateHandoffs(); err != nil {
		return err
	}
	for name, p := range c.Agents {
		if p.Workspace != "" && p.Workspace != "local" && p.Workspace != "worktree" {
			return fmt.Errorf("config: agent %q: workspace must be local|worktree, got %q", name, p.Workspace)
		}
		if rn := p.RuntimeName(); rn != "" {
			_, isController := c.Controllers[rn]
			_, isRuntime := c.Runtimes[rn]
			if !isController && !isRuntime {
				return fmt.Errorf("config: agent %q: unknown runtime %q (defined: %s)", name, rn, c.runtimeNames())
			}
		}
		if p.Host != "" {
			if _, ok := c.Hosts[p.Host]; !ok {
				return fmt.Errorf("config: agent %q: unknown host %q (defined: %s)", name, p.Host, c.hostNames())
			}
		}
	}
	return nil
}

// runtimeNames lists runtimes and legacy controllers, sorted, for errors.
func (c *Config) runtimeNames() string {
	names := make([]string, 0, len(c.Runtimes)+len(c.Controllers))
	for n := range c.Runtimes {
		names = append(names, n)
	}
	for n := range c.Controllers {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validateControllers checks the optional `controllers:` block: each entry sets
// exactly one of type/agent, transport and session_model (when set) are known
// values, and at most one controller is flagged default:true.
func (c *Config) validateControllers() error {
	validTransport := map[string]bool{"acp": true, "native": true, "cli": true}
	validModel := map[string]bool{"native": true, "resumable": true, "oneshot": true}
	defaults := 0
	for name, cc := range c.Controllers {
		if name == "" {
			return fmt.Errorf("config: controllers: empty controller name")
		}
		if (cc.Type == "") == (cc.Agent == "") {
			return fmt.Errorf("config: controller %q: set exactly one of `type` or `agent`", name)
		}
		if cc.Transport != "" && !validTransport[cc.Transport] {
			return fmt.Errorf("config: controller %q: transport must be acp|native|cli, got %q", name, cc.Transport)
		}
		if cc.SessionModel != "" && !validModel[cc.SessionModel] {
			return fmt.Errorf("config: controller %q: session_model must be native|resumable|oneshot, got %q", name, cc.SessionModel)
		}
		if err := c.checkRemoteHostSupport("controller", name, cc.Host, cc.Type, cc.Agent, cc.EffectiveTransport()); err != nil {
			return err
		}
		if cc.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("config: at most one controller may set `default: true` (%d do)", defaults)
	}
	return nil
}

// checkRemoteHostSupport validates a runtime/controller's `host:` reference:
// it must name a defined `hosts:` entry, and only cli/acp/agent-deck
// runtimes support running remotely — paseo checkouts are local, and
// opencode's native transport binds an HTTP server to 127.0.0.1, so
// ssh-wrapping either would be meaningless (paseo) or unreachable (opencode).
// kind is "runtime" or "controller" (for the error text); typ/agent/transport
// are the entry's own fields — transport must be the *effective* transport
// (e.g. via ControllerConfig.EffectiveTransport(), or
// RuntimeConfig.Controller().EffectiveTransport()) so an unset transport on
// an opencode agent entry still resolves to its acp/native default correctly.
func (c *Config) checkRemoteHostSupport(kind, name, host, typ, agent, transport string) error {
	if host == "" {
		return nil
	}
	if _, ok := c.Hosts[host]; !ok {
		return fmt.Errorf("config: %s %q: unknown host %q (defined: %s)", kind, name, host, c.hostNames())
	}
	return nil
}

// controllerNames lists the defined controller names, sorted, for error messages.
func (c *Config) controllerNames() string {
	if len(c.Controllers) == 0 {
		return "none"
	}
	names := make([]string, 0, len(c.Controllers))
	for n := range c.Controllers {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validTunnelProviders are the recognized `handoffs.*.web.tunnel.provider`
// values (empty == "static": no process, base_url used as-is). See
// internal/handoff/tunnel.go for what each spawns.
var validTunnelProviders = map[string]bool{
	"":            true,
	"static":      true,
	"lan":         true,
	"cloudflared": true,
	"ngrok":       true,
	"tailscale":   true,
	"ssh":         true,
	"localxpose":  true,
	"command":     true,
}

// validateHandoffs checks the optional `handoffs:` block: each entry sets
// exactly one channel sub-block (web/slack/discord), and at most one entry is
// flagged default:true. A `web` entry's `tunnel:` block (if any) is checked for
// a known provider, a sane `mode` for tailscale, a non-empty `command:` and
// compilable `url_pattern:` for the `command` provider, and a non-empty
// `ssh_host` for the `ssh` provider — so a typo/missing field is a config-load
// error, not a failure the first time a step tries to present a draft. (A
// workflow step's `handoff:` reference is checked alongside its `agent:`
// reference — see CheckAgentRefs/checkAgentRef, which runs after the
// step-owning integrations are built.)
func (c *Config) validateHandoffs() error {
	defaults := 0
	for name, hc := range c.Handoffs {
		if name == "" {
			return fmt.Errorf("config: handoffs: empty handoff name")
		}
		set := 0
		if hc.Web != nil {
			set++
			if err := validateTunnel(name, hc.Web.Tunnel); err != nil {
				return err
			}
		}
		if hc.Slack != nil {
			set++
			if err := validateSlackChat(name, hc.Slack); err != nil {
				return err
			}
		}
		if hc.Discord != nil {
			set++
			if err := validateDiscordChat(name, hc.Discord); err != nil {
				return err
			}
		}
		if set != 1 {
			return fmt.Errorf("config: handoff %q: set exactly one of `web`, `slack`, or `discord` (got %d)", name, set)
		}
		if hc.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("config: at most one handoff may set `default: true` (%d do)", defaults)
	}
	return nil
}

// validateSlackChat checks one `handoffs.<name>.slack:` block: `to` must be
// dm|thread, thread requires channel, dm requires user, and bot_token is always
// required (there's no way to post without it). Checked at config-load time so
// a missing field is a startup error, not a failure the first time a step tries
// to present a draft.
func validateSlackChat(handoffName string, hc *HandoffChat) error {
	switch hc.To {
	case "dm", "thread":
	default:
		return fmt.Errorf("config: handoff %q: slack.to must be dm|thread, got %q", handoffName, hc.To)
	}
	if hc.To == "thread" && hc.Channel == "" {
		return fmt.Errorf("config: handoff %q: slack.to: thread requires channel", handoffName)
	}
	if hc.To == "dm" && hc.User == "" {
		return fmt.Errorf("config: handoff %q: slack.to: dm requires user (a Slack user id, e.g. U0123ABCD)", handoffName)
	}
	if hc.BotToken == "" {
		return fmt.Errorf("config: handoff %q: slack.bot_token is required", handoffName)
	}
	return nil
}

// validateDiscordChat checks one `handoffs.<name>.discord:` block: `to` must
// be dm|thread, thread requires channel, dm requires user, and bot_token is
// always required. Mirrors validateSlackChat.
func validateDiscordChat(handoffName string, hc *HandoffChat) error {
	switch hc.To {
	case "dm", "thread":
	default:
		return fmt.Errorf("config: handoff %q: discord.to must be dm|thread, got %q", handoffName, hc.To)
	}
	if hc.To == "thread" && hc.Channel == "" {
		return fmt.Errorf("config: handoff %q: discord.to: thread requires channel", handoffName)
	}
	if hc.To == "dm" && hc.User == "" {
		return fmt.Errorf("config: handoff %q: discord.to: dm requires user (a Discord user id)", handoffName)
	}
	if hc.BotToken == "" {
		return fmt.Errorf("config: handoff %q: discord.bot_token is required", handoffName)
	}
	return nil
}

// validateTunnel checks one `handoffs.<name>.web.tunnel:` block. An empty
// Provider ("") is valid — it means "static" (no process, base_url used as-is).
func validateTunnel(handoffName string, t TunnelConfig) error {
	if !validTunnelProviders[t.Provider] {
		names := make([]string, 0, len(validTunnelProviders))
		for p := range validTunnelProviders {
			if p != "" {
				names = append(names, p)
			}
		}
		sort.Strings(names)
		return fmt.Errorf("config: handoff %q: tunnel provider must be one of %s, got %q", handoffName, strings.Join(names, "|"), t.Provider)
	}
	switch t.Provider {
	case "tailscale":
		if t.Mode != "" && t.Mode != "serve" && t.Mode != "funnel" {
			return fmt.Errorf("config: handoff %q: tunnel mode must be serve|funnel, got %q", handoffName, t.Mode)
		}
	case "ssh":
		if t.SSHHost == "" {
			return fmt.Errorf("config: handoff %q: tunnel provider \"ssh\" requires ssh_host (e.g. localhost.run, serveo.net, a.pinggy.io)", handoffName)
		}
	case "command":
		if len(t.Command) == 0 {
			return fmt.Errorf("config: handoff %q: tunnel provider \"command\" requires a non-empty command:", handoffName)
		}
	}
	if t.URLPattern != "" {
		if _, err := regexp.Compile(t.URLPattern); err != nil {
			return fmt.Errorf("config: handoff %q: invalid tunnel url_pattern %q: %w", handoffName, t.URLPattern, err)
		}
	}
	return nil
}

// handoffNames lists the defined `handoffs:` names, sorted, for error messages.
func (c *Config) handoffNames() string {
	if len(c.Handoffs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(c.Handoffs))
	for n := range c.Handoffs {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ActionRef is one configured action together with a human-readable location
// (e.g. `github[ednition] rules[0].actions.review_requested`), so a cross-config
// check can say exactly where a bad reference lives. Integrations enumerate these
// for the CLI's validate/startup pass; see CheckAgentRefs.
type ActionRef struct {
	Where  string
	Action Action
}

// CheckAgentRefs verifies every agent-type action (and every agent-type workflow
// step) names a profile that exists under top-level `agents:`. The engine looks a
// profile up with a bare map index, so an unknown name would otherwise resolve to
// an empty profile at dispatch time — no --provider — and paseo rejects the run
// with MISSING_PROVIDER only once a live trigger reaches that step. Catch it here.
func (c *Config) CheckAgentRefs(refs []ActionRef) error {
	for _, r := range refs {
		if err := c.checkAgentRef(r.Where, r.Action); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) checkAgentRef(where string, a Action) error {
	if a.Type == "agent" {
		if a.Agent == "" {
			return fmt.Errorf("config: %s: agent action needs `agent: <profile>` (defined: %s)", where, c.agentNames())
		}
		if _, ok := c.Agents[a.Agent]; !ok {
			return fmt.Errorf("config: %s: unknown agent profile %q (defined: %s)", where, a.Agent, c.agentNames())
		}
	}
	if a.Handoff != "" {
		if _, ok := c.Handoffs[a.Handoff]; !ok {
			return fmt.Errorf("config: %s: unknown handoff %q (defined: %s)", where, a.Handoff, c.handoffNames())
		}
	}
	for i, s := range a.Steps {
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("step%d", i+1) // mirrors the engine's default step id
		}
		if err := c.checkAgentRef(where+" step "+id, s); err != nil {
			return err
		}
	}
	return nil
}

// agentNames lists the defined profile names, sorted, for error messages.
func (c *Config) agentNames() string {
	if len(c.Agents) == 0 {
		return "none"
	}
	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func expandHome(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// StateDir returns the state directory to use for the default StateFile/AuditLog
// paths: ~/.local/state/conductor.
func StateDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".local/state/conductor")
}
