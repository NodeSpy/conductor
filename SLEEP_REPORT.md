# `sleep` — the first built-in helper step

**Branch:** `feat/helper-steps-sleep` (based on `main` @ `9a6efb6`)
**Implementation commit:** `08a9bec6f0ce14c7516ae8b29d033ba2d8b4ba11`
— *feat(steps): built-in helper step forms, starting with sleep*
(this report is committed on top)

**Toolchain:** `go1.26.3-X:nodwarf5 linux/amd64`

---

## 1. The surface

```yaml
steps:
  - id: cancel
    uses: gh.cancel_run
    options: { repo: "{{.repo}}", run_id: "{{.run_id}}" }
  - sleep: 5s                       # ← the helper
  - id: rerun
    uses: gh.rerun_run
    options: { repo: "{{.repo}}", run_id: "{{.run_id}}" }
```

One new field on `config.Step` (`internal/config/connectors.go:888`):

```go
// helper form (helpers.go). Sleep pauses the flow for a duration, …
Sleep Duration `yaml:"sleep,omitempty"`
```

It reuses the **existing** `config.Duration` (`internal/config/scalars.go:15`),
the same type behind `wait_timeout:` / `poll_interval:` / `retry.backoff:` — so
`500ms`, `5s`, `30m`, `6h`, `7d`, `1d12h` all parse with no new parser and the
value round-trips through `MarshalYAML` unchanged (which matters for the pack
lowering and `conductor config migrate`).

**The value is not templated.** No other `Duration` field in the schema is, the
YAML type decodes a scalar into a `time.Duration` before any scope exists, and
a literal is what the feature is for. A templated wait would want its own type
across every `Duration` field, not just this one. *(Decision, noted below.)*

### New files

| File | What |
| --- | --- |
| `internal/config/helpers.go` | the helper family: `HelperSleep`, `Step.HelperForm`, `Step.IsHelper`, `validateHelperStep` |
| `internal/flow/helpers.go` | the runner seam: `Runner.execHelper`, `Runner.execSleep` |
| `internal/config/helpers_test.go` | loader tests (form, durations, exclusivity, bad values, gate) |
| `internal/flow/sleep_test.go` | runtime tests (real wait, cancel, dry-run, `if:`/history, real config) |

---

## 2. Where it slots in

### `StepClass` / `Form` / `StepEngine`

- **`Step.Form()`** (`internal/config/connectors.go:1051`) gains **one** case,
  and it delegates rather than naming a helper:

  ```go
  case s.IsHelper():
      return s.HelperForm()      // "sleep"
  ```

  So `Form()` returns `"sleep"` — its own class, **not** agent / command /
  code / verb / workflow / team.

- **`Step.StepEngine()`** needs no change: a sleep step sets neither `use:`
  nor `run:`, so it already resolves to `("", EngineNone)` and every
  code-step path stays out of it. Asserted in `TestSleepStepForm`.

- **`flow.stepClass`** (`internal/flow/guard.go:237`, the agent-authored
  allow/approve matcher) returns the helper keyword ahead of its switch. See
  §7 for why it is a gated class rather than a free pass.

- The **`config.StepClassXxx`** constants in `agentauthored.go` are the
  *resource-policy* scope names (`code`/`cli`/`command`/`agent`/`workflow`) —
  they key `policy.resources` scopes for steps that can reach a store or a
  repo. A sleep step reaches nothing, so it deliberately gets no entry there.

### Dispatch

`internal/flow/flow.go:916`, `execStep`, **before** the form switch:

```go
// Helper steps (`sleep:`, and whatever joins it) are conductor's own work
// — no identity, no runtime, no connector — so they branch once, here,
// rather than adding an arm to this switch each time. See helpers.go.
if step.IsHelper() {
    return r.execHelper(ctx, t, step, id, shadow)
}
switch step.Form() { … }
```

Because it lands in `execStep`, a sleep step inherits **everything** the step
loop already does: `execStepWithFlow`'s `timeout:` / `for_each:` /
`parallel:` wrappers, `execWithRetry`, then in `runSteps` the `if:`
evaluation, the `start`/`done`/`fail` hooks, `recordOutputs`, the
`step`/`step_skipped` audit events, `checkpoint`, and the `histRec` timeline
entry. None of those needed a line changed.

### Validation

| Where | Change |
| --- | --- |
| `config.validateStep` (`connectors.go:1669`) | `s.IsHelper()` joins the `forms` tally → a sleep beside any other form is `step forms are mutually exclusive` |
| same, `forms == 0` message | now names the helper: ``…`uses:`, `call:`, or a helper (`sleep:`)`` |
| same, after the tally | calls `validateHelperStep(w, s)` |
| `config.validateHelperStep` (`helpers.go`) | `sleep` must be a **positive** duration |
| `Step.UnmarshalYAML` (`stepmerge.go:80`) | `sleep: 0` rejected **on the YAML node** (see §3) |
| `flow.validateOneStep` (`validate.go:349`) | untouched — its switch has no `default`, so an unlisted form is a no-op, which is correct for a step with nothing to resolve |
| `flow.validatePlanSteps` (`plan.go:241`) | its `default:` *does* error, so `if step.IsHelper() { break }` admits helpers there |
| `config.validateStepGate` (`gate.go:92`) | untouched and already correct — a `gate:` on a sleep step now errors with *"this is a sleep step"* (`TestSleepStepRejectsGate`) |

---

## 3. Rejecting `sleep: 0` — the one real ambiguity, and what I did

`Sleep Duration` is a value type, so a decoded `Duration(0)` is
**indistinguishable** from an absent key. Taken naively, `sleep: 0` would make
the step read as *formless* and the operator would be told to pick a step form
they had already picked.

The check therefore runs where the key is still visible — on the YAML node, in
`Step.UnmarshalYAML`, which already holds the merged node for the `<<:` /
`extends:` / `call:`-fold work:

```go
if v := valueAt(merged, "sleep"); v != nil && p.Sleep == 0 {
    return fmt.Errorf("`sleep: %s` must be a POSITIVE duration (e.g. `sleep: 5s`)", v.Value)
}
```

A **negative** duration *is* visible after the decode, so `HelperForm()`
deliberately matches `s.Sleep != 0` (not `> 0`) and `validateHelperStep`
produces the semantic error with the full `config: <where>:` prefix.

Both paths, through the real binary:

```
$ conductor validate --config bad.yaml          # sleep: 0
error: parse config: `sleep: 0` must be a POSITIVE duration (e.g. `sleep: 5s`)

$ conductor validate --config bad.yaml          # sleep: -5s
error: config: triggers[0] step step2: `sleep: -5s` must be a POSITIVE duration (e.g. `sleep: 5s`)

$ conductor validate --config bad.yaml          # sleep: 5s + uses: gh.comment
error: config: triggers[0] step step2: step forms are mutually exclusive (set exactly one of type/use/uses/call/sleep)
```

The alternative — `*Duration` — was rejected: it would have made `sleep:` the
only pointer duration in the schema and pushed a nil check into every reader,
to solve a problem that is six lines where the node already is.

---

## 4. Cancellation

`execSleep` waits on **`r.sleep`** — the Runner's existing ctx-aware wait
(`flow.go:205`), a `time.Timer` raced against `ctx.Done()`:

```go
r.sleep = func(ctx context.Context, d time.Duration) error {
    t := time.NewTimer(d)
    defer t.Stop()
    select {
    case <-ctx.Done(): return ctx.Err()
    case <-t.C:        return nil
    }
}
```

So the wait is exactly `min(duration, whatever is left of the context)`, and a
run cut short by **daemon shutdown, a step `timeout:`, a workflow timeout or a
budget stop** drops out of the sleep immediately, returning the context's own
error. That error propagates as an ordinary step error (`step "nap": context
canceled`) — the same thing every other step form does when its context dies,
so `continue_on_error:`, the `fail` hooks and the history record all behave
consistently. **There is no bare `time.Sleep` anywhere in the change.**

Reusing `r.sleep` rather than writing a second timer is deliberate: it is
already the runner's single wait primitive (retry backoff and
`while_output_matches` interval), it is injectable so tests can make waits
instant, and one primitive cannot drift from the other. Its doc comment was
updated to say it now backs both.

---

## 5. Dry-run / shadow

```go
if shadow {
    r.Log("%s [dry-run] would sleep %s", flowTag(t), d)
    return map[string]any{"stubbed": true}, "", nil
}
```

Same shape and same `{"stubbed": true}` marker as `execCode`'s and
`execVerb`'s dry-run arms, so a replay is uniformly stubbed. Real output from
`conductor replay` on a config with `sleep: 3s` — note the wall clock:

```
$ time conductor replay event.json --config once.yaml
• review_requested AcmeCorp/Widget#5300 [workflow: 3 steps] (dry-run)
flow[gh AcmeCorp/Widget#5300 review_requested] [dry-run] would run code step (cli)
flow[gh AcmeCorp/Widget#5300 review_requested] [dry-run] would sleep 3s
flow[gh AcmeCorp/Widget#5300 review_requested] [dry-run] would run code step (cli)

real    0m0.009s
```

The `shadow` flag reaches `execSleep` by the normal route (`Runner.DryRun`,
the trigger's `shadow:`, or the `Run(..., shadow)` argument), so per-trigger
shadow works too.

---

## 6. Outputs / outcome — mirroring the simplest non-agent step

A real sleep returns `map[string]any{}` (**empty**) and `raw == ""`.

- Empty rather than invented: there is nothing to report, and a field here
  would land in every downstream template scope forever.
- `""` for raw because raw exists only for `retry.while_output_matches`,
  which has nothing to match against a wait.
- Not `nil`: `recordOutputs` normalizes nil to `{}` anyway, but returning the
  map keeps `{{.steps.<id>.outputs}}` a map on every path, and
  `checkpoint`/`scrubOutputs` see the same shape a verb step gives them.
  Asserted in `TestSleepStepHonorsIfAndRecordsHistory`.

The outcome is plain success (`hist.stepDone(... "ok" ...)`, `event: step`
audit), or the context's error if interrupted. No dispatch identity, no gate,
no runtime, no workspace — nothing in those paths is reached, because none of
them is on the `execStep` → `execHelper` route.

---

## 7. Agent-authored plans

`flow.stepClass` returns `"sleep"` for a helper, which means an emitted plan
may only carry one if the operator listed `sleep` in
`policy.agent_authored.verbs`. That is the deliberate call: a sleep reaches
nothing outside conductor (it is not counted in `stepTouchesOutside`, reads no
secrets, writes no durable state, adds no sub-agent to the fleet budget), but
it *does* spend the run's wall clock, and a plan that can stall a run for
thirty minutes is a decision an operator should make by name. Every other
class works this way; treating helpers as a silent exception would have been
the surprising choice. `flow.validatePlanSteps` admits the form so the guard's
allowlist is the only thing deciding.

---

## 8. The extensibility seam

Adding a second helper (`log:`, `noop:`) is **four edits, all of them local**,
listed in the doc comment at the top of `internal/config/helpers.go`:

1. the field on `Step` (`connectors.go`, under `// helper form`),
2. a `HelperXxx` constant + its case in `Step.HelperForm`,
3. its case in `validateHelperStep`, *if* its value can be wrong,
4. its case in `Runner.execHelper` (`internal/flow/helpers.go`).

Nothing else branches on the form. `Step.Form`, the mutual-exclusion tally,
`execStep`, `flow.validatePlanSteps` and `flow.stepClass` all ask
`IsHelper()` / `HelperForm()` rather than naming a helper, so a new one
reaches every one of them by being listed in step 2. `execHelper`'s fallthrough
returns `helper %q has no runner` — unreachable through the loader, and named
out loud precisely so a config-side helper added without a runner fails loudly
rather than silently succeeding.

---

## 9. Verification — real output

### gofmt / build / vet

```
$ gofmt -l .
$ echo "gofmt-exit=$?"
gofmt-exit=0

$ go build ./... && echo BUILD-OK && go vet ./... && echo VET-OK
BUILD-OK
VET-OK
```

(`gofmt -l .` printed nothing — no unformatted files.)

### `go test ./...`

```
ok  	github.com/NodeSpy/conductor/cmd/conductor	2.522s
ok  	github.com/NodeSpy/conductor/internal/acp	(cached)
ok  	github.com/NodeSpy/conductor/internal/blob	(cached)
ok  	github.com/NodeSpy/conductor/internal/callable	(cached)
ok  	github.com/NodeSpy/conductor/internal/code	(cached)
ok  	github.com/NodeSpy/conductor/internal/config	0.827s
ok  	github.com/NodeSpy/conductor/internal/connector	(cached)
ok  	github.com/NodeSpy/conductor/internal/controller	(cached)
ok  	github.com/NodeSpy/conductor/internal/core	0.382s
ok  	github.com/NodeSpy/conductor/internal/cost	(cached)
ok  	github.com/NodeSpy/conductor/internal/dispatch	(cached)
ok  	github.com/NodeSpy/conductor/internal/engine	(cached)
ok  	github.com/NodeSpy/conductor/internal/expr	(cached)
ok  	github.com/NodeSpy/conductor/internal/flow	1.947s
ok  	github.com/NodeSpy/conductor/internal/gitdiff	(cached)
ok  	github.com/NodeSpy/conductor/internal/gitwt	(cached)
ok  	github.com/NodeSpy/conductor/internal/handoff	(cached)
ok  	github.com/NodeSpy/conductor/internal/hosts	(cached)
ok  	github.com/NodeSpy/conductor/internal/inbound	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/cron	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/github	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/rss	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/slack	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/webhook	(cached)
ok  	github.com/NodeSpy/conductor/internal/kv	(cached)
ok  	github.com/NodeSpy/conductor/internal/memory	0.398s
ok  	github.com/NodeSpy/conductor/internal/migrate	(cached)
ok  	github.com/NodeSpy/conductor/internal/models	(cached)
ok  	github.com/NodeSpy/conductor/internal/netguard	(cached)
ok  	github.com/NodeSpy/conductor/internal/notify	(cached)
ok  	github.com/NodeSpy/conductor/internal/plugin	(cached)
ok  	github.com/NodeSpy/conductor/internal/sandbox	(cached)
ok  	github.com/NodeSpy/conductor/internal/secrets	(cached)
ok  	github.com/NodeSpy/conductor/internal/skill	(cached)
ok  	github.com/NodeSpy/conductor/internal/sqlstore	(cached)
ok  	github.com/NodeSpy/conductor/internal/store	(cached)
ok  	github.com/NodeSpy/conductor/internal/vaults	(cached)
ok  	github.com/NodeSpy/conductor/pkg/githubkit	(cached)
ok  	github.com/NodeSpy/conductor/pkg/plugin	(cached)
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	(cached)
```

(Plus the `test/e2e/services/*` and `test/plugins/*` packages, all
`[no test files]`.)

### `CGO_ENABLED=1 go test -race ./...`

```
ok  	github.com/NodeSpy/conductor/cmd/conductor	11.511s
ok  	github.com/NodeSpy/conductor/internal/acp	1.086s
ok  	github.com/NodeSpy/conductor/internal/blob	1.031s
ok  	github.com/NodeSpy/conductor/internal/callable	1.829s
ok  	github.com/NodeSpy/conductor/internal/code	10.093s
ok  	github.com/NodeSpy/conductor/internal/config	5.247s
ok  	github.com/NodeSpy/conductor/internal/connector	2.292s
ok  	github.com/NodeSpy/conductor/internal/controller	1.357s
ok  	github.com/NodeSpy/conductor/internal/core	2.612s
ok  	github.com/NodeSpy/conductor/internal/cost	1.019s
ok  	github.com/NodeSpy/conductor/internal/dispatch	5.115s
ok  	github.com/NodeSpy/conductor/internal/engine	2.279s
ok  	github.com/NodeSpy/conductor/internal/expr	1.023s
ok  	github.com/NodeSpy/conductor/internal/flow	19.769s
ok  	github.com/NodeSpy/conductor/internal/gitdiff	1.227s
ok  	github.com/NodeSpy/conductor/internal/gitwt	3.683s
ok  	github.com/NodeSpy/conductor/internal/handoff	3.279s
ok  	github.com/NodeSpy/conductor/internal/hosts	1.050s
ok  	github.com/NodeSpy/conductor/internal/inbound	2.592s
ok  	github.com/NodeSpy/conductor/internal/integrations/cron	1.078s
ok  	github.com/NodeSpy/conductor/internal/integrations/github	11.878s
ok  	github.com/NodeSpy/conductor/internal/integrations/rss	1.052s
ok  	github.com/NodeSpy/conductor/internal/integrations/slack	2.069s
ok  	github.com/NodeSpy/conductor/internal/integrations/webhook	1.177s
ok  	github.com/NodeSpy/conductor/internal/kv	2.468s
ok  	github.com/NodeSpy/conductor/internal/memory	2.748s
ok  	github.com/NodeSpy/conductor/internal/migrate	3.926s
ok  	github.com/NodeSpy/conductor/internal/models	2.490s
ok  	github.com/NodeSpy/conductor/internal/netguard	1.014s
ok  	github.com/NodeSpy/conductor/internal/notify	1.551s
ok  	github.com/NodeSpy/conductor/internal/plugin	4.475s
ok  	github.com/NodeSpy/conductor/internal/sandbox	4.183s
ok  	github.com/NodeSpy/conductor/internal/secrets	4.693s
ok  	github.com/NodeSpy/conductor/internal/skill	1.023s
ok  	github.com/NodeSpy/conductor/internal/sqlstore	1.068s
ok  	github.com/NodeSpy/conductor/internal/store	1.180s
ok  	github.com/NodeSpy/conductor/internal/vaults	1.045s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	1.015s
ok  	github.com/NodeSpy/conductor/pkg/plugin	1.044s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.407s
```

No data races reported. The full suite is green under both.

### The new tests, verbosely, under `-race -count=1`

```
$ CGO_ENABLED=1 go test -race -count=1 -v \
    -run 'TestSleep|TestOnceSleepHelperStep' \
    ./internal/flow/ ./internal/config/ ./cmd/conductor/

--- PASS: TestSleepStepActuallyWaits (0.06s)
--- PASS: TestSleepStepInterruptedByCancel (0.04s)
--- PASS: TestSleepStepDryRunDoesNotWait (0.01s)
--- PASS: TestSleepStepHonorsIfAndRecordsHistory (0.03s)
--- PASS: TestSleepBetweenVerbsRealConfig (0.03s)
PASS
ok  	github.com/NodeSpy/conductor/internal/flow	1.209s
--- PASS: TestSleepStepForm (0.01s)
--- PASS: TestSleepStepRejectsBadDuration (0.01s)
    --- PASS: TestSleepStepRejectsBadDuration/zero (0.00s)
    --- PASS: TestSleepStepRejectsBadDuration/zero_duration_string (0.00s)
    --- PASS: TestSleepStepRejectsBadDuration/negative (0.01s)
--- PASS: TestSleepStepExclusiveWithOtherForms (0.03s)
    --- PASS: TestSleepStepExclusiveWithOtherForms/verb (0.01s)
    --- PASS: TestSleepStepExclusiveWithOtherForms/code (0.01s)
    --- PASS: TestSleepStepExclusiveWithOtherForms/command (0.01s)
    --- PASS: TestSleepStepExclusiveWithOtherForms/agent (0.00s)
    --- PASS: TestSleepStepExclusiveWithOtherForms/workflow_call (0.01s)
--- PASS: TestSleepStepBetweenVerbs (0.01s)
--- PASS: TestSleepStepRejectsGate (0.01s)
PASS
ok  	github.com/NodeSpy/conductor/internal/config	1.126s
--- PASS: TestOnceSleepHelperStep (0.19s)
PASS
ok  	github.com/NodeSpy/conductor/cmd/conductor	1.227s
```

### What each test asserts (against the checklist)

| Required | Test | How it proves it |
| --- | --- | --- |
| `sleep: 50ms` really delays a run | `TestSleepStepActuallyWaits` | wall clock of the whole run ≥ 45ms (timer slack), < 5s; both surrounding verb steps still fire. **Ran in 0.06s** — the delay is real and it is the sleep. |
| cancel interrupts `sleep: 10s` in ≪ 10s | `TestSleepStepInterruptedByCancel` | cancels after 30ms, asserts the run returns in < 3s **and** that the step failed with `context canceled`. **Ran in 0.04s** under `-race`. |
| dry-run does not sleep (fast + prints intent) | `TestSleepStepDryRunDoesNotWait` | `DryRun: true` over a `sleep: 10s`: run returns in < 2s (**0.01s actual**), log contains `would sleep 10s`, zero verbs invoked. |
| `sleep` + another form is a validation error | `TestSleepStepExclusiveWithOtherForms` | 5 sub-cases (verb / code / command / agent / call), each through the real loader, each `step forms are mutually exclusive`. |
| `sleep: 0` / negative errors | `TestSleepStepRejectsBadDuration` | `0`, `0s`, `-5s` — all rejected with `must be a POSITIVE duration`. |
| honors `if:` (skipped when false) + appears in the timeline | `TestSleepStepHonorsIfAndRecordsHistory` | an `if:`-false `sleep: 30s` and an `if:`-true `sleep: 20ms`: run finishes in **0.03s** (so the 30s really was skipped), and the history record has `skipped-nap` = `skipped`, `taken-nap` = `ok` with empty outputs. |
| real config with `sleep:` between two `uses:` loads **and runs** | `TestSleepStepBetweenVerbs` (loader) + `TestSleepBetweenVerbsRealConfig` (runtime, via `loadConfigViaLoader`) | the trigger parses to 3 steps with `Form() == "sleep"`; running it fires both verbs in order with rendered options, with a measurable wait between them |
| `conductor once` runs it too | `TestOnceSleepHelperStep` | one-shot end-to-end: `cli` marker → `sleep: 150ms` → `cli` marker; both marker files exist on disk and the job takes ≥ 140ms |
| extra: a `gate:` on a sleep step is refused | `TestSleepStepRejectsGate` | error says *"this is a sleep step"* |

### End-to-end through the built binary

**`conductor validate` on a real config with `sleep:` between two `uses:` steps:**

```yaml
triggers:
  - on: gh.new_comment
    name: rerun-checks
    steps:
      - id: cancel
        uses: gh.comment
        options: { repo: "{{.repo}}", number: "{{.number}}", body: "cancelling" }
      - sleep: 5s
      - id: rerun
        uses: gh.comment
        options: { repo: "{{.repo}}", number: "{{.number}}", body: "rerunning" }
```

```
$ conductor validate --config /tmp/sleepdemo/config.yaml
ok: 1 connector(s), 1 trigger(s), 0 workflow(s)
exit=0
```

**`conductor once` — one-shot, no daemon, real wait:**

```
$ time conductor once review --config once.yaml --fixture event.json
running trigger "review": review_requested AcmeCorp/Widget#5300 (3 step(s))
  · before
  ✓ before (ok) 3ms
  · step2
  ✓ step2 (ok) 3002ms
  · after
  ✓ after (ok) 3ms
outcome: ok

real    0m3.026s
exit=0

t1=1789635446.713221685 t2=1789635449.719319685
delta = 3.006 s
```

The two `use: cli` steps wrote timestamps 3.006s apart across a `sleep: 3s` —
the wait happened in one-shot mode, and the helper shows up in the one-shot
step timeline with its own duration (`✓ step2 (ok) 3002ms`).

---

## 10. Docs

- **`docs/wiki/Steps.md`** — `sleep:` added to the *Step forms* table and the
  *Fields* table, plus a new **`## Helper steps`** section: what a helper step
  is, the `cancel → sleep → rerun` example, and the six things to know
  (duration spellings + positivity, mutual exclusion / `id:` / `if:` /
  timeline / empty outputs, cancellation, dry-run, `conductor once` parity,
  and the agent-authored-plan class). It states explicitly that `sleep` is the
  first of a family and that `log:` / `noop:` are the shape of what follows.
- **`docs/wiki/Code-Steps.md`** — a callout next to the existing `use:` /
  `run:` / `call:` notes: don't reach for `use: cli, command: [sleep, "5s"]`,
  because that spawns a subprocess, needs the binary on the box (and a
  `host:` sandbox in an agent-authored plan), and blocks through a shutdown.
- **`config.example.yaml`** — a live (uncommented) `- sleep: 2s` in the
  `timer.nightly-tidy` trigger with a comment explaining the helper family.
  It is covered by `TestExampleConfigValidates`, which passes.

---

## 11. Decisions

1. **`Sleep Duration`, not `*Duration`.** As specified, and it keeps `sleep:`
   from being the schema's only pointer duration. The cost is that `sleep: 0`
   needs a node-level check; the check is six lines in a function that already
   holds the node. §3.
2. **The value is not templated.** No `Duration` field in the schema is, and a
   literal is the use case. Templated waits would be a change to the
   `Duration` type across every field that uses it, not a `sleep:`-only
   feature. Noted in the field's doc comment.
3. **Reuse `r.sleep` rather than a second timer.** It is already the runner's
   one ctx-aware wait, it is injectable for tests, and a single primitive
   cannot drift. Its doc comment now says it backs both retry and `sleep:`.
   §4.
4. **Empty outputs on a real sleep, `{"stubbed": true}` under shadow.** Empty
   because there is nothing to report; `stubbed` under shadow because that is
   what `execCode` and `execVerb` return and a replay should be uniformly
   marked. §5.
5. **A cancelled sleep fails its step** (returns `ctx.Err()`) rather than
   quietly succeeding — the same thing every other form does with a dead
   context, so `continue_on_error:`, `fail` hooks and the history record stay
   consistent. §4.
6. **`sleep` is an allowlisted class in agent-authored plans**, not a free
   pass. It reaches nothing external, but it spends the run's wall clock. §7.
7. **No `config.StepClassSleep` resource-policy constant.** Those constants
   scope `policy.resources` for steps that can reach a store or a repo; a
   sleep step reaches neither, so adding one would have implied a control that
   does not exist.
8. **`Form()` returns `"sleep"` by delegating to `HelperForm()`**, so the form
   keyword and the helper keyword can never disagree and a second helper does
   not need a second edit here. §8.
