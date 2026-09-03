# Dockerized e2e harness (issue #10)

A hermetic, in-repo end-to-end harness for the pluggable-controller feature set
(issue #9). It brings up an **isolated** Docker stack — a local bare-git forge, a
mock GitHub API, a notify sink-catcher, and stub controllers — and drives the real
`conductor` daemon through the milestone-feasible test groups, asserting
behavior from the audit log, the forge, the mock API, and the sinks.

**Fully isolated.** Conductor in the container sees only the local forge + mock
endpoints — never the production GitHub App, smee, or repos, and **no LLM keys**.

## Run it

```sh
make e2e          # hermetic stubs, CI-safe, no secrets  (the M6 ship gate)
make e2e-live     # real agents + mounted keys (manual; see below)
make e2e-down     # tear down a leftover stack (e.g. after KEEP=1)
```

Useful env: `KEEP=1` leaves the stack up for inspection; `PROJECT=<name>` isolates
parallel runs.

## What comes up (`docker-compose.yml`)

One image (`Dockerfile`) builds conductor + every harness binary; each service
overrides `command:`.

| Service | Role |
|---|---|
| `forge` | bare git repos served over `git://` — agents clone / commit / push here |
| `mock-github` | canned repo/PR/check reads; **captures** acts-as-the-user writes (`/_captured`) |
| `sink-catcher` | captures notify posts — Slack/Discord/ntfy/Pushover/Notifiarr (`/_captured`) |
| `conductor` | the real daemon, **no `controllers:` block** → paseo default path; webhook receiver for group H; web hand-off channel for C/D/E |
| `conductor-ctrl` | a second daemon **with** a `controllers:` block → group A resolution + every non-paseo controller (group B) + resumable hand-off (C/D2/F) + ACP crash (J3) |
| `conductor-fail` | a daemon whose fake paseo fails `workspace create` → group J2 |

### Stub controller runtimes (per transport)

Each fake is driven through conductor's **real** controller code (resolve →
provision a PR worktree → open a session → run the first turn). The shared
`services/fixer` package makes the driven "agent" do **real git** — edit + commit
(as the acts-as-you identity conductor passes) + push to the forge — so a commit
lands per controller, exactly like the paseo path.

- **`fakepaseo`** (installed as `paseo`) — the native controller. Speaks the full
  subcommand/JSON contract (`run`/`ls`/`inspect`/`send`/`archive`/`wait`/`clone`/
  `workspace …`) and, in live mode, still **provisions** the worktree (git only).
- **`fakecli`** (installed as `claude` + `codex`) — the `cli` transport: `claude -p …`
  (claude-code recipe, resumable) and `codex exec …` (codex recipe, oneshot).
- **`fakeacp`** — an ACP agent on the repo's own `internal/acp` library, driving the
  `acp:gemini` / `acp:codex-adapter` / `opencode-acp` rows. Advertises `loadSession`
  (→ resumable). `FAKE_ACP_CRASH=1` (injected per-route via the action env) dies as
  the session opens → J3.
- **`fakeopencode`** (installed as `opencode`) — the `opencode serve` HTTP surface
  the opencode-native controller drives.
- **`fakeagentdeck`** (installed as `agent-deck`) — the agent-deck CLI orchestrator.

## Event injection

- **`conductor force <kind> <repo>#<n>`** — primary, deterministic, no
  webhooks. Run via `docker compose exec` into a daemon container.
- **Webhook path (group H)** — signed fixture POSTs (`fixtures/*.json`) to the
  github `listen:` receiver; the mock API serves the reads. `X-Hub-Signature-256`
  is a real HMAC over the raw body (secret `e2e-webhook-secret`).

## Test groups & current coverage

The whole pluggable-controller feature set has merged, so `make e2e` **asserts every
group** — a full controller × workflow matrix, all PASS. The only remaining row is a
genuinely-N/A one (see below), never a stale skip.

| Group | Coverage |
|---|---|
| **A** resolution & config | ✅ A1 no-controllers→paseo · A2 explicit · A3 default:true · A4 precedence (explicit non-runnable > default) · A5 validation |
| **B** each controller runs a fixer | ✅ paseo · cli:claude-code · cli:codex · acp:gemini · acp:codex-adapter · opencode-acp · opencode-native · agent-deck — every row edits/commits/pushes to the forge, dispatched under its transport backend; agent archived when done |
| **C** session_model | ✅ native (paseo, bound live) · resumable (ACP, persisted by id) · oneshot (cli:codex fresh process, no persistent session) |
| **D** session broker | ✅ D1 burst of 3 → ONE live session (follow-ups queued) · D2 restart → resumable ref re-attached from the store, no orphan |
| **E** HandoffChannel | ✅ E1 web-link approve (paseo **and** a bare cli runner) · E3 revise loop · E4 discard · E2 Slack **N/A** (see below) |
| **F** capability degradation | ✅ F1 cli controller (InteractiveHandoff:false) completes review over the portable web channel · F2 acp controller runs in a conductor-supplied worktree |
| **G** notify sinks (M0) | ✅ all five sinks fire on dispatch |
| **H** fixers via webhook | ✅ H1 merge_conflict · H2 failing_checks + ignore_checks suppression · H3 new_comment |
| **I** identity & isolation | ✅ commit attributed to the user, API write uses the user token (not the App), no production endpoints |
| **J** failure & escalation | ✅ J1 non-runnable controller → escalate · J2 worktree-create failure → loud escalate (+ sink) · J3 ACP agent crash → detected → escalate |

**E2 Slack — N/A (not a stale skip).** The Slack hand-off channel
(`internal/handoff/slack.go` — inbox + `parseReply`) is implemented and unit-tested,
but it is **not wired into the daemon** (no `handoff.slack` config, no
`slack.SetReplyHook` call in `cmd/`), and its inbound path is Slack **Socket Mode** (a
WebSocket to `slack.com`) — not drivable by the hermetic harness without a production
feature addition beyond e2e scope. The identical controller-agnostic Review loop
(present → approve/revise/discard) **is** exercised end-to-end via the web channel.

Every scenario prints expected-vs-actual and a final **results matrix**. The run
exits non-zero if any assertion fails.

## Minimal testability hooks (production behavior unchanged when unset)

The harness needs a few seams into otherwise-hardwired endpoints. Each is env-gated
and a no-op in production:

- `PC_GITHUB_API_BASE` — point conductor's GitHub REST/GraphQL reads at the mock.
- `PC_PUSHOVER_URL` / `PC_NOTIFIARR_URL` / `PC_NTFY_DEFAULT_URL` — redirect the
  vendor-hardcoded notify sinks at the sink-catcher (Slack/Discord/ntfy already
  take their URL from config).
- `PC_REAPER_INTERVAL` / `PC_REAPER_MIN_AGE` — shrink the reaper cadence so
  archive-when-done is observable without the 3-minute production grace.

## Live mode (`make e2e-live`, manual)

Layers `docker-compose.live.yml` over the stack to drive the **real** agent CLIs
installed on the host through their controllers, against the same isolated
forge/mock/sink (so conductor's GitHub reads/writes stay mocked and the real agent's
work lands on the local forge). `fakepaseo` still provisions the PR worktree (a real
git clone — no LLM); the real agent then edits/commits/pushes. **Never run in CI** —
it needs provider keys and reaches real model providers.

- **Image:** `Dockerfile.live` is a glibc + Node runtime (vs. the hermetic alpine
  image). It installs the **real** `claude` binary (`@anthropic-ai/claude-code`) and
  creates its user matching the **host uid/gid** (build args from `run.sh`) so the
  read-only, owner-only credential mounts are readable. The daemon runs **non-root**
  (claude-code refuses `--dangerously-skip-permissions` under root).
- **Config:** `config/controllers.live.yaml` routes group-B repos to the real CLIs
  with explicit non-interactive flags (`claude -p --dangerously-skip-permissions`,
  `codex exec --dangerously-bypass-approvals-and-sandbox`, `gemini --acp -m …`).
- **Credentials — turnkey, nothing committed.** `run.sh` sources `live-env.sh` on the
  host, which reads the operator's own config at launch and exports the secrets as
  env for the compose file:
  - **claude → host TeamClaude proxy.** Reads the proxy port + key from
    `~/.config/teamclaude.json`; the container reaches it at
    `http://host.docker.internal:<port>` (via `extra_hosts`) and presents the proxy
    key as `ANTHROPIC_API_KEY` (required for non-loopback callers). The real `claude`
    binary is driven directly — the host `claude-teamclaude` wrapper is **not** used
    inside the container (it probes `127.0.0.1:3456`, which is the container).
  - **codex → copied OAuth tokens.** `~/.codex/auth.json` is mounted read-only into a
    staging path and copied into the container's writable `$HOME` (so codex can
    refresh its token in place — a read-only bind at the live location blocks that).
  - **gemini → decrypted api key.** gemini stores its api key AES-256-GCM in
    `~/.gemini/gemini-credentials.json` (keyed by host + user); `live-env.sh` decrypts
    it and passes `GEMINI_API_KEY` + `GEMINI_CLI_TRUST_WORKSPACE=true`.
  - omp/agent-deck keys are optional (`WAFER_SERVERLESS_API_KEY`, etc.).

**Results — what passes live, what needs operator action.** `make e2e-live` drives
group B and prints the matrix. Rows are classified honestly:

| Row | Result | Why |
|---|---|---|
| **cli:claude-code** | **PASS** (required) | Authenticates through the TeamClaude proxy and lands a full clone→edit→commit→push. A red row here is a real regression. |
| **cli:codex** | best-effort → usually **SKIP** | Wired correctly (token accepted, no 401), but this box's `codex` (v0.146.0) calls `chatgpt.com/backend-api/codex/*`, which returns **404** — reproduced on the host, so it's broken in the operator's own environment, not the harness. *Operator action:* restore codex backend access. |
| **acp:gemini** | best-effort → **PASS** with quota, else **SKIP** | Wired correctly end-to-end (auth, trust, ACP `initialize`, permission handling all verified). Blocked only by the operator's **free-tier Gemini quota (20 requests/day)** and flaky model availability. *Operator action:* a paid Gemini tier / fresh quota. |
| **acp:omp**, **agent-deck** | optional → **SKIP** unless their keys are present | PASS on commit, else a noted SKIP — never a hard FAIL. |

Best-effort rows distinguish a **conductor-level failure** (couldn't launch/handshake
the agent → red **FAIL**, catches regressions) from an **agent that launched &
authenticated but whose model provider blocked the turn** (→ **SKIP** with the
captured reason). So the run is green when conductor's wiring is correct, and it names
exactly which agents need operator action.

A conductor ACP-client fix rode along: the `initialize` handshake now always sends a
non-empty `clientInfo.version` (`internal/acp`), which spec-strict agents like
`gemini --acp` require — the lenient hermetic `fakeacp` accepted its absence, real
gemini rejected it.
