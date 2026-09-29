# Host commands

Inside the [agent workspace jail](Isolation#the-agent-workspace-jail) an agent
cannot read your credentials — `~/.ssh`, `~/.aws`, the gh login are simply not
there. The commands that need them still work: `gh`, `aws`, `kubectl`,
`docker`, … are **host commands**. The agent types them as usual; conductor
decides, runs the **real** binary on your machine with your own setup, and
streams the output back. Every one is recorded.

```
[review acme/app#42 lens:security]  host_command → exit 0   gh pr diff 42
[fix acme/app#43]                   sign → ok               git commit "fix: …"
[fix acme/app#43]                   git_push → ok           git push origin refs/heads/fix/43 a1b2c3d..3f2a9e1
[fix acme/app#43]                   host_command ✗ refused  kubectl delete deploy api — denied by kubectl.deny "delete *"
[fix acme/app#43]                   host_command ✗ refused  gh auth logout — denied (built-in: gh auth *)
[fix acme/app#43]                   host_command → exit 0   aws s3 ls (1 write(s) discarded: .aws/cli/cache/…)
```

That is `conductor watch`. The audit trail has the same rows.

## How it works

- Inside the jail, each host-set binary is a **shim**: conductor's own binary,
  first on `PATH` and (Linux) bound over the tool's real path, so `gh` and
  `/usr/bin/gh` both reach conductor. On macOS the real binary is exec-denied
  instead: running it by absolute path is refused.
- The shim sends `{argv, cwd, env (presentation only), stdin}` over the
  dispatch's own socket. stdin is forwarded only when an argument asks for it
  (`-F -`, `--input -`, `-f -`), and the host process gets no terminal — an
  interactive login flow cannot complete.
- conductor applies the guardrails, your rules, and target binding, then runs
  the real binary (resolved on the host, never from the agent) as you, with a
  **copy-on-write home**: the tool reads your real config and credentials,
  and every write it makes under `$HOME` lands in a throwaway layer that is
  discarded when it exits — reported in the event. Only a profile's persist
  paths (rotating refresh tokens, e.g. `~/.aws/sso/cache`) are written back.
- A profiled tool sees only **its own** slice of your home: `gh` sees
  `~/.config/gh`, `aws` sees `~/.aws`, `kubectl` sees `~/.kube` and the cloud
  configs its exec plugins read, `ssh` sees `~/.ssh`. So a host command handed
  agent-written input (a terraform `external` data source, a script) still
  cannot reach `~/.ssh` or another tool's credentials. A binary without a
  profile sees the whole (copy-on-write) home.
- conductor's own state and config dirs are hidden from every host command.
  A **path guard** refuses arguments that point into your home outside the
  workspace (`--body-file ~/.ssh/id_rsa`), at another process
  (`/proc/<pid>/environ`), or at your session's sockets (`/run/user/<uid>`):
  a host command can read what the jail hides, so it must not be handed a
  path to it.
- Linux builds the copy-on-write home with overlayfs in a fresh user + mount
  + pid namespace; macOS with APFS clones and a Seatbelt profile that denies
  writes to the real home.
- The environment is minimal: `PATH`, `HOME`, locale, plus the variables that
  are your machine's setup for that tool (`AWS_*` for aws, `KUBECONFIG` for
  kubectl, …) — never conductor's own secrets. Nothing is injected: `gh` uses
  your gh login.

## Which binaries

The **host set** of a dispatch:

- **Built in**, each only if installed: `gh`, `aws`, `kubectl`, `docker`,
  `terraform`, `gcloud`, `az`, `ssh`, `scp`, `npm`, `pnpm` (npm/pnpm only for
  their publish/auth verbs; `npm install`, `npm test`, … run natively in the
  jail). `git` is not a shim: its network and signing side reach conductor
  through the remote helper and the signing shim (see [git](#git)).
- **Plus** any binary you name in config.
- **Minus** any set to `false` — still shimmed, so the real one stays
  unreachable.

Everything else runs natively in the jail, confined to the workspace
(`go test`, `make`, `rg`, local `git`, …).

## Configuration

Optional, per binary; it can only restrict (or fix up):

```yaml
isolation:
  host:
    git: {}                                  # nothing to say — guardrails + your identity/signing
    gh:
      deny: ["pr merge *", "repo delete *"]
    aws:
      allow: ["s3 ls *", "s3 cp * s3://build-artifacts/*"]   # an allow list ⇒ ONLY these
      persist: ["~/.aws/sso/cache"]          # (the built-in profile already persists this one)
      network: { egress: [s3.amazonaws.com, sts.amazonaws.com] }
    kubectl:
      deny: ["delete *", "rollout undo *"]
      env:  { KUBECONFIG: ~/.kube/staging }  # fix-up for when the machine's default isn't right for agents
    terraform:
      allow: ["plan *", "show *"]
    mytool:                                  # any binary, by name
      allow: ["status *"]
    docker: false                            # keep it out of reach entirely
```

- `deny` refuses matching actions; `allow`, if present, permits **only**
  matching actions.
- `env` are fix-ups for the host process, never where credentials go. Runtime
  (or top-level) blocks only.
- `persist` are extra home paths whose writes survive. Runtime/top-level only.
- `network` runs that binary's host process behind the same enforced egress
  proxy as the jail (Linux: an empty network namespace; macOS: Seatbelt).
  Absent, host commands keep the host's network.

### Matching

A built-in profile matches the **parsed** command: the command path and
positional arguments, flags removed — `gh pr merge --squash 42` and
`gh -R acme/app pr merge 42` both read `pr merge 42`. kubectl's resource
types are canonicalized (`deploy`, `deployment/api` → `deployments`). A
pattern word is a glob; a final lone `*` matches any number of remaining
words. A word starting with `-` is a flag condition, matched wherever the flag
appears and however it is spelled: `* --profile=prod` catches
`aws --profile prod s3 ls`. Rules name flags by their long form. A binary
without a profile matches its raw argv.

### Resolution order

1. **Built-in guardrails** — always apply; nothing loosens them.
2. **The step's `isolation.host`** — may narrow (`aws: false`, a stricter
   `allow`, more `deny`s), never widen: its `allow` must match *in addition to*
   the runtime's, it cannot name a binary the layers above don't make a host
   command, and it cannot set `env`/`persist`.
3. **The runtime's `isolation.host`**, then the top-level `isolation.host`.
4. **Defaults** — installed built-ins are host commands; anything else is
   native.

## Guardrails

Three layers, so safety does not depend on recognizing every subcommand:

1. **Auth/config subcommands by name** (parsed): `gh auth *` (bar
   `auth status` without `--show-token`), `gh config set|…`, `gh alias *`,
   `gh extension *`; `aws configure *`, `aws sso login|logout`,
   `aws sts get-session-token|assume-role…`, `aws ecr get-login-password`;
   `gcloud auth *`, `gcloud config set|unset|configurations`,
   `gcloud … print-*-token`; `az login|logout|account set|account
   get-access-token`; `kubectl config set-*|use-context|delete-*|view --raw`,
   `kubectl create token`, `kubectl proxy|port-forward`; `docker
   login|logout|context *`; `npm|pnpm login|logout|adduser|token *|config
   set`; `terraform login|logout|console`. Containers that reach the host:
   `docker run -v /:…` (any bind outside the workspace), `--privileged`,
   `--cap-add`, `--device`, `--pid|--net|--ipc|--userns=host`, `docker build
   --ssh`, and `docker compose` beyond its read-only verbs. `ssh`/`scp`: no
   `-o ProxyCommand|LocalCommand|…`, no `-F`, no agent or port forwarding, and
   never to a git host (`git@github.com git-receive-pack` would push around
   the git policy).
2. **A keyword guard for binaries without a profile**: an argument of
   `login`, `logout`, `signin`, `signout`, `auth`, `revoke`, `configure`,
   `credentials`, `token`, `adduser` is refused unless an `allow` rule names
   the action.
3. **Structural**: the copy-on-write home. A `logout` that slipped past 1–2
   "succeeds" only in the throwaway layer — and the event says a write was
   discarded.

## gh {#gh}

Reads (`pr view|diff|checks|list`, `issue view|list`, `api` GET, GraphQL
queries) are bound to the dispatch's repository. Writes are bound to the
dispatch's **own target** (see [Isolation → writes](Isolation#writes-are-bound-to-the-dispatchs-own-target)):
a fixer may comment on its PR, review it, reply to and resolve its review
threads (`gh api graphql` `resolveReviewThread` — conductor resolves the
thread to its PR first); `pr create`, `issue create`, `api` POST
`/pulls|/issues`, writes to any other PR, merging and closing are refused.
`gh` never approves. `pr checkout` is refused (use git in the jail). The host
`gh` runs with `GH_REPO` set to the dispatch's repository.

## git {#git}

- Local git runs natively in the jail.
- Network: GitHub remotes (and the base clone's own origin) are rewritten —
  through `GIT_CONFIG_*`, no file touched — to `conductor::owner/repo`. Every
  fetch, push, lazy blob fetch, alias or script that needs the network runs
  `git-remote-conductor`; conductor performs it from the shared base clone
  with your identity after policy: the dispatch's repository only; pushes only
  to the dispatch's own branch — never the default branch, no force push, no
  deletion; review steps no push at all. Every push records the ref and
  old→new SHAs.
- Signing: `gpg.ssh.program` / `gpg.program` is conductor's signing shim. It
  sends the payload; conductor checks it is a commit object whose tree and
  parents exist in the dispatch's repository and signs it outside the jail
  with your configured `user.signingkey` (ssh-keygen / gpg reads the key, as
  for your own commits; a box without ssh-keygen signs natively in the SSHSIG
  format). Commits show **Verified**; the key never enters the jail. The limit
  is deliberate: an agent can get content *it wrote* signed as you — that is
  committing as you — but nothing else (no tags, no other namespace).
- conductor's own git against base clones and worktrees runs hardened:
  `core.hooksPath=/dev/null`, `core.fsmonitor=false`, a `core.sshCommand`
  read from your global/system config only, `protocol.ext.allow=never`, no
  external diff, replace refs ignored, and the git dir named explicitly — a
  worktree's `.git` pointer is agent-writable and never trusted.

## Tool-call hooks

For claude-code, conductor also passes `PreToolUse`/`PostToolUse` hooks
(`--settings`) and runs it with `--output-format stream-json`: every tool call
is a `tool_call` event before it runs and a `tool_result` after. Optional
intent rules refuse a call early with a reason the model sees:

```yaml
isolation:
  intent:
    allow_paths: ["src/**"]          # edits only here
    deny_paths: ["**/migrations/**"]
    max_delete_lines: 200            # refuse deleting/emptying a longer file
    deny_tools: [WebFetch]
```

This layer is guidance and visibility, not a wall: a shell can express the
same action in ways a tool-call rule cannot recognize. The guarantees stay
with the jail, the host-command policy, and brokered git. codex has no hook
point conductor can answer from a headless `codex exec`; jailed codex turns run
with `approval_policy="never"` and a `read-only` (review) or
`workspace-write` (fixer) sandbox.

## Limits

- **Host commands run workspace content on your machine.** `terraform plan`
  with an agent-written `external` data source, `docker build` of an
  agent-written Dockerfile, a `kubectl apply` of agent-written manifests: the
  copy-on-write home and the per-tool home view bound what that code can
  read, but it runs as you, with that tool's credentials. Set such tools to
  `false`, or narrow them with `allow`, where that matters.
- `npm publish` runs with `--ignore-scripts` on the host (lifecycle scripts
  are agent-written code).
- A tool that ignores `$HOME` and writes via its own absolute paths outside
  your home is not contained by the copy-on-write home (Linux still hides
  conductor's state; macOS denies writes to the real home).
