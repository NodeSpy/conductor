# Repository dependency caches

By default every agent run gets a fresh home ([`sandbox.persist: none`](Isolation#containment-and-detection)),
so every run downloads and unpacks its dependencies again. A Go repository's
module cache alone can be more than a gigabyte per run.

Repository caches work like a CI cache. A cache has a **name**, a **key**
rendered from the checkout's files (`go-{{hash "go.sum"}}`), and **paths** in
the agent's home or workspace. Conductor fills it once, from the repository's
default branch. Each later run in a checkout of that repository then starts
with the cache already at those paths. Restoring a cache copies nothing, and
a run can never change the shared copy.

Caches are on by default with the agent sandbox. Recipes for common
ecosystems are detected in the checkout, so most repositories need no
configuration.

## How a run gets its caches

When conductor launches a sandboxed agent in a checkout it made for a
repository it trusts (a dispatch's PR or branch worktree):

1. It works out the repository's cache definitions
   ([where definitions come from](#where-definitions-come-from)).
2. For each cache it renders the key over **the run's own checkout** and
   looks in the store for that exact key. If there is none, it takes the
   newest cache whose key starts with one of the `restore_keys`.
3. It shows the cache at its paths:
   - **Linux:** an overlay mount. The shared cache is the read-only lower
     layer. Everything the run writes there goes to an upper layer in the
     run's own home, which is deleted with the home. The overlay is mounted
     inside the run's namespace, with no extra privilege. The layers
     directory is hidden from the agent, and the store itself is never
     visible inside the sandbox.
   - **macOS:** a copy-on-write clone (APFS `clonefile`) into the home or
     workspace. It costs almost no disk until the run changes a file.
   - **Neither available** (a kernel before 5.11 or a filesystem overlay
     cannot use; a volume without clones): a plain copy, up to 1 GiB, or no
     restore.
4. It sets the cache's environment (`GOMODCACHE`, `GOFLAGS=-modcacherw`,
   `npm_config_cache`, …), so the run's tools use those paths.

A target that already has files in it is left alone, and so is a path that
goes through a symlink. The daemon log has a line for every cache, naming
the hit, partial or miss, the key and how it was restored:

```
repo cache acme/app go (autodetect): hit go-linux-amd64-300c… for go-linux-amd64-300c…, 2 path(s) by overlay (run d2)
repo cache acme/app go (autodetect): miss for go-linux-amd64-9f1e… (run d1)
```

## Who writes a cache

**Only conductor writes a cache, and only from the default branch.** An agent
run never publishes anything, whatever branch it is on. A run on a PR branch
is the channel a poisoned cache would come through, which is why CI systems
keep PR-branch caches apart.

Conductor fills a cache with a **warm** step:

1. A fresh checkout of the repository's **default branch**, as just fetched.
   No git directory is included. The warm never takes a branch name, so
   nothing a run controls can choose what is warmed.
2. The caches that checkout defines: its own `.conductor.yaml` and the
   recipes detected in it, plus your config.
3. For each cache whose key is not published yet, its `warm:` command runs
   **in the agent sandbox**, with the default-branch checkout as its
   workspace and a fresh, empty home. It gets your `sandbox.egress` and
   `sandbox.allow`, limits and detection, like any agent. It gets no
   conductor socket, no token, no model endpoint and no login.
4. If the command succeeds, conductor publishes **only the declared paths**.
   A path that is a symlink is not published. The cache is built under a
   temporary name and renamed into place in one step. A published key is
   never replaced or written to.

A warm runs when a launch misses its key or only restores a partial match
(at most once every ten minutes per repository), and on a schedule if you
set `warm_every`. The log shows each publish:

```
repo cache acme/app go: warming go-linux-amd64-300c… on the default branch 3c8d5639…
repo cache acme/app go: published go-linux-amd64-300c… (~/go/pkg/mod ~/.cache/go-build, 62.4MiB, in 13s)
```

A PR that changes its lockfile therefore gets a partial restore (the default
branch's cache, under a matching restore key) and fetches the difference
itself. Its cache is published after the PR merges.

**Eviction.** The store is capped (`max_size`, default 10 GiB). When it is
over the cap, the least recently restored caches are removed first. A cache
that a live home still has an overlay on is never removed.

## Where definitions come from

Definitions are merged by cache name. The first source that defines a name
wins:

1. `sandbox.caches.repos.<owner/name>` in your config.
2. `sandbox.caches.defaults` in your config, for every repository.
3. The repository's own [`.conductor.yaml`](#the-repository-file-conductoryaml),
   read from its **default branch only**.
4. **Autodetect:** the recipes whose `detect:` files the checkout has.

An entry with `disabled: true` turns a name off for every source below it.

## Recipes

A recipe is plain data: `detect` files, a `key` template, `restore_keys`,
`paths`, `env` and a `warm` command. Conductor has no other knowledge of any
ecosystem. The bundled recipes are:

| recipe | detected by | paths | warm |
|---|---|---|---|
| `go` | `go.sum` | `~/go/pkg/mod`, `~/.cache/go-build` | `go mod download`, then `go build ./...` (failures ignored) |
| `npm` | `package-lock.json`, `npm-shrinkwrap.json` | `~/.npm` | `npm ci --ignore-scripts` |
| `pnpm` | `pnpm-lock.yaml` | `~/.local/share/pnpm/store` | `pnpm fetch` |
| `yarn` | `yarn.lock` | `~/.cache/yarn` | `yarn install --frozen-lockfile --ignore-scripts` |
| `pip` | `requirements*.txt` | `~/.cache/pip` | `pip download -r …` |
| `uv` | `uv.lock` | `~/.cache/uv` | `uv sync --frozen --no-install-project` |
| `cargo` | `Cargo.lock` | `~/.cargo/registry`, `~/.cargo/git` | `cargo fetch --locked` |
| `bundler` | `Gemfile.lock` | `~/.bundle/vendor` | `bundle install` |

`sandbox.caches.recipes` replaces a bundled recipe by name, or adds a new
one. A recipe entry with `disabled: true` removes a bundled one.

### Key templates

A key is a Go template with three functions and nothing else:

- `{{hash "go.sum" "**/go.sum"}}` — SHA-256 over every regular file the
  globs match (path and content, in path order). `**` matches any number of
  directories. Symlinks are never followed; `.git` and `node_modules` are
  never walked. If nothing matches, it renders as an empty string.
- `{{os}}`, `{{arch}}` — the platform the run is on.

A rendered key may contain only `A-Z a-z 0-9 . _ + = -`, must start with a
letter or digit, and is at most 200 characters. A restore key is a prefix:
`go-{{os}}-{{arch}}-`.

### Paths and environment

- `~/rel` is in the agent's home; `rel` is in its workspace. Absolute paths,
  `..`, `.git`, the home or workspace itself, and paths containing `,` or `:`
  are refused.
- No cache may hold, contain or sit inside the agent tools' state and login
  files (`~/.claude`, `~/.codex`, …), credentials (`~/.ssh`, `~/.gnupg`,
  `~/.netrc`, `~/.aws`, `~/.docker`, `~/.kube`, `~/.config/gh`, git config),
  or shell start-up files.
- `env` values expand `{{home}}` and `{{workspace}}`. A cache may not set the
  loader, shell, proxy, CA, git or conductor variables (`PATH`, `HOME`,
  `LD_*`, `DYLD_*`, `*_PROXY`, `SSL_CERT_*`, `NODE_OPTIONS`, `GIT_*`,
  `CONDUCTOR_*`, …) or anything named like a credential.

## The repository file: `.conductor.yaml`

A repository can define its own caches in a `.conductor.yaml` at its root:

```yaml
version: 1
# autodetect: false        # this repository's caches are only the ones below
caches:
  - recipe: go             # the bundled recipe as is
  - recipe: npm
    warm: npm ci --ignore-scripts --workspaces
  - name: tools
    key: 'tools-{{os}}-{{hash "tools/go.sum"}}'
    restore_keys: ['tools-{{os}}-']
    paths: ["~/.cache/tools"]
    env: { TOOLS_CACHE: "{{home}}/.cache/tools" }
    warm: make tools
```

- Conductor reads it **only from the default branch**, both when a launch
  resolves its caches and when it warms. A `.conductor.yaml` that a PR adds
  or changes has no effect until it is merged. Otherwise a PR could point a
  cache at a file of its choosing, or choose the command a warm runs.
- **It grants nothing.** It cannot add egress, show a path outside the home or
  workspace, or lift any sandbox setting. Its warm commands run in the same
  sandbox as an agent, against the default branch, which is trusted for the
  same reason the default branch's CI is.
- `version: 1` is required. Keys this conductor does not read are logged as
  warnings and ignored, so the file can carry settings a newer conductor
  understands. Today only `caches:` and `autodetect:` are read.
- `repo_file: false` in your config stops conductor reading it.

This file is unrelated to the `.conductor/` directory in **conductor's own
config directory**, where vendored packs are kept. That directory is
conductor's, on your machine; `.conductor.yaml` belongs to a repository and
is read from its default branch.

## Configuration

```yaml
sandbox:
  caches:
    # enabled: true            # default with the agent sandbox
    # dir: <state dir>/repo-caches
    # max_size: 10GiB          # LRU-evicted past this; "off" = no cap
    # autodetect: true
    # repo_file: true          # read .conductor.yaml from the default branch
    # warm_on_miss: true       # warm when a launch misses (at most every 10m per repo)
    # warm_every: ""           # also re-warm every known repository, e.g. 24h (at least 10m)
    # warm_timeout: 20m
    recipes:                   # replace or add recipes by name
      - name: deno
        detect: [deno.lock]
        key: 'deno-{{os}}-{{hash "deno.lock"}}'
        restore_keys: ['deno-{{os}}-']
        paths: ["~/.cache/deno"]
        env: { DENO_DIR: "{{home}}/.cache/deno" }
        warm: deno cache main.ts
    defaults:                  # every repository
      - recipe: go
    repos:
      acme/web:
        - recipe: npm
          warm: npm ci --ignore-scripts
        - name: go
          disabled: true
```

## Seeing and clearing caches

```
$ conductor sandbox caches
REPO      CACHE  KEY                                         SIZE     LAST USED  ORIGIN      IN USE
acme/app  go     go-linux-amd64-300c3d1c3f09b311bee07873be…  62.4MiB  just now   autodetect  yes
1 cache(s), 62.4MiB in /home/op/.local/state/conductor/repo-caches

$ conductor sandbox caches clear acme/app            # every cache of a repository
$ conductor sandbox caches clear acme/app go         # one cache
$ conductor sandbox caches clear acme/app go KEY     # one key
```

`clear` keeps a cache that a live agent home still has an overlay on, unless
you pass `--force`.

## With the package proxy

A package proxy (a conductor-served `GOPROXY` with checksum-database
verification and a shared download store) covers **downloading and
verifying** packages. Repository caches cover what the tools **unpack and
build** from them: the extracted module cache and the build cache. The two
work together without any wiring. A warm runs in the agent sandbox, so it
fetches through whatever that sandbox fetches through, and a cache adds no
`GOPROXY` or `GONOSUMDB` of its own.

## Limits

- Caches are restored only into a checkout conductor made for a repository
  it trusts. An agent in a directory of your own, a member's workspace, or a
  workspace unpacked from an artifact gets none.
- An overlay's upper layer is mounted `volatile`, so it is never synced to
  disk. It is the run's scratch space: after a host crash, a run's own
  writes over a cache may be lost.
- A cache's content is only as trustworthy as the default branch it was
  built from, and the dependencies that branch names.
