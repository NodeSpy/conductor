# Authoring a connector

Conductor deliberately does **not** chase a huge prebuilt catalog (#36 §22):
the generic [`rest`/`graphql` connectors](Connectors) cover anything not yet
typed, and new typed connectors land in-tree as needed. The measure of
success is *adding a connector is cheap and safe* — this page is the whole
authoring path.

**Start from the executable template:**
`internal/connector/authoring_example_test.go` is a complete, documented,
tested miniature connector (`type: authoring-example`). Copy it into
`internal/connector/<yourtype>.go`, rename, grow. Because the template is a
test, it compiles and runs on every change to the contract — it cannot rot.

## The contract

A connector type is three things in one file:

```go
// 1. The self-description everything reads: validate, schema, the flow runner.
var myDecl = &connector.TypeDecl{
    Type: "mytype",
    Desc: "one line for `conductor connectors ls`",
    Connection: connector.Schema{ /* documented connection keys */ },
    Events: []connector.EventDecl{{
        Name:    "thing_happened",
        Filters: connector.Schema{...},  // match keys legal in this event's `filter:`
        Context: connector.Schema{...},  // facts published into templates
        Options: connector.Schema{...},  // source-side per-trigger options
    }},
    // Called ONE match key at a time: the `filter:` grammar owns AND/OR/NOT
    // (and the `not_` prefix), you answer "does this key hold".
    Filter: func(event string, filters, trigCtx map[string]any) (bool, error) { ... },
    Verbs: []connector.VerbDecl{{
        Name:    "do_thing",
        Options: connector.Schema{...},  // call options (Required, Enum, Desc)
        Outputs: connector.Schema{...},  // what later steps can reference
    }},
}

// 2. The per-instance implementation.
type myImpl struct{ ... }
func (m *myImpl) Validate() error
func (m *myImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error)
func (m *myImpl) Source(triggers []connector.CompiledTrigger) (core.Integration, error)
func (m *myImpl) DeclaredEvents() []string

// 3. Registration — one line, at init.
func init() { connector.RegisterType(myDecl, newMyImpl) }
```

### The declaration is load-bearing

`conductor validate` checks every `on:` kind, `filter:` key, `uses:` verb,
option name/type, and `{{…}}` reference against the schemas;
`conductor schema <conn>` prints them; the flow runner stubs dry-run outputs
from `Outputs`. An option you don't declare is a **load error for the user**;
one you declare but ignore is a bug report. Special declaration flags:

- `EventDecl.Dynamic` — event names come from connection config (cron
  schedules, rss feeds); implement `DeclaredEvents()` to list them.
- `VerbDecl.Ask` — a request-response verb that presents to a human and
  blocks for the answer (see [[Hand-offs]]).
- `VerbDecl.Open` — user-defined option keys (the rest/graphql pattern); for
  a bundled Go connector this is set directly in the `Decl` your `Impl`
  returns. A plugin (spawned, or an in-process contract builtin) sets the
  wire-level `Verb.Open` field instead — see "Per-instance declarations"
  below.
- `VerbDecl.BinaryIn` / `BinaryOut` — binary IO (#36 §21): declared inputs
  arrive as the blob's on-disk path; declared outputs return raw `[]byte`
  and leave as opaque handles. See [[Binary-Data]].

### The builder

```go
func newMyImpl(name string, ref config.ConnectorRef, deps connector.Deps) (connector.Impl, error)
```

- Parse the connection once: `ref.Decode(&myConn)` fills your YAML struct
  (the shared header keys — `type`, `enabled`, `options`, `policy` — are
  already handled).
- Resolve credentials through `deps.Secrets` (env `${…}` and secret-scheme
  URIs) so values are **tracked for redaction** everywhere they could leak.
- **Failure posture**: return an error for runtime conditions (unresolvable
  secret, unreachable socket) — `connector.Build` then *disables* the
  instance with that reason and the daemon keeps booting. A bad connector
  must never crash-loop the box. Structural config nonsense belongs in
  `Validate()` (reported at load) instead.
- `deps` also carries the loaded `Config`, a logger, the blob store, and
  vault plumbing — take what you need, ignore the rest.

### Sources

`Source(triggers)` lowers the triggers referencing this connector into the
integration that provides event transport (a listener, a poll loop). A
verb-only connector returns `(nil, nil)`. Emitted events must publish
exactly the `Context` schema — the validator holds `{{…}}` references in
user configs to it.

A source that polls on its own schedule and carries managed OAuth2 auth
(`auth: {type: oauth2}`) needs the LIVE token, not the connection snapshot it
was started with — `plugin.start_source` fires once, and a token minted or
rotated afterward is otherwise invisible to it for as long as the stream
runs. Ask the host directly instead of reading the connection's own
`access_token` field: implement `pkg/plugin.HostAware`'s `SetHost(*plugin.
HostConn)` (Serve calls it once, before serving anything) and call
`host.Auth(instance).Token(ctx, refresh)` — once per poll is fine, the host
caches; `refresh: true` after your own upstream call comes back 401, the
same retry-once-on-401 a verb invoke gets automatically
(docs/design/plugin-contract.md §1.9, `host.auth`). It errors for an
instance with no managed auth — fall back to the connection's own static
`auth:` unchanged. See `internal/builtins/rest`'s poller for the reference
implementation.

A plugin with no stderr of its own (every in-process builtin) can still log
through the daemon's own logger: `host.Log(instance, message)` — best-effort,
scoped like `host.auth` (docs/design/plugin-contract.md §1.9, `host.log`).
The host bounds it the same way it bounds `host.state`/`host.auth`: a message
is capped at 2 KiB and every control character is escaped (a plugin can't
start a new log line, forge a daemon-looking one, or move the terminal
cursor), and each instance gets a budget of 60 lines per rolling minute —
past that, lines are dropped and the drop count is logged once the window
turns over. Rate-limit your own calls per (instance, event) besides (see
`internal/builtins/rest`'s poller) — this budget is a backstop, not a
substitute for not flooding it in the first place.

### Per-instance declarations (Q6: `plugin.describe {instance, config}`)

A type whose events/verbs come from user config — rest and graphql declare
their own verbs this way (`internal/builtins/rest`, `internal/builtins/graphql`),
and webhook materializes one concrete event per configured source — does
**not** implement a connector-side Go interface for this. It is a contract
mechanism (docs/design/plugin-contract.md §1.4, §3.9 G13), the same for a
spawned plugin and an in-process builtin: the handler implements
`pkg/plugin.InstanceDescriber`,

```go
func (h *myHandler) DescribeInstance(ctx context.Context, instance string, config map[string]any) (plugin.Decl, error)
```

and the host calls `plugin.describe {instance, config}` once per configured
instance, in addition to the once-per-process `plugin.describe {host}` call.
A handler that does not implement `InstanceDescriber` answers
`CodeMethodNotFound` for that call automatically (the method dispatch does
it), and the type-level `Describe()` is every instance's declaration. The
returned `Decl` is checked exactly like the type-level one (must-understand
semantics, internal consistency) before it becomes the instance's effective
declaration everywhere — validation, introspection, verb dispatch.

The returned `Decl` must also be a **refinement** of the type-level one, not
a replacement (plugin-contract.md §1.4; `internal/connector/instance_refine.go`
`validateInstanceRefinement`) — a per-instance declaration that fails this is
disabled with the reason, the same as one that fails the must-understand
checks:

- a verb present in both declarations keeps identical `semantics`
  (`host_only`, `mints_credential`, `exposes`, …, byte for byte), keeps
  `Open` unchanged, and never drops or changes an option's `scope`;
- a verb's declared `outputs` may only get stricter (same type, at least as
  `required`) — never dropped or loosened;
- a brand-new verb the type decl never named is fine, as long as it carries
  no `semantics` of its own (rest/graphql's user-declared, plain verbs);
- connection-level semantics (`credentials`, `listeners`, `poll`, `scope`,
  `preflight`, `translate`) and the `capabilities` manifest must match the
  type-level declaration exactly — these are fixed at install, not per
  instance;
- a brand-new event (one the type decl never declared) is exempt — this is
  Q6's whole point. A **same-named** event may not add or change
  `conversation_reply` or `closes_target`, and its target's scope dimensions
  must be identical (not wider, not narrower) to the type-level
  declaration's same-named event — those three are the engine-honored
  escalation paths otherwise unchecked.

There is no equivalent for a bundled Go connector registered with
`connector.RegisterType` (the pattern this page otherwise documents): that
registration path has one static `TypeDecl` per type, with no per-instance
hook. A connector whose contract genuinely varies per instance belongs on
the plugin contract (even as an in-process builtin via
`connector.RegisterInProcessConnector`), not as a bundled type.

## Heavy dependencies: the build-tag pattern

The base binary stays lean and zero-cgo. A backend that would drag in a
heavy dependency ships behind a build tag, with a stub that registers a
clear error otherwise:

```go
// mytype_backend.go
//go:build mytype

package connector
func init() { RegisterType(myDecl, newMyImpl) }   // the real thing

// mytype_stub.go
//go:build !mytype

package connector
func init() {
    RegisterType(myDecl, func(name string, _ config.ConnectorRef, _ Deps) (Impl, error) {
        return nil, fmt.Errorf("connector type %q requires a build with -tags mytype", myDecl.Type)
    })
}
```

Both files carry the same declaration, so `validate`/`schema` work in every
build; only Invoke-time construction differs (and it *disables* the
connector with the reason, honestly, instead of failing the boot). Everything
must still build with `CGO_ENABLED=0` — prefer pure-Go drivers (the existing
SQL stores are the precedent).

## Conventions checklist

- One file per type in `internal/connector/`; `<type>_test.go` beside it.
- Tests are table-driven and hermetic: build through the real registry
  (`Build` over a YAML snippet), fake the service with `httptest` or an
  injectable seam, never touch the network. Cover: each verb's happy path +
  option validation, filter evaluation, the disabled-on-bad-creds path.
- Redact anything derived from credentials in error text.
- Respect `ctx` in Invoke (timeouts/cancellation) — the runner bounds steps.
- Rate limiting, option-default merging, enable/disable, and policy all
  happen in the shared `Instance` wrapper — don't reimplement them.
- Reserved names (`kv`, `sql`, `memory`, `workflow`, `conductor`, `blob`)
  are enforced by config validation; pick another type name.
- Ship docs with the connector: a `docs/connectors/<name>.md` page in the
  plugin repo (Setup → Connection → Verbs), and, when config surface is added, a
  `config.example.yaml` block — same commit.

Related: [[Connectors]] · [[Verbs]] · [[Binary-Data]] · [[Configuration]] · [[Plugins]]
