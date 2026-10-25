// Activity (design §4.2): every run in one place, grouped by who needs to act —
// Needs attention, Running now, Finished (paged) — with runs-per-hour above it
// (interactive here; static on Home) and the shared filter bar. Everything is
// live: the signals refresh on each SSE run event.
import type { JSX } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import { runs, liveTick, loadError, connAttrs, statusMark, dismissed } from '../state'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { FilterBar, type Option } from '../components/FilterBar'
import { emptyFilter, matchRun, rangeMinutes, hasAnyFilter, type FilterState } from '../lib/filter'
import { humanDuration, ago } from '../lib/format'
import { go } from '../router'

const PAGE = 20

function startedMs(r: RunSummary): number {
  return new Date(r.started).getTime()
}

function factsLine(r: RunSummary): JSX.Element | null {
  if (!r.repo) return null
  return <span class="facts-line mono">{r.repo}{r.number ? `#${r.number}` : ''}</span>
}

export function Activity(): JSX.Element {
  const tick = liveTick.value
  const skip = dismissed.value
  const [filter, setFilter] = useState<FilterState>(emptyFilter('12h'))
  const [shown, setShown] = useState(PAGE)
  const attrs = connAttrs.value
  const all = runs.value

  const matched = useMemo(
    () => all.filter((r) => matchRun(r, filter, attrs)).sort((a, b) => startedMs(b) - startedMs(a)),
    [all, filter, attrs, tick],
  )

  const needs = matched.filter((r) => r.status === 'failed' && !skip.includes(r.id))
  const running = matched.filter((r) => r.status === 'running')
  const finished = matched.filter((r) => r.status === 'ok' || r.status === 'retried' || (r.status === 'failed' && skip.includes(r.id)))

  const wfOptions: Option[] = useMemo(() => {
    const m = new Map<string, number>()
    all.forEach((r) => m.set(r.workflow, (m.get(r.workflow) || 0) + 1))
    return [...m.entries()].sort().map(([v, n]) => ({ v, label: v, n }))
  }, [all])

  return (
    <div class="col" data-tick={tick}>
      <h1 class="screen-title serif">Activity</h1>
      {loadError.value ? <div class="banner err">Couldn't load: {loadError.value}</div> : null}

      <FilterBar value={filter} onChange={(f) => { setFilter(f); setShown(PAGE) }} workflows={wfOptions} attrs={attrs} />

      <Strip runs={matched} since={filter.since} />

      <section class="panel" aria-label="Runs">
        <p class="panel-say">
          {`${matched.length} run${matched.length === 1 ? '' : 's'}. ${needs.length} need attention, ${running.length} running, ${finished.length} finished.`}
        </p>
        <div class="tbl-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th>Run</th>
                <th>Status</th>
                <th>When</th>
                <th>Took</th>
                <th>ID</th>
              </tr>
            </thead>
            <tbody>
              {needs.length ? <GroupHead title="Needs attention" n={needs.length} attn /> : null}
              {needs.map((r) => <Row r={r} rowState="fail" />)}
              {running.length ? <GroupHead title="Running now" n={running.length} /> : null}
              {running.map((r) => <Row r={r} />)}
              {finished.length ? <GroupHead title="Finished" n={finished.length} /> : null}
              {finished.slice(0, shown).map((r) => <Row r={r} quiet />)}
              {matched.length === 0 ? (
                <tr>
                  <td colSpan={5} class="tbl-empty">
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
          </table>
        </div>
        {finished.length > shown ? (
          <button class="more-btn" type="button" onClick={() => setShown((s) => s + PAGE)}>
            Show {Math.min(PAGE, finished.length - shown)} more of {finished.length}
          </button>
        ) : null}
      </section>
      <p class="muted small">Runs are kept for 14 days; each record is signed, and one that fails its signature shows as tampered.</p>
    </div>
  )
}

function GroupHead({ title, n, attn }: { title: string; n: number; attn?: boolean }): JSX.Element {
  return (
    <tr class="grp-row">
      <td colSpan={5}>
        <div class="grp-head" data-tone={attn ? 'attn' : undefined}>
          <span class="grp-t">{title}</span>
          <span class="grp-n">· {n}</span>
        </div>
      </td>
    </tr>
  )
}

function Row({ r, rowState, quiet }: { r: RunSummary; rowState?: string; quiet?: boolean }): JSX.Element {
  const m = statusMark(r.status)
  return (
    <tr class={quiet ? 'quiet clickable' : 'clickable'} data-row={rowState} data-run={r.id} onClick={() => go('runs/' + r.id)}>
      <td>
        <div class="cell-title">{r.workflow}</div>
        <div class="cell-facts">{factsLine(r)}</div>
        {rowState === 'fail' ? <div class="why-line">{r.error || `failed at ${r.failed_step || 'a step'}`}</div> : null}
      </td>
      <td><StatusMark s={m.s} word={m.word} /></td>
      <td>{ago(r.started)}</td>
      <td>{humanDuration(r.duration_ms)}</td>
      <td class="mono id">{r.id}</td>
    </tr>
  )
}

// Strip is the interactive runs-per-hour chart. Unlike Home's static strip, each
// bar reveals a tooltip (its window and per-status counts) on hover/focus, and
// the failures in range are named below.
function Strip({ runs: rs, since }: { runs: RunSummary[]; since: string }): JSX.Element {
  const [hover, setHover] = useState<number | null>(null)
  const span = rangeMinutes(since) || 10080
  const n = span <= 60 ? 12 : span <= 1440 ? Math.round(span / 60) : span <= 10080 ? 7 : 30
  const bucketMin = span / n
  const now = Date.now()

  type B = { passed: number; failed: number; running: number; other: number; from: number; to: number }
  const buckets: B[] = []
  for (let i = 0; i < n; i++) {
    const to = now - (n - 1 - i) * bucketMin * 60000
    const from = to - bucketMin * 60000
    buckets.push({ passed: 0, failed: 0, running: 0, other: 0, from, to })
  }
  rs.forEach((r) => {
    const t = startedMs(r)
    const idx = Math.floor((t - buckets[0].from) / (bucketMin * 60000))
    if (idx < 0 || idx >= n) return
    const b = buckets[idx]
    if (r.status === 'ok') b.passed++
    else if (r.status === 'failed') b.failed++
    else if (r.status === 'running') b.running++
    else b.other++
  })
  const max = Math.max(1, ...buckets.map((b) => b.passed + b.failed + b.running + b.other))
  const counts = {
    passed: rs.filter((r) => r.status === 'ok').length,
    running: rs.filter((r) => r.status === 'running').length,
    failed: rs.filter((r) => r.status === 'failed').length,
  }
  const fails = rs.filter((r) => r.status === 'failed')
  const unit = span <= 60 ? '5 minutes' : span <= 1440 ? 'hour' : 'day'

  const fmtWhen = (b: B): string => {
    const d = (ms: number) => new Date(ms)
    if (span <= 1440) return `${hm(d(b.from))}–${b.to >= now - 60000 ? 'now' : hm(d(b.to))}`
    return d(b.from).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }

  return (
    <section class="panel strip" aria-label="Runs per period">
      <div class="panel-head">
        <span class="panel-title serif">Runs per {unit}</span>
        <span class="panel-sub">{rs.length} runs</span>
      </div>
      <div class="chart interactive" role="img" aria-label={`${rs.length} runs; ${fails.length} failed`}>
        {buckets.map((b, i) => {
          const h = (x: number) => `${(x / max) * 100}%`
          return (
            <div
              class={'bar' + (hover === i ? ' hot' : '')}
              tabIndex={0}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover((h2) => (h2 === i ? null : h2))}
              onFocus={() => setHover(i)}
              onBlur={() => setHover((h2) => (h2 === i ? null : h2))}
            >
              <div class="bar-stack">
                {b.failed ? <span class="seg failed" style={{ height: h(b.failed) }} /> : null}
                {b.running ? <span class="seg running" style={{ height: h(b.running) }} /> : null}
                {b.other ? <span class="seg other" style={{ height: h(b.other) }} /> : null}
                {b.passed ? <span class="seg passed" style={{ height: h(b.passed) }} /> : null}
              </div>
              {hover === i ? (
                <div class="bar-tip" role="tooltip">
                  <div class="bar-tip-when">{fmtWhen(b)}</div>
                  <TipRow cls="passed" label="Passed" n={b.passed} />
                  <TipRow cls="running" label="Running" n={b.running} />
                  <TipRow cls="failed" label="Failed" n={b.failed} />
                  {b.other ? <TipRow cls="other" label="Retried" n={b.other} /> : null}
                  {b.passed + b.failed + b.running + b.other === 0 ? <div class="bar-tip-none">No runs</div> : null}
                </div>
              ) : null}
            </div>
          )
        })}
      </div>
      <div class="legend">
        <LegendItem cls="passed" word="Passed" n={counts.passed} />
        <LegendItem cls="running" word="Running" n={counts.running} />
        <LegendItem cls="failed" word="Failed" n={counts.failed} />
      </div>
      {fails.length ? (
        <p class="strip-foot">
          <StatusMark s="fail" mkOnly />
          <span>
            {fails.length} failed:{' '}
            {fails.slice(0, 4).map((r, i) => (
              <>
                {i ? ', ' : ''}
                <a class="link" href={'#/runs/' + r.id}>
                  {r.workflow} at {r.failed_step || 'a step'}
                </a>
              </>
            ))}
            {fails.length > 4 ? ` and ${fails.length - 4} more` : ''}.
          </span>
        </p>
      ) : (
        <p class="strip-foot">
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
    <div class="bar-tip-row">
      <span class={`swatch ${cls}`} />
      {label} <b>{n}</b>
    </div>
  )
}

function LegendItem({ cls, word, n }: { cls: string; word: string; n: number }): JSX.Element {
  return (
    <span class="legend-item">
      <span class={`swatch ${cls}`} />
      {word} {n}
    </span>
  )
}
