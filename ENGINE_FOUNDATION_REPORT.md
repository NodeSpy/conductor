# Code-step engines — FOUNDATION increment

Branch `feat/engines-cli-foundation`, off `main` (`c463023`).

Scope, as specified: the engine `UseKind`, its resolution, the builtin engine
registry, the config surface (`use:` / `call:` / `command:` / the `run:`
alias), and the built-in `cli` engine wired for **inputs + outputs only**. The
plugin wire protocol (`pkg/plugin/wire.go`, `internal/plugin`) and the
ctx-over-socket data plane are untouched — next increment.

- **Commit:** `95a510dae469cb26e38ab026f4af3a640cff8d45`

---

## 1. Verification

### gofmt

```
$ gofmt -l .
gofmt-exit=0
```

(no files listed — clean)

### build

```
$ CGO_ENABLED=0 go build ./...
build ok
```

### vet

```
$ go vet ./...
vet ok
```

### `go test ./...`

Every package `ok`. Tail:

```
ok  	github.com/NodeSpy/conductor/internal/store	(cached)
ok  	github.com/NodeSpy/conductor/internal/vaults	(cached)
ok  	github.com/NodeSpy/conductor/pkg/githubkit	(cached)
ok  	github.com/NodeSpy/conductor/pkg/plugin	(cached)
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	(cached)
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
```

### `go test -race ./...`

Green, no data races. Exact tail:

```
ok  	github.com/NodeSpy/conductor/internal/acp	1.078s
ok  	github.com/NodeSpy/conductor/internal/blob	1.503s
ok  	github.com/NodeSpy/conductor/internal/callable	2.221s
ok  	github.com/NodeSpy/conductor/internal/code	33.712s
ok  	github.com/NodeSpy/conductor/internal/config	8.429s
ok  	github.com/NodeSpy/conductor/internal/connector	3.575s
ok  	github.com/NodeSpy/conductor/internal/controller	1.727s
ok  	github.com/NodeSpy/conductor/internal/core	3.774s
ok  	github.com/NodeSpy/conductor/internal/cost	1.255s
ok  	github.com/NodeSpy/conductor/internal/dispatch	13.955s
ok  	github.com/NodeSpy/conductor/internal/engine	3.079s
ok  	github.com/NodeSpy/conductor/internal/expr	1.060s
ok  	github.com/NodeSpy/conductor/internal/flow	37.612s
ok  	github.com/NodeSpy/conductor/internal/gitdiff	2.280s
ok  	github.com/NodeSpy/conductor/internal/gitwt	17.032s
ok  	github.com/NodeSpy/conductor/internal/handoff	4.305s
ok  	github.com/NodeSpy/conductor/internal/hosts	1.221s
ok  	github.com/NodeSpy/conductor/internal/inbound	3.440s
ok  	github.com/NodeSpy/conductor/internal/integrations/cron	1.140s
ok  	github.com/NodeSpy/conductor/internal/integrations/github	14.328s
ok  	github.com/NodeSpy/conductor/internal/integrations/rss	1.127s
ok  	github.com/NodeSpy/conductor/internal/integrations/slack	2.215s
ok  	github.com/NodeSpy/conductor/internal/integrations/webhook	1.932s
ok  	github.com/NodeSpy/conductor/internal/kv	4.659s
ok  	github.com/NodeSpy/conductor/internal/memory	7.202s
ok  	github.com/NodeSpy/conductor/internal/migrate	13.417s
ok  	github.com/NodeSpy/conductor/internal/models	4.312s
ok  	github.com/NodeSpy/conductor/internal/netguard	1.048s
ok  	github.com/NodeSpy/conductor/internal/notify	1.717s
ok  	github.com/NodeSpy/conductor/internal/plugin	9.778s
ok  	github.com/NodeSpy/conductor/internal/sandbox	4.660s
ok  	github.com/NodeSpy/conductor/internal/secrets	8.705s
ok  	github.com/NodeSpy/conductor/internal/skill	1.122s
ok  	github.com/NodeSpy/conductor/internal/sqlstore	1.388s
ok  	github.com/NodeSpy/conductor/internal/store	1.651s
ok  	github.com/NodeSpy/conductor/internal/vaults	1.136s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	1.038s
ok  	github.com/NodeSpy/conductor/pkg/plugin	1.036s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.057s
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
```

(head of the same run: `ok github.com/NodeSpy/conductor/cmd/conductor`.)

### Binary checks

Built with `CGO_ENABLED=0 go build -o /tmp/conductor-eng ./cmd/conductor`.

**(a) `use: cli, command: [bash, -c]` + `code:` loads**

```
$ /tmp/conductor-eng validate --config a-cli.yaml
ok: 1 connector(s), 0 trigger(s), 1 workflow(s)
```

(the same file also carries `use: cli` + argv-only, `run: js`, `run: bash`,
and `type: command` steps — all five load together.)

**(b) an old step-level `use: <workflow>` fails, then migrates**

```
$ /tmp/conductor-eng validate --config b-old.yaml
error: config: triggers[0] step call: `use: review-flow` selects a code ENGINE, but
"review-flow" is a workflow — write `call: review-flow` (a step-level `use:` used to
mean the workflow call; `conductor config migrate` rewrites it)

$ /tmp/conductor-eng config migrate --config b-old.yaml
config migrate: b-old.yaml → connectors schema (backup b-old.yaml.pre-connectors)
  - migrated b-old.yaml (backup: b-old.yaml.pre-connectors)
  - rewrote 1 step-level `use:` to `call:` (a step's `use:` now selects the code engine)
migrated 1 file(s); originals backed up with the .pre-connectors suffix

$ sed -n '/^triggers:/,$p' b-old.yaml
triggers:
  t:
    on: gh.new_comment
    steps:
      - {id: call, call: review-flow, with: {pr: 1}}

$ /tmp/conductor-eng validate --config b-old.yaml
ok: 1 connector(s), 1 trigger(s), 1 workflow(s)
```

**(c) the example configs still load**

```
$ conductor validate --config config.example.yaml
ok: 5 connector(s), 10 trigger(s), 1 workflow(s)
$ conductor validate --config config.starter.yaml
ok: 1 connector(s), 3 trigger(s), 0 workflow(s)
$ conductor config migrate --config <copy of config.example.legacy.yaml>  # legacy fixture
… migrated 1 file(s)
$ conductor validate --config <migrated copy>
ok: 5 connector(s), 28 trigger(s), 0 workflow(s)
```

`config.example.legacy.yaml` does not pass `validate` directly — it is the
LEGACY fixture (`agents:` block) that exists to be migrated. Confirmed
pre-existing: stashing this branch's changes and running the same command on
`main` gives the identical `field agents not found` error.

**unknown engine error**

```
$ /tmp/conductor-eng validate --config c-bad.yaml
error: config: workflow w step x: `use: wasmtime` names no engine conductor can run —
the builtins are cli, go-embed, js, lua, risor, or name a host interpreter (bash, node,
python3, …) or a path to one; plugin-backed engines are not wired up yet. To CALL a
workflow, use `call:`
```

### Grep: nothing treats a step `use:` as a workflow call

Every reader of `Step.Use` in non-test code is engine resolution:

```
internal/config/engines.go:87    EngineSelector  — use: else run:
internal/config/engines.go:112   StepEngine      — `use:` strictness split
internal/config/engines.go:142-3 validateStepEngine — use:/run: both-set guard
internal/config/engines.go:178   engineKey       — which key to quote in an error
internal/config/connectors.go:1052 Form()        — code form
internal/config/connectors.go:1663/1667 validateStep — form counting
```

and a `grep` for any `.Use` reader mentioning workflow/call returns only
`internal/config/connectors.go:1663` (the form-count line, where `s.Workflow`
and `s.Use` sit side by side as different forms).

### e2e

`test/e2e/config/*.yaml` contained **no** step-level workflow `use:` — nothing
to migrate there. All four runnable e2e fixtures load and validate under the
new schema (`conductor.yaml`, `connectors.e2e.yaml`, `controllers.yaml`,
`controllers.live.yaml`); `bad-filter.yaml` is an intentional-failure fixture
and `legacy-migrate.yaml` / `unmappable.yaml` are legacy migration fixtures.

A new **K9** scenario exercising `use: cli` + `command:` was added to
`connectors.e2e.yaml` and asserted in `run.sh`. See §7 for its status.

---

## 2. Files changed, by area

**Engine kind + resolution (`internal/config`)**

| file | what |
| --- | --- |
| `use.go` | `UseKindEngine`, `Dir() → "engines"`, `builtinEngines` registry + `RegisterBuiltinEngine`/`BuiltinEngine`, `BuiltinNames`/`builtinFor` wiring, `otherKind` note |
| `use_engine_test.go` | *new* — the resolution table, the registry, the kind-isolation regression |

**Config surface (`internal/config`)**

| file | what |
| --- | --- |
| `engines.go` | *new* — `EngineClass`, `Step.EngineSelector`/`Step.StepEngine`, `validateStepEngine`, the unknown-engine error, the `Argv` type + `splitArgv` |
| `connectors.go` | `Step.Use`, `Step.Call`, `Command Argv`; `Form()` and `validateStep` form counting; engine validation hook; `import:` message now says `call:` |
| `stepmerge.go` | `Step.UnmarshalYAML` folds `call:` into `Workflow` (and rejects both-set) |
| `pack_reflint.go` | code-body scan keyed off `EngineSelector()` rather than `Run` |
| `engines_test.go` | *new* — step parse/validation for every surface below |
| `stepmerge_test.go` | two `reflect.DeepEqual` assertions converted for the `Argv` named type |

**The `cli` engine (`internal/code`)**

| file | what |
| --- | --- |
| `cli.go` | *new* — `execCLILocal`, `execCLIRemote`, `remoteCLIScript`, `cliArgv` |
| `code.go` | `Spec.Command`; `Exec` dispatches `cli`; package doc |
| `hostinterp.go` | `execRemote` hands a `cli` spec to `execCLIRemote` |
| `cli_test.go` | *new* — inputs, outputs, code-file mapping, args, failures, env allowlist, remote + argv quoting |

**Step dispatch (`internal/flow`)**

| file | what |
| --- | --- |
| `flow.go` | `execCode` builds its Spec from `StepEngine()`, renders and secret-resolves `command:` |
| `validate.go` | code-form check uses `EngineSelector()` |
| `plan.go` | agent-authored plan: code-form check uses `StepEngine()`, rejects a plugin engine and a `cli` step with no `command:` |
| `guard.go` | a `cli`-engine step classes as `"command"` for the plan allowlist |
| `supervise.go` | the approval summary renders `use: …` / `call: …` |
| `engines_test.go` | *new* — routing by engine, argv templating, dry-run stub, plan step class |

**Migration (`internal/migrate`)**

| file | what |
| --- | --- |
| `stepcall.go` | *new* — `applyStepCallPass` (step `use:` → `call:`) |
| `migrate.go` | pass wired into both the connectors-schema branch and the legacy-output branch |
| `stepcall_test.go` | *new* — rewrite, idempotency, every nesting site, no-op case |

**CLI / docs / fixtures**

| file | what |
| --- | --- |
| `cmd/conductor/packs.go` | `pack show` exports hint prints `call:` |
| `cmd/conductor/plugins.go` | `plugin list` lists the builtin engines as the third kind |
| `docs/wiki/Steps.md` | new "Step forms" section; `use`/`command`/`call` in the Fields table |
| `docs/wiki/Code-Steps.md` | `use:` engine selection, the `run:` alias note, the `call:` warning, a "The `cli` engine" section, ctx/trust/timeout paragraphs updated |
| `docs/wiki/Workflows.md` | step forms + file-based references on `call:` |
| `docs/wiki/Configuration.md`, `Examples.md`, `Packs.md` | step `workflow:` → `call:` |
| `config.example.yaml` | `call:` for the workflow call; `use: js` and a `use: cli` example with the legacy forms kept as comments |
| `examples/packs/review-kit/conductor-pack.yaml` | `workflow: review-flow` → `call:` |
| `test/e2e/config/connectors.e2e.yaml`, `test/e2e/run.sh` | K9 `use: cli` scenario |

---

## 3. The surface as implemented

### `use:` — the code engine

```yaml
- { id: shape, use: js,  code: "return { sev: ctx.body.severity }" }
- { id: build, use: cli, command: [make, -C, ./svc, release] }
- { id: py,    use: python3, code: "…" }
- { id: pin,   use: /opt/venv/bin/python, code: "…" }
```

Resolved by `Step.StepEngine()`, which returns `(selector, EngineClass)`:

| selector | class | runs as |
| --- | --- | --- |
| `cli` | `EngineCLI` | subprocess over `command:` |
| `js`, `go-embed`, `risor`, `lua` | `EngineInProcess` | in-process, local-only |
| a path (`./x`, `../x`, `/x`, `~/x`) | `EngineHost` | host interpreter at that path |
| a name in the host-interpreter list | `EngineHost` | host interpreter on PATH |
| anything else | `EnginePlugin` | **rejected** at load (not wired up yet) |

The host-interpreter list is `sh bash zsh dash ksh fish ash node deno bun
python python2 python3 ruby perl php lua5.1 go Rscript osascript pwsh
powershell tclsh awk`. It exists only because `use:` must choose between two
readings of a bare word; `run:` consults no list at all.

### `run:` — the back-compat alias

`run:` selects the same engine. Differences, both deliberate:

- `run: <anything unrecognized>` is a **host interpreter** — today's behavior
  byte for byte, so no existing config changes meaning or stops loading.
- `run: cli` now selects the cli engine rather than looking for a program
  literally named `cli` on PATH. (Specified. Noted as a decision below.)

Setting both `use:` and `run:` is an error.

### `command:` — the argv

A list of words, or one string split on whitespace with single/double quotes
and backslash honored (`internal/config.Argv` / `splitArgv`). **No shell**: no
variable expansion, no globbing, no `;`. It is required by `use: cli`, and an
error on any other engine (which would silently ignore it).

`command:` on a step with **no** engine is still the `type: command` form,
unchanged.

### `call:` — the workflow call

```yaml
- { id: review, call: review-flow, with: { pr: "{{.pr}}" } }
```

`call:` and `workflow:` are **one field**. `Step.UnmarshalYAML` folds `Call`
into `Workflow` before validation, walking, or pack rewriting runs, so every
downstream reader still reads `Step.Workflow` and cannot be surprised by which
key an operator wrote. Setting both to different values is an error.

---

## 4. Migrator behavior

`applyStepCallPass` (`internal/migrate/stepcall.go`) rewrites **every**
step-level `use: <scalar>` to `call: <scalar>`.

- **Unconditional.** No lookup of the name against the config's workflows. A
  step `use:` has only ever had one meaning, so there is nothing to
  disambiguate — and a conditional rewrite would leave exactly the configs
  whose workflow lives in another imported file (the common split) silently
  unmigrated and then broken at load.
- **Raw-node**, like the other standalone passes: comments, anchors, and
  formatting survive.
- **Scoped to steps.** It walks `triggers:` (map and list shapes) and
  `workflows:` entries' `steps:` lists, the one-step `checks:` entries, and the
  `parallel:` branches and `compensate:` bodies nested inside any of them. A
  connector's or runtime's `use:` is a different key in a different block and
  is never in scope (asserted by `TestStepUseBecomesCall`).
- **No-op is free.** A config with no step-level `use:` returns
  `changed=false`, so boot auto-migration does not rewrite every file on the
  box (`TestNoStepUseNoRewrite`).
- **Idempotent** (`TestStepUseMigrationIsIdempotent`).
- **Summary line:** ``rewrote N step-level `use:` to `call:` (a step's `use:`
  now selects the code engine)``.

`workflow:` is deliberately **not** rewritten — it is the other, still-valid
spelling of the same field, and churning it would touch every config in the
wild for a synonym.

`type: command` is untouched and keeps working. It is equivalent to
`use: cli, command: […]` modulo the execution path (`type: command` goes
through the dispatcher; `use: cli` goes through the code path and so gets ctx
on stdin and structured outputs) — noted in the migrator's docs but not
rewritten.

---

## 5. How `cli` maps `command:` + `code:`

One rule, no special cases:

```
argv = command… [+ code-file path, when code: is set] [+ args…]
```

`code:`, when present, is written to a private `0700` dir / `0600` file and
that **path is appended to the argv** — exactly what the host-interpreter path
already does. So:

| written | equals |
| --- | --- |
| `use: cli, command: [bash]` + `code:` | `run: bash` |
| `use: cli, command: [python3]` + `code:` | `run: python3` |
| `use: cli, command: [bash, -c, "echo hi"]` | an inline shell one-liner, no `code:` |

The rule is the FILE, never the text: a `code:` body is arbitrary bytes, and
an engine that sometimes passed it as an argv word would differ from the
host-interpreter path precisely where the body got interesting (quotes,
newlines, a NUL).

**Noted:** the spec sketched `use: cli, command: [bash, -c]` + `code:` as
"≈ today's `run: bash`". Under the one rule above that spelling produces
`bash -c /tmp/…/code`, which asks bash to execute the *path* as a command
rather than the file as a script — so it loads (verification 3a asks only that
it load, and it does) but the spelling that actually reproduces `run: bash` is
`command: [bash]` + `code:`. That is what the docs and the e2e fixture use. I
chose one unconditional rule over a "last argv word is a bare flag → pass the
code as its operand" heuristic, because the heuristic silently does the wrong
thing for `command: [foo, -v]`.

**ABI bridged (this increment): inputs and outputs only.**

- **inputs** — the rendered ctx document as JSON on the command's **stdin**,
  the same mechanism `internal/code/hostinterp.go` uses.
- **outputs** — `ParseOutputs` over stdout (`internal/code/outputs.go`),
  unchanged.
- **no `ctx.store` / `ctx.sql` / `ctx.memory`** — those are in-process
  bindings; reaching them from a subprocess needs the data plane that is the
  next increment.

Also carried over from the host-interpreter path, unchanged: the allowlisted
spawn base environment (`spawnBaseEnv` + the step's own `env:`, never the
daemon's), `workdir:`, `timeout:` via the context, and `host:`/inline `ssh:`
through the same generated remote sh script (argv shell-quoted, `code:`
base64-framed, ctx on stdin, exit 127 = "not found on host").

---

## 6. Resolution table (`UseKindEngine`)

`ParseUse(UseKindEngine, ref)` — no I/O, decides only where the implementation
comes from. Identical mechanics to a connector.

| `use:` | origin | repo | component | source |
| --- | --- | --- | --- | --- |
| `cli` | builtin | | | *(none)* |
| `js` | builtin | | | *(none)* |
| `go-embed` | builtin | | | *(none)* |
| `risor` | builtin | | | *(none)* |
| `lua` | builtin | | | *(none)* |
| `foo` | official | `NodeSpy/conductor-plugins` | `engines/foo` | `github.com/NodeSpy/conductor-plugins//engines/foo` |
| `acme/wasm` | github | `acme/wasm` | | `github.com/acme/wasm` |
| `acme/plugins/wasm` | github | `acme/plugins` | `wasm` | `github.com/acme/plugins//wasm` |
| `git.corp.example/team/engines//wasm` | host | `team/engines` | `wasm` | `git.corp.example/team/engines//wasm` |
| `./bin/conductor-wasm` | local | | | *(none)* |

`Dir()` is `engines`; `InstallKey()` is `engines/foo`; `TagPrefix()` is
`engines/foo/`. A builtin with an `@version` is rejected ("ships in the
binary"). Connector and runtime resolution is unchanged, asserted by
`TestEngineKindLeavesOtherKindsAlone` — including the two sharpest cases,
`cli` (builtin runtime **and** builtin engine) and `command` (builtin
connector).

---

## 7. Fixtures and docs migrated

**No fixture used a step-level workflow `use:`** — the key was not accepted by
the loader at all before this change (it is not a Step field on `main`; the
stale doc comment on `Step` that mentioned it was the only trace). So nothing
needed rewriting for correctness. What changed is spelling, for docs-travel:

- `config.example.yaml` — step `workflow:` → `call:` (1 live site + 3 comment
  sites); the `run: js` sample re-spelled `use: js` with the engine list in a
  comment; the `type: command` sample re-spelled `use: cli` with the legacy
  and remote forms kept as adjacent comments.
- `examples/packs/review-kit/conductor-pack.yaml` — `workflow: review-flow` →
  `call: review-flow`.
- `docs/wiki/Steps.md` — new "Step forms" section stating the `use:`/`call:`
  move; `use`, `command`, `call` added to the Fields table.
- `docs/wiki/Code-Steps.md` — rewritten around `use: <engine>`; new
  "The `cli` engine" section; the `run:` alias and the `call:` warning called
  out up front; ctx / where-it-runs / timeouts / trust-boundary paragraphs
  updated to include `cli`.
- `docs/wiki/Workflows.md` — step forms list and the "File-based references"
  section moved to `call:`.
- `docs/wiki/Configuration.md`, `docs/wiki/Examples.md`, `docs/wiki/Packs.md`
  — step `workflow:` → `call:`.
- `cmd/conductor/packs.go` — `pack show` prints `call: <instance>/<workflow>`.
- `cmd/conductor/plugins.go` — `plugin list` now lists engines beside
  connectors and runtimes (`cli` appears twice, once per kind, on purpose).
- `test/e2e/config/connectors.e2e.yaml` + `test/e2e/run.sh` — new **K9**
  scenario: two `use: cli` steps on the existing `conn/csvc` comment burst.
  `echoed` (`command: [sh]` + `code: "cat"`) proves ctx-on-stdin by handing
  the document straight back; `argv` (`command: [sh, -c, "printf …"]`) proves
  argv-only execution and stdout→outputs. A `slack.post` asserts
  `K9 first burst comment via cli-engine`.

`docs/wiki/Policy.md`'s `workflow: {}` is the verb-scopes map (a connector
name), not a step key — left alone.

**e2e status:** the full hermetic stack was built and run
(`MODE=stub test/e2e/run.sh`, docker available): **PASS=118 FAIL=0**, exit 0.
The new scenario passed:

```
  ✓ K9 cli engine bridged ctx-on-stdin and stdout outputs
…
====================================================
PASS=118  FAIL=0  (SKIP = genuinely N/A for this stack; see the row note)
```

---

## 8. Decisions

1. **`call:` is a new key; `workflow:` stays valid.** The spec said to rename
   the workflow-call field's yaml tag to `call`. In this codebase that field
   is `Step.Workflow` with tag `workflow:`, used across `config.example.yaml`,
   the pack examples, `internal/config/imports.go`, `pack_refs.go`, and many
   tests — and a step-level `use:` was never a valid key at all (it is absent
   from the Step struct on `main`; only a stale doc comment mentioned it).
   Renaming the tag outright would break every config in the wild, which the
   "keep existing workflow configs working" constraint forbids. So `call:` is
   added as the canonical spelling and folded into the same field, `workflow:`
   keeps parsing, and the migrator does the `use:` → `call:` rewrite the spec
   asked for (unconditional, and correct for the hypothetical old config even
   though the repo contains none).

2. **`command:` became a named type, `config.Argv`.** Required for the
   "string or list" surface. It is assignable to `[]string`, so no call site
   needed changing; two `reflect.DeepEqual` assertions in `stepmerge_test.go`
   did. The string form is **word-split, not shell-evaluated** — `command:
   "rm -rf $HOME"` is three literal words.

3. **`otherKind(UseKindEngine)` is `""`.** The cross-kind hint ("that is a
   builtin runtime, it can never be an engine") would be a confident lie for
   `cli`, which is deliberately both a builtin runtime and a builtin engine.

4. **`run: cli` selects the cli engine.** Specified. It previously meant "look
   for a program named `cli` on PATH", which was never a sensible config.

5. **A bare non-builtin `use:` is an error, not an official-plugin fetch.**
   `ParseUse` still resolves it to `conductor-plugins//engines/<name>` (the
   resolution table above is exactly as specified), but a STEP carrying one is
   rejected at load with a message naming the builtins and pointing at
   `call:`. Plugin-backed engines cannot run until the wire protocol lands next
   increment, and a config that loads and then fails at dispatch is worse than
   one that says so at `conductor validate`.

6. **A `use: cli` step classes as `"command"` for the agent-authored plan
   allowlist**, not `"code"`. It runs an argv, which is the capability an
   operator is deciding about when they write `allow: [command]` — and
   `matchAny` already treated the pattern `cli` as an alias for `command`, so
   classing it `"code"` would have made that pattern mean the opposite of what
   it now reads as. `allow: [code]` therefore does NOT admit a cli step, which
   is the narrower (safer) direction.

7. **Plan validation rejects a `use: cli` plan step with no `command:` and any
   plugin-class engine**, independently of the config loader — no plan path
   admits a step the loader would have refused.

8. **`command:` on a non-cli engine is an error** rather than an ignored key:
   a step carrying both has reached for the wrong engine.
