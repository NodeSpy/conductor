# `conductor-packs/` — an explicit namespace for the official pack registry

Branch `feat/pack-source-namespace`, based on `main` @ `f8e7ed1` (v0.9.10).

## What changed, in one line

A pack reference now NAMES the blessed registry — `use: conductor-packs/pr-review-team` —
instead of falling through a bare name to `NodeSpy/conductor-packs` where a
network fetch from a conductor-operated repo was indistinguishable from an
arbitrary local-looking string.

## Commit

```
<COMMIT_SHA>
```

## Resolution table — the five reference forms (`UseKindPack`)

| `use:` written | Origin | Host | Repo | Component | `Source()` | Default-trusted? |
|---|---|---|---|---|---|---|
| `pr-review-team` (bare) | — | — | — | — | — | **ERROR** (see below) |
| `conductor-packs/pr-review-team` | `official` | `github.com` | `NodeSpy/conductor-packs` | `pr-review-team` | `github.com/NodeSpy/conductor-packs//pr-review-team` | **yes** |
| `NodeSpy/conductor-packs/pr-review-team` | `github` | `github.com` | `NodeSpy/conductor-packs` | `pr-review-team` | `github.com/NodeSpy/conductor-packs//pr-review-team` | yes (same source) |
| `acme/my-packs/foo` (third party) | `github` | `github.com` | `acme/my-packs` | `foo` | `github.com/acme/my-packs//foo` | no — needs a `pack_trust.allow` entry |
| `./packs/house-style` (local) | `local` | — | — | — | `""` (not fetched) | n/a — the operator's own disk |

Plus, unchanged by this task:

- `github.com/conductor-packs/<repo>/<name>` → `github` origin, repo
  `conductor-packs/<repo>` — the documented escape hatch for a third-party org
  literally named `conductor-packs`. Verified by test.
- `conductor-packs/<name>@^1.2` and `conductor-packs//<name>` (the `//`
  component separator) both parse. Verified by test.
- **Connectors and runtimes are untouched**: `use: github` → builtin,
  `use: linear` → official *plugin* repo `NodeSpy/conductor-plugins`, component
  `connectors/linear`. `use: conductor-packs/whatever` under `connectors:` is
  still just an owner/repo pair on github.com — the alias does not leak across
  kinds. Verified by test.

## The exact new error messages

Bare pack name in a `use:` (`internal/config/use.go`, `barePackNameErr`):

```
use: "pr-review-team": a bare pack name is ambiguous — write "conductor-packs/pr-review-team" for the official registry, or "owner/repo/pr-review-team" for a third-party pack
```

A `packs:` entry with no `use:` and no `source:` (`internal/config/pack.go`,
`packDependencySource`):

```
pack "pr-review-team": no use: — a pack entry must name where it comes from: write "use: conductor-packs/pr-review-team" for the official registry, "use: owner/repo/pr-review-team" for a third-party pack, or a local path such as "use: ./packs/pr-review-team"
```

The alias written with no pack after it (`internal/config/use.go`, `ParseUse`):

```
use: "conductor-packs": "conductor-packs" names the official pack registry but no pack — write "conductor-packs/<name>"
```

All three strings are the verbatim output of a throwaway test run against the
built package, not hand-copied from source.

## How trust stays default for the blessed form

No change to `pack_trust.go` was needed, and that is the point.
`conductor-packs/<name>` resolves to the source string
`github.com/NodeSpy/conductor-packs//<name>`, which is exactly what a bare name
produced before. `PackTrustConfig.SourceAllowed` short-circuits on
`s == OfficialPacksSource || strings.HasPrefix(s, OfficialPacksSource+"/")`, and
`OfficialPacksSource` is `github.com/NodeSpy/conductor-packs`, so the `//`
continuation matches the prefix branch. `PluginSourceAllowed` has the same
branch over `{OfficialSource, OfficialPacksSource}`.

`TestPacksNamespaceAliasIsDefaultTrusted` asserts this through the resolver's
own output (`packUseSource(PacksNamespaceAlias + "/pr-review-team")`) rather
than a hard-coded string, so the alias and the trust check cannot drift apart —
and asserts the complement: a third-party `stranger/packs/foo` is refused under
an `allow: [github.com/acme/*]` policy and accepted once listed.

**One wart, noted deliberately.** `CanonicalRemoteRef("conductor-packs/x")`
returns `github.com/conductor-packs/x`, not the official source — the alias is a
`use:`-level spelling, and `CanonicalRemoteRef` canonicalizes *sources* and
*trust patterns*, which never carry it. So a `pack_trust` pattern must be
written `github.com/NodeSpy/conductor-packs`, and a pattern `conductor-packs/*`
would mean a github org of that name. Both `docs/wiki/Packs.md` and
`docs/design/use-unification.md` now say this explicitly. The existing
`TestTrustCanonicalizerAgreesWithTheUseResolver` is unaffected (it exercises
ordinary owner/repo refs) and still passes.

`namesAHost` and the host-default rules are untouched — the alias branch runs
*before* the host/owner split and only when `kind == UseKindPack` and no scheme
was written, so a host-qualified or `https://` reference never reaches it.

## Files changed

```
config.example.yaml                  |  20 ++-
docs/design/runtimes-models-packs.md |  37 ++++--
docs/design/use-unification.md       |  37 +++++-
docs/wiki/Packs.md                   |  68 ++++++++--
internal/config/pack.go              |  49 ++++---
internal/config/pack_resolve.go      |   5 +-
internal/config/pack_test.go         | 240 +++++++++++++++++++++++++++++++++--
internal/config/use.go               |  85 ++++++++++++-
8 files changed, 472 insertions(+), 69 deletions(-)
```

- `internal/config/use.go` — new `PacksNamespaceAlias` const with the rationale
  and the reservation's cost; reserved-segment branch in `ParseUse` ahead of the
  host/owner split; pack-only rejection of the bare-name case; `barePackNameErr`;
  updated the file-header search-path comment and the `UseKindPack` /
  `officialRepoFor` doc-comments.
- `internal/config/pack.go` — `packDependencySource` no longer falls back to the
  entry key; `PackInstance.Use` doc-comment rewritten (this is the pack.go:~50
  comment the task called out); `applyPackSourceDefaults` and `packUseSource`
  doc-comments updated.
- `internal/config/pack_resolve.go` — **comment only**, one hunk: the
  dependency-fallback comment said the alias implies the official repo, which is
  no longer true. No behavior change.
- `internal/config/pack_test.go` — tests (below).
- Docs + `config.example.yaml` (below).

Deliberately NOT touched, to stay clean against the concurrent
`feat/unified-filter-phase2` branch: `internal/config/pack_instantiate.go`, and
the `TriggerArm` struct in `pack.go` (my `pack.go` hunks are the `Use` field
doc-comment and the two source-resolution funcs, all far above `TriggerArm`).

## Tests

New in `internal/config/pack_test.go`:

- `TestPackNamespaceAliasResolvesToTheOfficialRepo` — origin/host/repo/component/
  name/`Source()` for `conductor-packs/pr-review-team`; also asserts
  `conductor-packs/github` and `conductor-packs/slack` resolve **official**, not
  builtin.
- `TestPackNamespaceAliasEdges` — `@^1.2` suffix, the `//` separator, the
  alias-alone error, and `github.com/conductor-packs/kit/foo` staying third-party.
- `TestBarePackNameIsAmbiguous` — the bare-name error and its two named routes.
- `TestBareNameResolutionIsUnchangedForConnectorsAndRuntimes` — builtin +
  official bare-name resolution for both other kinds, plus the alias not leaking
  into `connectors:`.
- `TestThirdPartyPackStillResolves` — `acme/my-packs/foo`.
- `TestPackEntryWithoutUseIsAnError` — the no-`use:` entry error and its content.
- `TestPacksNamespaceAliasIsDefaultTrusted` — the trust story above.

Modified:

- `TestPackKeyImpliesUse` → `TestPackUseLowersToSource`: the bare-key row is
  replaced by three rows (`conductor-packs/<name>`, an instance name that
  differs from the pack name, and the spelled-out `NodeSpy/conductor-packs/…`)
  plus an aliased-with-version row. Local / third-party / version-suffix /
  `source:`-wins rows are unchanged.
- `TestPackKeyImplicationDoesNotTouchDependencies` →
  `TestPackUseLoweringDoesNotTouchDependencies`: the parent entry now carries a
  `use:` (it must), and the assertion — a dependency's source is NOT pre-filled —
  is unchanged.
- `TestPackUseKindResolvesToItsOwnRepo` was replaced by the two alias tests
  above; its "a pack may be named after a builtin runtime" assertion survives
  inside `TestPackNamespaceAliasResolvesToTheOfficialRepo`, widened to builtin
  connectors too.

Three pre-existing tests failed before I updated them, and only those three —
exactly the three behaviors this change is supposed to alter.

## Fixtures and docs migrated

| File | What changed |
|---|---|
| `config.example.yaml` | The `packs:` block header comment: "THE KEY IS THE REFERENCE" → "EVERY ENTRY SAYS WHERE IT COMES FROM", the three-shape example now leads with `use: conductor-packs/pr-review-team`, and a paragraph on the bare-name error + the host-qualified escape. The block is fully commented out, so nothing about loading changed. |
| `docs/wiki/Packs.md` | "The key is the reference" → "Every pack says where it comes from": the reserved namespace, the three shapes, the verbatim bare-name error, the reservation's cost in a callout. Added the default-trust paragraph to "Trusted sources", including the note that patterns match sources not `use:` spellings. Added the now-required `use:` to the override example at §"Overriding pack internals". |
| `docs/design/use-unification.md` | §B: kind list now names `packs:`; new subsection "Packs: cases 1–2 are replaced by a reserved namespace" with the replacement table, the trust note, and the reservation's cost. |
| `docs/design/runtimes-models-packs.md` | §5.1 rewritten to the `use:`-carries-the-reference form with an explicit **Superseded** note recording that key-implies-`use:` is withdrawn and why. §5.2's `incident-responder: {}` now carries a `use:`. The §7 implementation-map bullet updated to name `PacksNamespaceAlias`. |

**No in-repo config used a bare official pack name or a no-`use:` pack entry.**
I swept `test/e2e/`, `test/plugins/`, `examples/`, `testdata/`, `docs/`, and the
three `config.*.yaml` files. `test/e2e/packs.sh` and
`examples/packs/review-kit/README.md` both use an explicit `source:` (a local
path and a `git::file://` repo), which this change does not touch. So there was
nothing to migrate in the fixtures — only the prose that taught the old rule.

## Verification — real output

`gofmt -l` on every touched Go file (no output = clean):

```
$ gofmt -l internal/config/use.go internal/config/pack.go internal/config/pack_resolve.go internal/config/pack_test.go internal/config/pack_trust.go
gofmt clean (no output above)
```

`go build` and `go vet` (no output = clean):

```
$ go build ./... && echo ok
ok
$ go vet ./... && echo ok
ok
```

`go test -count=1 -race ./...` — 40 packages, all `ok`, exit 0:

```
$ go test -count=1 -race ./... 2>&1 | grep -v "no test files" > /tmp/race.txt; echo "exit=$?"
exit=0
$ grep -c "^ok" /tmp/race.txt
40
$ grep -v "^ok" /tmp/race.txt | head        # nothing that is not "ok"
$ tail -6 /tmp/race.txt
ok  	github.com/NodeSpy/conductor/internal/sqlstore	1.081s
ok  	github.com/NodeSpy/conductor/internal/store	1.131s
ok  	github.com/NodeSpy/conductor/internal/vaults	1.041s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	1.013s
ok  	github.com/NodeSpy/conductor/pkg/plugin	1.020s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.013s
```

Earlier full `-race` run, unfiltered tail (`internal/config` 4.594s):

```
ok  	github.com/NodeSpy/conductor/internal/config	4.594s
...
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.018s
```

Example configs still load (`config.example.yaml` is what a live fleet copies):

```
$ env GH_WEBHOOK_SECRET=x GH_SMEE_URL=x SLACK_APP_TOKEN=x SLACK_BOT_TOKEN=x CW_SECRET=x \
      CONDUCTOR_INVOKE_TOKEN=x CONDUCTOR_INVOKE_HMAC=x \
      go run ./cmd/conductor validate --config config.example.yaml
connector "gh" disabled: app credentials: read app key: open ~/.config/conductor/github-app.pem: no such file or directory
connector "gh" disabled (...) — 5 trigger(s) inert
ok: 5 connector(s), 10 trigger(s), 1 workflow(s)

$ env GH_WEBHOOK_SECRET=x GH_SMEE_URL=x go run ./cmd/conductor validate --config config.starter.yaml
connector "gh" disabled: connector "gh": app: needs both app_id and private_key_path
connector "gh" disabled — 3 trigger(s) inert
ok: 1 connector(s), 3 trigger(s), 0 workflow(s)
```

(The `gh` connector disables because this box has no GitHub App key — environment,
not config. Both configs reach `ok:`.) `cmd/conductor/example_config_test.go`
covers all three example files and is green in the suite above.

## Two pre-existing breakages I did NOT introduce and did NOT fix

Both reproduce identically on a clean `git stash` of my work, i.e. on `f8e7ed1`:

1. **`config.example.legacy.yaml` does not pass a direct `validate`** — it fails
   with `line 409: field agents not found in type config.Config`. It is a
   *pre-migration* snapshot, consumed by `internal/migrate/migrate_test.go`
   through the migrator, not by the loader; that test is green. Verified against
   the stashed baseline: byte-identical error.
2. **`test/e2e/packs.sh` fails at step 1** — `line 9: field agents not found in
   type config.Config` / `line 17: field agents not found in type
   config.PackInstance`. Its generated consumer config still writes the retired
   `agents:` key. Verified against the stashed baseline: byte-identical error.
   This script is **not** invoked by `test/e2e/run.sh`, which is what CI runs, so
   CI is not gated on it. It also uses explicit local/`git::file://` `source:`
   values, so the namespace change is orthogonal to its failure.

I left both alone: fixing #2 means editing the pack fixture's `agents:`/`steps:`
shape, which is squarely in the territory the concurrent phase-2 branch is
editing, and the task asked me to stay narrow. **Neither is fixed. Flagging, not
claiming.**

## Design choices I made

1. **The bare-name rejection happens before the builtin lookup, not after.**
   `builtinFor(kind, name)` falls through to the *connector* registry for any
   non-runtime kind, so on `main` a pack named `github` or `slack` would have
   resolved `OriginBuiltin` — a latent mis-resolution. Rejecting packs at the top
   of the bare-name branch removes it. Asserted by
   `TestPackNamespaceAliasResolvesToTheOfficialRepo`.
2. **The alias branch sits before the host/owner split**, gated on
   `kind == UseKindPack && scheme == ""`. Placing it after would have required
   un-defaulting the host; placing it before makes the escape hatch fall out for
   free — a host-qualified or scheme'd ref never has `conductor-packs` as its
   first segment.
3. **A multi-segment component after the alias is allowed**
   (`conductor-packs/a/b` → component `a/b`, name `b`), mirroring how every other
   remote form treats everything past the repo as the component. The official
   repo is flat today, so this is forward-compatibility, not a used path.
4. **`conductor-packs` alone gets its own error** rather than the generic bare-name
   one, since the operator clearly meant the registry and only omitted the pack.
5. **`packDependencySource` keeps its name and signature** even though it no
   longer derives anything from the `name` argument except the error text. Its
   two callers (`applyPackSourceDefaults`, `pack_resolve.go`) both want the
   entry name in the message, and renaming it would widen the diff against the
   concurrent branch for no gain.
6. **`OriginOfficial` is reused for the alias** rather than a new origin value.
   `IsRemote()`, `Source()`, `InstallKey()`, and every `plugin list`-style
   consumer already behave correctly for it, and the resolved identity really is
   the same official repo — only the spelling at the reference site changed.
