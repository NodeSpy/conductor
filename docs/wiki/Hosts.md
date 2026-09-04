# Hosts (remote execution over SSH)

Running work on another machine is a transport option, not a connector type.
Define SSH targets once under `hosts:` and reference them by `host:` from
anything that executes — a code step, a command step, an agent profile, or a
cli/acp/agent-deck runtime — or drop an inline `ssh: {…}` for a one-off.

```yaml
hosts:
  build-box:
    host: build01.internal
    user: ci
    key: ~/.ssh/id_ed25519
    known_hosts: ~/.ssh/known_hosts    # optional pin; empty = ssh defaults
    cwd: /srv/build                    # default remote working directory
    env: { CI: "1" }                   # exported into every remote command
```

Execution goes through the system `ssh` binary with `BatchMode=yes` (never an
interactive prompt), key auth, and — when `known_hosts:` is set — strict host
key checking. Environment values are exported inside the remote shell and the
code travels as a base64 frame with the ctx JSON on stdin, so secrets never
ride local argv.

## What runs remotely

| what | how |
|---|---|
| host-interpreter code steps (`run: sh/node/ruby/go/…`) | the remote box's interpreter runs the code ([[Code-Steps]]) |
| command steps (`type: command` + `host:`) | outputs `{stdout, stderr, exit_code}` |
| cli / acp / agent-deck runtimes (`host:` on the runtime) | the runtime's subprocess launches on that box; a profile's `host:` overrides the runtime's |
| paseo runtimes (`host:` on the runtime) | every paseo CLI call — run, clone, workspace create, ls, inspect, send, wait, archive, the reaper's polls — executes on that box over ssh; the host entry's `env:` supplies the remote runtime's environment; checkouts land under the remote user's `~/.conductor/checkouts` |
| opencode runtimes (`host:` on the runtime) | `opencode serve` launches remotely, still bound to the REMOTE 127.0.0.1; every HTTP request reaches it through an `ssh -W` stdio forward, so no port opens on either machine |

## What does not

- `run: js` and `run: go-embed` execute inside conductor's own process —
  local-only by construction.

Notes on remote runtimes: a remote paseo skips this box's local-filesystem
fast paths (stale-lock clearing, git revalidation of memoized checkouts, the
$HOME-fallback detection, open-workspace adoption) — the remote paseo CLI is
the source of truth there. Remote cli/acp/opencode sessions receive a
conductor-provisioned worktree path only when it exists on that box; use
`checkout: none` or a remote-existing `workdir:` otherwise (paseo runtimes
provision remotely and need neither). Acts-as-you identity still governs
anything remote work posts back.

Related: [[Code-Steps]] · [[Runtimes]] · [[Connectors]]
