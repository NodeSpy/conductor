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
| `HOME` | its **identity's** tool state, read-write: `<state>/agent-state/<identity>/` — `~/.claude`, `~/.codex`, … — created before the launch and seeded once from your settings for the tool (never your history: `~/.claude.json`'s per-project history is dropped; nor your claude-code plugins: `settings.json`'s `enabledPlugins`, `extraKnownMarketplaces`, `pluginConfigs`, `appendPlugins` and `prependPlugins` are dropped, since the identity home has no marketplace checkout and a launch would try to clone one through the egress proxy). Your model key never enters it (the model gateway, below); a subscription login is a copy without its refresh token. By default the home is fresh for each dispatch (`sandbox.persist`). |
| the conductor socket | the socket file only (never the state dir it lives in), with a token minted for this launch — so `conductor call step.done` / `git.push` work |
| the base system | `/usr`, `/etc`, `/opt` (Linux) / the system paths (macOS), read-only; the agent CLI's own install, wherever it is |
| the network | **none of its own.** Its model is conductor's model gateway, relayed in on loopback; everything else goes through conductor's allowlist proxy: `sandbox.egress` (and the provider hosts of a model the gateway does not carry), TLS only, per-launch caps. A model endpoint on your host's loopback (a local router, a local Ollama) is reached through the gateway, or relayed in at the same address when the gateway does not carry it. |
| the environment | a minimal one: `PATH`, locale, terminal, the gateway's URL and a per-launch token in place of the model key, your git name/email. No `GH_TOKEN`, `SSH_AUTH_SOCK`, cloud keys, or a launcher's own tokens. |
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
commit is new when no branch on the remote already has it: merging the base
branch in pushes the merge, not re-signed copies of the base's commits, and
the 500-commit cap counts only new commits. A
review (a decide step, an output-schema step, a checkout-less step, a review
trigger — unless `expect_push:`) is never pushed. Every push is audited
(`git_push`).

**A fixer that reports a structured result sets `expect_push: true`.** An
`output_schema` is what a reviewer returns, so on its own it makes the step a
review; a fixer that also reports one (the review threads it addressed, say)
says `expect_push: true` so conductor pushes its work.

**Committed work is never dropped silently.** When the end-of-step push pushes
nothing — the step is a review, the checkout has no branch, the push was
refused — and the agent left commits no remote has, conductor records it: the
step's `push` output says `pushed: false` with `unpushed_commits` and the
`reason`, the daemon logs it, and a `git_push` audit row has outcome
`unlanded`. The step then **fails** as `no_progress` (the work would be lost
with its workspace; `hook.failure.diff` carries it), unless it is an explicit
review — a review trigger, a decide step, `checkout: none` — or its agent is
unsandboxed and pushes for itself, or `sandbox.push.unlanded: warn` says to
only record it. A review that is one only by its `output_schema` fails: it is
most likely a fixer missing `expect_push:`.

The step's `push` output is what a later step can trust, never the agent's
own report: `{{.<step>.push.pushed}}` is true only when conductor pushed this
step's work to its branch — at the step's end, or by a `git.push` during it.
Gate anything that assumes the change landed on it (an `if:` like
`fix.push.pushed`); it is absent, so false, when conductor did not push.

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
  prefetch: [go]                   # Go modules fetched on the host, not by the agent (below)
  # state_dir: ~/.local/state/conductor/agent-state   # the identity homes
  push:
    signing_key: ~/.ssh/conductor_signing   # default: git's user.signingkey (gpg.format ssh)
    # name / email: default git's user.name / user.email
    # sign: false        # push unsigned (default: refuse to push without a key)
    # at_step_end: false # only git.push pushes
    # unlanded: warn     # commits left unpushed are recorded, not a step failure (default: fail)
    # deny_ci_changes: true    # optional extra: refuse a push that changes a CI definition (default: off)
  # The containment knobs below are all optional; the values shown are the defaults.
  # model_credential: gateway     # login-copy | login-share
  # egress_protocol_check: true   # TLS, HTTP/1 and HTTP/2 naming their host pass; false: any bytes (alias egress_require_tls)
  # egress_limits: { idle: 10m, upload: 512MiB, download: 20GiB, model_upload: 16GiB, model_download: 16GiB }   # each takes "off"
  # detect:
  #   action: kill                # alert | off
  #   tripwires: true
  #   interval: 3s
  #   allow: []                   # programs exempt in EVERY launch (prefer a scoped allowance)
  #   connections: alert          # a tunnel shaped like a remote shell or a bulk upload: close | off
  # allow: { tunnels: [], raw: [], reverse_shell: false, pty: false }   # base allowance of every launch;
  #                                 # scope one in an isolation: block (§ Scoped allowances)
  # limits:                       # Linux: a systemd user scope per launch
  #   memory: 50%                 # per launch (systemd syntax: 4G, 50%)
  #   pids: 8192                  # per scope: isolation: { limits: { memory, pids, cpu, cpu_weight } }; peaks in `conductor report`
  #   cpu: ""                     # per launch, systemd CPUQuota (400% = four cores); none by default
  #   cpu_weight: 0               # per launch, systemd CPUWeight 1-10000 among agents (100 is systemd's default)
  #   aggregate_memory: 75%       # every agent launch together
  #   aggregate_cpu: ""           # every agent launch together (CPUQuota on the slice)
  #   disk_free_min: 5%           # or a size (5GiB), or off
  #   required: false             # true: refuse a launch whose limits can't be applied
  #   off: false
  # persist: none                 # cache | home
  # git: { shared: protected }    # direct
  # macos_posix_ipc: false
```

What each of these protects, and what loosening it gives up, is in
[Containment and detection](#containment-and-detection) below.

**Go without an allowlist (`prefetch: [go]`).** Allowlisting
`proxy.golang.org` is not enough: the proxy answers many module downloads
with a redirect to `storage.googleapis.com`, and allowlisting that host opens
every Cloud Storage bucket to the agent. With `prefetch: [go]`, conductor
downloads instead, on the host:

- when it registers a dispatch's checkout — before the agent runs — it reads
  the module files (`go.work`, `go.work.sum`, and the `go.mod`/`go.sum` of
  every module the workspace uses or a local `replace` points at; regular
  files inside the checkout only) into a temp dir of its own and runs
  `go mod download` there, into `<state_dir>/.go-modcache` — one cache,
  **shared by every identity**;
- that download itself runs **confined**: the same namespace jail a code step
  gets, with the module cache the one writable path outside its own scratch
  dir and the network cut to exactly `proxy.golang.org` (never `direct`, so
  no VCS runs against a module path a `go.mod` names, and never the
  operator's own module cache as a `file://` layer — this ONE cache is
  shared by every identity, and a `file://` layer onto the operator's own
  `$GOMODCACHE` would pull whatever private modules it holds into it,
  available to every identity from then on). `GOTOOLCHAIN=local`, checksum-
  database verification on, none of the daemon's environment beyond `PATH`
  and HTTP-proxy/CA settings, and none of the operator's real `$HOME`. If
  this box can't build the jail (no `unshare`, for instance), the prefetch is
  skipped with a log line — it never falls back to an unconfined download;
- every sandboxed agent sees that cache **read-only** (an agent cannot plant
  a symlink for the host's `go` to write through) with `GOMODCACHE` pointing
  at it and `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`. Its Go builds
  and tests offline; no Go host goes in `egress`.

A set of module files already downloaded is skipped (a few milliseconds); a
failed download is logged and the launch runs anyway. A module the agent adds
itself is not fetched — its `go get` fails offline, by design — and a target
the event's sender chose is never prefetched for (its agent runs with what the
cache already holds).

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
  base checkout's refs: the sandbox shows the common dir's objects, refs, logs
  and lfs writable and everything else read-only (`sandbox.git.shared`,
  below; sibling worktrees hidden). conductor's per-dispatch clones (`cli`)
  do not have this.
- What containment does not stop is listed under
  [What is left](#what-is-left).

### Containment and detection {#containment-and-detection}

The sandbox decides what an agent can reach. The protections below assume
that an agent will still try: they keep what it can take, keep, or leave
behind small, and make the attempt visible. Each has a strict default and
one knob that loosens it.

**The model key stays on the host (`sandbox.model_credential`).** The agent's
model endpoint is conductor's **model gateway**: on Linux a unix socket
relayed into the sandbox at a loopback address; on macOS a conductor-owned
loopback port, the only loopback port the agent's profile allows. The agent
gets the gateway's URL and a random token minted for this launch, never the
key. The gateway:

- refuses any request without that token (`x-api-key`, `Authorization:
  Bearer`, `x-goog-api-key`, `api-key` or a `key=` query);
- swaps the token for your real key, read from the daemon's environment,
  claude-code's `settings.json` or codex's `auth.json`;
- forwards only to the configured upstream: a request cannot name another
  host.

It carries claude-code, codex, gemini, pi and qwen with an API key. A local
model router is always reached through it. When it carries every route of a
launch, the provider's hosts leave the allowlist and no login file is placed
in the home. A launch the gateway cannot carry falls back to `login-copy`:
opencode, or a subscription (OAuth) login with no API key. A request with a
wrong credential, or a token-less write, is refused and audited as
`sandbox_model_gateway_refused`. A token-less HEAD or GET carries nothing to
refuse: claude's connectivity probe (`HEAD /api/hello`) is answered by the
gateway itself, any other gets a quiet 401, and neither is forwarded.

Every request through the gateway is audited as `{event: model_request}`,
with the run, upstream host, method, path (never the query), status, bytes
each way and duration. All of a launch's gateways share one meter, with its
own caps: `sandbox.egress_limits.model_upload` and `model_download`, 16GiB
each by default, and each takes `off`. They are far above the egress caps
because a model client resends its whole context with every request. A
launch past a cap has further model requests refused (HTTP 429). That raises
one `sandbox_limit` event (`kind: model_upload` or `model_download`, action
`refuse`), which is also notified. The request that crosses a cap finishes,
so a launch can go over by at most one request and its response.

| `model_credential` | the agent gets | you give up |
|---|---|---|
| `gateway` (default) | a per-launch token; a login copy for what the gateway cannot carry | — |
| `login-copy` | the API key in its environment; the login file as an access-token-only copy (refresh tokens and stored API keys removed) | the key is readable inside the sandbox for the launch |
| `login-share` | the key in its environment; your login file bound **read-only** | your refresh token is readable inside the sandbox |

Your login file is never bound read-write. An access-token-only copy expires
with its access token: a long session signs out until your own CLI refreshes
the login. `login-share` avoids that, at the price in the table.

**Egress sessions (`sandbox.egress_protocol_check`, `sandbox.egress_limits`).**
Each agent launch gets its own egress proxy session, attributed to its run:

- **Protocol check.** The proxy reads a CONNECT tunnel's first bytes
  **before it dials** the upstream and lets through only a protocol that
  names its host:
  - TLS whose ClientHello server name (SNI) is the CONNECT host;
  - HTTP/1.x with exactly one `Host` header naming it;
  - HTTP/2 (prior knowledge) whose first request's `:authority` names it.

  Anything else is refused without a connection being made, with the reason
  named: SSH (`SSH-` banner), a ClientHello with no or another server name,
  an encrypted ClientHello (ECH), unrecognized bytes, or a client that sends
  nothing for 15 seconds (`unrecognized protocol`). A target in
  `allow.raw` (host:port, alias `non_tls`) skips the check, for SSH to one
  bastion or a database port. `egress_protocol_check: false` (alias
  `egress_require_tls`) turns the check off for the scope.
- **Audit.** Every request is audited as `{event: egress}`: target, SNI,
  protocol, bytes each way, duration, outcome and, for a refusal or closure,
  the reason. A plain-HTTP request also carries its method and path, never
  the query string.
- **Close on revoke.** When the step ends, its session is revoked and its open
  tunnels are closed, not only new requests refused.
- **Caps.** A tunnel with no bytes either way for `idle` (10m) is closed. A
  session over its `upload` (512MiB) or `download` (20GiB) cap has its tunnels
  closed and further CONNECTs refused. Each limit takes `off`. Model traffic
  through the gateway is not counted here: it has its own `model_upload` and
  `model_download` caps (above).

Plugin and code-step egress keeps its plain sessions: no TLS check, no caps.

**Git (`sandbox.git.shared`, `sandbox.push.deny_ci_changes`).** Your own git
trusts what is in a repository's `.git`; conductor's does not:

- **Shared git dirs.** In a linked worktree, only the shared git dir's
  objects, refs, logs and lfs are writable. Its config, hooks, info,
  submodule git dirs, alternates, replace refs and shallow file are
  read-only, and so are the worktree's own `.git` file, commondir and
  gitdir. `git.shared: direct` makes the whole shared dir writable except
  those protected paths.
- **A private view of the shared git dir.** Git rewrites config and
  packed-refs through a lock file and a rename beside them. Each launch
  therefore gets its own view of the shared git dir: a fresh copy of its
  config, packed-refs and HEAD, with the real objects, refs, logs and lfs
  inside. `git config --local`, `gc`, `pack-refs`, stash, rebase and
  deleting a packed branch all work, and your config and packed-refs are
  never written.
  - On Linux the jail mounts the view at the shared dir's own path.
  - On macOS `GIT_COMMON_DIR` points at it. git's ref store still uses the
    real dir there, so packed-refs and its lock and temp file are writable
    on macOS, like the refs beside them.
  - Linux, at launch end: a ref the launch packed into its private
    packed-refs is written back as a loose ref, so it is not lost. That
    applies to valid branch, tag, remote and notes names only, and never
    through a symlink or over an existing ref. A packed ref the launch
    deleted stays in your packed-refs (`git_refs_restored` lists what was
    written back).
- **A recorded layout is re-checked.** The registry conductor keeps of each
  workspace's git paths is re-validated when it is read back after a
  restart, and again when a later dispatch reuses it:
  - every path must pass the same limits as a fresh layout (not `/`, the
    home or above it, or conductor's own state);
  - a worktree's pair must still be git's own registration for the
    workspace;
  - the push dir must be a git dir;
  - every read-write path must lie in the workspace or a git dir.

  An entry that fails is dropped and the workspace is resolved anew.
- **Conductor's own git.** Conductor's git reads run hardened (no hooks,
  filters, fsmonitor or config includes from the workspace). It decides a
  workspace's layout, adoption and push target from what it recorded when
  it registered the workspace, never from an agent-writable file, and that
  registry outlives a restart. A later dispatch into the workspace reuses
  the recorded layout. A new one is bound only if git's own worktree
  registration points back at the workspace.
- **Pushes.** Conductor learns what a push carries from the remote
  (`ls-remote`), not from remote-tracking refs an agent could rewrite. It
  refuses a shallow git dir.
- **CI definitions (optional, off by default).** `push.deny_ci_changes: true`
  refuses a push whose commits change a CI definition (`.github/workflows`,
  `.gitlab-ci.yml`, `.circleci`, …; a merge counts only what it changes
  against every parent). This is not a sandbox protection: it guards what
  conductor pushes, and an agent can still change what CI runs through any
  script, Makefile, test or config file a pipeline calls. It also blocks real
  fixer work on CI itself. Turn it on only as an extra tripwire; the controls
  that matter are your CI's own (secrets not exposed to branch pipelines,
  required review before workflows run).
- **Untrusted targets.** Conductor never checks out, prefetches for or pushes
  to a repository the event's *sender* chose. A dispatch's repository comes
  from the platform's own assignment or the step's `repo:`, never from a
  webhook body.
- **The git guard.** It records the workspace's `.git` pointer, commondir,
  alternates, config and hooks at launch. When they change, it restores
  them, or moves what was added to `*.conductor-quarantined-*` (not
  executable), and reports it. A config is held only to its executable
  settings (fsmonitor, hooksPath, sshCommand, filters, textconv, aliases,
  includes, credential helpers, …), so an agent's `user.name` stands.

**Detection (`sandbox.detect`).** A host-side watcher follows every process
of each launch: its root process, which the launcher names by pid and start
time when it starts it, the processes carrying a random per-launch marker in
their environment, and the descendants of both. It scans every `interval` (3s).

It judges a program by **what its binary is**, not its name. At its first
scan it indexes this machine's shells, interpreters and tunnel tools by file
identity, and by size and SHA-256 for copies. A launch process's executable
(`/proc/<pid>/exe` on Linux, lsof's path on macOS) is matched against that
index:
- a copy of bash renamed `./esbuild` is reported as bash;
- a copy of socat renamed `./dep-sync` as socat;
- an allowance for `ssh` does not cover a copy of socat named `ssh`.

A binary this machine does not have (one the agent compiled or downloaded)
has no identity. The egress proxy's connection signal below is what covers
it. macOS refuses to run a copy of a platform binary at all.

The watcher looks for:

| kind | what |
|---|---|
| `tunnel_tool` | ssh, socat, nc, chisel, websocat, cloudflared, ngrok, `openssl s_client`, tor, …, by name or by binary |
| `reverse_shell` | a shell (sh, bash, dash, zsh, busybox, …) holding a TCP/UDP socket on **any** descriptor, or an interpreter (python, perl, node, …) whose stdin or stdout is one. An interpreter's own sockets are its work: pip and npm hold them. |
| `pty_relay` | Linux: a process other than the agent itself holding a pty master and an inet socket (the Linux sandbox has no pty devices, so this guards a jail that has one) |
| `tripwire` | a read of a decoy credential in the home: `.ssh/id_ed25519`, `.ssh/id_rsa`, `.git-credentials`, `.vault-token`, `.pypirc` |
| `git` | the git guard undid a change |

The tripwires are named pipes. A reader blocks until the watcher answers it
with a believable fake, so every read is seen, however quick. The decoys are
files no build, test or agent CLI reads. Cloud, docker, kube and netrc
credentials are deliberately not decoys, because a project's own tests read
them.

The response is `detect.action`:

- **`kill`** (default): every process of the launch is killed (on Linux
  through a pidfd, after checking the process is still the one seen), so the
  step ends with its agent. The workspace is kept. The event is audited
  (`sandbox_detection`) and raised as a `conductor.escalate` notification
  ([[Notifications]]).
- **`alert`**: audited and notified only.
- **`off`**: no watcher, tripwires or git guard.

**Connections (`sandbox.detect.connections`).** The watcher scans every
few seconds. A one-shot upload, or a tunnel in a program the watcher does
not know, can be over before a scan. The egress proxy sees every
connection as it happens. Every `egress` row logs each tunnel's features:
- duration;
- bytes each way;
- the longest silence (`max_idle_ms`);
- the turns where the far end spoke first after 5s or more of silence
  (`server_first`).

The proxy flags two shapes while the tunnel is open, whatever program holds
it:

| shape | when |
|---|---|
| `interactive` | open 2 minutes or more, under 4 MiB in all, and the far end spoke first after a silence at least 5 times: a remote operator's commands |
| `bulk_upload` | 16 MiB or more sent and at least 8× what was received: one upload, however quick |

A flagged tunnel is a `sandbox_detection` (`kind: tunnel_shape`, with the
shape and features) and is notified like any detection. The default,
`connections: alert`, leaves the tunnel open. The reason: timing cannot
tell a remote operator from a server that answers every request slowly
(long polling, a websocket), and closing a build's connection on that guess
would break real work. `connections: close` also closes the tunnel. `off`
reports nothing, though the features are still logged. A tunnel an
allowance covers (a `tool@host` or `raw` target, or every tunnel when a
tool is allowed by name alone) is audited as `allowed`. Model traffic goes
through the gateway, not the proxy, and is not judged here.

`detect.allow: [ssh]` exempts a program in every launch, and
`tripwires: false` removes the decoys. To let one workflow or step use a
tunnel tool, raw TCP to a host, or weaker detection while the rest keep the
defaults, use a [scoped allowance](#scoped-allowances).

Whatever the action, when a launch ends the watcher kills whatever of it is
left (audited as `sandbox_leftover_killed`). That includes a daemonized child
and anything in a process group a launch process led, so a tunnel started in
the background does not outlive its step. A `conductor sandbox exec` launch
(a paseo session) sends its events and every egress request to the daemon's
audit log over the control socket (its events tagged `via: sandbox-exec`),
and prints its refusals and detections on its stderr.

**Resource limits (`sandbox.limits`).** On Linux each agent launch runs in a
systemd user scope capped at `memory` (50%) and `pids` (8192) tasks, inside
one `conductor-agents.slice` that caps every agent launch together at
`aggregate_memory` (75%). Only `systemd-run` sees the launcher's
`XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`, which it needs to reach the
user manager. They are removed again before the agent starts, so the agent
never sees the bus. Each scope is bound (`BindsTo=`, `After=`) to the unit
its launcher runs in: conductor's service for a daemon launch, the paseo
unit for `sandbox exec`. Stopping that unit (a restart or an upgrade) stops
the scope and everything in it, `setsid` children included, as when agents
lived in the launcher's own cgroup.

Each conductor process checks once whether the scope works, by running the
launch's own prefix around `true`. When it does not (no user manager, no bus
reachable), launches run uncapped in the launcher's cgroup, after a one-time
warning (`sandbox_limits_unavailable`). `required: true` refuses them
instead. `off: true` turns the defaults off (a scope's own `limits:` below
still apply). macOS has no equivalent.

**How close launches come to the caps.** Each capped launch's scope is a
named unit (`conductor-agent-<id>.scope`). While it runs, conductor reads its
cgroup every 250ms: `memory.peak` and `pids.peak` against `memory.max` and
`pids.max`, plus `memory.events` (`oom_kill`) and `pids.events` (`max`).
When the launch ends, one `sandbox_usage` audit row records them:

```json
{"event":"sandbox_usage","run":"<dispatch>","agent":"workflow:review/review-team/review[0]",
 "memory_peak":1932735283,"memory_max":16777216000,"pids_peak":212,"pids_max":8192,"source":"cgroup"}
```

- A launch that reached **80% of either cap** gets `near_cap: true` and a
  `warning`. The daemon logs one line: `sandbox: warning: launch used memory
  14.1GiB of 15.6GiB (90%): raise sandbox.limits for this scope before the
  cap bites (run …, agent …)`.
- A launch the **memory cap killed** gets `killed_by: memory`. A
  `sandbox_limit` event (`kind: memory_cap`, `action: killed`) is raised and
  sent to the operator. It comes from the cgroup's `oom_kill` count, or from
  the scope's own systemd result (`oom-kill`), which conductor reads once the
  launch ends and then resets.
- Forks the **task cap refused** give `pids_refused: N` and a
  `sandbox_limit` (`kind: pids_cap`, `action: refused`). The task cap does
  not kill anything: the launch's `fork` calls fail.
- **Sampling.** The peaks are the kernel's own high-water marks. A kernel
  without `memory.peak` (before 5.19) or `pids.peak` gives the current value,
  sampled every 250ms.
- **No cgroup.** On macOS, or on Linux with no user manager, there are no
  caps. The row's `source: processes` is the watcher's sampled maximum: the
  launch's resident memory and tasks summed at each scan. It has no `*_max`
  fields, and a burst between scans is not seen.

`conductor report` lists every agent with its highest peak against its cap
over the window:

```
sandbox caps (highest peak per agent vs its cap; near = launches at >=80% of a cap):
  agent                                    launches     memory peak / cap tasks peak / cap  near  capped
  autopilot/on_failing_checks/0                  21      14.1G/15.6G  90%   310/8192   4%     3   mem×1
  workflow:review/review-team/review[0]           6       1.8G/15.6G  12%    212/8192   3%     0
```

**Tuning the caps per scope.** Set `limits:` with no `mode:` on the
runtime, workflow, trigger or step whose launches need different caps. It
takes `memory` (systemd syntax: `16G`, `40%`), `pids`, `cpu` (a CPUQuota
such as `150%`), `cpu_weight` and `disk_free_min`, and overrides
`sandbox.limits` for that scope's agent launches. It works on paseo steps too,
and still applies where `sandbox.limits.off` is set:

```yaml
workflows:
  build-heavy:
    isolation:
      limits: { memory: 24G, pids: 16384 }   # these launches peak near the 50% default
    steps:
      - { id: fix, type: agent, prompt: "Fix the failing build." }
```

Raise a scope whose `near` count keeps growing, or that shows `mem×N`. Lower
one whose peak sits far below its cap, so a runaway in it stops sooner. The
slice's `aggregate_memory` still caps all agents together. A pack may set
`limits:` on its own steps only within the defaults: at most 8192 tasks, and
memory as a size or at most 50%, `cpu_weight` at most 100. Higher is the
operator's to write, from the `packs:` block.

**CPU.** There is no CPU cap by default. `cpu: 400%` caps one launch at four
cores (systemd `CPUQuota`), and `aggregate_cpu` caps all agents together on
the slice. `cpu_weight` (1–10000) sets a launch's share **among the other
agent launches** in `conductor-agents.slice` when the CPU is contended. It
cannot make agents yield to your own work outside the slice, which is why
there is no default; use `aggregate_cpu` for that. With a `mode:` (a code
step's own wrapper), `limits.cpu` keeps its older meaning for that wrapper.

**The disk guard.** It refuses a launch when its workspace or home
filesystem has less free space than `disk_free_min` (5%, or a size like
`5GiB`). While a launch runs, it stops the launch when that filesystem falls
below the minimum, whatever the detection action. This applies on Linux and
macOS. `disk_free_min: off` turns it off.

**Ephemeral homes (`sandbox.persist`).** By default each dispatch's agent gets
a fresh home of its own, seeded like an identity home. The home is kept for
the dispatch's own resumes and swept a day after its session ends, so
nothing one run plants (a settings hook, a shell rc file, a poisoned build
cache) reaches the next.

- `persist: cache` keeps a per-identity cache dir (`~/.cache`, and
  `~/Library/Caches` on macOS) across dispatches. A run can then plant
  cache entries later runs execute.
- `persist: home` keeps the whole identity home, as before.
- A launch with no dispatch (a bot's home session, your own paseo session)
  keeps its identity's home.

**IPC.** A Linux sandbox has its own System V IPC namespace. On macOS the
agent's profile grants no POSIX shared memory or semaphores, because they are
named host-wide. `sandbox.macos_posix_ipc: true` grants them (Python
`multiprocessing` needs them).

**macOS.** Seatbelt has no network namespace, so the model is reached through
the gateway's own loopback port and the profile allows no other loopback port
but those of a model the gateway does not carry. A server the agent exposes
listens on the host loopback at exactly its port. The watcher reads `ps` and
`lsof`, and kills by pid after checking the process's start time. macOS
hides the environment of platform and hardened-runtime binaries
(`sandbox-exec`, `ssh`, `sh`, most signed agent CLIs), so there the launch
is followed from the root its launcher named.

#### Scoped allowances {#scoped-allowances}

Some work needs what the defaults stop, such as a deploy that opens a
`cloudflared` tunnel or raw `ssh` to a bastion. An **allowance** lets one
scope do that while every other agent keeps the strict defaults.

Write an allowance in an `isolation:` block of a runtime, workflow, trigger
or step. It applies to that scope's agent launches. `sandbox.allow` is the
base for every launch, and a scope's allowance adds to it.

```yaml
workflows:
  deploy-preview:
    isolation:
      allow:
        tunnels:                          # tunnel tools the watcher lets run
          - cloudflared@*.trycloudflare.com # name@host[:port]: also joins this scope's egress
          - ssh@bastion.example.com:22
        raw: [bastion.example.com:22]     # no protocol check for these targets (alias non_tls)
        reverse_shell: false              # a shell on a network socket (default false)
        pty: false                        # a pty relayed to a network socket (default false)
      detect: { action: kill, tripwires: true }   # per-scope override of sandbox.detect
      egress_limits: { idle: 30m }                # per-scope override of sandbox.egress_limits
    steps:
      - { id: publish, type: agent, prompt: "Publish a preview and report its URL." }
  review:                                 # no allowance: cloudflared or ssh here is killed
    steps:
      - { id: look, type: agent, prompt: "Review the change." }
```

How it works:

- **Narrow by default, broad if you write it.** A tunnel names a program.
  With `@host[:port]`, that destination joins the scope's egress allowlist
  (a bare host means 443; globs work as in `egress:`). `raw` entries
  need an explicit port and must also be in the scope's egress. `*` is
  accepted when you write it.
- **A tunnel's destination is open to every process of the scope.** The
  egress proxy sees connections, not which process made them. So
  `cloudflared@*.trycloudflare.com` lets `curl`, a script or any other
  program in that scope's launches reach `*.trycloudflare.com`, not only
  `cloudflared`. `name@*` opens egress to everything for all of them.
  Write the narrowest host you can, and give the allowance to the
  narrowest scope (a step rather than its workflow).
- **An allowance covers the tool, not a shell under its name.** The watcher
  knows the host's shells and tunnel tools by their binaries, so a copy of
  bash named `ssh` is still bash. A tunnel allowance covers only a binary
  that is the tool: the host's installed one, or a copy of it. A binary
  the host does not have, named `cloudflared`, is not covered, so install
  the tool on the host rather than letting the agent download it. A process is allowed only when the
  allowance covers every verdict on it: whatever it is called, a program
  with a network socket on its stdin or stdout is judged a reverse shell,
  and one relaying a pty to a socket is a pty relay. Neither is ever
  audited as an allowed tunnel.
- **A block with only sandbox knobs needs no `mode:`.** That is `allow:`,
  `detect:`, `egress_limits:`, `egress_only:`, `egress_protocol_check:`,
  `persist:` and `limits:` ([every knob](#knobs-global-and-per-scope)). It layers onto the scope outside it and keeps that scope's
  mode, network and paths. It works on paseo steps and runtimes too. A
  block with a `mode:` replaces the outer one whole, as always. The most
  specific setting of each knob wins (step over trigger or workflow over
  runtime), so a step's `allow:` replaces its workflow's.
- **An `allow:` does not cross a `workflow:` call.** A trigger's or
  workflow's allowance reaches its own agent steps, never the steps of a
  workflow it calls, a pack's workflow included. A called workflow that
  needs one declares its own. The rest of the caller's isolation (mode,
  network, `detect:`, `egress_limits:`) is still inherited, as before.
- **Recorded workspaces follow your config.** A workspace records the
  scopes its allowance came from, not the allowance. Every launch there
  (a resume, `sandbox exec`) recomputes it from the config the daemon runs
  with, and the daemon rewrites its registry the same way when it starts.
  Removing an `allow:` or a grant tightens every recorded workspace at once.
- **Allowed is still audited.** An allowed detection is a
  `sandbox_detection` row with `action: allowed` and `allowed_by: <scope>`,
  for example `workflow deploy-preview` or `sandbox + workflow
  deploy-preview`. It is logged but not escalated. A tunnel that skipped
  the TLS check is an `egress` row with `allowed_by`.
- **Everything else still applies to an allowed launch.** That covers: no
  credentials, the model gateway, the step-lifetime kill and leftover kill,
  close-on-revoke, the caps, the git protections, the tripwires (unless the
  scope's `detect:` turns them off) and the git guard.
- **Only your config grants allowances.** An agent-authored workflow's
  `allow:`, `detect:` and `egress_limits:` are ignored. Nothing an event
  carries reaches them.
- **Packs request allowances; you grant them.** A pack cannot write
  `allow:` (or weaken `detect:` or `egress_limits:`) itself. It declares a
  request in its manifest, and the request applies, to the steps it names
  only, once you grant it. A grant is pinned to the content you reviewed:
  `conductor validate` prints each request with the exact grant to write,
  `name@digest`. The digest covers what the request allows, the steps it
  names, and each of those steps as the pack ships it: its prompt, command,
  code, run and options, the whole step definition (not the reason's
  wording, and not your own `steps:` overlay):

  ```yaml
  # the pack's manifest
  allowances:
    tunnel:
      reason: publishes a preview through a tunnel
      steps: [deploy/publish]
      allow: { tunnels: [cloudflared@*.trycloudflare.com] }

  # your config
  packs:
    previews:
      use: …
      grant: [tunnel@<digest>]   # exactly as validate prints it
  ```

  A grant without a digest is refused, and the message gives the digest to
  write. If a later version of the pack changes a granted allowance (more
  tools, other hosts, other steps) or rewrites a granted step (its prompt,
  its command or code), the config **fails to load** with what changed, until you review it and write the new digest. This is separate
  from the pack lock's drift warning, which only warns. A file the step
  reads at run time from elsewhere in the pack (a script it runs, say) is
  not in the step's definition: the lock's drift warning is what reports
  that change.

  You can also write an allowance on a pack's step yourself:
  `packs: <instance>: steps: <ref>: { isolation: { allow: … } }`.
- **`conductor validate` lists every loosening.** That includes:
  - `sandbox.allow`;
  - each scope's allowance, weakened `detect:` and lifted egress limit;
  - each pack grant, with the digest it is pinned to;
  - each pack request you have not granted.

`sandbox.detect.allow` (older, global) exempts a program from every rule
in every launch, matched by what its binary is. A binary the host does not
know, named after an exempt program, is not exempt. Prefer a scoped `tunnels:` allowance.

#### Every knob, globally and per scope {#knobs-global-and-per-scope}

Each knob below has a global default under `sandbox:` and can be set
tighter **or** looser for one runtime, workflow, trigger or step in an
`isolation:` block with no `mode:`. The most specific setting wins (step
over trigger or workflow over runtime). Only your config sets them: an
agent-authored workflow's knobs are ignored, and a pack may only tighten
(the last column; anything looser is yours to write in the `packs:` block).

| knob | default | global key (`sandbox.`) | per-scope key (`isolation.`) | tighten | loosen | a pack may |
|---|---|---|---|---|---|---|
| egress allowlist | model hosts only | `egress` | `egress_only` replaces it; `network.egress` (with `mode: namespace`) adds | yes | yes | narrow only: its `egress_only` is intersected with what it inherits |
| protocol check | on | `egress_protocol_check` | `egress_protocol_check` | yes | yes | no `false` |
| TLS inspection (enforce `https://` paths) | none | `inspect` | `inspect` (adds to the global list) | — | yes | no |
| raw (unchecked) targets | none | `allow.raw` | `allow.raw` (adds to the base) | keep the base empty | yes | by a granted request |
| tunnel tools, reverse shell, pty | none | `allow`, `detect.allow` | `allow` (adds to the base) | keep the base empty | yes | by a granted request |
| egress idle / upload / download | 10m / 512MiB / 20GiB | `egress_limits` | `egress_limits` | yes | yes (`off`) | lower only |
| model upload / download | 16GiB / 16GiB | `egress_limits.model_*` | `egress_limits.model_*` | yes | yes (`off`) | lower only |
| memory per launch | 50% | `limits.memory` | `limits.memory` | yes | yes | a size, or at most 50% |
| tasks per launch | 8192 | `limits.pids` | `limits.pids` | yes | yes | at most 8192 |
| CPU per launch | none | `limits.cpu` | `limits.cpu` | yes | yes | yes |
| CPU weight among agents | none (systemd's 100) | `limits.cpu_weight` | `limits.cpu_weight` | yes | yes | at most 100 |
| free disk floor | 5% | `limits.disk_free_min` | `limits.disk_free_min` | yes | yes (`off`) | 5% or more, never `off` |
| all agents together | memory 75%, CPU none | `limits.aggregate_memory`, `aggregate_cpu` | — (one slice for every agent) | global | global | — |
| detection action | kill | `detect.action` | `detect.action` | yes | yes (`alert`, `off`) | no weaker action |
| tripwires | on | `detect.tripwires` | `detect.tripwires` | yes | yes | no `false` |
| watcher interval | 3s | `detect.interval` | `detect.interval` | yes (down to 100ms) | yes | 3s or faster |
| connection shapes | alert | `detect.connections` | `detect.connections` | yes (`close`) | yes (`off`) | not `off` |
| persisted home | none | `persist` | `persist` | yes | yes (`cache`, `home`) | `none` only |
| model credential | gateway | `model_credential` | — | global | global | — |

**Egress is host:port.** The allowlist decides by host and port. An HTTPS
request's path is encrypted, so conductor cannot filter it without
intercepting TLS: allowing `registry.example.com` over HTTPS allows every
path on it, and an `https://host/path` entry is a `validate` error that
says to list the host alone, or to add the host to `inspect:` (below).
Plain HTTP is different: an `http://host:port/prefix/` entry restricts
that host to the paths under the prefix (below).

A locked-down workflow: one registry, small caps, kill on any detection.

```yaml
workflows:
  dependency-bump:
    isolation:
      egress_only: [registry.npmjs.org]    # replaces sandbox.egress; the model is still reached
      limits: { memory: 4G, pids: 1024, cpu: 200%, cpu_weight: 50 }
      detect: { action: kill, tripwires: true, interval: 1s, connections: close }
      egress_limits: { upload: 64MiB, download: 2GiB }
      persist: none
```

An opened-up step: broad egress, raw SSH to one host, bigger caps, a kept
cache.

```yaml
      - id: release
        type: agent
        isolation:
          egress_only: ["*.github.com", "*.githubusercontent.com", "*.npmjs.org", "*.pypi.org"]
          allow:
            tunnels: [ssh@deploy.example.com:22]   # ssh may run; its host joins egress
            raw: [deploy.example.com:22]           # SSH is not TLS: skip the protocol check here
          limits: { memory: 70%, pids: 16384, cpu: 800% }
          egress_limits: { upload: 4GiB, download: off }
          persist: cache
        prompt: "Cut the release and deploy it."
```

#### A local package mirror {#local-package-mirror}

A mirror on your network (a Go module proxy, a PyPI or npm cache) lives in
private address space, which the proxy refuses by default. An **exact**
entry reaches it:

```yaml
sandbox:
  egress:
    - "http://mirror.lan:3000/goproxy/"   # plain HTTP, only paths under /goproxy/
    - "http://mirror.lan:3000/npm/"       # ...and /npm/ on the same host
    - pypi.mirror.lan:443                 # HTTPS: the whole host (host:port only)
    - "10.20.0.0/24:8081"                 # a literal address in this range, port 8081
  # allow: { raw: ["db.lan:5432"] }       # a non-HTTP protocol to one host:port
```

- **Exact names and literal addresses reach private space; wildcards never
  do.** A name listed exactly (no `*`, `?` or `[`) may resolve to a loopback
  or private address, because you named that host. `*.lan` never reaches
  private space. A CIDR entry or a literal IP entry matches only a literal
  address as the target. Link-local addresses, cloud metadata
  (`169.254.169.254`) among them, are reached only by listing that literal
  IP and using it as the target.
- **Plain-HTTP path entries** (`http://host[:port]/prefix/`, port 80 by
  default) are checked on every request of a keep-alive connection. The
  path is decoded before matching (`%2F` counts as `/`, so npm's scoped
  packages work), and a request with `.` or `..` segments, an empty
  segment (`//`), a leftover `%`, a backslash or a NUL is refused. The
  request is forwarded unchanged. A CONNECT tunnel to a path-restricted
  host is refused, since the proxy could not see its paths. The prefix
  `/` allows the whole host. Each request is audited with its method and
  path; the query string is never logged.
- Point the tools at the mirror the usual way (`GOPROXY=http://mirror.lan:3000/goproxy`,
  an `.npmrc` registry, `PIP_INDEX_URL`); they reach it through
  `HTTP_PROXY` as every other request does.

#### TLS inspection for a host (opt-in) {#tls-inspection}

To restrict an HTTPS host to some paths, conductor has to read its
requests, which means terminating the agent's TLS. `inspect:` opts one
host into that. Nothing is inspected unless you list it.

```yaml
sandbox:
  egress: [registry.npmjs.org]
workflows:
  dependency-bump:
    isolation:
      inspect:
        - { host: npm.corp.lan, ca: /etc/ssl/corp-root.pem }   # the mirror's own CA, for the upstream check
      egress_only: ["https://npm.corp.lan/@myorg/"]            # this mirror, /@myorg/ only
      limits: { memory: 4G, pids: 1024 }
      detect: { action: kill }
```

What happens for a launch whose scope inspects a host:

- **A CA for this launch only.** At launch conductor mints an ECDSA P-256
  CA, valid for 24 hours and name-constrained to the inspected hosts (no
  IP addresses). Its key stays in conductor's memory. The sandbox gets a
  read-only bundle of the system roots plus that CA's certificate, named by
  `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`,
  `CURL_CA_BUNDLE` and `GIT_SSL_CAINFO`. A launch that inspects nothing
  gets none of this.
- **Per request.** For an inspected host the proxy presents a leaf for
  that host, minted on first use, and reads each request, whether it comes
  over HTTP/1.1 keep-alive or as an HTTP/2 stream. Every request's path is
  checked against the host's `https://` entries, with the same rules as
  plain HTTP: the path is decoded, and dot segments, `//`, a leftover `%`,
  a backslash or a NUL are refused. A request whose `Host` names another
  host is refused. A websocket upgrade is allowed only on an allowed path.
  A host listed by `inspect:` and also allowed whole (a bare `host` entry)
  is inspected and audited, with any path allowed.
- **Upstream.** Each request goes on over a new TLS connection to the
  host, which is verified against the system roots plus the host's `ca:`
  file. Verification cannot be turned off, and an upstream whose
  certificate fails it is refused (502, audited `upstream certificate
  refused`). Requests are forwarded as sent. Only hop-by-hop headers
  (`Connection`, `Keep-Alive`, `TE`, `Upgrade` outside an upgrade) are
  handled by the proxy, as any HTTP proxy does. No `X-Forwarded-For` is
  added. The upstream leg is HTTP/1.1.
- **Audit.** Each request is an `egress` row with `inspected: true`,
  method, host and path. The query string, headers and bodies are never
  logged. The connection itself is a row too, and keeps the session's
  caps, idle limit, connection shapes and close-on-revoke.
- **Clients that refuse the CA.** A client with a pinned certificate, or
  one that uses only the platform's trust store, fails the handshake. The
  failure is audited as `client rejected inspection CA`. On macOS that
  includes Go programs and anything using the system keychain, which ignore
  these variables. On Linux, Go reads `SSL_CERT_FILE`. Such a client can
  still reach the host only if you allow it uninspected, which means
  dropping it from `inspect:`.
- **Every other host stays a blind tunnel**: the client sees the server's
  own certificate, and conductor sees host, port, timing and byte counts,
  nothing more.

The tradeoffs, plainly:

- **conductor reads that traffic.** Tokens, cookies and bodies to an
  inspected host pass through conductor's process in clear text. None of
  them are logged or stored, but a bug in conductor could expose them. The
  host's own end-to-end TLS guarantee is replaced by two TLS legs with
  conductor in between.
- **The agent trusts a new CA, but only a narrow one.** The CA can sign
  only for the inspected names (and, as X.509 name constraints work, their
  subdomains). It expires with the launch's day, and its key never enters
  the sandbox. A forged certificate for any other host fails verification.
- **Some clients break.** See pinned and platform-store clients, above.
- **Only HTTP is understood.** Another protocol inside TLS to an inspected
  host fails as a malformed request.
- **Cost.** Every request is parsed and re-sent, and the upstream leg is
  HTTP/1.1. That is fine for a package mirror; it would not suit bulk
  transfers.

Only the operator sets `inspect:`, in `sandbox:` or an `isolation:` block.
Wildcards, addresses and ports are `validate` errors, a pack may not set
it, and an agent-authored workflow's is ignored. `conductor validate`
lists every inspected host with its scope.

#### What is left {#what-is-left}

These are known gaps, stated so you can weigh them:

- **Domain fronting.** The TLS check proves the tunnel's server name is an
  allowed host, not who answers inside it. A CDN that serves many sites
  behind one allowed name can carry traffic to another of its customers.
  Allow narrow hosts, not a CDN's shared ones. An
  [inspected](#tls-inspection) host is the exception: each request's
  `Host` must be the inspected name.
- **Inspected traffic.** For a host in `inspect:`, conductor's process sees
  the requests in clear text (never logged), and the launch's CA could sign
  for the inspected names' subdomains too, as X.509 name constraints
  work. Its key never leaves conductor's memory.
- **Inside the allowlist.** An agent can send what it reads to any allowed
  host: a package registry it can publish to, an issue tracker, a paste
  site. The upload cap bounds the volume, the audit records it, and the
  allowlist is the control.
- **Linked worktrees.** Refs stay writable in the shared git dir, so an
  agent can move or forge a ref (a branch tip, a loose ref) the base
  checkout sees. On macOS packed-refs is writable too. Conductor's own
  pushes are unaffected (they read the remote), but your own git in the base
  checkout sees the change. A submodule git dir an agent creates, and the
  filters of a repository read with the sandbox off, are not guarded.
- **What detection catches, and what it does not.**
  - **Caught:** a known tunnel tool, by name or by binary (renamed copies
    included); a shell holding a network socket on any descriptor (renamed
    copies included); an interpreter on a socket's stdio; a decoy read;
    and, at the proxy, a one-shot bulk upload or a long-lived interactive
    tunnel from any program.
  - **Not caught:** a tunnel or shell in a binary this machine does not
    have, or a modified copy of one (a single changed byte gives a new
    digest), unless its traffic takes one of the two flagged shapes.
  - **Below the thresholds:** a slow trickle under 16 MiB, or an
    interactive channel that keeps its turns fewer, shorter or larger,
    passes as ordinary allowed egress. The audit row (target, bytes,
    features) is the record, the allowlist and the upload cap the
    controls.
  - **Shape alerts can be wrong.** A server that answers slowly every time
    looks interactive, which is why the default is to alert.
  - **Process tree.** A process that leaves the launch's tree
    (double-forked, reparented) and its process groups is followed only
    while its environment still carries the marker. On macOS a platform or
    signed binary's environment cannot be read, and there is no `pty_relay`
    rule.
- **The decoys are pipes.** An agent that `stat`s a decoy before reading it
  can tell it is not a file. They exist only while a launch runs (removed at
  its end, so nothing blocks on one later), and a read of a running launch's
  home from outside it (your own `grep -r` over conductor's state dir) is
  answered and counts as a read.
- **opencode** is not carried by the gateway: its key stays in its
  environment, and on macOS its loopback routes are reached directly.
- **macOS exposed servers.** A server a launch exposes (opencode's
  per-dispatch server) listens on the host loopback, so every local process
  on your machine can reach it. Only the random password conductor gives it
  guards it.
- **Best effort.** The aggregate slice cap is applied with `systemctl --user
  set-property --runtime` and logged when it fails. The disk guard stops
  every launch on the low filesystem, not only the one that filled it. A
  `sandbox exec` launch's events reach the audit log only while the daemon
  is reachable; they are on its stderr either way.
- **A detection kill is not a verdict on the dispatch.** The step fails as
  its agent died. A retry the step's own policy allows launches a fresh
  agent under the same watcher.

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

## Code steps — `cli`, host interpreters, `run: go` {#code-steps}

The section above covers code-step **engine plugins** (`run: js`, `run: lua`,
a third-party `use: <plugin>`). The OTHER code-step shapes — `use: cli` /
`command:`, a host interpreter (`run: bash`, `run: python3`, …), and
`run: go` — are **confined by default too**, the same `namespace`-mode jail
with the network denied, for the same reason: the step's `command:`/`args:`/
`env:` are rendered from templates, and a template can read event/trigger
data its author does not fully control. "You wrote this step" is not "you
control its argv at run time."

```yaml
code:
  isolation_default: namespace   # the default — every local cli/host/go step
                                  # with no isolation: of its own gets the jail
# isolation_default: none        # the escape hatch — restores the pre-existing
                                  # bare-by-default behavior for every such step
```

Per-step opt-outs work exactly like a pack's (see [[Trust-and-Isolation]]):

- `trust: full` on the step — "I've reviewed this one, run it unconfined."
- an explicit `isolation: { mode: none }` — the same thing, spelled as an
  isolation block.
- an explicit `isolation:` of any other shape — used verbatim, and (unlike
  the synthesized default) **fails closed**: if this box can't build what you
  asked for, the step errors instead of running unconfined.

`conductor validate` lists every code step that opted out one of these two
ways, so an unconfined step is a decision you see at validate/boot time, not
one you discover from an incident.

**`run: go` and the toolchain's own caches.** A jailed `run: go` step cannot
see the operator's real `GOCACHE`/`GOMODCACHE` any more than it can see
anything else outside its allow-list — they are hidden by absence like
everywhere else in the jail. It gets a private build/module cache of its own
instead (under the daemon's cache dir, read-write, shared across confined
`run: go` steps for the cache's benefit), and the network stays denied like
every other confined code step: a `run: go` step whose code imports a
third-party module needs its own `isolation: { network: { egress: [...] } }`
naming the module proxy/VCS host it needs — the same way any other code step
declares egress it needs beyond the default deny.

**A deployment not ready for the stricter default** sets
`code: { isolation_default: none }` while it migrates: every local code step
goes back to running exactly as it did before this default existed, and the
operator adds `isolation:` (or `trust: full`) to individual steps as they are
reviewed, same as before confined-by-default landed. This is a top-level,
OPTIONAL key — a config that never sets it keeps working, defaulted to the
secure posture.

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

Note that `/proc` isolation (this refusal, and `namespace`'s own pid
namespace below) only closes **one** way a token could reach a process it
wasn't minted for — a sibling reading it straight out of `/proc/<pid>/environ`.
It says nothing about the socket authorization itself: the broker's CLI-face
session token used to authorize **any process at the daemon's own uid**, so
however a token got out (a log line, a crash report, a different bug
entirely) it was good from any sandbox or host process sharing that uid —
`namespace` isolating one sandbox's `/proc` from another's doesn't touch the
daemon's own socket, which every sandbox reaches regardless. The broker now
binds that token to the launching process's own tree instead (root pid +
start time, descendants only) — see [[Agent-Skill#how-identity-is-bound]] —
so the two protections are independent and both matter.

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
- **Private space only by an exact entry.** The proxy resolves names
  itself, and refuses an address in a private, loopback, link-local or
  metadata range unless an entry names it exactly:
  - an IP-literal target matches the same literal listed, or a listed CIDR
    range (`10.20.0.0/24:8081`), for its port;
  - the target `localhost` reaches loopback when `localhost` itself is listed;
  - a hostname listed exactly (no glob) may resolve to a private or
    loopback address, never to a link-local (metadata) or multicast one.

  A name matched only by a glob (`*.lan`) that resolves (or rebinds) to an
  internal address is refused. Cloud metadata is reached only by listing
  its literal IP and using it as the target. See
  [A local package mirror](#local-package-mirror).
- **Entry forms.** `host`, `host:port`, `host:*`, globs on the host, a CIDR
  range, and `http://host[:port]/prefix/` (plain HTTP restricted to a path
  prefix). An `https://` entry with a path is a `validate` error: the path
  of an HTTPS request cannot be enforced without intercepting TLS.
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
| a push whose commits change a CI definition | allowed; refused only with the optional `sandbox.push.deny_ci_changes` (an agent can still change what CI runs via scripts and tests) |
| a launch's filesystem below `sandbox.limits.disk_free_min` | refused (a running launch is stopped) |
| no systemd user manager for the launch limits | runs uncapped after one warning; refused with `sandbox.limits.required` |
| a model the gateway cannot carry (opencode, an OAuth-only login) | `login-copy`: an access-token-only login copy, the key in the environment |
| a CONNECT tunnel that does not open with TLS, HTTP/1 or HTTP/2 naming its host (SSH, unknown bytes, silence) | refused before the upstream is dialed, the protocol named (`allow.raw` for one target, `egress_protocol_check: false` for a scope) |
| a plain-HTTP request outside a host's `http://` path entries, or a CONNECT to such a host | refused (403), audited with method and path |
| an `https://` egress entry with a path, its host not in any `inspect:` | rejected by `validate` (inspect the host, or list it alone) |
| a CONNECT to a host only `https://` path entries allow, in a launch that does not inspect it | refused, audited |
| an inspected host's upstream certificate does not verify (system roots + its `ca:`) | the request is refused (502), audited `upstream certificate refused` |
| a client rejects the inspection CA (pinned, or a platform trust store) | the connection fails, audited `client rejected inspection CA` |
| `inspect:` with a wildcard, an address or a port | rejected by `validate` |
| `skill:` + `mode: user` isolation | rejected by `validate` (claim theft under a shared uid) |
| `agent_authored.host` without `isolation:` | rejected by `validate` (`trust: full` opts out) |

## Audit

Every denied egress attempt is logged and audited as
`{event: egress_denied, target: host:port}`, so a sandboxed agent probing
the network is visible in `conductor report`'s audit trail.

An agent launch's containment adds:

| event | when |
|---|---|
| `egress` | each request through its egress session: `run`, `target`, `sni`, `protocol` as recognized (`tls`, `http`, `h2c`, `ssh`), `method` and `path` for plain HTTP and inspected HTTPS (never the query), `inspected: true` for a host whose TLS conductor terminated, `up`/`down` bytes, `duration_ms`, `max_idle_ms`, `server_first`, `outcome` (`allowed`, `refused`, `closed`), a `reason` for a refusal or closure, `shape` for a flagged tunnel, and `allowed_by` for a tunnel a `raw` allowance let skip the protocol check |
| `sandbox_detection` | the watcher, a tripwire, the git guard or the proxy's connection shapes (`kind: tunnel_shape`, with `shape`, `target` and the features): `kind`, `reason`, `action` (also a `conductor.escalate` notification); `action: allowed` with `allowed_by: <scope>` when the launch's [allowance](#scoped-allowances) covers it (audited and logged, not notified) |
| `model_request` | each request through the model gateway: `run`, `upstream`, `method`, `path`, `status`, `up`/`down` bytes, `duration_ms` (logged only when it failed) |
| `sandbox_limit` | the disk guard stopped a launch, a launch passed a model cap (`kind: model_upload` / `model_download`, `action: refuse`), the memory cap killed it (`kind: memory_cap`, `action: killed`) or the task cap refused its forks (`kind: pids_cap`, `action: refused`); also notified |
| `sandbox_usage` | each launch's end: `memory_peak`/`memory_max`, `pids_peak`/`pids_max`, `source`, and `near_cap`, `killed_by`, `pids_refused` when they apply |
| `sandbox_leftover_killed` | processes a launch left running were killed at its end |
| `sandbox_model_gateway_refused` | a request to the model gateway with a wrong credential, or a token-less write (token-less HEAD/GET probes are not audited) |
| `sandbox_limits_unavailable` | once: no systemd user manager, launches run without memory and task caps |

A `conductor sandbox exec` launch's rows carry `via: sandbox-exec`.
