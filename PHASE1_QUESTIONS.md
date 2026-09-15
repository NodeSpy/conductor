# Phase 1 questions — cli git worktrees

Open points where `docs/design/cli-git-worktrees.md` was ambiguous or where I
read it as under-specified. Each says what I did, so nothing was blocked; each
is a one-line change if you want the other answer.

## 1. `checkout-pr` lands on a branch, not a detached `FETCH_HEAD`

The doc's table says `git worktree add <wt> FETCH_HEAD`. Taken literally that
leaves the worktree on a **detached HEAD**, and the agent then cannot push the
way it does on the paseo path: `test/e2e/services/fixer/fixer.go` reads
`rev-parse --abbrev-ref HEAD`, gets `HEAD`, and falls back to pushing a branch
called `conductor-work` — while the harness asserts a `conductor:` commit on
branch **`pr-1`** (`forge_has_conductor_commit … pr-1`, `test/e2e/run.sh:246`).
fakepaseo does `fetch origin refs/pull/N/head:pr-N` and checks that branch out.

So I fetch `refs/pull/<n>/head` as the doc says and then
`git worktree add -B <branch> <wt> FETCH_HEAD`, where `<branch>` is the PR's own
head ref from `Trigger.Context["head_ref"]` (validated by `safeBranch` — it is
sender-controlled data), falling back to `pr-<n>`. If the branch is already
checked out in another live worktree, it retries `--detach` rather than yank the
other checkout's branch pointer.

**Question:** is naming the branch after the PR head ref right for a
cross-fork PR? `git push origin HEAD` from a fork-PR checkout pushes to a branch
of that name in the *base* repo, not the fork. The paseo path has the same
shape, so this is not a regression — but it is not a fix either.

## 2. Where the base clone's remote URL comes from

The doc specifies `git@github.com:owner/repo.git`. Hardcoding that breaks the
hermetic e2e, whose forge is `git://forge/<repo>.git`. I made it
`gitwt.DefaultRemoteURL`: ssh/github by default, overridable with the
`CONDUCTOR_GIT_REMOTE_BASE` env var, and I set that var in
`test/e2e/docker-compose.yml` for the conductor services (next to the existing
`FORGE_BASE`).

**Question:** env var, or should this be a config field (`checkout.remote_base`)
alongside the dispatcher's existing `CloneProtocol`? An env var is what the
harness needed; config is what an operator with a GHE host will want.

## 3. `Dispatcher.RemoveWorktree` is a no-op

The doc allows "a no-op or a `paseo workspace archive`, keeping existing
behavior". I chose the **no-op**: paseo workspace lifetime is already owned by
`Dispatcher.Archive` (which archives the whole worktree workspace) and the paseo
reaper, and archiving again from a session `Close` could archive a workspace out
from under an agent the engine still treats as live.

**Consequence:** a cli runtime pinned to a `host:` still leaks a paseo
workspace, exactly as today. That is the phase-1 non-goal, but it is now the
*only* remaining leak — worth naming as phase-2 work.

## 4. The live set is the provisioner's own, not the controller's

The doc offers "the controller knows its live set — pass it, or reap dirs older
than a grace period". I did neither exactly: `GitProvisioner` tracks what it has
provisioned and not yet removed, which is the same set without a new seam
between the packages, **and** requires `MinAge` before removing. Both conditions
must hold, so a fresh orphan survives until the grace period and a live worktree
survives regardless of age.

Defaults: `MinAge` 1h, tick 15m, overridable by `PC_GIT_WORKTREE_MIN_AGE` /
`PC_GIT_WORKTREE_REAP_INTERVAL` (the e2e's `conductor-ctrl` sets 30s/5s).

## 5. Removal discards work the agent committed but never pushed

`Close` deletes the worktree, so a fixer that commits without pushing loses the
commit. This matches the paseo path (`Dispatcher.Archive` archives the whole
worktree workspace), so I did not add a guard — but if you want one, "refuse to
remove a worktree with unpushed commits, leave it for the reaper" is a small
addition to `RemoveWorktree`.

## 6. Partial clone falls back to a full clone

`git clone --filter=blob:none` fails outright against a server with
`uploadpack.allowFilter` off — which includes the e2e's `git daemon`. I retry as
a full clone rather than fail the dispatch. Slower, never wrong.
