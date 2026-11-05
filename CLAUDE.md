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

## Examples

| Wrong (in core) | Right |
|---|---|
| Rebasing an agent's commits when a PR branch moved | The git plugin provides the git verbs; the pack decides when to use them |
| Prompt text telling an agent to `git rebase` | Prompts live in packs |
| A config key naming a language (`prefetch: [go]`) | Generic egress; anything language-specific goes in a pack |
| PR or branch concepts in core config | Connector fields and scopes |
