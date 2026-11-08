// Plugins: what conductor can use, in four tabs (Connections, Agent runtimes,
// Code languages, Machines). Ported line for line from the prototype's
// js/screens/system.js `connectors/:tab?` route: one flat row per thing, its
// name, how it is in words, its facts (service, origin, version policy,
// version), and one ⋯ menu for everything you can do with it; "Add a …" opens
// the catalog picker (PluginForms.tsx). Every change is staged, reviewed as a
// diff, and applied with a passkey where it widens.
//
// Status is said only as far as conductor knows it: there is no connector
// health probe, so a connection reads "Working" from the runs it started
// today and "Can't run yet" when its plugin isn't loaded; a machine is "Not
// checked yet" (nothing probes hosts). Updating a plugin and choosing one of
// its releases run on the conductor machine (conductor plugin update); the
// menu says so instead of pretending.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import type { AddReview, CatalogItem, ConnectionsResponse, ConnectorRow, HostInfo, LanguageInfo, PluginRef, RuntimeRow } from '../api/types'
import { FactsLine, type Fact } from '../components/Facts'
import { EmptyState } from '../components/EmptyState'
import { FilterBar } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { StatusMark } from '../components/StatusMark'
import { TabsBar } from '../components/TabsBar'
import { cfgError, config, loadConfig, stage, stageDelete, staged } from '../configStore'
import { go, seg } from '../router'
import { useHashFlag } from '../lib/hashFlag'
import { Confirm, ConnForm, HostForm, LanguageSheet, Permissions, Picker, RangeForm, RuntimeForm, SourceForm, pluginFact, type ConnTarget, type Kind } from './PluginForms'

type Tab = 'connectors' | 'runtimes' | 'languages' | 'machines'

type Sheet =
  | { t: 'pick'; kind: Kind }
  | { t: 'source'; kind: Kind }
  | { t: 'conn'; target: ConnTarget }
  | { t: 'rt'; name?: string; use0: string; review?: AddReview | null }
  | { t: 'host'; name?: string }
  | { t: 'lang'; r: AddReview }
  | { t: 'perm'; name: string; lines: string[] }
  | { t: 'range'; coll: string; name: string; plugin: string; ver: string }
  | { t: 'confirm'; title: string; body: string; ok: string; onOk: () => void }

const UPDATE_WHY = (p: string) => `Updating runs on the conductor machine: conductor plugin update ${p}`
const CHOOSE_WHY = (p: string) => `Choose a release on the conductor machine: conductor plugin update ${p}@VERSION`

const baseOf = (use: string) => (use.lastIndexOf('@') > 0 ? use.slice(0, use.lastIndexOf('@')) : use)
const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

function tree(): Record<string, Record<string, Record<string, unknown>>> {
  return (config.value?.tree ?? {}) as Record<string, Record<string, Record<string, unknown>>>
}

// stagedTag mirrors system.js stagedTag: what's staged for one entry.
function stagedOf(coll: string, name: string): 'new' | 'edit' | 'removal' | '' {
  const xs = staged.value.filter((c) => c.path[0] === coll && c.path[1] === name)
  if (!xs.length) return ''
  if (xs.some((c) => c.delete && c.path.length === 2)) return 'removal'
  return tree()[coll]?.[name] === undefined ? 'new' : 'edit'
}
function StagedTag({ coll, name }: { coll: string; name: string }): JSX.Element | null {
  const s = stagedOf(coll, name)
  return s ? <span class="tag" data-s="run">{s === 'removal' ? 'removal staged' : s === 'new' ? 'new · staged' : 'edit staged'}</span> : null
}

// policyFacts is a plugin's origin, version policy and version (UI.pluginFact).
function policyFacts(p: PluginRef): (Fact | null)[] {
  return [
    pluginFact.waiting(p.waiting),
    pluginFact.origin(p.origin, p.source),
    p.origin !== 'builtin' ? pluginFact.policy(p.policy, p.version, p.constraint) : null,
    p.origin !== 'builtin' ? (p.version ? pluginFact.version(p.version) : { icon: 'tag', value: 'Not installed yet', label: 'Fetched when the change applies' }) : null,
  ]
}

export function Connections(): JSX.Element {
  const [data, setData] = useState<ConnectionsResponse | null>(null)
  const [err, setErr] = useState('')
  const [sheet, setSheet] = useState<Sheet | null>(null)
  const [menu, setMenu] = useState<{ el: HTMLElement; items: MenuItem[]; label: string } | null>(null)
  const [q, setQ] = useState('')
  const [svc, setSvc] = useState<string[]>([])
  const [status, setStatus] = useState<string[]>([])
  const [group, setGroup] = useState('none')
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [rtKinds, setRtKinds] = useState<string[]>([])
  const [connCat, setConnCat] = useState<CatalogItem[]>([])

  const load = () => api.connections().then(setData).catch((e) => setErr(humanError(e)))
  useEffect(() => {
    void load()
    void loadConfig()
    api.pluginCatalog('runtime').then((r) => setRtKinds((r.items ?? []).map((i) => i.name))).catch(() => {})
    api.pluginCatalog('connector').then((r) => setConnCat(r.items ?? [])).catch(() => {})
  }, [])

  const tab = ((): Tab => {
    const s = seg(1)
    return s === 'runtimes' || s === 'languages' || s === 'machines' ? s : 'connectors'
  })()

  const conns = data?.connectors ?? []
  const rts = data?.runtimes ?? []
  const langs = data?.languages ?? []
  const hosts = data?.hosts ?? []

  // After staging, go to the review (UI.staged): the change is applied there.
  const staged1 = () => go('system/config')
  const open = (el: EventTarget | null, label: string, items: MenuItem[]) => setMenu({ el: el as HTMLElement, items, label })

  // ---------- Connections ----------

  const stOf = (c: ConnectorRow) => (c.unavailable ? 'attn' : c.runs_today > 0 ? 'ok' : 'idle')
  const say = (c: ConnectorRow) =>
    c.unavailable
      ? `Can’t run yet: ${c.unavailable}`
      : c.disabled
        ? 'Switched off'
        : c.runs_today > 0
          ? `Working: ${plural(c.runs_today, 'run')} came from it today`
          : 'Set up; no runs from it today'

  const svcTok = useMemo(() => [...q.matchAll(/(?:^|\s)service:(\S+)/g)].map((m) => m[1]), [q])
  const words = q.replace(/(?:^|\s)service:\S+/g, ' ').trim().toLowerCase()
  const keepC = (c: ConnectorRow) => {
    const sv = [...svc, ...svcTok]
    return (!sv.length || sv.includes(c.plugin)) && (!status.length || status.includes(stOf(c))) && (!words || `${c.name} ${c.plugin} ${c.use}`.toLowerCase().includes(words))
  }
  const shownC = conns.filter(keepC).sort((a, b) => (stOf(a) === 'attn' ? 0 : 1) - (stOf(b) === 'attn' ? 0 : 1) || a.name.localeCompare(b.name))
  const tally = (xs: string[]) => {
    const m = new Map<string, number>()
    xs.forEach((x) => m.set(x, (m.get(x) ?? 0) + 1))
    return [...m].map(([v, n]) => ({ v, label: v, n }))
  }

  async function configure(c: ConnectorRow) {
    const it = connCat.find((x) => x.name === c.plugin)
    setSheet({ t: 'conn', target: { use: c.use, label: c.plugin, name: c.name, cur: tree().connectors?.[c.name] ?? null, fields: it?.fields ?? [], hasSchema: !!it?.has_schema } })
  }
  async function setUse(coll: string, name: string, use: string, why: string) {
    await stage([coll, name, 'use'], use, `${coll}.${name}: ${why}`)
    if (!cfgError.value) staged1()
  }
  const removeIt = (coll: string, name: string, what: string, body: string) =>
    setSheet({
      t: 'confirm', title: `Remove ${what} ${name}?`, body, ok: `Stage removing ${name}`,
      onOk: async () => {
        await stageDelete([coll, name], `remove ${coll}.${name}`)
        if (!cfgError.value) staged1()
      },
    })
  const pluginItems = (coll: string, name: string, p: PluginRef): MenuItem[] => {
    if (p.origin === 'builtin') return []
    const base = baseOf(p.use)
    return [
      '-',
      { label: 'Update now', icon: 'circle-arrow-up', disabled: UPDATE_WHY(p.plugin) },
      { label: 'Hold at this version', icon: 'pin', disabled: !p.version ? 'Nothing is installed yet to hold' : p.policy === 'pinned' ? 'Held already' : false, onClick: () => void setUse(coll, name, `${base}@${p.version}`, `hold at ${p.version}`) },
      { label: 'Stay current', icon: 'refresh-cw', disabled: p.policy === 'current' ? 'It takes every release already' : false, onClick: () => void setUse(coll, name, base, 'take every release') },
      { label: 'Stay within…', icon: 'brackets', disabled: !p.version ? 'Nothing is installed yet to start from' : false, onClick: () => setSheet({ t: 'range', coll, name, plugin: base, ver: p.version! }) },
      { label: 'Choose a version…', icon: 'tag', disabled: CHOOSE_WHY(p.plugin) },
      { label: 'Show the plugin’s permissions', icon: 'shield', disabled: p.permissions?.length ? false : 'Its plugin isn’t installed yet', onClick: () => setSheet({ t: 'perm', name: p.plugin, lines: p.permissions ?? [] }) },
    ]
  }
  const connMenu = (c: ConnectorRow): MenuItem[] => [
    { label: 'Configure…', icon: 'settings', onClick: () => void configure(c) },
    ...pluginItems('connectors', c.name, c),
    '-',
    {
      label: 'Remove this connection…', icon: 'trash',
      onClick: () => removeIt('connectors', c.name, 'the connection', c.bots?.length ? `${plural(c.bots.length, 'bot')} (${c.bots.join(', ')}) use it; their verbs on it stop working. It’s staged like any change.` : 'It’s staged like any change; nothing changes until you apply it.'),
    },
  ]

  const cnRow = (c: ConnectorRow) => {
    const s = stOf(c)
    return (
      <li class={'dl-conn conn-row cn-row' + (s === 'attn' ? ' attn' : '')} data-s={s === 'attn' ? 'fail' : s} data-c={c.name} key={c.name}>
        <label class="cn-sel">
          <input type="checkbox" class="cbx" checked={sel.has(c.name)} aria-label={`Select ${c.name}`}
            onChange={(e) => { const n = new Set(sel); (e.target as HTMLInputElement).checked ? n.add(c.name) : n.delete(c.name); setSel(n) }} />
        </label>
        <span class="dl-nt">
          <b>{c.name} <StagedTag coll="connectors" name={c.name} /></b>
          <span class={s === 'attn' ? 'reason' : 'dl-why'}>
            {say(c)}
            {c.bots?.length ? <> · <span class="dl-sub" title={`reached by ${c.bots.join(', ')}`}>used by {plural(c.bots.length, 'bot')}</span></> : null}
          </span>
          <FactsLine facts={[pluginFact.service(c.plugin), ...policyFacts(c)]} />
        </span>
        <span class="dl-na">
          {s === 'attn' ? <button class="btn sm secondary" type="button" data-act="conn-edit" data-c={c.name} onClick={() => void configure(c)}>Fix it…</button> : null}
          <button class="icon-btn" type="button" data-act="conn-more" data-c={c.name} aria-label={`More for ${c.name}`} onClick={(e) => open(e.currentTarget, c.name, connMenu(c))}>
            <Icon name="more" />
          </button>
        </span>
      </li>
    )
  }
  const empty = (s: string) => <li class="dl-empty-row dl-sub">{s}</li>
  const cnList = (xs: ConnectorRow[]) => (
    <ul class="dl-list dl-conns cn-list">
      {xs.length ? xs.map(cnRow) : empty(conns.length ? 'No connection matches.' : 'No connections yet. A connection is how conductor talks to a service: add one to start.')}
    </ul>
  )
  const newStaged = (coll: string, live: string[]) =>
    staged.value
      .filter((c) => c.path[0] === coll && c.path.length === 2 && !c.delete && !live.includes(c.path[1]))
      .map((c) => (
        <div class="item bot-card" style="border-style:dashed" key={c.id} data-staged={c.path[1]}>
          <span class="conn-name"><b>{c.path[1]}</b> <span class="item-id">{coll === 'hosts' ? (c.value as { host?: string })?.host : String((c.value as { use?: string })?.use ?? '').split('@')[0]}</span></span>
          <span class="grow"><span class="tag" data-s="run">new · staged</span> <span class="muted small">Not live until you apply it.</span></span>
          <a class="btn sm" href="#/system/config">Review</a>
        </div>
      ))

  const bulkHold = async () => {
    for (const n of sel) {
      const c = conns.find((x) => x.name === n)
      if (c && c.origin !== 'builtin' && c.version && c.policy !== 'pinned') await stage(['connectors', n, 'use'], `${baseOf(c.use)}@${c.version}`, `connectors.${n}: hold at ${c.version}`)
    }
    setSel(new Set())
    if (!cfgError.value) staged1()
  }
  const holdable = [...sel].some((n) => { const c = conns.find((x) => x.name === n); return c && c.origin !== 'builtin' && c.version && c.policy !== 'pinned' })

  const services = (
    <>
      <FilterBar
        q={q}
        onQueryChange={setQ}
        placeholder="Find a connection, or filter: service:github"
        ariaLabel="Find and filter connections"
        filterByHint
        menus={[
          { key: 'svc', label: 'Service', multi: true, selected: svc, options: tally(conns.map((c) => c.plugin)), onToggle: (v) => setSvc(svc.includes(v) ? svc.filter((x) => x !== v) : [...svc, v]), onClear: () => setSvc([]) },
          {
            key: 'status', label: 'Status', multi: true, selected: status,
            options: ([['attn', 'Needs a look'], ['ok', 'Working'], ['idle', 'Not used yet']] as const).map(([v, label]) => ({ v, label, n: conns.filter((c) => stOf(c) === v).length })).filter((o) => o.n),
            onToggle: (v) => setStatus(status.includes(v) ? status.filter((x) => x !== v) : [...status, v]), onClear: () => setStatus([]),
          },
          { key: 'group', label: 'Group by', view: true, selected: [group], options: [{ v: 'none', label: 'None' }, { v: 'svc', label: 'Service' }], onToggle: setGroup },
        ]}
      />
      {sel.size ? (
        <div class="m-bulk cn-bulk">
          <b>{sel.size} selected</b>
          <button class="btn sm secondary" type="button" data-act="cn-bulk-update" disabled title={UPDATE_WHY('NAME')}>Update now</button>
          <button class="btn sm secondary" type="button" data-act="cn-bulk-hold" disabled={!holdable} title={holdable ? undefined : 'None of these has a plugin version to hold'} onClick={() => void bulkHold()}>Hold at their versions</button>
          <button class="btn sm ghost" type="button" data-act="cn-bulk-none" onClick={() => setSel(new Set())}>Clear</button>
        </div>
      ) : null}
      {group === 'svc' && shownC.length ? (
        <div class="tk-groups">
          {[...new Set(shownC.map((c) => c.plugin))].map((u) => {
            const first = shownC.find((c) => c.plugin === u)!
            return (
              <section class="dl-card dl-flush cn-group" key={u}>
                <header class="dl-ch cn-gh"><h2>{u}</h2><span class="dl-sub">{pluginFact.origin(first.origin).value}</span></header>
                {cnList(shownC.filter((c) => c.plugin === u))}
              </section>
            )
          })}
          {newStaged('connectors', conns.map((c) => c.name))}
        </div>
      ) : (
        <div class="panel">
          {cnList(shownC)}
          {newStaged('connectors', conns.map((c) => c.name))}
        </div>
      )}
    </>
  )

  // ---------- the other three tabs ----------

  const plRow = (o: { key: string; name: string; glyph: string; staged?: JSX.Element | null; bad?: boolean; say: JSX.Element | string; facts: (Fact | null)[]; act: string; attrs: Record<string, string>; items: MenuItem[] }) => (
    <li class={'dl-conn conn-row pl-row' + (o.bad ? ' attn' : '')} data-k={o.key} key={o.key}>
      <span class="dl-ng">{o.bad ? <StatusMark s="fail" lg mkOnly label="Needs a look" /> : <Icon name={o.glyph} />}</span>
      <span class="dl-nt">
        <b>{o.name} {o.staged}</b>
        <span class={o.bad ? 'reason' : 'dl-why'}>{o.say}</span>
        <FactsLine facts={o.facts} />
      </span>
      <span class="dl-na">
        <button class="icon-btn" type="button" data-act={o.act} {...o.attrs} aria-label={`More for ${o.name}`} onClick={(e) => open(e.currentTarget, o.name, o.items)}>
          <Icon name="more" />
        </button>
      </span>
    </li>
  )

  const rtMenu = (r: RuntimeRow): MenuItem[] => [
    { label: 'Edit…', icon: 'edit', onClick: () => setSheet({ t: 'rt', name: r.name, use0: r.use }) },
    ...pluginItems('runtimes', r.name, r),
    '-',
    { label: 'Remove…', icon: 'trash', onClick: () => removeIt('runtimes', r.name, 'the runtime', r.bots?.length ? `${plural(r.bots.length, 'bot')} (${r.bots.join(', ')}) run on it and would need another runtime.` : 'No bot runs on it. It’s staged like any change.') },
  ]
  const runtimes = (
    <div class="panel">
      <ul class="dl-list dl-conns">
        {rts.length
          ? rts.map((r) =>
              plRow({
                key: r.name, name: r.name, glyph: r.tool || r.type === 'cli' ? 'terminal' : 'server', staged: <StagedTag coll="runtimes" name={r.name} />,
                bad: !!(r as RuntimeRow & { unavailable?: string }).unavailable,
                say: `${r.bots?.length ? `Used by ${plural(r.bots.length, 'bot')}` : 'Set up, not used yet'}${r.default ? ' · the default for new bots' : ''}`,
                facts: [
                  r.isolation === 'none' ? { icon: 'shield', value: 'Not sandboxed', tone: 'warning', label: 'Every agent it starts runs as you' } : { icon: 'obj-sandbox', value: r.isolation ? 'Sandboxed' : 'Default isolation', label: r.isolation || 'conductor’s sandbox setting applies' },
                  { icon: 'server', value: r.tool || r.type || r.plugin, label: 'Agent runtime' },
                  r.host ? { icon: 'obj-host', value: `on ${r.host}`, label: 'Runs its agents on this machine' } : null,
                  ...policyFacts(r),
                ],
                act: 'rt-more', attrs: { 'data-r': r.name }, items: rtMenu(r),
              }),
            )
          : empty('No agent runtimes yet. A runtime is what bots and agent steps run on: add one to start.')}
      </ul>
      {newStaged('runtimes', rts.map((r) => r.name))}
    </div>
  )

  const usedSay = (l: LanguageInfo) =>
    l.steps ? (
      <>
        Used by {plural(l.steps, 'workflow step')}, in{' '}
        {l.workflows.map((w, i) => <>{i ? ', ' : ''}<a href={`#/workflows/${encodeURIComponent(w)}`} key={w}>{w}</a></>)}
      </>
    ) : (
      'Not used by any workflow yet'
    )
  const langMenu = (l: LanguageInfo): MenuItem[] => {
    const where: MenuItem = { label: 'Show where it’s used', icon: 'workflows', disabled: l.workflows.length ? false : 'No workflow uses it yet', onClick: () => go(`workflows/${encodeURIComponent(l.workflows[0])}`) }
    if (l.class === 'builtin') return [where, '-', { label: 'Built into conductor', icon: 'dot', disabled: 'It ships with conductor and updates with it' }]
    if (l.class === 'host') return [where, '-', { label: 'A program on this machine', icon: 'terminal', disabled: 'conductor runs the copy installed on this machine' }]
    return [
      where,
      '-',
      { label: 'Update now', icon: 'circle-arrow-up', disabled: UPDATE_WHY(l.plugin) },
      { label: 'Show the plugin’s permissions', icon: 'shield', disabled: l.permissions?.length ? false : 'Its plugin isn’t installed yet', onClick: () => setSheet({ t: 'perm', name: l.plugin, lines: l.permissions ?? [] }) },
      '-',
      { label: 'Remove…', icon: 'trash', disabled: l.steps ? 'Code steps use it: change or remove those steps first' : 'Nothing names it, so there’s nothing to remove' },
    ]
  }
  const languages = (
    <div class="panel">
      <ul class="dl-list dl-conns">
        {langs.length
          ? langs.map((l) =>
              plRow({
                key: l.name, name: l.name, glyph: l.class === 'plugin' ? 'code' : 'terminal', say: usedSay(l),
                facts: l.class === 'host' ? [{ icon: 'terminal', value: 'On this machine', label: 'A program installed on this machine' }, l.hardened ? { icon: 'shield', value: 'Hardened', label: 'Has an engines: entry' } : null] : [...policyFacts(l), l.class === 'builtin' ? { icon: 'tag', value: l.version || data?.version || '', label: 'Ships with this conductor' } : null],
                act: 'lang-more', attrs: { 'data-p': l.name, 'data-builtin': l.class === 'builtin' ? '1' : '' }, items: langMenu(l),
              }),
            )
          : empty('No code languages yet.')}
      </ul>
    </div>
  )

  const hostMenu = (h: HostInfo): MenuItem[] => [
    { label: 'Test the connection', icon: 'plugtest', disabled: 'conductor doesn’t check machines ahead of time; a step that runs there reports whether it could sign in' },
    { label: 'Edit…', icon: 'edit', onClick: () => setSheet({ t: 'host', name: h.name }) },
    '-',
    { label: 'Remove…', icon: 'trash', onClick: () => removeIt('hosts', h.name, 'the machine', h.runtimes?.length ? `The runtime${h.runtimes.length > 1 ? 's' : ''} ${h.runtimes.join(', ')} run there and would need another machine.` : 'Nothing runs there. It’s staged like any change.') },
  ]
  const machines = (
    <div class="panel">
      <ul class="dl-list dl-conns">
        {hosts.length
          ? hosts.map((h) =>
              plRow({
                key: h.name, name: h.name, glyph: 'obj-host', staged: <StagedTag coll="hosts" name={h.name} />,
                say: `Not checked yet${h.runtimes?.length ? ` · runs ${h.runtimes.join(', ')}` : ''}`,
                facts: [{ icon: 'globe', value: h.port && h.port !== 22 ? `${h.host}:${h.port}` : h.host, label: 'Address' }, h.user ? { icon: 'members', value: `as ${h.user}`, label: 'Signs in as' } : null],
                act: 'host-more', attrs: { 'data-h': h.name }, items: hostMenu(h),
              }),
            )
          : empty('No machines yet. A machine is another computer conductor runs agents on, over SSH.')}
      </ul>
      {newStaged('hosts', hosts.map((h) => h.name))}
    </div>
  )

  // ---------- adding ----------

  const [act, addLabel] = ({ runtimes: ['rt-add', 'Add a runtime'], languages: ['lang-add', 'Add a language'], machines: ['host-add', 'Add a machine'] } as Record<string, [string, string]>)[tab] ?? ['conn-add', 'Add a connection']
  const add = () => (tab === 'machines' ? setSheet({ t: 'host' }) : setSheet({ t: 'pick', kind: tab === 'runtimes' ? 'runtime' : tab === 'languages' ? 'engine' : 'connector' }))
  // #/connections?add=1 (the ⌘K palette's Connect a service) opens the connection picker.
  useHashFlag('add', () => setSheet({ t: 'pick', kind: 'connector' }))

  async function picked(kind: Kind, it: CatalogItem) {
    try {
      const r = await api.pluginReview(kind, it.name)
      reviewed(kind, r, it)
    } catch (e) {
      setErr(humanError(e))
    }
  }
  function reviewed(kind: Kind, r: AddReview, it?: CatalogItem) {
    if (kind === 'engine') return setSheet({ t: 'lang', r })
    if (kind === 'runtime') return setSheet({ t: 'rt', use0: r.use, review: r })
    setSheet({ t: 'conn', target: { use: r.use, label: r.plugin, fields: r.fields ?? it?.fields ?? [], hasSchema: r.has_schema, review: r } })
  }

  // A tab whose list is empty shows the hero (fix brief: hero empty states):
  // it carries the one add action, so the header's button and the tab's count
  // step aside until there is at least one.
  const liveNames = { connectors: conns.map((c) => c.name), runtimes: rts.map((r) => r.name), machines: hosts.map((h) => h.name), languages: langs.map((l) => l.name) }
  const stagedNew = (coll: string, live: string[]) => staged.value.some((c) => c.path[0] === coll && c.path.length === 2 && !c.delete && !live.includes(c.path[1]))
  const isEmpty = (t: Tab) => !!data && !liveNames[t].length && (t === 'languages' || !stagedNew(t === 'machines' ? 'hosts' : t, liveNames[t]))
  const HERO: Record<Tab, { icon: string; title: string; say: string; label: string }> = {
    connectors: { icon: 'plug', title: 'Connect your first service', say: 'A connection is how conductor hears from a service and acts in it: a code host, a chat, a schedule. Its secrets stay in your vault; the config only points to them.', label: 'Connect a service' },
    runtimes: { icon: 'server', title: 'Add an agent runtime', say: 'A runtime is what your bots and agent steps run on: an agent tool on this machine, or on one of your machines.', label: 'Add a runtime' },
    languages: { icon: 'code', title: 'No code languages yet', say: 'A code language is what a workflow’s code steps run in.', label: 'Add a language' },
    machines: { icon: 'obj-host', title: 'Add a machine', say: 'A machine is another computer conductor can run agents on. It signs in over SSH with this machine’s own keys.', label: 'Add a machine' },
  }
  const hero = (t: Tab) => (
    <EmptyState hero testid={`plugins-empty-${t}`} art={<Icon name={HERO[t].icon} />} title={HERO[t].title} action={{ label: HERO[t].label, onClick: add, testid: `plugins-add-${t}` }}>
      <p>{HERO[t].say}</p>
    </EmptyState>
  )

  const close = () => setSheet(null)
  const done = () => { void load(); staged1() }
  const counts: Record<Tab, number> = { connectors: conns.length, runtimes: rts.length, languages: langs.length, machines: hosts.length }
  const TABS: [Tab, string][] = [['connectors', 'Connections'], ['runtimes', 'Agent runtimes'], ['languages', 'Code languages'], ['machines', 'Machines']]

  return (
    <div class="pl-page">
      <div class="page-head">
        <div>
          <h1>Plugins</h1>
          <p class="lede-2">What conductor can use: the connections to the services it talks to, and what it runs agents and code with.</p>
        </div>
        {isEmpty(tab) ? null : (
          <div class="actions">
            <button class="btn primary" type="button" data-act={act} onClick={add}><Icon name="plus" />{addLabel}</button>
          </div>
        )}
      </div>
      <TabsBar
        items={TABS.map(([id, label]) => ({ id, label, href: '#/connections/' + (id === 'connectors' ? '' : id), count: data && counts[id] ? counts[id] : null }))}
        active={tab}
        ariaLabel="Plugins"
      />
      {err ? <div class="banner err" role="alert">{err}</div> : null}
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      {!data && !err ? <p class="dl-sub">Loading…</p> : isEmpty(tab) ? hero(tab) : tab === 'runtimes' ? runtimes : tab === 'machines' ? machines : tab === 'languages' ? languages : services}
      <p class="muted small" style="margin-top:12px">Every change here is reviewed as a diff and validated on a copy before it applies.</p>

      {menu ? <Menu anchor={menu.el} items={menu.items} label={`More for ${menu.label}`} onClose={() => setMenu(null)} /> : null}
      {sheet?.t === 'pick' ? <Picker kind={sheet.kind} onClose={close} onSource={() => setSheet({ t: 'source', kind: sheet.kind })} onPick={(it) => void picked(sheet.kind, it)} /> : null}
      {sheet?.t === 'source' ? <SourceForm kind={sheet.kind} onClose={close} onReviewed={(r) => reviewed(sheet.kind, r)} /> : null}
      {sheet?.t === 'conn' ? <ConnForm t={sheet.target} taken={Object.keys(tree().connectors ?? {})} onClose={close} onStaged={done} /> : null}
      {sheet?.t === 'rt' ? (
        <RuntimeForm name={sheet.name} use0={sheet.use0} review={sheet.review} cur={sheet.name ? tree().runtimes?.[sheet.name] : null} kinds={rtKinds} hosts={Object.keys(tree().hosts ?? {})} taken={Object.keys(tree().runtimes ?? {})} onClose={close} onStaged={done} />
      ) : null}
      {sheet?.t === 'host' ? <HostForm name={sheet.name} cur={sheet.name ? tree().hosts?.[sheet.name] : null} taken={Object.keys(tree().hosts ?? {})} onClose={close} onStaged={done} /> : null}
      {sheet?.t === 'lang' ? <LanguageSheet r={sheet.r} onClose={close} /> : null}
      {sheet?.t === 'perm' ? <Permissions name={sheet.name} lines={sheet.lines} onClose={close} /> : null}
      {sheet?.t === 'range' ? <RangeForm coll={sheet.coll} name={sheet.name} plugin={sheet.plugin} ver={sheet.ver} onClose={close} onStaged={done} /> : null}
      {sheet?.t === 'confirm' ? <Confirm title={sheet.title} body={sheet.body} ok={sheet.ok} danger onOk={sheet.onOk} onClose={close} /> : null}
    </div>
  )
}
