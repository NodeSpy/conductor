// Workflows (CONFORMANCE "Workflows": the chosen direction 1 — yours, then
// each pack as a section; a bot's own workflow isn't listed, it lives on the
// bot's page). A row: an on/off switch, the name, what it does, what starts
// it, and its health this week (runs and the share failed), linking to
// Activity. Health filter, Most failing first sort. New workflow opens the
// builder.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PackInfo, WorkflowSummary } from '../api/types'
import { plural } from '../lib/format'
import { EmptyState } from '../components/EmptyState'
import { FilterBar } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { ReviewPanel } from '../components/ReviewPanel'
import { humanError } from '../api/errors'
import { stage, staged, loadConfig } from '../configStore'

type HealthKey = 'failing' | 'some' | 'healthy' | 'none'

export function Workflows(): JSX.Element {
  const [wfs, setWfs] = useState<WorkflowSummary[] | null>(null)
  const [packs, setPacks] = useState<PackInfo[]>([])
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [onOff, setOnOff] = useState<string[]>([])
  const [health, setHealth] = useState<string[]>([])
  const [sort, setSort] = useState<'name' | 'failing'>('name')
  const [group, setGroup] = useState<'source' | 'none'>('source')
  const [open, setOpen] = useState<Record<string, boolean>>({})

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

  const pendingTriggers = staged.value.filter((c) => c.path[0] === 'triggers').length

  const all = wfs || []
  const filtered = useMemo(() => {
    return all.filter((w) => {
      if (onOff.length && !onOff.includes(w.disabled ? 'off' : 'on')) return false
      if (health.length && !health.includes(w.health || 'none')) return false
      if (q && !`${w.name} ${w.does || ''} ${w.when || ''} ${w.on || ''}`.toLowerCase().includes(q.toLowerCase())) return false
      return true
    })
  }, [all, q, onOff, health])

  const order = (xs: WorkflowSummary[]): WorkflowSummary[] =>
    sort === 'failing'
      ? [...xs].sort((a, b) => (b.health === 'failing' ? 1 : 0) - (a.health === 'failing' ? 1 : 0) || (b.failed_week || 0) - (a.failed_week || 0) || a.name.localeCompare(b.name))
      : [...xs].sort((a, b) => a.name.localeCompare(b.name))

  const toggleOn = (name: string, nowOn: boolean) => {
    const w = all.find((x) => x.name === name)
    if (!w || w.index < 0) return
    void stage(['triggers', String(w.index), 'enabled'], nowOn, `${nowOn ? 'switch on' : 'switch off'} ${name}`)
  }

  if (wfs === null) {
    return (
      <div class="col m-page">
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
      <div class="col m-page">
        <div class="dl-phead"><div><h1>Workflows</h1></div></div>
        <EmptyState
          testid="workflows-empty"
          art={<FlowGlyph />}
          title="No workflows yet"
          action={{ label: 'Build a workflow', href: '#/workflows/new', testid: 'workflows-empty-new' }}
          secondary={{ label: 'Get one from a pack', href: '#/packs' }}
        >
          <p>A workflow is a fixed recipe: when something happens, run these steps. Want an AI to decide what to do? Hand the event to a bot instead.</p>
        </EmptyState>
      </div>
    )
  }

  const mine = all.filter((w) => !w.source)
  const packSources = packs.filter((p) => all.some((w) => w.source === `pack:${p.name}`))
  const offCount = all.filter((w) => w.disabled).length
  const isOpen = (k: string, def: boolean) => (k in open ? open[k] : def)

  const bar = (
    <FilterBar
      q={q}
      onQueryChange={setQ}
      placeholder="Find a workflow"
      ariaLabel="Find and filter workflows"
      menus={[
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
        ...(packSources.length
          ? [
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
            ]
          : []),
      ]}
    />
  )

  return (
    <div class="col m-page">
      <div class="dl-phead">
        <div>
          <h1>Workflows <span class="h1-count">{plural(all.length, 'workflow')}{offCount ? ` · ${offCount} off` : ''}{filtered.length < all.length ? ` · showing ${filtered.length}` : ''}</span></h1>
          <p class="lede-2">A workflow is a fixed recipe: when something happens, run these steps. Want an AI to decide what to do? Hand the event to a <a class="link" href="#/bots">bot</a>.</p>
        </div>
        <div class="dl-actions tight">
          <a class="btn primary" href="#/workflows/new" data-testid="new-workflow"><Icon name="plus" />New workflow</a>
        </div>
      </div>

      {err ? <div class="banner err" role="alert">Couldn't load workflows: {err}</div> : null}

      {all.length > 1 ? bar : null}

      {group === 'none' ? (
        <section class="dl-card m-team">
          <ul class="m-list m-flat">
            {order(filtered).length ? order(filtered).map((w) => <WorkflowRow key={w.name} w={w} onToggleOn={toggleOn} />) : <li class="dl-empty-row">No workflow matches.</li>}
          </ul>
        </section>
      ) : (
        <>
          <Section
            k="config"
            title="Yours"
            sub={`${mine.length} in your config${mine.filter((w) => w.disabled).length ? ` · ${mine.filter((w) => w.disabled).length} off` : ''}`}
            open={isOpen('config', true)}
            onToggle={() => setOpen((o) => ({ ...o, config: !isOpen('config', true) }))}
            rows={order(mine.filter((w) => filtered.includes(w)))}
            onToggleOn={toggleOn}
          />
          {packSources.map((p) => {
            const xs = all.filter((w) => w.source === `pack:${p.name}`)
            return (
              <Section
                key={p.name}
                k={`pack:${p.name}`}
                title={`From ${p.name}`}
                sub={`${plural(xs.length, 'workflow')}${p.version ? ` · pack ${p.version}` : ''}${p.hold ? ' · updates held' : ''}`}
                action={<a class="btn sm secondary" href="#/packs">Customize</a>}
                open={isOpen(`pack:${p.name}`, true)}
                onToggle={() => setOpen((o) => ({ ...o, [`pack:${p.name}`]: !isOpen(`pack:${p.name}`, true) }))}
                rows={order(xs.filter((w) => filtered.includes(w)))}
                onToggleOn={toggleOn}
              />
            )
          })}
        </>
      )}

      {pendingTriggers ? <ReviewPanel stagedCount={staged.value.length} /> : null}
    </div>
  )
}

function Section({
  k,
  title,
  sub,
  action,
  open,
  onToggle,
  rows,
  onToggleOn,
}: {
  k: string
  title: string
  sub: string
  action?: JSX.Element
  open: boolean
  onToggle: () => void
  rows: WorkflowSummary[]
  onToggleOn: (name: string, on: boolean) => void
}): JSX.Element {
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
      {open ? (
        <ul class="m-list">
          {rows.length ? rows.map((w) => <WorkflowRow key={w.name} w={w} onToggleOn={onToggleOn} />) : <li class="dl-empty-row">No workflow here matches the filter.</li>}
        </ul>
      ) : null}
    </section>
  )
}

function WorkflowRow({ w, onToggleOn }: { w: WorkflowSummary; onToggleOn: (name: string, on: boolean) => void }): JSX.Element {
  const on = !w.disabled
  const facts: string[] = []
  if (w.when) facts.push(w.when)
  if (w.index < 0) facts.push('read-only here')
  return (
    <li class={'m-row' + (on ? '' : ' off')} data-wf={w.name}>
      <span class="m-sel" />
      <span class="m-sw">
        {w.index >= 0 ? (
          <button type="button" role="switch" class={'switch' + (on ? ' on' : '')} aria-checked={on} aria-label={`${on ? 'Switch off' : 'Switch on'} ${w.name}`} onClick={() => onToggleOn(w.name, !on)} />
        ) : null}
      </span>
      <a class="m-main" href={'#/workflows/' + encodeURIComponent(w.name)}>
        <b>{w.name}</b>
        <span class="dl-why">{w.does || 'runs its steps'}</span>
        <span class="fx">{facts.map((f) => <span class="fx-i">{f}</span>)}</span>
      </a>
      <span class="m-st"><HealthMark w={w} /></span>
    </li>
  )
}

function HealthMark({ w }: { w: WorkflowSummary }): JSX.Element {
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
