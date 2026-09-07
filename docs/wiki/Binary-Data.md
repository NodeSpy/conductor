# Binary / file data (blobs)

Files — build outputs, PDFs, images, patches, screenshots — pass between
steps and agents as **content-addressed blobs** (#36 §21), not
base64-in-JSON. The daemon owns one artifact store beside its state file
(`blobs/`); identical content is stored once (sha256 addressing), each run
holds references, and **a run's artifacts are GC'd when the run ends**.

## The handle

In the template/step scope a blob is an opaque, JSON-friendly handle:

```json
{ "$blob": "sha256:…", "name": "report.pdf", "media_type": "application/pdf", "size": 48213 }
```

Only metadata rides the scope — templates address it
(`{{.steps.build.outputs.artifact.size}}`), checkpoints persist it, and the
same redaction that scrubs any step output applies to it. The **bytes** never
enter the JSON scope, the checkpoint file, or the audit; redaction/egress
rules govern the metadata, not the content (scan artifacts before `blob.put`
if their bytes may hold secrets).

Pass a handle between steps with a sole-reference template
(`file: "{{.dl.body}}"`) — a sole `{{.x}}` preserves the raw value.

## The `blob` verbs (built-in, always available)

| Verb | Does |
|---|---|
| `blob.put { path \| text, name?, media_type? }` | store a local file (or inline text) → `{blob, digest, size}` |
| `blob.get { blob, path }` | write a blob's bytes to a local path (e.g. into an agent's worktree) |
| `blob.read { blob, max_bytes? }` | read a text blob into the scope (default cap 1 MiB) |
| `blob.stat { blob }` | metadata: digest / name / media_type / size |

```yaml
connectors:
  ci: { type: webhook, listen: ":8099", sources: { build_done: { path: /hooks/build } } }

triggers:
  - on: ci.build_done
    steps:
      - { id: art, uses: blob.put, options: { path: "{{.body.artifact_path}}", media_type: "application/gzip" } }
      - { id: fetch, uses: blob.get, options: { blob: "{{.art.blob}}", path: "/srv/agents/wt/input.tar.gz" } }
      - { id: fix, type: agent, agent: fixer,
          prompt: "The build artifact is at input.tar.gz in your worktree ({{.art.size}} bytes). …" }
```

Agents consume artifacts through `blob.get` into their worktree (and produce
them via a step that `blob.put`s a file they wrote) — the handle, never the
bytes, moves through prompts and outputs.

## Verbs with declared binary IO

A connector verb can declare binary inputs/outputs in its schema
(`VerbDecl.BinaryIn` / `BinaryOut` — see [[Authoring-Connectors]]):

- **BinaryIn** option: a blob handle arriving there is resolved to the
  blob's on-disk path before the verb runs — the implementation streams
  bytes off disk and never sees base64. A plain string passes through (the
  option can accept ordinary local paths too). The staged file is the
  store's canonical copy: **read-only**.
- **BinaryOut** output: the implementation returns raw `[]byte`; the runner
  stores it as a run-scoped blob and the scope receives the handle. An
  optional sibling `<name>_media_type` string output sets the MIME type.

## Lifecycle & GC

- Blobs are **run-scoped**: when the run completes (or fails), its
  references are dropped and any blob no other run references is deleted.
  Don't park handles in KV for later runs — the bytes won't outlive the run.
- Content addressing dedups across concurrent runs; the last reference
  deletes the file.
- A boot sweep removes unreferenced blob files older than 24h (crash
  leftovers, out-of-run puts).

## Policy

`blob.put` writes caller-supplied values into durable state, so it is gated
like `kv.set` for agent-authored plans: the `no_secret_egress` write barrier
refuses to park tracked secret material there, and the resource/verb
allowlists apply as usual. `blob.put`/`blob.get` read/write conductor's own
filesystem — treat them like `cli` when granting them in
`policy.agent_authored.allow`.

Related: [[Verbs]] · [[Workflows]] · [[Secrets]]
