# Agent isolation & sandboxing

Locally-dispatched agents historically ran as the same OS user as the
daemon — a sibling agent could read another's process table, files, and fds.
The `isolation:` block closes that: per-dispatch isolation for the runtimes
conductor launches itself, plus a conductor-enforced network egress
allowlist. (#36 §15.)

## The agent workspace jail {#the-agent-workspace-jail}

**Every agent conductor launches itself — a `cli` or `acp` runtime on this
box — runs jailed to its workspace by default.** No configuration: a launch
with no `isolation:` block gets a synthesized jail (#154). The agent sees:

| | |
|---|---|
| read-write | the workspace; the git common dir behind a worktree — with its `config`, `hooks/` and `objects/info` read-only and other dispatches' worktree metadata hidden; the tool's own state (`~/.claude`, `~/.claude.json`, `~/.codex`, …) |
| read-only | `/usr`, `/etc`, `/opt`, `~/.gitconfig`, the agent CLI's own install (e.g. `~/.local/share/claude`), the conductor binary, the dispatch's broker socket |
| scratch | a private `$HOME` (tmpfs on Linux, a per-dispatch dir on macOS) and a per-dispatch `/tmp` |
| absent | everything else: `~/.ssh`, cloud and tool configs, the daemon's config and state, other repositories |

- **No credentials in the agent's environment.** `GH_TOKEN`, `GITHUB_TOKEN`,
  the `PC_GH_*` tokens, `SSH_AUTH_SOCK`, cloud credentials, and anything named
  like a token/secret/password are removed; the agent's own model credential
  (`ANTHROPIC_API_KEY` for claude-code, `OPENAI_API_KEY` for codex, …) is
  kept. The daemon's own environment is never inherited wholesale.
- **Credentialed work goes through conductor**: [host commands](Host-Commands)
  (`gh`, `aws`, `kubectl`, …) run on your machine with your setup after
  guardrails and your rules; git's network and signing side is brokered
  (pushes only to the dispatch's own branch; commits signed with your key,
  which never enters the jail). Every crossing is an audit row and a
  `conductor watch` event.
- The agent runs as your real uid (claude-code refuses
  `--dangerously-skip-permissions` as root; the jail is built as
  root-in-userns and dropped before exec).

Knobs — every loosening is explicit:

```yaml
runtimes:
  claude:
    use: cli
    tool: claude-code
    isolation:
      fs: [~/go, ~/.cache/go-build]     # add paths (read-write) to the jail
      network: audit                    # see "Network" below
      host: { docker: false }           # see Host-Commands
      writes: { create_pr: true }       # see "Writes" below
      # mode: none                      # opt out: today's unconfined launch
      # mode: namespace, privileged: true   # the older full-view namespace
```

A top-level `isolation:` block is the fleet-wide base (runtime blocks, then a
step's, refine it). **The synthesized default degrades loudly** where the OS
cannot build it (conductor running as root, no unprivileged user namespaces,
no `unshare`): a warning, a `jail degraded` audit row and watch event, and the
launch runs as before. **An explicit `isolation:` block fails closed.**
`conductor validate` and boot report which runtimes are jailed, the host set,
and whether this box can build the jail.

Out of scope, by construction (validate notes them): **paseo** and
**agent-deck** runtimes (another daemon owns the agent process), **opencode**
(an HTTP control channel), external **runtime plugins** (explicit `isolation:`
only), and **`host:`** runtimes (the remote box's isolation applies).

### Network {#network}

```yaml
isolation:
  network: open            # default: the host network (today's behavior)
  # network: audit         # everything through conductor's proxy — allowed, and every destination recorded
  # network: { egress: [proxy.golang.org, registry.npmjs.org] }   # ENFORCED allowlist
  # network: deny          # nothing but the agent's own model endpoint
```

In `audit`, allowlist, and `deny` modes the jail has **no route out** except
conductor's filtering proxy — a tool that ignores `HTTPS_PROXY` reaches
nothing rather than bypassing the list. Linux: an empty network namespace, the
in-jail forwarder, the proxy over a unix socket, no DNS inside. macOS: Seatbelt
denies all outbound traffic except to the proxy's loopback port, and denies the
resolver's Mach service. **The agent's own model endpoint is always allowed**:
the tool's API hosts, plus an `ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL` override
(env or `~/.claude/settings.json`) — a loopback router (`http://127.0.0.1:3456`)
is relayed into the jail, a private-network host is allowlisted by its
addresses. `audit` is how an allowlist gets built: run with it, read the
`egress` events in `conductor watch` / the audit trail, then switch to
`egress:`. The default stays `open` because the enforced modes break tools
that do not honor `HTTPS_PROXY`; `audit` shows exactly which.

### Writes are bound to the dispatch's own target {#writes-are-bound-to-the-dispatchs-own-target}

Every write conductor performs on an agent's behalf — a host command, a
brokered push, a conductor verb — must target the dispatch's own PR or issue:
its number, its head branch, its repository. A fixer may push its PR's head
branch, comment on the PR, and reply to and resolve its review threads.
Refused by default for every step: opening a PR or issue, writing to any other
PR/issue, pushing any other branch (or the default branch, or a force push, or
a deletion), merging, closing, reopening. Review steps — a `decide:` step, a
step with an `output_schema`, a `checkout: none` step, or a
review-requested/self-review trigger, unless `expect_push:` — write nothing.
A refusal is audited with the rule that fired
(`target: write to acme/app#43 but dispatch target is #42`) and fails for the
agent like any failed command, with the reason on stderr.

```yaml
isolation:
  writes: read_only          # or: target (the fixer default, e.g. to opt a review-shaped step in)
  # writes: { create_pr: true, create_issue: true, other_targets: true, branches: ["release/*"], merge: true }
```

A step may widen this explicitly (a workflow whose job is to open PRs); a
**pack's** step cannot widen past what your runtime or top-level `writes:`
allows.

**Work stops when its target goes away.** When a dispatch's PR merges or
closes, conductor cancels the agents running for it, records
`cancelled: target merged|closed`, and notifies. Independently, the broker
refuses every write for a closed target (it checks the PR's live state before
a write), so a session that is mid-command when the event lands still cannot
act. Agents are also told the rule, so a well-behaved one reports instead of
trying — but the policy is what holds.

### macOS {#macos-jail}

The same jail on the Seatbelt backend (verified on macOS 26): a per-dispatch
scratch `$HOME` with the tool-state paths linked in (the real home unreadable
except those paths), a per-dispatch `TMPDIR` (claude-code's scratch included,
via `CLAUDE_CODE_TMPDIR`), a shim dir first on `PATH` with every host-set
binary's real path exec-denied, Mach services narrowed to an explicit list,
and git's `config`/`hooks`/`objects/info` write-denied.

**The Keychain is closed by default.** Allowing the Security framework's
services (what a Keychain-held claude-code login needs) also lets the agent
read other Keychain items whose access list trusts `/usr/bin/security` —
verified: with them allowed, a jailed `security find-generic-password` read
gh's `gh:github.com` token; without them it read nothing. Authenticate
claude-code with an API key (`ANTHROPIC_API_KEY`, or in
`~/.claude/settings.json`) or a `claude setup-token`
(`CLAUDE_CODE_OAUTH_TOKEN`), which need no Keychain. The explicit loosening is
`isolation: { macos_keychain: true }` (runtime/top-level only).

## Where it applies

`isolation:` can be set at three scopes:

| Scope | Applies to | Wins |
|---|---|---|
| `agents.<name>.isolation` | that profile's launches | most specific |
| `runtimes.<name>.isolation` | every launch of that runtime | fallback |
| `hosts.<name>.isolation` | every script that SSH host runs | independent |

It only applies to launches conductor performs itself: **acp**, **cli**,
**opencode**, and **agent-deck** runtimes, and `hosts:` scripts (code steps,
remote commands). (cli and acp runtimes on this box are jailed even with no
block — see [the agent workspace jail](#the-agent-workspace-jail).) A **paseo** runtime's agents are children of the paseo
daemon — conductor never holds that process, so `conductor validate` rejects
`isolation:` on paseo runtimes and on profiles that resolve to one, rather
than silently not isolating. Use paseo's own sandboxing there, or move the
profile to a runtime conductor launches.

## Code-step engines (sandboxed by default)

Connectors and runtimes follow the app-extension model: a plugin you added is
trusted, so with no `isolation:` block it runs under its permission manifest +
scrubbed env, not an OS jail. **Code-step ENGINES are the exception.** A `run: js`
step executes arbitrary code and the engine declares no capabilities, so
conductor sandboxes engine plugins **by default** — a `namespace`-mode sandbox
with the network denied. Tune or opt out per engine with the top-level
`engines:` block, keyed by engine name:

```yaml
engines:
  js:   {}                                             # default: sandboxed (namespace, network denied)
  lua:  { trust: full }                                # opt OUT — run unconfined (an engine you've audited)
  wasm: { isolation: { mode: user, user: sandbox } }   # explicit override (fail-closed)
```

The synthesized default is **best-effort**: where the OS sandbox can't be applied
(non-Linux, running as root, or `unshare` / unprivileged user namespaces
unavailable) the engine runs with a loud warning rather than failing — a code
engine that worked yesterday keeps working. An explicit `isolation:` block is
**fail-closed**, like everywhere else. Daemon-path masking is not part of the
default (an unprivileged `unshare` cannot reliably overmount the state/config
dirs), so the default leans on the network + pid namespaces — enough to confine a
pure-compute engine (no egress, no view of other processes); use an explicit
`isolation:` (or `mode: container`) if you need filesystem masking too.

## Modes

```yaml
x-templates:
  risky-fixer: &risky-fixer
    type: agent
    runtime: gemini
    isolation:
      mode: user | namespace | container
      user: sandboxagent                  # mode: user
      container: { image: agents:latest, engine: docker }  # mode: container
      limits: { memory: 2g, cpu: 200%, pids: 256 }
      network:
        egress: [ "api.github.com:443", "*.internal:443" ]
        deny: true    # + egress ⇒ ENFORCED allowlist (namespace/container);
                      # alone ⇒ structural no-network; absent ⇒ advisory proxy
      fs: [ "/srv/media" ]  # filesystem allow-list: extra paths the sandbox may
                            # see (namespace = a real pivot_root jail; container
                            # = -v binds). Empty ⇒ just the workdir.
      # privileged: true   # namespace mode: opt back into the daemon's full
      #                    # filesystem view (state/config masked by default)
      # allow_root: true   # namespace mode: run the sandbox even when the
      #                    # daemon is root (NOT a boundary then — see below)
```

### `mode: user` — a distinct low-privilege user

The launch is wrapped in `sudo -n -u <user> --`. Works on any Unix; the
conductor user needs a sudoers rule like:

```
conductor ALL=(sandboxagent) NOPASSWD: ALL
```

The sandbox user should own nothing but its own scratch space; the worktree
must be readable/writable by it (group membership or ACLs).

**Honest scope — what `mode: user` does and doesn't give you.**
It isolates the agent **from the daemon** (different uid → no reading
conductor's state, config, or memory). It does **not** isolate concurrent
dispatches **from each other** when they share the account: same EUID means
a sibling agent can read `/proc/<pid>/environ` (tokens included) and
signal/ptrace its peers. And any `egress:` allowlist under `mode: user` is
**advisory-only** — there's no network namespace, so only the proxy env
steers traffic; a runtime that ignores `HTTP(S)_PROXY` reaches the network
directly. `conductor validate` warns on both. For agent-vs-agent isolation
give each concurrent scope its own `user:`, or use `namespace`/`container`;
for an enforced allowlist use `deny: true` + `egress:` under
`namespace`/`container`.

One combination is refused outright (no override): a `skill:` profile whose
effective isolation is `mode: user`. The skill's one-shot claim code rides
the tool server's environment, and same-EUID siblings can read it from
`/proc/<pid>/environ` and race the claim — re-opening the broker-identity
hijack the claim flow exists to close. Use `namespace`/`container` (separate
`/proc` views) with `skill:`, or drop one of the two.

### `mode: namespace` — the OS-native least-privilege jail

`mode: namespace` is **portable**: it means "the OS's native least-privilege
jail", and conductor picks the backend by OS — **Linux user namespaces** here,
**macOS Seatbelt** (`sandbox-exec`) on a Mac. The config surface is identical
(`network:` / `fs:`), so the same YAML — including the confined-by-default a
pack gets — works on both; see [[#macos-seatbelt]] below for what differs. The
rest of this section describes the Linux backend.

The launch is wrapped in
`unshare --user --map-current-user --pid --fork --mount-proc --kill-child`:
its own user/pid/mount namespaces, so it cannot see sibling process tables
or `/proc/<pid>/environ` of other agents. With `limits:` set, a
`systemd-run --user --scope` prefix applies cgroup caps (`MemoryMax`,
`CPUQuota`, `TasksMax`). `network: {deny: true}` adds `--net`: the agent has
no network interface but loopback in an empty namespace — structural.

**Filesystem — two shapes.** A namespace keeps the daemon's own uid, so file
permissions alone would let the launch read everything the daemon can.
Conductor closes that off in one of two ways:

- **CODE steps (`use: cli`/`command:`, `run: <interpreter>`) get a real
  filesystem JAIL.** The launch is re-entered through conductor's own helper,
  which builds a fresh root, bind-mounts in **only** the allow-list — the
  workdir, the interpreter essentials (`/usr`, `/etc`, `/bin`…, read-only), a
  private `/proc` + `/dev` + `/tmp`, the step's own code/ctx sockets, and any
  `fs:` paths you declare — and `pivot_root`s into it. Everything else on the
  host, the daemon's state/config/secrets included, is **gone by absence** (not
  merely masked). The privilege that lets it mount is dropped before your code
  runs, so the code cannot pivot back out. This is a bwrap-style jail with **no
  docker required**; declare the paths a step legitimately needs with `fs:`.
- **Agent launches on a cli or acp runtime get the workspace jail** above —
  with or without a `mode: namespace` block. Other runtime and plugin launches
  (opencode, agent-deck, engine and connector plugins) instead **mask the
  daemon's state and config directories** (empty read-only tmpfs over
  directories, `/dev/null` over files); `privileged: true` opts out. There the
  rest of the daemon's uid view (its `$HOME`, other repos) is still visible —
  for a full jail on such a launch, use `mode: container` or `mode: user`.

A confinement that can't be applied fails the launch rather than running
unconfined — except a pack's *synthesized* default (below), which degrades
with a warning so a pack still runs on a box without namespaces.

`isolation: { mode: namespace, privileged: true }` is the deliberate
opt-in to the daemon's full filesystem view (no masking) — for a trusted
profile that genuinely needs the daemon's own files. It's the same
philosophy as `trust: full`: the strong posture is the default, the
footgun is explicit. Remote (`hosts:`) namespace wraps never mask (the
helper binary lives on this box) — the remote box's own account setup is
the wall there.

**Never a boundary as root — refused by default.** `--map-current-user` maps
the daemon's own uid into the new user namespace. When the daemon runs as
**root (euid 0)** that mapping is root→root: the sandboxed agent keeps real
uid 0 and full `CAP_SYS_ADMIN` over the host, so the user namespace confers
**no privilege separation at all** — masks, `--net`, and cgroup limits become
things a root agent can simply undo. Conductor therefore **refuses a
namespace-mode launch when euid is 0** with an error pointing you at a
non-root daemon user or `mode: container`. Run conductor as a dedicated
non-root user (the intended posture), or switch that profile to
`mode: container`.

`isolation: { mode: namespace, allow_root: true }` is the deliberate opt-in
to run the namespace sandbox as root anyway — valid **only** when you are
using the namespace for process/mount cleanup or cgroup limits and are **not**
relying on it as a security wall against the agent. Same philosophy as
`privileged:` and `trust: full`: strong-by-default, footgun explicit. It
applies to namespace mode only (`validate` rejects it elsewhere as a no-op).

The Linux backend needs user namespaces + `pivot_root`; the macOS backend
(below) needs `sandbox-exec`. On any other platform `conductor validate`
rejects `mode: namespace` (a remote `hosts:` entry skips the local check — the
remote box's OS applies).

#### macOS — Seatbelt {#macos-seatbelt}

On a Mac the same `mode: namespace` is realized by **Seatbelt**, Apple's kernel
sandbox, via `sandbox-exec -p <profile>` (shipped on every Mac; no root, no
Docker). conductor generates a deny-by-default SBPL profile from the SAME
allow-list the Linux jail uses:

- `(deny default)` → the daemon's config/state/secrets are unreadable (the
  macOS form of "hidden by absence" — deny-default denies metadata too, so
  `stat` is refused, not just reads).
- each `fs:` path + the workdir → `(allow file-read* file-write* (subpath …))`;
  the step's own code/ctx temp dirs are added read-only/read-write for you.
- `network: {deny: true}` → `(deny network*)`; open/advisory → `(allow
  network*)` (the advisory `HTTP(S)_PROXY` still steers a well-behaved runtime).

Two differences from Linux, both enforced at `validate`:

- **cgroup `limits:` are ignored** on macOS (no systemd/cgroups analog).
- **For code steps, an enforced egress allowlist (`deny: true` + `egress:`)
  is Linux-only** — the code-step profile has no forwarder. On macOS use
  `mode: container` there, or plain `deny: true` / an advisory `egress:`
  without `deny`. The **agent** jail enforces allowlists on macOS too
  (outbound confined to the proxy's loopback port; see above).

Seatbelt is a kernel sandbox, not a uid trick, so the "not a boundary as root"
caveat does not apply on macOS.

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
`namespace` only; the egress proxy lives on the daemon's box and doesn't
reach remote launches (validation rejects a remote `egress:` list — use
`deny: true` with namespace mode there).

A host named by `policy.agent_authored.host` **must carry an `isolation:`
block** — a "sandbox" host that doesn't isolate is a plain remote shell
wearing the name, so `conductor validate` rejects the combination.
`agent_authored: { trust: full }` is the documented opt-out: the same knob
that lifts the allow/approve/host gates lifts this requirement.

## Degradation

| Situation | Behavior |
|---|---|
| `mode: namespace` on macOS/Windows | rejected by `validate` (local) / launch error with a clear message |
| `mode: namespace` while daemon is root (euid 0) | launch refused (not a boundary as root) unless `allow_root: true` |
| `sudo` / `unshare` / `docker` missing | launch fails with "needs X on PATH", never silently unisolated |
| egress policy with no proxy wired | launch fails closed |
| enforced egress with no unix endpoint wired | launch fails closed |
| a default filesystem mask can't be applied | launch fails, never runs unmasked |
| isolation on a paseo runtime | rejected by `validate` |
| `skill:` + `mode: user` isolation | rejected by `validate` (claim theft under a shared uid) |
| `agent_authored.host` without `isolation:` | rejected by `validate` (`trust: full` opts out) |
| the default agent jail can't be built (root, no user namespaces, no `unshare`) | the agent runs unconfined, with a warning, a `jail degraded` audit row, and a watch event |
| an explicit `isolation:` block on a cli/acp runtime the box can't jail | launch fails closed (`validate`: error) |
| the jail's setup fails at launch | the turn fails with the sandbox's error (never reported as the agent's reply) |

## Audit

Every denied egress attempt is logged and audited as
`{event: egress_denied, target: host:port}`, so a sandboxed agent probing
the network is visible in `conductor report`'s audit trail.

A jailed agent's boundary crossings are audit rows and live `conductor watch`
events, attributed to the dispatch (`label: "fix acme/app#43"`):

| event | what |
|---|---|
| `jail` | the jail came up (with its host set), or `degraded` |
| `host_command` | a host command: `exit N` (with any discarded writes), or `refused` with the rule |
| `git_push` / `git_fetch` | brokered git: refs and old→new SHAs, or `refused` with the rule |
| `sign` | a commit signed for the dispatch, or `refused` |
| `tool_call` / `tool_result` | claude-code's tool calls (hooks), `refused` by an intent rule |
| `egress` | a destination reached (each once) or refused, attributed to the dispatch |
| `cancelled` | agents cancelled because their target merged or closed |
