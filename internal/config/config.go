// Package config loads and validates conductor's YAML configuration.
//
// The top level is integration-agnostic: control/notify/agents/dispatch/store
// plus a list of raw integration entries. Each integration decodes its own
// sub-config (see internal/integrations/*). Action and Step are shared here
// because both the integration (which maps events→actions) and the dispatch
// package (which executes them) need them.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// strictUnmarshal decodes data into v rejecting unknown keys, so a typo'd
// config key (known_hostss, filtres, …) is a named load error instead of a
// silently dropped setting.
//
// Before the strict pass the document is prepared for YAML-anchor reuse
// (see anchors.go): aliases are expanded into their content and top-level
// `x-` extension keys are dropped. Everything else is judged exactly as
// strictly as before — `x-` is the whole exemption.
func strictUnmarshal(data []byte, v any) error { return strictDecode(data, v, true) }

// strictDecode is strictUnmarshal with the `x-` exemption made explicit.
// topLevel is true only for a whole config document; see prepareStrict.
func strictDecode(data []byte, v any, topLevel bool) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	prepared := prepareStrict(&doc, topLevel)
	if prepared == nil {
		return nil // an empty document decodes to the zero value
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(prepared); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return strictDecodeBytes(b.Bytes(), v)
}

// strictDecodeBytes is the strict decode itself, over a document already
// prepared by prepareStrict.
func strictDecodeBytes(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // an empty document decodes to the zero value
		}
		return err
	}
	return nil
}

// strictNodeDecode is strictUnmarshal for custom UnmarshalYAML(*yaml.Node)
// implementations: yaml.v3 does not propagate KnownFields into them, so the
// node is re-encoded and run through a strict decoder.
//
// It is NOT the document top level, so the `x-` exemption does not apply:
// an `x-note` on a step or a runtime is an unknown key, exactly like any
// other typo. The exemption exists so a document has somewhere to park
// anchors — a step has no such need.
func strictNodeDecode(n *yaml.Node, v any) error {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	if err := enc.Encode(n); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return strictDecode(b.Bytes(), v, false)
}

// Config is the whole config file.
type Config struct {
	// Imports lists other YAML files (paths or globs, relative to this file's
	// directory) to merge in, so the config can be split across files. Maps merge
	// recursively, lists (e.g. integrations) concatenate, and this file's own keys
	// win over imported ones. Processed at load time; empty here after loading.
	Imports []string `yaml:"imports"`

	// baseDir is the directory the config file was loaded from — the anchor for
	// resolving relative paths (e.g. a plugin's local source). Set by Load.
	baseDir string

	// Legacy* fields are top-level keys removed with the legacy config schema
	// (docs/design/plugin-contract.md Q4, §3 rows V3/G17/G18). They decode (so
	// a config that still carries one of these keys gets the uniform
	// migration error from Validate's checkLegacyBlocks rather than a raw
	// "field not found in type" decode failure) but carry no data a live
	// config can read. An operator migrates with the PREVIOUS release's
	// `conductor config migrate`, then upgrades.
	LegacyIntegrations legacyBlock `yaml:"integrations"`
	LegacyNotify       legacyBlock `yaml:"notify"`
	LegacyHandoff      legacyBlock `yaml:"handoff"`
	LegacyHandoffs     legacyBlock `yaml:"handoffs"`
	LegacyControllers  legacyBlock `yaml:"controllers"`
	LegacyControl      legacyBlock `yaml:"control"`
	LegacyPaseoBin     legacyBlock `yaml:"paseo_bin"`

	// ConnectorsMap, Runtimes, Hosts, Workflows, Triggers, Policy, and
	// SecretRefs are the connectors-model schema (see connectors.go). They
	// coexist with the legacy blocks: a config may carry either schema (or,
	// mid-migration, both).
	ConnectorsMap map[string]ConnectorRef `yaml:"connectors"`
	Runtimes      RuntimeSet              `yaml:"runtimes"`
	Hosts         map[string]HostConfig   `yaml:"hosts"`
	// Engines is the OPTIONAL per-engine hardening block, keyed by engine name
	// (`js`, `lua`, …). Code-step engine plugins are UNTRUSTED by default: a
	// step's `run: js` has no config surface of its own, so an operator tunes
	// isolation here (or opts out with `trust: full`). Absent → the engine gets
	// the default OS sandbox. See EngineConfig and internal/config/plugins.go.
	Engines map[string]EngineConfig `yaml:"engines,omitempty"`
	// Models is the OPTIONAL top-level `models:` block — named FLEETS. A fleet
	// is a ranked list of acceptable models plus a fallback posture
	// (`{ any: [...], required: bool }`), so a step (or a pack) can name what
	// it needs rather than hardcoding a model the consumer may not have. See
	// FleetSpec and docs/design/runtimes-models-packs.md §2.
	Models map[string]FleetSpec `yaml:"models,omitempty"`
	// Stores are named data stores (boltdb/redis/http) addressed by the
	// `store:` selector on kv.* verbs; nothing is implicit.
	Stores map[string]StoreRef `yaml:"stores"`
	// (There is no `plugins:` block. An external plugin is declared by the
	// `use:` reference on the connectors:/runtimes: entry that uses it — see
	// PluginRefs and docs/design/use-unification.md.)

	// Vaults are named secret stores (conductor/onepassword/pass/file/
	// hashicorp) addressed by {{ vault "<name>" "<key>" }} references and
	// per-vault read/write verbs; env stays the implicit baseline.
	Vaults map[string]VaultRef `yaml:"vaults"`
	// Memory is the shared agent memory (`memory:` section): durable notes
	// with provenance and scope, written by verbs/agents and injected into
	// opted-in agent prompts. Nil = memory not configured (no default).
	Memory    *MemoryConfig          `yaml:"memory"`
	Workflows map[string]WorkflowDef `yaml:"workflows"`
	// Triggers is the `triggers:` section. It decodes from the list form or
	// the named/qualified MAP form, where the key is the trigger's stable
	// address and a `source.event` key implies `on:` — see TriggerList and
	// docs/design/runtimes-models-packs.md §5.4.
	Triggers TriggerList `yaml:"triggers"`
	Policy   *Policy     `yaml:"policy"`
	// Checks are the named quality-gate checks (#36 §16) `gate: run:` lists
	// reference. Each check is one ordinary step (command / code / verb /
	// critic agent) evaluated to pass/fail against the agent's worktree.
	Checks map[string]Step `yaml:"checks"`
	// Pricing overrides the built-in model→$ table cost estimation uses
	// (#36 §14). Model prices drift; the built-ins are coarse defaults and
	// every estimated figure is marked approximate.
	Pricing *PricingConfig `yaml:"pricing"`
	// SecretRefs is the named `secrets:` block: name -> secret reference
	// (env:/op://…), readable in templates as {{.secrets.<name>}}.
	SecretRefs map[string]string `yaml:"secrets"`

	// Callable is the OPTIONAL `callable:` block (#36 §13): conductor's
	// authenticated inbound invoke surface, so an external orchestrator (n8n,
	// cron, a queue, curl, any MCP client) can fire a named callable workflow
	// and get a structured result back. Off unless configured. See
	// CallableConfig and internal/callable.
	Callable CallableConfig `yaml:"callable"`

	Store  Store  `yaml:"store"`
	Update Update `yaml:"update"`
	DryRun bool   `yaml:"dry_run"`
	// AdoptOpenWorkspaces routes PR feedback (new_comment/changes_requested) to an
	// agent whose checkout is already on the PR's head branch — e.g. a workspace you
	// opened by hand — instead of spawning a fresh worktree. Opt-in.
	AdoptOpenWorkspaces bool `yaml:"adopt_open_workspaces"`

	// AgentGuidance is layer 0 of every agent's guidance stack — house rules for
	// tone/format appended to each dispatched prompt (after the identity/write
	// wrapper), e.g. "keep replies short and human". A profile's own guidance and
	// its extends: ancestors stack ON TOP of this rather than replacing it (see
	// GuidanceSpec and (*Engine).agentGuidance). Unset (nil) → the built-in
	// concise/human-tone default is layer 0 instead; a profile's `guidance:
	// { replace: … }` drops this layer for that agent.
	AgentGuidance *string `yaml:"agent_guidance"`

	// Settings is the OPTIONAL top-level `settings:` block: named values
	// substituted into `${settings.NAME}` references ANYWHERE in the config,
	// across every imported file, before it is decoded.
	//
	//	settings:
	//	  review_channel: "#code-reviews"
	//	  org:            acme
	//	  deploy_repo:    "${settings.org}/deploys"   # chains
	//	  bot_channel:    "${env.BOT_CHANNEL}"        # from the environment
	//
	//	policy: { agent_authored: { allow_scopes: { channel: ["${settings.review_channel}"] } } }
	//
	// It is the same mechanism a PACK's `settings:` gives a pack manifest
	// (pack.go), applied to the operator's own config: one place to change a
	// channel, an org, a repo glob that appears in twenty fields. Resolved at
	// LOAD — the same for every dispatch. For a value that varies per event,
	// scope allowlists also render `{{ }}` at dispatch (see
	// docs/design/scope-templating.md).
	//
	// A reference to an undeclared setting is a load error; a setting that
	// resolves to empty is too. See settings.go.
	Settings map[string]string `yaml:"settings,omitempty"`

	// Packs is the OPTIONAL `packs:` block (issue #53): distributable, versioned,
	// parameterized instances of reusable packs (Terraform-modules-for-conductor).
	// Each entry is namespaced under its instance name and, once fetched by
	// `conductor init`, instantiated into the effective config at load (namespace
	// + bind + settings + disarmed triggers). Absent → nothing changes. See
	// pack.go / pack_instantiate.go / pack_resolve.go.
	Packs map[string]PackInstance `yaml:"packs,omitempty"`

	// PackTrust is the OPTIONAL operator-level provenance allowlist (issue #53
	// §21 Phase A): when set, `conductor init` refuses any REMOTE pack source —
	// at any depth, including a dependency's — that does not match one of its
	// `allow:` source globs, unless the operator passes `--allow-unlisted`. Absent
	// → no restriction. See pack_trust.go.
	PackTrust *PackTrustConfig `yaml:"pack_trust,omitempty"`

	// PluginTrust is the OPTIONAL operator-level provenance allowlist for REMOTE
	// plugin sources (#59), the exact analogue of PackTrust: when set, resolving
	// a remote plugin (`conductor init` / `plugin update`) refuses any source not
	// matching an `allow:` glob unless `--allow-unlisted`. Absent → no
	// restriction. Reuses the pack-trust matcher. See pack_trust.go.
	PluginTrust *PackTrustConfig `yaml:"plugin_trust,omitempty"`

	// packWarnings holds non-fatal notices raised while instantiating packs
	// (deprecations, armed-but-unscoped triggers). Not serialized. See PackWarnings.
	packWarnings []string `yaml:"-"`

	// packsInstantiated records that instantiatePacks ran to completion, so a
	// caller can tell a config whose packs have been expanded into steps from
	// one that merely DECLARES packs. Only the former can answer "which
	// plugins does this config need?" completely — see PluginRefsComplete.
	packsInstantiated bool `yaml:"-"`
}

// Update configures periodic self-update checks.
type Update struct {
	Auto     bool     `yaml:"auto"`     // check for and install new releases periodically
	Interval Duration `yaml:"interval"` // how often to check (default 10m; checks are cheap conditional requests)
	// Apply is what happens when a newer release is detected: true (the
	// default — install and re-exec into it, unattended), false (install
	// and stage; a restart applies), or "workflow" (install NOTHING —
	// emit conductor.update_available so a trigger drives the update with
	// pre/post steps around `uses: conductor.update`).
	Apply ApplyMode `yaml:"apply"`
	// Deps controls whether each auto-update cycle also re-resolves `packs:` and
	// `plugins:` — pulling the newest release each dependency's `version:`
	// constraint allows, re-vendoring, and (if anything changed and the resulting
	// config still validates) restarting to load them. **Unset, it follows
	// `auto`**: turning on unattended binary updates opts you into unattended
	// dependency updates too, because wanting one but silently freezing the other
	// is rarely intended. Set `deps: false` to keep the binary current while
	// pinning dependencies to explicit `conductor init` / `pack update` /
	// `plugin update`. Per-item `hold: true` freezes one dependency even when this
	// is on; pinning a plugin to an exact `@version` freezes it. A dep refresh
	// that fails to validate is discarded — the running config stands, the same
	// fail-safe as a bad binary release. Every change and every hold is logged.
	Deps *bool `yaml:"deps"`

	// Reload controls whether a dependency refresh applies a moved plugin by
	// hot-swapping its subprocess IN PLACE (no daemon restart) when the new
	// build's Decl surface is unchanged, falling back to a restart otherwise.
	// Unset follows Deps (so it's on wherever dependency auto-update is). Set
	// `reload: false` to force restart-always — the fleet kill-switch if
	// in-place reload ever misbehaves. A reload that can't be done in place
	// (source connector, ACP runtime, changed Decl/permissions, drain timeout)
	// restarts regardless, so this is never less safe than a plain restart.
	Reload *bool `yaml:"reload"`
}

// ReloadEnabled reports whether a dependency refresh should try in-place plugin
// hot-reload before falling back to a restart. Unset follows DepsEnabled.
func (u Update) ReloadEnabled() bool {
	if u.Reload != nil {
		return *u.Reload
	}
	return u.DepsEnabled()
}

// DepsEnabled reports whether the auto-update cycle should also refresh packs and
// plugins. Unset follows Auto (see Deps): `auto: true` implies dependency updates
// unless `deps: false` says otherwise. The loop itself only runs when Auto is on,
// so an unset Deps is off exactly when nothing is auto-updating anyway.
func (u Update) DepsEnabled() bool {
	if u.Deps != nil {
		return *u.Deps
	}
	return u.Auto
}

// ShouldApply reports whether to re-exec after a successful update (default true).
func (u Update) ShouldApply() bool { return u.Apply.mode == "" || u.Apply.mode == "true" }

// ApplyWorkflow reports the emit-don't-apply mode (`apply: workflow`).
func (u Update) ApplyWorkflow() bool { return u.Apply.mode == "workflow" }

// ApplyMode parses update.apply: a YAML bool or the string "workflow".
type ApplyMode struct {
	mode string // "" (default true) | "true" | "false" | "workflow"
}

// ApplyModeFor builds an ApplyMode (tests, migration).
func ApplyModeFor(mode string) ApplyMode { return ApplyMode{mode: mode} }

func (m *ApplyMode) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if err := n.Decode(&b); err == nil {
		if b {
			m.mode = "true"
		} else {
			m.mode = "false"
		}
		return nil
	}
	var s string
	if err := n.Decode(&s); err == nil && s == "workflow" {
		m.mode = "workflow"
		return nil
	}
	return fmt.Errorf("config: update.apply must be true, false, or workflow")
}

// AgentCap returns the effective concurrent-agent cap: the
// `policy.concurrency.max_agents` when set, else a default of 3. <=0 means
// unlimited. (The legacy `control.max_concurrent_agents` fallback was removed
// with the legacy config schema; set `policy.concurrency.max_agents` instead.)
func (c *Config) AgentCap() int {
	if c.Policy != nil && c.Policy.Concurrency != nil && c.Policy.Concurrency.MaxAgents != nil {
		return *c.Policy.Concurrency.MaxAgents
	}
	return 3
}

// AgentsPerHour returns the effective rolling-hour dispatch cap: the
// `policy.concurrency.max_agents_per_hour` when set, else 0 (unlimited).
// (The legacy `control.max_agents_per_hour` fallback was removed with the
// legacy config schema; set `policy.concurrency.max_agents_per_hour` instead.)
func (c *Config) AgentsPerHour() int {
	if c.Policy != nil && c.Policy.Concurrency != nil && c.Policy.Concurrency.MaxAgentsPerHour != nil {
		return *c.Policy.Concurrency.MaxAgentsPerHour
	}
	return 0
}

// GlobalPauseLabel returns the fleet-wide pause label set at the top-level
// `policy:` scope ("" when unset). Mirrors the retired `control.pause_label`;
// a connector/trigger-scoped pause label is resolved through the normal
// policy cascade instead (see (*Engine).policyFor).
func (c *Config) GlobalPauseLabel() string {
	if c.Policy != nil && c.Policy.PauseLabel != nil {
		return *c.Policy.PauseLabel
	}
	return ""
}

// GlobalShadow reports the fleet-wide shadow-mode default set at the
// top-level `policy:` scope (false when unset). Mirrors the retired
// `control.shadow`; a connector/trigger-scoped override is resolved through
// the normal policy cascade instead (see (*Engine).policyFor), and a
// per-action `shadow:` always wins over either.
func (c *Config) GlobalShadow() bool {
	return c.Policy != nil && c.Policy.Shadow != nil && *c.Policy.Shadow
}

// DefaultFlowMaxFanOut bounds a config-authored for_each / parallel fan-out
// when the operator sets no `policy.max_fan_out`. Generous enough for real
// operator flows, finite so a data-driven list cannot spawn an unbounded
// number of dispatches (#36 §146 F3).
const DefaultFlowMaxFanOut = 100

// FlowMaxFanOut is the effective cap on one config-authored for_each/parallel
// step's fan-out: the global `policy.max_fan_out` when set (>0), else
// DefaultFlowMaxFanOut. Always finite — there is no "unlimited" spelling, so a
// runaway list is always refused.
func (c *Config) FlowMaxFanOut() int {
	if c != nil && c.Policy != nil && c.Policy.MaxFanOut != nil && *c.Policy.MaxFanOut > 0 {
		return *c.Policy.MaxFanOut
	}
	return DefaultFlowMaxFanOut
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
	// HistoryRetention / HistoryMaxRuns bound the run-history directory
	// (#36 §20). Zero → 14d / 500 runs.
	HistoryRetention Duration `yaml:"history_retention"`
	HistoryMaxRuns   int      `yaml:"history_max_runs"`
}

// ControllerConfig is the internal shape the controller registry consumes for
// one named agent runtime. It used to also be decoded directly from a
// top-level `controllers:` block; that block was removed with the legacy
// config schema (`runtimes:` is the only way to declare one now), but the
// type stays — every `runtimes:` entry still converts to one via
// RuntimeConfig.Controller.
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
	// (see cmd/conductor's resolvePaseoBin).
	Bin string `yaml:"bin"`
	// Home is the paseo daemon home this runtime targets (the `--home` flag paseo
	// 0.9+ uses to select a local daemon; older paseo has no such flag and
	// conductor omits it after detecting the version). `~` is expanded. Empty →
	// PASEO_HOME env, else paseo's default. Mirrors RuntimeConfig.Home.
	Home string `yaml:"home"`
	// Server is an explicit paseo daemon ENDPOINT (paseo's `--host`), winning
	// over Home. Distinct from Host below, which names an SSH box: `server:`
	// picks which daemon to talk to, `host:` picks where the CLI runs.
	// Mirrors RuntimeConfig.Server.
	Server string `yaml:"server"`
	// Host names a `hosts:` entry; this controller's subprocess launches run
	// there over SSH instead of locally. All controller types support it —
	// cli/acp/agent-deck wrap their subprocess in the ssh launch, paseo runs
	// its whole CLI remotely, and opencode's server is reached
	// through an ssh -W stdio forward — see checkRemoteHostSupport.
	Host string `yaml:"host"`
	// Isolation wraps this runtime's launches in the per-dispatch sandbox
	// (#36 §15) — carried from the `runtimes:` form; see RuntimeConfig.
	Isolation *IsolationConfig `yaml:"isolation,omitempty"`
	// ScrubEnv makes an ACP launch inherit only a minimal env allowlist
	// (sandbox.MinimalEnv) instead of the daemon's full os.Environ(), so the
	// daemon's credential-bearing env is not handed to the child. Set for
	// EXTERNAL runtime plugins (#54 §8.1) — untrusted third-party code. Not a
	// user-facing config key (synthesized in cmd/conductor); never decoded.
	ScrubEnv bool `yaml:"-"`
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

// MergedControllers converts every `runtimes:` entry to the ControllerConfig
// shape the controller registry consumes (via RuntimeConfig.Controller).
// Named "Merged" for its callers (it used to also merge in a legacy
// `controllers:` block, removed with the legacy config schema).
func (c *Config) MergedControllers() map[string]ControllerConfig {
	merged := make(map[string]ControllerConfig, len(c.Runtimes))
	for name, rt := range c.Runtimes {
		merged[name] = rt.Controller()
	}
	return merged
}

// DefaultRuntimeName returns the name of the runtime flagged default:true, or
// "" when none is (resolution then falls back to the built-in paseo). At most
// one `runtimes:` entry may set it — see validateRuntimeDefaults.
func (c *Config) DefaultRuntimeName() string {
	for name, rt := range c.Runtimes {
		if rt.Default {
			return name
		}
	}
	return ""
}

// SkillPolicy is the per-profile `skill:` block (#36 §12): which of
// conductor's own capabilities a dispatched agent may reach back into over
// the daemon socket. The zero value denies everything.
type SkillPolicy struct {
	// Verbs is the DENY-BY-DEFAULT allowlist of connector verbs this step may
	// call — the one list that drives all three agent-facing surfaces: the
	// capability card injected into the prompt, `conductor discover` / the
	// MCP tool list, and what the daemon enforces
	// (docs/design/skill-capability-and-pack-interface.md §A/§B).
	//
	// Grant forms (path.Match patterns, same matcher policy.agent_authored
	// uses):
	//
	//	verbs: ["*"]                    all verbs of all connectors
	//	verbs: [github.*]               all verbs of one connector
	//	verbs: [github.*, sentry.*]     several connectors
	//	verbs: [github.submit_review]   specific verbs
	//
	// The block also takes a MAP form — the same grant, per verb, with the
	// RESOURCE each call may name (docs/design/skill-verb-scope.md):
	//
	//	verbs:
	//	  slack.post:           { channel: ["#code-reviews"] }
	//	  github.submit_review: {}
	//	  kv.*:                 { store: ["shared-kv"] }
	//
	// The keys inside an entry are that verb's OWN scope-tagged options, so
	// `channel` under slack.post and `repo` under github.submit_review never
	// collide. A scoped option the entry does not list is limited to the
	// dispatch's own context (its repo, the channel its event came from);
	// listing widens it. Naming an option the verb doesn't declare as a
	// resource is a load error. Both forms carry the same verb access — see
	// VerbScopes.
	//
	// Empty (or no `skill:` block at all) → no surface: no tools, no card,
	// nothing injected, everything denied. READS ARE NOT OPEN BY DEFAULT —
	// a read verb outside the grant is refused like any other. Breadth is
	// an explicit dial, never a default.
	//
	// `conductor.*` and `workflow.*` are never reachable here at ANY
	// breadth, including `["*"]`: conductor's own orchestration goes through
	// run_step under policy.agent_authored. Inside a PACK the grant is
	// additionally bounded by requires.connectors (§C).
	Verbs []string `yaml:"verbs"`
	// VerbScopes carries the MAP form's per-verb resource constraints:
	// verb pattern → option name → allowed values (exact or glob). It is
	// derived from `verbs:` at decode time, never written directly, and is
	// always a subset of Verbs' patterns — the list form leaves it empty,
	// which means "every scoped option is limited to the dispatch's own
	// context".
	VerbScopes map[string]map[string][]string `yaml:"-"`
	// SecretsVia picks how this agent obtains a credential it truly needs:
	// "broker" (the audited single-use secret broker), "env" (DEPRECATED —
	// template the secret into the step's env:, which puts the raw value in
	// the runtime's environment), or "none" (the default: no secrets).
	SecretsVia string `yaml:"secrets_via"`
	// AllowSecrets names the `secrets:` entries the broker may issue to this
	// profile. Exact names only — no patterns; broadening is a config edit,
	// never an agent request. Empty → the broker issues nothing.
	AllowSecrets []string `yaml:"allow_secrets"`
	// MaxCalls caps verb executions per skill session (analogous to
	// agent_authored.limits). 0 = the built-in default (256).
	MaxCalls int `yaml:"max_calls"`
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
	// Exclude skips PRs matching these criteria (e.g. release PRs). No config
	// surface sets it any more (the `filters: {exclude: …}` block is gone —
	// write `not_branch`/`not_label_any`/`not_title` in `filter:` instead); it
	// remains because a legacy integration config decodes straight into Action
	// and its lowering is what gives an event its intrinsic default.
	Exclude Exclude `yaml:"exclude"`
	// Filter is the unified composable filter (docs/design/unified-filter.md,
	// docs/design/unified-filter-phase2.md) — the trigger's whole predicate.
	// When set it REPLACES the event's intrinsic default keep-condition at
	// every site that evaluates one; when unset each site lowers its own
	// defaults into the same IR, so both paths run one evaluator.
	//
	// A routing-ONLY filter (nothing but `repo`/`not_repo`) does not count as
	// set: it says where the trigger applies, not what it wants of the event,
	// so the intrinsic default still stands. See the lowering in
	// internal/connector.githubImpl.lowerTrigger.
	Filter *Filter `yaml:"filter,omitempty"`

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
	If           string         `yaml:"if"`            // step condition (see pkg/expr)
	OutputSchema map[string]any `yaml:"output_schema"` // agent step: JSON schema for structured output
	Background   bool           `yaml:"background"`    // workflow step: dispatch `paseo run --background` and don't
	//                                                    wait/capture — launch a live agent to drive interactively
	// Handoff names an ask-capable connector (one with an `ask` verb — slack,
	// discord, web) a background step presents its interactive review draft
	// on, the same way a connectors-model step's `handoff:` does (see
	// connectors.go Step.Handoff and internal/flow/validate.go
	// checkAskCapable). Empty = no hand-off channel (paseo-native). Only
	// meaningful on a background step.
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
	AuthorBot          *bool          `yaml:"author_bot"`    // comment/review events: require the author to be (true) / not be (false) a bot; nil = either
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

// Refs labels each action in the set with where it lives (see ActionRef).
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

	// Cheap probe: no import of any form (top-level `imports:`, a section's
	// `imports:` key, a `- import:` trigger item) → parse the single file
	// directly (unchanged path, no map round-trip). Only pay the merge
	// machinery when imports are used.
	var probe map[string]any
	if err := yaml.Unmarshal(expanded, &probe); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	var c Config
	c.baseDir = filepath.Dir(path)
	if !hasAnyImports(probe) {
		// `${settings.NAME}` substitution happens on the BODY, before the
		// decode, so a setting lands wherever it was referenced — including
		// inside values the decode would otherwise have already fixed.
		expanded, err = ExpandSettings(expanded)
		if err != nil {
			return nil, err
		}
		if err := strictUnmarshal(expanded, &c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	} else {
		// TWO PASSES over the same import traversal, so the settings a file
		// declares reach every OTHER file's body and no traversal logic is
		// duplicated: pass 1 merges with no substitution and tells us what
		// `settings:` the graph declares; pass 2 re-runs it with those
		// resolved, substituting each file's body as it is read.
		merged, err := loadMerged(path, map[string]bool{}, nil)
		if err != nil {
			return nil, err
		}
		declared, err := settingsFromDoc(merged)
		if err != nil {
			return nil, err
		}
		if len(declared) > 0 {
			resolved, rerr := resolveSettingValues(declared)
			if rerr != nil {
				return nil, rerr
			}
			merged, err = loadMerged(path, map[string]bool{}, resolved)
			if err != nil {
				return nil, err
			}
		}
		out, err := yaml.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merge imports: %w", err)
		}
		// Re-parse the merged document so the custom unmarshalers (IntegrationRef,
		// ActionSet, Duration, …) still run over each node. Strict: an unknown
		// key from ANY of the merged files is a named error.
		if err := strictUnmarshal(out, &c); err != nil {
			return nil, fmt.Errorf("parse merged config: %w", err)
		}
	}
	// A `${settings.X}` that survived into a real field names nothing declared
	// — the typo, caught here rather than as a mystery value at dispatch.
	if err := checkSettingRefs(&c); err != nil {
		return nil, err
	}
	// File-referencing `workflow:` forms (workflow:+import:, a bare file path) join
	// the merged workflow set first — before packs add namespaced `review/flow`
	// refs that would otherwise look like relative file paths.
	if err := c.resolveWorkflowFiles(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Instantiate `packs:` into the effective config (namespace + bind + settings
	// + disarmed triggers) from the already-vendored packs, BEFORE the trigger/
	// extends/normalize passes — so a pack's own triggers (including list-form
	// `on:` and `extends:`) and pack-local `extends:` on agents/workflows get the
	// same treatment as the consumer's own. No-op without a `packs:` block, so
	// existing configs are unaffected. Offline — the network fetch is
	// `conductor init`.
	if err := c.instantiatePacks(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Trigger `extends:` resolves + abstract bases are stripped BEFORE
	// normalization, so a child can inherit a base's `on:` and bases (which may
	// carry no `on:`) never reach the on:-required / manual-name checks.
	if err := c.resolveTriggerExtends(); err != nil {
		return nil, err
	}
	// Multi-source `on:` lists expand into one trigger per source before
	// anything downstream sees them.
	if err := c.NormalizeTriggers(); err != nil {
		return nil, err
	}
	// `extends:` inheritance across map sections resolves before defaults fold
	// (agent_guidance → policy) and before validation cross-checks references.
	if err := c.resolveExtends(); err != nil {
		return nil, err
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
func loadMerged(p string, loaded map[string]bool, settings map[string]string) (map[string]any, error) {
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
	// Settings substitute per FILE, through the shared primitive — a setting
	// supplies a value, never document structure (see SubstituteRefs). nil on
	// pass 1, the pass that discovers what `settings:` the graph declares.
	expanded, serr := substituteInBody(expanded, settings)
	if serr != nil {
		return nil, fmt.Errorf("%s: %w", p, serr)
	}
	var m map[string]any
	if err := yaml.Unmarshal(expanded, &m); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", p, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	// `x-` holders are per-file anchor parks (anchors.go). The decode above
	// already resolved this file's aliases into values, so only the holder
	// itself needs dropping — and dropping it HERE, per file, is what keeps
	// anchors file-local: a holder never merges into another file's scope.
	StripExtensionKeys(m)
	// Section-scoped imports expand per file, so their globs resolve against
	// THIS file's directory and a duplicate entry names both sources.
	if err := expandSectionImports(p, m); err != nil {
		return nil, err
	}
	imports := toStrings(m["imports"])
	delete(m, "imports")

	merged := map[string]any{}
	dir := filepath.Dir(p)
	for _, imp := range imports {
		// Through globImport so `**` is refused by name here too (filepath
		// globs match one level; a silent nested-dir skip is worse than an
		// error). Top-level imports keep their stricter contract: even an
		// unmatched GLOB is an error, not a ready-to-fill no-op.
		matches, err := globImport(dir, p, imp)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: import %q matched no files", p, imp)
		}
		for _, f := range matches {
			sub, err := loadMerged(f, loaded, settings)
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
// It substitutes into the PARSED tree, inside scalar values only, for the
// same reason `${settings.X}` does (see substituteSettingsNode): an
// environment variable supplies a VALUE. A value carrying a newline, a quote
// or a `#` must not be able to close a scalar and open a sibling key — and on
// a shared box the environment is not always the operator's alone.
//
// A reference inside a `#` comment is left alone, which is what the old
// line-splitting implementation went out of its way to do and what a node
// walk gets for free: comments are not scalar values.
func expandEnv(path string, b []byte) ([]byte, error) {
	var missing []string
	seen := map[string]bool{}
	out, err := SubstituteRefs(b, envRe, func(ref string) (string, bool) {
		name := envRe.FindStringSubmatch(ref)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			if !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			return "", true // substitute empty; the error below is the real answer
		}
		return v, true
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("config %s references undefined environment variable(s): %s (define them in %s or the environment)",
			path, strings.Join(missing, ", "), filepath.Join(filepath.Dir(path), "conductor.env"))
	}
	if err != nil {
		return nil, err
	}
	return out, nil
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
	// A runtime's name implies its `use:` when it names no implementation.
	// After resolveExtends, so an inherited use: still wins.
	c.applyRuntimeUseDefaults()
	// Back-compat: the top-level agent_guidance is now the GLOBAL scope of
	// policy.guidance. Fold it in so connector/trigger-scoped guidance stacks
	// on top of it through the normal policy cascade. policy.guidance (if set
	// explicitly at global scope) wins; agent_guidance is then ignored.
	if c.AgentGuidance != nil {
		if c.Policy == nil {
			c.Policy = &Policy{}
		}
		if c.Policy.Guidance == nil {
			c.Policy.Guidance = &GuidanceSpec{Parts: []string{*c.AgentGuidance}}
		}
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
	if c.Update.Auto && c.Update.Interval == 0 {
		c.Update.Interval = Duration(10 * time.Minute)
	}
}

// legacyBlock is a placeholder for a top-level key removed with the legacy
// config schema (docs/design/plugin-contract.md decision Q4, §3 rows
// V3/G17/G18). It decodes successfully for ANY YAML shape under the key — so
// a config that still carries the key gets the uniform migration error from
// checkLegacyBlocks (below) instead of a raw "field not found in type"
// strict-decode failure — but records nothing: there is nothing left to read.
type legacyBlock struct{ present bool }

// UnmarshalYAML only runs when the key is present in the document at all
// (yaml.v3 never calls it for an absent key), so present==true is exactly
// "the config still has this key", regardless of what it holds (`{}`, `[]`,
// `null`, a populated block — all count).
func (b *legacyBlock) UnmarshalYAML(n *yaml.Node) error {
	b.present = true
	return nil
}

// checkLegacyBlocks is the first check Validate runs: it names the first
// still-present legacy top-level key (checked in a fixed order, for a
// deterministic error when more than one lingers) with the uniform migration
// message. An operator migrates with the PREVIOUS release's `conductor config
// migrate`, then upgrades — this binary never reads these blocks.
func (c *Config) checkLegacyBlocks() error {
	for _, k := range []struct {
		name    string
		present bool
	}{
		{"integrations", c.LegacyIntegrations.present},
		{"notify", c.LegacyNotify.present},
		{"handoff", c.LegacyHandoff.present},
		{"handoffs", c.LegacyHandoffs.present},
		{"controllers", c.LegacyControllers.present},
		{"control", c.LegacyControl.present},
		{"paseo_bin", c.LegacyPaseoBin.present},
	} {
		if k.present {
			return removedLegacyBlockErr(k.name)
		}
	}
	return nil
}

// removedLegacyBlockErr is the one helper every removed-legacy-block error
// goes through, so the wording is uniform regardless of which key triggered
// it.
func removedLegacyBlockErr(key string) error {
	return fmt.Errorf("config: `%s:` was removed with the legacy config schema — "+
		"migrate it with `conductor config migrate` on the release before the plugin contract, then upgrade", key)
}

// Validate checks required fields and cross-field consistency.
func (c *Config) Validate() error {
	if err := c.checkLegacyBlocks(); err != nil {
		return err
	}
	if !c.HasConnectors() {
		return fmt.Errorf("config: no connectors configured")
	}
	if err := c.validateConnectors(); err != nil {
		return err
	}
	if err := c.validatePluginRefs(); err != nil {
		return err
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	if err := c.validateStores(); err != nil {
		return err
	}
	if err := c.validateVaults(); err != nil {
		return err
	}
	if err := c.validateMemory(); err != nil {
		return err
	}
	if err := c.validateSessions(); err != nil {
		return err
	}
	if c.Policy != nil {
		if err := validateAgentAuthored("policy", c.Policy.AgentAuthored, c.Hosts); err != nil {
			return err
		}
		if err := validateBudget("policy", c.Policy.Budget); err != nil {
			return err
		}
	}
	if err := c.validatePricing(); err != nil {
		return err
	}
	if err := c.validateSteps(); err != nil {
		return err
	}
	if err := c.validateCallable(); err != nil {
		return err
	}
	return nil
}

// SkillEnabled reports whether the daemon serves the tool socket and builds
// the skill broker. Always true: the done signal (step.done) is auto-granted
// to EVERY dispatch — with no background reaper, an agent's own done call is
// how its workspace gets reclaimed, so every agent needs the surface (the
// socket to reach and the broker to mint its token). A step's own skill: block
// only widens what the grant admits beyond that.
func (c *Config) SkillEnabled() bool { return true }

// Skill delivery modes: how a dispatched agent reaches the conductor skill
// surface on its runtime.
const (
	SkillModeNone = "none" // the surface can't reach this agent (an unknown runtime)
	SkillModeMCP  = "mcp"  // injected as an MCP server at launch (ACP session/new, opencode config)
	SkillModeCLI  = "cli"  // the agent shells the `conductor` CLI over the local daemon socket
)

// SkillDelivery reports HOW the conductor skill surface reaches a profile's
// agent. MCP runtimes (ACP transports, native opencode) carry the tool server
// injected at launch. Every other LOCAL runtime — paseo, agent-deck, bare cli —
// exposes no MCP surface, but the agent runs on the daemon's box with a shell,
// so it uses the `conductor` CLI over the unix socket (broker.MintSession token
// in env). A REMOTE launch (a runtime/profile host:) can reach neither today —
// the daemon socket isn't on that box — so it's SkillModeNone until the HTTP
// endpoint lands.
func (c *Config) SkillDelivery(p Step) (runtime, mode string) {
	rn := p.Runtime
	if rn == "" {
		rn = c.DefaultRuntimeName()
	}
	if rn == "" {
		rn = BuiltinPaseoRuntime
	}
	cc, found := c.MergedControllers()[rn]
	if !found {
		// The implicit built-in paseo is a local runtime with a shell → CLI;
		// an unknown named runtime is its own validation error.
		if rn == BuiltinPaseoRuntime {
			return rn, SkillModeCLI
		}
		return rn, SkillModeNone
	}
	host := cc.Host
	if p.Host != "" {
		host = p.Host
	}
	if host != "" {
		// Remote runtime: no local socket, but it has a shell, and conductor
		// reaches it over SSH — so the agent reaches back through the `conductor`
		// CLI over an SSH reverse tunnel forwarding the daemon socket to the
		// remote box (dispatch wires it). Internal wiring over the existing SSH
		// trust; nothing public. Same CLI face as a local shell runtime.
		return rn, SkillModeCLI
	}
	switch {
	case cc.Type == "opencode", cc.Agent == "opencode" && cc.EffectiveTransport() == "native":
		return rn, SkillModeMCP
	case cc.EffectiveTransport() == "acp":
		return rn, SkillModeMCP
	}
	return rn, SkillModeCLI // paseo / agent-deck / bare-cli, all local with a shell
}

// SkillToolsSupported reports whether the skill surface reaches this profile's
// agent at all (via MCP or the CLI). `conductor validate` warns when it does
// not (a skill: profile that gets nothing — a remote launch today).
func (c *Config) SkillToolsSupported(p Step) (runtime string, ok bool) {
	rn, mode := c.SkillDelivery(p)
	return rn, mode != SkillModeNone
}

// runtimeSupportsLaunchFields reports whether a step's resolved runtime is a
// paseo-type runtime — the only kind that carries Step.Detach/Repo/Images
// (`paseo run -d`, its checkout, `--image`). known is false when the runtime
// can't be resolved statically (an unconfigured Config, or a named runtime
// this Config doesn't define — a different validator already rejects that),
// in which case the caller should not fail the step on this check alone; the
// engine re-checks at dispatch.
func (c *Config) runtimeSupportsLaunchFields(s Step) (supported, known bool) {
	if c == nil {
		return true, false
	}
	rn := s.Runtime
	if rn == "" {
		rn = c.DefaultRuntimeName()
	}
	if rn == "" || rn == BuiltinPaseoRuntime {
		return true, true
	}
	cc, found := c.MergedControllers()[rn]
	if !found {
		return true, false
	}
	return cc.Type == "paseo" || (cc.Type == "" && cc.Agent == ""), true
}

// BuiltinPaseoRuntime is the implicit default runtime's name (mirrors
// controller.BuiltinPaseo without the import).
const BuiltinPaseoRuntime = "paseo"

// runtimeNames lists defined runtime names, sorted, for errors.
func (c *Config) runtimeNames() string {
	names := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// checkRemoteHostSupport validates a runtime/controller's `host:` reference:
// it must name a defined `hosts:` entry. Every runtime type runs remotely —
// cli/acp/agent-deck ssh-wrap their subprocess, paseo executes its whole CLI
// (checkouts under the remote ~/.conductor) on the host, and
// opencode's remotely-launched server is reached through an ssh -W stdio
// forward. kind is "runtime" or "controller" (for the error text); the
// typ/agent/transport fields are accepted so a future type-specific
// restriction has the context it needs.
func (c *Config) checkRemoteHostSupport(kind, name, host, typ, agent, transport string) error {
	if host == "" {
		return nil
	}
	if _, ok := c.Hosts[host]; !ok {
		return fmt.Errorf("config: %s %q: unknown host %q (defined: %s)", kind, name, host, c.hostNames())
	}
	return nil
}

// ActionRef is one configured action together with a human-readable location
// (e.g. `github[acme] rules[0].actions.review_requested`), so a cross-config
// check can say exactly where a bad reference lives. An integration built from
// the connectors-model lowering (internal/connector/convert.go) can still
// enumerate these (see ActionSet.Refs) — no caller resolves them against
// anything anymore (there is no top-level `agents:` profile registry in the
// connectors model), but the shape stays so those lowered integrations keep
// compiling.
type ActionRef struct {
	Where  string
	Action Action
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

// BaseDir returns the directory the config was loaded from (empty for a config
// built in memory rather than loaded from disk).
func (c *Config) BaseDir() string { return c.baseDir }

// PluginStagingDir is the root under which each plugin instance gets the
// staging directory its file-returning verbs write to (plugin-contract.md Q7).
// A templated path a step hands to a launch (`images:`) must resolve under it.
func PluginStagingDir() string { return filepath.Join(StateDir(), "plugins", "staging") }

// stateDirOverride is set by --state-dir, for a CLI invocation or a test
// that must not touch the real install state.
var stateDirOverride string

// SetStateDir overrides the state directory for this process.
func SetStateDir(dir string) { stateDirOverride = dir }

// StateDir returns the state directory for the default StateFile/AuditLog
// paths, honouring the override, then XDG_STATE_HOME, then the
// spec's default of ~/.local/state.
//
// The XDG variable is the whole point of the XDG path it was hardwiring:
// hardcoding $HOME/.local/state meant a box that redirects its state — a
// container, a multi-tenant host, a test — silently wrote install state
// somewhere it did not expect, and read a different box's back.
func StateDir() string {
	if stateDirOverride != "" {
		return stateDirOverride
	}
	if x := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); x != "" {
		return filepath.Join(x, "conductor")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".local/state/conductor")
}
