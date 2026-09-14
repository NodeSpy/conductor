# Model discovery — where the available-model list comes from

Fleets and wildcards need to know what your box can actually run. That list is
the **roster**, and conductor discovers it **per runtime**.

Discovery is deliberately not one mechanism. Every runtime knows something
different about itself: paseo owns a provider registry, agent-deck drives other
people's CLIs, a bare CLI has a vendor API behind it. So discovery is an
adapter capability — each runtime answers however it can, and a runtime that
cannot answer still works.

**Nothing here is required for conductor to run.** Every path degrades to a
[bare launch](Model-Selection.md#bare-launch).

## Per-runtime strategies

| Runtime | How it enumerates |
| --- | --- |
| `paseo` | **Native.** `paseo provider ls --json` + `paseo provider models <p> --json`. paseo is authoritative for paseo; conductor asks it and does nothing else. Providers paseo reports as unavailable are skipped. |
| `agent-deck` | Reads which **tools** the installation is configured for (`default_tool`, each `[<tool>]` section, each `[profiles.*.<tool>]` account slot) and unions those providers from the public catalog. |
| `cli` / `acp` | The **vendor's own API first**, with the tool's stored credentials — then the catalog. |
| anything else | No adapter → no roster → bare launch. |

Discovery is **never funnelled through paseo**. A `cli` runtime driving claude
does not ask paseo what claude can run; it asks Anthropic, then the catalog.

## The live-API path

Where a tool's stored credentials authorise it, conductor asks the vendor what
**this account** may run. That is account-scoped truth rather than a list of
everything that exists in the world.

| Tool | Live listing | Why |
| --- | --- | --- |
| `claude` | **Yes** | `GET api.anthropic.com/v1/models` with the OAuth token from `~/.claude/.credentials.json` returns the live list. |
| `codex` | No | `~/.codex/auth.json` is `auth_mode: chatgpt`; `/v1/models` returns `403 missing scopes: api.model.read`. |
| `gemini` | No | Google's models API needs an API key or registered identity; the CLI's OAuth token does not authorise it. |

codex and gemini go straight to the catalog rather than burning a request on a
call known to fail. If either vendor ships a listable scope, it gets a prober
and nothing else changes.

A successful live probe is used as an **entitlement filter**: the live list
decides membership and order, and the catalog supplies the metadata the live
response omits (context window, pricing). A live probe that fails — no
credentials, an expired token, no network — falls through to the catalog. It is
an optional filter, never a gate.

### Credential handling

Credential files are read **only** to mint an `Authorization` header for one
request. The token is never logged, never echoed, never written into the cache,
and never placed in an error string; diagnostics carry a redacted fingerprint
(`sk-ant…(redacted)`) so you can tell two credentials apart without seeing
either. There are tests asserting this.

## The catalog: models.dev

[`https://models.dev/api.json`](https://models.dev/api.json) is public, needs no
auth, and carries both current model ids and the metadata neither a vendor SDK
nor a live `/v1/models` response provides — context window, pricing,
modalities. It is the catalog for every adapter that cannot enumerate natively.

It is ~4MB, so it is never fetched on a hot path. It is cached under the state
dir (`~/.local/state/conductor/models/models.dev.json`) with a 24h TTL, and
every failure degrades one step at a time:

```
memory → fresh disk cache → network → STALE disk cache → cannot enumerate
```

The last step is a bare launch, not a crash. A box with no network keeps
dispatching agents; it just cannot filter by model.

A corrupt cache is refetched. A cache that cannot be written is not an error —
discovery still worked this time. A load failure is remembered for the process,
so an offline box does not re-attempt a 4MB fetch on every dispatch.

## Defaults are not discovered

No source marks a cross-provider default — the live `/v1/models` response
carries no default flag — so conductor does not try to guess one. A default
resolves as:

1. an explicit `models.default:` (or a resolved `prefer:` hit) → pass that
   model;
2. nothing declared or resolved → **bare launch**.

## Adding discovery to a new runtime

Implement one method:

```go
type Lister interface {
    List(ctx context.Context) (Roster, error)
}
```

Register it against the runtime's implementation name. Return
`models.ErrNoDiscovery` when you cannot enumerate — that is not a failure, it
means bare launch. Roster order is load-bearing: a wildcard expands in roster
order, so return newest/best first.
