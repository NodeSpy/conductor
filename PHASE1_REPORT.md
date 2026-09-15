# Phase 1 report — cli runtime: git-native checkouts

Branch `cli-git-worktrees-impl`, two commits, not pushed.

The cli runtime now provisions its checkout as a plain `git` worktree under
conductor's own state dir and removes it when the session closes. paseo is
untouched on its own path; acp / opencode / agent-deck are untouched.

Open questions and deliberate deviations: `PHASE1_QUESTIONS.md`.

## Files touched

New:

| file | what |
|---|---|
| `internal/gitwt/gitwt.go` | the `GitProvisioner` (`gitwt.Provisioner`) + orphan reaper |
| `internal/gitwt/gitwt_test.go` | unit tests against real git repos in `t.TempDir()` |
| `internal/controller/provision.go` | `routedProvisioner` — per-dispatch git/paseo routing + ownership |
| `internal/controller/cli_worktree_test.go` | leak, reuse, idempotence and routing tests |

Changed:

| file | what |
|---|---|
| `internal/controller/runner.go` | `Provisioner` gains `RemoveWorktree`; the corrective schema turn opens its session with `ReuseWorkspace: true` |
| `internal/controller/controller.go` | `Spec.ReuseWorkspace` |
| `internal/controller/cli.go` | `cliSession` records the provisioned id + ownership; `Close` releases it |
| `internal/controller/registry.go` | `RegistryOption` / `WithCLIProvisioner`, `cliProvisioner()` per-controller pick |
| `internal/controller/helpers_test.go` | `fakeProv.RemoveWorktree` records removals |
| `internal/dispatch/provision.go` | `Dispatcher.RemoveWorktree` (no-op); exports `WorkDir`, `EffectiveStrategy`, `BranchSlug` |
| `internal/core/targetreads_meta_test.go` | two `rawTargetExceptions` for the new checkout path |
| `cmd/conductor/main.go` | builds the git provisioner, injects it, starts its reaper |
| `test/e2e/docker-compose.yml` | `CONDUCTOR_GIT_REMOTE_BASE` for the hermetic forge; fast reaper hooks on `conductor-ctrl` |

## How it is wired

```
cmd/conductor/main.go
  gitProv := gitwt.New(stateDir)                    // <state>/checkouts, <state>/worktrees
  controller.NewRegistry(…, controller.WithCLIProvisioner(gitProv))
  go gitProv.Run(ctx)                               // startup + periodic orphan reap

internal/controller/registry.go
  buildController(… prov, cliProv)
    case cli  → newCLIController(name, cc, cliProvisioner(cc, prov, cliProv))
    all other → newXController(name, cc, prov)      // unchanged, dispatcher-backed

cliProvisioner → routedProvisioner{host: cc.Host, git: gitProv, fallback: prov}
  ProvisionWorktree: resolveHost(cc.Host, step.Host) != ""  → fallback (paseo)
                     otherwise                              → git
                     records id → owner
  RemoveWorktree:    back to the recorded owner; unknown id → no-op
```

The controller and its runner share one `routedProvisioner` instance
(`cliController.prov`, handed to `newControllerRunner`), so the id the runner
provisions is the id the session releases.

Lifecycle:

```
controllerRunner.Dispatch → prov.ProvisionWorktree → Spec{Cwd, WorkspaceID}
  cliController.NewSession → cliSession{wsID, ownsWT: wsID != "" && !ReuseWorkspace}
  … turn runs; engine reads the proposed diff and runs the quality gate here …
controllerRunner.Archive → cliSession.Close → prov.RemoveWorktree(wsID)
```

Removal is in `Close`, not in `Archive`, so it fires on every close path — but
`Close` is reached only after the gate and the `gitdiff.Proposed` read, both of
which the flow runner does while the session is still live (`internal/flow/flow.go`
runs gate → diff → plan → `Archive`). Two things had to be true for that to be
safe, and both are now pinned by tests:

- **the corrective `output_schema` turn must not release.** `enforceSchema`
  opens a *second* session on the *first* session's worktree and `defer`-Closes
  it; without `Spec.ReuseWorkspace` that close would delete the checkout before
  the gate/diff read. It is now marked as a borrower and releases nothing.
- **release must survive a cancelled context.** `Close` runs its git under
  `context.WithoutCancel` + a 30s timeout, so a shutdown-path close still tears
  the worktree down instead of leaving one behind.

Provisioning itself follows `dispatch.EffectiveStrategy` — the *same* function
the paseo path resolves its strategy with, now exported, so the two can't drift:

| strategy | git commands |
|---|---|
| `checkout-pr` | `fetch --no-tags --force origin refs/pull/<n>/head`; `worktree add -B <head_ref\|pr-n> <wt> FETCH_HEAD` (`--detach` retry if that branch is checked out elsewhere) |
| `branch-off` | `worktree add -B conductor/<kind>-<n> <wt> <origin/base \| base \| origin/HEAD>` |
| `none` | `("", "", nil)` — no worktree, as before |

An explicit action `work_dir` still wins over all of it, and returns no id — an
operator's directory is never conductor's to delete. Base clones are one per
repo at `<state>/checkouts/<owner>__<repo>`, `--filter=blob:none` with a full
clone fallback, `fetch --prune` on reuse, serialized by a per-repo mutex. Every
git failure is wrapped `dispatch.Unrecoverable`, so a failed checkout escalates
and retries exactly as a failed `paseo workspace create` does.

**Non-goal guard:** a cli launch whose effective host is non-empty (controller
`host:` or step `host:`) routes to the paseo provisioner — the git path only
ever touches this box's filesystem, and a remote agent handed a local path would
land nowhere. `TestCLIProvisionerRoutesRemoteLaunchesToPaseo` covers both the
controller-level and step-level host.

## Test structure

### The leak test — `internal/gitwt`

`TestRemoveWorktreeLeavesNothingBehind` is the one that proves the bug is gone.
It provisions against a real bare repo, asserts the checkout exists *and* that
`git worktree list` in the base clone reports it, calls `RemoveWorktree`, then
asserts all three of: the directory is gone, `git worktree list` no longer names
it, and the provisioner's live set is empty (so the reaper isn't blocked on a
stale claim). A second `RemoveWorktree` must return nil — `Close` then a reaper
pass is a real sequence.

Companions: `TestRemoveWorktreeRecoversTheBaseCloneAfterRestart` (a fresh
provisioner over the same state dir still removes *through git*, recovering the
base clone from the worktree's `.git` pointer) and
`TestRemoveWorktreeIgnoresForeignPaths` (an empty id, a paseo workspace id, a
path outside the state dir, a *nested* path under `worktrees/`, and the parent
of `worktrees/` are all inert — files planted at those paths survive).

Controller side, `TestCLICloseReleasesTheProvisionedWorktree` asserts nothing is
released before `Close` and exactly the provisioned id after it; plus
`…ReleasesOnlyOnce`, `…ReleasesUnderACancelledContext`,
`TestCLIReuseSessionDoesNotReleaseTheWorktree`, and
`TestCLICloseWithoutAWorktreeReleasesNothing`.

### The reaper tests

`TestReapRemovesOrphansButNeverALiveWorktree` provisions two worktrees, drops
one from the live set (the killed-daemon case), and backdates **both** dirs to
the same age — so only liveness can distinguish them. After `Reap`: the orphan's
directory *and* git's record of it are gone, the live one's directory *and*
git's record of it are intact. Two more fence it in:
`TestReapRespectsTheGracePeriod` (an unclaimed but fresh dir survives) and
`TestReapOnlyTouchesTheWorktreesDir` (a backdated `state.json` next to it and
the base clone itself both survive).

### Provisioning tests

Real `git` against a real bare repo seeded with `main`, a `feature` branch and
`refs/pull/7/head` — the refs a forge actually exposes. Base clone created then
re-fetched (a commit pushed between the two calls must appear); `checkout-pr`
lands the PR head SHA on the head-ref branch, and on `pr-<n>` without a head
ref; `branch-off` lands `conductor/<kind>-<n>` at `main`'s tip and *not* the PR
head; `none` creates no worktrees dir at all; an unreachable remote and a
repo-less trigger both yield `dispatch.IsUnrecoverable`; two dispatches sharing
an id get different directories; `prBranch` rejects `--upload-pack=…`, `../evil`
and friends while accepting `users/me/fix-1`.

### Mutation-checked

Every assertion above was verified to bite by breaking the code and watching a
named test fail: no-op `RemoveWorktree` (3 tests), no-op `Close` (3), ignored
`ReuseWorkspace` (1), reaper ignoring the live set (1), skipped re-fetch (1),
detached `checkout-pr` (2), git provisioner used for remote hosts (1), git
provisioner handed to acp (1). Reverted after each.

No existing test was weakened. One existing meta-test needed a real entry:
`TestEveryDispatchTargetReadIsAuditedOrRouted` flagged the two
`Trigger.Target` reads in the new checkout path, which are now in
`rawTargetExceptions` with the same reason `internal/dispatch/paseo.go`'s
checkout reads carry — they pick a checkout, they do not authorize anything, and
the branch name is validated before it reaches git.

## Deviations from the design doc

Full reasoning in `PHASE1_QUESTIONS.md`; in short:

1. **`checkout-pr` lands on a named branch**, not a detached `FETCH_HEAD`. A
   detached head makes the agent push to `conductor-work`, and the e2e asserts
   the commit on `pr-1`. Branch = validated `head_ref`, else `pr-<n>`.
2. **Clone URL is overridable** (`CONDUCTOR_GIT_REMOTE_BASE`), defaulting to
   `git@github.com:<repo>.git`. Hardcoded github ssh cannot clone the hermetic
   forge. The e2e compose now sets it for the conductor services.
3. **`Dispatcher.RemoveWorktree` is a no-op**, the doc's "keep existing
   behavior" option. Consequence: a `host:`-pinned cli runtime still leaks a
   paseo workspace, as today — the only leak left.
4. **`-B` rather than `-b`** for branch-off, so a re-dispatch resets its branch
   instead of failing on "branch already exists".
5. **Partial clone falls back to a full clone** when the server rejects
   `--filter` (the e2e's `git daemon` does).
6. **The reaper's live set is the provisioner's own**, not one passed in from
   the controller — same set, no new seam — *and* `MinAge` must also have
   elapsed. Both conditions, not either.

## e2e (not run here — yours to run)

`test/e2e/docker-compose.yml` now sets `CONDUCTOR_GIT_REMOTE_BASE:
"git://forge/"` on the conductor services and
`PC_GIT_WORKTREE_REAP_INTERVAL: 5s` / `PC_GIT_WORKTREE_MIN_AGE: 30s` on
`conductor-ctrl` (which runs the Group B cli rows). Expected behavior: the
`cli:claude-code` and `cli:codex` rows still edit+commit+push to the forge on
`pr-1` — now from `<state>/worktrees/<dispatch-id>` — and `fakepaseo`'s state
records **no** `workspace create` for them. The seed already publishes
`refs/pull/1/head`, which is what the git path fetches.

I have not run the docker e2e; `docker` is not available in this worktree.

## Gates

```
$ gofmt -l ./cmd ./internal ./test
(no output)

$ go build ./...
(no output)

$ go test -race ./...
ok  	github.com/NodeSpy/conductor/internal/gitwt	3.402s
ok  	github.com/NodeSpy/conductor/internal/controller	1.357s
ok  	github.com/NodeSpy/conductor/internal/dispatch	4.575s
ok  	github.com/NodeSpy/conductor/internal/core	2.434s
ok  	github.com/NodeSpy/conductor/internal/flow	14.581s
ok  	github.com/NodeSpy/conductor/internal/engine	2.187s
…
40 packages ok, 0 FAIL
```
