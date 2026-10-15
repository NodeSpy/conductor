# Agent sandbox — design & build handoff

Status: **built** on `feat/agent-sandbox`. It supersedes #160 (agent jail)
and #175 (paseo plugin/relay). Operator docs: `docs/wiki/Isolation.md` (the
agent sandbox), `docs/wiki/Paseo-Sandbox.md`. §12 records what the build
found and where it departs from the handoff below; §13 the extension to the
opencode and agent-deck runtimes and the daemon's own paseo setup.

This document was the spec handed to the builder: the approved design, every
integration point on `main` at the time, the carry-over list from
#160/#175/#177, the proven policy and prototypes, the gotchas, and the
evidence bar. (The prototypes it names lived under `.scratch/prototypes/` on
the branch and were removed once reimplemented.)

---

## 1. Approved design and why

**Agents are read-only; conductor writes.** The agent process holds no push or
write credentials. It edits and commits in its workspace; conductor pushes
(signed), and all GitHub/cloud writes are conductor verbs. This retires the
whole privileged-command surface the jail used to broker.

**Engine: the built-in `internal/sandbox` (namespace + pivot_root on Linux,
Seatbelt on macOS), not `srt`.** Both passed the confinement suite in the spike;
the built-in won on:
- **zero new runtime deps** — pure Go in the conductor binary (its own
  `sandbox-net` forwarder, syscall namespaces/pivot_root). `srt` needs
  Node + bubblewrap + socat + ripgrep + prebuilt seccomp binaries; a missing
  `socat` silently disables it.
- **clean conductor-socket reach on Linux** — just bind the socket; no
  `allowAllUnixSockets` (which `srt` needs on Linux, disabling its AF_UNIX
  seccomp).
- **allowlist-by-absence** — the pivot_root jail shows only a minimal root;
  nothing to enumerate, no deny-list completeness problem (`srt` is deny-list,
  one missed path from a leak).
- **structural enforced egress** — `Deny`+`egress` gives a netns with no
  network of its own; raw-TCP bypass returns `Network is unreachable`.
- **host-loopback model relay** — the builtin pipes the host's loopback model
  endpoint in over a unix socket, sidestepping the non-loopback forwarder hack
  `srt` forced on this box.

**Paseo integration: a provider entry + `conductor sandbox exec`, NOT a plugin
or ACP relay.** Paseo keeps its native claude/codex/pi providers (rewind, steer,
subsessions). A conductor-owned provider entry wraps the real agent command:

```json
"claude-sandboxed": { "extends": "claude", "label": "Claude (sandboxed)",
  "command": ["conductor", "sandbox", "exec", "--", "<the real claude command>"] }
```

`conductor sandbox exec -- <cmd…>`: reads its own cwd (the agent's), asks the
daemon over the unix socket — before sandboxing — for the identity and policy
for that cwd, mints a **fresh** token for this launch, and execs the real agent
under the built-in sandbox with a clean env. Resume = paseo runs the command
again → same identity tool state, new token. Daemon unreachable or no policy ⇒
**refuse to launch**, never run unsandboxed. Paseo's injected MCP server is on
the daemon's loopback port, which the sandbox's isolated netns makes
unreachable — fail-closed by construction. Proven in the spike: real Claude
**and** Codex ran through paseo via these entries and completed tasks, with the
secret read blocked and never in the transcript. `extends`+`command`+`reload`
work on paseo 0.10.1 and 0.11.2.

**Deliberately NOT carried over** (retired with the read-only model): the
host-command broker and its platform backends, `internal/hostcmd`
(profiles/vop/paths/granted), `internal/grant` (grants), brokered git / the
remote helper / the signing shim, the copy-on-write home, and the #175 paseo
plugin + ACP relay (`paseorelay.go`, the canary-against-plugin).

---

## 2. Integration points on current `main` (85ca0c9)

| Scope | Where on main | Notes |
|---|---|---|
| Agent launch wrap | `internal/controller/launch.go` → `prepareLaunch` (≈L123), calls `sandbox.WrapLocalCommand` (≈L151) | Already wraps cli/acp launches, but without per-identity HOME, socket bind, model relay, or Confine. This is where scope 1 hooks in. |
| Sandbox engine | `internal/sandbox` (14 non-test .go on main): `sandbox.go` (`Spec`,`FromConfig`,`ProxyPolicy`,`EnforcedEgress`,`Check`), `wraplocal.go` (`WrapLocalCommand`,`LocalWrapDeps`,`BindMount`), `proxy.go` (`ProxyManager`,`UnixEndpoint`), `forward.go` (`Relay` type), `seatbelt.go`, `exec_linux.go`/`jail_linux.go` (pivot_root), `mask_*`,`loopback_*`,`env.go` (`MinimalEnv`,`FilterEnv`) | `WrapLocalCommand` is already **fail-closed** (any Check/proxy/wrap error aborts the launch; nil spec is a pass-through). |
| Token mint | `internal/skill/broker.go` → `MintSession(id Identity, uid uint32, ttl)` (≈L233), `MintHandoffSession` (≈L255) | On main. Fresh-token-per-launch for scope 3 uses this. |
| Skill env (endpoint+token) | `internal/dispatch/toolserver.go` (≈L230–256): builds `{CONDUCTOR_ENDPOINT: unix://…, CONDUCTOR_SKILL_TOKEN: …}` | The pattern `conductor sandbox exec` injects into the sandboxed agent. |
| Daemon control socket | `cmd/conductor/main.go`: `controlSockPath` (≈L1323), `serveControl` (≈L977), `controlRequest`/`controlResponse` (≈L1327/1359) | Add the `sandbox-policy` control command here (cwd → identity+policy+fresh token). |
| Subcommand dispatch | `cmd/conductor/main.go` dispatch switch (`case "call"`, `"sandbox-net"`, …, ≈L71–151) | Add `case "sandbox"` (→ `exec`) and `case "paseo"` (→ `setup`). `sandbox-net` (the in-sandbox forwarder) already exists — `SelfExe` points conductor at it. |
| `conductor call` (verbs) | `cmd/conductor/skillcli.go` → `cmdCall`, dialing the skill socket with `CONDUCTOR_ENDPOINT`+token | `git.push` is a new verb reachable here. |

**`LocalWrapDeps` on main** has `SelfExe, MaskPaths, Confine, ExtraBinds,
EgressAddr, EgressUnix` — **but no `Relays` field and no `Agent *AgentProfile`
field**. Those were added by #160 and must be ported (below). `internal/member`
is **not** on main — per the members builder, members *consume* this sandbox;
do **not** add a member seam here. The real member-worker-under-sandbox proof is
#178, which rebases onto this branch.

---

## 3. Carry-over list from #160/#175/#177

Branch heads for reference: #160 `feat/agent-jail` = `8528140`; #175
`feat/paseo-provider` = `ea3e7bc`. **Diff each file against `main` before
porting** — some of this (`Relay` plumbing in `forward.go`) may be partially on
main already.

| Item | Source | Port as… |
|---|---|---|
| `modelRoute(tool, home)` — split model endpoints into allowlist hosts + loopback addrs needing a relay | `internal/controller/jailwire.go` (#160/#175) | **port minus broker/grants** — keep the model-route logic, drop jail/broker refs |
| `Relay{Listen,Unix}` + `LocalWrapDeps.Relays` + the relay setup (`JailManager.Relay(...)` → a daemon-side forwarder socket) | `jailwire.go` relay block (≈L485–510 on #175) + `internal/sandbox/forward.go` | **port** — becomes a sandbox-level "relay a host loopback endpoint into the netns"; drop the `JailManager` coupling, keep the socket forwarder |
| `LocalWrapDeps.Agent *AgentProfile` + `internal/sandbox/agentprofile.go` + `seatbelt_agent.go` (macOS agent Seatbelt: scratch home, exec denials, Mach services, `LoopbackPorts`) | #160 additions to `internal/sandbox` | **port** — needed for macOS parity; add the unix-socket allow (§5) |
| Per-identity tool state: `StateKey` → `<state>/agent-state/<key>/`, seeded once from the operator's settings minus other projects' history, login files shared | `internal/jail/identity.go` (#175) | **port minus broker/grants** — the mechanism is independent of the jail broker |
| npm/ACP-in-sandbox fixes: resolve argv0 via `LookPath`; recreate `~/.local/bin` symlink hops as `Link` binds; bind the npm package root for node shebang scripts | `jailwire.go` / `internal/jail` (#175, commit `213bdae` "an npm-installed agent CLI starts in the jail") | **port** — still needed; an ACP adapter installed under `~/.local/bin` must start in the sandbox |
| Test-isolation: jail-test scratch dirs under `CONDUCTOR_JAILTEST_DIR` else `/var/tmp` (never `/tmp` or `$HOME`), pid-marked, swept, `TestMain` cleanup | `internal/jail/testdirs_test.go` (#160, commit `8528140` "scratch dirs under /var/tmp, never left behind") | **port** — adapt paths to the new package layout |
| `internal/testhome.Main` + the HOME-isolation guard | #177 (draft; **not yet on main** — pull the exact commit when #177 merges, or copy the package) | **port** — new sandbox tests must run under a temp `$HOME`; wrap each new package's `TestMain` around `testhome.Main` |
| macOS socket-path-length handling: `sun_path` is ~104 chars on darwin, so relay/egress/skill socket paths must be SHORT — mint them under a short dir (e.g. `/tmp/cj…`), never a deep worktree path | #160 darwin code (grep `sun_path` / socket creation on `feat/agent-jail`; **confirm the exact file when porting** — I could not pin the commit from a fresh worktree) | **port** — this is why the spike's relay sockets used short `/tmp` paths |

**Not carried over** (see §1): broker, backends, `hostcmd`, `grant`, brokered
git/remote-helper/signing-shim, COW home, paseo plugin + ACP relay.

---

## 4. The exact policy (proven in the spike)

`conductor sandbox exec` builds, for the builtin engine:

```go
spec := &sandbox.Spec{Mode: "namespace", Deny: true, HasEgress: true, Egress: policy.Egress}
deps := sandbox.LocalWrapDeps{
    SelfExe:    func() (string, error) { return conductorBin, nil }, // the sandbox-net forwarder
    Confine:    true,                                                 // pivot_root: hide by absence
    ExtraBinds: []sandbox.BindMount{
        {Path: workspace},                 // rw
        {Path: identityToolState},         // rw: the agent's ~/.claude (etc.), per-identity
        {Path: conductorSocket},           // rw: the skill/memory socket FILE only (not its dir)
        {Path: conductorBin, RO: true},    // ro: so `conductor call` resolves inside
    },
    EgressUnix: proxyMgr.UnixEndpoint,     // ENFORCED egress (deny + unix forwarder)
    EgressAddr: proxyMgr.Endpoint,
    Relays:     []sandbox.Relay{{Listen: "127.0.0.1:<model port>", Unix: relaySock}}, // loopback model
}
wrapped, env, cleanup, err := sandbox.WrapLocalCommand(spec, argv, workspace, cleanEnv, deps)
```

- `Deny: true` + `HasEgress` + `Egress` + `Mode: namespace` ⇒ `EnforcedEgress()`
  is true: the netns has no network of its own; the only way out is the
  allowlist proxy over a unix socket. (Advisory `HasEgress` without `Deny`
  leaves the process in the host netns with a bypassable `HTTP_PROXY` — do not
  use it for agents.)
- `cleanEnv` = `sandbox.MinimalEnv()` + `HOME=<identity home>` + only the needed
  model/key vars (`ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY`).
  **srt/the spike did NOT scrub env by default** — the launcher must. Here
  `MinimalEnv()` does it; verify no `CLAUDE_CODE_*`/token vars leak.
- Operator secrets (`~/.ssh`,`~/.aws`,`~/.config/gh`,`~/.gnupg`, conductor
  state, other orgs) are simply **never bound** → absent in the jail. Nothing
  to deny-list.
- Bind the socket **file**, not its directory (the state dir holds secrets).

**macOS Seatbelt socket gap and fix.** In the spike, the macOS CI builtin leg
passed fs + net but **failed the socket check**: the prototype passed no
`AgentProfile`, so the generated Seatbelt profile denied the unix socket. Fix:
the Seatbelt generator (`internal/sandbox/seatbelt.go` + the ported
`seatbelt_agent.go`/`agentprofile.go`) must **allow the conductor socket path**
— analogous to how it already allows `LoopbackPorts` and narrowed Mach
services. Add an explicit unix-socket allow entry (the path from the policy) to
the agent profile, and close the failing CI leg. macOS is a hard requirement.

---

## 5. `conductor sandbox exec` + the daemon endpoint (scope 3)

Prototype: `.scratch/prototypes/sandboxexec/` (builds against this branch; reads
policy from `SBX_POLICY`/`SBX_ENGINE` env — the daemon-endpoint call replaces
that).

- **`conductor sandbox exec -- <cmd…>`**: resolve cwd → dial the daemon control
  socket `sandbox-policy` command → get `{identity, policy, token}` → build the
  spec/deps (§4) → exec. Refuse (non-zero, no exec) if the daemon is
  unreachable **or** the cwd has no policy and no default applies.
- **Daemon endpoint** (new `controlRequest.Cmd == "sandbox-policy"` in
  `serveControl`): input the cwd; output the identity (the cwd→identity lookup),
  the policy (workspace, tool-state dir, egress allowlist incl. the model
  endpoints via `modelRoute`, the socket path), and a **fresh** token
  (`broker.MintSession(id, uid, ttl)`) minted per call. Authorize by the
  control socket's same-user peer creds (as the existing control commands do).
- **Unknown cwd** (Daniel's own ad-hoc sandboxed sessions, not a
  conductor-created workspace): return a **default policy** scoped to that
  workspace — identity = the cwd itself, tool-state = a per-cwd dir, egress =
  the model endpoint only, workspace rw. Document this clearly: an unknown cwd
  is still sandboxed (never unconfined), just with a default workspace-scoped
  policy. The only hard refusal is daemon-unreachable.

---

## 6. `git.push` verb + end-of-step push (scope 5)

Prototype: `.scratch/prototypes/git-push-verb.sh` (ref rules + ff-only +
signing, all proven).

- A `git.push` verb (git connector) **plus an end-of-step push**. Both run
  **outside** the agent sandbox, as conductor.
- Push **workspace `HEAD` → `refs/heads/<dispatch-branch>` only**. The agent
  cannot name any other ref.
- **Fast-forward only, never forced** (a diverged remote rejects, not forces).
- Refuse every other/illegal ref (`refs/*`, names with space/`~`/`^`/`:`/`*`/
  `[`/`\`, `-`-led, `HEAD`, empty).
- **Sign the pushed commits as Daniel** (SSH signing) on the way out — sign the
  whole new range `remote..HEAD`, not just HEAD as the prototype does.
- Agents hold no push/write credentials; GitHub writes stay conductor verbs.
- **Verified badge**: the signature verifies `G` (Good) locally; GitHub shows
  "Verified" only when conductor's signing key is registered as an **SSH signing
  key** on the account. Document this; **do not touch account settings**.

---

## 7. `conductor paseo setup` (scope 4)

Prototype: `.scratch/prototypes/paseo-setup.py` (merge semantics proven against
a config COPY).

- Idempotent merge of **only** conductor-owned provider entries, marked
  `"x-conductor": true`. Reports `added/updated/unchanged/skipped`.
- Never clobbers an operator entry: a same-named entry without the
  `x-conductor` marker is **skipped**, not overwritten. All operator providers
  preserved.
- After a write, run `paseo reload` (proven to pick up changed entries on 0.10.1
  and 0.11.2).
- Provide entries for `claude` and `codex`; add `pi` only where paseo reports
  `pi` available (the `extends`+`command` mechanism was proven for codex, also a
  built-in provider).
- **Never touch the real `~/Projects/paseo-home/config.json` in tests** — tests
  run against a copy.

---

## 8. Gotchas from the spike (read before building)

- **Pre-create per-identity tool-state dirs on the host before launch.** If the
  `allowWrite`/bind source dir doesn't exist, the write lands in an ephemeral
  mount and is lost (this cost the spike its first resume attempt). Conductor
  already creates per-identity state — just ensure ordering.
- **Env is not scrubbed by default.** Use `MinimalEnv()` + explicit allow;
  verify no launcher token (`CLAUDE_CODE_MESSAGING_TOKEN`, etc.) leaks in.
- **Ubuntu CI runners need unprivileged userns on** for the namespace sandbox:
  `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` (and
  `kernel.unprivileged_userns_clone=1`). Without it, bwrap/netns setup fails
  with `RTM_NEWADDR: Operation not permitted`. (The builtin uses its own Go
  namespaces, but the same host userns restriction applies.)
- **macOS `sun_path` ≈ 104 chars.** Mint relay/egress/skill sockets under a
  SHORT dir, never a deep worktree path. (See the carry-over macOS fix.)
- **e2e compose project + image tag must be per-checkout.** Two worktrees
  sharing `pc-e2e` / `conductor-e2e:latest` collide: one run's teardown takes
  the other's stack down, and a build swaps the binary under a running stack.
  Derive both from the checkout dir (the spike fixed this on the canary branch).
- **Test HOME isolation (#177):** new packages' `TestMain` must wrap
  `internal/testhome.Main`; the whole suite must pass with `HOME` set to a temp
  dir and leave nothing behind.
- **paseo's `allowAllUnixSockets` is an srt concept** — not relevant to the
  builtin, which reaches the socket by binding it. Don't reach for it.
- **`conductor call` needs `CONDUCTOR_ENDPOINT`+token in the env**; the agent
  gets them from `sandbox exec`'s fresh mint, not from a conductor-launched
  dispatch (paseo launches the home agent).

---

## 9. CI canary (scope 6)

- Turn the spike branch `spike/srt-macos-canary` into a **real** workflow for
  the **built-in sandbox only** (drop the `srt` legs), Linux **and** macOS.
- A scheduled job against the **latest published paseo** exercising the
  provider-entry + `conductor sandbox exec` path (fake agent, no model, no
  secrets).
- Starting point: the spike branch's `test/srt-canary/` + `test/sandbox-canary/`
  (builtin canary `builtin.sh` + `sandboxexec/`). **Delete `spike/srt-macos-canary`
  after this branch's workflow lands.**
- The spike's builtin canary was green on Linux; macOS needs the §5 Seatbelt
  socket fix to go green.

---

## 10. Evidence bar & live-proof checklist (from the brief)

**Gates:** gofmt, vet, `go test -race ./...`, full Dockerized e2e, CI green on
**both** Linux and macOS. Never touch the live service, the real config, or the
live state dir (`~/.local/state/conductor` — keep `installed.yaml` byte-identical;
baseline hash `62dd533e`).

**Mutants:** on policy generation (the spec/deps builder), `sandbox exec`
refusal paths (daemon-down, unknown cwd), `paseo setup` merge rules, and
`git.push` ref checks.

**Live proofs** (throwaway daemons, temp `HOME`, read every reply to
completion):
- [ ] Claude through paseo via the provider entry completes a task.
- [ ] Codex through paseo via the provider entry completes a task.
- [ ] The secret read is blocked (and never enters the transcript).
- [ ] **Resume across a real paseo restart** with memory intact.
- [ ] A **native paseo feature driven end to end** (steer or rewind).
- [ ] Paseo's MCP tools fail closed.
- [ ] Daemon down ⇒ refusal to launch (never unsandboxed).
- [ ] A cli worker runs sandboxed and calls `step.done` (plain conductor
      dispatch — NOT the member gate; real member proof is #178).
- [ ] `git.push` to a throwaway repo: other refs refused, commits signed (`G`).
- [ ] **pi:** install into a temp prefix if feasible and prove it, or state
      exactly why not.

**Docs in this PR:** this design doc, `docs/wiki/Isolation` (updated), a new
paseo-sandbox wiki page, `config.example.yaml`, and an explicit section on what
the jail no longer does (broker, grants, signing shim) and why.

**After green:** close #160 and #175 as superseded with a comment linking this
PR (they stay on GitHub for reference). Flag what conductor-plugins#56 (the
github plugin scope tags) should become — keep / trim / close.

---

## 11. Prototypes in this branch

`.scratch/prototypes/` (force-added past `.scratch/` ignore):
- `sandboxexec/main.go` — `conductor sandbox exec` for both engines (builtin is
  the chosen one); builds against this branch. Policy via `SBX_POLICY` env in
  the prototype; the real one calls the daemon endpoint (§5).
- `paseo-setup.py` — `conductor paseo setup` merge semantics (§7).
- `git-push-verb.sh` — `git.push` ref rules + ff-only + signing (§6).

These are spike artifacts to start from, not product code — reimplement in Go in
the proper packages, keep the proven behavior.

---

## 12. As built — what the build found

Where the code lives: `internal/agentsandbox` (policy, identity homes, model
routes, env, registry, relays), `internal/gitpush` (conductor's push),
`internal/paseosetup` + `cmd/conductor/paseosetup.go`, `cmd/conductor/
sandboxexec.go` + `sandboxpolicy.go` (the wrapper and the control-socket
`sandbox-policy` command), `internal/controller/agentsandbox.go` (cli/acp
launches), the built-in `git` connector (`git.push`).

Departures from §1–§11, each found in a live run or a mutation run:

- **The session's directory is `PASEO_AGENT_CWD`, not the wrapper's cwd.**
  paseo starts `codex app-server` in its own daemon's cwd and sends the
  session's directory over the protocol; it names it in `PASEO_AGENT_CWD`
  for every provider launch. A launch with none (paseo listing a provider's
  models) whose cwd may not be a workspace gets the tool's **scratch
  workspace** — an unknown cwd in the home, `/`, or conductor's state is
  otherwise refused, a narrowing of §5's "only hard refusal is
  daemon-unreachable" (showing the home read-write would bind the very
  secrets §4 keeps out).
- **Launcher temp files on argv are shown read-only.** paseo hands pi an
  `--extension` and `--mcp-config` it writes to the system temp dir.
- **Per-dispatch clones** (ported from #160 `ca3f6d4`): with linked
  worktrees one sandboxed agent could move a sibling dispatch's branch,
  which conductor would then push and sign.
- **conductor's diff read of a sandboxed workspace runs sandboxed**: the
  agent writes the checkout's `.git/config` and `.gitattributes`, whose
  filter/textconv/fsmonitor commands host-side git would run as conductor.
- **`step.done` resolves on the runner that launched the dispatch**, and an
  in-flight dispatch defers before its agent is bound — both were bugs on
  `main` that blocked a sandboxed cli worker's (and an eager paseo agent's)
  done call.
- **The agent's git identity** reaches it through the env (`GIT_AUTHOR_*`
  are not credentials) and its identity home's `.gitconfig`, which falls
  back to `sandbox.push`'s name/email.
- **`git.push` refuses any option** instead of ignoring one.
- **Identity** is a dispatch's profile per TRUSTED repository (a
  sender-chosen target gets a per-dispatch identity), never the raw target.
- **Fail-closed has one explicit opt-out**: `sandbox.agents: off` (logged at
  boot), or `isolation: {mode: none}` per runtime/step. The e2e's root
  daemons use it; the sandbox group runs non-root.

---

## 13. Extension: opencode, agent-deck, the daemon's paseo setup

**opencode — a per-dispatch `opencode serve`, launched sandboxed, its port
exposed to conductor.** The controller already ran one `opencode serve` per
session (not a shared server), over its HTTP API: a create, a blocking
message turn, abort, resume by session id, remote boxes through `ssh -W`.
Switching to `opencode run` (one process per turn, JSON events on stdout) or
`opencode acp` (the acp controller) would each have replaced that transport
— resume, abort and the remote path with it — to avoid one thing: reaching a
port inside a sandbox with no network. That is a small, generic addition to
the sandbox instead: an **expose** relay, the reverse of the model relay
(`sandbox.Expose`, `sandbox-net --expose SOCK=ADDR`). The in-sandbox forwarder
listens on a host unix socket — opened before the jail is built, so its
directory is never shown — and pipes each connection to the server's port in
the sandbox's own network namespace (fixed: nothing else is there). macOS has
no network namespace: the server takes a free host port, the only one its
Seatbelt profile lets it bind (`AgentProfile.ListenPorts`). Either way the
server needs a per-session password. Everything above the transport is
unchanged; the agent is the server, so its tools are confined.

The allowlist follows the configured provider (`opencodeRoute`): the
dispatch's model (`Entry.Model`, "provider/model"), the operator's opencode
config (`model`, `small_model`, a provider's `options.baseURL` — loopback is
relayed), else the providers it is signed in to; plus the model catalog
(1.x reads `models.opencode.ai`). Found live: opencode 1.x routes a turn by a
`model: {providerID, modelID}` object and ignored the top-level pair the
controller sent — it ran its default model; both are sent now.

**agent-deck — its `--wrapper`, around the whole command.** agent-deck types
a compound command (`export …; … exec env … claude …`) into a shell in its
own tmux session; conductor holds neither that shell nor the agent. The
wrapper template conductor gives `agent-deck add` runs `{command}` — whole,
via a quoted heredoc with a fresh delimiter — as `/bin/bash -c` inside
`conductor sandbox exec` (`--bin` shows the tool, `--agent-cwd` refuses
rather than substitutes a scratch workspace). Found live: a wrapper of
`sandbox exec -- {command}` confines only `export` and runs the agent bare;
claude's interactive UI stops at its folder-trust question in a fresh
identity home (conductor now records a dispatch's workspace as trusted there,
read with `O_NOFOLLOW`, replaced by rename); and against the real agent-deck
CLI the controller's `--prompt` does not exist (`--message`), and a session
added without a path lands in its group's existing one — another dispatch's
workspace.

**The daemon's paseo setup.** `conductor paseo setup` is split into a plan
(resolve, merge) and a write (+ `paseo reload`); the daemon runs both at every
start for each paseo home it can name, pointing the entries at its control
socket. It never follows the endpoint ladder's address guess to a config.

**Not changed.** Interactive hand-offs: still the operator's decision.
