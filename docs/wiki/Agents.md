# Agents

An `agents:` profile is a named, reusable definition of *what* to run — a provider, a model, a mode,
a workspace policy, and optional tone guidance — referenced by `agent:` on any `type: agent` step
across every trigger. It says nothing about *how* the process is executed; that's the job of a
[[Runtimes|runtime]], which the profile selects via `runtime:` (the legacy `controller:` key still
works). A profile may also pin `host:` — a [[Hosts]] SSH target its runtime launches on (cli/acp/
agent-deck runtimes).

## Config

```yaml
agents:
  fixer:
    provider: claude                  # -> paseo run --provider  (or provider/model shorthand)
    model: claude-opus-5              # -> --model     (optional; omit for the provider's default)
    thinking: ""                      # -> --thinking  (optional)
    mode: ""                          # -> --mode      (optional; one of the provider's modes)
    workspace: worktree               # local | worktree -> --new-workspace
    wait_timeout: 30m                 # -> --wait-timeout
    archive_when_done: true           # reaper archives the agent+worktree once it goes idle
    labels: { team: autopilot }       # extra --label pairs
    # controller: claude-cli          # optional: run this agent on a controllers: entry instead
                                      #   of paseo. provider/model above apply to the paseo/opencode/
                                      #   agent-deck controllers; an acp or cli controller IS the
                                      #   named agent, so they're ignored for it (don't pair
                                      #   provider: claude with an acp: gemini controller)
    # guidance: |                     # per-agent tone/format; overrides the top-level agent_guidance
    #   One or two sentences, plain and direct.   #   (unset -> that default, "" -> none, text -> this)
  planner:                            # cheaper/faster model for planning/triage steps
    provider: claude
    model: claude-haiku-4-5
    workspace: local
    archive_when_done: true
```

| Field | Meaning |
| --- | --- |
| `provider` | Paseo provider name, or the `provider/model` shorthand (e.g. `codex/gpt-5.5`). Maps to `paseo run --provider`. |
| `model` | Model ID from `paseo provider models <provider>`. Maps to `--model`. Omit to use the provider's default. |
| `thinking` | Maps to `--thinking`. Optional. |
| `mode` | One of the provider's modes, from the `MODES` column of `paseo provider ls` (e.g. `plan`, `default`, `bypass`). Maps to `--mode`. |
| `workspace` | `local` or `worktree`. Maps to `--new-workspace` — whether the dispatched agent runs in the existing checkout or a fresh git worktree. |
| `wait_timeout` | Maps to `--wait-timeout` — how long a foreground (`Wait: true`) dispatch waits before giving up. |
| `archive_when_done` | Whether the reaper archives the agent (and its worktree) once it goes idle. |
| `labels` | Extra `--label key=value` pairs attached to the dispatched agent. |
| `controller` | Name of a `controllers.<name>` entry to run this agent on instead of the built-in `paseo` runtime. See [[Controllers]]. |
| `guidance` | Per-agent tone/format text appended to this agent's prompts. Unset falls through to the top-level `agent_guidance`; `""` disables guidance entirely for this agent; any text replaces the default. |

## Behavior

- `agents.<name>` profiles are referenced by name from `agent:` on any action, in any integration —
  github rules, cron schedules, webhook sources, sentry/pagerduty rules, rss feeds, and slack
  triggers all share the same pool.
- `conductor validate` cross-checks every `agent:` reference in every action against a defined
  `agents.<name>` profile — an unresolved reference fails validation before the daemon starts.
- `provider`/`model`/`mode`/`thinking` are validated against your **Paseo daemon**, not against
  conductor's own config schema — a provider that isn't installed/enabled in Paseo, or a model ID
  that provider doesn't recognize, fails at dispatch time even though `conductor validate` accepted
  the YAML shape. Check what's actually available with:
  ```sh
  paseo provider ls                    # providers + status (available/enabled) + available modes
  paseo provider models claude         # model IDs for a provider (use the ID column as `model:`)
  paseo provider diagnostic claude     # troubleshoot a provider's install/auth/availability
  ```
  Example output of `paseo provider models claude`:
  ```
  ID                     MODEL          DESCRIPTION
  claude-opus-5          Opus 5         Opus 5 · Latest release
  claude-sonnet-5        Sonnet 5       Sonnet 5 · Best for everyday tasks
  claude-haiku-4-5       Haiku 4.5      Haiku 4.5 · Fastest for quick answers
  claude-opus-4-8[1m]    Opus 4.8 1M    Opus 4.8 with 1M context window
  ```
  Put the ID column in `model:` and the provider name in `provider:`.
- `workspace` governs the profile's default checkout lifecycle (a persistent local checkout vs. a
  fresh worktree per dispatch); the action's own `checkout:` (`checkout-pr` | `branch-off` | `none`)
  governs what git state that checkout is put into for a specific trigger (an existing PR branch, a
  fresh branch off base, or no repo at all). The two are independent knobs — a `workspace: worktree`
  profile can still be dispatched with `checkout: none` for a triage-only step.
- Every step of a multi-step [[Workflows|workflow]] can name a different agent — a common pattern is
  a cheap `planner` profile assessing an issue, then handing off to a stronger `fixer` profile only
  if the assessment justifies it (`if: steps.evaluate.outputs.has_context == true`).
- `archive_when_done: true` agents are still protected from premature cleanup: the reaper skips one
  that's paused on a permission prompt, and an agent can hold itself alive by creating a
  `.paseo-hold` marker in its worktree (guidance for this is added to its prompt automatically).

## Explanation

An agent and a controller answer different questions. The **agent** profile answers "what should
run" — which provider, which model, what tone, what workspace lifecycle. The **controller** answers
"how is it run" — which process or API actually executes it. A `fixer` profile with
`provider: claude` can run on the built-in `paseo` dispatcher, on `agent-deck`, or through
opencode's HTTP API, unchanged, just by pointing `controller:` at a different entry — the provider
and model still route through whichever runtime is selected. The one place this decouples is an
**ACP** or **cli** controller: there, the controller's own `agent:`/`command:` names the runtime
directly (e.g. `gemini` over ACP), so the profile's `provider`/`model` fields have nothing to route
and are ignored. With no `controllers:` configured at all, every agent profile runs on `paseo`, so
this distinction is invisible until you actually introduce a second runtime. See [[Controllers]] for
the full resolution order and controller kinds.
