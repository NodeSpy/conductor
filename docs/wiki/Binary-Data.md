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
| `blob.put { path \| text, name?, media_type? }` | store a local file or inline text → `{blob, digest, size}` |
| `blob.get { blob, path }` | write a blob's bytes to a local path (e.g. into an agent's worktree) |
| `blob.read { blob, max_bytes? }` | read a text blob into the scope (default cap 1 MiB) |
| `blob.stat { blob }` | metadata: digest / name / media_type / size |

```yaml
connectors:
  ci: { use: webhook, listen: ":8099", sources: { build_done: { path: /hooks/build } } }

triggers:
  - on: ci.build_done
    steps:
      - { id: art, uses: blob.put, options: { path: "{{.body.artifact_path}}", media_type: "application/gzip" } }
      - { id: fetch, uses: blob.get, options: { blob: "{{.art.blob}}", path: "/srv/agents/wt/input.tar.gz" } }
      - { id: fix, type: agent, name: fixer,
          prompt: "The build artifact is at input.tar.gz in your worktree ({{.art.size}} bytes). …" }
```

Agents consume artifacts through `blob.get` into their worktree (and produce
them via a step that `blob.put`s a file they wrote) — the handle, never the
bytes, moves through prompts and outputs.

## Starting an agent from a directory archive

An agent step with `workspace: { from: "{{.snap.archive}}" }` starts in its
own fresh copy of a directory archive (a tar blob) instead of a checkout (see
[[Steps]]). This is how a panel of parallel agents shares one fetch. One verb
returns the tree as an artifact, and every reviewer starts from it:

```yaml
steps:
  # The PR's head and base as one archive, made by the github plugin with
  # its own credentials; conductor stores it as a run-scoped blob.
  - { id: snap, uses: github.pr_snapshot, options: { repo: "{{.repo}}", pr: "{{.number}}" } }
  - id: lens
    type: agent
    for_each: "{{.lenses}}"
    parallel: true
    workspace: { from: "{{.snap.archive}}" }
    prompt: "…"
```

Conductor doesn't make the tree: a connector verb does, and returns it as a
[binary output](#verbs-with-declared-binary-io). Any verb whose output is a
tar archive of a directory works the same way.

The archive is unpacked as untrusted, whoever made it. Every entry lands
inside the destination (through an `os.Root`, so not even a link planted
earlier in the archive can redirect a write), every symlink must resolve
inside it, and a hard link or device fails the unpack. Each copy is the
agent's own: it runs under the normal sandbox, is removed with the agent,
and is never pushed. Only a blob the run itself holds can be named, and the
step needs a local conductor-launched runtime (cli, acp, …).

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
- **A plugin's BinaryOut** (`plugin.Verb.BinaryOut` on the wire): the verb
  writes the file under the call's staging directory (`invoke.staging`) and
  returns its path. The runner stores that file as a run-scoped blob, named
  after the file, removes it, and the scope receives the handle. The bytes
  never cross the plugin wire, so the message size cap doesn't bound them.
  A path outside the instance's staging directory, or anything but a
  regular file, fails the step and is left alone.

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
