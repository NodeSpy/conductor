# Paseo sessions in conductor's sandbox

Paseo runs your Claude, Codex and pi sessions with its native providers —
rewind, steer, subsessions. `conductor paseo setup` adds a **sandboxed** entry
for each of them to paseo's config, so a session you start with it runs in
conductor's [[Isolation#the-agent-sandbox|agent sandbox]]: its workspace
read-write, its own tool state as `HOME`, the network cut but for its model,
none of your credentials. There is no paseo plugin and no relay: paseo still
talks to the real agent CLI; conductor only wraps the command paseo runs.

## Setup

```sh
conductor paseo setup            # writes the entries, then `paseo reload`
```

It merges, into paseo's `config.json` (`$PASEO_HOME`, else `~/.paseo`;
`--home`/`--file` to point elsewhere):

```json
"claude-sandboxed": {
  "extends": "claude", "label": "Claude (sandboxed)",
  "command": ["/path/to/conductor", "sandbox", "exec", "--tool", "claude", "--", "claude"],
  "x-conductor": true
}
```

and `codex-sandboxed` the same way — and `pi-sandboxed` when paseo reports pi
available. The command each entry wraps is your own for that provider when
your config names one (a wrapper script that routes the model), else the
provider's name.

- **Idempotent.** Re-running updates conductor's entries in place and reports
  `added / updated / unchanged / skipped`. Nothing else in the file changes
  value or position.
- **Never yours.** Only entries marked `"x-conductor": true` are conductor's.
  An entry of the same name without the marker is skipped, never overwritten.
- `--config PATH` / `--socket PATH`: the conductor daemon the entries ask, when
  it is not the default. `--dry-run` writes nothing; `--no-reload` writes but
  does not reload.

Then pick **Claude (sandboxed)** (or `--provider claude-sandboxed`) for a
session. conductor's own paseo dispatches use the sandboxed entries
automatically when the sandbox is on.

## How a session runs

Paseo runs the entry's command — `conductor sandbox exec -- claude …` — with
the session's arguments appended. `conductor sandbox exec`:

1. takes the session's directory (`PASEO_AGENT_CWD`, which paseo sets for
   every provider launch; else its own cwd);
2. asks the running conductor daemon, over its control socket, for that
   directory's **identity and policy** and a **fresh token** for this launch;
3. runs the agent under the sandbox with a clean environment, its own egress
   proxy and model relay, for as long as the agent runs.

**Resume** is paseo running the command again: the same directory resolves to
the same identity, so the session finds its tool state (its transcript) — a
new token, the same memory. This holds across a paseo restart.

**Identity.** A directory conductor created (a dispatch's worktree, a
member's workspace) is registered with the daemon and resolves to its
dispatch or member — its `conductor call step.done` and `git.push` work. Any
other directory is its own identity, sandboxed with a default policy scoped
to it: the directory read-write, the model endpoints only, a token that grants
no verb. Your home directory, `/`, and conductor's own state and config are
refused as a workspace. (A launch paseo makes with no session — listing a
provider's models from its own cwd — gets the tool's empty scratch workspace
instead.)

**Fail-closed.** If the conductor daemon is unreachable, or has no policy for
the directory, `conductor sandbox exec` refuses to launch (exit 125) and paseo
reports the session's error. An agent is never launched unsandboxed.

**Paseo's tools.** Paseo injects its MCP server on its daemon's loopback port;
the sandbox has no route to it, so those tools fail closed (the agent sees a
connection error and no `mcp__paseo__*` tools).

## What paseo needs from conductor, and what it doesn't

The only paseo surface conductor depends on: provider entries with `extends`
+ `command` (paseo 0.10+), `paseo reload`, `paseo provider ls --json`, and
`PASEO_AGENT_CWD` in a provider's launch environment. A scheduled CI job runs
this path against the latest published paseo (`test/sandbox-canary/paseo.sh`).

## Troubleshooting

| You see | Because |
|---|---|
| `refusing to launch: the conductor daemon is unreachable` | conductor is not running, or the entry names another daemon (`--config`) |
| `refusing … it contains your home directory` | the session was started in your home; start it in a project directory |
| `egress to X denied` in the session's stderr | X is not a model endpoint or in `sandbox.egress` |
| `git push` fails inside the session | by design: commit, and conductor pushes (a dispatch), or push from outside the sandbox (your own session) |
