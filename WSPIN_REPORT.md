# Workspace pin / retire scratch — report

Branch `feat/workspace-pin-retire-scratch`, based on `main` @ `8ab0fcd` (v0.10.0).

Implementation commit: **`1424528`** — "Retire conductor-scratch; per-run
workspaces and a `workspace: { pin }`". (This report is committed on top.)

---

## Verification

All commands run from the repo root, `CGO_ENABLED=0` default, Go 1.26.3.

### `gofmt -l` on touched files

```
$ gofmt -l $(git diff --name-only HEAD~1 -- '*.go')
$ echo $?
0
```

Empty output — clean. (Repo-wide `gofmt -l internal cmd pkg test` is also empty.)

### `go build ./...`

```
$ go build ./...
exit=0
```

No output, exit 0.

### `go vet ./...`

```
$ go vet ./...
exit=0
```

No output, exit 0.

### `go test -race ./...`

```
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	(cached)
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]

EXIT=0
FAIL lines: 0
ok lines: 40
```

40 packages pass, 0 failures, no race or leak reports. `go test ./...` (non-race)
is likewise clean.

### Retired-symbol grep

```
$ grep -rn "scratchWorkspaceTitle\|resolveScratchWorkspace\|createScratchWorkspace\
\|findWorkspaceByTitle\|cullScratch\|findScratch\|ScratchWorkspace\|scratchWS\
\|worktreeWorkspaceMap\|worktreeWorkspaces\|agentWorktreeWorkspace" .
```

No hits in any `.go` file. The only remaining `conductor-scratch` strings are
prose: `docs/design/workspace-pin.md` (the retirement note itself),
`docs/design/paseo-runtime-plugin.md` (a dated historical reference), and two
test comments explaining what the new behavior replaced. No test was deleted to
make the grep pass — each was rewritten against the new semantics (see below).

---

## Files changed

36 files, +1170/−331.

**Config surface**
- `internal/config/workspace.go` (new) — the polymorphic `Workspace` type.
- `internal/config/workspace_test.go` (new) — decode/marshal/merge/validation.
- `internal/config/connectors.go` — `Step.Workspace` retyped `string` → `Workspace`.
- `internal/config/steps.go` — `Workspace.Validate` + new `validateWorkspacePin`.

**Dispatch**
- `internal/dispatch/paseo.go` — scratch machinery removed; `runWorkspace`,
  `makeRunDir`, `runWorkspaceSlug`, `isEphemeralRunWorkspace`,
  `pinnedWorkspace`, `reclaimableWorkspaceMap`, `agentOwnedWorkspace`;
  `workspaceMode` reads `.Isolation`; `Archive` and `effectiveStrategy` docs.
- `internal/dispatch/dispatch.go` — `ScratchWorkspace` hook → `RunWorkspace`;
  `scratchWS` field dropped; `Request.Workspace` documented as the pin.
- `internal/dispatch/reaper.go` — `cullScratch`/`findScratch` removed;
  `worktreeWorkspaces` → `reclaimableWorkspaces`.
- `internal/dispatch/remote.go` — `remoteMkdirAll` (remote run-dir creation).
- `internal/dispatch/backend.go`, `cli_backend.go` — stale doc comments.

**Tests** — `archive_test.go`, `dispatch_test.go`, `paseo_more_test.go`,
`reaper_more_test.go`, `reaper_backend_test.go`, `worktree_test.go`,
`rpc_backend_integration_test.go`, plus mechanical retype in
`internal/config/*_test.go`, `internal/controller/controller_test.go`,
`internal/migrate/*_test.go`, `internal/dispatch/provision_test.go`.

**Docs / examples / e2e** — `docs/design/workspace-pin.md` (new),
`docs/design/paseo-runtime-plugin.md`, `docs/wiki/Steps.md`,
`config.example.yaml`, `test/e2e/services/fakepaseo/main.go`,
`test/e2e/docker-compose.yml`, `test/e2e/run.sh`.

---

## The two `workspace:` flow map

There are two unrelated config keys named `workspace:`, in different layers.
They never meet; conflating them is the trap here.

### Path A — connectors-model step (`workspace:` = isolation mode, now + pin)

```
YAML  steps: { workspace: worktree }
              or { workspace: { isolation: local, pin: triage } }
  ↓
config.Step.Workspace                       internal/config/connectors.go:912
  type config.Workspace{Isolation, Pin}     internal/config/workspace.go
  ↓ validated
Workspace.Validate           isolation ∈ {local, worktree, ""}   steps.go
validateWorkspacePin         pin ⊥ explicit checkout-pr/branch-off
  ↓ carried on the dispatch
dispatch.Request.Step (config.Step)         every Request construction site
  ↓ read in internal/dispatch/paseo.go
  ├─ .Isolation → workspaceMode(req)  → `--new-workspace <local|worktree>`
  └─ .Pin       → pinnedWorkspace(req) → `--workspace <name>`   (strat == "none")
```

### Path B — legacy github rule (`workspace:` = a workspace id/path)

```
YAML  rules: [{ workspace: wks_x }]
  ↓
github.Rule.Workspace (string)              internal/integrations/github/github.go:123
  ↓
MergeRule: out.Workspace = r.Workspace      github.go:433, 445-446
  ↓
(nothing)
```

**Finding: Path B is inert on today's code path.** `MergeRule` overlays the
field and *nothing downstream reads the result* — `grep` confirms no non-test
code anywhere writes `dispatch.Request.Workspace`. So the `req.Workspace != ""`
branch at the old `paseo.go:154` was plumbing with no producer; it existed
waiting for one. `workspace: { pin: … }` is now that producer, arriving via
`Step.Workspace.Pin` rather than by populating `Request.Workspace` at each of
the four construction sites.

That is a deliberate deviation from the brief's "wire the resolved pin … into
`dispatch.Request.Workspace`": `req.Step` already carries the whole
`config.Step` to every agent dispatch, so reading the pin there covers all four
sites (engine single-action, engine workflow step, flow runner, flow team)
uniformly and cannot drift between them. `Request.Workspace` is kept as the
higher-precedence *caller* override — a caller that resolved a workspace for
one specific dispatch beats the step's standing config. Both are resolved in
one place, `pinnedWorkspace(req)`, and the precedence is tested
(`TestPaseoNoneRequestPinBeatsStepPin`).

The legacy path is untouched: `Rule.Workspace` is still a plain `string`, still
merged the same way. The config migration (`internal/migrate/agents.go`
`behaviorKeys`) still moves a `workspace:` key verbatim off a legacy `agents:`
profile onto the step template — a **raw-node** pass, so the string→object type
change does not affect it, and a bare string still lands as an isolation mode.
`internal/migrate` tests pass unchanged apart from the mechanical retype of
their assertions.

---

## How the reclaim invariant is guaranteed

### The constraint that shaped the design

The brief specified "no `--workspace` — paseo creates the agent its OWN per-run
workspace," *and* that those workspaces must be reclaimed (marked
NON-NEGOTIABLE). **Those two cannot both hold on paseo 0.8.0**, which I verified
against the live daemon on this box:

- `paseo ls --json` returns `id, shortId, name, provider, thinking, status,
  cwd, created` — **no workspace id**.
- `paseo inspect <id> --json` returns `Cwd`, `Worktree: null` — **no workspace
  id** either.
- An implicitly-created per-run workspace is `isolation: local`, `cwd: $HOME`,
  named after the agent title. The live daemon has several
  (e.g. `wks_83685667ff10735e local "Add LAN and Tailscale…" /home/daniel`)
  alongside the real `wks_673ccb4b82b47f73 local conductor-scratch /home/daniel`.

So the *only* join key from a finished agent back to its workspace is the cwd,
and implicit workspaces all share `$HOME` — an ambiguous key exactly where it
must not be. "Archive this finished run's workspace" could archive a live one.

**Decision: conductor owns the creation.** The reclaim requirement wins over the
literal argv shape. The observable outcome the brief asked for is unchanged —
each default `checkout: none` run gets its own workspace, nothing is shared —
and it is now provably reclaimed. Flagged here and in
`docs/design/workspace-pin.md`.

### The mechanism

1. **Creation** — `runWorkspace` (`internal/dispatch/paseo.go`):
   `os.MkdirAll ~/.conductor/runs/<kind>-<key>-<6 random bytes>` (or
   `mkdir -p` over SSH via `remoteMkdirAll` for a remote runtime — real
   `paseo workspace create --path` errors `WORKSPACE_CREATE_FAILED "Directory
   not found"` on a missing dir, which I verified directly), then
   `paseo workspace create --isolation local --path <dir> --title
   conductor-run-<slug>`, then `--workspace <id>` on the run.
   - The **unique directory** is what makes the cwd→workspace join unambiguous.
   - The **`conductor-run-` title prefix** is the ownership marker
     (`isEphemeralRunWorkspace`) — it can never match a workspace you created
     or a pinned one.

2. **Primary reclaim** — `Dispatcher.Archive(agentID)`
   (`internal/dispatch/paseo.go`). It was already the `archive_when_done` path
   (`internal/engine/engine.go:539`, `internal/engine/steps.go:268`,
   `internal/flow/flow.go:1597`, `internal/controller/affinity.go:828`). It
   maps the agent's cwd through `reclaimableWorkspaceMap`, which now keeps
   `isolation == "worktree"` **or** `isEphemeralRunWorkspace`, and archives the
   WORKSPACE — reclaiming the directory and the agent together. Previously this
   map was worktree-only, so a checkout:none agent archived only itself; that is
   the change that makes the invariant hold.

3. **Reaper backstop** — `Reaper.reap` (`internal/dispatch/reaper.go`) for a run
   that crashed or was abandoned before reaching `Archive`. Its
   `worktreeWorkspaces` became `reclaimableWorkspaces` over the same shared
   `reclaimableWorkspaceMap`, so both callers agree by construction.

4. **A pinned workspace matches neither branch** — only its agent is archived,
   and the workspace persists for the next run.

### Deliberate gap, stated plainly

The reaper does **not** sweep `conductor-run-*` workspaces that have no agent.
An ephemeral workspace lives exactly as long as its agent, so "no agent" means
either it was already reclaimed with its agent, or it is being created right now
for an agent that has not launched yet — and archiving that one would pull the
directory out from under a starting run. The agent-anchored walk has no such
window. Residual exposure: a conductor killed between `workspace create` and
`paseo run` leaves one empty workspace + directory. This is bounded (one per
hard kill in a sub-second window), versus the unbounded per-dispatch leak the
change removes. Documented in a comment in `reaper.go` and asserted by
`TestReapLeavesUnclaimedRunWorkspaceAlone`.

Note also that the per-run directories under `~/.conductor/runs/` are not
themselves deleted when the workspace is archived — same as the pre-existing
`~/.conductor/checkouts/` cache. Only the paseo *workspace* (the pile-up the
brief targets) is reclaimed.

### Tests proving it

| Test | Proves |
|---|---|
| `TestArchiveReclaimsEphemeralRunWorkspace` | an unpinned checkout:none workspace **is** archived on finish |
| `TestArchivePinnedWorkspaceSurvives` | a **pinned** workspace (and a base checkout) is **not** — agent only |
| `TestCheckoutNoneDispatchThenArchiveLeavesNothingBehind` | full round trip: real `Dispatch` creates the workspace, real `Archive` reclaims **that same** workspace |
| `TestReclaimableWorkspaceMap` | worktree + `conductor-run-*` in, pinned/base/user-made out |
| `TestReapScenario`, `TestReapThroughInjectedBackend` | reaper backstop archives `wks_run`, spares `wks_pin` (CLI and injected-Backend paths) |
| `TestReapLeavesUnclaimedRunWorkspaceAlone` | no agentless sweep (no race with a starting dispatch) |
| `TestPaseoNoneGetsItsOwnRunWorkspace` | consecutive dispatches get **different** workspaces (no sharing) |
| `TestPaseoNonePinSkipsRunWorkspace` | a pin emits `--workspace <name>` and creates no ephemeral |
| `TestPaseoNoneRequestPinBeatsStepPin` | `Request.Workspace` > `Step.Workspace.Pin` |
| `TestCheckoutNoneWorkspacePerRun` | interactive hand-off takes the same path, own workspace |
| `TestRunWorkspaceCreation` / `…SlugIsUniqueAndTraceable` | `--isolation local`, title prefix, dir exists, one dir per run |
| `TestWorkspace*` (config) | string form byte-identical; object form; strict rejects; marshal round-trip; `extends:` merge |
| `TestStepWorkspacePinValidation` | pin ⊥ explicit repo checkout |

---

## Pin on a repo-checkout step — the decision

**An explicit `checkout: checkout-pr` or `branch-off` alongside a pin is a
config error** (`validateWorkspacePin`, `internal/config/steps.go`).

Rationale: a pin is one workspace reused across runs; a repo checkout gives each
dispatch a fresh isolated worktree, which is the isolation that lets two PRs be
worked at once. Both cannot be honoured. Silently ignoring the pin leaves an
operator believing their agent has a durable home; silently ignoring the
checkout would run every PR's work in one shared directory. Refusing says so.

**A step that leaves `checkout:` unset is allowed to carry a pin.** Its strategy
comes from the trigger (`effectiveStrategy` → PR ⇒ `checkout-pr`, repo ⇒
`branch-off`, else `none`), so the pin applies on runs with no repo context and
is ignored on runs that get a worktree. The same step legitimately serves both,
so this is documented (wiki + design note) rather than refused.

**Rejected alternative:** making the pin the BASE workspace to worktree from
(matching `Request.Workspace`'s older "base workspace id/path to worktree from"
doc). It conflates two different things, and it would have changed the
checkout-resolution path — the most failure-prone code in the dispatcher, with
the clone cache and project remapping — for a case nobody asked for. Keeping
checkout-pr/branch-off byte-for-byte unchanged is also what keeps the existing
e2e green.

---

## Other design choices made

- **`Request.Workspace` precedence** (caller override beats step config) — see
  the flow map above. Not specified in the brief; chosen as least-surprising and
  tested.
- **Ephemeral workspaces live at `~/.conductor/runs/<slug>`**, not `$HOME`. The
  old scratch sat at `$HOME`; a dedicated empty directory per run is what makes
  the reclaim join unambiguous, and is better isolation besides (agents still
  have `$HOME` in env, only cwd differs). Remote runtimes use
  `.conductor/runs/<slug>` relative to the host's `cwd:`, mirroring the old
  scratch's `"."`.
- **Slug = trigger kind + key + 6 random bytes.** Traceable back to the
  dispatch in `paseo workspace ls`, unique even for two dispatches off one
  trigger. `crypto/rand` failure falls back to `UnixNano` rather than reusing a
  name, because a collision means two live runs sharing a directory.
- **`workspace:` with no value / `null` / `""` reads as unset**, not an error.
  Verified empirically that yaml.v3 bypasses a custom unmarshaler for a null
  node, so an error branch there would have been unreachable; "unset" matches
  every other scalar `Step` field. Asserted in `TestWorkspaceStringFormUnchanged`.
- **`Workspace` is a value struct, not a pointer.** `mergeStruct`
  (`internal/config/extends.go`) reads `IsZero` for the scalar case, which works
  for structs, so `extends:` inherits an unset workspace and a child that sets
  one wins whole. Tested (`TestWorkspaceMergePolicy`).
- **`FAKE_PASEO_FAIL_WORKSPACE` scoped to worktree creates** in the e2e fake.
  It previously failed *any* `workspace create`; since checkout:none now creates
  one too, leaving it unscoped would have broken every triage scenario running
  under that daemon instead of just J2's worktree failure.

---

## Scope notes / what was not done

- **The e2e suite was not executed** — it needs Docker Compose and network
  services this environment does not provide. The fake paseo was updated to
  handle `--isolation local` creates (including mirroring real paseo's
  "directory must exist" error), the J2 forced-failure was scoped to worktree
  creates, and stale "scratch" wording was corrected — but those changes are
  **unverified by an actual e2e run**. Nothing else in `test/e2e/` referenced
  the scratch workspace.
- **No new e2e scenario for `pin:`** was added. Writing one I cannot run risks
  committing a broken scenario; the pin is covered by unit tests at both the
  config and dispatch layers, and by a real (loaded and validated) object-form
  usage in `config.example.yaml`.
- **Example configs validated:** `config.example.yaml` → `ok: 5 connector(s),
  10 trigger(s), 1 workflow(s)` (with env vars stubbed), and it now contains a
  genuine `workspace: { isolation: local, pin: conductor-triage }` that the real
  loader parses. `config.starter.yaml` → `ok: 1 connector(s), 3 trigger(s)`.
  The four behavioral `test/e2e/config/*.yaml` get past parse+validate and fail
  only at `blob: mkdir /data: permission denied` (a container path).
  `config.example.legacy.yaml`, `test/e2e/config/legacy-migrate.yaml` and
  `unmappable.yaml` fail on `field agents not found` — these are **migration
  inputs**, and I confirmed by `git stash` that they fail identically on the
  base commit. `bad-filter.yaml` is an intentional-failure fixture.
- **Live-daemon probing side effects, all cleaned up:** two probe workspaces and
  one probe agent were created against the running paseo daemon to establish the
  facts above, and all three were archived. `~/.config/conductor/` was not
  touched. Nothing was pushed, tagged, or PR'd.
