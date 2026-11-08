// Activity (design §4.2), a line-for-line port of the prototype's
// js/screens/runs.js activityPage: every run in one place, grouped by who
// needs to act — Needs attention, Running now, Finished (paged) — under the
// shared filter bar (Status · Workflow · Bot, the time range at the far
// right) and the runs-per-period chart (a tooltip per bar). A desktop grouped
// table with a checkbox column and a selection bar; on a phone the list
// (#act-phone) and no chart (#act-strip hides at the narrow breakpoint).
// Everything is live: the signals refresh on each SSE run event.
//
// Bulk Retry/Stop call the same single-run endpoints as a run's page, once per
// selected run (the daemon has no bulk verb). The usage/cost column stays out
// (usage display has no daemon-side switch).
import type { JSX } from 'preact'
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { runs, status, liveTick, loadError, connAttrs, dismissed } from '../state'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { EmptyState } from '../components/EmptyState'
import { FilterBar, type FilterMenu, type Option } from '../components/FilterBar'
import { ConfirmModal } from '../components/FormModal'
import { toast, ToastHost } from '../components/ChatBits'
import { Icon } from '../components/Icon'
import { emptyFilter, matchRun, rangeMinutes, hasAnyFilter, botOfRun, STATUSES, TIME_RANGES, type FilterState } from '../lib/filter'
import { agoText, isFailedRun, plainError, runBucket, runDur, runMark, runTargetWords } from '../components/RunBits'
import { hm, runningFor } from '../lib/runwords'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import { go } from '../router'
import { usePhoneLayout } from '../lib/layout'
import '../styles/runs-repass.css'
import { Chipped } from '../components/Chips'

const PAGE = 20

function startedMs(r: RunSummary): number {
  return new Date(r.started).getTime()
}

function hasFacts(r: RunSummary): boolean {
  return !!(r.repo && !r.repo.startsWith('manual:'))
}

function FactsLine({ r }: { r: RunSummary }): JSX.Element | null {
  if (!hasFacts(r)) return null
  return (
    <span class="fx">
      <span class="fx-i" data-pri={r.number ? 1 : 3} title="Repo">
        <Icon name={r.number ? 'pr' : 'git-fork'} />
        {r.repo}
        {r.number ? `#${r.number}` : ''}
      </span>
    </span>
  )
}

function RunName({ r }: { r: RunSummary }): JSX.Element {
  return (
    <>
      <b>{r.workflow.startsWith('bot:') ? r.workflow.slice(4) : r.workflow}</b>
      {hasFacts(r) ? null : <> <span class="tgt">{runTargetWords(r)}</span></>}
    </>
  )
}

// reason: where it stopped and why, the step named once (runs.js reason)
function reason(r: RunSummary): string {
  const e = plainError(r)
  return `${r.failed_step ? `Failed at “${r.failed_step}”` : 'Failed'}${e ? `: ${e}` : ''}${/\.$/.test(e) ? '' : '.'}`
}

function runParts(rs: RunSummary[]): string {
  return (
    [
      [rs.filter((r) => r.status === 'ok').length, 'went fine'],
      [rs.filter(isFailedRun).length, 'failed'],
      [rs.filter((r) => r.status === 'running').length, 'running'],
      [rs.filter((r) => r.status === 'retried').length, 'retried'],
      [rs.filter((r) => r.status === 'interrupted').length, 'interrupted'],
    ] as [number, string][]
  )
    .filter(([n]) => n)
    .map(([n, t]) => `${n} ${t}`)
    .join(' · ')
}

export function Activity(): JSX.Element {
  const tick = liveTick.value
  const skip = dismissed.value
  const phone = usePhoneLayout()
  const [filter, setFilter] = useState<FilterState>(emptyFilter('12h'))
  const [shown, setShown] = useState(PAGE)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [last, setLast] = useState<number | null>(null)
  const [ask, setAsk] = useState<null | 'retry' | 'stop'>(null)
  const attrs = connAttrs.value
  const all = runs.value

  const matched = useMemo(
    () => all.filter((r) => matchRun(r, filter, attrs)).sort((a, b) => startedMs(b) - startedMs(a)),
    [all, filter, attrs, tick],
  )

  const needs = matched.filter((r) => isFailedRun(r) && !skip.includes(r.id))
  const running = matched.filter((r) => r.status === 'running')
  const finished = matched.filter((r) => !needs.includes(r) && r.status !== 'running')
  const finishedShown = finished.slice(0, shown)
  const visible = [...needs, ...running, ...finishedShown]

  // functional updates: a typed status:failed toggles a menu and sets the search in one go
  const update = (patch: Partial<FilterState>) => {
    setFilter((f) => ({ ...f, ...patch }))
    setShown(PAGE)
  }
  const toggle = (key: 'status' | 'workflow' | 'bot', v: string) => {
    setFilter((f) => ({ ...f, [key]: f[key].includes(v) ? f[key].filter((x) => x !== v) : [...f[key], v] }))
    setShown(PAGE)
  }

  const count = (xs: string[]): Option[] => {
    const m = new Map<string, number>()
    xs.forEach((x) => x && m.set(x, (m.get(x) || 0) + 1))
    return [...m.entries()].map(([v, n]) => ({ v, label: v, n }))
  }
  const wfOptions = useMemo(() => count(all.filter((r) => !r.workflow.startsWith('bot:')).map((r) => r.workflow)), [all])
  const botOptions = useMemo(() => count(all.map(botOfRun)), [all])
  const botMenu: FilterMenu = { key: 'bot', label: 'Bot', multi: true, selected: filter.bot, options: botOptions, onToggle: (v: string) => toggle('bot', v), emptyText: 'No bot has run yet.' }

  const toggleSel = (id: string, shift: boolean) => {
    const i = visible.findIndex((r) => r.id === id)
    setSel((s) => {
      const next = new Set(s)
      const on = !next.has(id)
      if (shift && last != null && i >= 0) visible.slice(Math.min(last, i), Math.max(last, i) + 1).forEach((r) => (on ? next.add(r.id) : next.delete(r.id)))
      else if (on) next.add(id)
      else next.delete(id)
      return next
    })
    setLast(i)
  }
  const selAll = () => setSel(new Set(visible.map((r) => r.id)))
  const selMatching = () => setSel(new Set(matched.map((r) => r.id)))
  const clearSel = () => setSel(new Set())

  const selRuns = all.filter((r) => sel.has(r.id))
  const retryable = selRuns.filter(isFailedRun)
  const stoppable = selRuns.filter((r) => r.status === 'running')
  const clearFilters = () => {
    setFilter(emptyFilter('all'))
    setShown(PAGE)
  }

  const cols = 6
  const selBar = (
    <div class="dl-selbar" role="region" aria-label="Selection">
      <b>{sel.size} selected</b>
      <button class="btn sm secondary" type="button" aria-disabled={retryable.length ? undefined : 'true'} data-tip={retryable.length ? undefined : 'None of the selected runs failed'} onClick={() => retryable.length && setAsk('retry')}>
        <Icon name="undo" />
        Retry{retryable.length && retryable.length < sel.size ? ` ${retryable.length}` : ''}…
      </button>
      <button class="btn sm secondary" type="button" aria-disabled={stoppable.length ? undefined : 'true'} data-tip={stoppable.length ? undefined : 'None of the selected runs is running'} onClick={() => stoppable.length && setAsk('stop')}>
        <Icon name="x" />
        Stop{stoppable.length && stoppable.length < sel.size ? ` ${stoppable.length}` : ''}…
      </button>
      <span class="dl-sp" />
      {matched.length > sel.size ? (
        <button class="btn sm ghost" type="button" onClick={selMatching}>
          Select all {matched.length} matching
        </button>
      ) : null}
      <button class="btn sm ghost" type="button" onClick={clearSel}>
        Clear
      </button>
    </div>
  )

  return (
    <div data-tick={tick}>
      <div class="dl-phead">
        <div>
          <h1>Activity</h1>
        </div>
      </div>
      {loadError.value ? <div class="banner err">Couldn't load: {loadError.value}</div> : null}

      {status.value && all.length === 0 ? (
        // No run has happened at all (within the 14 days kept): the screen's one
        // thing doesn't exist yet, so it is the hero.
        <EmptyState
          hero
          icon="live"
          testid="activity-empty"
          title="No runs yet"
          action={{ label: 'See your workflows', href: '#/workflows', testid: 'activity-empty-workflows' }}
        >
          <p>Every time a workflow fires, its run lands here with each step, how long it took and what it said.</p>
        </EmptyState>
      ) : (
        status.value && (
          <>
            <div id="act-filters">
              <FilterBar
                q={filter.q}
                onQueryChange={(q) => update({ q })}
                placeholder="Search, or filter: status:failed"
                ariaLabel="Search and filter runs"
                menus={[
                  { key: 'status', label: 'Status', multi: true, selected: filter.status, options: STATUSES.map(([v, label, f]) => ({ v, label, n: all.filter(f).length })), onToggle: (v) => toggle('status', v) },
                  { key: 'workflow', label: 'Workflow', multi: true, selected: filter.workflow, options: wfOptions, onToggle: (v: string) => toggle('workflow', v) },
                  botMenu,
                ]}
                attrs={attrs}
                attrsValue={filter.attrs}
                onAttrsChange={(next) => update({ attrs: next })}
                since={filter.since}
                onSinceChange={(v) => update({ since: v })}
                sinceDef="12h"
                showTime
                filterByHint
                qualifiers
              />
            </div>

            <div id="act-strip">
              <Strip all={all} runs={matched} filter={filter} onAllTime={() => update({ since: 'all' })} />
            </div>

            <section class="dl-card dl-flush dl-runs-card">
              <table class="dl-tbl grouped" id="act-tbl">
                {sel.size ? (
                  <thead class="selecting">
                    <tr>
                      <th colSpan={cols + 1}>{selBar}</th>
                    </tr>
                  </thead>
                ) : (
                  <thead>
                    <tr>
                      <th class="sel">
                        <input type="checkbox" class="cbx" aria-label="Select every run shown" checked={false} onChange={selAll} />
                      </th>
                      <th>Run</th>
                      <th>Status</th>
                      <th>When</th>
                      <th class="num">Took</th>
                      <th class="id-h">ID</th>
                      <th />
                    </tr>
                  </thead>
                )}
                {matched.length === 0 ? (
                  <tbody>
                    <tr>
                      <td colSpan={cols + 1} class="dl-empty-row" data-testid="activity-none-match">
                        No run matches these filters.{' '}
                        <button class="link" type="button" onClick={clearFilters}>
                          Clear filters
                        </button>
                      </td>
                    </tr>
                  </tbody>
                ) : null}
                {needs.length ? (
                  <tbody class="g-attn">
                    <GroupHead title="Needs attention" n={needs.length} cols={cols} />
                    {needs.map((r) => <Row r={r} rich sel={sel.has(r.id)} onSel={(sh) => toggleSel(r.id, sh)} />)}
                  </tbody>
                ) : null}
                {running.length ? (
                  <tbody class="g-live">
                    <GroupHead title="Running now" n={running.length} cols={cols} />
                    {running.map((r) => <Row r={r} sel={sel.has(r.id)} onSel={(sh) => toggleSel(r.id, sh)} />)}
                  </tbody>
                ) : null}
                {finished.length ? (
                  <tbody class="g-done">
                    <GroupHead title="Finished" n={finished.length} cols={cols} />
                    {finishedShown.map((r) => <Row r={r} sel={sel.has(r.id)} onSel={(sh) => toggleSel(r.id, sh)} />)}
                    {finished.length > shown && !phone ? (
                      <tr class="more">
                        <td colSpan={cols + 1}>
                          <button class="btn sm secondary" type="button" id="act-more" onClick={() => setShown((s) => s + PAGE)}>
                            Show {Math.min(PAGE, finished.length - shown)} more ({finished.length} in all)
                          </button>
                        </td>
                      </tr>
                    ) : null}
                  </tbody>
                ) : null}
              </table>
            </section>

            <div class="dl-phone-groups" id="act-phone">
              {matched.length === 0 ? (
                <p class="dl-empty-row" data-testid="activity-none-match">
                  No run matches these filters.{' '}
                  {hasAnyFilter(filter) ? (
                    <button class="link" type="button" onClick={clearFilters}>
                      Clear filters
                    </button>
                  ) : null}
                </p>
              ) : (
                <PhoneGroups needs={needs} running={running} finished={finished} shown={shown} onMore={() => setShown((s) => s + PAGE)} />
              )}
            </div>
          </>
        )
      )}
      <p class="dl-sub dl-keep">Runs are kept for 14 days; each record is signed, and one that fails its signature shows as tampered.</p>

      {ask === 'retry' ? (
        <ConfirmModal
          title={`Retry ${retryable.length} run${retryable.length > 1 ? 's' : ''}?`}
          body="Each starts again from the step that failed; the steps before it are replayed from what they recorded, so nothing they did is repeated."
          ok={`Retry ${retryable.length}`}
          onClose={() => setAsk(null)}
          onOk={async () => {
            let bad = ''
            for (const r of retryable) await api.retryRun(r.id, { from: r.failed_step || '' }).catch((e) => (bad = humanError(e)))
            clearSel()
            if (bad) toast('Some runs didn’t retry', bad, 'fail')
            else toast(`Retried ${retryable.length} run${retryable.length > 1 ? 's' : ''}`, 'they’re running now', 'run')
          }}
        />
      ) : null}
      {ask === 'stop' ? (
        <ConfirmModal
          title={`Stop ${stoppable.length} running run${stoppable.length > 1 ? 's' : ''}?`}
          body="Each stops mid-step; anything already pushed stays. You can retry them from any step."
          ok={`Stop ${stoppable.length}`}
          danger
          onClose={() => setAsk(null)}
          onOk={async () => {
            let bad = ''
            for (const r of stoppable) await api.stopRun(r.id).catch((e) => (bad = humanError(e)))
            clearSel()
            if (bad) toast('Some runs didn’t stop', bad, 'fail')
            else toast(`Stopped ${stoppable.length}`, 'retry them from any step', 'ok')
          }}
        />
      ) : null}
      <ToastHost />
    </div>
  )
}

function GroupHead({ title, n, cols }: { title: string; n: number; cols: number }): JSX.Element {
  return (
    <tr class="gh">
      <td colSpan={cols + 1}>
        <b>{title}</b>
        <span class="gn">{n}</span>
      </td>
    </tr>
  )
}

function Row({ r, rich, sel, onSel }: { r: RunSummary; rich?: boolean; sel: boolean; onSel: (shift: boolean) => void }): JSX.Element {
  const m = runMark(r.status)
  return (
    <tr
      class={rich ? 'attn' : ''}
      data-s={rich ? 'fail' : undefined}
      data-run={r.id}
      data-href={'#/runs/' + r.id}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('a, button, input, label, select')) return
        go('runs/' + r.id)
      }}
    >
      <td class="sel">
        <input
          type="checkbox"
          class="cbx"
          checked={sel}
          aria-label={`Select ${r.workflow} ${r.id}`}
          onClick={(e) => onSel((e as MouseEvent).shiftKey)}
        />
      </td>
      <td class="run">
        <RunName r={r} />
        {rich ? <span class="reason"><Chipped text={reason(r)} /></span> : null}
        <FactsLine r={r} />
      </td>
      <td class="st-c">
        <StatusMark s={m.s} word={m.word} />
      </td>
      <td class="when nowrap">{agoText(r.started)}</td>
      <td class="num">{r.status === 'running' ? <span class="dl-sub">{runningFor(r.started)} so far</span> : runDur(r)}</td>
      <td class="mono id">{r.id}</td>
      <td class="act">
        {rich ? (
          <a class="btn sm secondary row-act" href={'#/runs/' + r.id}>
            See why
          </a>
        ) : null}
      </td>
    </tr>
  )
}

function PhoneGroups({
  needs,
  running,
  finished,
  shown,
  onMore,
}: {
  needs: RunSummary[]
  running: RunSummary[]
  finished: RunSummary[]
  shown: number
  onMore: () => void
}): JSX.Element {
  const [showDone, setShowDone] = useState(false)
  const finishedShown = finished.slice(0, shown)
  const grp = (t: string, xs: RunSummary[], rich: boolean, total = xs.length) =>
    xs.length ? (
      <>
        <h3 class="ph-g">
          {t}
          <span>{total}</span>
        </h3>
        <ul class="dl-list dl-runs-phone">
          {xs.map((r) => (
            <PhoneRow r={r} rich={rich} />
          ))}
        </ul>
      </>
    ) : null
  return (
    <>
      {grp('Needs attention', needs, true)}
      {grp('Running now', running, false)}
      {finished.length ? (
        <button type="button" class="ph-done" aria-expanded={showDone} onClick={() => setShowDone((v) => !v)}>
          <span>
            <b>{finished.length} finished</b> · {runParts(finished)}
          </span>
          <span class="dl-link">{showDone ? 'Hide' : 'Show'}</span>
        </button>
      ) : null}
      {showDone ? (
        <>
          {grp('Finished', finishedShown, false, finished.length)}
          {finished.length > shown ? (
            <button class="btn sm secondary" type="button" onClick={onMore}>
              Show {Math.min(PAGE, finished.length - shown)} more ({finished.length} in all)
            </button>
          ) : null}
        </>
      ) : null}
    </>
  )
}

function PhoneRow({ r, rich }: { r: RunSummary; rich?: boolean }): JSX.Element {
  const m = runMark(r.status)
  return (
    <li class={rich ? 'attn' : ''} data-s={rich ? 'fail' : undefined} data-run={r.id}>
      <a href={'#/runs/' + r.id}>
        <span class="dl-nt">
          <b>
            <RunName r={r} />
          </b>
          {rich ? <span class="reason"><Chipped text={reason(r)} /></span> : null}
          <FactsLine r={r} />
          <StatusMark s={m.s} word={m.word} />
        </span>
        <span class="dl-age">{agoText(r.started).replace(' ago', '')}</span>
      </a>
    </li>
  )
}

type Per = { ok: number; other: number; run: number; fail: number }
const SEG: [keyof Per, string, string][] = [
  ['ok', 'hb-ok', 'Passed'],
  ['other', 'hb-other', 'Waiting or parked'],
  ['run', 'hb-run', 'Running'],
  ['fail', 'hb-fail', 'Failed'],
]
const bucketOf = (r: RunSummary): keyof Per => runBucket(r)
const failedAt = (r: RunSummary) => hm(r.finished || r.started)

// Strip is the runs-per-period chart (runs.js strip + overview.js UI.stackBars
// with o.tip and UI.barLegend): an aggregate strip, failures stacked in the
// danger colour and named below; each bar a tooltip stop (hover, focus, tap,
// the arrow keys).
function Strip({ all, runs: rs, filter, onAllTime }: { all: RunSummary[]; runs: RunSummary[]; filter: FilterState; onAllTime: () => void }): JSX.Element {
  const [tip, setTip] = useState<{ i: number; g: Element; x: number; y: number; placed: boolean } | null>(null)
  const cardRef = useRef<HTMLElement>(null)
  const tipRef = useRef<HTMLDivElement>(null)
  const sinceMin = rangeMinutes(filter.since)
  const span = sinceMin || 10080
  const n = span <= 60 ? 12 : span <= 1440 ? Math.round(span / 60) : span <= 10080 ? 7 : 30
  const bucket = span / n
  const w = 960
  const h = 96
  const base = h - 17
  const now = Date.now()
  const per: Per[] = Array.from({ length: n }, () => ({ ok: 0, other: 0, run: 0, fail: 0 }))
  const failedIn: RunSummary[][] = Array.from({ length: n }, () => [])
  rs.forEach((r) => {
    const m = (now - startedMs(r)) / 60000
    if (m > span) return
    const i = Math.min(n - 1, Math.max(0, n - 1 - Math.floor(m / bucket)))
    const k = bucketOf(r)
    per[i][k]++
    if (k === 'fail') failedIn[i].push(r)
  })
  const at = (min: number) => new Date(now - min * 60000).toISOString()
  const unitOf = span <= 1440 ? 'time' : 'day'
  const when = (i: number) => {
    const from = (n - i) * bucket
    const to = (n - 1 - i) * bucket
    return unitOf === 'day'
      ? new Date(at(from - bucket / 2)).toLocaleDateString(undefined, span > 10080 ? { month: 'short', day: 'numeric' } : { weekday: 'long' })
      : `${hm(at(from))}–${to ? hm(at(to)) : 'now'}`
  }
  const label = (TIME_RANGES.find(([v]) => v === filter.since) || TIME_RANGES[1])[1].toLowerCase().replace('last ', '')
  const unit = span <= 60 ? '5 minutes' : span <= 1440 ? 'hour' : 'day'
  const fails = rs.filter((r) => isFailedRun(r) && (now - startedMs(r)) / 60000 <= span)
  // failures from before the range aren't listed either; say how many, one click from showing them
  const older = sinceMin ? all.filter((r) => isFailedRun(r) && matchRun(r, { ...filter, since: 'all' }, []) && now - startedMs(r) > sinceMin * 60000).length : 0
  const olderNote = older ? (
    <>
      {' '}
      <button class="link" type="button" onClick={onAllTime}>
        {older} older failure{older > 1 ? 's' : ''} · show all time
      </button>
    </>
  ) : null
  const mx = Math.max(4, ...per.map((p) => p.ok + p.other + p.run + p.fail))
  const bw = w / n - 6
  const words = (p: Per) => {
    const xs = SEG.filter(([k]) => p[k]).map(([k, , l]) => `${p[k]} ${l.toLowerCase()}`)
    return xs.length ? xs.join(', ') : 'no runs'
  }
  const show = (i: number, g: Element) => setTip({ i, g, x: 0, y: 0, placed: false })
  // place it once it has drawn (its own size): above the chart, over its bar, kept inside the card
  useLayoutEffect(() => {
    if (!tip || tip.placed) return
    const card = cardRef.current
    if (!card || !tipRef.current) return
    const cr = card.getBoundingClientRect()
    const br = tip.g.getBoundingClientRect()
    const top = (tip.g as SVGGElement).ownerSVGElement?.getBoundingClientRect().top ?? br.top
    const tw = tipRef.current.offsetWidth
    const th = tipRef.current.offsetHeight
    setTip({ ...tip, placed: true, x: Math.min(Math.max(8, br.left + br.width / 2 - cr.left - tw / 2), cr.width - tw - 8), y: Math.max(8 - cr.top, top - cr.top - th - 6) })
  }, [tip])
  const totals: Per = { ok: 0, other: 0, run: 0, fail: 0 }
  per.forEach((p) => (Object.keys(totals) as (keyof Per)[]).forEach((k) => (totals[k] += p[k])))
  const tp = tip ? per[tip.i] : null

  return (
    <section class={'dl-card dl-strip' + (tip ? ' hb-tipped' : '')} ref={cardRef} onMouseLeave={() => setTip(null)}>
      <header class="dl-ch">
        <h2>Runs per {unit}</h2>
        <span class="dl-sub">{sinceMin ? `the last ${label}` : 'the last 7 days'}</span>
      </header>
      <svg class="dl-hours" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={`Runs per ${unit}: ${fails.length} failed`}>
        {per.map((p, i) => {
          const x = i * (w / n) + 3
          let y = base
          const segs = SEG.filter(([k]) => p[k])
          return (
            <g
              class={'hb' + (tip?.i === i ? ' on' : '')}
              tabIndex={i === n - 1 ? 0 : -1}
              role="img"
              aria-label={`${when(i)}: ${words(p)}`}
              data-i={i}
              data-hbtip=""
              onMouseEnter={(e) => show(i, e.currentTarget)}
              onFocus={(e) => show(i, e.currentTarget)}
              onClick={(e) => (tip?.i === i && (e as unknown as PointerEvent).pointerType === 'touch' ? setTip(null) : show(i, e.currentTarget))}
              onKeyDown={(e) => {
                if (e.key === 'Escape') return setTip(null)
                if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
                e.preventDefault()
                const next = (e.currentTarget.parentNode as Element).querySelector<SVGGElement>(`.hb[data-i="${i + (e.key === 'ArrowRight' ? 1 : -1)}"]`)
                next?.focus()
              }}
            >
              <rect class="hb-hit" x={x - 3} y={0} width={w / n} height={base} />
              {segs.map(([k, cls], j) => {
                const hh = (p[k] / mx) * (h - 22)
                y -= hh
                const g = j ? 2 : 0
                return (
                  <>
                    <rect x={x} y={y} width={bw} height={Math.max(0, hh - g)} rx={2} class={cls} />
                    {k === 'ok' ? <rect x={x} y={y} width={bw} height={Math.min(2, hh - g)} class="hb-cap" /> : null}
                  </>
                )
              })}
            </g>
          )
        })}
        <line x1={0} x2={w} y1={h - 17} y2={h - 17} class="hb-axis" />
      </svg>
      <div class="dl-axis">
        <span>{sinceMin ? `${label.replace(/^hour$/, '1 hour')} ago` : '7 days ago'}</span>
        <span>now</span>
      </div>
      <div class="hb-foot">
        <ul class="hb-legend">
          {([['ok', 'Passed'], ['fail', 'Failed'], ['run', 'Running'], ['other', 'Waiting or parked']] as [keyof Per, string][]).map(([k, l]) => (
            <li>
              <i class={`hb-sw hb-sw-${k}`} aria-hidden="true" />
              {l} <b>{totals[k]}</b>
            </li>
          ))}
        </ul>
      </div>
      {fails.length ? (
        <p class="dl-foot-note">
          <StatusMark s="fail" mkOnly />
          <span>
            {fails.length} failed:{' '}
            {fails.slice(0, 4).map((r, i) => (
              <>
                {i ? ', ' : ''}
                <a class="dl-link" href={'#/runs/' + r.id}>
                  {r.workflow.replace(/^bot:/, '')} at {failedAt(r)}
                </a>
              </>
            ))}
            {fails.length > 4 ? ` and ${fails.length - 4} more` : ''}.{olderNote}
          </span>
        </p>
      ) : (
        <p class="dl-foot-note">
          <StatusMark s="ok" mkOnly />
          <span>
            Nothing failed {sinceMin ? `in the last ${label}` : 'in the last 7 days'}.{olderNote}
          </span>
        </p>
      )}
      <div class="hb-tip" role="tooltip" ref={tipRef} hidden={!tip} style={tip ? { left: `${tip.x}px`, top: `${tip.y}px`, visibility: tip.placed ? undefined : 'hidden' } : undefined}>
        {tip && tp ? (
          <>
            <b class="hb-tip-h">{when(tip.i)}</b>
            {SEG.some(([k]) => tp[k]) ? (
              <ul class="hb-tip-l">
                {SEG.filter(([k]) => tp[k]).map(([k, , l]) => (
                  <li>
                    <i class={`hb-sw hb-sw-${k}`} aria-hidden="true" />
                    {l} <b>{tp[k]}</b>
                  </li>
                ))}
              </ul>
            ) : (
              <span class="dl-sub">No runs</span>
            )}
            {failedIn[tip.i].length ? (
              <p class="hb-tip-f">
                {failedIn[tip.i].slice(0, 3).map((r, i) => (
                  <>
                    {i ? ', ' : ''}
                    <a class="dl-link" href={'#/runs/' + r.id}>
                      {r.workflow.replace(/^bot:/, '')} at {failedAt(r)}
                    </a>
                  </>
                ))}
                {failedIn[tip.i].length > 3 ? ` and ${failedIn[tip.i].length - 3} more` : ''}
              </p>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  )
}
