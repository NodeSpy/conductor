# Dockerized e2e harness (issue #10)

A hermetic, in-repo end-to-end harness for the pluggable-controller feature set
(issue #9). It brings up an **isolated** Docker stack — a local bare-git forge, a
mock GitHub API, a notify sink-catcher, and stub controllers — and drives the real
`paseo-conductor` daemon through the milestone-feasible test groups, asserting
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

- **`paseo-conductor force <kind> <repo>#<n>`** — primary, deterministic, no
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
  image) so Node CLIs (gemini) and glibc CLIs (omp) run alongside the static ones.
- **Config:** `config/controllers.live.yaml` routes group-B repos to the real CLIs.
- **Mounts:** host tool dirs + credential dirs (parameterized, defaulted for a dev
  box); provider keys via env — populate `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` /
  `GEMINI_API_KEY` / `WAFER_SERVERLESS_API_KEY` before running.

Wired for the tools present on this box: **cli:claude-code**, **cli:codex**,
**acp:gemini**, **acp:omp** (oh-my-pi), **agent-deck**. Recorded **N/A** (not
installed here): opencode, copilot, and `codex-acp` (the acp:codex-adapter binary).
`make e2e-live` drives group B — each installed controller runs a real fixer and the
run asserts a new commit lands on the forge — then prints the matrix.
