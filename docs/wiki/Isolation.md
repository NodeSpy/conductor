# Agent isolation & sandboxing

Locally-dispatched agents historically ran as the same OS user as the
daemon — a sibling agent could read another's process table, files, and fds.
The `isolation:` block closes that: per-dispatch isolation for the runtimes
conductor launches itself, plus a conductor-enforced network egress
allowlist. (#36 §15.)

## Where it applies

`isolation:` can be set at three scopes:

| Scope | Applies to | Wins |
|---|---|---|
| `agents.<name>.isolation` | that profile's launches | most specific |
| `runtimes.<name>.isolation` | every launch of that runtime | fallback |
| `hosts.<name>.isolation` | every script that SSH host runs | independent |

It only applies to launches conductor performs itself: **acp**, **cli**,
**opencode**, and **agent-deck** runtimes, and `hosts:` scripts (code steps,
remote commands). A **paseo** runtime's agents are children of the paseo
daemon — conductor never holds that process, so `conductor validate` rejects
`isolation:` on paseo runtimes and on profiles that resolve to one, rather
than silently not isolating. Use paseo's own sandboxing there, or move the
profile to a runtime conductor launches.

## Modes

```yaml
agents:
  risky-fixer:
    runtime: gemini
    isolation:
      mode: user | namespace | container
      user: sandboxagent                  # mode: user
      container: { image: agents:latest, engine: docker }  # mode: container
      limits: { memory: 2g, cpu: 200%, pids: 256 }
      network:
        egress: [ "api.github.com:443", "*.internal:443" ]
        # deny: true       # structural no-network (namespace/container only)
```

### `mode: user` — a distinct low-privilege user

The launch is wrapped in `sudo -n -u <user> --`. Works on any Unix; the
conductor user needs a sudoers rule like:

```
conductor ALL=(sandboxagent) NOPASSWD: ALL
```

The sandbox user should own nothing but its own scratch space; the worktree
must be readable/writable by it (group membership or ACLs).

### `mode: namespace` — Linux namespaces + cgroups

The launch is wrapped in
`unshare --user --map-current-user --pid --fork --mount-proc --kill-child`:
its own user/pid/mount namespaces, so it cannot see sibling process tables
or `/proc/<pid>/environ` of other agents. With `limits:` set, a
`systemd-run --user --scope` prefix applies cgroup caps (`MemoryMax`,
`CPUQuota`, `TasksMax`). `network: {deny: true}` adds `--net`: the agent has
no network interface but loopback in an empty namespace — structural.

Linux-only; `conductor validate` rejects it on other platforms (a remote
`hosts:` entry skips the local check — the remote box's OS applies).

### `mode: container` — docker / podman

The launch becomes `docker run --rm -i -v <worktree>:<worktree> -w <worktree> …`
(engine `podman` selectable). The image must carry the runtime binary the
launch expects (e.g. `claude`, `gemini`). `deny: true` becomes
`--network=none`; `limits:` map to `--memory/--cpus/--pids-limit`. The
identity env is passed through with `-e KEY` — the daemon's own environment
is **not** forwarded into the container. Not supported for remote (`host:`)
launches — configure it on that box's own conductor.

## The egress allowlist (conductor-enforced)

A `network:` block routes the launched runtime's HTTP(S) traffic through a
loopback forward proxy **inside conductor's own process**:

- `egress: [ "api.github.com:443", "*.internal", "10.0.0.7:8443" ]` —
  CONNECT tunnels and plain-HTTP proxy requests are matched against the
  patterns (`host`, `host:port`, `host:*`, glob on the host half). A bare
  host means **:443 only** (the safe default); any other port needs an
  explicit `host:port`, and `host:*` is the deliberate any-port opt-in.
  Non-matching targets get a 403 and an `egress_denied` audit record.
- `network: {}` (present but empty) — deny-all: every egress attempt is
  refused and audited.
- The launch env gets `HTTP_PROXY`/`HTTPS_PROXY` (and lowercase) pointing at
  the proxy **with a per-dispatch credential** in the URL;
  `NO_PROXY=127.0.0.1,localhost,::1` keeps conductor's own local surfaces
  (the skill socket, a local opencode server) reachable.
- The proxy **requires that credential** (`Proxy-Authorization`): it's a
  host-wide loopback listener, so without auth any local process could ride
  an allowlisted profile's egress. Unauthenticated clients get 407 before
  any target matching; the credential is stripped before anything leaves the
  box.

**Deny by default for agent-authored work:** a dispatch that came from an
agent-authored plan (§11) is routed through the deny-all proxy even with no
`isolation:` configured at all. An explicit `network.egress:` on the profile
opts specific targets back in; `trust` doesn't change this — only config
does.

### Enforced vs. advisory

The allowlist has two strengths, and the strong one is what you should
reach for:

- **Enforced — `deny: true` + `egress:` under `namespace`/`container`.**
  The sandbox's network is removed structurally (`unshare --net` /
  `--network=none`); the launch is re-entered through `conductor
  sandbox-net`, an in-sandbox forwarder that pipes into conductor's
  filtering proxy over a **unix socket** (a filesystem object — it crosses
  the namespace boundary; nothing else does). The runtime's
  `HTTP(S)_PROXY` points at the forwarder's in-sandbox loopback address,
  reachable from inside and nowhere else. A runtime that ignores proxy env
  reaches **nothing**: there is no interface, no route, and no DNS — the
  proxy resolves CONNECT targets itself outside the sandbox, so
  DNS-tunnel exfiltration is closed with the rest. The allowlist is an OS
  boundary. (Container mode bind-mounts conductor's own static binary and
  the socket into the container; same-architecture image required.)
- **Advisory — `egress:` under `mode: user` (or without `deny`).** Only
  the proxy env steers traffic; a runtime that ignores `HTTP(S)_PROXY`
  can still reach the network directly. `conductor validate` says so out
  loud. Use it for audit/visibility, not as a boundary — and prefer the
  enforced form whenever the profile runs on this box.

Plain `deny: true` (no list) remains the full structural cutoff. `deny:
true` in any form is rejected for opencode runtimes (it would sever
conductor's own HTTP control channel — use `network: {}` instead), and an
`egress:` list needs a local launch (the proxy lives on this box; use
plain `deny: true` with namespace mode on a remote host).

## Hosts

```yaml
hosts:
  sandbox:
    host: sandbox.internal
    user: ci
    isolation: { mode: user, user: agents }
```

Every script that host runs — including agent-authored code forced onto it
by `policy.agent_authored.host` — executes wrapped
(`sudo -n -u agents -- sh -c '…'`) on the remote box. Modes `user` and
`namespace` only; the egress proxy is loopback-only and doesn't reach remote
launches (validation rejects a remote `egress:` list — use `deny: true` with
namespace mode there).

## Degradation

| Situation | Behavior |
|---|---|
| `mode: namespace` on macOS/Windows | rejected by `validate` (local) / launch error with a clear message |
| `sudo` / `unshare` / `docker` missing | launch fails with "needs X on PATH", never silently unisolated |
| egress policy with no proxy wired | launch fails closed |
| isolation on a paseo runtime | rejected by `validate` |

## Audit

Every denied egress attempt is logged and audited as
`{event: egress_denied, target: host:port}`, so a sandboxed agent probing
the network is visible in `conductor report`'s audit trail.
