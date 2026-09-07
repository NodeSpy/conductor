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
key checking. The `--` operand separator always precedes the host, so a host
value can never be parsed as an ssh flag. Environment VALUES never appear in
shell text or on argv (where `ps` on either end would see them): the export
preamble travels base64-encoded as the first line of stdin, decoded and
eval'd by the remote wrapper before the rest of stdin reaches the script.
Env KEYS must be valid variable names (`[A-Za-z_][A-Za-z0-9_]*`) — anything
else is a hard error, since a key sits unquoted in the `export` text. Code
travels as a base64 frame with the ctx JSON on stdin.

## What runs remotely

| what | how |
|---|---|
| host-interpreter code steps (`run: sh/node/ruby/go/…`) | the remote box's interpreter runs the code ([[Code-Steps]]) |
| command steps (`type: command` + `host:`) | outputs `{stdout, stderr, exit_code}` |
| command connectors (`connectors: x: {type: command, host: …}`) | `uses: x.run` executes the command on that box over SSH; the connection's `env:`/`cwd:` apply inside the remote shell ([[Connectors]]) |
| cli / acp / agent-deck runtimes (`host:` on the runtime) | the runtime's subprocess launches on that box; a profile's `host:` overrides the runtime's |
| paseo runtimes (`host:` on the runtime) | every paseo CLI call — run, clone, workspace create, ls, inspect, send, wait, archive, the reaper's polls — executes on that box over ssh; the host entry's `env:` supplies the remote runtime's environment; checkouts land under the remote user's `~/.conductor/checkouts` |
| opencode runtimes (`host:` on the runtime) | `opencode serve` launches remotely, still bound to the REMOTE 127.0.0.1; every HTTP request reaches it through an `ssh -W` stdio forward, so no port opens on either machine |

## What does not

- `run: js`, `run: go-embed`, `run: risor`, and `run: lua` execute inside
  conductor's own process — local-only by construction.

Notes on remote runtimes: a remote paseo skips this box's local-filesystem
fast paths (stale-lock clearing, git revalidation of memoized checkouts, the
$HOME-fallback detection, open-workspace adoption) — the remote paseo CLI is
the source of truth there. Remote cli/acp/opencode sessions receive a
conductor-provisioned worktree path only when it exists on that box; use
`checkout: none` or a remote-existing `workdir:` otherwise (paseo runtimes
provision remotely and need neither). Acts-as-you identity still governs
anything remote work posts back.

A host may carry an `isolation:` block (modes `user`/`namespace`): every
script it runs — including agent-authored code forced onto it by
`policy.agent_authored.host` — executes wrapped de-privileged on the remote
box. See [[Isolation]].

Related: [[Code-Steps]] · [[Runtimes]] · [[Connectors]] · [[Isolation]]
