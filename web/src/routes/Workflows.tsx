// Workflows (CONFORMANCE "Workflows": the chosen direction 1 — yours, then
// each pack as a section; a bot's own workflow isn't listed, it lives on the
// bot's page). A row: a select box, an on/off switch, the name, what it does,
// its facts (where it comes from, what starts it), its health this week, and a
// ⋯ menu (open, run now, duplicate, customize, delete). New workflow names it
// (triggered or reusable) and opens the builder on a draft.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { createPortal } from 'preact/compat'
import { parseDocument } from 'yaml'
import { api } from '../api/client'
import type { PackInfo, WorkflowSummary } from '../api/types'
import { wfApi } from '../api/wfbuilder'
import { plural } from '../lib/format'
import { EmptyState } from '../components/EmptyState'
import { FilterBar } from '../components/FilterBar'
import { FactsLine, type Fact } from '../components/Facts'
import { FormModal, ConfirmModal } from '../components/FormModal'
import { Menu, type MenuItem } from '../components/Menu'
import { Icon } from '../components/Icon'
import { ReviewPanel } from '../components/ReviewPanel'
import { humanError } from '../api/errors'
import { useHashFlag } from '../lib/hashFlag'
import { go } from '../router'
import { stage, stageDelete, staged, loadConfig, config, cfgError } from '../configStore'
import { draftNames, writeDraft } from './WorkflowDetail'

type HealthKey = 'failing' | 'some' | 'healthy' | 'none'
type Row = WorkflowSummary & { fresh?: boolean }

const NAME_RE = /^[a-z][a-z0-9-]*$/
const ownerOf = (w: Row): 'config' | 'pack' => (w.source?.startsWith('pack:') ? 'pack' : 'config')
const packOf = (w: Row) => (w.source || '').slice(5)
const srcKey = (w: Row) => (ownerOf(w) === 'pack' ? w.source! : 'config')
// a workflow's own node can be changed here: one of your named triggers (by
// its index in the config file) or one of your reusable workflows: entries
const editable = (w: Row) => ownerOf(w) === 'config' && !w.fresh && (w.kind === 'workflow' || w.index >= 0)
const hasSwitch = (w: Row) => !w.fresh && w.kind !== 'workflow'

export function Workflows(): JSX.Element {
  const [wfs, setWfs] = useState<Row[] | null>(null)
  const [packs, setPacks] = useState<PackInfo[]>([])
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [source, setSource] = useState<string[]>([])
  const [onOff, setOnOff] = useState<string[]>([])
  const [health, setHealth] = useState<string[]>([])
  const [sort, setSort] = useState<'name' | 'failing'>('name')
  const [group, setGroup] = useState<'source' | 'none'>('source')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [menu, setMenu] = useState<{ anchor: HTMLElement; w: Row } | null>(null)
  const [modal, setModal] = useState<{ k: 'new' } | { k: 'dup' | 'run' | 'del' | 'discard'; w: Row } | null>(null)
  const [note, setNote] = useState('')
  const [, bump] = useState(0)

  const load = () => {
    api
      .workflows()
      .then((r) => {
        setWfs(r.workflows ?? [])
        setErr('')
      })
      .catch((e) => setErr(humanError(e)))
    api.packs().then((r) => setPacks(r.packs ?? [])).catch(() => setPacks([]))
  }
  useEffect(() => {
    load()
    void loadConfig()
  }, [])
  useHashFlag('new', () => setModal({ k: 'new' }))

  const pendingWf = staged.value.filter((c) => c.path[0] === 'triggers' || c.path[0] === 'workflows').length

  // drafts (named in this browser, not saved yet) are listed with the rest
  const all: Row[] = [...(wfs || [])]
  draftNames()
    .filter((n) => !all.some((w) => w.name === n))
    .forEach((n) => all.push({ name: n, disabled: false, manual: false, index: -1, fresh: true, health: 'none', steps: 0, runs: 0 } as unknown as Row))

  // the search box also takes source:yours / source:<pack>, on:off, health:failing
  const parsed = useMemo(() => {
    const toks = q.trim().split(/\s+/).filter(Boolean)
    const f: Record<string, string[]> = {}
    const words: string[] = []
    toks.forEach((t) => {
      const m = t.match(/^(source|on|health):(.+)$/i)
      if (!m) return words.push(t.toLowerCase())
      const k = m[1].toLowerCase()
      const v = m[2].toLowerCase()
      ;(f[k] = f[k] || []).push(k === 'source' ? (v === 'yours' || v === 'config' ? 'config' : 'pack:' + v) : v)
    })
    return { f, text: words.join(' ') }
  }, [q])
  const keep = (w: Row) => {
    const src = [...source, ...(parsed.f.source || [])]
    const oo = [...onOff, ...(parsed.f.on || [])]
    const hh = [...health, ...(parsed.f.health || [])]
    if (src.length && !src.includes(srcKey(w))) return false
    if (oo.length && !oo.includes(w.disabled ? 'off' : 'on')) return false
    if (hh.length && !hh.includes(w.health || 'none')) return false
    if (parsed.text && !`${w.name} ${w.does || ''} ${w.when || ''} ${w.on || ''}`.toLowerCase().includes(parsed.text)) return false
    return true
  }
  const filtered = all.filter(keep)

  const order = (xs: Row[]): Row[] =>
    sort === 'failing'
      ? [...xs].sort((a, b) => (b.health === 'failing' ? 1 : 0) - (a.health === 'failing' ? 1 : 0) || (b.failed_week || 0) - (a.failed_week || 0) || a.name.localeCompare(b.name))
      : [...xs].sort((a, b) => a.name.localeCompare(b.name))

  const setOn = async (w: Row, nowOn: boolean): Promise<boolean> => {
    if (ownerOf(w) !== 'config' || w.index < 0 || w.kind === 'workflow') return false
    await stage(['triggers', String(w.index), 'enabled'], nowOn, `${nowOn ? 'switch on' : 'switch off'} ${w.name}`)
    return true
  }
  const toggleOn = (w: Row, nowOn: boolean) => void setOn(w, nowOn)
  const bulk = async (on: boolean) => {
    const names = [...sel]
    let skipped = 0
    for (const n of names) {
      const w = all.find((x) => x.name === n)
      if (!w || !(await setOn(w, on))) skipped++
    }
    setSel(new Set())
    setNote(skipped ? `${plural(skipped, 'workflow')} left as they were: a pack’s workflow is switched on its pack’s page, and a reusable one runs only when another workflow calls it.` : '')
  }
  const pick = (name: string, on: boolean) =>
    setSel((cur) => {
      const n = new Set(cur)
      if (on) n.add(name)
      else n.delete(name)
      return n
    })

  const newModal =
    modal?.k === 'new' ? (
      <FormModal
        caps="new workflow"
        title="Name your workflow"
        fields={[
          { k: 'name', label: 'Name', type: 'string', req: true, ph: 'e.g. release-notify', pattern: NAME_RE, patternMsg: 'Lowercase letters, digits and dashes.', validate: (v) => (all.some((w) => w.name === v) ? 'A workflow by that name exists.' : '') },
          { k: 'kind', label: 'Kind', type: 'seg', options: [['trigger', 'Triggered (on: an event)'], ['workflow', 'Reusable (call: it)']], def: 'trigger' },
        ]}
        submit="Create"
        testid="wf-new"
        onClose={() => setModal(null)}
        onSubmit={(v) => {
          const kind = v.kind === 'workflow' ? 'workflow' : 'trigger'
          writeDraft(v.name, kind === 'workflow' ? `name: ${v.name}\ninputs:\n  repo: { type: string, required: true }\nsteps: []\n` : `name: ${v.name}\nsteps: []\n`, kind)
          setModal(null)
          go('workflows/' + encodeURIComponent(v.name))
        }}
      />
    ) : null

  if (wfs === null) {
    return (
      <div class="m-page">
        <div class="dl-phead"><div><h1>Workflows</h1></div></div>
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load workflows: {err} <button class="btn sm" type="button" onClick={() => load()}>Try again</button>
          </div>
        ) : (
          <p class="muted">Loading…</p>
        )}
      </div>
    )
  }

  if (all.length === 0) {
    return (
      <div class="m-page">
        <div class="dl-phead"><div><h1>Workflows</h1></div></div>
        <EmptyState
          hero
          testid="workflows-empty"
          art={<FlowGlyph />}
          title="No workflows yet"
          action={{ label: 'Build a workflow', href: '#/workflows?new=1', testid: 'workflows-empty-new' }}
          secondary={{ label: 'Get one from a pack', href: '#/packs' }}
        >
          <p>A workflow is a fixed recipe: when something happens, run these steps. Want an AI to decide what to do? Hand the event to a bot instead.</p>
        </EmptyState>
        {newModal ? createPortal(newModal, document.body) : null}
      </div>
    )
  }

  const mine = all.filter((w) => ownerOf(w) === 'config')
  const packSources = packs.filter((p) => all.some((w) => w.source === `pack:${p.name}`))
  const offCount = all.filter((w) => w.disabled).length
  const isOpen = (k: string, def: boolean) => (k in open ? open[k] : def)
  const big = all.length > 40
  const tally = (k: string) => all.filter((w) => srcKey(w) === k).length

  const bar = (
    <FilterBar
      q={q}
      onQueryChange={setQ}
      placeholder="Find a workflow, or filter: source:yours"
      ariaLabel="Find and filter workflows"
      filterByHint
      menus={[
        {
          key: 'source',
          label: 'Source',
          multi: true,
          selected: source,
          options: [{ v: 'config', label: 'Yours', n: tally('config') }, ...packSources.map((p) => ({ v: `pack:${p.name}`, label: `From ${p.name}`, n: tally(`pack:${p.name}`) }))].filter((o) => o.n),
          onToggle: (v) => setSource((cur) => (cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])),
          onClear: () => setSource([]),
        },
        {
          key: 'on',
          label: 'On or off',
          multi: true,
          selected: onOff,
          options: [
            { v: 'on', label: 'On', n: all.filter((w) => !w.disabled).length },
            { v: 'off', label: 'Off', n: offCount },
          ].filter((o) => o.n),
          onToggle: (v) => setOnOff((cur) => (cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])),
          onClear: () => setOnOff([]),
        },
        {
          key: 'health',
          label: 'Health',
          multi: true,
          selected: health,
          options: (['failing', 'some', 'healthy', 'none'] as HealthKey[])
            .map((k) => ({ v: k, label: { failing: 'Failing (20% or more)', some: 'Some failures', healthy: 'Healthy', none: 'No runs this week' }[k], n: all.filter((w) => (w.health || 'none') === k).length }))
            .filter((o) => o.n),
          onToggle: (v) => setHealth((cur) => (cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])),
          onClear: () => setHealth([]),
        },
        {
          key: 'sort',
          label: 'Sort',
          view: true,
          selected: [sort],
          options: [
            { v: 'name', label: 'Name' },
            { v: 'failing', label: 'Most failing first' },
          ],
          onToggle: (v) => setSort(v as 'name' | 'failing'),
        },
        {
          key: 'group',
          label: 'Group by',
          view: true,
          selected: [group],
          options: [
            { v: 'source', label: 'Source' },
            { v: 'none', label: 'None' },
          ],
          onToggle: (v: string) => setGroup(v as 'source' | 'none'),
        },
      ]}
    />
  )

  const row = (w: Row, withSource = false) => (
    <WorkflowRow key={w.name} w={w} pack={packs.find((p) => p.name === packOf(w))} withSource={withSource} selected={sel.has(w.name)} onSelect={pick} onToggleOn={toggleOn} onMore={(anchor) => setMenu({ anchor, w })} />
  )

  const menuItems = (w: Row): MenuItem[] => {
    const o = ownerOf(w)
    const runWhy = w.kind === 'workflow' ? 'Another workflow runs it with call:' : !w.manual ? `It starts on ${w.on || 'an event'}, not by hand: add manual to its on: to run it from here` : ''
    const items: (MenuItem | null)[] = [
      { label: 'Open in the builder', icon: 'canvas', onClick: () => go('workflows/' + encodeURIComponent(w.name)) },
      w.fresh ? null : { label: 'Run now…', icon: 'play', disabled: runWhy || false, onClick: () => setModal({ k: 'run', w }) },
      w.fresh ? null : { label: 'Duplicate…', icon: 'copy', onClick: () => setModal({ k: 'dup', w }) },
      o === 'pack' ? { label: 'Customize in its pack', icon: 'edit', onClick: () => go('packs/' + encodeURIComponent(packOf(w))) } : null,
      o === 'config' ? '-' : null,
      o === 'config' && !w.fresh
        ? { label: 'Delete…', icon: 'trash', disabled: editable(w) ? false : 'This trigger has no name: remove it in the config file', onClick: () => setModal({ k: 'del', w }) }
        : o === 'config'
          ? { label: 'Discard the draft…', icon: 'trash', onClick: () => setModal({ k: 'discard', w }) }
          : null,
    ]
    return items.filter((x): x is MenuItem => x != null)
  }

  return (
    <div class="m-page">
      <div class="dl-phead">
        <div>
          <h1>
            Workflows <span class="h1-count">{plural(all.length, 'workflow')} · {offCount} off{filtered.length < all.length ? ` · showing ${filtered.length}` : ''}</span>
          </h1>
          <p class="lede-2">
            A <a class="term" href="#/glossary">workflow</a> is a fixed recipe: when something happens, run these steps. Want an AI to decide what to do? Hand the event to a <a class="link" href="#/bots">bot</a>.
          </p>
        </div>
        <div class="dl-actions tight">
          <button class="btn primary" type="button" data-testid="new-workflow" onClick={() => setModal({ k: 'new' })}>
            <Icon name="plus" />New workflow
          </button>
        </div>
      </div>

      {err ? <div class="banner err" role="alert">Couldn't load workflows: {err}</div> : null}

      {bar}

      {sel.size ? (
        <div class="m-bulk">
          <b>{sel.size} selected</b>
          <button class="btn sm secondary" type="button" onClick={() => void bulk(true)}>Switch on</button>
          <button class="btn sm secondary" type="button" onClick={() => void bulk(false)}>Switch off</button>
          <button class="link" type="button" onClick={() => setSel(new Set())}>Clear</button>
        </div>
      ) : null}
      {note ? <div class="callout info small" role="status">{note}</div> : null}

      {group === 'none' ? (
        <section class="dl-card m-team">
          <ul class="m-list m-flat">{order(filtered).length ? order(filtered).map((w) => row(w, true)) : <li class="dl-empty-row">No workflow matches.</li>}</ul>
        </section>
      ) : (
        <>
          {!source.length || source.includes('config') ? (
            <Section
              k="config"
              title="Yours"
              sub={`${mine.length} in your config${mine.filter((w) => w.disabled).length ? ` · ${mine.filter((w) => w.disabled).length} off` : ''}`}
              open={isOpen('config', true)}
              onToggle={() => setOpen((o) => ({ ...o, config: !isOpen('config', true) }))}
              rows={order(mine.filter(keep)).map((w) => row(w))}
            />
          ) : null}
          {packSources
            .filter((p) => !source.length || source.includes(`pack:${p.name}`))
            .map((p, i) => {
              const xs = all.filter((w) => w.source === `pack:${p.name}`)
              return (
                <Section
                  key={p.name}
                  k={`pack:${p.name}`}
                  title={`From ${p.name}`}
                  sub={`${plural(xs.length, 'workflow')} · ${p.local ? 'your own pack' : `pack ${p.version || ''}`.trim()}${p.hold ? ' · updates held' : ''}`}
                  action={<a class="btn sm secondary" href={'#/packs/' + encodeURIComponent(p.name)}>Customize</a>}
                  open={isOpen(`pack:${p.name}`, !big && i < 2)}
                  onToggle={() => setOpen((o) => ({ ...o, [`pack:${p.name}`]: !isOpen(`pack:${p.name}`, !big && i < 2) }))}
                  rows={order(xs.filter(keep)).map((w) => row(w))}
                />
              )
            })}
        </>
      )}

      {pendingWf ? <ReviewPanel stagedCount={staged.value.length} /> : null}

      {createPortal(
        <>
      {menu ? <Menu anchor={menu.anchor} items={menuItems(menu.w)} onClose={() => setMenu(null)} /> : null}
      {newModal}
      {modal?.k === 'run' ? <RunModal w={modal.w} onClose={() => setModal(null)} /> : null}
      {modal?.k === 'dup' ? <DupModal w={modal.w} all={all} onClose={() => setModal(null)} /> : null}
      {modal?.k === 'del' ? (
        <ConfirmModal
          title={`Delete ${modal.w.name}?`}
          body="Stages removing it from config.yaml. Review the diff and apply with your passkey; nothing changes until then."
          ok="Stage removal"
          danger
          onClose={() => setModal(null)}
          onOk={async () => {
            const w = modal.w
            await stageDelete(w.kind === 'workflow' ? ['workflows', w.name] : ['triggers', String(w.index)], `delete workflow ${w.name}`)
            setModal(null)
          }}
        />
      ) : null}
      {modal?.k === 'discard' ? (
        <ConfirmModal
          title={`Discard the draft ${modal.w.name}?`}
          body="The draft lives only in this browser; it was never saved to config.yaml."
          ok="Discard"
          danger
          onClose={() => setModal(null)}
          onOk={() => {
            writeDraft(modal.w.name, null)
            setModal(null)
            bump((n) => n + 1)
          }}
        />
      ) : null}
        </>,
        document.body,
      )}
    </div>
  )
}

// RunModal starts a manual run (on: manual), the same dispatch `conductor run`
// makes, then opens the run.
function RunModal({ w, onClose }: { w: Row; onClose: () => void }): JSX.Element {
  return (
    <FormModal
      caps="manual run · control run"
      title={`Run ${w.name}`}
      intro="Runs it now with no event, the same as conductor run."
      fields={[]}
      submit="Run now"
      submitIcon="play"
      onClose={onClose}
      onSubmit={async () => {
        try {
          const r = await wfApi.run(w.name, {})
          onClose()
          if (r.run_id) go('runs/' + encodeURIComponent(r.run_id))
          return
        } catch (e) {
          return humanError(e)
        }
      }}
    />
  )
}

// DupModal stages a copy of a workflow in your config, switched off.
function DupModal({ w, all, onClose }: { w: Row; all: Row[]; onClose: () => void }): JSX.Element {
  return (
    <FormModal
      caps={`a copy of ${w.name}`}
      title={`Duplicate ${w.name}`}
      values={{ name: `${w.name.replace(/^.*\//, '')}-copy` }}
      fields={[{ k: 'name', label: 'Name', type: 'string', req: true, pattern: NAME_RE, patternMsg: 'Lowercase letters, digits and dashes.', validate: (x) => (all.some((y) => y.name === x) ? 'A workflow by that name exists.' : '') }]}
      note={w.kind === 'workflow' ? 'The copy is yours, in your config.' : 'The copy is yours, in your config, switched off until you switch it on.'}
      submit="Stage the copy"
      onClose={onClose}
      onSubmit={async (v) => {
        try {
          const tree = (config.value?.tree || {}) as Record<string, unknown>
          if (w.kind === 'workflow') {
            const own = (tree.workflows as Record<string, unknown> | undefined)?.[w.name]
            const node = own ?? parseDocument((await api.workflow(w.name)).yaml || '').toJS()
            const copy = JSON.parse(JSON.stringify(node ?? {}))
            delete copy.name
            delete copy.PackInstance
            await stage(['workflows', v.name], copy, `new workflow ${v.name}, a copy of ${w.name}`)
          } else {
            const list = Array.isArray(tree.triggers) ? (JSON.parse(JSON.stringify(tree.triggers)) as unknown[]) : []
            const node = ownerOf(w) === 'config' && w.index >= 0 ? list[w.index] : parseDocument((await api.workflow(w.name)).yaml || '').toJS()
            const copy = JSON.parse(JSON.stringify(node ?? {})) as Record<string, unknown>
            const out: Record<string, unknown> = { name: v.name, enabled: false }
            Object.entries(copy).forEach(([k, x]) => {
              if (k !== 'name' && k !== 'enabled') out[k] = x
            })
            list.push(out)
            await stage(['triggers'], list, `new workflow ${v.name}, a copy of ${w.name}`)
          }
          if (cfgError.value) return cfgError.value
          onClose()
          return
        } catch (e) {
          return humanError(e)
        }
      }}
    />
  )
}

function Section({ k, title, sub, action, open, onToggle, rows }: { k: string; title: string; sub: string; action?: JSX.Element; open: boolean; onToggle: () => void; rows: JSX.Element[] }): JSX.Element {
  return (
    <section class="dl-card m-team" data-src={k}>
      <header class="m-team-h">
        <button type="button" class="m-tog" aria-expanded={open} aria-label={(open ? 'Fold ' : 'Show ') + title} onClick={onToggle}>
          <Icon name={open ? 'chevd' : 'chev'} />
        </button>
        <div class="m-team-t">
          <h2>{title}</h2>
          <span class="dl-sub">{sub}</span>
        </div>
        <span class="m-st" />
        <div class="m-team-a">{action}</div>
      </header>
      {open ? <ul class="m-list">{rows.length ? rows : <li class="dl-empty-row">No workflow here matches the filter.</li>}</ul> : null}
    </section>
  )
}

function WorkflowRow({
  w,
  pack,
  withSource,
  selected,
  onSelect,
  onToggleOn,
  onMore,
}: {
  w: Row
  pack?: PackInfo
  withSource: boolean
  selected: boolean
  onSelect: (name: string, on: boolean) => void
  onToggleOn: (w: Row, on: boolean) => void
  onMore: (anchor: HTMLElement) => void
}): JSX.Element {
  const on = !w.disabled
  const o = ownerOf(w)
  const starts = w.fresh ? 'Nothing starts it yet' : w.when || ''
  const src: Fact | null = !withSource
    ? null
    : o === 'pack'
      ? { icon: 'obj-pack', value: `${packOf(w)}${pack?.version && !pack.local ? ' ' + pack.version : ''}`, href: `#/packs/${encodeURIComponent(packOf(w))}`, pri: 1, label: 'From a pack' }
      : { icon: 'edit', value: 'yours', pri: 1, label: 'In your config' }
  const switchWhy = o === 'pack' ? 'Switch it on or off on its pack’s page' : w.index < 0 ? 'This trigger has no name: switch it in the config file' : ''
  return (
    <li class={'m-row' + (on ? '' : ' off')} data-wf={w.name}>
      <span class="m-sel">
        <input type="checkbox" class="cbx m-cbx" aria-label={`Select ${w.name}`} checked={selected} onChange={(e) => onSelect(w.name, e.currentTarget.checked)} />
      </span>
      <span class="m-sw">
        {hasSwitch(w) ? (
          <button
            type="button"
            role="switch"
            class={'switch' + (on ? ' on' : '')}
            aria-checked={on}
            aria-label={`${on ? 'Switch off' : 'Switch on'} ${w.name}`}
            {...(switchWhy ? { 'aria-disabled': 'true', 'data-reason': switchWhy, 'data-tip': switchWhy } : {})}
            onClick={() => !switchWhy && onToggleOn(w, !on)}
          />
        ) : null}
      </span>
      <a class="m-main" href={'#/workflows/' + encodeURIComponent(w.name)}>
        <b>{w.name}</b>
        <span class="dl-why">{w.fresh ? 'A draft in this browser' : w.does || 'runs its steps'}</span>
        <FactsLine
          facts={[
            src,
            { icon: /schedule|every|cron|Mondays|night|day at/i.test(starts) ? 'clock' : 'zap', value: starts, pri: 1, label: 'What starts it' },
            w.fresh ? { icon: 'edit', value: 'draft, not applied', pri: 1 } : null,
          ]}
        />
      </a>
      <span class="m-st">
        <HealthMark w={w} />
      </span>
      <button type="button" class="icon-btn" aria-label={`More for ${w.name}`} onClick={(e) => onMore(e.currentTarget)}>
        <Icon name="more" />
      </button>
    </li>
  )
}

function HealthMark({ w }: { w: Row }): JSX.Element {
  const strip = (w.last20 || []).length >= 3 ? (
    <span class="wf-strip" aria-hidden="true">
      {[...(w.last20 || [])].reverse().map((s) => <i data-s={s === 'ok' ? undefined : s === 'failed' ? 'fail' : s === 'running' ? 'run' : 'idle'} />)}
    </span>
  ) : null
  const pct = w.runs_week ? Math.round(((w.failed_week || 0) / w.runs_week) * 100) : 0
  return (
    <a class={'q-st wf-health' + (w.health === 'failing' ? ' fail' : '')} href={`#/activity?wf=${encodeURIComponent(w.name)}&since=7d`} title="Its runs in Activity">
      {strip}
      <span>{w.runs_week ? `${plural(w.runs_week, 'run')} this week · ${pct}% failed` : 'No runs this week'}</span>
    </a>
  )
}

// FlowGlyph is a trigger feeding two steps: the empty state's picture.
function FlowGlyph(): JSX.Element {
  return (
    <svg width="72" height="44" viewBox="0 0 72 44" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" stroke-linecap="round">
      <circle cx="10" cy="22" r="6" />
      <rect x="28" y="6" width="16" height="12" rx="3" opacity="0.8" />
      <rect x="28" y="26" width="16" height="12" rx="3" opacity="0.55" />
      <rect x="54" y="16" width="16" height="12" rx="3" opacity="0.55" />
      <path d="M16 22h6M22 22V12h6M22 22v10h6M44 12h4v10h6M44 32h4v-10" opacity="0.7" />
    </svg>
  )
}
