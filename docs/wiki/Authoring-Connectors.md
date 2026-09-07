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
        Filters: connector.Schema{...},  // legal `filters:` keys for this event
        Context: connector.Schema{...},  // facts published into templates
        Options: connector.Schema{...},  // source-side per-trigger options
    }},
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

`conductor validate` checks every `on:` kind, `filters:` key, `uses:` verb,
option name/type, and `{{…}}` reference against the schemas;
`conductor schema <conn>` prints them; the flow runner stubs dry-run outputs
from `Outputs`. An option you don't declare is a **load error for the user**;
one you declare but ignore is a bug report. Special declaration flags:

- `EventDecl.Dynamic` — event names come from connection config (cron
  schedules, rss feeds); implement `DeclaredEvents()` to list them.
- `VerbDecl.Ask` — a request-response verb that presents to a human and
  blocks for the answer (see [[Hand-offs]]).
- `VerbDecl.Open` — user-defined option keys (the rest/graphql pattern);
  pair with `InstanceDecler` (below).
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
integration that provides event transport (webhook route, poll loop). A
verb-only connector returns `(nil, nil)`. Emitted events must publish
exactly the `Context` schema — the validator holds `{{…}}` references in
user configs to it.

### Per-instance declarations (`InstanceDecler`)

A type whose events/verbs come from user config (rest/graphql declare their
own verbs) implements

```go
func (m *myImpl) InstanceDecl(base *connector.TypeDecl) *connector.TypeDecl
```

to materialize the instance's actual contract; validation, introspection,
and dispatch then see it instead of the static declaration.

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
- Ship docs with the connector: a `Integration-<Type>` (connector) wiki page
  and, when config surface is added, a `config.example.yaml` block — same
  commit.

Related: [[Connectors]] · [[Verbs]] · [[Binary-Data]] · [[Configuration]]
