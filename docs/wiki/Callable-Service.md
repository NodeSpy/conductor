# Callable service (invoke API)

Conductor as a callable service: an external orchestrator fires a named
workflow over HTTP and gets a structured result back. It is the inbound
counterpart to `conductor run` — the same manual machinery, reachable from
n8n, cron, a queue, plain `curl`, or (via the MCP face) any MCP client.

The division of labour is deliberate: **the caller owns generic automation and
scheduling; conductor owns agents-on-code.** The surface is vendor-neutral —
n8n is only the first documented adapter, and nothing here is n8n-specific.

Off unless a `callable:` block is configured.

## It is a control surface, gated like one

The invoke endpoint dispatches agents, so it is gated like every other control
surface in conductor. **All three gates must pass** — none alone is enough:

1. **Authenticated** — every request presents a credential that resolves to a
   configured token: a bearer secret *or* an HMAC signature over the request
   body. An unknown/absent credential is refused with a uniform `401` that
   reveals nothing about which tokens exist.
2. **Deny-by-default scope** — a token invokes only the workflows named in its
   `workflows:` allow-list. There is no wildcard.
3. **Explicit opt-in** — a trigger is reachable only if it declares
   `callable: true`. A workflow a token lists but the trigger has not opted
   into is *not* invocable.

A denied invoke — bad auth, out of scope, or not callable — never reaches the
dispatch machinery, and an out-of-scope caller gets a uniform `403` that
does not disclose whether the workflow exists.

Everything downstream is unchanged: the invoke runs through the same
policy / quiet-hours / budget / concurrency / audit path as any manual run.
The entry point changes; the containment does not.

## Configuration

```yaml
callable:
  # Bind for the invoke endpoints. Mounts on the shared inbound listener, so it
  # may reuse a `listen:` a webhook/sentry connector already binds. Put it
  # behind TLS / an internal network — the bearer token is a shared secret.
  listen: ":8099"
  # Bounds a synchronous ?wait=true call (default 30s, hard-capped at 5m).
  wait_timeout: 30s
  tokens:
    # A bearer caller.
    - name: n8n-prod                       # recorded as the caller identity in the audit
      bearer: "${CONDUCTOR_INVOKE_TOKEN}"  # from env/secrets — never inline
      workflows: [pr-summary]              # deny-by-default: only these
    # An HMAC caller: signs the raw request body (reuses the webhook-signature
    # machinery). Mutually exclusive with bearer.
    - name: ci-signer
      hmac:
        secret: "${CONDUCTOR_INVOKE_HMAC}"
        header: X-Conductor-Signature      # header carrying the signature
        scheme: hex                         # hex (default) | base64; a "sha256=" prefix is stripped
      workflows: [pr-summary]

triggers:
  # A callable manual workflow. `callable: true` is the trigger-side opt-in;
  # without it the workflow is invisible to the invoke surface even if a token
  # lists it. Inputs arrive under `.inputs` and at the top level.
  - on: manual
    name: pr-summary
    callable: true
    steps:
      - id: summarize
        type: agent
        agent: fixer
        prompt: "Summarize {{.repo}}#{{.pr}} for the release notes."
```

Validation (`conductor validate`) enforces the invariants up front: a
`callable: true` trigger must be a named `on: manual` trigger; a token is
authenticated exactly one way (an empty credential is never accepted); every
workflow a token scopes must exist and have opted in; `listen` and `tokens`
are set together (either alone is a misconfiguration).

## Invoking

`POST /invoke/<name>` with a JSON body `{"input": { … }}`. The `input` object
becomes the trigger context (available under `.inputs` and at the top level in
templates). The default is asynchronous — the call returns a `run_id`
immediately:

```
$ curl -s -X POST localhost:8099/invoke/pr-summary \
    -H 'Authorization: Bearer '"$CONDUCTOR_INVOKE_TOKEN" \
    -d '{"input":{"repo":"acme/api","pr":42}}'
{"run_id":"r5x9…","status":"accepted"}
```

There are three delivery modes; the caller picks per request.

### Async (default)

Returns `202 {"run_id","status":"accepted"}` the moment the run is admitted.
The caller polls `GET /runs/<id>` for the result, or supplies a callback (below).

### Synchronous — `?wait=true`

Blocks (bounded by `wait_timeout`, hard-capped at 5m) and returns the structured
result inline. `200` once the run reaches a terminal status; `202` (still the
same body shape, `status:"running"`) if the deadline arrives first, so the
caller falls back to polling:

```
$ curl -s -X POST 'localhost:8099/invoke/pr-summary?wait=true' \
    -H 'Authorization: Bearer '"$CONDUCTOR_INVOKE_TOKEN" \
    -d '{"input":{"repo":"acme/api","pr":42}}'
{"run_id":"r5x9…","status":"ok","outputs":{"summarize":{"text":"…"}}}
```

### Callback — `callback_url`

Returns `202` immediately (like async), then POSTs the same structured result to
the given URL when the run finishes. Delivery is best-effort and audited
(`event: callable_callback`, `delivered: true|false`):

```
$ curl -s -X POST localhost:8099/invoke/pr-summary \
    -H 'Authorization: Bearer '"$CONDUCTOR_INVOKE_TOKEN" \
    -d '{"input":{"repo":"acme/api","pr":42},"callback_url":"https://n8n.internal/webhook/pr-done"}'
{"run_id":"r5x9…","status":"accepted"}
```

## Reading a run — `GET /runs/<id>`

```
$ curl -s localhost:8099/runs/r5x9… -H 'Authorization: Bearer '"$CONDUCTOR_INVOKE_TOKEN"
{"run_id":"r5x9…","status":"ok","outputs":{"summarize":{"text":"…"}}}
```

The result body is uniform across all three modes and `GET /runs`:

| field         | meaning                                                              |
| ------------- | ------------------------------------------------------------------- |
| `run_id`      | the id minted at invoke time                                        |
| `status`      | `running` \| `ok` \| `failed` \| `retried`                          |
| `outputs`     | per-step outputs keyed by step id (secret-scrubbed, as in §20)      |
| `error`       | present only when `status: failed` — a generic `"workflow failed"`  |
| `failed_step` | present only when `status: failed` — the step id that failed        |

The `error` field is deliberately generic: a raw connector/step error can carry
local paths or hostnames (non-secret, but internal), so the external caller sees
only `"workflow failed"` plus the operator-named `failed_step`. The full failure
detail stays in the §20 history record — read it with `conductor runs <id>`.

Reads are isolated per token: a token may read only runs it invoked. An
unknown id it did not issue returns `404`; one issued by *another* token
returns `403`. (After a daemon restart the in-memory issue map is empty, so a
pre-restart run is readable by any authenticated token — the §20 record itself
is the durable audit.)

### HMAC callers

An HMAC token signs the **raw request body** with HMAC-SHA256 and presents the
signature in its configured header:

```
body='{"input":{"repo":"acme/api","pr":42}}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$CONDUCTOR_INVOKE_HMAC" -hex | awk '{print $2}')
curl -s -X POST localhost:8099/invoke/pr-summary \
  -H "X-Conductor-Signature: sha256=$sig" -d "$body"
```

## Recipe: calling conductor from n8n

n8n is the first documented adapter, but nothing here is n8n-specific — the
surface is plain authenticated HTTP. Conductor owns the agents-on-code step;
n8n owns the trigger, the fan-in, and whatever happens with the result.

**Credential.** Create an n8n *Header Auth* credential once — name
`Authorization`, value `Bearer <the token>` (store the token in n8n's
credential vault, not in the node). Every HTTP Request node below references it.

**Pattern A — fire-and-forget (async).** An **HTTP Request** node:
`POST http://conductor.internal:8099/invoke/pr-summary`, JSON body
`{"input": {"repo": "{{ $json.repo }}", "pr": {{ $json.pr }}}}`. It returns a
`run_id` in milliseconds; the n8n workflow moves on. Use when conductor's result
isn't needed inline.

**Pattern B — wait for the result (synchronous).** The same node, but URL
`…/invoke/pr-summary?wait=true` and the node's timeout raised past
`wait_timeout`. The response body *is* the structured result — read
`{{ $json.outputs.summarize.text }}` in the next node. Add an **IF** node on
`{{ $json.status }} === "ok"` to branch failures. Best for short workflows where
n8n should block.

**Pattern C — callback (long runs).** POST without `wait`, but include
`"callback_url": "{{ $execution.resumeUrl }}"` from a **Wait** node set to
"On Webhook Call". n8n pauses; conductor POSTs the result to the resume URL when
the run finishes; n8n continues with the result as the node output. Best for
runs longer than any sane HTTP timeout.

In every pattern the request is one authenticated HTTP call and the response is
the uniform result body above — swap n8n for Zapier, Make, a cron job, or a
shell script without changing the conductor side.

## MCP tool face

The same callable workflows are exposed as MCP tools for a local MCP client
(an agent, an IDE, a desktop assistant) via a stdio server:

```
conductor mcp callable
```

The client sees one tool per `callable: true` workflow and nothing else — the
tool list is exactly the opted-in triggers, so it is scoped callable-only by
construction. A tool takes a single free-form `input` object (the same body the
HTTP endpoint accepts); calling it fires the workflow and blocks for the
structured result, returned as the tool's text content (`isError: true` when the
run failed).

Unlike the HTTP face, this one carries no bearer/HMAC token: it dispatches over
the daemon's same-user control socket — the same privilege boundary
`conductor run` uses — so it is a local face, launched by the MCP client's own
config. A typical client entry:

```json
{
  "mcpServers": {
    "conductor": { "command": "conductor", "args": ["mcp", "callable", "--config", "/etc/conductor/config.yaml"] }
  }
}
```

## Audit

Every invoke is audited with the caller identity, the workflow, and the run id
(`event: callable_invoke`). A refused invoke is logged with the reason. The
run itself then leaves the usual §20 history record, readable with
`conductor runs <run_id>`.

## See also

- [[Runs]] — the execution history the invoke surface hands back.
- [[Policy]] — quiet-hours / budget / concurrency, which apply to every invoke.
- [[Configuration]] — the full `callable:` schema.
