# Which bundled connectors can leave core (issue #59) — investigation result

**Verdict: sentry and pagerduty are out. github and slack are NOT extractable
without dropping features.** This is the honest account, with file:line
evidence, of what moved, what did not, and why.

Measured, not asserted: removing sentry + pagerduty takes the stripped daemon
from **46,379,273 to 46,280,969 bytes — 98,304 bytes, 0.21%**. Both were thin
wrappers over packages (`net/http`, `internal/inbound`, `internal/config`,
`internal/core`) that stay linked for the remaining connectors. The gain is
attack surface, dependency count, and blast radius, not bytes. Anyone quoting
this work as "a much smaller daemon" is overselling it.

## The test for extractability

A bundled connector can leave core only if **every** capability the daemon
consumes from it is expressible over the plugin protocol
(`pkg/plugin`): a `describe` call, `invoke` verb calls, and one-way
`plugin.event` notifications from a `start_source` stream.

The protocol has no reverse RPC. A plugin cannot be *called back* by the daemon
outside a verb invocation, cannot hand the daemon a credential on demand, and
cannot expose a Go interface for the daemon to type-assert on. Anything the
daemon reaches into the integration for is therefore a hard blocker.

## sentry, pagerduty — EXTRACTED

Both were source-only: no verbs, no daemon-side callbacks, no interfaces the
daemon asserted on. Their entire contribution was "receive a webhook, verify an
HMAC, emit a normalized trigger" — exactly one `start_source` stream. Nothing in
conductor's own autopilot consumed them; they were opt-in alerting inputs.

They now ship from the `conductor-plugins` repo. `internal/integrations/{sentry,
pagerduty}` and their connector decls/impls/filters in
`internal/connector/sources.go` are deleted.

### The one thing this is not: a drop-in migration

`conductor config migrate` deliberately does **not** transform a legacy
`integrations: sentry|pagerduty` entry. It skips it with an actionable note
(`internal/migrate/legacy_extracted.go`), because the plugin's contract is not
the bundled connector's:

- **Event names differ.** The bundled connector had one `alert` event covering
  the issue/error/event_alert resources; the plugin declares three
  (`issue_alert`, `error_alert`, `event_alert`). A migrated `on: errors.alert`
  would name an event nothing emits.
- **Context shape differs.** The bundled integration emitted a nested map, so
  steps templated `{{.sentry.title}}`. The plugin emits flat keys plus
  filter-key aliases. A migrated prompt would render empty.
- **`exclude:` is not evaluated.** The bundled connectors had type-specific
  filter functions that understood the exclusion maps the migration generated to
  preserve legacy first-match-wins rule precedence. Plugin sources go through
  the generic evaluator (`internal/connector/pluginsource.go:100` `filterMatch`),
  which requires every filter key to be present in the event context — so an
  `exclude:` key makes the trigger match *nothing*.

Auto-transforming would emit a config that looks migrated but whose triggers
never fire. Skipping with instructions is the honest behaviour. The rest of a
mixed legacy file still migrates, so one extracted integration does not block
anyone's migration.

Closing this gap properly (making a plugin source a true drop-in for a bundled
one) means teaching the plugin-source layer to nest context under the connector
type and to evaluate a generic `exclude:` key. That is a separate change with
its own tests, not a side effect of deleting two integrations.

## github — NOT extractable

The daemon does not merely *route* github events; its own autopilot is built on
the bundled integration. Four independent blockers, each a Go interface the
daemon type-asserts on:

1. **App-token re-mint on resume.** `cmd/conductor/main.go:878` `appTokener`
   (`AppToken(ctx, installationID)`), consumed by
   `cmd/conductor/main.go:884` `refreshAppToken` and wired into the engine at
   `cmd/conductor/main.go:528`. When a held or restarted PR workflow resumes,
   the engine re-mints an installation token
   (`internal/engine/engine.go:1075`). Only
   `internal/integrations/github/github.go:275` implements it.

2. **Dispatch tuning and agent credentials.**
   `cmd/conductor/main.go:965` `dispatchTuner` (`RetryPolicy()`,
   `IdentityTokens()`), consumed by `cmd/conductor/main.go:975`
   `dispatchTuning`. This is where the retry policy and the read/write tokens
   handed to *every dispatched agent* come from — they land in the agent's
   environment as `GH_TOKEN`/`GITHUB_TOKEN`/`PC_GH_WRITE_TOKEN`
   (`internal/dispatch/provision.go:69`). Implemented only at
   `internal/integrations/github/github.go:284` and `:293`.

3. **The catch-up sweep.** `cmd/conductor/main.go:1150` `sweepNower`, driven by
   the SIGUSR1 handler (`cmd/conductor/main.go:717` and `:809`), by
   `conductor sweep` (`cmd/conductor/main.go:1113` `cmdSweep`, which asserts a
   `SweepOnce` interface), and by the daemon-global `sweep` verb
   (`internal/connector/github.go:756` `runSweepHook`). The sweep is a
   long-lived, adaptive, in-daemon poll loop
   (`internal/integrations/github/sweep.go`) — not a request/response verb.

4. **Events that need polling plus "me" identity.** `merge_conflict`,
   `pr_behind`, `failing_checks`, `stuck_checks`, `merge_ready`, `self_review`,
   and `issue_matched` are derived by cross-referencing several API calls
   against the operator's own identity and prior state. A stateless plugin
   process handed one webhook payload cannot compute them.

The `conductor-github` plugin covers what genuinely *is* expressible: the full
verb surface via `pkg/githubkit`, and the webhook events derivable from a single
delivery (`new_comment`, `review_requested`, `changes_requested`, `release`,
`deployment_status`, `dependabot_alert`, `secret_scanning_alert`). It is
**additive** — a second way to reach github, useful for isolating credentials in
a sandboxed subprocess — not a replacement. `internal/connector/github.go` is
unchanged and stays the daemon's github.

**No safe subset was removed.** Deleting only the verbs would leave
`internal/connector/github.go` needing them for its own decl; deleting only the
stateless events would not remove the sweep, the token seams, or `gh.Config`
(`internal/connector/github.go:668`), which the source face lowers into.

## slack — NOT extractable

Slack's Socket Mode connection is the *return path* for interactive hand-offs, a
core feature: an agent asks a question in a thread, a human replies, the reply
resumes the run.

- `internal/integrations/slack/slack.go:35` `SetReplyHook` is a package-level
  global the daemon installs at startup —
  `cmd/conductor/main.go:942` (`wireSlackHandoffInbox`, `main.go:937`) for
  `handoffs:` entries and `cmd/conductor/connectors.go:325` for connector-model
  inboxes. Each delivers an inbound thread reply into
  `internal/handoff`'s registry. This is a **daemon-inward** call from the
  integration. The plugin protocol's only plugin→daemon message is a
  `plugin.event` trigger notification; there is no way for a plugin subprocess
  to resolve a pending hand-off.
- `cmd/conductor/main.go:907` `completionHandler`, implemented at
  `internal/integrations/slack/handle.go:202` `HandleCompletion`, is a
  daemon→integration callback delivering a dispatch's final outcome so slack can
  post its `on_done`/`on_fail` feedback. There is no plugin verb for "the run you
  triggered just finished".

`cmd/conductor/main.go:953` `anySlackIntegration` exists precisely because a
slack hand-off without a running slack integration is a silently stuck
hand-off — the daemon warns about it at boot. Moving slack out of process would
make that the permanent state.

## Consequence for the type-replace protection

`internal/connector/external.go:31` refuses to let a plugin provide a **bundled**
connector type. Nothing needed relaxing: sentry and pagerduty are no longer
bundled, so `plugins: { sentry: … }` works by construction, while github (still
bundled) stays protected.
`internal/migrate/extracted_plugin_test.go` `TestPluginCanProvideFormerlyBundledType`
asserts all four halves: the two types are gone from the bundled registry, a
plugin can register them and is tagged external, github is still unreplaceable,
and a second plugin claiming the same type still collides.
