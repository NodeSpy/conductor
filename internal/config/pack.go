package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Distributable config packs (issue #53) — Terraform-modules-for-conductor.
//
// A pack is a self-contained, versioned bundle of behavior (agents, workflows,
// policy, disarmed triggers) that anyone can install from a source, parameterize,
// override, and compose. The top-level `packs:` block instantiates them.
//
// The governing rule is DEFINE BEHAVIOR / BIND ENVIRONMENT:
//   - Define-in-pack (shipped, namespaced, overridable): agents, workflows,
//     policy, checks, triggers. Pure behavior. (Agents may opt into memory via
//     their own memory: selector — that's behavior — but the daemon-wide memory
//     BACKEND is not shippable.)
//   - Bind-only (declared in requires:, wired in the instance, NEVER shipped):
//     connectors, secrets, vaults, stores, runtimes, hosts, handoffs, and the
//     memory backend. Anything carrying credentials, endpoints, or infra identity.
//
// This is the security boundary: a pack from a stranger can define prompts,
// policy, and workflows, but cannot smuggle in a credential or point at
// infrastructure. The consumer always owns the environment side.
//
// Two layers keep daemon boot offline and deterministic:
//   - resolve (conductor init): fetch sources into the vendor dir, write the
//     sha-pinned lockfile. The only network step. See pack_resolve.go.
//   - instantiate (every config.Load): read the already-vendored packs, namespace
//     + bind + settings-substitute + arm triggers, merge into the effective
//     Config. Offline. See pack_instantiate.go.
// ---------------------------------------------------------------------------

// PackInstance is one entry in the top-level `packs:` block: a sourced,
// versioned, parameterized instance of a distributable pack. The instance name
// (the map key in `packs:`) becomes the namespace for everything the pack
// defines — `agents.reviewer` in the pack resolves to `<instance>/reviewer`.
//
// The block IS the override surface. Every field is optional; a pack authored
// for the zero-config 80% case (§26) runs with near-nothing bound.
type PackInstance struct {
	// Use names WHERE this pack comes from, in the same one-field form
	// connectors and runtimes use (docs/design/runtimes-models-packs.md §5.1).
	// It is rarely written: the `packs:` KEY IS the reference, so
	// `packs: { pr-review-team: {} }` resolves to the official pack repo
	// (OfficialPacksRepo) with no `use:` line at all. Write `use:` only to
	// point somewhere else — `use: ./packs/house-style`, `use: acme/packs/x`.
	//
	// Source (below) is the older, longer spelling and still wins when both
	// are set; applyPackSourceDefaults lowers Use (and the key) onto it, so
	// resolution and the lockfile have one field to read.
	Use string `yaml:"use,omitempty"`
	// Source locates the pack: a go-getter/Terraform-style
	// `github.com/org/repo//subdir@ref`, an SSH form
	// `git::ssh://git@github.com/…`, or a local path (`./packs/review-kit`,
	// relative to the config file) for vendored/example packs.
	Source string `yaml:"source"`
	// Version is a semver constraint (Terraform/gems-style: ">= 1.2, < 2.0",
	// "~> 1.1", "^1.0", "1.0"). An unpinned git source resolves to the highest
	// tag satisfying it (a monorepo tags components "<subdir>/vX.Y.Z"). A hard
	// @ref in Source overrides it; an empty constraint tracks HEAD. Recorded in
	// the lockfile and used as the default `version:` for a child dependency
	// that omits its own. To pin, append `@<tag|branch|sha>` to Source; the
	// lockfile then records the resolved sha for reproducibility.
	Version string `yaml:"version,omitempty"`
	// Hold, when true, freezes this instance at its currently-locked revision:
	// auto-update (`update.deps: true`) will not re-resolve it. An explicit
	// `conductor init` / `pack update` still moves it. No effect on the first
	// resolve (nothing to hold yet). Mirrors a plugin's `hold:`.
	Hold bool `yaml:"hold,omitempty"`
	// Auth is an OPTIONAL fetch credential for a private source, resolved
	// through the consumer's secrets:/vaults: (redacted, never written into a
	// pack). Absent → the box's ambient `gh`/git auth. Bind-environment: the
	// credential is the consumer's, never the pack's.
	Auth string `yaml:"auth,omitempty"`

	// Preset selects one of the manifest's named `presets:` (a pre-filled
	// bundle of setting values, e.g. a `claude` vs `codex` flavor). Individual
	// Settings below still override the preset.
	Preset string `yaml:"preset,omitempty"`
	// Settings overrides the pack's typed `settings:` defaults, substituted into
	// the pack's templated fields at instantiate time (`${settings.NAME}`).
	Settings map[string]any `yaml:"settings,omitempty"`

	// Connectors/Stores/Secrets/Handoffs are BIND-ONLY (environment): they map
	// a pack `requires:` name to one of the consumer's own globals. A pack never
	// ships these; the consumer always owns them.
	Connectors map[string]string `yaml:"connectors,omitempty"`
	Stores     map[string]string `yaml:"stores,omitempty"`
	Secrets    map[string]string `yaml:"secrets,omitempty"`
	Handoffs   map[string]string `yaml:"handoffs,omitempty"`

	// Policy deep-merges onto the pack's bundled policy (behavior override).
	Policy map[string]any `yaml:"policy,omitempty"`
	// Steps satisfies each of the pack's step roles (what `agents:` used to
	// be): absent → the bundled default, string → bind to one of your own
	// `steps:` templates, map → override (deep-merged onto the bundle). See
	// Binding.
	Steps map[string]Binding `yaml:"steps,omitempty"`
	// Models overrides the pack's named fleets by name — the top rung of the
	// model resolution ladder (docs/design/runtimes-models-packs.md §2.3,
	// §5.3). A string collapses the fleet to one model.
	Models map[string]FleetSpec `yaml:"models,omitempty"`
	// On overrides the pack's triggers by their qualified name, deep-merging
	// onto what the pack ships (§5.3). It is the mirrored-section overlay:
	// the keys are the pack's own trigger addresses.
	On map[string]TriggerArm `yaml:"on,omitempty"`
	// Triggers arms (and optionally overrides) the pack's DISARMED triggers,
	// keyed by trigger name. Arming — enabled:true + a repo scope — is the
	// environment binding that constitutes consent. See TriggerArm.
	Triggers map[string]TriggerArm `yaml:"triggers,omitempty"`

	// Packs instantiates the pack's own pack dependencies (requires.packs),
	// recursively — the identical default/override/bind surface, one level down.
	Packs map[string]PackInstance `yaml:"packs,omitempty"`
}

// applyPackSourceDefaults lowers the `use:` reference — and, failing that, the
// instance KEY — onto Source, so everything downstream (resolve, the trust
// allowlist, the lockfile) keeps reading one field.
//
// Precedence: an explicit Source wins (it is the pre-`use:` spelling and may
// carry a go-getter form `use:` cannot express), then `use:`, then the key.
//
// It deliberately does NOT recurse into pack DEPENDENCIES. A dependency's
// source is declared by its parent's `requires.packs.<alias>.source`, which
// the resolver reads at the point it descends; implying one from the alias up
// front would shadow the author's declaration. The resolver applies the same
// implication for a dependency only after that declaration comes up empty
// (see packDependencySource).
func applyPackSourceDefaults(packs map[string]PackInstance) error {
	for _, name := range sortedPackKeys(packs) {
		inst := packs[name]
		if inst.Source == "" {
			src, err := packDependencySource(name, inst.Use)
			if err != nil {
				return err
			}
			inst.Source = src
		}
		packs[name] = inst
	}
	return nil
}

// packDependencySource resolves one instance's source from its `use:` (or, if
// that is empty too, from its key/alias).
func packDependencySource(name, use string) (string, error) {
	ref := strings.TrimSpace(use)
	if ref == "" {
		ref = name
	}
	src, err := packUseSource(ref)
	if err != nil {
		return "", fmt.Errorf("pack %q: %w", name, err)
	}
	return src, nil
}

// packUseSource turns a pack `use:` reference into the `source:` string the
// resolver understands. A local path passes through; anything else resolves
// through the shared use: search path, with a bare name landing in the
// official pack repo.
func packUseSource(ref string) (string, error) {
	u, err := ParseUse(UseKindPack, ref)
	if err != nil {
		return "", err
	}
	switch u.Origin {
	case OriginLocal:
		return u.Path, nil
	case OriginBuiltin:
		// There are no builtin packs, so ParseUse cannot produce this; guard
		// rather than emit an empty source if that ever changes.
		return "", fmt.Errorf("use: %q resolves to a builtin, which is not a pack", ref)
	}
	src := u.Host + "/" + u.Repo
	if u.Component != "" {
		src += "//" + u.Component
	}
	if u.Version != "" {
		src += "@" + u.Version
	}
	return src, nil
}

// Binding is the polymorphic satisfy-a-resource value (§4): its YAML shape
// selects the strategy.
//
//	absent  -> the pack's bundled default
//	string  -> BIND: swap in one of your own existing globals entirely
//	map     -> OVERRIDE: keep the bundle, deep-merge changes onto it
type Binding struct {
	// Bind, when non-empty, names a consumer global to substitute for the
	// pack's bundled resource.
	Bind string
	// Override, when non-nil, is deep-merged onto the pack's bundled resource.
	Override map[string]any
}

// UnmarshalYAML accepts the string (bind) and map (override) forms.
func (b *Binding) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Decode(&b.Bind)
	case yaml.MappingNode:
		b.Override = map[string]any{}
		return n.Decode(&b.Override)
	default:
		return fmt.Errorf("a pack binding is a string (bind to a global) or a map (override the bundle)")
	}
}

// IsBind reports the bind form (a global name was supplied).
func (b Binding) IsBind() bool { return b.Bind != "" }

// IsOverride reports the override form (a deep-merge map was supplied).
func (b Binding) IsOverride() bool { return b.Override != nil }

// TriggerArm arms and optionally scopes a pack's disarmed trigger. A pack ships
// its triggers so the consumer never rebuilds the event mapping / gates / steps,
// but they arrive DISARMED — disabled, with no repos. Arming is the two
// environment-only things: enabled:true AND a repo scope. A trigger with no repo
// scope has nothing to match, so even an accidental enabled:true fires nothing —
// the binding you must do IS the consent (§7).
type TriggerArm struct {
	// Enabled arms the trigger. Absent/false leaves the shipped trigger inert.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Repos scopes the trigger to the consumer's repositories (the consent). An
	// armed trigger with no repos matches nothing.
	Repos []string `yaml:"repos,omitempty"`
	// Filters deep-merges onto the shipped trigger's filters (behavior override).
	Filters map[string]any `yaml:"filters,omitempty"`
	// Policy deep-merges onto the shipped trigger's policy (behavior override).
	Policy map[string]any `yaml:"policy,omitempty"`
}

// IsArmed reports whether the consumer armed this trigger (enabled:true).
func (t TriggerArm) IsArmed() bool { return t.Enabled != nil && *t.Enabled }

// ---------------------------------------------------------------------------
// Pack manifest (conductor-pack.yaml) — the pack's own definition.
// ---------------------------------------------------------------------------

// PackManifestFile is the conventional manifest filename inside a pack source.
const PackManifestFile = "conductor-pack.yaml"

// PackManifest is a pack's self-contained definition: identity/discovery/compat
// metadata (`pack:`), typed settings + presets, the public `exports:` surface,
// and the bundled behavior (agents/workflows/policy/checks/triggers).
//
// The bind-only sections (connectors/secrets/vaults/stores/runtimes/hosts) are
// FORBIDDEN in a manifest and rejected at install with a precise error — the
// security boundary. They are captured here (as raw nodes) only so the rejection
// names them rather than surfacing a generic unknown-field error.
type PackManifest struct {
	Pack     PackMeta                  `yaml:"pack"`
	Settings map[string]SettingSpec    `yaml:"settings,omitempty"`
	Presets  map[string]map[string]any `yaml:"presets,omitempty"`
	Exports  PackExports               `yaml:"exports,omitempty"`

	// Define-in-pack (behavior) — shipped, namespaced, overridable. (Agents may
	// still opt INTO memory via their own `memory:` selector — that's behavior;
	// the daemon-wide memory BACKEND below is not shippable.)
	Steps     map[string]Step        `yaml:"steps,omitempty"`
	Models    map[string]FleetSpec   `yaml:"models,omitempty"`
	Workflows map[string]WorkflowDef `yaml:"workflows,omitempty"`
	Triggers  TriggerList            `yaml:"triggers,omitempty"` // shipped DISARMED
	Policy    *Policy                `yaml:"policy,omitempty"`
	Checks    map[string]Step        `yaml:"checks,omitempty"`

	// Bind-only sections — FORBIDDEN here. Captured as raw nodes so an offending
	// pack is rejected by name (see (*PackManifest).checkNoEnvironment). The
	// `memory:` block selects the daemon-wide memory backend (a `stores:` entry
	// or a filesystem dir) — infra identity, so it is bind-only too.
	Connectors map[string]yaml.Node `yaml:"connectors,omitempty"`
	Secrets    map[string]yaml.Node `yaml:"secrets,omitempty"`
	Vaults     map[string]yaml.Node `yaml:"vaults,omitempty"`
	StoresRaw  map[string]yaml.Node `yaml:"stores,omitempty"`
	Runtimes   map[string]yaml.Node `yaml:"runtimes,omitempty"`
	Hosts      map[string]yaml.Node `yaml:"hosts,omitempty"`
	Memory     yaml.Node            `yaml:"memory,omitempty"`
}

// PackMeta is the manifest's identity/discovery/compatibility header.
type PackMeta struct {
	// Name is the pack's CANONICAL identity, independent of the hosting URL, so
	// it can move repos without breaking references or the lockfile. Distinct
	// from the instance name (the local namespace) and the source URL (location).
	Name        string       `yaml:"name"`
	Version     string       `yaml:"version"`
	Description string       `yaml:"description,omitempty"`
	Tags        []string     `yaml:"tags,omitempty"`
	Homepage    string       `yaml:"homepage,omitempty"`
	License     string       `yaml:"license,omitempty"`
	Maintainers []string     `yaml:"maintainers,omitempty"`
	Requires    PackRequires `yaml:"requires,omitempty"`
	// Deprecated, when non-empty, makes the daemon warn on load (§22) without
	// forcing an update — the message is surfaced verbatim.
	Deprecated string `yaml:"deprecated,omitempty"`
}

// PackRequires is the pack's interface (§5): the resources it needs. It does
// double duty — the "sockets you plug in" list AND the allowlist of global
// names the pack may reach. Anything NOT in requires: is pack-local.
//
// There is deliberately no `roles:` here. It was vestigial after the agents
// removal: "which model/agent fills this role" is answered by fleets + the
// runtime roster + the mirrored overlay, and "what capabilities must a
// binding satisfy" is answered by requires.connectors, which bounds every
// grant the pack can make (docs/design/skill-capability-and-pack-interface.md
// §C/§E).
type PackRequires struct {
	// Conductor is the daemon-version constraint (§16) — REQUIRED for the
	// auto-updating fleet. Loading a pack outside its range is a named error,
	// never a crash. Mirrors Terraform required_version.
	Conductor string `yaml:"conductor,omitempty"`
	// Connectors is the pack's connector interface AND its capability
	// boundary: version-aware (`{ jira: ">=2.0" }`), with a bare list as
	// sugar for "any version". A pack's skill.verbs may name no connector
	// outside it (§C), and each constraint is gated at instantiate against
	// the consumer's resolved connector (§D).
	Connectors ConnectorReqs `yaml:"connectors,omitempty"`

	Stores   []string              `yaml:"stores,omitempty"`
	Handoffs []string              `yaml:"handoffs,omitempty"`
	Secrets  map[string]SecretReq  `yaml:"secrets,omitempty"`
	Packs    map[string]PackDepReq `yaml:"packs,omitempty"`
	// Sources names the connector SOURCE TYPES this pack's triggers bind to
	// (github, gitlab, pagerduty…). Scope for each lives on the CONSUMER's
	// connector of that type, never on the pack
	// (docs/design/runtimes-models-packs.md §5.2). A source the consumer has
	// no connector for leaves its triggers DORMANT with a load-time notice,
	// unless the author marks it required.
	Sources map[string]SourceReq `yaml:"sources,omitempty"`
}

// ConnectorNames lists the connectors the pack declares, sorted. This is
// the pack's capability boundary: its `skill.verbs` may name no connector
// outside it (docs/design/skill-capability-and-pack-interface.md §C).
func (r PackRequires) ConnectorNames() []string { return r.Connectors.Names() }

// SourceReq declares one connector source type a pack's triggers bind to.
// Required turns the missing-connector notice into a hard error — for a pack
// that is meaningless without that source.
type SourceReq struct {
	Desc     string `yaml:"desc,omitempty"`
	Required bool   `yaml:"required,omitempty"`
}

// SecretReq documents a secret the pack needs (name/role, no value).
type SecretReq struct {
	Desc string `yaml:"desc,omitempty"`
}

// PackDepReq declares a pack dependency (§10), satisfied by a nested `packs:`
// instance block.
type PackDepReq struct {
	Source  string `yaml:"source"`
	Version string `yaml:"version,omitempty"`
}

// SettingSpec is one typed pack setting with a default (§6).
type SettingSpec struct {
	Type    string `yaml:"type,omitempty"` // string | integer | number | boolean
	Default any    `yaml:"default,omitempty"`
	Desc    string `yaml:"desc,omitempty"`
}

// PackExports declares the pack's public surface (§8) — the workflows/agents
// meant to be referenced externally — so authors can refactor internals without
// breaking consumers. Empty → everything is addressable.
type PackExports struct {
	Workflows []string `yaml:"workflows,omitempty"`
	Steps     []string `yaml:"steps,omitempty"`
}

// checkNoEnvironment enforces the security boundary: a manifest that ships any
// bind-only (environment) section is rejected by name. Packs ship no connectors
// and no secrets — enforced at install (§13).
func (m *PackManifest) checkNoEnvironment() error {
	var shipped []string
	if len(m.Connectors) > 0 {
		shipped = append(shipped, "connectors")
	}
	if len(m.Secrets) > 0 {
		shipped = append(shipped, "secrets")
	}
	if len(m.Vaults) > 0 {
		shipped = append(shipped, "vaults")
	}
	if len(m.StoresRaw) > 0 {
		shipped = append(shipped, "stores")
	}
	if len(m.Runtimes) > 0 {
		shipped = append(shipped, "runtimes")
	}
	if len(m.Hosts) > 0 {
		shipped = append(shipped, "hosts")
	}
	if m.Memory.Kind != 0 {
		shipped = append(shipped, "memory")
	}
	if len(shipped) > 0 {
		return fmt.Errorf("pack %q ships bind-only section(s) %v: a pack must not carry connectors, secrets, vaults, stores, runtimes, hosts, or a memory backend (define behavior / bind environment) — declare them in requires: and let the consumer bind them; agents may still opt into memory via their own memory: selector", m.Pack.Name, shipped)
	}
	return nil
}
