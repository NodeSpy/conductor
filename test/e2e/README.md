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
| `conductor` | the real daemon, **no `controllers:` block** → paseo default path; webhook receiver for group H |
| `conductor-ctrl` | a second daemon **with** a `controllers:` block → group A resolution |
| `conductor-fail` | a daemon whose fake paseo fails `workspace create` → group J2 |

### Stub controllers (per transport)

- **`fakepaseo`** — the native controller. Conductor execs it via `paseo_bin:`,
  exactly as the real paseo. It does **real git** (clone/commit/push to the forge),
  commits as the acts-as-the-user identity conductor passes via `--env`, and speaks
  the full subcommand/JSON contract (`run`/`ls`/`inspect`/`send`/`archive`/`wait`/
  `clone`/`workspace …`). This is what makes groups B/H/I genuinely end-to-end.
- **`fakeacp`** — an ACP agent built on the repo's own `internal/acp` library
  (scaffold for M3; `FAKE_ACP_CRASH=1` simulates a mid-session crash for J3).
- **`fakeopencode`** — the `opencode serve` HTTP surface (scaffold for M4/T4.1).
- **`fakeagentdeck`** — the agent-deck CLI (scaffold for M4/T4.2).

## Event injection

- **`paseo-conductor force <kind> <repo>#<n>`** — primary, deterministic, no
  webhooks. Run via `docker compose exec` into a daemon container.
- **Webhook path (group H)** — signed fixture POSTs (`fixtures/*.json`) to the
  github `listen:` receiver; the mock API serves the reads. `X-Hub-Signature-256`
  is a real HMAC over the raw body (secret `e2e-webhook-secret`).

## Test groups & current coverage

Only groups whose milestones have merged are **asserted**; the rest are recorded
`SKIP` with the milestone that unlocks them (the harness is already wired for them).

| Group | Coverage |
|---|---|
| **A** resolution & config | ✅ A1 no-controllers→paseo · A2 explicit · A3 default:true · A4 precedence (explicit non-runnable > default) · A5 validation (two defaults / unknown transport rejected, one default accepted) |
| **B** controller runs a fixer | ✅ paseo row (edit/commit/push, agent archived) · other controllers SKIP (M3/M4) |
| **G** notify sinks (M0) | ✅ all five sinks fire on dispatch |
| **H** fixers via webhook | ✅ H1 merge_conflict · H2 failing_checks + ignore_checks suppression · H3 new_comment |
| **I** identity & isolation | ✅ commit attributed to the user, API write uses the user token (not the App), no production endpoints |
| **J** failure & escalation | ✅ J1 non-runnable controller → escalate · J2 worktree-create failure → loud escalate (+ sink) · J3 SKIP (M3) |
| **C/D/E/F** | SKIP — session broker / handoff / capability layer (M2/M3) not yet landed |

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

## Live mode

`make e2e-live` layers `docker-compose.live.yml` (a scaffold) over the stack:
real agents + mounted provider keys, but the forge/mock/sink stay in place so the
real agent works against the isolated repo while conductor's reads/writes stay
mocked. Fill in the controller binary + key mounts for your environment; never run
it in CI.

## Extending as milestones land

When M2/M3/M4 land, flip a `skip` to a real assertion in `run.sh`:
- **M3 (ACP transport):** wire an `acp` controller in a config, point it at
  `fakeacp`, and assert group B's `acp:*` row + J3 (`FAKE_ACP_CRASH=1`).
- **M4 (opencode/agent-deck):** add services for `fakeopencode`/`fakeagentdeck`,
  wire the controllers, assert the corresponding B rows.
- **M2 (session broker / handoff):** assert C (session_model), D (broker), E
  (HandoffChannel), F (capability degradation).
