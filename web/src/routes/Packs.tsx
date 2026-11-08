// Packs (the design's js/screens/system.js "extensions" routes and
// js/screens/catalog.js, ported line for line): the Installed tab, the
// Browse tab (the catalog's marketplace grid), a catalog pack's Details, its
// Install review (Add), Add from a source, and a pack's own page
// (Customize: its overrides).
//
// Every action stages through the one config pipeline (packs.<instance>, the
// same stage → review → apply the rest of the dashboard uses) and goes to
// the review, the way the design's UI.staged does; nothing here writes
// config. The data:
//   - GET /api/v1/packs: installed instances, from their vendored manifests
//   - GET /api/v1/packs/catalog: the catalog `conductor pack search` serves
//     (installed + the registry's), each entry with its install review
//     (config.BuildPackReview, what `conductor pack add` prints)
//   - POST /api/v1/packs/review: fetch any source and review it (Add from a
//     source), never staging
//   - GET /api/v1/packs/{name}/manifest: a pack's roles, triggers and steps
//
// Left out: the prototype's demo state (fake versions history and dates; the
// catalog has no release history, so Versions lists what is known).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { parse as parseYAML, stringify as stringifyYAML } from 'yaml'
import { api } from '../api/client'
import type { PacksResponse, PackInfo, PackManifestResponse, CatalogResponse, CatalogPackInfo, PackReview, PackReviewResponse } from '../api/types'
import { config, staged, loadConfig, stage, stageDelete, cfgError } from '../configStore'
import { humanError } from '../api/errors'
import { Icon } from '../components/Icon'
import { FactsLine, type Fact } from '../components/Facts'
import { Menu, type MenuItem } from '../components/Menu'
import { TabsBar } from '../components/TabsBar'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { StatusMark } from '../components/StatusMark'
import { Yaml } from '../components/Yaml'
import { EmptyState } from '../components/EmptyState'
import { seg } from '../router'

// ---------- routes ----------

export function Packs(): JSX.Element {
  const s1 = seg(1)
  useEffect(() => {
    void loadConfig()
  }, [])
  if (s1 === 'item') return <PackDetails name={seg(2)} />
  if (s1 === 'install') return <InstallPack name={seg(2)} />
  if (s1 === 'source') return <PackFromSource />
  if (s1 === 'pack') return <PackCustomize name={seg(2)} />
  if (s1 && s1 !== 'browse') return <PackCustomize name={s1} />
  return <PacksPage tab={s1 === 'browse' ? 'browse' : 'installed'} />
}

// ---------- shared ----------

const enc = encodeURIComponent
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

// The staged view of one pack instance (the design's S.staged('packs', n)):
// the live packs.<name> block with every staged edit under it applied.
function packView(name: string): Record<string, any> | null {
  const tree = (config.value?.tree as any) || {}
  let cur: any = tree.packs?.[name] ? JSON.parse(JSON.stringify(tree.packs[name])) : null
  for (const ch of staged.value) {
    if (ch.path[0] !== 'packs' || ch.path[1] !== name) continue
    const rest = ch.path.slice(2)
    if (!rest.length) {
      cur = ch.delete ? null : JSON.parse(JSON.stringify(ch.value ?? null))
      continue
    }
    if (!cur) cur = {}
    let o = cur
    for (const k of rest.slice(0, -1)) {
      if (typeof o[k] !== 'object' || o[k] === null) o[k] = {}
      o = o[k]
    }
    const last = rest[rest.length - 1]
    if (ch.delete) delete o[last]
    else o[last] = ch.value
  }
  return cur
}

// The tag a staged pack carries next to its name (the design's stagedTag).
function stagedWord(name: string): string {
  const chs = staged.value.filter((c) => c.path[0] === 'packs' && c.path[1] === name)
  if (!chs.length) return ''
  const whole = chs.find((c) => c.path.length === 2)
  const live = !!(config.value?.tree as any)?.packs?.[name]
  if (whole?.delete) return 'removal staged'
  if (!live) return 'new · staged'
  return 'edit staged'
}
function StagedTag({ name }: { name: string }): JSX.Element | null {
  const w = stagedWord(name)
  return w ? <span class="tag" data-s="run">{w}</span> : null
}

// Go to the review the way the design's UI.staged does, once a stage landed.
function toReview() {
  if (!cfgError.value) location.hash = '#/system/config'
}

function updateDeps(): boolean {
  const u = (config.value?.tree as any)?.update || {}
  return u.deps != null ? !!u.deps : !!u.auto
}

// cmpVer: semver-ish comparison (a newer catalog release is an update).
function cmpVer(a: string, b: string): number {
  const pa = String(a).replace(/^v/, '').split(/[.-]/).map((x) => parseInt(x, 10) || 0)
  const pb = String(b).replace(/^v/, '').split(/[.-]/).map((x) => parseInt(x, 10) || 0)
  for (let i = 0; i < 3; i++) if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0)
  return 0
}

const fact = {
  origin: (official: boolean, src?: string): Fact =>
    official
      ? { icon: 'badge-check', value: 'Official', label: 'Published and verified by conductor’s maintainers' }
      : { icon: 'git-fork', value: src ? `Custom · ${src}` : 'Custom source', tone: 'custom', label: 'From a source you added: its trust differs' },
  update: (ver: string): Fact => ({ icon: 'circle-arrow-up', value: `${ver} available`, tone: 'update', label: 'A newer version can be installed' }),
  latest: (ver: string): Fact | null => (ver ? { icon: 'tag', value: ver, label: 'The newest release' } : null),
  version: (ver: string): Fact | null => (ver ? { icon: 'tag', value: ver, label: 'The installed version' } : null),
  policy: (held: boolean, ver: string): Fact | null =>
    !ver ? null : held ? { icon: 'pin', value: `Held at ${ver}`, tone: 'held', label: 'Held at this version until you change it' } : { icon: 'refresh-cw', value: 'Stays current', label: 'Takes every new release' },
}

// A source's short name for its origin fact: the registry or repo it's in.
function srcId(source?: string): string {
  if (!source) return ''
  const parts = source.replace(/^git::/, '').replace(/\/\/[^/]*$/, '').replace(/\.git$/, '').split(/[/:]/).filter(Boolean)
  return parts.length > 1 ? parts[parts.length - 2] : parts[0] || ''
}

// The service's name as people write it (the design's UI.serviceName).
const serviceName = (n: string) => n.split('-').map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w)).join(' ')

function useCatalog(enabled = true): { cat: CatalogResponse | null; err: string } {
  const [cat, setCat] = useState<CatalogResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (!enabled) return
    let off = false
    api
      .packCatalog('')
      .then((r) => !off && setCat(r))
      .catch((e) => !off && setErr(humanError(e)))
    return () => {
      off = true
    }
  }, [enabled])
  return { cat, err }
}

function usePacks(): { packs: PackInfo[] | null; err: string } {
  const [packs, setPacks] = useState<PackInfo[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api
      .packs()
      .then((r: PacksResponse) => setPacks(r.packs ?? []))
      .catch((e) => setErr(humanError(e)))
  }, [])
  return { packs, err }
}

// The design's UI.toast (a staged edit that stays on the page).
function Toast({ t, onDone }: { t: { title: string; sub: string } | null; onDone: () => void }): JSX.Element | null {
  useEffect(() => {
    if (!t) return
    const h = setTimeout(onDone, 6000)
    return () => clearTimeout(h)
  }, [t])
  if (!t) return null
  return (
    <div class="toasts" aria-live="polite">
      <div class="toast" data-s="run" role="status">
        <StatusMark s="run" />
        <div>
          <b>{t.title}</b>
          <p>{t.sub}</p>
        </div>
        <span />
      </div>
    </div>
  )
}

function Confirm({ title, body, ok, onOk, onClose }: { title: string; body: string; ok: string; onOk: () => void; onClose: () => void }): JSX.Element {
  return (
    <div class="scrim" onClick={onClose}>
      <div class="modal" role="dialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}>
        <div class="modal-head">
          <div>
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body small dim">{body}</div>
        <div class="modal-foot">
          <button class="btn ghost" type="button" onClick={onClose} autoFocus>
            Cancel
          </button>
          <button class="btn danger" type="button" onClick={onOk}>
            {ok}
          </button>
        </div>
      </div>
    </div>
  )
}

// What a pack instance's use:/source: is written as, minus its version.
function instRef(c: Record<string, any> | null): { key: 'use' | 'source'; ref: string } {
  if (c?.source) return { key: 'source', ref: String(c.source) }
  return { key: 'use', ref: String(c?.use || '') }
}

// The exact release an entry is pinned to: =1.2.0, as its version: or as its
// use: @suffix (the same constraint). A bare 1.2.0 means ">= 1.2.0" and a
// range (^1) or no version at all takes new releases.
function exactPin(c: Record<string, any> | null): string {
  const v = String(c?.version || (c?.use ? String(c.use).split('@')[1] || '' : '')).trim()
  const m = /^==?\s*(v?\d+\.\d+\.\d+\S*)$/.exec(v)
  return m ? m[1] : ''
}

// The pack actions every place shares: hold / let it update, remove, update.
function usePackActions(onToast: (t: { title: string; sub: string }) => void) {
  const [confirmDel, setConfirmDel] = useState<string | null>(null)
  const [plan, setPlan] = useState<{ p: PackInfo; latest?: string } | null>(null)
  const hold = async (name: string) => {
    const c = packView(name) || {}
    const pin = exactPin(c)
    const release = !!(c.hold || pin)
    if (c.hold) await stageDelete(['packs', name, 'hold'], `let packs.${name} update again`)
    else if (pin && c.version) await stageDelete(['packs', name, 'version'], `let packs.${name} update again`)
    else if (pin) {
      const { key, ref } = instRef(c)
      await stage(['packs', name, key], ref.split('@')[0], `let packs.${name} update again`)
    } else await stage(['packs', name, 'hold'], true, `hold packs.${name} at this version`)
    if (!cfgError.value) {
      const n = staged.value.length
      onToast({ title: `Staged: ${release ? `let ${name} update again` : `hold ${name} at this version`}`, sub: `Not applied yet · ${n} change${n > 1 ? 's' : ''} to review and apply` })
    }
  }
  const remove = async (name: string) => {
    setConfirmDel(null)
    await stageDelete(['packs', name], `remove packs.${name}`)
    toReview()
  }
  const update = async (name: string, to: string) => {
    const c = packView(name) || {}
    const { key, ref } = instRef(c)
    setPlan(null)
    // a release is a version: (see installBlock); an @suffix on the
    // reference would otherwise outrank it as a git ref
    if (ref.includes('@')) await stage(['packs', name, key], ref.split('@')[0], `update packs.${name} to ${to}`)
    // an exact pin moves to the new release and stays pinned; otherwise
    // "at least the new release" (a bare version is >=)
    if (!cfgError.value) await stage(['packs', name, 'version'], exactPin(c) ? `=${to}` : to, `update packs.${name} to ${to}`)
    toReview()
  }
  const modals = (
    <>
      {confirmDel ? (
        <Confirm
          title={`Remove pack ${confirmDel}?`}
          body="Stages removing it from packs: (and its overrides). Its workflows stop; conductor drops the vendored copy on apply."
          ok="Stage removal"
          onOk={() => void remove(confirmDel)}
          onClose={() => setConfirmDel(null)}
        />
      ) : null}
      {plan ? <PlanModal p={plan.p} latest={plan.latest} onClose={() => setPlan(null)} onUpdate={(to) => void update(plan.p.name, to)} /> : null}
    </>
  )
  return { hold, askRemove: setConfirmDel, showPlan: (p: PackInfo, latest?: string) => setPlan({ p, latest }), modals }
}

// The design's pack-plan modal ("conductor pack plan"): from, to, source and
// what this daemon can say about it.
function PlanModal({ p, latest, onClose, onUpdate }: { p: PackInfo; latest?: string; onClose: () => void; onUpdate: (to: string) => void }): JSX.Element {
  const upd = latest && p.version && !p.local && cmpVer(latest, p.version) > 0 ? latest : ''
  const src = (p.source || p.use || '').split('@')[0]
  return (
    <div class="scrim" onClick={onClose}>
      <div class="modal wide" role="dialog" aria-modal="true" aria-label={upd ? `Update ${p.name} to ${upd}` : `Plan for ${p.name}`} onClick={(e) => e.stopPropagation()}>
        <div class="modal-head">
          <div>
            <div class="caps">conductor pack plan</div>
            <h2>{upd ? `Update ${p.name} to ${upd}` : `Plan for ${p.name}`}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body stack small">
          <dl class="kv">
            <dt>from</dt>
            <dd class="mono">{p.version || 'not fetched yet'}</dd>
            <dt>to</dt>
            <dd class="mono">{upd || p.version || '—'}</dd>
            <dt>source</dt>
            <dd class="mono">
              {src} ({p.official ? 'trusted' : p.local ? 'local · yours' : 'pack_trust'})
            </dd>
          </dl>
          {upd ? (
            <>
              <div class="caps">What changes</div>
              <div class="check-row">
                <StatusMark s="ok" />
                <div>
                  the catalog has <code>{upd}</code>; your overrides stay in your config and still apply
                </div>
              </div>
              <div class="check-row">
                <StatusMark s="wait" />
                <div>new triggers arrive <b>disarmed</b>: nothing new runs until you arm it</div>
              </div>
            </>
          ) : (
            <div class="check-row">
              <StatusMark s="ok" />
              <div>{p.local ? 'a pack of your own: it changes when its folder does' : latest ? 'up to date with the catalog' : 'no newer release in the catalog'}</div>
            </div>
          )}
          <div class="check-row">
            <StatusMark s="ok" />
            <div>the change is validated against your config before it applies; a pack can narrow isolation, never widen it</div>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn ghost" type="button" onClick={onClose}>
            Close
          </button>
          {upd ? (
            <button class="btn primary" type="button" onClick={() => onUpdate(upd)}>
              <Icon name="check" />
              Stage update
            </button>
          ) : null}
        </div>
      </div>
    </div>
  )
}

// ---------- Packs: Installed | Browse ----------

function PacksPage({ tab }: { tab: 'installed' | 'browse' }): JSX.Element {
  const { packs, err } = usePacks()
  const listed = (packs ?? []).filter((p) => !p.template_only)
  // the catalog says what's newer; only a pack from somewhere else can update
  const needCat = tab === 'browse' || listed.some((p) => !p.local)
  const { cat, err: catErr } = useCatalog(packs !== null && needCat)
  const [toast, setToast] = useState<{ title: string; sub: string } | null>(null)
  const actions = usePackActions(setToast)
  // nothing installed (or nothing in the catalog either, on Browse): the
  // centred hero with its one action, in place of the header's Add a pack
  const catEmpty = (cat !== null || !!catErr) && !(cat?.packs || []).some((it) => !it.template_only)
  const hero = packs !== null && (tab === 'installed' ? !listed.length : catEmpty)
  const latestOf = (p: PackInfo) => cat?.packs.find((c) => c.installed && c.pack_instance === p.name)?.latest || ''

  return (
    <div class="pk-page">
      <div class="page-head">
        <div>
          <h1>Packs</h1>
          <p class="lede-2">
            A{' '}
            <span class="term" tabIndex={0} title="A bundle of workflows (and sometimes bot templates) someone else wrote, installed and updated as one unit." data-term="pack">
              pack
            </span>{' '}
            is a bundle of workflows someone else wrote, installed and updated as one unit.
          </p>
        </div>
        {hero ? null : (
          <div class="actions">
            <a class="btn primary" href="#/packs/browse">
              <Icon name="plus" />
              Add a pack
            </a>
          </div>
        )}
      </div>
      <TabsBar
        items={[
          { id: 'installed', label: 'Installed', href: '#/packs', count: packs && listed.length ? listed.length : null },
          { id: 'browse', label: 'Browse', href: '#/packs/browse' },
        ]}
        active={tab}
        ariaLabel="Packs"
      />
      {err ? <div class="banner err" role="alert">Couldn't load your packs: {err}</div> : null}
      {tab === 'browse' ? (
        <Browse cat={cat} err={catErr} hero={hero} />
      ) : (
        <>
          {hero ? (
            <EmptyState hero testid="packs-empty" art={<Icon name="extensions" />} title="No packs installed yet" action={{ label: 'Browse packs', href: '#/packs/browse', testid: 'packs-browse' }}>
              <p>A pack brings a set of ready-made workflows in one go. Browse the catalog, or add one from its source; each is reviewed before anything changes.</p>
            </EmptyState>
          ) : (
            <div class="panel">
              <ul class="dl-list dl-conns pack-list">
                {packs === null ? (
                  <li class="dl-sub pack-empty">{err ? 'Nothing to show until conductor answers.' : 'Loading…'}</li>
                ) : (
                  listed.map((p) => <PackRow key={p.name} p={p} latest={latestOf(p)} actions={actions} />)
                )}
              </ul>
            </div>
          )}
          <p class="dl-sub" style="margin-top:12px">Adding, updating or removing a pack is reviewed like any other change.</p>
        </>
      )}
      <div class="small muted sentence" style="margin-top:6px">
        <Icon name="updates" /> {updateDeps() ? 'Packs update themselves when a new version still checks out against your config.' : 'Packs update only when you ask.'}
      </div>
      {actions.modals}
      <Toast t={toast} onDone={() => setToast(null)} />
    </div>
  )
}

function PackRow({ p, latest, actions }: { p: PackInfo; latest: string; actions: ReturnType<typeof usePackActions> }): JSX.Element {
  const c = packView(p.name) || {}
  const armed = Object.values((c.triggers || {}) as Record<string, any>).filter((t) => t && t.enabled).length
  const n = p.triggers_total
  const unbound = (p.roles || []).filter((r) => !(c.connectors || {})[r] && (p.role_candidates?.[r] ?? 0) !== 1)
  const dormant = unbound.length > 0 && armed > 0
  const upd = !p.local && latest && p.version && cmpVer(latest, p.version) > 0 && String(c.version || '') !== latest && !String(c.use || '').endsWith('@' + latest) ? latest : ''
  const pin = exactPin(c)
  const held = !!(c.hold || pin)
  const tp = p.contents?.bot_templates || 0
  const src = instRef(c).ref.split('@')[0] || (p.source || p.use || '').split('@')[0]
  const page = `#/packs/pack/${enc(p.name)}`
  const facts: (Fact | null)[] = [
    upd ? { ...fact.update(upd), onClick: () => actions.showPlan(p, latest) } : null,
    tp ? { icon: 'bots', value: `Includes ${plural(tp, 'bot template')}`, href: '#/bots', label: 'Start a bot from it in New bot' } : null,
    { icon: 'play', value: !n ? 'Nothing to switch on' : n === 1 ? (armed ? 'Automation on' : 'Automation off') : `${armed} of ${n} automations on`, label: 'Its automations' },
    p.official ? fact.origin(true) : { ...fact.origin(false), value: p.local ? 'Your own' : `Third party · ${src}` },
    // a pack of your own has no releases to take or hold
    !p.local ? fact.policy(held, pin || p.version || '') : null,
    fact.version(p.version || ''),
  ]
  const btnRef = useRef<HTMLButtonElement>(null)
  const [menu, setMenu] = useState(false)
  const items: MenuItem[] = [
    { label: 'Customize', icon: 'edit', onClick: () => (location.hash = page) },
    { label: 'What an update would change…', icon: 'eye', onClick: () => actions.showPlan(p, latest) },
    { label: held ? 'Let it update again' : 'Hold it at this version', icon: 'pause', onClick: () => void actions.hold(p.name) },
    '-',
    { label: 'Remove this pack…', icon: 'trash', onClick: () => actions.askRemove(p.name) },
  ]
  return (
    <li class={'dl-conn pack-row' + (dormant ? ' attn' : '')} data-s={dormant ? 'wait' : held ? 'idle' : 'ok'} data-pack={p.name}>
      <span class="dl-ng">{dormant ? <StatusMark s="wait" lg mkOnly label="Waiting" /> : held ? <StatusMark s="idle" lg mkOnly label="Parked" /> : null}</span>
      <span class="dl-nt">
        <b>
          {p.name} <StagedTag name={p.name} />
        </b>
        {dormant ? (
          <span class="reason">Not working yet: it needs a {unbound[0]} connection and none is chosen.</span>
        ) : (
          <span class="dl-why pack-desc">{p.vendored ? p.description || 'Your own pack.' : 'Not fetched yet: it is fetched when the change that added it applies.'}</span>
        )}
        <FactsLine facts={facts} />
      </span>
      <span class="dl-na">
        {dormant ? (
          <a class="btn sm secondary" href={page}>
            Choose a connection
          </a>
        ) : upd ? (
          <button class="btn sm secondary" type="button" onClick={() => actions.showPlan(p, latest)}>
            Update to {upd}…
          </button>
        ) : (
          <a class="btn sm secondary" href={page} title="Switch its automations on, pick its connections, adjust its steps: kept in your config, so updates don’t undo them">
            Customize
          </a>
        )}
        <button ref={btnRef} class="icon-btn" type="button" aria-label={`More for ${p.name}`} onClick={() => setMenu((o) => !o)}>
          <Icon name="more" />
        </button>
        {menu ? <Menu anchor={btnRef.current} items={items} onClose={() => setMenu(false)} label={`More for ${p.name}`} /> : null}
      </span>
    </li>
  )
}

// ---------- Browse: the catalog grid (catalog.js catalogHTML 'browse') ----------

type Origin = 'official' | 'custom'
const originOf = (it: CatalogPackInfo): Origin => (it.official ? 'official' : 'custom')
const needsOf = (it: CatalogPackInfo): string[] => it.requires || []

// a search matches words (in the name, description or what it uses); only
// the name tolerates a typo-style subsequence (catalog.js fuzzy)
function fuzzy(q: string, s: string): boolean {
  q = q.toLowerCase().trim()
  s = s.toLowerCase()
  if (s.includes(q)) return true
  const name = s.split(' ')[0]
  let i = 0
  for (const ch of name) if (ch === q[i]) i++
  return q.length > 2 && i === q.length
}

function Browse({ cat, err, hero }: { cat: CatalogResponse | null; err: string; hero: boolean }): JSX.Element {
  const [q, setQ] = useState('')
  const [origin, setOrigin] = useState<string[]>([])
  const [inst, setInst] = useState<string[]>([])
  const [needs, setNeeds] = useState<string[]>([])
  const [sort, setSort] = useState('name')
  const all = (cat?.packs || []).filter((it) => !it.template_only)
  const n = (f: (it: CatalogPackInfo) => boolean) => all.filter(f).length
  const toggle = (set: (f: (xs: string[]) => string[]) => void) => (v: string) => set((xs) => (xs.includes(v) ? xs.filter((x) => x !== v) : [...xs, v]))
  const instOf = (it: CatalogPackInfo) => (it.installed ? 'installed' : 'not')
  const items = all
    .filter(
      (it) =>
        (!origin.length || origin.includes(originOf(it))) &&
        (!inst.length || inst.includes(instOf(it))) &&
        (!needs.length || needs.some((x) => needsOf(it).includes(x))) &&
        (!q || fuzzy(q, `${it.name} ${it.description} ${needsOf(it).join(' ')}`)),
    )
    .sort((a, b) => a.name.localeCompare(b.name))
  const allNeeds = Array.from(new Set(all.flatMap(needsOf))).sort()
  const menus: FilterMenu[] = [
    {
      key: 'origin',
      label: 'Origin',
      multi: true,
      selected: origin,
      onToggle: toggle(setOrigin),
      options: [
        { v: 'official', label: 'Official', n: n((it) => originOf(it) === 'official') },
        { v: 'custom', label: 'Custom source', n: n((it) => originOf(it) === 'custom') },
      ].filter((o) => o.n),
    },
    {
      key: 'inst',
      label: 'Installed',
      multi: true,
      selected: inst,
      onToggle: toggle(setInst),
      options: [
        { v: 'installed', label: 'Installed', n: n((it) => it.installed) },
        { v: 'not', label: 'Not installed', n: n((it) => !it.installed) },
      ].filter((o) => o.n),
    },
    { key: 'needs', label: 'More filters', title: 'Needs a connection to', multi: true, selected: needs, onToggle: toggle(setNeeds), options: allNeeds.map((v) => ({ v, label: v, n: n((it) => needsOf(it).includes(v)) })) },
    { key: 'sort', label: 'Sort', view: true, selected: [sort], onToggle: (v) => setSort(v), options: [{ v: 'name', label: 'Name' }] },
  ]
  const problems = cat?.registry_problems || []
  if (hero) {
    const unreachable = !!err || !!cat?.registry_error
    return (
      <div class="cat" data-kinds="pack" data-mode="browse">
        <EmptyState
          hero
          testid="catalog-empty"
          art={<Icon name="extensions" />}
          title={unreachable ? "The catalog can't be reached" : 'The catalog has no packs yet'}
          action={{ label: 'Add a pack from its source', href: '#/packs/source', testid: 'catalog-source' }}
        >
          <p>
            {unreachable
              ? 'conductor couldn’t read the pack registry just now, so there is nothing to browse. Try again in a moment, or add a pack straight from its source: an owner/repo, a git URL or a folder on this machine.'
              : 'The registry conductor reads lists no packs yet. You can still add one straight from its source: an owner/repo, a git URL or a folder on this machine.'}
          </p>
        </EmptyState>
      </div>
    )
  }
  return (
    <div class="cat" data-kinds="pack" data-mode="browse">
      <FilterBar q={q} onQueryChange={setQ} placeholder="Find by name, what it does or a tag" ariaLabel="Find and filter the catalog" menus={menus} filterByHint />
      <p class="dl-sub cat-source">
        Not in the catalog?{' '}
        <a class="link" href="#/packs/source">
          Add a pack from a source or git URL
        </a>
      </p>
      {err ? (
        <div class="callout warn small" role="status">
          <Icon name="warn" />
          <span>The catalog couldn't be read just now ({err}). Try again in a moment, or add a pack from its source.</span>
        </div>
      ) : null}
      {cat?.registry_error ? (
        <div class="callout warn small" role="status" data-testid="registry-error">
          <Icon name="warn" />
          <span>The pack registry couldn't be reached, so only the packs you already have are listed. Try again in a moment, or add a pack from its source.</span>
        </div>
      ) : null}
      {problems.length ? (
        <div class="callout warn small" role="status">
          <Icon name="warn" />
          <span>
            {plural(problems.length, 'pack')} in the registry couldn't be read, so {problems.length === 1 ? "it isn't" : "they aren't"} listed: {problems.join('; ')}
          </span>
        </div>
      ) : null}
      <div class="cat-grid" id="cat-grid">
        {cat === null && !err ? (
          <div class="empty small">Loading the catalog…</div>
        ) : items.length ? (
          items.map((it) => <CatCard key={it.name + (it.pack_instance || '')} it={it} />)
        ) : (
          <div class="empty small">{all.length ? 'Nothing matches.' : 'The catalog has no packs to offer yet.'}</div>
        )}
      </div>
    </div>
  )
}

function CatCard({ it }: { it: CatalogPackInfo }): JSX.Element {
  const upd = it.installed && it.latest && cmpVer(it.latest, it.version) > 0 ? it.latest : ''
  const needs = needsOf(it)
  return (
    <div class={'cat-card' + (it.installed ? ' inst' : '')} data-cat={'pack:' + it.name} data-origin={originOf(it)}>
      <div class="cc-top">
        <span class="sc-ic sm">
          <Icon name="extensions" />
        </span>
        <b class="cc-name">{it.name}</b>
      </div>
      <div class="cc-desc">{it.description}</div>
      <div class="cc-meta">
        <FactsLine
          facts={[
            upd ? fact.update(upd) : null,
            fact.origin(it.official, srcId(it.source)),
            it.installed ? { icon: 'check', value: `installed ${it.version}`, pri: 1, label: 'Installed' } : null,
            fact.latest(it.latest || it.version),
            needs.length ? { icon: 'obj-connector', value: `uses ${needs.map(serviceName).join(', ')}`, pri: 2, label: 'The connections it needs' } : null,
          ]}
        />
      </div>
      <div class="cc-act card-acts">
        <a class="btn sm" href={`#/packs/item/${enc(it.name)}`}>
          Details
        </a>
        {!it.installed ? (
          <a class="btn sm secondary" href={`#/packs/install/${enc(it.name)}`}>
            <Icon name="plus" />
            Add
          </a>
        ) : null}
      </div>
    </div>
  )
}

// ---------- a catalog pack's Details (catalog.js detail) ----------

function NotInCatalog({ err }: { err?: string }): JSX.Element {
  return (
    <div class="pk-page">
      <div class="empty">
        <span class="serif">{err ? "The catalog couldn't be read" : 'Not in the catalog'}</span>
        {err ? <p>{err}</p> : null}
        <a class="btn" href="#/packs/browse">
          Browse
        </a>
      </div>
    </div>
  )
}

function Crumbs({ items }: { items: [string, string?][] }): JSX.Element {
  return (
    <nav class="dl-crumbs" aria-label="Where you are">
      {items.map(([l, h], i) => (
        <>
          {i ? (
            <span class="sep" aria-hidden="true">
              ›
            </span>
          ) : null}
          {i === items.length - 1 && !h ? (
            <span class="cur" aria-current="page">
              {l}
            </span>
          ) : (
            <a href={h}>{l}</a>
          )}
        </>
      ))}
    </nav>
  )
}

function PackDetails({ name }: { name: string }): JSX.Element {
  const { cat, err } = useCatalog()
  const { packs } = usePacks()
  const [toast, setToast] = useState<{ title: string; sub: string } | null>(null)
  const actions = usePackActions(setToast)
  if (!cat) return err ? <NotInCatalog err={err} /> : <div class="pk-page"><p class="muted">Loading…</p></div>
  const it = cat.packs.find((x) => x.name === name && !x.template_only) || cat.packs.find((x) => x.name === name)
  if (!it) return <NotInCatalog />
  const rv = it.review
  const p = it.installed ? packs?.find((x) => x.name === it.pack_instance) : undefined
  const upd = it.installed && it.latest && cmpVer(it.latest, it.version) > 0 ? it.latest : ''
  const c = it.pack_instance ? packView(it.pack_instance) || {} : {}
  return (
    <div class="pk-page">
      <div class="page-head">
        <div>
          <Crumbs items={[['Packs', '#/packs'], ['Browse', '#/packs/browse'], [it.name]]} />
          <h1>{it.name}</h1>
          <p class="sub">Pack · {it.official ? 'official registry' : 'custom source'}</p>
        </div>
        <div class="actions">
          {it.installed ? (
            <HeadActions
              items={[
                upd && p ? { label: `Update to ${upd}`, icon: 'updates', primary: true, onClick: () => actions.showPlan(p, it.latest) } : null,
                { label: 'Configure', icon: 'edit', href: `#/packs/pack/${enc(it.pack_instance || it.name)}` },
                { label: c.hold ? 'Let it update again' : 'Disable / hold', onClick: () => void actions.hold(it.pack_instance || it.name) },
                { label: 'Remove', cls: 'secondary', onClick: () => actions.askRemove(it.pack_instance || it.name) },
              ]}
            />
          ) : (
            <a class="btn primary" href={`#/packs/install/${enc(it.name)}`}>
              <Icon name="plus" />
              Install…
            </a>
          )}
        </div>
      </div>
      <div class="grid g-side">
        <div class="stack">
          <div class="panel">
            <div class="panel-body prose">
              <h2>{it.name}</h2>
              <p>{it.description}</p>
              <h3>Use</h3>
              <p>
                Install it as an instance; its triggers arrive <strong>disarmed</strong> until you arm them with a scope.
              </p>
            </div>
          </div>
          <div class="panel">
            <div class="panel-head">
              <h3>Versions &amp; changelog</h3>
            </div>
            <div class="list">
              <div class="item small">
                <b class="mono">{it.latest || it.version || '—'}</b>
                <span class="grow">Latest release</span>
                <span class="mono tiny muted" />
              </div>
              {it.installed && it.latest && it.latest !== it.version ? (
                <div class="item small">
                  <b class="mono">{it.version}</b>
                  <span class="grow">Installed</span>
                  <span class="mono tiny muted" />
                </div>
              ) : null}
            </div>
          </div>
        </div>
        <div class="stack">
          <div class="panel">
            <div class="panel-head">
              <h3>What it adds</h3>
            </div>
            <div class="panel-body small stack tight">
              <div>
                triggers:{' '}
                {rv?.triggers?.length
                  ? rv.triggers.map((t, i) => (
                      <>
                        {i ? ', ' : ''}
                        <code>{t.name}</code> ({t.connector || t.on}, disarmed)
                      </>
                    ))
                  : 'none'}
              </div>
              <div>
                workflows: <code>{rv?.workflows?.join(', ') || 'none'}</code>
              </div>
              {rv?.bots?.length ? (
                <div>
                  bots: <code>{rv.bots.join(', ')}</code>
                </div>
              ) : null}
            </div>
          </div>
          <div class="panel">
            <div class="panel-head">
              <h3>What it needs</h3>
            </div>
            <div class="panel-body small">
              {rv?.connectors?.length ? (
                <>
                  connector roles:{' '}
                  {rv.connectors.map((r, i) => (
                    <>
                      {i ? ', ' : ''}
                      <code>{r.name}</code>
                    </>
                  ))}
                </>
              ) : (
                'nothing'
              )}
              {rv?.secrets?.length ? <div>secrets: <code>{rv.secrets.join(', ')}</code></div> : null}
              {rv?.stores?.length ? <div>stores: <code>{rv.stores.join(', ')}</code></div> : null}
            </div>
          </div>
          <div class="panel">
            <div class="panel-head">
              <h3>What it's allowed to do</h3>
            </div>
            <div class="panel-body small">Its agents run in the agent sandbox. A pack can narrow isolation, never widen it.</div>
          </div>
          <div class="panel">
            <div class="panel-head">
              <h3>Source &amp; trust</h3>
            </div>
            <div class="panel-body small">
              <code>{it.install_ref || it.source}</code>
              <div style="margin-top:6px">
                {it.official ? (
                  <span class="tag" data-s="ok">official · trusted by default</span>
                ) : it.local ? (
                  <span class="tag" data-s="ok">local · yours, no trust entry needed</span>
                ) : (
                  <span class="tag" data-s="wait">third party · trusted by pack_trust</span>
                )}
              </div>
            </div>
          </div>
        </div>
      </div>
      {actions.modals}
      <Toast t={toast} onDone={() => setToast(null)} />
    </div>
  )
}

// The page head's actions; on a phone the ones beyond the first two (a
// primary first) fold into a ⋯ menu (ui.js afterRender's ph-more).
type HeadAction = { label: string; icon?: string; primary?: boolean; cls?: string; href?: string; onClick?: () => void }
function useNarrow(): boolean {
  const mq = typeof matchMedia === 'function' ? matchMedia('(max-width: 720px)') : null
  const [narrow, setNarrow] = useState(!!mq?.matches)
  useEffect(() => {
    if (!mq) return
    const f = () => setNarrow(mq.matches)
    mq.addEventListener('change', f)
    return () => mq.removeEventListener('change', f)
  }, [])
  return narrow
}
function HeadActions({ items }: { items: (HeadAction | null)[] }): JSX.Element {
  const xs = items.filter((x): x is HeadAction => !!x)
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLButtonElement>(null)
  const keep: HeadAction[] = xs.filter((x) => x.primary).slice(0, 1)
  for (const x of xs) if (keep.length < 2 && !keep.includes(x)) keep.push(x)
  const extra = xs.length > 2 ? xs.filter((x) => !keep.includes(x)) : []
  const go = (x: HeadAction) => (x.href ? (location.hash = x.href.replace(/^#/, '')) : x.onClick?.())
  return (
    <>
      {xs.map((x) => {
        const cls = 'btn' + (x.primary ? ' primary' : x.cls ? ' ' + x.cls : '') + (extra.includes(x) ? ' ph-folded' : '')
        const inner = (
          <>
            {x.icon ? <Icon name={x.icon} /> : null}
            {x.label}
          </>
        )
        return x.href ? (
          <a class={cls} href={x.href} key={x.label}>
            {inner}
          </a>
        ) : (
          <button class={cls} type="button" key={x.label} onClick={x.onClick}>
            {inner}
          </button>
        )
      })}
      {narrow && extra.length ? (
        <>
          <button ref={ref} type="button" class="btn ph-more" aria-label="More actions" onClick={() => setOpen((o) => !o)}>
            <Icon name="more" />
          </button>
          {open ? <Menu anchor={ref.current} items={extra.map((x) => ({ label: x.label, icon: 'chev', onClick: () => go(x) }))} onClose={() => setOpen(false)} label="More actions" /> : null}
        </>
      ) : null}
    </>
  )
}

// ---------- Install: the review before Add stages (catalog.js install) ----------

type Ver = 'latest' | 'major' | 'exact' | 'branch'

// The packs:<instance> block an install writes, for the version choice.
function installBlock(key: string, ref: string, fetch: string, local: boolean, latest: string, ver: Ver, branch: string): Record<string, any> {
  const major = String(latest).replace(/^v/, '').split('.')[0]
  if (local || ver === 'latest' || !latest) return { [key]: ref }
  if (ver === 'branch') return { source: (key === 'source' ? ref : fetch) + '@' + (branch || 'main') }
  const v = ver === 'major' ? `^${major}` : `=${latest}` // a bare version would mean >=
  // version: works for use: and source: alike (a use: …@suffix reaches git
  // as a raw ref, which a monorepo's <pack>/vX.Y.Z tags never match)
  return { [key]: ref, version: v }
}

const blockWords = (b: Record<string, any>) =>
  Object.entries(b)
    .map(([k, v]) => `${k}: ${v}`)
    .join(', ')

function InstallPack({ name }: { name: string }): JSX.Element {
  const { cat, err } = useCatalog()
  if (!cat) return err ? <NotInCatalog err={err} /> : <div class="pk-page"><p class="muted">Loading…</p></div>
  const it = cat.packs.find((x) => x.name === name && !x.installed) || cat.packs.find((x) => x.name === name)
  if (!it || !it.review) return <NotInCatalog />
  return (
    <InstallForm
      key={it.name}
      title={it.name}
      sub={`Pack · ${it.official ? 'official registry' : 'custom source (trusted)'}`}
      crumbs={[['Packs', '#/packs'], [it.name, `#/packs/item/${enc(it.name)}`], ['Install']]}
      back={`#/packs/item/${enc(it.name)}`}
      rv={it.review}
      installKey={it.install_key || 'use'}
      installRef={it.install_ref || it.source || ''}
      fetch={it.install_ref || it.source || ''}
      local={!!it.local}
      latest={it.version}
      taken={cat.instances || []}
    />
  )
}

function InstallForm(props: {
  title: string
  sub: string
  crumbs: [string, string?][]
  back: string
  rv: PackReview
  installKey: string
  installRef: string
  fetch: string
  local: boolean
  latest: string
  taken: string[]
}): JSX.Element {
  const { rv, local, latest } = props
  const [inst, setInst] = useState(() => {
    const base = rv.name.replace(/-team$|-import$/, '') || rv.name
    if (!props.taken.includes(base)) return base
    let i = 2
    while (props.taken.includes(`${base}-${i}`)) i++
    return `${base}-${i}`
  })
  const [ver, setVer] = useState<Ver>(local || !latest ? 'latest' : 'major')
  const [branch, setBranch] = useState('main')
  const [bind, setBind] = useState<Record<string, string>>({})
  const [arm, setArm] = useState<Record<string, boolean>>({})
  const [repos, setRepos] = useState<Record<string, string>>({})
  const [err, setErr] = useState('')
  const [instErr, setInstErr] = useState('')
  const major = String(latest).replace(/^v/, '').split('.')[0]
  const block = installBlock(props.installKey, props.installRef, props.fetch, local, latest, ver, branch)
  const armed = (rv.triggers || []).filter((t) => arm[t.name])
  const boundOf = (role: string) => bind[role] || ((rv.connectors || []).find((r) => r.name === role)?.candidates?.length === 1 ? rv.connectors!.find((r) => r.name === role)!.candidates![0] : '') || role
  const writes = (rv.grants || []).flatMap((g) => g.verbs || []).filter((x) => !/get_\*|watch$/.test(x)).map((x) => {
    const [role, ...verb] = x.split('.')
    return verb.length ? `${boundOf(role)}.${verb.join('.')}` : x
  })
  const uniqWrites = Array.from(new Set(writes))
  const nw = (rv.workflows || []).length
  const lock = `packs:\n  ${inst || rv.name}:\n    ${Object.entries(block).filter(([k]) => k !== 'version').map(([k, v]) => `${k}: ${v}`).join('\n    ')}\n    version: ${latest || 'latest'}\n    requires: [${(rv.connectors || []).map((r) => r.name).join(', ')}]`
  const hint = local
    ? 'A folder on this machine: it changes when the folder does.'
    : { latest: 'Follows every release automatically (update.deps).', major: `Takes ${major}.x updates; a new major waits for you.`, exact: 'Pinned: never updates until you change it.', branch: 'Tracks a branch head; for testing.' }[ver]
  const off = local ? 'A folder on this machine has no versions to choose' : !latest ? 'Its manifest names no version' : ''

  async function review() {
    setErr('')
    setInstErr('')
    if (!/^[a-z][a-z0-9-]*$/.test(inst)) return setInstErr('lowercase, digits, dashes')
    if (props.taken.includes(inst)) return setInstErr('That name is taken.')
    const n: Record<string, any> = { ...block }
    const conns: Record<string, string> = {}
    for (const r of rv.connectors || []) {
      if ((r.candidates || []).length === 1) continue
      if (!bind[r.name]) return setErr(`Bind the ${r.name} role: choose a connection, or create one first.`)
      conns[r.name] = bind[r.name]
    }
    if (Object.keys(conns).length) n.connectors = conns
    const trg: Record<string, any> = {}
    for (const t of rv.triggers || []) {
      if (!arm[t.name]) continue
      const a: Record<string, any> = { enabled: true }
      const rs = (repos[t.name] || '').split(',').map((x) => x.trim()).filter(Boolean)
      if (rs.length) a.repos = rs
      trg[t.name] = a
    }
    if (Object.keys(trg).length) n.triggers = trg
    await stage(['packs', inst], n, `install ${rv.name} as packs.${inst}`)
    if (cfgError.value) return setErr(cfgError.value)
    toReview()
  }

  return (
    <div class="pk-page">
      <div class="page-head">
        <div>
          <Crumbs items={props.crumbs} />
          <h1>Install {props.title}</h1>
          <p class="sub">{props.sub}</p>
        </div>
      </div>
      <div class="panel">
        <div class="panel-body stack" id="iw-form">
          <div class="field">
            <label for="f-inst">
              Name <span class="req" aria-label="required">*</span>
            </label>
            <input class={'input mono' + (instErr ? ' invalid' : '')} id="f-inst" value={inst} onInput={(e) => setInst((e.target as HTMLInputElement).value)} />
            {instErr ? <div class="field-err">{instErr}</div> : null}
          </div>
          <div class="field">
            <label>Version</label>
            <div class="seg" role="group">
              {(
                [
                  ['latest', 'latest (tracks)', ''],
                  ['major', `^${major || '?'} (recommended)`, off],
                  ['exact', `exact ${latest || '?'}`, off],
                  ['branch', 'a branch', local ? off : ''],
                ] as [Ver, string, string][]
              ).map(([v, l, dis]) => (
                <button
                  type="button"
                  key={v}
                  aria-pressed={ver === v}
                  class={ver === v ? 'on' : ''}
                  aria-disabled={dis ? 'true' : undefined}
                  title={dis || undefined}
                  onClick={() => !dis && setVer(v)}
                >
                  {l}
                </button>
              ))}
            </div>
            {ver === 'branch' ? <input class="input mono" id="iw-branch" value={branch} aria-label="Branch" onInput={(e) => setBranch((e.target as HTMLInputElement).value)} /> : null}
            <div class="hint">
              {hint} Written as <code>{blockWords(block)}</code>.
            </div>
          </div>
          {(rv.connectors || []).map((r) => {
            const cands = r.candidates || []
            return cands.length === 1 ? (
              <div class="field" key={r.name}>
                <label>
                  The <b>{r.name}</b> role
                </label>
                <p class="small" style="margin:0">
                  Bound automatically to <code>{cands[0]}</code>, your only {r.name} connection. Nothing is written for it.
                </p>
              </div>
            ) : (
              <div class="field" key={r.name}>
                <label for={`iw-role-${r.name}`}>
                  Bind the <b>{r.name}</b> role
                </label>
                <select
                  class="select mono"
                  id={`iw-role-${r.name}`}
                  value={bind[r.name] || ''}
                  onChange={(e) => {
                    const v = (e.target as HTMLSelectElement).value
                    if (v === '__new__') {
                      location.hash = '#/connections'
                      return
                    }
                    setBind({ ...bind, [r.name]: v })
                  }}
                >
                  <option value="">Choose a connection…</option>
                  {cands.map((c) => (
                    <option value={c} key={c}>
                      {c}
                    </option>
                  ))}
                  <option value="__new__">+ New {r.name} connector…</option>
                </select>
                <div class="hint">
                  {cands.length ? `You have ${cands.length} ${r.name} connections, so the choice is yours.` : `You have no ${r.name} connection yet.`} A pack reaches only what it's bound to.
                </div>
              </div>
            )
          })}
          <div class="caps">Turn on what starts it</div>
          <div class="muted tiny">Its triggers come in off. Turning one on is your consent; a trigger that acts on repos must name the repos it may act on.</div>
          {(rv.triggers || []).length ? (
            (rv.triggers || []).map((t) => (
              <div class="arm-row" key={t.name}>
                <label class="row small">
                  <input type="checkbox" class="cbx" id={`iw-arm-${t.name}`} checked={!!arm[t.name]} onChange={(e) => setArm({ ...arm, [t.name]: (e.target as HTMLInputElement).checked })} /> arm{' '}
                  <code>{t.name}</code> <span class="muted">({t.connector || t.on})</span>
                </label>
                <input
                  class="input mono"
                  id={`iw-repos-${t.name}`}
                  value={repos[t.name] || ''}
                  placeholder="repos, e.g. acme/api, acme/*"
                  aria-label={`Repos for ${t.name}`}
                  disabled={!arm[t.name]}
                  onInput={(e) => setRepos({ ...repos, [t.name]: (e.target as HTMLInputElement).value })}
                />
              </div>
            ))
          ) : (
            <div class="muted tiny">It ships no triggers: nothing starts it on its own.</div>
          )}
          <div class="panel pk-plan">
            <div class="panel-head">
              <h3>What installing adds</h3>
            </div>
            <div class="panel-body stack tight small">
              <div>
                <b>{plural(rv.agent_steps || 0, 'agent step')}</b>, {plural(nw, 'workflow')}
                {rv.bots?.length ? `, bots ${rv.bots.join(', ')}` : ''}; triggers arrive disarmed
                {armed.length ? (
                  <>
                    , and you are arming{' '}
                    {armed.map((t, i) => (
                      <>
                        {i ? ', ' : ''}
                        <code>{t.name}</code>
                      </>
                    ))}
                  </>
                ) : null}
                .
              </div>
              {uniqWrites.length ? (
                <div class="callout warn small grants-loud">
                  <Icon name="warn" />{' '}
                  <span>
                    <b>It asks to:</b>{' '}
                    {uniqWrites.map((x, i) => (
                      <>
                        {i ? ', ' : ''}
                        {x.split('.').slice(-1)[0].replace(/_/g, ' ')} <code>({x})</code>
                      </>
                    ))}
                    . They run only from an armed trigger, on that trigger's repos.{' '}
                    {armed.length ? "You're arming a trigger: that's a widening, so applying needs your passkey." : 'With nothing armed, installing widens nothing (a plain Apply); arming a trigger later asks for your passkey.'}
                  </span>
                </div>
              ) : (
                <div class="muted">Read-only: it asks for no actions that change anything.</div>
              )}
              {rv.secrets?.length || rv.stores?.length ? (
                <div>
                  It also needs {rv.secrets?.length ? <>secrets <code>{rv.secrets.join(', ')}</code></> : null}
                  {rv.secrets?.length && rv.stores?.length ? ' and ' : ''}
                  {rv.stores?.length ? <>stores <code>{rv.stores.join(', ')}</code></> : null}: bind them on its page once it's added.
                </div>
              ) : null}
              <details>
                <summary>Lockfile change (conductor.lock.yaml)</summary>
                <Yaml text={lock} />
              </details>
            </div>
          </div>
          {err ? <div class="form-err callout danger small" role="alert">{err}</div> : null}
          <div class="row">
            <a class="btn ghost" href={props.back}>
              Back
            </a>
            <button class="btn primary" style="margin-left:auto" type="button" onClick={() => void review()}>
              <Icon name="check" />
              Review everything it adds
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

// ---------- Add a pack from a source (system.js extensions/source/add) ----------

function PackFromSource(): JSX.Element {
  const [use, setUse] = useState('')
  const [read, setRead] = useState<PackReviewResponse | null>(null)
  const [inst, setInst] = useState('')
  const [bind, setBind] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const taken = Object.keys(((config.value?.tree as any) || {}).packs || {})

  async function readIt() {
    setBusy(true)
    setErr('')
    try {
      const r = await api.packReview({ source: use })
      setRead(r)
      setInst(r.instance)
      setBind({})
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  async function add() {
    if (!read) return
    setErr('')
    if (!/^[a-z][a-z0-9-]*$/.test(inst) || taken.includes(inst)) return setErr('Choose another name: lowercase letters, digits and dashes, not already used.')
    const n: Record<string, any> = { ...read.config_value }
    const conns: Record<string, string> = {}
    for (const r of read.review.connectors || []) if (bind[r.name]) conns[r.name] = bind[r.name]
    if (Object.keys(conns).length) n.connectors = conns
    await stage(['packs', inst], n, `install ${read.review.name} as packs.${inst}`)
    if (cfgError.value) return setErr(cfgError.value)
    toReview()
  }
  const m = read?.review
  return (
    <div class="pk-page">
      <Crumbs items={[['Packs', '#/packs'], ['Browse', '#/packs/browse'], ['A pack from a source']]} />
      <div class="dl-phead">
        <div>
          <h1>A pack from a source</h1>
          <p class="dl-lead">For a pack that isn’t in the official catalog: one of your own, or a team’s. conductor reads what it needs before anything is added.</p>
        </div>
      </div>
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Where it is</h2>
            </header>
            <div class="field">
              <label for="ps-use">Source</label>
              <input
                class="input mono"
                id="ps-use"
                value={use}
                placeholder="acme/conductor-packs/kit@^1.2"
                onInput={(e) => setUse((e.target as HTMLInputElement).value.trim())}
                onChange={() => setRead(null)}
                onKeyDown={(e) => e.key === 'Enter' && use && void readIt()}
              />
              <div class="hint">
                An owner/repo (optionally with a path and <code>@version</code>), or a local directory such as <code>./packs/house-style</code>. A source outside the official catalog needs a{' '}
                <code>pack_trust</code> entry.
              </div>
            </div>
            <button
              class="btn secondary"
              type="button"
              data-act="ps-read"
              aria-disabled={!use || busy ? 'true' : undefined}
              title={!use ? 'Enter where the pack is first' : undefined}
              onClick={() => use && !busy && void readIt()}
            >
              {busy ? 'Reading…' : 'Read its manifest'}
            </button>
            {err && !read ? <div class="form-err callout danger small" role="alert" style="margin-top:10px">{err}</div> : null}
          </section>
          {m && read ? (
            <section class="dl-card">
              <header class="dl-ch">
                <h2>{m.name}</h2>
                <span class="dl-sub">{m.version || 'latest'}</span>
              </header>
              <p class="dl-sub" style="margin-top:0">
                {m.description || 'Read from its manifest (conductor-pack.yaml).'}
              </p>
              <div class="field">
                <label for="ps-inst">Name it</label>
                <input class="input mono" id="ps-inst" value={inst} onInput={(e) => setInst((e.target as HTMLInputElement).value.trim())} />
                <div class="hint">
                  How it’s known in your config (<code>packs.{inst}</code>); change it to install the same pack twice.
                </div>
              </div>
              <h3 class="po-h3">Connections it needs</h3>
              <ul class="dl-list">
                {(m.connectors || []).length ? (
                  m.connectors!.map((r) => {
                    const fit = r.candidates || []
                    return (
                      <li class="po-row" key={r.name}>
                        <div class="dl-nt">
                          <b>{r.name}</b>
                        </div>
                        <label class="dl-search dl-select po-sel">
                          <select
                            id={`ps-role-${r.name}`}
                            aria-label={`Which ${r.name} connection`}
                            value={bind[r.name] ?? (fit.length === 1 ? fit[0] : '')}
                            onChange={(e) => {
                              const v = (e.target as HTMLSelectElement).value
                              if (v === '__new__') {
                                location.hash = '#/connections'
                                return
                              }
                              setBind({ ...bind, [r.name]: v })
                            }}
                          >
                            <option value="">{fit.length ? 'Choose a connection…' : `No ${r.name} connection yet`}</option>
                            {fit.map((c) => (
                              <option value={c} key={c}>
                                {c}
                              </option>
                            ))}
                            <option value="__new__">+ Create a {r.name} connection…</option>
                          </select>
                        </label>
                      </li>
                    )
                  })
                ) : (
                  <li class="dl-sub">None.</li>
                )}
              </ul>
              <h3 class="po-h3">What it adds</h3>
              <p class="dl-sub" style="margin:0">
                Workflows{' '}
                {(m.workflows || []).length
                  ? m.workflows!.map((w, i) => (
                      <>
                        {i ? ', ' : ''}
                        <code>{w}</code>
                      </>
                    ))
                  : 'none'}
                ; triggers{' '}
                {(m.triggers || []).length
                  ? m.triggers!.map((t, i) => (
                      <>
                        {i ? ', ' : ''}
                        <code>{t.name}</code>
                      </>
                    ))
                  : 'none'}
                , all disarmed until you arm them.
              </p>
              <h3 class="po-h3">What it may do</h3>
              <p class="dl-sub" style="margin:0">
                {(m.grants || []).length
                  ? m.grants!.map((g, i) => (
                      <>
                        {i ? '; ' : ''}
                        <code>{g.where}</code>: {[...(g.verbs || []), ...(g.secrets || []).map((s) => `secret ${s}`)].join(', ')}
                      </>
                    ))
                  : 'Read-only: it asks for no actions that change anything.'}
                {m.secrets?.length ? <> It needs the secrets <code>{m.secrets.join(', ')}</code>, bound on its page once it's added.</> : null}
              </p>
              {err ? <div class="form-err callout danger small" role="alert" style="margin-top:10px">{err}</div> : null}
              <div class="dl-actions">
                <button class="btn primary" type="button" onClick={() => void add()}>
                  Review the change
                </button>
              </div>
            </section>
          ) : null}
        </div>
        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Looking for a pack?</h2>
            </header>
            <p class="dl-sub" style="margin-top:0">
              The official catalog is searchable and every pack in it is trusted by default.
            </p>
            <a class="btn secondary" href="#/packs/browse">
              Browse the catalog
            </a>
          </section>
        </div>
      </div>
    </div>
  )
}

// ---------- a pack's own page: Customize (system.js extensions/pack/:name) ----------

const OVERRIDABLE = ['connectors', 'secrets', 'steps', 'triggers', 'policy', 'models', 'settings', 'preset', 'folders', 'on', 'decide', 'stores', 'handoffs']

function PackCustomize({ name }: { name: string }): JSX.Element {
  const [man, setMan] = useState<PackManifestResponse | null>(null)
  const [err, setErr] = useState('')
  const [missing, setMissing] = useState(false)
  const [yamlOpen, setYamlOpen] = useState(false)
  const [yamlErr, setYamlErr] = useState('')
  useEffect(() => {
    api
      .packManifest(name)
      .then(setMan)
      .catch((e) => {
        if (/no pack instance|not.found|404/i.test(String(e?.message || e) + humanError(e))) setMissing(true)
        setErr(humanError(e))
      })
  }, [name])
  const live = (config.value?.tree as any)?.packs?.[name]
  if (missing || (config.value && !live && !packView(name))) {
    return (
      <div class="pk-page">
        <div class="empty">
          <span class="serif">There is no pack called {name}</span>
          <a class="btn" href="#/packs">
            Packs
          </a>
        </div>
      </div>
    )
  }
  const c = packView(name) || {}
  const isStaged = staged.value.some((s) => s.path[0] === 'packs' && s.path[1] === name)
  const save = async (path: string[], v: unknown, summary: string) => {
    if (v === undefined) await stageDelete(['packs', name, ...path], summary)
    else await stage(['packs', name, ...path], v, summary)
  }
  const roles = man?.connectors || []
  const conns: Record<string, any> = c.connectors || {}
  const trigs: Record<string, any> = c.triggers || {}
  const steps: Record<string, any> = c.steps || {}
  const current: [string, string, string][] = [
    ...Object.entries(conns).map(([k, v]) => ['connectors', k, `the ${k} role → ${v}`] as [string, string, string]),
    ...Object.entries(trigs)
      .filter(([, v]) => v && v.enabled)
      .map(([k, v]) => ['triggers', k, `${k} armed${v.repos?.length ? ' on ' + v.repos.join(', ') : ''}`] as [string, string, string]),
    ...Object.entries(steps).map(([k, v]) => ['steps', k, `${k}: ${Object.keys(v || {}).join(', ')}`] as [string, string, string]),
    ...(c.policy ? [['policy', '', 'policy changed'] as [string, string, string]] : []),
    ...(c.models ? [['models', '', 'models changed'] as [string, string, string]] : []),
  ]
  const yamlNow = (() => {
    const o = Object.fromEntries(Object.entries(c).filter(([k]) => !['use', 'source', 'version', 'hold'].includes(k)))
    return Object.keys(o).length ? stringifyYAML(o) : ''
  })()
  const onYaml = async (text: string) => {
    setYamlErr('')
    let v: any
    try {
      v = parseYAML(text || '') || {}
    } catch (e: any) {
      return setYamlErr(`Not valid YAML: ${e?.message || e}`)
    }
    if (typeof v !== 'object' || Array.isArray(v)) return setYamlErr('Not valid YAML: write the block as keys and values')
    const bad = Object.keys(v).filter((k) => !OVERRIDABLE.includes(k))
    if (bad.length) return setYamlErr(`${bad[0]}: a pack can’t be overridden there`)
    const stepNames = (man?.steps || []).map((s) => s.ref)
    const bs = Object.keys(v.steps || {}).filter((k) => !stepNames.includes(k))
    if (bs.length) return setYamlErr(`steps.${bs[0]}: the pack has no step by that name (it has ${stepNames.join(', ') || 'none'})`)
    const bt = Object.keys(v.triggers || {}).filter((k) => k !== '*' && !(man?.triggers || []).some((t) => t.name === k))
    if (bt.length) return setYamlErr(`triggers.${bt[0]}: the pack has no trigger by that name`)
    const keep = Object.fromEntries(Object.entries(c).filter(([k]) => ['use', 'source', 'version', 'hold'].includes(k)))
    await stage(['packs', name], { ...keep, ...v }, `customize packs.${name}`)
    if (cfgError.value) setYamlErr(cfgError.value)
  }
  const reset = (k: string, x: string) => void save(x ? [k, x] : [k], undefined, `reset packs.${name}.${k}${x ? '.' + x : ''} to the pack's`)

  return (
    <div class="pk-page">
      <Crumbs items={[['Packs', '#/packs'], [name, `#/packs/item/${enc(name)}`], ['Customize']]} />
      <div class="dl-phead">
        <div>
          <h1>Customize {name}</h1>
          <p class="dl-lead">
            What you change about this pack lives in your config, never in its files, and survives its updates. A pack can only narrow what it does; anything that loosens it is marked and asks for your passkey when you apply.
          </p>
        </div>
        <div class="dl-actions tight">
          {isStaged ? (
            <a class="btn primary" href="#/system/config">
              Review the change
            </a>
          ) : null}
        </div>
      </div>
      {err && !missing ? <div class="banner err" role="alert">Couldn't read this pack: {err}</div> : null}
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Connections it needs</h2>
            </header>
            {!man ? (
              <p class="dl-sub">{err ? 'Not known until its manifest can be read.' : 'Loading…'}</p>
            ) : roles.length ? (
              <ul class="dl-list">
                {roles.map((r) => {
                  const fit = r.candidates || []
                  const cur = conns[r.name] as string | undefined
                  return (
                    <li class="po-row" key={r.name}>
                      <div class="dl-nt">
                        <b>{r.name}</b>
                        <span class="dl-why">
                          {cur ? (
                            `bound to ${cur}`
                          ) : fit.length === 1 ? (
                            `bound automatically to ${fit[0]}, your only ${r.name} connection`
                          ) : fit.length ? (
                            <span class="po-warn">
                              <StatusMark s="wait" mkOnly /> {fit.length} {r.name} connections: choose one, or the pack won’t load
                            </span>
                          ) : (
                            <span class="po-warn">
                              <StatusMark s="wait" mkOnly /> No {r.name} connection: the triggers that need it stay dormant
                            </span>
                          )}
                        </span>
                      </div>
                      <label class="dl-search dl-select po-sel">
                        <select
                          aria-label={`Which ${r.name} connection`}
                          value={cur || ''}
                          onChange={(e) => {
                            const v = (e.target as HTMLSelectElement).value
                            if (v === '__new__') {
                              location.hash = '#/connections'
                              return
                            }
                            void save(['connectors', r.name], v || undefined, v ? `bind packs.${name} ${r.name} → ${v}` : `unbind packs.${name} ${r.name}`)
                          }}
                        >
                          <option value="">{fit.length === 1 ? `Automatic (${fit[0]})` : fit.length ? 'Choose a connection…' : `No ${r.name} connection yet`}</option>
                          {fit.map((cn) => (
                            <option value={cn} key={cn}>
                              {cn}
                            </option>
                          ))}
                          <option value="__new__">+ Create a {r.name} connection…</option>
                        </select>
                      </label>
                    </li>
                  )
                })}
              </ul>
            ) : (
              <p class="dl-sub">It needs no connection.</p>
            )}
          </section>
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Triggers</h2>
              <span class="dl-sub">they arrive disarmed; arming one with its repos is your consent</span>
            </header>
            <ul class="dl-list">
              {(man?.triggers || []).length ? (
                man!.triggers!.map((t) => {
                  const tc = trigs[t.name] || {}
                  const on = !!tc.enabled
                  return (
                    <li class="po-row" key={t.name}>
                      <div class="dl-nt">
                        <b>{t.name}</b>
                        <span class="dl-why">{on ? (tc.repos?.length ? `armed on ${tc.repos.join(', ')}` : 'armed') : 'disarmed, as the pack ships it'}</span>
                        {on ? (
                          <input
                            class="input mono po-repos"
                            value={(tc.repos || []).join(', ')}
                            placeholder="acme/app, acme/web"
                            aria-label={`Repos ${t.name} may run on`}
                            onChange={(e) => {
                              const rs = (e.target as HTMLInputElement).value.split(',').map((x) => x.trim()).filter(Boolean)
                              void save(['triggers', t.name], { ...tc, repos: rs }, `packs.${name}: ${t.name} repos`)
                            }}
                          />
                        ) : null}
                      </div>
                      <button
                        type="button"
                        role="switch"
                        class={'switch' + (on ? ' on' : '')}
                        aria-checked={on}
                        aria-label={`Arm ${t.name}`}
                        onClick={() => void save(['triggers', t.name], on ? undefined : { ...tc, enabled: true }, on ? `disarm packs.${name} ${t.name}` : `arm packs.${name} ${t.name}`)}
                      />
                    </li>
                  )
                })
              ) : (
                <li class="dl-sub">No triggers.</li>
              )}
            </ul>
          </section>
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Steps</h2>
              <span class="dl-sub">change a model, a prompt or isolation; the pack’s value is shown beside yours</span>
            </header>
            <ul class="dl-list">
              {(man?.steps || []).length ? (
                man!.steps!.map((st) => {
                  const [wf, ...rest] = st.ref.split('/')
                  const ovr = steps[st.ref]
                  return (
                    <li class="po-row" key={st.ref}>
                      <div class="dl-nt">
                        <b>
                          {wf} · {rest.join('/')}
                        </b>
                        <span class="dl-why">
                          {ovr
                            ? `your override: ${Object.entries(ovr)
                                .map(([k, v]) => `${k} ${typeof v === 'object' ? JSON.stringify(v) : v}`)
                                .join(', ')}`
                            : 'as the pack wrote it'}
                        </span>
                      </div>
                      <span class="dl-na">
                        <a class="btn sm secondary" href={`#/workflows/${enc(`${name}/${wf}`)}`}>
                          Override in the builder
                        </a>
                        {ovr ? (
                          <button class="btn sm ghost" type="button" onClick={() => reset('steps', st.ref)}>
                            Reset
                          </button>
                        ) : null}
                      </span>
                    </li>
                  )
                })
              ) : (
                <li class="dl-sub">Nothing to override.</li>
              )}
            </ul>
          </section>
        </div>
        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Your overrides</h2>
              <span class="dl-sub">{current.length}</span>
            </header>
            {current.length ? (
              <ul class="dl-list">
                {current.map(([k, x, say]) => (
                  <li class="po-row" key={k + x}>
                    <span>{say}</span>
                    <button class="btn sm ghost" type="button" onClick={() => reset(k, x)}>
                      Reset to the pack’s
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p class="dl-sub">None: it runs exactly as the pack ships.</p>
            )}
          </section>
          <details class="dl-card dl-tech-d po-yaml" open={yamlOpen} onToggle={(e) => setYamlOpen((e.target as HTMLDetailsElement).open)}>
            <summary>
              <Icon name="chev" />
              Edit as YAML{' '}
              <span class="dl-sub">
                the same block, <code>packs.{name}</code>
              </span>
            </summary>
            <textarea class="textarea mono" rows={10} id="po-y" aria-label={`packs.${name} as YAML`} value={yamlNow} onChange={(e) => void onYaml((e.target as HTMLTextAreaElement).value)} />
            <p class="dl-sub" id="po-y-err">
              {yamlErr}
            </p>
          </details>
        </div>
      </div>
    </div>
  )
}
