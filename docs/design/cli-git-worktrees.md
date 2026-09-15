# cli runtime: git-native checkouts (decouple from paseo)

Status: design / phase 1. Implementation contract.

## Why

A `workspace: worktree` dispatch provisions its checkout via **paseo**
(`dispatch.Dispatcher.ProvisionWorktree` → `createWorktree` →
`Backend.CreateWorktree` = `paseo workspace create`, `internal/dispatch/paseo.go:498`)
*regardless of which runtime runs the agent*. So a claude-**cli** fixer still
makes a paseo workspace, the cli path never reuses one, and `cliSession.Close`
(`internal/controller/cli.go:518`) never removes it — paseo workspaces pile up
even though the work runs on claude code.

Goal: the **cli** runtime provisions its **own plain `git` worktree** (no paseo)
and tears it down when the session closes. Then cli work is self-contained,
creates zero paseo workspaces, and paseo is genuinely hand-off-only. conductor
already shells plain `git` (`internal/gitdiff/gitdiff.go:72`, `git -C <dir> …`).

## Surface

Implement `controller.Provisioner` (`internal/controller/runner.go:16`):

```
ProvisionWorktree(ctx, req dispatch.Request) (id, cwd string, err error)
```

plus a teardown the cli session calls on close:

```
RemoveWorktree(ctx, id string) error
```

Add `RemoveWorktree` to the `Provisioner` interface (the paseo `*Dispatcher`
implements it as a no-op or a `paseo workspace archive`, keeping existing
behavior; the cli path implements the real removal). The `cliSession` stores the
`id` returned by `ProvisionWorktree` and calls `RemoveWorktree` in `Close`.

## `GitProvisioner`

State under a conductor dir (respect `config.SetStateDir` / the same base the
store uses; default `~/.local/state/conductor`):

- **Base clones:** `<state>/checkouts/<owner>__<repo>` — one per repo. First use:
  `git clone --filter=blob:none <ssh-url> <dir>`. Subsequent: `git -C <dir> fetch
  --prune origin`. **ssh** URL (`git@github.com:owner/repo.git`) to match the
  acts-as-you push identity (like `cloneRepo`'s ssh default,
  `internal/dispatch/paseo.go`). Serialize concurrent fetches of one base clone
  with a per-repo mutex.
- **Worktrees:** `<state>/worktrees/<dispatch-id-or-random>` created from the
  base clone. `req` carries the dispatch id; use it (fall back to a random slug
  — `Math.random`/time are fine here, this is runtime state, not a replayable
  workflow).

`ProvisionWorktree` by `effectiveStrategy(req)` (`internal/dispatch/paseo.go:369`):

| strategy | steps |
|---|---|
| `checkout-pr` | `git -C <base> fetch origin pull/<PR>/head`; `git -C <base> worktree add <wt> FETCH_HEAD` |
| `branch-off` | `git -C <base> worktree add -b <branchSlug> <wt> <BaseRef>` (fetch base first) |
| `none` | return `("", "", nil)` — the read-only judges; NO worktree |

Return `(wt, wt, nil)` (id == cwd == the worktree path). On any git failure wrap
with `dispatch.Unrecoverable(err)` so the engine escalates + retries exactly like
the paseo path.

`RemoveWorktree(id)`: `git -C <base> worktree remove --force <id>` then
`git -C <base> worktree prune`. Tolerate an already-gone worktree (idempotent).

## Orphan reaper

On daemon startup and on a periodic tick: for each base clone, `git worktree
prune`, and remove any `<state>/worktrees/*` dir with no live cli session (the
controller knows its live set — pass it, or reap dirs older than a grace period
with no matching live agent). Crash safety so a killed daemon doesn't leak
worktrees. Keep it conservative: only remove dirs under conductor's own
`<state>/worktrees`, never anything else.

## Wiring

- `cmd/conductor/main.go`: build a `GitProvisioner` and inject it as the
  **cli controller's** `prov` (`buildController` gets `prov` — give the cli case a
  git provisioner, leave paseo/acp/opencode on the existing dispatcher-based one).
  Simplest: pass both provisioners into the registry and let `buildController`
  pick per controller type.
- `internal/controller/cli.go`: `cliSession` records the worktree id from
  `NewSession`'s `Spec.WorkspaceID`/cwd and calls `prov.RemoveWorktree` in
  `Close`. (The controller already has `prov`; thread it to the session or have
  the runner do the removal in `Archive` after `Close`.)
- The `gitdiff` proposed-diff read (`internal/gitdiff`) already works on any git
  worktree — unchanged.

## Non-goals (phase 1)

- **Remote `host:` runtimes** (a cli runtime over SSH): keep those on paseo for
  now — the git provisioner is local-box only. Guard: if the effective host is
  non-empty, fall back to the paseo provisioner (or refuse with a clear error).
- acp/opencode controllers: leave on the paseo provisioner (inactive here).
- Adopt/reuse an existing worktree for a PR that already has a live cli agent:
  nice-to-have, phase 2. Phase 1 is one worktree per dispatch, cleaned on close.

## Verification

- **Unit** (against `t.TempDir()` git repos, real `git`): base clone + re-fetch;
  `checkout-pr` and `branch-off` worktree add land the right ref; `RemoveWorktree`
  leaves no dir and no `git worktree list` entry; the reaper prunes an orphan dir
  but never a live one. A **leak test**: `Provision` then `Remove` → clean.
- **e2e:** the existing cli fixer scenario (Group B `cli:claude-code`) must still
  edit+commit+**push** to the local forge — now via a git worktree — and no paseo
  workspace is created for it. Assert both.
- `gofmt` clean, `go build ./...`, `go test -race ./...` green; do not weaken
  existing tests.

## Workflow for the implementer

Commit to this branch in logical commits (no AI-attribution/Co-Authored-By
footers). Do NOT push or open a PR — the parent reviews, runs the docker e2e,
and stages the live rollout. Write a `PHASE1_REPORT.md` at repo root: files
touched, how the git provisioner is wired, the leak/reaper test structure, any
deviations, and the tail of your build/-race/gofmt output.
