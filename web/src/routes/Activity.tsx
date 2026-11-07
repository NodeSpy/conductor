// Activity (design §4.2, prototype js/screens/runs.js activityPage/strip): every
// run in one place, grouped by who needs to act — Needs attention, Running now,
// Finished (paged) — with the interactive runs-per-hour strip above it (a
// tooltip per bar, unlike Home's static one) and the shared filter bar.
// Everything is live: the signals refresh on each SSE run event.
//
// Markup and classes are the prototype's (css/daylight.css .dl-tbl.grouped,
// .dl-strip, .dl-selbar, .dl-phone-groups): a desktop grouped table with a
// checkbox column and a pinned selection bar, and a phone list (CSS swaps
// between them at the narrow breakpoint, so both render and only one shows).
//
// Left out (prototype-only, no backend yet): the Bot filter menu (the daemon's
// RunSummary carries no bot id of its own — bot runs are named "bot:<id>" in
// workflow today) and the usage/cost column (usage display has no server-side
// toggle). Bulk Retry/Stop call the same single-run endpoints as the row
// action, once per selected run — there is no bulk daemon verb.
import type { JSX } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import { runs, status, liveTick, loadError, connAttrs, statusMark, dismissed } from '../state'
import { EmptyState } from '../components/EmptyState'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { FilterBar, type Option } from '../components/FilterBar'
import { emptyFilter, matchRun, rangeMinutes, hasAnyFilter, STATUSES, type FilterState } from '../lib/filter'
import { humanDuration, ago, plural } from '../lib/format'
import { api } from '../api/client'
import { go } from '../router'
import { usePhoneLayout } from '../lib/layout'

const PAGE = 20

function startedMs(r: RunSummary): number {
  return new Date(r.started).getTime()
}

function factsLine(r: RunSummary): JSX.Element | null {
  if (!r.repo) return null
  return (
    <span class="fx">
      <span class="fx-i">{r.repo}{r.number ? `#${r.number}` : ''}</span>
    </span>
  )
}

function reason(r: RunSummary): string {
  return `${r.failed_step ? `Failed at “${r.failed_step}”` : 'Failed'}${r.error ? `: ${r.error}` : '.'}`
}

export function Activity(): JSX.Element {
  const tick = liveTick.value
  const skip = dismissed.value
  // The table's own "Show N more" button (below) is left out of the DOM on a
  // phone: the phone groups render the identical button for the same
  // `shown`/`finished`, and CSS alone hiding the table (display: none, not
  // removed) still leaves it a second, invisible match for any locator that
  // finds buttons by their text — it collided with the phone disclosure's own
  // "Show"/"Hide" toggle, which shares the word "Show".
  const phone = usePhoneLayout()
  const [filter, setFilter] = useState<FilterState>(emptyFilter('12h'))
  const [shown, setShown] = useState(PAGE)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const attrs = connAttrs.value
  const all = runs.value

  const matched = useMemo(
    () => all.filter((r) => matchRun(r, filter, attrs)).sort((a, b) => startedMs(b) - startedMs(a)),
    [all, filter, attrs, tick],
  )

  const needs = matched.filter((r) => r.status === 'failed' && !skip.includes(r.id))
  const running = matched.filter((r) => r.status === 'running')
  const finished = matched.filter((r) => r.status === 'ok' || r.status === 'retried' || (r.status === 'failed' && skip.includes(r.id)))
  const finishedShown = finished.slice(0, shown)

  const update = (patch: Partial<FilterState>) => {
    setFilter({ ...filter, ...patch })
    setShown(PAGE)
    clearSel()
  }
  const toggle = (key: 'status' | 'workflow', v: string) => {
    const cur = filter[key]
    update({ [key]: cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v] } as Partial<FilterState>)
  }

  const wfOptions: Option[] = useMemo(() => {
    const m = new Map<string, number>()
    all.forEach((r) => m.set(r.workflow, (m.get(r.workflow) || 0) + 1))
    return [...m.entries()].sort().map(([v, n]) => ({ v, label: v, n }))
  }, [all])

  const toggleSel = (id: string) => {
    setSel((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  const selAll = () => setSel(new Set([...needs, ...running, ...finishedShown].map((r) => r.id)))
  const selMatching = () => setSel(new Set(matched.map((r) => r.id)))
  const clearSel = () => setSel(new Set())

  const selRuns = all.filter((r) => sel.has(r.id))
  const selRetryable = selRuns.filter((r) => r.status === 'failed').length
  const selStoppable = selRuns.filter((r) => r.status === 'running').length

  const bulkRetry = async () => {
    const rs = selRuns.filter((r) => r.status === 'failed')
    if (!rs.length) return
    if (!confirm(`Retry ${rs.length} run${rs.length > 1 ? 's' : ''}? Each starts again from the step that failed.`)) return
    setBusy(true)
    try {
      for (const r of rs) await api.retryRun(r.id, {}).catch(() => undefined)
    } finally {
      clearSel()
      setBusy(false)
    }
  }
  const bulkStop = async () => {
    const rs = selRuns.filter((r) => r.status === 'running')
    if (!rs.length) return
    if (!confirm(`Stop ${rs.length} running run${rs.length > 1 ? 's' : ''}? Each stops mid-step; you can retry it from any step.`)) return
    setBusy(true)
    try {
      for (const r of rs) await api.stopRun(r.id).catch(() => undefined)
    } finally {
      clearSel()
      setBusy(false)
    }
  }

  const cols = 6 // sel, run, status, when, took, id (+ act)

  return (
    <div data-tick={tick}>
      <div class="dl-phead">
        <div>
          <h1>Activity</h1>
        </div>
      </div>
      {loadError.value ? <div class="banner err">Couldn't load: {loadError.value}</div> : null}

      {status.value && all.length === 0 ? (
        <EmptyState
          testid="activity-empty"
          title="No runs yet"
          action={{ label: 'See your workflows', href: '#/workflows', testid: 'activity-empty-workflows' }}
        >
          <p>Every time a workflow fires, its run lands here with each step, how long it took and what it said. Find, filter and retry them from this page.</p>
        </EmptyState>
      ) : (
        <>
          <FilterBar
            q={filter.q}
            onQueryChange={(q) => update({ q })}
            placeholder={attrs.find((a) => a.field === 'repo') ? 'Search, or filter: status:failed, repo:acme/web' : 'Search, or filter: status:failed'}
            menus={[
              { key: 'status', label: 'Status', multi: true, selected: filter.status, options: STATUSES.map(([v, label]) => ({ v, label })), onToggle: (v) => toggle('status', v) },
              ...(wfOptions.length ? [{ key: 'workflow', label: 'Workflow', multi: true, selected: filter.workflow, options: wfOptions, onToggle: (v: string) => toggle('workflow', v) }] : []),
            ]}
            attrs={attrs}
            attrsValue={filter.attrs}
            onAttrsChange={(next) => update({ attrs: next })}
            since={filter.since}
            onSinceChange={(v) => update({ since: v })}
            showTime
          />

          <Strip runs={matched} since={filter.since} />

          <section class="dl-card dl-flush dl-runs-card" aria-label="Runs">
              <table class="dl-tbl grouped">
                {sel.size ? (
                  <thead class="selecting">
                    <tr>
                      <th colSpan={cols + 1}>
                        <div class="dl-selbar" role="region" aria-label="Selection">
                          <b>{sel.size} selected</b>
                          <button class="btn sm secondary" type="button" disabled={busy || !selRetryable} onClick={bulkRetry}>
                            Retry{selRetryable && selRetryable < sel.size ? ` ${selRetryable}` : ''}…
                          </button>
                          <button class="btn sm secondary" type="button" disabled={busy || !selStoppable} onClick={bulkStop}>
                            Stop{selStoppable && selStoppable < sel.size ? ` ${selStoppable}` : ''}…
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
                      </th>
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
                <tbody>
                  {matched.length === 0 ? (
                    <tr>
                      <td colSpan={cols + 1} class="dl-empty-row">
                        No run matches these filters.{' '}
                        {hasAnyFilter(filter) ? (
                          <button class="link" type="button" onClick={() => setFilter(emptyFilter('all'))}>
                            Clear filters
                          </button>
                        ) : null}
                      </td>
                    </tr>
                  ) : null}
                </tbody>
                {needs.length ? (
                  <tbody class="g-attn">
                    <GroupHead title="Needs attention" n={needs.length} cols={cols} />
                    {needs.map((r) => <Row r={r} rich sel={sel.has(r.id)} onSel={() => toggleSel(r.id)} />)}
                  </tbody>
                ) : null}
                {running.length ? (
                  <tbody class="g-live">
                    <GroupHead title="Running now" n={running.length} cols={cols} />
                    {running.map((r) => <Row r={r} sel={sel.has(r.id)} onSel={() => toggleSel(r.id)} />)}
                  </tbody>
                ) : null}
                {finished.length ? (
                  <tbody class="g-done">
                    <GroupHead title="Finished" n={finished.length} cols={cols} />
                    {finishedShown.map((r) => <Row r={r} sel={sel.has(r.id)} onSel={() => toggleSel(r.id)} />)}
                    {finished.length > shown && !phone ? (
                      <tr class="more">
                        <td colSpan={cols + 1}>
                          <button class="btn sm secondary" type="button" onClick={() => setShown((s) => s + PAGE)}>
                            Show {Math.min(PAGE, finished.length - shown)} more ({finished.length} in all)
                          </button>
                        </td>
                      </tr>
                    ) : null}
                  </tbody>
                ) : null}
              </table>
          </section>

          <div class="dl-phone-groups">
            <PhoneGroups needs={needs} running={running} finished={finished} shown={shown} onMore={() => setShown((s) => s + PAGE)} />
          </div>
        </>
      )}
      <p class="dl-sub dl-keep">Runs are kept for 14 days; each record is signed, and one that fails its signature shows as tampered.</p>
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

function Row({ r, rich, sel, onSel }: { r: RunSummary; rich?: boolean; sel: boolean; onSel: () => void }): JSX.Element {
  const m = statusMark(r.status)
  const edge = rich ? 'fail' : undefined
  return (
    <tr
      class={rich ? 'attn' : ''}
      data-s={edge}
      data-run={r.id}
      data-href={'#/runs/' + r.id}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('input, button, a')) return
        go('runs/' + r.id)
      }}
    >
      <td class="sel">
        <input type="checkbox" class="cbx" checked={sel} aria-label={`Select ${r.workflow} ${r.id}`} onChange={onSel} />
      </td>
      <td class="run">
        <b>{r.workflow}</b>
        {rich ? <span class="reason">{reason(r)}</span> : null}
        {factsLine(r)}
      </td>
      <td class="st-c">
        <StatusMark s={m.s} word={m.word} />
      </td>
      <td class="when nowrap">{ago(r.started)}</td>
      <td class="num">{humanDuration(r.duration_ms)}</td>
      <td class="mono id">{r.id}</td>
      <td class="act">{rich ? <a class="btn sm secondary row-act" href={'#/runs/' + r.id}>See why</a> : null}</td>
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
  return (
    <>
      {needs.length ? (
        <>
          <h3 class="ph-g">Needs attention<span>{needs.length}</span></h3>
          <ul class="dl-list dl-runs-phone">
            {needs.map((r) => <PhoneRow r={r} rich />)}
          </ul>
        </>
      ) : null}
      {running.length ? (
        <>
          <h3 class="ph-g">Running now<span>{running.length}</span></h3>
          <ul class="dl-list dl-runs-phone">
            {running.map((r) => <PhoneRow r={r} />)}
          </ul>
        </>
      ) : null}
      {finished.length ? (
        <button type="button" class="ph-done" aria-expanded={showDone} onClick={() => setShowDone((v) => !v)}>
          <span><b>{finished.length} finished</b></span>
          <span class="dl-link">{showDone ? 'Hide' : 'Show'}</span>
        </button>
      ) : null}
      {showDone ? (
        <>
          <h3 class="ph-g">Finished<span>{finished.length}</span></h3>
          <ul class="dl-list dl-runs-phone">
            {finishedShown.map((r) => <PhoneRow r={r} />)}
          </ul>
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
  const m = statusMark(r.status)
  return (
    <li class={rich ? 'attn' : ''} data-s={rich ? 'fail' : undefined} data-run={r.id}>
      <a href={'#/runs/' + r.id}>
        <span class="dl-nt">
          <b>{r.workflow}</b>
          {rich ? <span class="reason">{reason(r)}</span> : null}
          {factsLine(r)}
          <StatusMark s={m.s} word={m.word} />
        </span>
        <span class="dl-age">{ago(r.started).replace(' ago', '')}</span>
      </a>
    </li>
  )
}

// Strip is the interactive runs-per-hour chart (prototype UI.stackBars with
// o.tip): unlike Home's static strip, each bar is a tooltip stop (hover, tap or
// the arrow keys) naming its window, each kind's count and the failures in it.
function Strip({ runs: rs, since }: { runs: RunSummary[]; since: string }): JSX.Element {
  const [hover, setHover] = useState<number | null>(null)
  const span = rangeMinutes(since) || 10080
  const n = span <= 60 ? 12 : span <= 1440 ? Math.round(span / 60) : span <= 10080 ? 7 : 30
  const bucketMin = span / n
  const now = Date.now()

  type B = { ok: number; fail: number; run: number; other: number; from: number; to: number }
  const buckets: B[] = []
  for (let i = 0; i < n; i++) {
    const to = now - (n - 1 - i) * bucketMin * 60000
    const from = to - bucketMin * 60000
    buckets.push({ ok: 0, fail: 0, run: 0, other: 0, from, to })
  }
  rs.forEach((r) => {
    const t = startedMs(r)
    const idx = Math.floor((t - buckets[0].from) / (bucketMin * 60000))
    if (idx < 0 || idx >= n) return
    const b = buckets[idx]
    if (r.status === 'ok') b.ok++
    else if (r.status === 'failed') b.fail++
    else if (r.status === 'running') b.run++
    else b.other++
  })
  const max = Math.max(4, ...buckets.map((b) => b.ok + b.fail + b.run + b.other))
  const counts = {
    ok: rs.filter((r) => r.status === 'ok').length,
    fail: rs.filter((r) => r.status === 'failed').length,
    run: rs.filter((r) => r.status === 'running').length,
    other: rs.filter((r) => r.status === 'retried').length,
  }
  const fails = rs.filter((r) => r.status === 'failed')
  const unit = span <= 60 ? '5 minutes' : span <= 1440 ? 'hour' : 'day'
  const w = 960
  const h = 96
  const base = h - 17
  const bw = w / n - 6

  const fmtWhen = (b: B): string => {
    const d = (ms: number) => new Date(ms)
    if (span <= 1440) return `${hm(d(b.from))}–${b.to >= now - 60000 ? 'now' : hm(d(b.to))}`
    return d(b.from).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }

  return (
    <section class="dl-card dl-strip" aria-label="Runs per period">
      <header class="dl-ch">
        <h2>Runs per {unit}</h2>
        <span class="dl-sub">{plural(rs.length, 'run')}</span>
      </header>
      <svg class="dl-hours" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={`${plural(rs.length, 'run')}; ${fails.length} failed`}>
        {buckets.map((b, i) => {
          const x = i * (w / n) + 3
          let y = base
          const order: ['fail' | 'run' | 'other' | 'ok', string][] = [
            ['fail', 'hb-fail'],
            ['run', 'hb-run'],
            ['other', 'hb-other'],
            ['ok', 'hb-ok'],
          ]
          return (
            <g
              class={'hb' + (hover === i ? ' on' : '')}
              tabIndex={0}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover((h2) => (h2 === i ? null : h2))}
              onFocus={() => setHover(i)}
              onBlur={() => setHover((h2) => (h2 === i ? null : h2))}
            >
              <rect class="hb-hit" x={x - 3} y={0} width={w / n} height={base} />
              {order.map(([k, cls], j) => {
                if (!b[k]) return null
                const hh = (b[k] / max) * (h - 22)
                y -= hh
                const gap = j ? 2 : 0
                return (
                  <>
                    <rect x={x} y={y} width={bw} height={Math.max(0, hh - gap)} rx={2} class={cls} />
                    {k === 'ok' ? <rect x={x} y={y} width={bw} height={Math.min(2, hh - gap)} class="hb-cap" /> : null}
                  </>
                )
              })}
            </g>
          )
        })}
        <line x1={0} x2={w} y1={base} y2={base} class="hb-axis" />
        <text x={0} y={h - 3} class="hb-ax">
          {span <= 1440 ? `${Math.round(span / 60) || 1} h ago` : span === 1440 ? '24 h ago' : `${Math.round(span / 1440)} days ago`}
        </text>
        <text x={w} y={h - 3} class="hb-ax" text-anchor="end">
          now
        </text>
      </svg>
      {hover != null ? (
        <div class="hb-tip" role="tooltip" style={{ left: `${((hover + 0.5) / n) * 100}%`, bottom: '100%', transform: 'translateX(-50%)', marginBottom: '8px' }}>
          <b class="hb-tip-h">{fmtWhen(buckets[hover])}</b>
          <ul class="hb-tip-l">
            <TipRow cls="ok" label="Passed" n={buckets[hover].ok} />
            <TipRow cls="run" label="Running" n={buckets[hover].run} />
            <TipRow cls="fail" label="Failed" n={buckets[hover].fail} />
            {buckets[hover].other ? <TipRow cls="other" label="Waiting or parked" n={buckets[hover].other} /> : null}
          </ul>
        </div>
      ) : null}
      <div class="hb-foot">
        <ul class="hb-legend">
          <li><i class="hb-sw hb-sw-ok" aria-hidden="true" />Passed <b>{counts.ok}</b></li>
          <li><i class="hb-sw hb-sw-fail" aria-hidden="true" />Failed <b>{counts.fail}</b></li>
          <li><i class="hb-sw hb-sw-run" aria-hidden="true" />Running <b>{counts.run}</b></li>
          <li><i class="hb-sw hb-sw-other" aria-hidden="true" />Waiting or parked <b>{counts.other}</b></li>
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
                  {r.workflow} at {r.failed_step || 'a step'}
                </a>
              </>
            ))}
            {fails.length > 4 ? ` and ${fails.length - 4} more` : ''}.
          </span>
        </p>
      ) : (
        <p class="dl-foot-note">
          <StatusMark s="ok" mkOnly />
          <span>Nothing failed in this range.</span>
        </p>
      )}
    </section>
  )
}

function hm(d: Date): string {
  return d.toTimeString().slice(0, 5)
}

function TipRow({ cls, label, n }: { cls: string; label: string; n: number }): JSX.Element {
  return (
    <li>
      <i class={`hb-sw hb-sw-${cls}`} aria-hidden="true" />
      {label} <b>{n}</b>
    </li>
  )
}
