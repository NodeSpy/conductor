# PR #60 adversarial-review fixes

Every item below was independently found by a review agent AND re-verified in the
code by the maintainer. Fix ALL of them. Each item: file:line, the defect, the fix
direction, and the test to add. Keep the fail-safe/degraded-boot/deny-by-default/
secret invariants intact. `gofmt -l`/`go vet`/`go test ./...` must stay green.

Where a test currently PINS the buggy behavior, CHANGE the test to the correct
behavior — do not preserve the bug (flagged inline).

## CRITICAL

### C1 — dispatch hardcodes trigger index 0
`internal/flow/flow.go:282` `ScopeForTrigger(spec, 0)`. Every lookup path uses the
REAL index (`internal/config/steps.go:24` `ScopeForTrigger(c.Triggers[i], i)`), so an
unnamed list-form trigger past index 0 gets a dispatch-time identity that diverges
from its lookup identity — session reaped immediately, `end_on` never fires,
gate-revise escalates, unnamed triggers cross-wire.
FIX: thread the trigger's real index into `Runner.Run` (its caller `SpecFor` already
parses it out of `act.FlowRef` and discards it) and pass it to `ScopeForTrigger`.
TEST: two unnamed list-form triggers; assert the dispatch-time step identity equals
`WalkSteps`' identity for the trigger at index 1 (not `.../[0]`).

### C2 — list-form `on:` pack trigger crashes load instead of going dormant
`internal/config/pack_sources.go:47` binds via `TriggerSpec.Connector()`, which reads
`t.On` — empty for `on: [a,b]` (peeled into `OnSources`). The connector-scope
machinery is skipped, the generic `github` source is never rebound to the consumer's
instance, and generic validation fails `Load()` with `unknown connector "github"`.
FIX: `bindPackSources` must iterate a trigger's sources from `OnSources` when `t.On`
is empty (list form), applying the same auto-bind / ambiguity / dormancy / `required`
treatment the scalar path gets, and rewrite each.
TEST: armed list-form pack trigger — (a) dormant+warn when the source connector is
absent; (b) rebinds `github`→`gh` when present; (c) ambiguity error with two of a type.

## HIGH

### H1 — pack skill `*.read` widens to full `<conn>.*` (drops the verb restriction)
`internal/config/pack_skill.go` `grantConnector` returns `""` for a globbed-connector
pattern; `boundGrant` then expands `""` to `<declared>.*` for every declared
connector, discarding the verb suffix. `skill.verbs: ["*.read"]` → full read-write.
FIX: only the TRUE bare wildcard (`"*"`, or a `*.*`) expands to `<declared>.*`. A
pattern with a globbed connector half AND a specific verb suffix (`*.read`) expands
per declared connector PRESERVING the suffix → `<declared>.read`. Add a lint warning
if a kept pattern's post-expansion scope is broader than authored.
TEST (CHANGE the existing ones — they pin the bug): `internal/config/pack_skill_test.go`
currently asserts `"*.read" → ""` (grantConnector) and `["*.read"],[github] →
["github.*"]` (boundGrant). Correct them to `["*.read"],[github] → ["github.read"]`,
and keep the bare-`*` case expanding to `github.*`.

### H2 — `requires.connectors` version gate fails OPEN for monorepo tags
A plugin release tag keeps its subdir prefix (`jira-connector/v1.0.0`).
`resolvedConnectorVersion` reports it `known=true`; `checkConductorConstraint`
(`internal/config/pack_version.go:26-29`) then fails `parseSemver` on it and SILENTLY
returns `nil` — an incompatible version loads clean, no warning.
FIX: strip the `<subdir>/` prefix before `parseSemver` (reuse the trim in
`internal/config/version_resolve.go` `bestMatch`); AND change the `parseSemver`-fails
branch for a NON-EMPTY version from silent `nil` to a surfaced warning (keep silent
only for `""`/`dev`).
TEST: `SetConnectorVersions({"my-jira":"jira-connector/v1.0.0"})` + constraint `>=2.0`
→ constraint error (or at minimum a warning), not clean load.

### H3 — `Decision.Runtime` discarded
`internal/engine/models.go` `resolveModel` returns only `d.Model`. Call sites set
`profile.Runtime` from the legacy `Backend` only, so a multi-runtime fleet dispatches
the chosen model on the DEFAULT runtime.
FIX: propagate `d.Runtime` — when the resolver picked a runtime and the step didn't
pin one, set `profile.Runtime` to `d.Runtime` before `controllers.Resolve`.
TEST: two runtimes, a fleet whose winning model only exists on the non-default one,
unpinned step → dispatched on the runtime that offers it.

### H4 — `use: cli` / `use: acp` never apply the resolved model
`internal/controller/cli.go` (0 `Request.Model` refs) and `acp.go` (only
`SessionModel`) drop `spec.Request.Model`; an exact pin launches the tool default.
FIX: wire `spec.Request.Model` into the cli argv (`--model <m>` per tool, mirroring
`agentdeck.go`/`opencode.go`) and the ACP `NewSessionParams`.
TEST: a step pinned to an exact model on a `use: cli` runtime and on a `use: acp`
runtime → assert the model reaches the argv / session params.

### H5 — migration is order-dependent and permanently sticks
`internal/migrate/auto.go:55-94` validates the WHOLE tree after EACH per-file write;
`Config` has no tolerant `agents:` field. If the referencing file sorts before the
definer, the mid-pass reload sees an un-migrated `agents:` block → strict-decode fails
→ abort+restore → the box never auto-migrates.
FIX: transform ALL files to temp first, validate the combined tree ONCE, then commit
all (rename) — or restore all on failure (preserve the fail-safe: never leave a
partially-migrated tree). Do NOT reintroduce a permanent `agents:` field.
TEST: import tree with `conf.d/aaa-triggers.yaml` (reference) + `conf.d/zzz-agents.yaml`
(definition) migrates cleanly regardless of filename order.

### H6 — non-manual trigger `name:` uniqueness unchecked
`internal/config/connectors.go` `NormalizeTriggers` dedups only `Manual()` triggers.
Two `name: review` non-manual triggers collapse to one identity.
FIX: enforce `name:` uniqueness across ALL named triggers (names are identity now).
TEST: two non-manual triggers sharing a `name:` → load error.

### H7 — parallel branches collide identity
`internal/config/steps.go` `walkStep` walks every `Parallel.Branches[bi]` with the
SAME scope and slot restarting at 0 → branch-0 steps in different branches share an
identity (session/outcome/memory).
FIX: namespace each branch by its index (fold `bi` into the scope or slot) so
same-position steps in different branches get distinct identities. Apply the SAME rule
everywhere identity is computed (WalkSteps + any dispatch path from C1).
TEST: two parallel branches, each a bare step → distinct identities; same rendered
session key → distinct binding keys.

### H8 — memory scope key `"global"` collides with the reserved shared scope
`internal/memory/memory.go` `NormalizeScope("global") == NormalizeScope("")` →
`GlobalScope`. An agent-supplied `scope: "global"` (skill/MCP `memory_remember`) lands
in the universally-injected shared bucket → cross-repo/tenant leak on a shared daemon.
FIX: treat `"global"` as RESERVED on the agent-facing write path — reject (or
namespace) an agent-supplied scope equal to `"global"`. Config/engine-authored global
context stays legitimate; only the agent/skill-supplied key is constrained.
TEST: `memory_remember` with `scope:"global"` from the skill path is rejected (or
lands in a distinct namespace, not the shared bucket).

## MEDIUM

### M1 — models.dev catalog unbounded read
`internal/models/catalog.go:236` `io.ReadAll(resp.Body)`. FIX: wrap in
`io.LimitReader` with a sane cap (catalog ~4.5MB → cap ~32MB). TEST: oversized body
→ bounded error, not OOM.

### M2 — no singleflight on cold roster discovery
`internal/models/resolve.go` `roster` calls `lister.List` outside the lock with no
dedup. FIX: singleflight/inflight-map keyed by runtime name so concurrent `parallel:`
branches share one `List`. TEST: N concurrent `Resolve` for one runtime → one `List`.

### M3 — migration drops `budget:` across files
`internal/migrate/agents.go` `moveBudgetToRuntime` only inspects same-file `runtimes:`.
FIX: use the whole-tree gather (like `CollectProfiles`) to find `runtimes:` across
imports; fix the misleading "declares no runtimes" note. TEST: `runtimes:` in main,
`agents.x.budget` in an imported file → budget moved, not dropped.

### M4 — mirrored overlay can't address array/instance triggers
`internal/config/pack_overlay.go` / `stepref.go` match on `TriggerSpec.Name`, which for
an instance is `<addr>#<hash>`. FIX: match overlays on the author-facing base address
(the base name / `source.event`); applying to all instances of that base. TEST:
overlay `packs.x.on.review` against an instance-array `review` trigger applies.

### M5 — nested `x-` keys silently dropped
`internal/config/config.go` re-entrant `strictNodeDecode` runs `dropExtensionKeys` at
each custom-`UnmarshalYAML` boundary (`Step`/`RuntimeConfig`/`TriggerSpec`), so a
nested `x-foo` is stripped instead of erroring. FIX: only strip top-level `x-` at the
TRUE document top level (pass a flag so re-entrant strict decodes don't strip). TEST:
`x-note` on a step / trigger → unknown-field error, not silent drop.

## LOW
- L1 — `internal/flow/capability_test.go` `enforcedIDs` reimplements the gate; rewire it
  to call `RunSkillVerb` so it guards the real path.
- L2 — `internal/config/stepmerge.go` `mergeSources`: error on a non-mapping entry in a
  `<<:` sequence (match yaml.v3) instead of silently `continue`.
- L3/L4 — migration operator notes: fix the false "no step referenced it / dropped"
  note for cross-file profiles (`inlineProfiles` checks only the current file); emit a
  note when a non-mapping `agents:` entry is skipped (`agents.go:163-167`).
- L5 — `internal/controller/affinity.go` `\x1f` key-delimiter: guard/escape it (or
  reject a control byte in a rendered key) so a runtime-pool key can't be misclassified
  as step-scoped.
- L6 — remove dead code: `internal/migrate/agents.go` `rewriteAgentRefs`; the
  `Kind:"step"` rung in `internal/config/identity.go`; add a warn/doc note if a shared
  anchor/base carries an `id:` (rung-2 identity collision, per config-merge #4).

## Also (config-merge #1, promoted — it's a real HIGH)
### H9 — `extends:` appends `command:`/`args:` instead of overriding
`internal/config/stepmerge.go:226` blanket `SequenceNode+SequenceNode → appendSequences`
has no carve-out, so `Step.Command`/`Step.Args` (shell argv) APPEND — a garbled
invocation — while the sibling `extends.go` REPLACES the same `Command []string`
(`extends_test.go:157` "slice replaces"). FIX: carve out argv-type fields
(`command`, `args`) to REPLACE (child wins when non-empty), matching `extends.go`;
lists that are allow-lists (`network`, `skill.verbs`) keep appending. TEST: a step
`extends:`-ing a base with `command:` and setting its own `command:` → child's argv,
not concatenation.

## After
Run `gofmt -l . && go vet ./... && go test ./...` (all green). Commit per severity
tier. Report which findings are fixed, any you disagree with (with reasoning), and the
real gate output.
