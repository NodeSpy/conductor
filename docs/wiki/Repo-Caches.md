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

Caches are **off** until you turn them on with `sandbox.caches.enabled:
true`, and they need the agent sandbox. An upgrade never starts warms, uses
disk or fetches anything for a config that did not ask for it. Once caches
are on, recipes for common ecosystems are detected in the checkout, so most
repositories need no further configuration.

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
     layer. Everything the run writes there goes to an upper layer that
     belongs to the run. The overlay is mounted inside the run's namespace,
     with no extra privilege. The upper and work directories sit **beside the
     store** (`<dir>/.layers/`), never in the run's home or workspace.
     `mount(2)` takes them as paths and follows a symlink in them, so they
     must be somewhere no agent can write. The store, and these layers, are
     never visible inside the sandbox. Each launch has its own layers, even
     when launches share a home (`persist: home`), and they are removed when
     the launch ends. A cache at a path that cannot be shown (for example
     under `~/.cache` when `persist: cache` links it elsewhere) is skipped
     and logged; the launch runs without that cache, never fails for it.
   - **A path that already has content** (a kept home, `persist: home`,
     whose earlier launch missed and filled `~/go/pkg/mod` itself) still
     gets the cache. An overlay is mounted over it: the home's own files
     are hidden for the launch and left as they were. A copy replaces
     them. A file found where the cache or a directory on its way should
     be is moved aside, to `<name>.conductor-moved-<random>`, and a
     directory made in its place. A symlink on the way is never followed
     or replaced: that cache is skipped for the launch.
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
   conductor socket, no token, no model endpoint and no login. A dependency
   on another private repository by its SSH URL is fetched over HTTPS with
   a git fetch credential the warm never holds (below).
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

**Eviction.** The store is capped (`max_size`, default 10 GiB). The cap
covers the published caches and warm scratch. When the store is over the
cap, the least recently restored caches are removed first. A cache that a
running launch has an overlay on is never removed. That record is
conductor's own, kept beside the store: one entry per launch, held for as
long as the launch runs and removed when it ends (or, if the daemon died,
swept once its lock is free). A home that is kept, or never removed, pins
nothing once its launches have ended. Nothing in a home is ever read for it.

What a running launch writes over a cache (its upper layer) is not counted
against the cap, so one launch's writes never evict the caches others
restore. It is bounded like the rest of the run's disk: the sandbox's
low-disk guard watches the store's filesystem as well as the home's and
the workspace's.

A warm whose output is larger than the cap is not published, because it
would be evicted straight away. The log says so, and the store keeps a mark
with the size, so that key is not warmed again, across restarts, while the
cap is smaller than that. Raising `max_size` (or setting it `off`) lets it
warm again; so does `conductor sandbox caches clear` for that repository,
cache or key. A warm that fits the cap but not the room the caches in use
leave is not published, and not warmed again until there is room for it.

A warm in flight is cancelled when the daemon stops, and on Linux killed if
the daemon dies. Scratch left by a daemon that died mid-warm is removed when
the next one starts.

`sandbox.caches.dir` must not be inside, equal to or above any `fs:` path
of an `isolation:` block: the store holds the launches' overlay layers, and
a launch that can see them could redirect a mount. Such a config is refused
at load.

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
- `env` values expand `{{home}}` and `{{workspace}}`. No cache may set the
  loader, shell, proxy, CA, git or conductor variables (`PATH`, `HOME`,
  `LD_*`, `DYLD_*`, `*_PROXY`, `SSL_CERT_*`, `NODE_OPTIONS`, `GIT_*`,
  `CONDUCTOR_*`, …), or anything named like a credential.
- A repository's own file is held to an **allow-list**. It may set only
  cache-location variables (`GOMODCACHE`, `GOCACHE`, `npm_config_cache`,
  `npm_config_store_dir`, `YARN_CACHE_FOLDER`, `PIP_CACHE_DIR`,
  `UV_CACHE_DIR`, `BUNDLE_PATH`), each to `{{home}}/…` or `{{workspace}}/…`
  inside one of that cache's own paths. It may also set `GOFLAGS` to exactly
  `-modcacherw`. A definition that sets anything else (a checksum database, a
  registry, an insecure transport, a toolchain, `-toolexec`, a shell
  variable) is ignored, with a warning. Your own config is not limited this
  way.

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
  workspace, set any but a cache-location variable, or lift any sandbox
  setting. Its warm commands run in the same
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
    enabled: true              # off by default; needs the agent sandbox
    # dir: <state dir>/repo-caches
    # max_size: 10GiB          # everything under dir; LRU-evicted past this; "off" = no cap
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

`clear` also forgets that a cleared key's warm was over `max_size`. It keeps
a cache that a running launch has an overlay on, unless
you pass `--force`.

## Private git dependencies

A lockfile that names a private repository by its SSH URL
(`git+ssh://git@github.com/acme/dep.git`) is warmed the way an agent fetches
it ([[Isolation#git-fetch]]): the warm's git rewrites the SSH URL to HTTPS,
and its egress proxy adds a git fetch credential to fetches of the
repositories in scope. The credential comes from the connector that declares
one (`git_fetch`), minted for the warmed repository through the connector
instance that last dispatched trusted work on it. Until such work has run
since the daemon started, a warm gets none, and the fetch fails as it would
without this:

```
Bad owner or permissions on /etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf
fatal: Could not read from remote repository.
```

yarn 1 prints `You don't appear to have an internet connection` at the start
of every warm. It decides that from the network interfaces it sees, and the
sandbox's network namespace has only loopback. It is a warning only: yarn
reaches the registry and git hosts through the egress proxy
(`HTTPS_PROXY`), which resolves names itself.

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
- A launch's layers, and its pin on the caches they sit on, last as long as
  the launch. A run's writes over a cache are not kept for its next launch,
  even in a kept home.
- A cache's content is only as trustworthy as the default branch it was
  built from, and the dependencies that branch names.
