# Conductor: what belongs in core

Conductor is a conductor. It sends work to tools and agents and helps them work together.
It does not know how any particular tool or workflow works.

Core handles:

- events in, steps out
- dispatching work to runtimes
- passing data and artifacts between steps
- state, recovery and restarts
- sandboxing and trust boundaries
- the plugin and pack contracts

Everything else goes in a **plugin** or a **pack**:

- **Plugins** hold knowledge of a tool or service: git, GitHub, Slack, a language toolchain.
  This covers its commands, its failure modes and its quirks.
- **Packs** hold knowledge of a workflow: reviewing a PR, fixing CI, what to do when a branch
  moves mid-job, which prompts to send.

## Before adding code to core

Ask: does this code know about a specific tool, service, language, repo or workflow?
If yes, it belongs in a plugin or a pack.

If a pack or plugin can't express it today, add the smallest generic capability to core
that would let it. Examples are a step output, a hook, an artifact or a contract field.
Don't add the specific behaviour itself.

Some tool-specific code already lives in core, such as the end-of-step git push. Don't use it
as a precedent. Don't grow it. Move it out when touching it is practical.

## Plugins and packs own their own state

- A plugin works through the plugin contract and nothing else. Conductor learns a plugin's verbs
  and events from the plugin at runtime. Conductor never keeps a copy of a real plugin's
  declaration, not even as test data. Core tests use small synthetic declarations with neutral
  names.
- Adding or changing a verb in a plugin must never require a conductor change. If it does, the
  coupling is the bug.
- A plugin does one call's work and returns. It doesn't keep local files, checkouts or caches
  between calls, and it doesn't expose "discard" verbs to clean up after itself. Anything
  temporary is created and removed inside the call.
- Data that outlives a step is an artifact (a blob) owned by the run. The pack decides what to
  store and who gets it; conductor stores it and cleans it up.
- Use the plugin that's already there. If GitHub work is needed, it goes in the github plugin,
  with its existing auth. Don't add a second plugin or new credentials for it.

## Large temporary data never goes in /tmp

`/tmp` and `os.TempDir()` are often a RAM-backed tmpfs, where every byte written is memory.
Anything large or of unpredictable size goes in the scratch dir (`scratch.dir`, default
`<state dir>/scratch`). That covers clones, checkouts, archives, staging, uploads, builds,
browser profiles and unpacked artifacts. In core, use `scratch.Dir()` / `scratch.MkdirTemp()`.
A plugin writes inside the call's staging dir, never `os.TempDir()`. The guard test in
`internal/scratch` fails on new system-temp uses. Only tiny, bounded files may be allowlisted
there, and each one needs a reason.

## Examples

| Wrong (in core) | Right |
|---|---|
| Rebasing an agent's commits when a PR branch moved | The git plugin provides the git verbs; the pack decides when to use them |
| Prompt text telling an agent to `git rebase` | Prompts live in packs |
| A config key naming a language (`prefetch: [go]`) | Generic egress; anything language-specific goes in a pack |
| PR or branch concepts in core config | Connector fields and scopes |
| Conductor test data copying the github plugin's verb list | Synthetic test declarations in core |
| A plugin verb that clones into a temp dir, plus a `discard` verb | One verb that returns the content as an artifact |
| Retry logic for GitHub fetches in core's git code | Retry inside the plugin that does the fetch |
