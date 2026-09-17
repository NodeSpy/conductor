# installed.yaml pack-engine prune — diagnosis, fix, verification

**Branch:** `fix/pack-engine-manifest-prune`
**Fix commit:** `b52306f` — *fix(plugin): never prune install state against a partial plugin set*
(this report is committed on top)

---

## 1. Diagnosis

### The hypothesis was wrong — noted, per the brief

The brief proposed that something recomputes `PluginRefs()` from a config where
the pack's internal `run: js` is **not** visible (pack not instantiated), then
prunes. I tested that directly before changing anything, and it is **false** at
every `cfg.PluginRefs()`-based prune site.

`config.Load` instantiates packs at `internal/config/config.go:1113`
(`c.instantiatePacks`) — *before* the trigger/extends/normalize passes — so a
pack's workflows, triggers and checks are in `c.Workflows` / `c.Triggers` /
`c.Checks` by the time anything asks for the plugin set. `PluginRefs` reads
engines off the steps via `c.WalkSteps` (`internal/config/plugins.go:137-149`),
and `WalkSteps` walks exactly those three maps (`internal/config/steps.go:23-37`).

Empirically, with a throwaway probe against a pack whose only engine reference
is its own `run: js` step:

```
armed=true  workflows=[team/team-flow] triggers=1  PluginRefs=[engines/js]
armed=false workflows=[team/team-flow] triggers=1  PluginRefs=[engines/js]
```

Armed or disarmed, the pack-internal engine **is** in the derived set. Pack
instantiation is also all-or-nothing — a missing vendored tree
(`pack_instantiate.go:735`), a failed `requires.conductor` gate
(`pack_instantiate.go:165`) and a failed `requires.connectors` gate
(`pack_connectors.go:241`) are all hard errors, so `Load` either yields a config
with the pack's steps present or fails outright (and a failed load holds
degraded at `cmd/conductor/main.go:282` without reconciling anything).

### The actual clearing path

**`internal/plugin/resolve.go:139-147`** (pre-fix `:123-133`) — the drop loop in
`Reconcile`, the **only** code in the tree that removes entries from
`installed.yaml` other than the explicit `conductor plugin remove`
(`cmd/conductor/plugins.go:638`):

```go
if opts.Only == "" && !opts.GapsOnly {
    for _, k := range state.Keys() {
        if _, still := refs[k]; !still {
            state.Delete(k)
            ...
```

It treats `refs` as the authoritative complete desired set. It has no way to
tell a whole-config refs map from a deliberately partial one.

There are exactly four reconcile entry points:

| # | Site | refs passed | Prunes pre-fix? |
|---|------|-------------|-----------------|
| 1 | `resolvePluginsForInit` — `cmd/conductor/packs.go:77` (`conductor init`) | `cfg.PluginRefs()`, `cfg` from `config.Load` | yes — **correct**, complete set |
| 2 | `cmdPluginUpdate` — `cmd/conductor/plugins.go:416` | `cfg.PluginRefs()`, `cfg` from `config.Load` | yes when no name given — **correct** |
| 3 | `refreshPlugins` — `cmd/conductor/update.go:258` (auto-update `deps` cycle) | `cfg.PluginRefs()`, `cfg` from `config.Load` | yes — **correct** |
| 4 | **`cmdPluginAdd` — `cmd/conductor/plugins.go:472-476`** | **one-entry map: `{u.InstallKey(): pr}`**, `Only` empty, `GapsOnly` false | **yes — destructive** |

Site 4 is the bug. `conductor plugin add <anything>` resolves exactly one
reference and then lets the drop loop delete **every other record**. Proven
against stock `main`:

```
after installing js:        [engines/js]
after `plugin add jira`:    [connectors/jira]
engines/js was PRUNED by an unrelated `plugin add`
```

`engines/js` is precisely what a pack-internal `run: js` needs, and it is the
kind of entry nothing in the operator's own config re-adds — which is why the
box then failed `conductor validate` with `plugin js: … not installed` until
`conductor plugin update js` put it back.

### Why a same-version restart differs from a version boot

Not because boot prunes — **boot does not reconcile at all.** `Options.GapsOnly`
(the documented "boot gap-fill" posture, `resolve.go:72-74`) has **no production
caller**; `grep` finds it only in its own definition, the drop/skip guards, and
one test. `cmd/conductor/main.go` contains no `plugin.` reference whatsoever. So
neither a same-version restart nor a new-version boot rewrites `installed.yaml`
on its own.

The asymmetry the box shows is therefore about *what runs around* the version
change, not about the boot itself. A same-version manual restart runs none of
the four sites above, so the entry survives. A version change runs the
update/`init`/`plugin add` machinery — and `conductor plugin add` (site 4) is
the one that wipes unrelated records. Sites 1–3 pass a fully-instantiated config
and were never the cause; I verified that a full reconcile against a real
instantiated-pack config keeps `engines/js` even pre-fix.

Root cause, stated exactly: **the prune trusted a refs map the caller never
promised was complete.**

---

## 2. The fix

Make the prune **opt-in**, and only opt in where the set is provably complete.

1. **`internal/plugin/resolve.go`** — new `Options.Prune`. The drop loop is now
   `if opts.Prune && opts.Only == "" && !opts.GapsOnly`. A partial refs map can
   no longer authorize a deletion.

2. **`internal/config/plugins.go`** — new `(*Config).PluginRefsComplete()`. It
   reports whether `PluginRefs()` may be treated as authoritative: true when
   there is no `packs:` block, or when packs have been instantiated. This is the
   requirement "a pack-USED engine must count as referenced", enforced at the
   config layer rather than left to each caller's discretion.

3. **`internal/config/config.go` / `pack_instantiate.go`** — unexported
   `packsInstantiated`, set when `instantiatePacks` completes (and immediately
   for a config with no packs). Nothing is serialized.

4. **`cmd/conductor/plugins.go`** —
   - `reconcilePlugins` (sites 1–3) sets `opts.Prune = cfg.PluginRefsComplete()`.
   - `cmdPluginAdd` (site 4) leaves `Prune` false, with a comment explaining that
     its refs map is deliberately a subset.

The trust/prune model is **not** weakened beyond the stated line: a genuinely
unreferenced engine is still removed by `init` / `plugin update`
(`TestReconcileDropsUnreferencedEngine` asserts exactly this). Connector and
runtime reconcile behavior is untouched, as is `plugin remove`.

Docs updated: `docs/design/use-unification.md` §C gains a "Pruning: only what is
provably unreferenced" subsection; `docs/wiki/Plugins.md` states that `plugin
add` only ever adds and that a pack-used engine counts as referenced.

---

## 3. Regression tests

**`internal/plugin/resolve_test.go`**
- `TestReconcilePartialRefsNeverPrunes` — the box bug at the prune site:
  install `engines/js`, then run the `plugin add jira` shape; `engines/js` must
  survive.
- `TestReconcileDropsUnreferencedEngine` — an engine nothing references is still
  pruned (guards against over-correcting).
- `TestReconcileDropsUnreferenced` — updated to pass `Prune: true`, since the
  prune is now opt-in.

**`internal/plugin/pack_engine_prune_test.go`** (new)
- `TestReconcileKeepsPackUsedEngine` — the full box scenario end to end: a config
  whose **only** engine reference is inside an instantiated pack; asserts the
  engine is in the reconcile's required set, that an authorized full reconcile
  installs it, that a **second** authorized reconcile does not prune it, and that
  it survives a reload of install state.
- `TestReconcileSkipsPruneWhenPacksNotInstantiated` — a config that declares
  packs it has not instantiated must not authorize a prune.

**`internal/config/pack_engine_plugin_test.go`** (new)
- `TestPluginRefsIncludesPackInternalEngine` (armed + disarmed subtests) — pins
  the property the diagnosis established, so the instantiation path cannot
  regress underneath the prune.
- `TestPluginRefsCompleteRequiresInstantiatedPacks` — the completeness predicate.

Pack fixtures are written inline via the package's existing `writePackSource`
idiom (local `source:` dir, offline), matching the surrounding pack tests; no new
`testdata/` tree was needed.

### Pre-fix failure output

Verified by stashing the fix to a stock `main` tree (`9a6efb6`) and running
main-API-compatible copies of the two new assertions:

```
$ git stash push -u && git log --oneline -1
9a6efb6 conductor once: one-shot / no-daemon event execution (GitHub Actions keystone) (#85)

$ go test ./internal/plugin/ -run TestPreFix -v
=== RUN   TestPreFixPartialRefsNeverPrunes
    zz_prefix_test.go:23: an unrelated single-plugin install pruned engines/js; state = [connectors/jira]
--- FAIL: TestPreFixPartialRefsNeverPrunes (0.00s)
=== RUN   TestPreFixSkipsPruneWhenPacksNotInstantiated
    zz_prefix_test.go:41: pruned against a config that cannot see its packs' engines: []
--- FAIL: TestPreFixSkipsPruneWhenPacksNotInstantiated (0.00s)
FAIL
FAIL	github.com/NodeSpy/conductor/internal/plugin	0.011s
```

Post-fix:

```
$ go test ./internal/plugin/ ./internal/config/ -run 'PackUsedEngine|PartialRefsNeverPrunes|PacksNotInstantiated|PackInternalEngine|PluginRefsComplete|DropsUnreferenced' -v
--- PASS: TestReconcileKeepsPackUsedEngine (0.00s)
--- PASS: TestReconcileSkipsPruneWhenPacksNotInstantiated (0.00s)
--- PASS: TestReconcileDropsUnreferenced (0.00s)
--- PASS: TestReconcileDropsUnreferencedEngine (0.00s)
--- PASS: TestReconcilePartialRefsNeverPrunes (0.00s)
ok  	github.com/NodeSpy/conductor/internal/plugin	0.015s
--- PASS: TestPluginRefsIncludesPackInternalEngine (0.01s)
    --- PASS: TestPluginRefsIncludesPackInternalEngine/armed
    --- PASS: TestPluginRefsIncludesPackInternalEngine/disarmed
--- PASS: TestPluginRefsCompleteRequiresInstantiatedPacks (0.00s)
ok  	github.com/NodeSpy/conductor/internal/config	0.029s
```

Note that `TestReconcileKeepsPackUsedEngine` (the full-set path) passes on
`main` too — correctly so, since the diagnosis found sites 1–3 were never
broken. The tests that fail pre-fix are the two partial-set ones above.

---

## 4. Verification

```
$ gofmt -l .
(no output — clean)

$ go build ./...
OK

$ go vet ./...
OK

$ go test ./...        # filtered to non-ok lines
(no FAIL lines — all packages green)

$ CGO_ENABLED=1 go test -race ./...    # filtered to non-ok lines
(no FAIL lines — all packages green)

$ go test ./internal/plugin/... ./cmd/conductor/...
ok  	github.com/NodeSpy/conductor/internal/plugin
ok  	github.com/NodeSpy/conductor/cmd/conductor
```

### Grep confirmation — no other prune site

```
$ grep -rn "Reconcile(" --include=*.go . | grep -v _test.go
cmd/conductor/plugins.go:477   (plugin add — Prune left false)
cmd/conductor/plugins.go:676   (reconcilePlugins — Prune = cfg.PluginRefsComplete())
internal/plugin/resolve.go:110 (the definition)

$ grep -rn "\.Delete(" --include=*.go cmd/ internal/plugin/ | grep -v _test.go
internal/plugin/resolve.go:143   the prune — now gated
cmd/conductor/plugins.go:638     `plugin remove <name>` — explicit, operator-named
cmd/conductor/workflows.go:100   unrelated (workflow store)
cmd/conductor/vault.go:102       unrelated (vault)

$ grep -rn "installStateFile" --include=*.go . | grep -v _test.go
internal/plugin/install.go only — one reader, one writer
```

`plugin remove` is unchanged and remains correct: it removes one
operator-named plugin and already prints a NOTE when the config still references
it, computed from `cfg.PluginRefs()` on a loaded (pack-instantiated) config — so
a pack-used engine does produce that warning.

---

## 5. Does the reconcile now need pack instantiation? — No network required

**No.** The prune needs the packs *instantiated*, but instantiation is an
**offline** read of the already-vendored tree under
`<config-dir>/.conductor/packs` (`packVendorDir`, `pack_instantiate.go:34`). The
network step is `conductor init` / `ResolvePacks`, which is separate and already
runs before any of the reconcile sites.

All four reconcile entry points already hold a `config.Load`-ed config, and
`Load` calls `instantiatePacks` unconditionally and **hard-fails** if a pack
cannot be instantiated (`loadPackManifest` → *"not fetched … run `conductor
init`"*). So in practice the complete set is always available with zero extra
work and zero network.

The `PluginRefsComplete()` guard is the belt-and-braces half, and it implements
exactly the rule the brief asked for: **when the pack cannot be instantiated,
the prune is skipped rather than run against a partial view** — we never delete
what we cannot prove is unused. A config that declares `packs:` without
instantiating them reconciles (installing gaps) but removes nothing.
