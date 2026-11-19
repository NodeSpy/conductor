# Step caches

By default every agent run gets a fresh home ([`sandbox.persist: none`](Isolation#containment-and-detection)),
so every run downloads and unpacks its dependencies again. A Go module cache
alone can be more than a gigabyte per run.

A step cache lets runs of a step start from what an earlier run of it
filled. The step names its cache with a **key**, a template over its facts.
Conductor learns which directories in the home runs of that key keep
filling. When a run's step succeeds, conductor publishes those directories
under the key. The next run of the key starts with them already in place.
Restoring a cache copies nothing on Linux, and a run can never change a
published copy.

Conductor knows no package manager, repository layout or branch. A key is
an opaque name the step's config gives, and what a cache holds is learned
from what runs write.

Caches are **off** until you turn them on with `sandbox.caches.enabled:
true`. They need the agent sandbox, and a step only gets one if it names a
key. An upgrade never uses disk for a config that did not ask for it.

## Naming a step's cache

```yaml
workflows:
  fix:
    steps:
      - id: fixer
        type: agent
        cache:
          key: "pr-{{.repo}}-{{.pr}}"         # this step's own cache
          restore_from: ["{{.repo}}"]          # read-only fallbacks, in order
  refresh:
    cache: { key: "{{.repo}}" }               # default for the workflow's agent steps
    steps:
      - { id: build, type: agent, prompt: "Build and run the tests." }
```

- `key` is required. It is the cache the step restores from first, and the
  only one it ever publishes to.
- `restore_from` lists up to 8 more keys. A launch whose own key has nothing
  published yet is shown the first of these that has. They are
  **read-only** to the step: what its run writes over them is published to
  the step's own key, never back to theirs.
- A workflow's `cache:` is the default for its own agent steps. A step's own
  `cache:` wins. A called workflow does not inherit its caller's.
- A step with no key gets no cache. So does a background (hand-off) step,
  a shadow run, and any step an agent wrote ([agent-authored
  plans](Workflows) may not set `cache:` — a key is a write grant into a
  store other runs restore from).

### Rendering and namespacing

Each template renders over the step's facts the same way a memory scope
key does, `${repo}`, `${workflow}` and `${step}` included. A key that does
not render (a fact the step has no value for) means the step runs without a
cache. A `restore_from` entry that does not render is dropped.

A rendered key may use only `A-Z a-z 0-9 . _ + = : # @ / -`. It must start
with a letter or digit, be at most 200 characters, and contain no `..`, no
`//` and no trailing `/`. A fact that renders anything else (a space, a
quote, a newline, a glob) refuses the key, and the step runs uncached.

A key's identity is three things: the **namespace** (the pack instance
whose step declared it; your own config has none), the **template text**
and the **rendered value**. Two steps share a cache only when all three
agree. The store names each key by a digest of the three, never by the
rendered text. So a template that renders a value an event's sender
controls (a branch name, a PR title) can never land on another template's
key, however the value is spelled. A PR whose head branch is named
`acme/web` does not reach the `{{.repo}}` key `acme/web`.

A step gets a cache only when its target is one the platform assigned. A
target the event's sender chose (a body-templated webhook's `repo:`) gets
none.

## What a cache holds: learned paths

When a keyed run's step succeeds, conductor looks at what the run wrote
under its home. A directory becomes one of the key's **paths** once
`learn.min_runs` successful runs of the key (default 2) each wrote at least
`learn.min_size` there (default 50 MiB). From then on each successful run
publishes it. Paths are learned per key and kept across restarts. Once a
directory is learned, the directories inside it are part of it.

**The granularity rule.** A candidate is a directory **directly inside**
the home or one of these base directories: `~/.cache`, `~/.local`,
`~/.local/share`, `~/.local/state`, `~/.config`, and on macOS
`~/Library/Caches`. So `~/go`, `~/.npm`, `~/.cache/go-build`,
`~/.local/share/pnpm` and `~/Library/Caches/Yarn` are each one candidate.
The home and the base directories themselves never are. A candidate that
holds a protected path (below) is split into its children instead, down to
six levels. For example, `~/.cargo` holds `~/.cargo/credentials.toml`, so
`~/.cargo/registry` and `~/.cargo/git` are the candidates. A symlinked
directory is never a candidate.

**Never cached**, however much a run writes there:

- the agent tools' own state and login files (`~/.claude`, `~/.codex`, …)
- credentials: `~/.ssh`, `~/.gnupg`, `~/.netrc`, `~/.aws`, `~/.azure`,
  `~/.config/gcloud`, `~/.docker`, `~/.kube`, `~/.config/gh`, git config,
  `~/.npmrc`, `~/.pypirc`, `~/.cargo/credentials*`, `~/.m2/settings.xml`,
  `~/.gradle/gradle.properties`, `~/.terraform.d`, `~/Library/Keychains`, …
- shell start-up files and history
- conductor's own files in the home (`~/.conductor`, `~/.tmp`)

These are checked again whenever a cache is restored or published, so
learning state edited on disk cannot name one either.

## How a run gets its cache

When conductor launches a sandboxed agent for a keyed step:

1. It takes the newest published copy of the step's key. If the key has
   none, it takes the newest copy of the first `restore_from` key that has
   one.
2. It shows each of that copy's paths at its place in the home:
   - **Linux:** an overlay mount. The published copy is the read-only lower
     layer. Everything the run writes there goes to an upper layer that
     belongs to the launch. The overlay is mounted inside the run's
     namespace, with no extra privilege. The upper and work directories sit
     **beside the store** (`<dir>/.layers/`), never in the run's home or
     workspace. `mount(2)` takes them as paths and follows a symlink in
     them, so they must be somewhere no agent can write. The store and the
     layers are never visible inside the sandbox. Each launch has its own
     layers, even when launches share a home (`persist: home`).
   - **A path that already has content** (a kept home whose earlier launch
     filled `~/.cache/yarn` itself) still gets the cache. An overlay is
     mounted over it: the home's own files are hidden for the launch and
     left as they were. A copy replaces them. A file found where the cache
     or a directory on its way should be is moved aside, to
     `<name>.conductor-moved-<random>`, and a directory is made in its
     place. A symlink on the way is never followed or replaced: that path
     is skipped for the launch. A path that cannot be shown (under
     `~/.cache` when `persist: cache` links it elsewhere) is skipped too.
   - **macOS:** a copy-on-write clone (APFS `clonefile`) into the home. It
     costs almost no disk until the run changes a file.
   - **Neither available** (a kernel before 5.11, or a filesystem overlay
     cannot use; a volume without clones): a plain copy, up to 1 GiB, or no
     restore.

A launch never fails for its cache: anything that cannot be restored is
logged and skipped. The daemon log has a line for each launch:

```
cache autopilot:pr-acme/app-12: hit v01795…-a281…, 2 path(s) by overlay (run d2)
cache autopilot:pr-acme/app-13: restored from autopilot:acme/app v01795…-77c0…, 2 path(s) by overlay (run d3)
cache autopilot:pr-acme/app-14: miss (run d4)
```

## Publishing

At the end of a keyed step, conductor waits up to 10 seconds for the
step's launches to stop. If the step **succeeded**, it publishes from the
newest launch that ran:

1. It learns from what the run wrote (above).
2. It builds a new copy of the key's paths. Where the launch had an
   overlay, the copy is the published lower layer with the run's upper
   layer applied the way the overlay showed it: deletions (whiteouts) and
   replaced directories included. Elsewhere it is copied from the home,
   walked by descriptor: a symlink is copied as a link and never followed,
   and FIFOs, sockets and devices are skipped. Nothing in the store ever
   shares an inode with a home.
3. It writes the copy to disk under a temporary name, then renames it into
   place as the key's newest version.

If the step **failed**, nothing is published and nothing is learned.
Either way, the launches' layers are then removed.

Two runs of one key that publish at once each build a whole copy. The one
that commits last is the newest, and a restore only ever sees a committed
copy, never a partial one. A superseded copy is removed as soon as no
running launch uses it.

```
cache autopilot:pr-acme/app-12: published v01795…-fc80… (~/.cache/yarn ~/.npm, 412.0MiB)
cache autopilot:pr-acme/app-12: nothing learned yet to publish (run 1795…)
```

**Who writes a key.** Only a step whose own `cache.key` renders to it. A
step's `restore_from` keys are never written by it. Because the namespace
and template are part of a key's identity, a shared key (`{{.repo}}`) is
written only by the steps that declare that template as their own key.
Give that key to steps whose runs you trust, such as runs on the default
branch, and let PR steps `restore_from` it. A PR step then starts warm and
publishes only to its own per-PR key.

## Eviction

The store is capped (`max_size`, default 10 GiB). Superseded versions go
first, then the least recently restored. A copy a running launch uses is
never removed. Whether a copy is in use is conductor's own record beside the
store: one entry per launch, held for as long as the launch runs. Nothing in
a home is ever read for it. A launch's entry is swept once its step has
ended and published, or an hour after the launch ended (sooner when the
store is over its cap). If the daemon died, its entries are swept once their
locks are free.

What a running launch writes over a cache (its upper layer) is not counted
against the cap, so one launch's writes never evict the caches others
restore. It is bounded like the rest of the run's disk: the sandbox's
low-disk guard watches the store's filesystem as well as the home's and the
workspace's.

A copy larger than the cap is not published, because it would be evicted
straight away. Nor is one that does not fit beside the copies running
launches use. The log says which.

`sandbox.caches.dir` must not be inside, equal to or above any `fs:` path of
an `isolation:` block. The store holds the launches' overlay layers, and a
launch that can see them could redirect a mount. Such a config is refused at
load.

## Configuration

```yaml
sandbox:
  caches:
    enabled: true              # off by default; needs the agent sandbox
    # dir: <state dir>/repo-caches
    # max_size: 10GiB          # superseded, then least recently used, evicted past this; "off" = no cap
    # learn:
    #   min_size: 50MiB        # a directory a run must fill this much…
    #   min_runs: 2            # …in this many successful runs of a key to be cached
```

### Fields removed with the recipe caches

Earlier versions had per-repository caches defined by recipes. These
fields under `sandbox.caches` are now **accepted and ignored**. A config
that still has them loads, and each gets a one-line warning at load:

`autodetect`, `repo_file`, `warm_on_miss`, `warm_every`, `warm_timeout`,
`recipes`, `defaults`, `repos`

A repository's `.conductor.yaml` `caches:` are no longer read. Name a step's
cache with its `cache:` block instead. What an older version left in the
store is removed when the daemon starts.

## Seeing and clearing caches

```
$ conductor sandbox caches
KEY                        TEMPLATE                     PATHS                        SIZE      LAST USED  IN USE
autopilot:acme/app         {{.repo}}                    ~/.cache/yarn ~/.npm         412.0MiB  just now
autopilot:pr-acme/app-12   pr-{{.repo}}-{{.number}}     ~/.cache/yarn ~/.npm         415.2MiB  2m ago     yes
autopilot:pr-acme/app-13   pr-{{.repo}}-{{.number}}     (learning)                   -         -
3 key(s), 2 published cop(ies), 827.2MiB in /home/op/.local/state/conductor/repo-caches

$ conductor sandbox caches clear autopilot:pr-acme/app-12   # by key as shown, its value, or its id
$ conductor sandbox caches clear --all
```

`clear` removes a key whole: its published copies and the paths it learned.
It keeps a copy that a running launch has an overlay on, unless you pass
`--force`.

## Private git dependencies

A dependency on another private repository by its SSH URL
(`git+ssh://git@github.com/acme/dep.git`) is fetched the way an agent
fetches it ([[Isolation#git-fetch]]). The run's git rewrites the SSH URL to
HTTPS, and its egress proxy adds a git fetch credential. The credential
comes from the connector that declares one (`git_fetch`). It is minted for
the repository through the connector instance that last dispatched trusted
work on it.

A launch's credential covers its repository plus the repository's
**learned dependencies**. A **teaching** launch is what learns them. A
launch teaches when all of these hold:

- its step's own key is one some step lists in `restore_from` (a shared
  key: other runs already trust what it publishes);
- it runs in a checkout conductor made fresh for it, at the head of the
  repository's default branch (not a PR's checkout, not a workspace another
  dispatch worked in, not an artifact);
- its target is one the platform assigned.

A teaching launch's credential covers any repository on the host. Each one
the forge served it is recorded once its step succeeds and its cache is
published, as the repository's learned dependencies for that key. This
replaces what that key's previous teaching run learned, so a dependency
removed from the default branch drops off. If the step fails, nothing is
learned.

Teaching adds no trust: a writer of a shared key is already trusted by
every run that restores from it.

Every other launch on the repository (a PR's, a fixer's, a hand-off's) gets
the repository plus those learned dependencies. A PR can never widen the
set. A PR that adds a new private dependency fetches it once the change is
on the default branch and a teaching run has fetched it. Until then, its
launches' audit rows say the credential was withheld, and why. Nothing is
configured, nothing is read from a lockfile, and the state lives in
conductor's state dir, kept across restarts.

A repository no trusted work has ever run on gets no credential, and the
fetch fails as it would without this:

```
Bad owner or permissions on /etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf
fatal: Could not read from remote repository.
```

## With the package proxy

A package proxy (a conductor-served `GOPROXY` with checksum-database
verification and a shared download store) covers **downloading and
verifying** packages. Step caches cover what the tools **unpack and build**
from them: the extracted module cache and the build cache. The two work
together without any wiring.

## Limits

- Teaching runs only in launches conductor runs itself (the `cli` and `acp`
  runtimes). A paseo-run step publishes its cache but does not teach.
- A step's launches must stop within 10 seconds of its end to publish. A
  step whose agent outlives it (an interactive or kept-alive session)
  publishes nothing.
- An overlay's upper layer is mounted `volatile`, so it is never synced
  while the run writes. A copy is synced to disk before it is published,
  but after a host crash a running launch's own writes may be lost.
- A cache's content is only as trustworthy as the runs that publish its
  key. Give a key that other steps restore from only to steps whose runs
  you trust.
