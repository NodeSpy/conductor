# Agent isolation & sandboxing

Locally-dispatched agents historically ran as the same OS user as the
daemon — a sibling agent could read another's process table, files, and fds.
Two layers close that:

- **The agent sandbox** (on by default): every agent conductor launches runs
  read-only with respect to everything but its own workspace, and conductor
  performs every write — the push, the forge write, the cloud call. Covered
  first below.
- **The `isolation:` block**: per-dispatch isolation for any launch conductor
  performs itself (code-step engines, `hosts:` scripts, and runtimes you
  configure explicitly), plus a conductor-enforced network egress allowlist.
  (#36 §15.)

## The agent sandbox

**Agents are read-only; conductor writes.** An agent holds no push, forge or
cloud credential. It edits and commits in its workspace; conductor pushes the
commits (signed) and performs every forge or cloud write as a verb. Every
agent conductor launches — a `cli` or `acp` runtime's worker, an `opencode`
runtime's per-dispatch server, an `agent-deck` session's agent, a paseo
session started with a sandboxed provider entry ([[Paseo-Sandbox]]), a member
worker — runs under conductor's built-in sandbox: Linux user + mount + pid +
network namespaces with a `pivot_root` filesystem, or macOS Seatbelt. Design
and rationale: `docs/design/agent-sandbox.md`.

What the agent sees:

| | |
|---|---|
| its workspace | read-write (the dispatch's own clone, `.git` included) |
| `HOME` | its **identity's** tool state, read-write: `<state>/agent-state/<identity>/` — `~/.claude`, `~/.codex`, … — created before the launch and seeded once from your settings for the tool (never your history: `~/.claude.json`'s per-project history is dropped). The tool's login file is shared from your real home, so it is signed in as you. |
| the conductor socket | the socket file only (never the state dir it lives in), with a token minted for this launch — so `conductor call step.done` / `git.push` work |
| the base system | `/usr`, `/etc`, `/opt` (Linux) / the system paths (macOS), read-only; the agent CLI's own install, wherever it is |
| the network | **none of its own.** The only way out is conductor's allowlist proxy: the tool's model endpoints, plus `sandbox.egress`. A model endpoint on your host's loopback (a local router at `127.0.0.1:3456`, a local Ollama) is relayed in at the same address. |
| the environment | a minimal one: `PATH`, locale, terminal, the tool's model key and endpoint, your git name/email. No `GH_TOKEN`, `SSH_AUTH_SOCK`, cloud keys, or a launcher's own tokens. |
| everything else | **absent** — your `~/.ssh`, `~/.aws`, `~/.config/gh`, other projects, conductor's state and config. Nothing is denied by a list: what is not shown is not there. |

An **identity** is who the agent is: for a dispatch, its profile per (trusted)
repository — `agent:fixer@acme/app`; for a member, the member; for a directory
conductor did not create, the directory itself. Every launch of an identity —
a resume included — sees the same tool state, so a resumed session finds its
memory; no identity sees another's transcripts.

**It fails closed.** A launch whose sandbox cannot be built does not run: the
dispatch fails with the reason. conductor checks at boot and logs it when this
box cannot build one (root daemon; no unprivileged user namespaces — Ubuntu
23.10+ restricts them through AppArmor: `sysctl
kernel.apparmor_restrict_unprivileged_userns=0`; no `sandbox-exec`).

### opencode and agent-deck

**opencode.** conductor's opencode runtime drives opencode over its HTTP
server, one `opencode serve` per session. That server is the agent — its
tools run inside it — so it is launched in the sandbox under the dispatch's
identity, like a cli worker:

- it listens on the sandbox's own loopback. On Linux the sandbox has its own
  network namespace, so the port is fixed and the in-sandbox forwarder serves
  it to conductor over a unix socket it opens before the jail is built (the
  agent never sees that socket's directory). On macOS it listens on a free
  host port, the only one its Seatbelt profile lets it bind;
- it answers only with a per-session password (`OPENCODE_SERVER_PASSWORD`);
- its `HOME` holds opencode's state (`~/.local/share/opencode`, `~/.config/opencode`,
  `~/.local/state/opencode`, `~/.cache/opencode`): your `auth.json` is shared,
  your `opencode.json(c)` seeded once, never your sessions;
- opencode is multi-provider, so its allowlist follows what is configured:
  the provider the dispatch's model names (`anthropic/claude-…` → Anthropic's
  endpoints, `openrouter/…` → `openrouter.ai`, …), your opencode config's
  `model` / `small_model`, and a provider's `options.baseURL` — a local model
  server (`http://127.0.0.1:11434/v1`) is relayed in like a local router.
  With no model named anywhere, the providers you are signed in to (`auth.json`,
  a provider key in the environment) and those your config defines. Plus its
  model catalog (`models.dev`, `models.opencode.ai`). A provider opencode
  knows but conductor's table does not needs its host in `sandbox.egress`;
- the agent's shell gets the dispatch's conductor token, so `conductor call
  step.done` / `git.push` work from its bash tool, as well as through the
  conductor MCP server.

opencode also tries to install npm packages (plugins) and download language
servers at start; the sandbox denies both (logged, not fatal) —
`OPENCODE_DISABLE_LSP_DOWNLOAD` and `OPENCODE_DISABLE_AUTOUPDATE` are set. Its
own permission prompts still apply: a path outside the project
(`external_directory`) is an `ask` by default, which a headless dispatch has
no one to answer — set it in your opencode config's `permission:` (the sandbox,
not opencode's prompt, is the boundary).

**agent-deck.** agent-deck runs the agent in a tmux session of its own,
typing a shell command it builds — exports, its flags, then `exec … claude …`
— so conductor never holds the agent's process, and confining agent-deck
itself would confine the session manager, not the agent. The seam is
agent-deck's `--wrapper`: conductor's runs that whole command, as one
argument, through a shell inside `conductor sandbox exec`:

```
conductor sandbox exec --socket <control.sock> --tool claude-code --agent-cwd --bin claude -- \
  /bin/bash -c "$(cat <<'__CONDUCTOR_<random>__'
{command}
__CONDUCTOR_<random>__
)"
```

so every part of it runs sandboxed (a plain `sandbox exec -- {command}` would
confine only the first word of agent-deck's command and run the agent bare).
The session's workspace is registered with the daemon first, so `sandbox exec`
finds the dispatch's identity and token for it; `--bin` shows the tool the
inner shell runs; `--agent-cwd` refuses a directory that is not a workspace
instead of substituting a scratch one. A claude-code identity's home records
that workspace as trusted (claude's interactive UI otherwise stops at its
folder-trust question). It fails closed: the session is created stopped
(`agent-deck add`), its stored wrapper checked to be exactly conductor's, and
only then started and sent the task — an agent-deck without `--wrapper`
refuses `add`, one that keeps another wrapper is removed
unstarted, and a sandbox that cannot be built ends the session (`sandbox
exec` exits 125). The agent-deck CLI itself runs unsandboxed and gets no forge
credential.

### Pushing: conductor, not the agent

The agent commits; conductor pushes:

- **at the end of the step** — after its gate passes, before the diff preview
  and `expect_push` read what is unpushed — and
- whenever the agent runs **`conductor call git.push`** (auto-granted with
  `step.done`).

Either way conductor pushes **the workspace's `HEAD` to the dispatch's own
branch only** — the agent names no ref (`git.push` refuses any option) —
**fast-forward only, never forced** (a diverged remote is a refusal), and
**re-signs every new commit** with your SSH signing key on the way out. A
review (a decide step, an output-schema step, a checkout-less step, a review
trigger — unless `expect_push:`) is never pushed. Every push is audited
(`git_push`).

conductor never runs git in the agent's checkout with the agent's config: it
reads `HEAD` and the clone's own objects by path with `O_NOFOLLOW`, and pushes
from the base clone's trusted git dir with hooks, fsmonitor and the ext
transport off. Its diff read of a sandboxed workspace (the diff preview) runs
inside the sandbox too — the checkout's `.git/config` and `.gitattributes` are
the agent's. Each dispatch gets **its own clone** (objects borrowed from the
base clone through `alternates`), so one agent cannot move another's branch.

**The Verified badge.** Signatures verify `G` with your key locally. GitHub
shows "Verified" on a pushed commit only when the signing key is registered on
your account as an **SSH signing key** (Settings → SSH and GPG keys → New SSH
key → Key type: Signing Key) and the committer email is one of your verified
emails. conductor does not touch your account settings.

### Configuration

```yaml
sandbox:
  agents: on                       # default; `off` is the explicit opt-out (logged at boot)
  egress: [registry.npmjs.org, proxy.golang.org]   # beyond the model endpoints
  # state_dir: ~/.local/state/conductor/agent-state   # the identity homes
  push:
    signing_key: ~/.ssh/conductor_signing   # default: git's user.signingkey (gpg.format ssh)
    # name / email: default git's user.name / user.email
    # sign: false        # push unsigned (default: refuse to push without a key)
    # at_step_end: false # only git.push pushes
```

Per runtime, workflow, trigger or step, an `isolation:` block keeps working
(most specific wins: step → workflow/trigger → runtime): `mode: none` runs that
launch unwrapped (the explicit opt-out, below); `mode: user` / `container`, or
`namespace` with `privileged: true`, keep their own wrappers; a plain
`namespace` block shapes the agent sandbox (its `network.egress` joins the
allowlist, its `fs:` paths are shown read-write). A remote (`host:`) launch
runs under that box's own isolation.

### Hand-offs, and opting a workflow out

**Hand-offs are sandboxed.** An interactive hand-off (a `background: true`
agent step on paseo) launches through the provider's conductor-owned
`<provider>-sandboxed` entry, like every other paseo dispatch: the agent you
drive has its workspace, its identity's tool state and its model — not your
keys, your other projects, or a credentialed `gh`. Its paseo title says so
— `conductor: acme/app#7 review_requested [sandboxed]` — the agent carries
the label `sandbox=on`, and the daemon logs each verdict:

```
paseo hand-off conductor: acme/app#7 review_requested: sandboxed (provider claude-sandboxed)
```

**Detached launches are sandboxed too.** A `detach: true` step — the Slack
handover that turns a thread into a paseo workspace of yours — is seeded from
content conductor does not vouch for: a thread can carry anyone's messages,
which is a prompt-injection channel into an agent that then works as you. So
it launches through the sandboxed entry as well, titled `… [sandboxed]`,
labelled `sandbox=on`, and logged (`paseo detached launch <title>: sandboxed
(provider claude-sandboxed)`). It stays what a detached launch is — not
conductor's (never archived), with no conductor token — so its sandbox is the
default one for its own worktree: that workspace read-write, its own tool
state, the model, nothing else. The same `{mode: none}` opt-out below
applies, with the same rules about who may write it.

**`isolation: {mode: none}` is the explicit, loosening knob.** On a step, a
workflow, or a trigger (its default for every agent step it runs, the
workflows it calls included, unless a step sets its own), it launches the
agent **unsandboxed, as you**: on paseo through the *plain* provider — a
hand-off or a detached launch titled `[UNSANDBOXED]` — labelled
`sandbox=off`, and logged:

```
paseo hand-off conductor: acme/app#7 review_requested: UNSANDBOXED — isolation: {mode: none} (the operator's opt-out); plain provider claude
```

On a paseo step or runtime, `{mode: none}` is the only isolation that applies
(conductor does not wrap a paseo agent itself; it names the entry paseo
launches it with) — anything else is a `conductor validate` error.
`sandbox.agents: off` turns the sandbox off for everything.

**Only your own config can loosen it.** Your runtimes, your workflows and
triggers, and the overrides you write for a pack in your `packs:` block. **A
pack cannot opt its own steps out** — it can narrow its sandbox (`network:
{deny: true}`, `limits:`), never loosen it: a pack's step, workflow or
trigger carrying `mode: none` — or, for its agents, `mode: user`/`container`,
`privileged: true`, `network.egress` or `fs:` — is refused at load and by
`conductor pack lint`, naming the override to write instead:

```yaml
packs:
  review:
    use: conductor-packs/review
    steps:
      <workflow>/<step>: { isolation: { mode: none } }   # this step only (its ref in the pack)
    triggers:
      <trigger>: { enabled: true, repos: [acme/app], isolation: { mode: none } }  # the whole workflow it runs
```

These are the same overlays that route a pack step to your runtime or set
its gate. An agent-authored dispatch never carries an opt-out (it is
stripped with the rest of its inheritable grants).

### What the sandbox no longer does, and why

The agent jail this replaces (#160, #175) brokered credentialed work: a
host-command broker (profiles for `gh`, `aws`, `vop`, `terraform` …, with
per-org home narrowing), approval **grants** for exact commands, brokered git
through a remote helper, a **signing shim** that signed the agent's own
commits, and a copy-on-write home. None of it is carried over. With agents
read-only there is nothing for an agent to broker: it never runs a
credentialed command, so no profile has to decide which ones are safe, no
grant has to approve one, and no shim has to sign on its behalf — conductor
signs what it pushes, once, outside the sandbox. A credentialed write an agent
needs is a conductor verb (`git.push`, a connector's forge or cloud verbs),
authorized by the dispatch's token and audited. The surface that took six
review rounds to harden is gone rather than hardened.

### Limits

- **What conductor launches, or names.** Every runtime conductor launches
  locally is covered (cli, acp, opencode, agent-deck), and paseo through its
  sandboxed provider entries. A paseo session you start with paseo's own
  provider, or an agent-deck session you start from agent-deck's own UI, is
  that tool's, not conductor's. A remote (`host:`) launch runs under that
  box's isolation.
- **Registrations live in the daemon.** After conductor restarts, a
  workspace it registered is no longer known: an agent-deck session that
  agent-deck restarts then is refused (`--agent-cwd`), and a paseo session
  resumed there runs under that directory's own identity.
- **agent-deck's `waiting`.** When claude finishes a turn in agent-deck its
  status is `waiting` — also its status just before a turn starts — so
  conductor ends the step at `step.done`'s step boundary or the step's
  timeout, not on that status.
- A **blob:none** base clone fetches lazily; the sandbox has no network for
  that, so a blob the base never fetched (an old file version for `git blame`)
  is unavailable inside it.
- **Linked worktrees** (paseo's own worktrees for paseo dispatches) share their
  base checkout's refs: the sandbox shows the common dir's objects and refs
  writable (its config, hooks and alternates read-only, sibling worktrees
  hidden). conductor's per-dispatch clones (`cli`) do not have this.

### A browser for agents

A sandboxed agent can drive a real browser through `conductor browser …`
(`browser:` in the config). Conductor runs that browser in a sandbox of its
own:
- its only network is the egress proxy with `browser.allow`;
- its profile is anonymous;
- a logged-in site or a credential is used only for your own requests;
- files cross only through the workspace.

The agent never holds the browser. See [[Browser]].

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
`isolation:` on paseo runtimes and on steps that resolve to one, rather
than silently not isolating — except `{mode: none}`, which launches through
paseo's plain provider instead of the sandboxed entry ([above](#hand-offs-and-opting-a-workflow-out)).
For another sandbox, move the step to a runtime conductor launches.

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
- **Runtime/plugin launches** (an agent runtime's own process) instead **mask
  the daemon's state and config directories** (empty read-only tmpfs over
  directories, `/dev/null` over files); `privileged: true` opts out. Here the
  rest of the daemon's uid view (its `$HOME`, other repos) is still visible —
  for a full jail on a runtime launch, use `mode: container` or `mode: user`.

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
- **An enforced egress allowlist (`deny: true` + `egress:`) is Linux-only** —
  Seatbelt can cut the network wholesale but not run the in-sandbox forwarder.
  On macOS use `mode: container` for an enforced allowlist, or plain
  `deny: true` / an advisory `egress:` without `deny`.

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
- **Hosts are taken exactly as bound.** The proxy resolves names itself, and
  refuses an address in a private, loopback, link-local or metadata range
  unless the target names it exactly:
  - an IP-literal target matches the same literal listed (not a glob) for its
    port;
  - the target `localhost` reaches loopback when `localhost` itself is listed.

  So `localhost:3000`, `127.0.0.1:3000` and `[::1]:3000` are three entries,
  none implying another, and an allowlisted name that resolves (or rebinds)
  to an internal address is refused, even when that address is listed too.
  To reach an internal host, list its address and use it as the target.
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
| agent sandbox can't be built (root, no user namespaces, no `sandbox-exec`) | every agent launch refused with the reason; logged at boot |
| `conductor sandbox exec` with the daemon unreachable | refused (exit 125); the agent never runs |
| `conductor sandbox exec` in your home, `/`, or conductor's state | refused (a paseo utility launch with no agent directory gets a scratch workspace instead) |
| a push with signing on and no signing key | refused with the reason |
| `skill:` + `mode: user` isolation | rejected by `validate` (claim theft under a shared uid) |
| `agent_authored.host` without `isolation:` | rejected by `validate` (`trust: full` opts out) |

## Audit

Every denied egress attempt is logged and audited as
`{event: egress_denied, target: host:port}`, so a sandboxed agent probing
the network is visible in `conductor report`'s audit trail.
