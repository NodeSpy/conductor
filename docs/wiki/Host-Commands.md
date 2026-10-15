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
  `terraform`, `gcloud`, `az`, `ssh`, `scp`, `npm`, `pnpm`, `vop` (npm/pnpm only for
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
    git:                                     # the push binding (git is never a shim)
      allow: ["*", "push release/*"]         # pushes may also reach release/* branches
    gh:
      deny: ["pr merge *", "repo delete *"]
    aws:
      allow: ["s3 ls *", "s3 cp * s3://build-artifacts/*"]   # an allow list ⇒ ONLY these
      persist: ["~/.aws/sso/cache"]          # (the built-in profile already persists this one)
      network: { egress: [s3.amazonaws.com, sts.amazonaws.com] }
      paths: ["~/.aws/config"]               # narrows aws's own view further still (no ~/.aws/credentials)
    kubectl:
      deny: ["delete *", "rollout undo *"]
      env:  { KUBECONFIG: ~/.kube/staging }  # fix-up for when the machine's default isn't right for agents
    terraform:
      allow: ["plan *", "init *", "fmt *"]   # plan/init execute workspace content: named here, they run confined
      # network: { egress: [registry.terraform.io, s3.amazonaws.com] }   # replaces the default endpoints of its confined runs
    mytool:                                  # any binary, by name
      allow: ["status *"]
    docker: false                            # keep it out of reach entirely
```

- `deny` refuses matching actions; `allow`, if present, permits **only**
  matching actions.
- `env` are fix-ups for the host process, never where credentials go. Runtime
  (or top-level) blocks only.
- `persist` are extra home paths whose writes survive. Runtime/top-level only.
- `paths` **narrows** the home-relative entries the binary's host-side process
  may see (copy-on-write). A profiled tool (`gh`, `aws`, …) may only list
  paths inside its own built-in view (its own paths plus `~/.gitconfig` and
  `~/.config/git`) — anything else is a config error, since this can only
  narrow what the binary already sees, never widen it. A binary with no
  profile sees the whole home by default; `paths` replaces that with exactly
  the list. A `persist` path must lie inside the narrowed view (a built-in
  profile's own persist default that falls outside it is just dropped).
  Runtime/top-level only. When more than one operator block sets it, the view
  is what all of them allow: `~/.aws` and `~/.aws/orgA` give `~/.aws/orgA`,
  and two that share nothing give nothing.

  One member per org, each seeing only its own org's AWS files:

  ```yaml
  runtimes:
    member_org_a:
      use: acp
      agent: claude-code
      isolation:
        host:
          aws:
            paths: ["~/.aws/orgA"]
            env: { AWS_CONFIG_FILE: "~/.aws/orgA/config", AWS_SHARED_CREDENTIALS_FILE: "~/.aws/orgA/credentials" }
          terraform:
            paths: ["~/.aws/orgA", "~/.terraform.d"]
            env: { AWS_CONFIG_FILE: "~/.aws/orgA/config", AWS_SHARED_CREDENTIALS_FILE: "~/.aws/orgA/credentials" }
  ```

  Org B's `~/.aws/orgB` isn't there for that member's `aws` or `terraform`.
  `aws --profile orgB-…` fails because no file names that profile, not
  because a rule caught the word.
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
   command, and it cannot set `env`/`persist`/`paths`.
3. **The runtime's `isolation.host`**, then the top-level `isolation.host`.
4. **Defaults** — installed built-ins are host commands; anything else is
   native.

## Commands that execute workspace content {#content-executing}

Some host commands run what the agent wrote: `terraform plan` runs the
configuration's providers and data sources — an `external` data source is
any program — and `docker build` the Dockerfile's steps. On the host they
would run outside the jail, as you, with your credentials. So they are
**refused unless your own config names them** in the binary's `allow` list —
a global or runtime block (a step can only narrow), and a pattern whose first
word is literal (`plan *`; a blanket `*` does not count). The refusal says
why and names the knob:

```
host_command ✗ refused  terraform plan — denied (terraform plan executes workspace content on the host: it runs the
  workspace configuration's provider plugins and data sources — an `external` data source is any program. Allow it in
  your config — isolation.host.terraform.allow: ["plan *"] — and it runs in a host-side jail)
```

| tool | content-executing (refused unless allowed) | stays available |
|---|---|---|
| terraform | `init`, `get`, `plan`, `apply`, `destroy`, `refresh`, `import`, `console`, `test`; and `show`, `validate`, `providers schema`, `state show` (they load the provider binaries under `.terraform`, which the agent can write) | `fmt`, `version`, `output`, `workspace …`, `state list`, … |
| docker | `build`, `image build`, `buildx build`, `buildx bake`; `run`/`create` of an image built on this machine (no registry digest) | `ps`, `images`, `run` of a pulled image, … (`compose` beyond its read-only verbs stays refused outright) |
| kubectl | `apply`, `create`, `replace` with `-f`/`-k`; `kustomize` (`plugin` stays refused outright) | `get`, `describe`, `logs`, … |
| aws | `cloudformation deploy`, `package`, `create-stack`, `update-stack`, `create-change-set`, `create-stack-set`, `update-stack-set`; `lambda create-function`, `update-function-code`, `publish-layer-version` | everything else |
| gcloud | `builds submit`, `functions deploy`, `run deploy`, `app deploy` | everything else |
| az | `deployment …`, `functionapp deployment …`, `webapp deploy`, `webapp up` | everything else |
| npm, pnpm | `publish` | `whoami`, …; `install`, `ci`, `run`, `exec`, `test`, `start` run natively **in the agent's jail** (no registry token, the jail's network) — never on the host |
| gh | extensions (refused outright) | — |

**An allowed one runs in a host-side jail**, never unconfined:

- **Only what it needs is there.** Linux: an allow-list root (the same
  pivot_root construction as the agent's jail) — the base system read-only,
  the tool's install, the copy-on-write view of **the tool's own** config and
  credential paths, the workspace, the dispatch's `/tmp`. Your session's
  sockets (`/run/user/<uid>`: ssh-agent, gpg-agent, D-Bus), other daemons'
  sockets (the Docker socket is there only for `docker`), the rest of your
  home and conductor's state do not exist. macOS: a Seatbelt profile that
  denies every write but the scratch dir and the dispatch's `TMPDIR`, every
  read of your home but the tool's cloned config, and the Keychain services.
- **The workspace is read-only.** Its writes land in the dispatch's
  copy-on-write layer for that tool (Linux: an overlay's upper dir; macOS: an
  APFS clone the command runs in, what it changed kept for the next run) —
  so `terraform init` then `terraform plan` works within a dispatch, and
  nothing reaches the workspace itself.
- **No stdin and no terminal.**
- **The network is the tool's service endpoints only**, through conductor's
  egress proxy with no other route (Linux: an empty network namespace;
  macOS: Seatbelt allows only the proxy's port): terraform the registry,
  HashiCorp releases and GitHub release downloads, and the AWS, Google Cloud
  and Azure APIs; docker the common registries; aws `*.amazonaws.com`; npm
  the npm registry; and so on. The binary's own `network:` block replaces the
  default (`open` still goes through the proxy, recorded).
- The event says so: `host_command → exit 0 terraform plan` with
  `confined: true` and the egress allowlist in the audit row.

What stays true of a confined run: it acts with that tool's credentials
against that tool's services — a `terraform apply` you allow will change
infrastructure. The jail keeps the agent's code from reaching anything
*else*. `docker build` steps run inside your Docker daemon's own isolation,
which conductor does not control.

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
   set`; `terraform login|logout`. Containers that reach the host:
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
queries) run on the dispatch's repository. The **gh profile binds writes to
the dispatch's own PR/issue**: a fixer may comment on it, review it, edit it,
update its branch, reply to and resolve its review threads (`gh api graphql`
`resolveReviewThread` — conductor resolves the thread to its PR first).
Refused unless `isolation.host.gh.allow` names the command: `pr create`,
`issue create`, `api` POST `/pulls|/issues`, `pr merge|close|reopen`, a
repository-level write (`release create`, `label create`, …), a write to any
other PR or issue, and anything on another repository (`-R other/repo`).
A review step's gh writes nothing unless named the same way; a closed target
takes no gh write at all.

```yaml
isolation:
  host:
    gh:
      allow: ["*", "pr create *", "pr comment * --repo=acme/docs"]
      deny:  ["pr merge *"]
```

`"*"` keeps every gh command available (an allow list is otherwise *only*
what it names); `"pr create *"` names opening a PR; the third entry names
comments on `acme/docs`. `gh` never approves. `pr checkout` is refused (use
git in the jail). The host `gh` runs with `GH_REPO` set to the dispatch's
repository. This is the binary's own policy: conductor's `github` verbs are
granted separately (see [[Isolation#writes-are-bound-to-the-dispatchs-own-target]]).

## git {#git}

- Local git runs natively in the jail.
- Network: GitHub remotes (and the base clone's own origin) are rewritten —
  through `GIT_CONFIG_*`, no file touched — to `conductor::owner/repo`. Every
  fetch, push, lazy blob fetch, alias or script that needs the network runs
  `git-remote-conductor`; conductor performs it from the base clone (never
  the dispatch's own clone, whose config is the agent's — see
  [[Isolation#git-one-clone-per-dispatch]]) with your identity after policy:
  the dispatch's repository only; then the **git profile's push binding**:
  the dispatch's own branch by default; any other branch only when
  `isolation.host.git.allow` names it (`["*", "push release/*"]`); never a
  force push or a deletion; nothing from a review step or to a closed target.
  `isolation.host.git` configures only this binding — git is never a shim.
  Every push records the ref and old→new SHAs.
- Signing: `gpg.ssh.program` / `gpg.program` is conductor's signing shim. It
  sends the payload; conductor checks it is a commit object whose tree and
  parents exist in the dispatch's repository and signs it outside the jail
  with your configured `user.signingkey`, read from your global config and
  conductor's base clone — never the dispatch clone's (ssh-keygen / gpg reads the key, as
  for your own commits; a box without ssh-keygen signs natively in the SSHSIG
  format). Commits show **Verified**; the key never enters the jail. The limit
  is deliberate: an agent can get content *it wrote* signed as you — that is
  committing as you — but nothing else (no tags, no other namespace).
- conductor's own git against base clones and worktrees runs hardened:
  `core.hooksPath=/dev/null`, `core.fsmonitor=false`, a `core.sshCommand`
  read from your global/system config only, `protocol.ext.allow=never`, no
  external diff, replace refs ignored, and the git dir named explicitly — a
  checkout's `.git` is agent-writable and never trusted; the dispatch's
  objects reach conductor's git only as a conductor-owned copy, made without
  following symlinks.

## vop {#vop}

`vop exec <profile> -- <command>` runs a command with AWS credentials fetched
from 1Password, so the profile judges two commands.

- **vop's own subcommand.**
  - Refused: the ones that reveal or change credentials or config, or open a
    shell: `dump`, `cred-process`, `cat`, `add`, `edit`, `rm`, `rotate`,
    `migrate`, `auth`, `serve`, `shell`, `unlock`, `refresh`.
  - Run: `ls`, `profile`, `show`, `version`, `check`, `skill`, `test`.
  - Anything unknown is refused.
- **Pinned per member.** `exec` needs an explicit profile, and your config
  must name it literally:

  ```yaml
  isolation:
    host:
      vop:
        allow: ["exec orgA *"]   # this member's runtime may use profile orgA, and no other
  ```

  - A blanket `*` (or `exec *`) opens no profile.
  - A bare `vop exec -- …` is refused: it would pick the profile from the
    environment or a `.vop` file. `VOP_PROFILE` never reaches the host run.
- **The inner command** must be `aws`, `terraform` or `kubectl`. It is
  decided as that tool on its own: its guardrails, its
  `isolation.host.<tool>` rules, `paths` and `env`, and its path guard.
  - A content-executing one (`terraform plan`/`apply`) is refused under vop;
    confined runs under vop aren't supported.
- **Read-only by default.** Under vop the inner command may read
  (`aws … describe-*`/`list-*`/`get-*`, `aws s3 ls`, `terraform state list`,
  `kubectl get`, …). A write runs only if your allow list for that tool names
  it literally: `isolation.host.aws.allow: ["*", "s3 cp *"]`. `*` alone
  opens no write.

## Approval grants {#grants}

A command the broker would refuse because your allow list doesn't name it
can run once you approve that exact command. The member gate mints a grant
(`internal/grant`):
- for one agent: its [[Paseo-Provider|provider identity]], or a cli worker's
  dispatch;
- for one tool and one argv;
- pinned to the content of the files it names, short-lived, usually single-use.

When the command comes:
1. **The broker finds the grant.** It decides the command again as if your
   allow list named it. A built-in guardrail, a `deny`, the path guard, a
   closed target, and a step's own allow list still refuse it, and a
   content-executing command still runs confined.
2. **It copies each pinned file into private scratch**: a per-user `0700` dir
   the jail never shows. It never follows a symlink and never reads outside
   the workspace or the dispatch's `/tmp`. It hashes what it copied and checks
   the approved digest. A mismatch is refused (`grant.ErrDigest`) and the
   grant isn't used up.
3. **It consumes the grant and runs the command** with those files named by
   their private copies, read-only. The bytes it checked are the bytes the
   tool reads.

A second use of a single-use grant is refused (`grant.ErrConsumed`), and so
is an expired grant. The audit row carries the grant id, approver and case.

**Which bytes a pinned file is.** The gate hashes a proposal's files with the
jail's `DigestFile(identity, path)`, through the same read the broker makes
at use, so you approve exactly the bytes the command will run on:
- `path` is as the agent names it, relative to its workspace; a file in the
  jail's `/tmp` is the dispatch's own, never the host's;
- a file a confined run wrote is read from the dispatch's copy-on-write layer
  it went to: a confined `terraform plan -out=plan.tfplan` leaves the plan
  there, never in the workspace. Any other file is the workspace's;
- the layers are per tool, and neither the gate nor the broker picks one by
  the command. A path two tools' layers both hold is refused as ambiguous;
- a command run from a subdirectory resolves a relative path from there,
  while the gate resolves it from the workspace. If they name different
  files, the digest doesn't match and the grant is refused, not used.

What counts as "the same command" is `grant.ParseArgv`:
- words, and a bare flag's value (`-var-file prod.tfvars`), keep their order;
- an extra or different word, flag or value is a different command;
- self-contained `-k=v` / `--k=v` flags are compared as a set: across
  different names their order doesn't matter, as it doesn't to the tool,
  while values of one repeated flag keep their order.

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

- `npm publish` (once allowed) runs with `--ignore-scripts` too (lifecycle
  scripts are agent-written code).
- A tool that ignores `$HOME` and writes via its own absolute paths outside
  your home is not contained by the copy-on-write home (Linux still hides
  conductor's state; macOS denies writes to the real home).
