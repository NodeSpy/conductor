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

## What does not

- `run: js` and `run: go-embed` execute inside conductor's own process —
  local-only by construction.
- `host:` on a **paseo** runtime is rejected at validation: paseo checkout
  resolution is local filesystem work. Use a cli/acp runtime on that host, or
  run a conductor there.
- `host:` on an **opencode** runtime is rejected: its spawned server would
  bind on the remote box where conductor cannot reach it.

Acts-as-you identity still governs anything remote work posts back.

Related: [[Code-Steps]] · [[Runtimes]] · [[Connectors]]
