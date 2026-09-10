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
	"time"

	"gopkg.in/yaml.v3"
)

// ConnectorRef is one entry in the `connectors:` map: the common header plus
// the raw node so the concrete connector type can decode its own connection
// fields (tokens, app creds, schedules, feeds, …).
type ConnectorRef struct {
	// Use names WHAT IMPLEMENTS this connector — a builtin type (`github`), a
	// bare name resolved from the official plugin repo (`sentry`), an explicit
	// repo (`acme/plugins/jira`), or a local binary (`./bin/conductor-jira`).
	// It replaced the old `type:` field plus the whole `plugins:` block; the
	// kind (connector) comes from this block, never from the operator. See
	// ParseUse and docs/design/use-unification.md.
	Use string `yaml:"use,omitempty"`
	// Network is this instance's DECLARED EGRESS — the "host:port" targets it
	// is allowed to reach. It is the visible half of the permission manifest:
	// what the operator accepts when they add the connector. It may not exceed
	// the implementation's own declared egress.
	Network []string `yaml:"network,omitempty"`
	Enabled *bool    `yaml:"enabled,omitempty"`
	// Options are the connector's default verb options; each `uses:` call's
	// options merge over these (the call wins). Identity (`as:`) lives here.
	Options map[string]any `yaml:"options,omitempty"`
	// Policy is the connector-scoped policy block (ignore/rate_limits/backoff/
	// pause_label live here; quiet_hours/concurrency may override the global).
	Policy *Policy `yaml:"policy,omitempty"`
	// Isolation is OPTIONAL hardening for a plugin-backed connector: the OS
	// confinement layer (#36 §15) applied to the plugin's subprocess. Absent is
	// the NORMAL case — the default model is the permission manifest above, not
	// a jail. Set this on a locked-down box where OS confinement is wanted on
	// top. Ignored for a builtin connector (nothing separate to confine).
	Isolation *IsolationConfig `yaml:"isolation,omitempty"`
	// AllowSecrets optionally tightens which secret refs may cross the process
	// boundary to a plugin-backed connector — an EXACT-match allowlist (no
	// globs). Empty = no extra restriction beyond the structural guarantee that
	// an implementation only ever receives its own instances' credentials.
	AllowSecrets []string `yaml:"allow_secrets,omitempty"`
	raw          yaml.Node
	// legacyType holds a pre-`use:` `type:` value. It is NOT part of the schema
	// — it exists only so validateConnectors can emit a migration-specific error
	// instead of the silent "missing use:" a dropped field would produce.
	legacyType string
}

// UnmarshalYAML captures the header fields and retains the raw node.
func (r *ConnectorRef) UnmarshalYAML(n *yaml.Node) error {
	type hdr struct {
		Use     string         `yaml:"use,omitempty"`
		Network []string       `yaml:"network,omitempty"`
		Enabled *bool          `yaml:"enabled,omitempty"`
		Options map[string]any `yaml:"options,omitempty"`
		Policy  *Policy        `yaml:"policy,omitempty"`
		// Type is the retired field, read for diagnostics only (see legacyType).
		Type         string           `yaml:"type,omitempty"`
		Isolation    *IsolationConfig `yaml:"isolation,omitempty"`
		AllowSecrets []string         `yaml:"allow_secrets,omitempty"`
	}
	var h hdr
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Use, r.Network, r.Enabled, r.Options, r.Policy = h.Use, h.Network, h.Enabled, h.Options, h.Policy
	r.Isolation, r.AllowSecrets, r.legacyType, r.raw = h.Isolation, h.AllowSecrets, h.Type, *n
	return nil
}

// Decode unmarshals the raw connector node into a type-specific struct.
func (r ConnectorRef) Decode(v any) error { return r.raw.Decode(v) }

// IsEnabled reports whether the connector is enabled (default true).
func (r ConnectorRef) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Resolved parses this entry's `use:` reference as a connector.
func (r ConnectorRef) Resolved() (Use, error) { return ParseUse(UseKindConnector, r.Use) }

// TypeName is the connector TYPE this entry implements — the name the connector
// registry is keyed by. For a builtin it is the `use:` name itself; for a plugin
// it is the component leaf (`acme/plugins/jira` → "jira"). Empty when `use:`
// does not parse; validateConnectors reports that as a config error.
func (r ConnectorRef) TypeName() string {
	u, err := r.Resolved()
	if err != nil {
		return ""
	}
	return u.Name
}

// RuntimeConfig is one entry in the `runtimes:` map — where agents run
// (today's controllers, renamed, plus launch config that used to be global).
type RuntimeConfig struct {
	// Extends names another runtimes: entry this one inherits unset fields from
	// (see resolveExtends) — e.g. several cli runtimes sharing host/isolation.
	Extends string `yaml:"extends,omitempty"`
	// Use names WHAT IMPLEMENTS this runtime — a builtin (`paseo`, `acp`,
	// `opencode`, `agent-deck`, `cli`), a bare name resolved from the official
	// plugin repo (`modal`), an explicit repo, or a local binary. It replaced
	// the old `type:` field plus the whole `plugins:` block; the kind (runtime)
	// comes from this block. See ParseUse.
	Use string `yaml:"use,omitempty"`
	// Agent names the agent driven over the ACP transport (gemini, opencode,
	// …). Valid only with `use: acp`, which it is required by.
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
	// Isolation wraps every launch this runtime performs (#36 §15). A
	// profile's own isolation: wins over the runtime's.
	Isolation *IsolationConfig `yaml:"isolation,omitempty"`
	// Models is the OPTIONAL model policy for this runtime: which model it
	// passes by default, how it ranks a choice, and what it may ever run.
	// Absent = fully automatic (roster discovered, bare launch, no
	// restrictions). See RuntimeModels and
	// docs/design/runtimes-models-packs.md §1.2.
	Models *RuntimeModels `yaml:"models,omitempty"`

	// legacy holds a pre-`use:` `type:` value. NOT part of the schema — it is
	// accepted by the decoder only so validateConnectors can name the migration
	// instead of the strict decoder emitting "field type not found", which
	// would be an opaque wall for every not-yet-migrated config on a box that
	// auto-updates. See UnmarshalYAML.
	legacy string
}

// runtimeFields is RuntimeConfig without its UnmarshalYAML method, so the strict
// decode below does not recurse. The retired `type:` rides alongside it, read
// for diagnostics only.
type runtimeFields RuntimeConfig

type runtimeDecode struct {
	runtimeFields `yaml:",inline"`
	Type          string `yaml:"type,omitempty"`
}

// UnmarshalYAML strict-decodes the runtime entry (so a typo is still an error)
// while tolerating the retired `type:` key, which is captured for the migration
// diagnostic in validateConnectors rather than rejected here.
func (r *RuntimeConfig) UnmarshalYAML(n *yaml.Node) error {
	var d runtimeDecode
	if err := strictNodeDecode(n, &d); err != nil {
		return err
	}
	*r = RuntimeConfig(d.runtimeFields)
	r.legacy = d.Type
	return nil
}

// legacyType returns a retired `type:` value this entry still carries, for the
// migration diagnostic.
func (r RuntimeConfig) legacyType() string { return r.legacy }

// RuntimeSet is the `runtimes:` section. It is a map of named runtimes, but
// the YAML accepts three shapes (docs/design/runtimes-models-packs.md §1.1) —
// the common case is one word:
//
//	runtimes: paseo                  # scalar → one runtime
//	runtimes: [paseo, claude]        # list   → several
//	runtimes:                        # map    → named, with config
//	  paseo: { models: { prefer: [claude-opus-5] } }
//
// In every shape a runtime's NAME implies its `use:` reference when they
// match, so `runtimes: paseo` needs no `use:` line at all (filled by
// applyRuntimeUseDefaults, which runs after `extends:` so an inherited `use:`
// still wins over the implication).
type RuntimeSet map[string]RuntimeConfig

// UnmarshalYAML accepts the scalar, list, and map forms.
func (s *RuntimeSet) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*s = nil
			return nil
		}
		var name string
		if err := n.Decode(&name); err != nil {
			return fmt.Errorf("runtimes: must be a name, a list of names, or a map of named runtimes: %w", err)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("runtimes: empty runtime name")
		}
		*s = RuntimeSet{name: {}}
		return nil
	case yaml.SequenceNode:
		out := RuntimeSet{}
		for i, item := range n.Content {
			name, rt, err := decodeRuntimeItem(i, item)
			if err != nil {
				return err
			}
			if _, dup := out[name]; dup {
				return fmt.Errorf("runtimes[%d]: duplicate runtime %q — name them apart, or use the map form", i, name)
			}
			out[name] = rt
		}
		*s = out
		return nil
	case yaml.MappingNode:
		out := RuntimeSet{}
		type plain map[string]RuntimeConfig
		var m plain
		if err := n.Decode(&m); err != nil {
			return err
		}
		for k, v := range m {
			out[k] = v
		}
		*s = out
		return nil
	}
	return fmt.Errorf("runtimes: must be a name, a list of names, or a map of named runtimes")
}

// decodeRuntimeItem decodes one entry of the LIST form: a bare name, or an
// object that carries its own `use:` (from which the name is taken).
func decodeRuntimeItem(i int, item *yaml.Node) (string, RuntimeConfig, error) {
	if item.Kind == yaml.ScalarNode {
		var name string
		if err := item.Decode(&name); err != nil {
			return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: %w", i, err)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: empty runtime name", i)
		}
		return name, RuntimeConfig{}, nil
	}
	if item.Kind != yaml.MappingNode {
		return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: a list item is a runtime name or a { use: …, models: … } object", i)
	}
	var rt RuntimeConfig
	if err := item.Decode(&rt); err != nil {
		return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: %w", i, err)
	}
	if rt.Use == "" {
		return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: an object list item needs `use:` to name what implements it (or write the map form, where the key is the name)", i)
	}
	u, err := ParseUse(UseKindRuntime, rt.Use)
	if err != nil {
		return "", RuntimeConfig{}, fmt.Errorf("runtimes[%d]: %w", i, err)
	}
	return u.Name, rt, nil
}

// applyRuntimeUseDefaults fills the key-implies-`use:` rule: a runtime that
// names no implementation IS its own name. Runs from applyDefaults, i.e. AFTER
// `extends:` resolution, so a child inheriting a parent's `use:` keeps it and
// only a genuinely unset entry falls back to its key. An entry still carrying
// the retired `type:` is left alone so validateConnectors can name the
// migration instead of implying a reference the operator never wrote.
func (c *Config) applyRuntimeUseDefaults() {
	for name, rt := range c.Runtimes {
		if rt.Use != "" || rt.legacy != "" || name == "" {
			continue
		}
		rt.Use = name
		c.Runtimes[name] = rt
	}
}

// Resolved parses this entry's `use:` reference as a runtime.
func (r RuntimeConfig) Resolved() (Use, error) { return ParseUse(UseKindRuntime, r.Use) }

// BuiltinType is the built-in controller kind this runtime maps onto — paseo |
// opencode | agent-deck | cli — or "" for `use: acp` (driven by Agent) and for a
// PLUGIN runtime (whose ControllerConfig is synthesized at boot once the binary
// is verified, in cmd/conductor).
func (r RuntimeConfig) BuiltinType() string {
	u, err := r.Resolved()
	if err != nil || !u.IsBuiltin() || u.Name == "acp" {
		return ""
	}
	return u.Name
}

// IsPlugin reports whether this runtime is implemented by an external plugin
// rather than compiled into the daemon.
func (r RuntimeConfig) IsPlugin() bool {
	u, err := r.Resolved()
	return err == nil && !u.IsBuiltin()
}

// Controller converts a runtime entry to the legacy controller shape the
// controller registry consumes, carrying Bin, Host, and Isolation through.
func (r RuntimeConfig) Controller() ControllerConfig {
	return ControllerConfig{
		Type: r.BuiltinType(), Agent: r.Agent, Transport: r.Transport,
		SessionModel: r.SessionModel, Default: r.Default,
		Tool: r.Tool, Command: r.Command,
		Bin: r.Bin, Host: r.Host, Isolation: r.Isolation,
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
	// Isolation wraps every script this host runs (code steps, remote
	// commands) in the sandbox prefix ON the remote box — mode user or
	// namespace only (the remote box needs the matching sudoers rule /
	// util-linux). This is how the agent_authored sandbox `host:` gets a
	// second wall: even on the sandbox box, agent code runs de-privileged.
	Isolation *IsolationConfig `yaml:"isolation,omitempty"`
}

// IsolationConfig is the per-dispatch isolation block (#36 §15), settable on
// an agent profile, a runtime, or a `hosts:` entry (most-specific wins:
// profile → runtime). It only applies to launches conductor performs itself
// (acp / cli / opencode / agent-deck runtimes, host scripts) — a paseo
// runtime's agents are children of the paseo daemon, which conductor cannot
// wrap (validate rejects the combination).
type IsolationConfig struct {
	// Mode selects the wrapper: user | namespace | container.
	Mode string `yaml:"mode"`
	// User is the low-privilege account for mode: user (sudo -n -u <user>).
	User string `yaml:"user,omitempty"`
	// Container configures mode: container.
	Container *ContainerIsolation `yaml:"container,omitempty"`
	// Limits are cgroup/engine resource caps (namespace mode applies them via
	// systemd-run --user --scope; container mode via engine flags).
	Limits *IsolationLimits `yaml:"limits,omitempty"`
	// Privileged (namespace mode only) is the deliberate opt-in to run the
	// sandboxed process with the daemon's own filesystem view: it skips the
	// default masking of conductor's state/config directories inside the
	// mount namespace. Default false = the agent cannot read the daemon's
	// config, secrets env, store, or audit trail (#36 iso-review H7).
	Privileged bool `yaml:"privileged,omitempty"`
	// AllowRoot (namespace mode only) is the deliberate opt-in to permit
	// namespace isolation when the daemon runs as root (euid 0). Under root,
	// `unshare --user --map-current-user` maps root→root: the sandboxed
	// process keeps real uid 0 and full CAP_SYS_ADMIN over the host, so the
	// user namespace is NOT a privilege boundary. Default false = conductor
	// REFUSES namespace mode as root and points you at a non-root daemon user
	// or mode container (#36 iso-review round 2, item 2). Set true only when
	// you understand the namespace is being used for cleanup/limits, not as a
	// security wall.
	AllowRoot bool `yaml:"allow_root,omitempty"`
	// Network is the egress policy. ABSENT → no restriction for
	// config-authored dispatches (agent-authored dispatches still get the
	// deny-all proxy — deny by default). Present-but-empty → deny-all via
	// conductor's egress proxy (audited). `egress:` patterns → the proxy
	// allows only matching host:port targets. `deny: true` → structurally no
	// network (namespace/container modes).
	Network *IsolationNetwork `yaml:"network,omitempty"`
}

// ContainerIsolation configures isolation mode: container.
type ContainerIsolation struct {
	// Image is the container image the runtime launches in (required). The
	// image must carry the runtime binary the launch expects.
	Image string `yaml:"image"`
	// Engine is docker (default) or podman.
	Engine string `yaml:"engine,omitempty"`
}

// IsolationLimits are the resource caps.
type IsolationLimits struct {
	Memory string `yaml:"memory,omitempty"` // e.g. "2g" (systemd MemoryMax / engine --memory)
	CPU    string `yaml:"cpu,omitempty"`    // e.g. "200%" (CPUQuota) or "2" (--cpus)
	Pids   int    `yaml:"pids,omitempty"`   // TasksMax / --pids-limit
}

// IsolationNetwork is the egress policy for one isolation scope.
type IsolationNetwork struct {
	// Egress allowlists "host", "host:port", or "*.glob:port" targets through
	// conductor's egress proxy. Empty (with the block present) denies all.
	Egress []string `yaml:"egress,omitempty"`
	// Deny cuts the network structurally (namespace --net / --network=none).
	// Mutually exclusive with Egress.
	Deny bool `yaml:"deny,omitempty"`
}

// WorkflowDef is one entry in the `workflows:` map — a named, parameterized
// step list invoked from triggers (or other workflows) via `workflow:`.
type WorkflowDef struct {
	// Extends names another workflows: entry this one inherits from (unset
	// inputs/outputs/gate filled, steps replace if set). See resolveExtends.
	Extends string `yaml:"extends,omitempty"`
	// Description makes the workflow self-describing: with the declared
	// inputs/outputs it's what `conductor schema`, `workflow.list`, and a
	// choosing agent (#36 §11) see about what this does and when to use it.
	Description string               `yaml:"description,omitempty"`
	Inputs      map[string]InputSpec `yaml:"inputs,omitempty"`
	Outputs     map[string]string    `yaml:"outputs,omitempty"` // name -> template over internal step outputs
	Steps       []Step               `yaml:"steps,omitempty"`
	// Gate is the default quality gate (#36 §16) for this workflow's agent
	// steps (a step's own gate wins).
	Gate *GateSpec `yaml:"gate,omitempty"`
}

// InputSpec declares one workflow input.
type InputSpec struct {
	Type     string `yaml:"type,omitempty"` // string | integer | number | boolean | list | map | any
	Required bool   `yaml:"required,omitempty"`
	Default  any    `yaml:"default,omitempty"`
}

// OnSource is one item of a multi-source `on:` list: a bare "conn.event"
// scalar, or a single-key mapping keyed by the event name whose value is a
// per-source block — `filters:`, `policy:`, `hooks:` — scoped to events from
// that source:
//
//	on:
//	  - timer.nightly
//	  - manual
//	  - gh.issue_matched:
//	      filters: { labels_any: [billing] }  # merged over the shared base (per-source wins)
//	      policy:  { reply_to_bots: off }     # innermost policy scope
//	      hooks:   [ {at: start, uses: gh.react, options: {emoji: eyes}} ]  # appended after the shared hooks
//
// Steps stay trigger-level (shared); a per-source block takes nothing else.
type OnSource struct {
	Source  string
	Filters map[string]any
	Policy  *Policy
	Hooks   []Hook
}

// UnmarshalYAML accepts the bare-scalar and event-keyed one-key map forms,
// rejecting multi-key items and unknown per-source block keys.
func (o *OnSource) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&o.Source)
	}
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		var keys []string
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				keys = append(keys, n.Content[i].Value)
			}
		}
		return fmt.Errorf("an `on:` list item is a bare \"conn.event\" or a one-key map `conn.event: {filters, policy, hooks}` — got keys: %s", strings.Join(keys, ", "))
	}
	if err := n.Content[0].Decode(&o.Source); err != nil {
		return err
	}
	val := n.Content[1]
	switch {
	case val.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(val.Content); i += 2 {
			switch k := val.Content[i].Value; k {
			case "filters", "policy", "hooks":
			default:
				return fmt.Errorf("on: %s: unknown per-source key %q — a per-source block takes filters, policy, hooks (steps stay on the trigger)", o.Source, k)
			}
		}
		var block struct {
			Filters map[string]any `yaml:"filters"`
			Policy  *Policy        `yaml:"policy"`
			Hooks   []Hook         `yaml:"hooks"`
		}
		if err := val.Decode(&block); err != nil {
			return err
		}
		o.Filters, o.Policy, o.Hooks = block.Filters, block.Policy, block.Hooks
		return nil
	case val.Kind == yaml.ScalarNode && val.Tag == "!!null":
		return nil // `- conn.event:` with an empty block
	default:
		return fmt.Errorf("on: %s: the per-source value is a block {filters, policy, hooks}", o.Source)
	}
}

// ManualSource is the built-in `on:` source with no connector: a trigger
// listing it is runnable on demand via `conductor run <name>`.
const ManualSource = "manual"

// TriggerSpec is one entry in the `triggers:` list: on/filters/steps/hooks
// plus optional grouping, policy, and source-side options.
type TriggerSpec struct {
	// On selects the inbound event: <connector>.<event>, or the built-in
	// "manual" source. The YAML `on:` also accepts a list (see OnSources).
	On string `yaml:"on,omitempty"`
	// OnSources holds the parsed list form of `on:`. Load expands a
	// multi-source trigger into one internal trigger per source — shared
	// steps/hooks/group, per-source filters merged over the shared base — so
	// everything downstream only ever sees the scalar On.
	OnSources []OnSource `yaml:"-"`
	// FanSources lists every source of the original multi-source trigger
	// (set on each expanded variant). Steps of a fan-in trigger are shared
	// across heterogeneous sources, so their references validate against the
	// UNION of the listed sources' contexts — a field one source publishes
	// and another doesn't is referenced defensively ({{.x | default ""}}).
	FanSources []string `yaml:"-"`
	// Name is an optional variant name (distinguishes dedup/attempt state when
	// several triggers listen to the same event; mirrors legacy action names).
	// Required (and unique) for triggers reachable by `conductor run`, and the
	// handle an `extends:` child references a base by.
	Name string `yaml:"name,omitempty"`
	// Extends names another trigger (by Name) this one inherits from: filters
	// and options deep-merge, steps/hooks replace when set, policy/gate/group
	// fill if unset. Resolved before NormalizeTriggers. See resolveTriggerExtends.
	Extends string `yaml:"extends,omitempty"`
	// Abstract marks a base that exists only to be extended: it never fires and
	// is stripped after resolution (so it needs no `on:`). A `conductor run`
	// target cannot be abstract.
	Abstract bool  `yaml:"abstract,omitempty"`
	Enabled  *bool `yaml:"enabled,omitempty"`
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
	// Gate is the default quality gate (#36 §16) for every agent step of
	// this trigger that doesn't carry its own.
	Gate *GateSpec `yaml:"gate,omitempty"`
	// Callable opts a manual trigger into the inbound invoke surface (#36 §13):
	// `callable: true` makes it reachable via `POST /invoke/<name>`. Unset/false
	// (the default) means an external caller can never fire it — the entry point
	// is deny-by-default at the trigger, on top of the per-token scope.
	Callable *bool `yaml:"callable,omitempty"`
}

// UnmarshalYAML peels a list-valued `on:` into OnSources before the plain
// field decode, so the scalar form stays a plain string everywhere else.
func (t *TriggerSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value != "on" {
				continue
			}
			if v := n.Content[i+1]; v.Kind == yaml.SequenceNode {
				if err := v.Decode(&t.OnSources); err != nil {
					return err
				}
				*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
			}
			break
		}
	}
	type plain TriggerSpec
	// Strict: KnownFields does not reach into custom unmarshalers, so a
	// typo'd trigger key would otherwise drop silently.
	return strictNodeDecode(n, (*plain)(t))
}

// IsEnabled reports whether the trigger is enabled (default true).
func (t TriggerSpec) IsEnabled() bool { return t.Enabled == nil || *t.Enabled }

// Manual reports whether this trigger fires from `conductor run`.
func (t TriggerSpec) Manual() bool { return t.On == ManualSource }

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

// NormalizeTriggers expands multi-source `on:` lists — one internal trigger
// per source, sharing steps/hooks/group, each with the shared base filters
// merged under its per-source block (per-source keys win) — and enforces the
// manual-trigger naming rules. Load runs it before validation, so the rest of
// the system only ever sees scalar-On triggers.
func (c *Config) NormalizeTriggers() error {
	var out []TriggerSpec
	for i, t := range c.Triggers {
		if len(t.OnSources) == 0 {
			out = append(out, t)
			continue
		}
		if t.On != "" {
			return fmt.Errorf("config: triggers[%d]: `on:` is both a scalar and a list", i)
		}
		seen := map[string]bool{}
		fan := make([]string, 0, len(t.OnSources))
		for _, src := range t.OnSources {
			fan = append(fan, src.Source)
		}
		for _, src := range t.OnSources {
			if src.Source == "" {
				return fmt.Errorf("config: triggers[%d]: an `on:` list item is a bare \"conn.event\" or a one-key map `conn.event: {filters, policy, hooks}`", i)
			}
			if seen[src.Source] {
				return fmt.Errorf("config: triggers[%d]: duplicate source %q in the `on:` list", i, src.Source)
			}
			seen[src.Source] = true
			v := t
			v.OnSources = nil
			v.On = src.Source
			v.Filters = mergeFilterMaps(t.Filters, src.Filters)
			// Per-source policy is the innermost scope: merged over the
			// trigger's own block here, so the engine's global → connector →
			// trigger resolution makes it most specific.
			if src.Policy != nil {
				p := MergePolicy(t.Policy, src.Policy)
				v.Policy = &p
			}
			// Per-source hooks append after the shared ones and fire only
			// for events from this source (this expanded trigger).
			if len(src.Hooks) > 0 {
				hooks := make([]Hook, 0, len(t.Hooks)+len(src.Hooks))
				hooks = append(hooks, t.Hooks...)
				hooks = append(hooks, src.Hooks...)
				v.Hooks = hooks
			}
			v.FanSources = fan
			out = append(out, v)
		}
	}
	c.Triggers = out

	// A trigger reachable by `conductor run` needs a unique name to run it by.
	manualAt := map[string]int{}
	for i, t := range c.Triggers {
		if !t.Manual() {
			continue
		}
		if t.Name == "" {
			return fmt.Errorf("config: triggers[%d]: a trigger reachable by `conductor run` (on: manual) requires a name:", i)
		}
		if j, dup := manualAt[t.Name]; dup {
			return fmt.Errorf("config: triggers[%d] and triggers[%d]: manual trigger name %q is not unique — `conductor run` resolves triggers by name", j, i, t.Name)
		}
		manualAt[t.Name] = i
	}
	return nil
}

// mergeFilterMaps overlays a per-source filter block over the shared base;
// a per-source key overrides the base for that source.
func mergeFilterMaps(base, over map[string]any) map[string]any {
	if len(base) == 0 {
		return over
	}
	if len(over) == 0 {
		return base
	}
	m := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range over {
		m[k] = v
	}
	return m
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
	Agent string `yaml:"agent,omitempty"`
	// Model selects WHICH MODEL this step runs on: a named fleet from the
	// top-level `models:` block, an exact model id, a wildcard, an inline list
	// (sugar for `{ any: [...], required: false }`), or the full
	// `{ any, required }` object. Unset → the runtime's `models.default:`, and
	// failing that a BARE LAUNCH (no --model, the runtime's own default). See
	// ModelSpec and docs/design/runtimes-models-packs.md §2.2.
	Model ModelSpec `yaml:"model,omitempty"`
	// Runtime pins WHERE this step runs — a `runtimes:` entry. Unset lets
	// model resolution pick the runtime that offers the chosen model, falling
	// back to the runtime flagged `default: true`.
	Runtime         string         `yaml:"runtime,omitempty"`
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

	// agent-authored plan fields (#36 §11; honored inside plans)
	// EscalateTo: "agent" routes this step back to the authoring agent's
	// session after it runs — a mid-plan check-in (failures always route).
	EscalateTo string `yaml:"escalate_to,omitempty"`
	// Compensate is this step's undo action, run in reverse order with the
	// other committed steps' compensations when a plan fails mid-way.
	Compensate *Step `yaml:"compensate,omitempty"`

	// step-level hooks, scoped to this step
	Hooks []Hook `yaml:"hooks,omitempty"`

	// Gate is the quality gate on this agent step's PROPOSED change
	// (#36 §16): named checks from the top-level checks: map run in the
	// agent's worktree after it finishes; a failure loops back to the agent
	// (bounded), then escalates. Agent-form foreground steps only.
	Gate *GateSpec `yaml:"gate,omitempty"`

	// Team splits this step's prompt across a planner / parallel workers /
	// critic / reconciler (#36 §19). Uses the step's prompt as the task.
	Team *TeamSpec `yaml:"team,omitempty"`

	Backend string `yaml:"backend,omitempty"` // dispatch backend override (carried from legacy)
	Shadow  *bool  `yaml:"shadow,omitempty"`
}

// GateSpec configures one quality gate (#36 §16): which checks run against
// the agent's proposed change and how many revise rounds a failure gets
// before the run escalates. Settable on an agent step, or as a default for
// every agent step of a trigger/workflow (the step's own gate wins).
type GateSpec struct {
	// Run names checks from the top-level `checks:` map, run in order.
	Run []string `yaml:"run"`
	// Require is the pass criterion: "pass" (default — every check must
	// pass). Reserved for future criteria; only "pass" is valid today.
	Require string `yaml:"require,omitempty"`
	// MaxRevisions bounds the fail → agent-revise → re-check loop before the
	// gate escalates (default 3; 0 = no revisions, escalate on first fail).
	MaxRevisions *int `yaml:"max_revisions,omitempty"`
}

// DefaultGateMaxRevisions bounds the gate revise loop when unset.
const DefaultGateMaxRevisions = 3

// TeamSpec is the multi-agent team step (#36 §19): ONE unit of work split
// across agents — a planner decomposes it into subtasks, workers run them in
// parallel isolated worktrees (§15 isolation via their profiles), a critic
// judges each worker's result (through the §16 gate machinery, revise loop
// included), and a reconciler merges. Distinct from for_each/parallel, which
// fan MANY events/items over the same step.
type TeamSpec struct {
	// Planner decomposes the step's prompt into subtasks (an agents: name).
	Planner string `yaml:"planner"`
	// Worker is the profile each subtask dispatches on.
	Worker string `yaml:"worker"`
	// Critic (optional) judges each worker's result: it runs as an implicit
	// gate check (pass: true|false verdict) with the gate's revise loop.
	Critic string `yaml:"critic,omitempty"`
	// Reconcile merges the workers' results (defaults to the planner).
	Reconcile string `yaml:"reconcile,omitempty"`
	// MaxWorkers bounds the subtask fan-out AND the parallelism (default 4).
	MaxWorkers int `yaml:"max_workers,omitempty"`
	// Gate adds explicit checks (from checks:) to every worker, beside the
	// implicit critic check.
	Gate *GateSpec `yaml:"gate,omitempty"`
}

// DefaultTeamMaxWorkers bounds a team's fan-out when unset.
const DefaultTeamMaxWorkers = 4

// MaxWorkersOrDefault returns the team's fan-out bound.
func (ts *TeamSpec) MaxWorkersOrDefault() int {
	if ts != nil && ts.MaxWorkers > 0 {
		return ts.MaxWorkers
	}
	return DefaultTeamMaxWorkers
}

// ReconcilerOrPlanner returns the merging agent (default: the planner).
func (ts *TeamSpec) ReconcilerOrPlanner() string {
	if ts.Reconcile != "" {
		return ts.Reconcile
	}
	return ts.Planner
}

// MaxRevisionsOrDefault returns the gate's revise bound.
func (g *GateSpec) MaxRevisionsOrDefault() int {
	if g != nil && g.MaxRevisions != nil {
		return *g.MaxRevisions
	}
	return DefaultGateMaxRevisions
}

// Form returns the step's form keyword: "agent", "command", "code", "verb",
// "workflow", "parallel", or "" when indeterminate.
func (s Step) Form() string {
	switch {
	case s.Team != nil:
		return "team"
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
	// MaxFanOut caps a config-authored for_each / parallel step's fan-out
	// (#36 §146 F3): the number of items or branches a single step may spawn.
	// A for_each whose list resolves past this — the list can be data-driven
	// and externally influenced — is refused rather than spawning an unbounded
	// number of dispatches. Read at global scope only; default
	// DefaultFlowMaxFanOut. Agent-authored plans carry their own, tighter cap
	// (agent_authored.limits.max_fan_out).
	MaxFanOut *int `yaml:"max_fan_out,omitempty"`
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
	// AgentAuthored governs agent-authored plans (#36 §11): the allowlist,
	// approval gate, sandbox host, identity, and limits an agent-emitted
	// step runs under. Nil at every scope = plans rejected (safe default).
	AgentAuthored *AgentAuthoredPolicy `yaml:"agent_authored,omitempty"`
	// Budget is a hard $/token spend cap over a rolling window (#36 §14).
	// Global scope caps everything; a trigger-level budget caps that
	// workflow; an agent profile's own `budget:` caps that profile. Over-cap
	// dispatches shed like the agent-count budget (recorded, retried when
	// the window frees) and notify.
	Budget *BudgetPolicy `yaml:"budget,omitempty"`
	// Guidance is the scoped house-tone baseline (layer 0) for every agent
	// this scope governs — the successor to the top-level agent_guidance (which
	// now folds into the global scope's Guidance). Because policy cascades
	// global → connector → trigger, the baseline is scopable: a broader scope's
	// guidance stacks UNDER a narrower one by default, and a scope using the
	// `{ replace: … }` form resets the stack from that scope down. The agent
	// profile's own guidance (with its extends: chain) then stacks on top of
	// this resolved baseline. See MergePolicy and (*Engine).agentGuidance.
	Guidance *GuidanceSpec `yaml:"guidance,omitempty"`
}

// BudgetPolicy is one hard spend cap: $ and/or tokens over a rolling window.
type BudgetPolicy struct {
	// Window is the rolling spend window (default 24h).
	Window Duration `yaml:"window,omitempty"`
	// MaxCostUSD caps $ spend in the window (0 = uncapped).
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
	// MaxTokens caps token spend in the window (0 = uncapped; "500k"/"2m"
	// shorthand accepted).
	MaxTokens TokenCount `yaml:"max_tokens,omitempty"`
}

// DefaultBudgetWindow is the rolling window when a budget doesn't set one.
const DefaultBudgetWindow = 24 * time.Hour

// PricingConfig overrides the cost layer's built-in model price table.
type PricingConfig struct {
	// Models maps model-name glob patterns ("claude-*") to $/1M-token prices;
	// checked before the built-in defaults.
	Models map[string]ModelPrice `yaml:"models,omitempty"`
	// Default replaces the built-in fallback for models nothing matches.
	Default *ModelPrice `yaml:"default,omitempty"`
}

// ModelPrice is $ per 1M tokens.
type ModelPrice struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// validatePricing checks the pricing block's shape.
func (c *Config) validatePricing() error {
	if c.Pricing == nil {
		return nil
	}
	check := func(where string, p ModelPrice) error {
		if p.Input < 0 || p.Output < 0 {
			return fmt.Errorf("config: pricing: %s: input/output must be >= 0 ($ per 1M tokens)", where)
		}
		return nil
	}
	for pat, p := range c.Pricing.Models {
		if strings.TrimSpace(pat) == "" {
			return fmt.Errorf("config: pricing.models: empty model pattern")
		}
		if err := check("models."+pat, p); err != nil {
			return err
		}
	}
	if c.Pricing.Default != nil {
		return check("default", *c.Pricing.Default)
	}
	return nil
}

// WindowOrDefault returns the budget's rolling window (default 24h).
func (b *BudgetPolicy) WindowOrDefault() time.Duration {
	if b != nil && b.Window.D() > 0 {
		return b.Window.D()
	}
	return DefaultBudgetWindow
}

// validateBudget checks one budget block's shape.
func validateBudget(where string, b *BudgetPolicy) error {
	if b == nil {
		return nil
	}
	if b.MaxCostUSD < 0 {
		return fmt.Errorf("config: %s: budget.max_cost_usd must be >= 0", where)
	}
	if b.MaxTokens < 0 {
		return fmt.Errorf("config: %s: budget.max_tokens must be >= 0", where)
	}
	if b.MaxCostUSD == 0 && b.MaxTokens == 0 {
		return fmt.Errorf("config: %s: budget needs max_cost_usd and/or max_tokens (an empty budget caps nothing)", where)
	}
	if b.Window.D() < 0 {
		return fmt.Errorf("config: %s: budget.window must be positive", where)
	}
	return nil
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
// agent_authored.host is checked against hosts: only where the full config
// is in reach (Validate passes c.Hosts for the global block).
func validatePolicyBlock(where string, p *Policy) error {
	if p == nil {
		return nil
	}
	if err := validateAgentAuthored(where, p.AgentAuthored, nil); err != nil {
		return err
	}
	if err := validateBudget(where, p.Budget); err != nil {
		return err
	}
	if p.ReplyToBots == nil {
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
		if p.AgentAuthored != nil {
			out.AgentAuthored = p.AgentAuthored
		}
		if p.Budget != nil {
			out.Budget = p.Budget
		}
		// Guidance is the one field that STACKS across scopes instead of
		// most-specific-wins: a scope's parts append under the narrower scope's,
		// so a trigger adds to (rather than erases) the global house tone. The
		// `{ replace: … }` form opts back into most-specific-wins — it resets
		// the accumulation to this scope's own parts. The merged result's
		// Replace flag is not meaningful downstream (the engine reads Parts).
		if p.Guidance != nil {
			if p.Guidance.Replace || out.Guidance == nil {
				out.Guidance = &GuidanceSpec{Parts: append([]string(nil), p.Guidance.Parts...)}
			} else {
				merged := append([]string(nil), out.Guidance.Parts...)
				out.Guidance = &GuidanceSpec{Parts: append(merged, p.Guidance.Parts...)}
			}
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
	if len(c.SecretRefs) > 0 {
		return fmt.Errorf("config: the secrets: block was replaced by vaults: entries and {{ vault \"<name>\" \"<key>\" }} references — auto-migration rewrites it at boot, or run `conductor config migrate`")
	}
	// The notify: block was replaced by conductor.* lifecycle triggers on
	// the connectors model. Legacy configs (integrations:) keep the legacy
	// delivery until they migrate.
	if c.Notify.Configured() && len(c.Integrations) == 0 && c.HasConnectors() {
		return fmt.Errorf("config: the notify: block was replaced by triggers on the conductor.* lifecycle events (on: conductor.escalate, …) — auto-migration rewrites it at boot, or run `conductor config migrate`")
	}
	for name, ref := range c.ConnectorsMap {
		if name == "" {
			return fmt.Errorf("config: connectors: empty connector name")
		}
		if name == ManualSource {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in `on: manual` source)", ManualSource)
		}
		if name == "kv" {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in state store — always available, nothing to configure)", name)
		}
		if name == "sql" {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in SQL verbs — always available; connections live in stores:)", name)
		}
		if name == "conductor" {
			return fmt.Errorf("config: connectors: %q is reserved (conductor's own lifecycle events and verbs — always available, nothing to configure)", name)
		}
		if name == "memory" {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in shared agent memory — configure it via the top-level memory: section)", name)
		}
		if name == "workflow" {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in workflow verbs — always available, nothing to configure)", name)
		}
		if name == "blob" {
			return fmt.Errorf("config: connectors: %q is reserved (the built-in artifact verbs — always available, nothing to configure)", name)
		}
		if err := validateUseRef("connector "+name, ref.Use, ref.legacyType, UseKindConnector); err != nil {
			return err
		}
		if ref.Isolation != nil {
			if err := validateIsolation("connector "+name, ref.Isolation, false); err != nil {
				return err
			}
		}
		for _, s := range ref.AllowSecrets {
			if strings.ContainsAny(s, "*?") {
				return fmt.Errorf("config: connector %q: allow_secrets entries are exact names, no globs (%q)", name, s)
			}
		}
		for _, n := range ref.Network {
			if err := validateEgressTarget("connector "+name+" network", n); err != nil {
				return err
			}
		}
		if err := validatePolicyBlock("connector "+name+" policy", ref.Policy); err != nil {
			return err
		}
	}
	for name, rt := range c.Runtimes {
		if name == "" {
			return fmt.Errorf("config: runtimes: empty runtime name")
		}
		if err := validateUseRef("runtime "+name, rt.Use, rt.legacyType(), UseKindRuntime); err != nil {
			return err
		}
		// `agent:` is the ACP runtime's own field: it names the agent the ACP
		// transport drives. It is meaningless on any other implementation, and
		// `use: acp` without it has nothing to drive.
		u, _ := rt.Resolved()
		if u.IsBuiltin() && u.Name == "acp" && rt.Agent == "" {
			return fmt.Errorf("config: runtime %q: `use: acp` needs `agent:` (the agent the ACP transport drives, e.g. gemini)", name)
		}
		if rt.Agent != "" && !(u.IsBuiltin() && u.Name == "acp") {
			return fmt.Errorf("config: runtime %q: `agent:` applies to `use: acp` only (got use: %s)", name, rt.Use)
		}
		if err := c.checkRemoteHostSupport("runtime", name, rt.Host, rt.BuiltinType(), rt.Agent, rt.Controller().EffectiveTransport()); err != nil {
			return err
		}
		if rt.Isolation != nil {
			if rt.BuiltinType() == "paseo" {
				return fmt.Errorf("config: runtime %q: isolation cannot apply to a paseo runtime (its agents are the paseo daemon's children) — use an acp/cli/opencode/agent-deck runtime, or paseo's own sandboxing", name)
			}
			if err := validateIsolation("runtime "+name, rt.Isolation, rt.Host != ""); err != nil {
				return err
			}
			if err := validateIsolationControlChannel("runtime "+name, rt.Isolation, rt.Controller()); err != nil {
				return err
			}
		}
	}
	if err := c.validateRuntimeDefaults(); err != nil {
		return err
	}
	if err := c.validateChecks(); err != nil {
		return err
	}
	for name, h := range c.Hosts {
		if name == "" {
			return fmt.Errorf("config: hosts: empty host name")
		}
		if h.Host == "" {
			return fmt.Errorf("config: host %q: missing host address", name)
		}
		if err := validateIsolation("host "+name, h.Isolation, true); err != nil {
			return err
		}
	}
	for i, t := range c.Triggers {
		where := fmt.Sprintf("triggers[%d]", i)
		if t.On == "" {
			return fmt.Errorf("config: %s: missing `on:`", where)
		}
		if !t.Manual() {
			conn, event, ok := strings.Cut(t.On, ".")
			if !ok || conn == "" || event == "" {
				return fmt.Errorf("config: %s: `on: %s` must be <connector>.<event> (or the built-in `manual`)", where, t.On)
			}
			if _, okc := c.ConnectorsMap[conn]; !okc && conn != "conductor" {
				// "conductor" is the built-in lifecycle source (always
				// available, like the manual source).
				return fmt.Errorf("config: %s: unknown connector %q in `on: %s` (defined: %s)", where, conn, t.On, c.connectorNames())
			}
		}
		if len(t.Steps) == 0 {
			return fmt.Errorf("config: %s (on: %s): no steps", where, t.On)
		}
		if err := validatePolicyBlock(where+" policy", t.Policy); err != nil {
			return err
		}
		if err := c.validateGate(where, t.Gate); err != nil {
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
		if err := c.validateGate(where, wf.Gate); err != nil {
			return err
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
		s.Uses != "", s.Workflow != "", s.Run != "", s.Team != nil,
		s.Type == "agent" || (s.Type == "" && s.Agent != "" && s.Uses == "" && s.Workflow == "" && s.Team == nil),
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
	if c != nil {
		if err := c.validateStepGate(w, s); err != nil {
			return err
		}
		if err := c.validateTeam(w, s.Team); err != nil {
			return err
		}
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
func (c *Config) vaultNames() string     { return sortedKeys(c.Vaults) }
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
