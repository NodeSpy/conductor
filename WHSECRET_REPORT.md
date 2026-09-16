# `app.webhook_secret` → `webhook.secret`

Branch `feat/webhook-secret-under-webhook`, based on `main` @ `12280cc` (v0.10.1).

Webhook verification moved out of the GitHub connector's `app:` block and into
`webhook:`, where it belongs: the secret and the signature switch describe the
RECEIVER, not App auth. The tell was App-less configs, which had to write
`app: { webhook_secret: … }` with no App anywhere in the file.

Target shape:

```yaml
webhook:
  smee_url: ...
  listen: 127.0.0.1:8787
  path: /webhook
  secret: ${SECRET}          # was app.webhook_secret
  verify_signature: false    # was app.verify_signature
app:
  app_id: ...                # app: keeps ONLY real App-auth fields
  private_key_path: ...
```

**Commit:** `91f104b` — the implementation commit. This report is committed on top of it (a report naming its own sha is impossible), so the branch is two commits.

---

## Verification

All commands run from the repo root on this branch, after the final edit.

### `gofmt -l .`

```
(no output — clean)
```

### `go build ./...`

```
(no output — exit 0)
```

Also confirmed with the project's build setting: `CGO_ENABLED=0 go build ./...`
→ exit 0.

### `go vet ./...`

```
(no output — exit 0)
```

### `go test -race ./...` — tail

```
ok  	github.com/NodeSpy/conductor/internal/plugin	4.520s
ok  	github.com/NodeSpy/conductor/internal/sandbox	4.164s
ok  	github.com/NodeSpy/conductor/internal/secrets	4.326s
ok  	github.com/NodeSpy/conductor/internal/skill	1.024s
ok  	github.com/NodeSpy/conductor/internal/sqlstore	1.065s
ok  	github.com/NodeSpy/conductor/internal/store	1.147s
ok  	github.com/NodeSpy/conductor/internal/vaults	1.048s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	1.017s
ok  	github.com/NodeSpy/conductor/pkg/plugin	1.021s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.015s
```

Exit 0. 41 packages tested, 0 failures, no data races. (`-race` needs cgo, so
this run used the toolchain default `CGO_ENABLED=1`; the `CGO_ENABLED=0` build
above is the separate check.)

### Docker e2e — `MODE=stub bash test/e2e/run.sh`

Docker was available, so the full hermetic stack was run rather than skipped.

```
PASS=117  FAIL=0  (SKIP = genuinely N/A for this stack; see the row note)
E2E_EXIT=0
```

One SKIP, pre-existing and unrelated: `E2 Slack channel — N/A, Slack hand-off
channel not wired into the daemon (Socket Mode inbound)`.

The rows that matter for this change:

```
H      H1                PASS  H1 webhook accepted (HTTP 202)
H      H1                PASS  H1 merge_conflict dispatched from the webhook path
L      L1-backup         PASS  L1 pre-migration backup written (config.yaml.pre-connectors)
L      L1-schema         PASS  L1 config now on the connectors schema (integrations: gone)
L      L1-http           PASS  L1 webhook accepted post-migration (HTTP 202)
L      L1-works          PASS  L1 migrated trigger fixed & pushed (same event → same work)
A      A5a               PASS  A5 validation rejects two default:true controllers
A      A5b               PASS  A5 validation rejects an unknown transport
A      A5c               PASS  A5 validation accepts a single default:true controller
```

`H1` is a signed delivery verified against `webhook.secret` with
`verify_signature: true` in `connectors.e2e.yaml`. `L1-http` is stronger still:
the L1 daemon boots `legacy-migrate.yaml` — which still carries
`app.webhook_secret` / `app.verify_signature` on purpose — auto-migrates it in
place, and then accepts a signed webhook. That is the app→webhook rewrite
driving real HMAC verification in a running daemon, end to end.

---

## The exact rejection error string

```
github[gh]: app.webhook_secret moved to webhook.secret (and app.verify_signature → webhook.verify_signature) — run 'conductor config migrate'
```

on the legacy `integrations:` path, and on the connectors path:

```
connector "gh": app.webhook_secret moved to webhook.secret (and app.verify_signature → webhook.verify_signature) — run 'conductor config migrate'
```

Both wrap the one sentinel `gh.ErrAppWebhookMoved` (so `errors.Is` works) with
their own instance prefix. Verified against the built binary:

```
$ conductor validate --config /tmp/whtest/config.yaml      # connectors config with app.webhook_secret
error: connector "gh": app.webhook_secret moved to webhook.secret (and app.verify_signature → webhook.verify_signature) — run 'conductor config migrate'
exit=1
```

## How the migration-specific error is surfaced under strict parsing

The premise in the brief — "the strict `KnownFields(true)` parser will already
reject the now-unknown keys under `app:`" — turned out **not** to hold, and this
is the one design call worth reading closely.

`ConnectorRef.UnmarshalYAML` retains the connector body as a raw `yaml.Node` and
`ConnectorRef.Decode` is `r.raw.Decode(v)` — a plain, non-strict decode
(`internal/config/connectors.go:88`). `IntegrationRef.Decode` is the same
(`internal/config/config.go:327`). yaml.v3 does not propagate `KnownFields` into
a custom unmarshaler, so an unknown key inside a connector's `app:` block was
being **silently dropped**, not rejected. Left alone, removing the fields would
have taken the operator's webhook secret with them and quietly fallen back to
`verify_signature`'s default — the worst of the available failures.

So the detection is explicit, mirroring the `filters:`-removal pattern in its
*retained-detection-field* form — the same shape as `ConnectorRef.legacyType`,
which exists only so `validateConnectors` can emit a migration-specific error:

1. **Retained detection fields.** `gh.AppConfig` keeps the two yaml tags, renamed
   so nothing can read them by accident, and documented as not-part-of-the-schema
   (`internal/integrations/github/github.go`):

   ```go
   LegacyWebhookSecret string `yaml:"webhook_secret"`
   LegacyVerifySig     *bool  `yaml:"verify_signature"`
   ```

   plus `AppConfig.LegacyWebhookKeys()` as the one detection predicate.

2. **Legacy `integrations:` path** — `newIntegration` checks it right after
   decode and returns the error. A hard failure: `buildIntegrations` propagates
   it out of `config validate` and out of the daemon's boot path.

3. **Connectors path** — `newGithubImpl` checks it before any secret resolution.

Point 3 needed one small addition to make "must FAIL to load" true. `connector.Build`
treats every Impl construction error as a reason to **disable** the connector and
keep booting — deliberate, and right for an unresolvable secret or an unreadable
key file (`internal/connector/connector.go`). A moved key is a different animal:
no retry will ever clear it, and booting past it runs the connector on a
verification setting the operator believes is in effect. So `Build` now
distinguishes the two:

```go
type configError struct{ err error }
func ConfigErr(err error) error   // marks a construction failure as config-shape
...
if isConfigErr(err) { return nil, err }   // load error, not a disabled connector
```

`newGithubImpl` wraps its rejection in `ConfigErr(...)`. The error text is
unchanged by the wrapper; only its disposition changes. Without this the config
loaded with exit 0, the connector disabled and its triggers inert — a soft
failure the brief explicitly rules out. (Noted: the retired `file:`/`op://`
secret-scheme migration message still goes through the soft disable path. I
left that alone — out of scope — but the `ConfigErr` seam is now there for it.)

## The migrate rewrite

`internal/migrate/github.go` (`githubTransform`):

- `app:` is emitted only when `app_id` or `private_key_path` is set. An App-less
  legacy config whose `app:` held nothing but the secret loses the block
  entirely — which is what App-less should look like.
- `webhook:` gains `secret` / `verify_signature`, sourced from the legacy
  `app.*` keys. A value already present under `webhook:` wins over the legacy
  one (a half-migrated file migrates cleanly and idempotently).
- The `webhook:` block is now emitted when the block would otherwise be empty
  but a secret/switch exists, so a legacy config with no transport fields still
  carries its secret across.
- The move is reported in the mapping summary, not applied silently:

  ```
  #  - github[gh]: app.webhook_secret/app.verify_signature → webhook.secret/webhook.verify_signature (webhook verification is not App auth)
  ```

Verified end to end with the built binary on an App-less legacy config:

```
$ conductor config migrate --config legacy.yaml --dry-run
connectors:
  gh:
    use: github
    policy:
      reply_to_bots: decline_only
    token: ghp_x
    webhook:
      listen: 127.0.0.1:8787
      secret: shhh
      verify_signature: true
...
#  - github[gh]: app.webhook_secret/app.verify_signature → webhook.secret/webhook.verify_signature (webhook verification is not App auth)
# dry-run: transformed config validates; nothing written
```

No `app:` block in the output at all.

---

## Files changed

### Schema / behavior

| file | change |
|---|---|
| `internal/integrations/github/github.go` | `AppConfig`: `WebhookSecret`/`VerifySig` → retained `LegacyWebhookSecret`/`LegacyVerifySig` detection fields + `LegacyWebhookKeys()`; new `ErrAppWebhookMoved`; `WebhookConfig` gains `Secret`/`VerifySig` and the `Verify()` method (moved off `AppConfig`, default true); `newIntegration` rejects the legacy keys; `Validate()` now reads `Webhook.Secret`/`Webhook.Verify()` and its error names `webhook.secret` / `webhook.verify_signature` |
| `internal/integrations/github/smee.go` | `deliver()` verifies with `cfg.Webhook.Secret` / `cfg.Webhook.Verify()`; the smee startup warning names `webhook.verify_signature` |
| `internal/connector/github.go` | `githubWebhook` gains `verify_signature` (it previously accepted only `secret`, and a `verify_signature` written under `webhook:` was silently dropped — see "latent fix" below); `newGithubImpl` rejects the legacy app keys via `ConfigErr`; dropped the `app.webhook_secret` resolve + the `app ← webhook` fallback; `Source()` passes `Secret`/`VerifySig` into `gh.WebhookConfig`; connection schema `Desc` strings moved the two keys from `app` to `webhook` |
| `internal/connector/connector.go` | new `configError` / `ConfigErr` / `isConfigErr`; `Build` returns config-shape failures as load errors instead of disabling the connector |
| `internal/migrate/github.go` | the app→webhook rewrite described above |

### Tests

New:

- `internal/integrations/github/webhook_secret_test.go` — `webhook.{secret,verify_signature}` parse from YAML and drive real HMAC verification; `verify_signature` defaults on; the retired app keys are refused (3 cases: secret, switch, App-less) with `errors.Is(err, ErrAppWebhookMoved)` and the message naming `github[gh]` / `webhook.secret` / `webhook.verify_signature` / `conductor config migrate`; App-less with `webhook.secret` + `verify_signature: true` validates; an **empty `app: {}` block stays App-less**; verify-on-without-secret errors naming `webhook.secret required`; App + `webhook.secret` still validates.
- `internal/connector/github_webhook_test.go` — the connectors path: an App-less connector with `webhook.secret` builds, lowers and validates; the retired app keys **fail `connector.Build`** (not merely disable the connector).
- `internal/migrate/github_webhook_test.go` — the rewrite, including the App-less case where `app:` disappears entirely and the migrated connector still decodes a working `webhook:` block; asserts the mapping-summary note.

Updated (fixtures moved to the new location, no assertion weakened):
`internal/integrations/github/{events,events_more,gate_project,identity,issue_matched,rules,sweep,sweep_adaptive,validate,http,smee}_test.go`,
`internal/config/config_test.go` (the env-expansion probes now read
`webhook.secret`), `internal/migrate/migrate_test.go` (the `legacyGithub`
golden fixture moved its secret to `webhook:`, so the legacy→migrated
behavioral-equivalence build still constructs the legacy integration; the
app-block shape is covered by the new dedicated tests instead).

### Fixtures migrated

| file | note |
|---|---|
| `config.example.yaml` | `webhook.secret`; `verify_signature` left commented (the example's effective behavior is unchanged — verification stays on by default) |
| `config.starter.yaml` | `webhook.secret` + `verify_signature: false` |
| `config.example.legacy.yaml` | still a legacy `integrations:` file, but on the new webhook keys — otherwise `TestLegacyExampleConfigStillLoads` dead-ends before it can migrate |
| `test/e2e/config/connectors.e2e.yaml` | moved |
| `test/e2e/config/conductor.yaml` | moved |
| `test/e2e/config/controllers.yaml` | moved |
| `test/e2e/config/controllers.live.yaml` | moved |
| `test/e2e/config/resolution/a3-valid-default.yaml` | moved |
| `test/e2e/config/resolution/a5-two-defaults.yaml` | moved |
| `test/e2e/config/resolution/a5-unknown-transport.yaml` | moved |
| `test/e2e/config/legacy-migrate.yaml` | **deliberately left on the old keys**, with a comment saying why: the L1 group boots this file and asserts the daemon's own auto-migration rewrites it in place, so it now also proves the app→webhook move end to end |

`cmd/conductor/migrate_dryrun_test.go` fixtures were likewise left on the old
keys on purpose — they are legacy inputs to `config migrate --dry-run`, and
they now exercise the rewrite through the real CLI path.

Every e2e connectors config was validated with the built binary (container
paths `/etc/conductor/github-app.pem` and `/data` rewritten to temp paths):

```
=== conductor.yaml ===        ok: 7 connector(s), 14 trigger(s), 0 workflow(s)
=== connectors.e2e.yaml ===   ok: 3 connector(s), 19 trigger(s), 0 workflow(s)
=== controllers.live.yaml === ok: 1 connector(s), 5 trigger(s), 0 workflow(s)
=== controllers.yaml ===      ok: 2 connector(s), 13 trigger(s), 0 workflow(s)
```

The resolution fixtures now reach their own intended outcomes rather than
dying on the webhook key: `a3-valid-default` → `ok: 1 integration(s) configured
(1 enabled)`; `a5-two-defaults` → `at most one runtime may set default: true`;
`a5-unknown-transport` → `transport must be acp|native|cli`. Those last two are
negative fixtures — those errors are what the harness asserts.

### Docs

| file | change |
|---|---|
| `docs/wiki/GitHub-App-Setup.md` | config sample moved; field table rows `app.webhook_secret`/`app.verify_signature` → `webhook.secret`/`webhook.verify_signature` (with the default stated); a paragraph on why verification is a webhook concern and that old configs are refused with a migration message; the Transports, "Explanation", and "Running without an App" sections all renamed to the qualified keys |
| `docs/wiki/Integration-GitHub.md` | connector sample restructured (`app:` auth-only, `webhook:` transport + auth); App-less prose now says `webhook.secret` |
| `docs/wiki/Connectors.md` | the inline `gh` sample |
| `docs/wiki/Migration.md` | new row in the "What maps where" table documenting the move, that the old keys are refused at load, and that `app:` disappears when it held nothing else |

No design doc documented these keys (`rg` over `docs/design/` found none), so
none needed updating.

---

## Grep audit

```
$ grep -rn "App\.WebhookSecret|App\.VerifySig|App\.Verify\(\)|AppConfig\{.*WebhookSecret" --include="*.go" .
internal/integrations/github/webhook_secret_test.go:132:// to read App.WebhookSecret; it must now name webhook.secret.
```

The single hit is prose in a test comment. No reader survives.

```
$ grep -rn "webhook_secret|verify_signature" --include="*.yaml" .
```

Every remaining `verify_signature` is under a `webhook:` block. The only
remaining `webhook_secret` is `test/e2e/config/legacy-migrate.yaml` (intentional,
see above) and `cmd/conductor/migrate_dryrun_test.go` (a Go string, likewise).

---

## Design choices worth flagging

1. **Hard rejection on both paths, not just the connectors model.** The
   `filters:` precedent rejects only on the connectors surface and leaves the
   legacy structs permissive. I went stricter here because the brief calls it a
   HARD move, and because the failure mode of permissiveness is specifically
   bad: a legacy config whose secret is silently dropped keeps running with
   verification pointed at an empty secret. The blast radius I checked first —
   `AutoMigrate` transforms **every** file, validates the tree **once**, then
   commits (`internal/migrate/auto.go`), so there is no half-migrated
   intermediate state for the stricter check to trip over.

2. **`ConfigErr` as a new seam in `connector.Build`.** Discussed above. It is a
   genuine addition to a shared file rather than a github-local change, but the
   alternative was a config that "fails to load" by exiting 0.

3. **Latent bug fixed in passing.** `githubWebhook` accepted `secret:` but had
   no `verify_signature` field, and the connection node decodes non-strictly —
   so `webhook: { verify_signature: false }` written by an operator was accepted
   and ignored. `test/e2e/packs.sh:40` already writes exactly that. It is now
   honored. Worth a note on the PR: anyone who wrote that key under `webhook:`
   was silently running with verification ON, and will now get the setting they
   asked for.

4. **`Verify()` moved, not duplicated.** `AppConfig.Verify()` is gone rather
   than kept as a deprecated alias, so there is no second source of truth for
   the default.
