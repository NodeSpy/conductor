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
  refresh:                                     # run on pushes to the default branch
    cache: { key: "{{.repo}}" }                # default for the workflow's agent steps
    steps:
      - { id: build, type: agent, prompt: "Build and run the tests." }
```

- `key` is required. It is the cache the step restores from first, and the
  only one it ever publishes to.
- `restore_from` lists up to 8 more keys. A launch whose own key has nothing
  published yet is shown the first of these that has. They are
  **read-only** to the step: what its run writes over them is published to
  the step's own key, never back to theirs.
- `teach:` is removed. A config or pack that still sets it loads, with a
  warning, and the field is ignored ([below](#private-git-dependencies)).
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
`learn.min_runs` successful runs (default 2) each wrote at least
`learn.min_size` there (default 50 MiB). From then on each successful run
publishes it. Once a directory is learned, the directories inside it are
part of it.

Paths are learned per **template**, not per rendered key: runs of
`pr-{{.repo}}-{{.pr}}` count together for every PR, so a key that is new
for every PR does not start learning from nothing. Once the template has
learned its paths, a new PR's first successful run publishes, and its
second run gets a hit. The learned paths are kept across restarts.

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

- the agent tools' own state, login files, install and logs (`~/.claude`,
  `~/.codex`, `~/.local/share/claude`, `~/.cache/claude-cli-nodejs`, …)
- the credential and login files conductor shares or copies into a home:
  `~/.ssh`, `~/.gnupg`, `~/.netrc`, `~/.aws`, `~/.azure`,
  `~/.config/gcloud`, `~/.docker`, `~/.kube`, `~/.config/gh`, git config,
  `~/.npmrc`, `~/.pypirc`, `~/.cargo/credentials*`, `~/.m2/settings.xml`,
  `~/.gradle/gradle.properties`, `~/.terraform.d`, `~/Library/Keychains`, …
- shell start-up files and history
- conductor's own files in the home (`~/.conductor`, `~/.tmp`)

These are checked again whenever a cache is restored or published, so
learning state edited on disk cannot name one either.

What a run writes elsewhere in its learned paths is published as it is.
Conductor does not scan a cache's content: a cache is kept from reaching
runs of a different trust by its key ([who writes a key](#publishing)),
not by what is inside it.

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
   - **A copy or clone** is made without going by path below the home:
     conductor opens each directory on the way from the home down without
     following a symlink, builds the copy in a staging directory it makes
     there, and renames it into place through the directory it opened. An
     agent of another launch sharing the home (`persist: home`, a resumed
     session) that swaps a directory for a symlink meanwhile cannot send
     the copy, or the removal of what was there, outside the home.
   - **Neither available** (a kernel before 5.11, or a filesystem overlay
     cannot use; a volume without clones): a plain copy, up to 1 GiB, or no
     restore.
   - **Leftovers.** If conductor stopped in the middle of a copy restore,
     the next restore into that home removes its staging directory
     (`.conductor-restore-<random>`, once it is an hour old) and what was
     moved aside for the path.

A second launch of the same dispatch in the same home (a resumed session,
an `output_schema` re-run) continues the first: it takes over the first
launch's overlay layers, so it sees what the first wrote, and nothing is
restored again over a copy the first already put in the home.

A launch never fails for its cache: anything that cannot be restored is
logged and skipped. The daemon log has a line for each launch:

```
cache autopilot:pr-acme/app-12: hit v01795…-a281…, 2 path(s) by overlay (run d2)
cache autopilot:pr-acme/app-13: restored from autopilot:acme/app v01795…-77c0…, 2 path(s) by overlay (run d3)
cache autopilot:pr-acme/app-14: miss (run d4)
```

## Publishing

At the end of a keyed step, conductor waits up to 10 seconds for the
step's launches to stop. Whatever a launch left running is killed when it
ends, before this: on Linux its pid namespace ends with it; on macOS a
cached launch runs in a process group of its own, and what is left in that
group is killed. If the step **succeeded**, it publishes from the newest
launch that ran (which, after a resume, holds both launches' writes):

1. It learns from what the run wrote (above).
2. It builds a new copy of the key's paths. Where the launch had an
   overlay, the copy is the published lower layer with the run's upper
   layer applied the way the overlay showed it: deletions (whiteouts) and
   replaced directories included. Elsewhere it is copied from the home,
   walked by descriptor: a symlink is copied as a link and never followed,
   and FIFOs, sockets and devices are skipped. Nothing in the store ever
   shares an inode with a home. A copied file is charged its size, not
   only its allocated blocks, against the store's cap: a sparse file is
   written out in full when it cannot be cloned, so it is refused before
   the copy rather than after.
3. Merged files lose any setuid, setgid or sticky bit, and every file
   loses its extended attributes.
4. It writes the copy to disk under a temporary name, then renames it into
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

## Trust model

The agent sandbox guards what leaves a run, not what the agent does inside
it ([[Isolation#what-the-sandbox-guards]]). For caches that is one rule:
lower-trust work never feeds higher-trust work.

- A run publishes only to its own step's key; `restore_from` keys are
  read-only to it.
- A key's identity includes its namespace and template, so a value an
  event's sender controls cannot land on another template's key.
- A step an agent wrote, and a step whose target the event's sender chose,
  gets no cache.

## Eviction

The store is capped (`max_size`, default 10 GiB). Superseded versions go
first, then the least recently restored. A file several versions share (a
per-PR copy built over its `restore_from` copy hard-links what it kept) is
counted once against the cap. A key with no published copy left and no
launch for a day loses its directory; what its template learned stays. A copy a running launch uses is
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

`clear` removes a key whole: its published copies. What the key's
template learned is shared by every key of the template and stays; `clear
--all` removes it too. `clear` keeps a copy that a running launch has an
overlay on, or that the daemon is copying into a launch's home at that
moment, unless you pass `--force`. Both are recorded on disk beside the
store, so `clear` sees them without asking the daemon.

## Private git dependencies

A dependency on another private repository by its SSH URL
(`git+ssh://git@github.com/acme/dep.git`) is fetched the way an agent
fetches it ([[Isolation#git-fetch]]). The run's git rewrites the SSH URL to
HTTPS, and its egress proxy adds the git fetch credential of the connector
that declares one (`git_fetch`) to fetches of any repository on the
connector's hosts, including a dependency a PR adds for the first time.
Nothing is learned, configured, or read from a lockfile. A push is
refused.

## With the package proxy

A package proxy (a conductor-served `GOPROXY` with checksum-database
verification and a shared download store) covers **downloading and
verifying** packages. Step caches cover what the tools **unpack and build**
from them: the extracted module cache and the build cache. The two work
together without any wiring.

## Limits

- On macOS a process that moves itself to another process group and hides
  its environment from `ps` (a platform binary does) escapes the kill at the
  end of its launch, and may still write the home while it is published.
- A step's launches must stop within 10 seconds of its end to publish. A
  step whose agent outlives it (an interactive or kept-alive session)
  publishes nothing.
- An overlay's upper layer is mounted `volatile`, so it is never synced
  while the run writes. A copy is synced to disk before it is published,
  but after a host crash a running launch's own writes may be lost.
- A cache's content is only as trustworthy as the runs that publish its
  key. Give a key that other steps restore from only to steps whose runs
  you trust.
