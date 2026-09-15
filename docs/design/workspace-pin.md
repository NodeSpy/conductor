# Workspace pinning, and retiring `conductor-scratch`

Status: implemented. Supersedes the shared-scratch behavior described in
`paseo-runtime-plugin.md` §2.

## The problem

A `checkout: none` dispatch — the review `assess` step, synthetic-source
triage, a cron agent with no repo — has no PR or branch to build a worktree
from, so it needs a plain workspace to run in. Until now every such dispatch
was pinned into ONE shared paseo workspace titled `conductor-scratch`, created
on demand at `$HOME` and culled by the reaper when it went idle.

Two things were wrong with that.

**One workspace, many agents.** Every triage agent across every repo ran in the
same directory, so one agent's leftovers were the next one's starting state,
and a running agent could have its workspace archived out from under it by a
cull racing an in-flight launch. The cull had a cwd check to avoid exactly
that, which is itself a sign the shape was wrong.

**No way to say "keep this one."** A long-lived agent that legitimately wants a
durable home — a chat or triage agent that should find its notes where it left
them — had no way to ask for one. `dispatch.Request.Workspace` plumbed a
`--workspace` pin all the way to the paseo argv, but nothing in the config
surface produced it, so the field had no producer outside tests.

## The change

**1. `conductor-scratch` is gone.** `scratchWorkspaceTitle`,
`resolveScratchWorkspace`, `createScratchWorkspace`, `findWorkspaceByTitle`,
the `Dispatcher.ScratchWorkspace` hook, the memoized `scratchWS`, and the
reaper's `cullScratch`/`findScratch` are all removed.

**2. `workspace:` is polymorphic.** The step field keeps its string form and
gains an object form:

```yaml
workspace: worktree                          # isolation mode (unchanged)
workspace: { isolation: local, pin: triage }  # mode + a workspace to reuse
```

`pin:` names a paseo workspace the step always runs in — created on first use,
reused by every later run. It feeds the existing `Request.Workspace` →
`--workspace <name>` path (`pinnedWorkspace`), so a caller-set
`Request.Workspace` still wins over the step's config. Decoding is strict:
unknown keys are rejected, a present-but-blank `pin:` is rejected, and
`isolation` must be `local | worktree`. `MarshalYAML` re-emits a bare string
when there is no pin, so round-tripping an existing config changes nothing.

**3. An un-pinned `checkout: none` run gets its OWN workspace, and gets it
back.** This is the part that needed care.

## The reclaim invariant

Removing the shared scratch means each default `checkout: none` run now needs a
throwaway workspace, and a throwaway workspace that is never reclaimed is the
pile-up this project has been burned by before (hundreds of leaked "empty"
workspaces). So: **every ephemeral per-run workspace must be archived when its
agent finishes.**

The obvious implementation — emit no `--workspace` and let paseo create one
implicitly — cannot satisfy that, for a concrete reason. paseo (0.8.0) exposes
**no agent → workspace handle**: neither `paseo ls --json` nor `paseo inspect
--json` reports a workspace id, only a `Cwd`. The only join key from a finished
agent back to its workspace is the working directory. And an implicitly-created
workspace lands on `$HOME`, which every other implicit workspace also uses — so
the join is ambiguous exactly where it must not be, and "archive this finished
run's workspace" could archive a live one instead.

So conductor creates the workspace itself:

- `runWorkspace` makes `~/.conductor/runs/<kind>-<key>-<random>` (`mkdir` over
  SSH for a remote runtime — `paseo workspace create --path` errors on a
  missing directory), then `paseo workspace create --isolation local --path
  <that dir> --title conductor-run-<slug>`, and pins the agent into it.
- The **unique directory** makes the cwd → workspace map unambiguous.
- The **`conductor-run-` title prefix** is the ownership marker: only conductor
  creates a workspace with it, so `isEphemeralRunWorkspace` can never match a
  workspace you made, or a pinned one.

Reclaim then rides the machinery worktrees already used:

- `Dispatcher.Archive(agentID)` maps the agent's cwd through
  `reclaimableWorkspaceMap` — worktree-isolation workspaces **and**
  `conductor-run-*` ones — and archives the WORKSPACE, which reclaims the
  directory and the agent together. This is the normal path
  (`archive_when_done`, the engine's `archiveAgent`, the flow runner).
- The **reaper** is the backstop for a run that crashed or was abandoned before
  it reached `Archive`: it walks the same map from any idle `archive=1` agent.
- A **pinned** workspace matches neither branch, so only its agent is archived
  and the workspace persists for the next run — asserted by
  `TestArchivePinnedWorkspaceSurvives`.

The reaper deliberately does NOT sweep `conductor-run-*` workspaces that have
no agent. An ephemeral workspace lives exactly as long as its agent, so
"no agent" means either it was already reclaimed together with its agent, or it
is being created right now for an agent that has not launched yet — and
archiving that one would pull the directory out from under a starting run. The
agent-anchored walk has no such window.

### Deviation from the original brief

The brief specified "no `--workspace` — paseo creates the agent its OWN per-run
workspace," alongside a NON-NEGOTIABLE requirement that those workspaces be
reclaimed. Given paseo exposes no agent → workspace handle, those two cannot
both hold: an implicitly-created workspace is not identifiable afterwards. The
reclaim invariant wins, so conductor owns the creation. The observable outcome
the brief asked for is unchanged — each default `checkout: none` run gets its
own workspace and nothing is shared between runs — and it is now provably
reclaimed.

## `pin` on a repo-checkout step

A pin and a repo checkout want opposite things: a pin is one workspace reused
across runs, while `checkout-pr` / `branch-off` give each dispatch a fresh
isolated worktree, which is the isolation that lets two PRs be worked at once.

An **explicit** `checkout: checkout-pr` or `branch-off` alongside a pin is a
config error (`validateWorkspacePin`). Rejecting beats either silent
resolution: ignoring the pin leaves an operator believing their agent has a
durable home, and ignoring the checkout would run every PR's work in one shared
directory.

A step that leaves `checkout:` unset derives its strategy from the trigger (a PR
→ `checkout-pr`, otherwise `none`), so its pin applies on the runs with no repo
context and is ignored on the ones that get a worktree. That is documented
rather than refused — the same step can legitimately serve both.

The alternative considered and rejected was making the pin the BASE workspace to
worktree from, matching `Request.Workspace`'s older doc comment. It conflates
two different things ("a workspace to run in" vs "a checkout to branch from"),
and it would have changed the checkout-resolution path — the most failure-prone
code in the dispatcher — for a case nobody has asked for.

## The two `workspace:` keys

There are two unrelated config keys named `workspace:`, in different layers.
They are not the same field and never meet.

| | connectors-model step | legacy github rule |
|---|---|---|
| declared | `config.Step.Workspace` (`internal/config/connectors.go`) | `github.Rule.Workspace` (`internal/integrations/github/github.go:123`) |
| type | `config.Workspace` (`string \| {isolation, pin}`) | `string` |
| means | isolation mode, plus an optional workspace to pin | a workspace id/path |
| validated | `Workspace.Validate` + `validateWorkspacePin` (`internal/config/steps.go`) | not validated |
| consumed | `workspaceMode` → `--new-workspace <mode>`; `pinnedWorkspace` → `--workspace <name>` | `MergeRule` (`github.go:446`) only |

The legacy rule field is **inert on today's code path**: `MergeRule` overlays
`defaults.Workspace` onto a rule and nothing downstream reads the result — no
production code writes `dispatch.Request.Workspace`. It is retained because the
config migration (`internal/migrate/agents.go` `behaviorKeys`) still moves a
`workspace:` key verbatim off a legacy `agents:` profile onto the step template,
where it lands on the connectors-model field and takes on THAT meaning. That
migration is a raw-node pass, so the string → object type change does not affect
it: a bare string still parses as an isolation mode.
