// The Overview/Activity screen (design §4.1, §4.2). It answers, in reading
// order: is it healthy, what is it doing, and what needs me — then the run
// history (Activity merged in) below. Everything is live: the signals refresh
// on every SSE run event.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { status, runs, loadError, liveTick, statusMark, needsYou, dismiss } from '../state'
import { browser, type Purchase } from '../api/browser'
import { PurchaseRow } from './Purchase'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { humanDuration, ago, uptime } from '../lib/format'

const HOUR = 3_600_000

function startedMs(r: RunSummary): number {
  return new Date(r.started).getTime()
}

export function Overview(): JSX.Element {
  // Read liveTick so the whole screen re-renders on each SSE event (the pulse).
  const tick = liveTick.value
  const all = runs.value
  const st = status.value
  const now = Date.now()
  const last12 = all.filter((r) => startedMs(r) >= now - 12 * HOUR)

  const running = all.filter((r) => r.status === 'running').sort((a, b) => b.duration_ms - a.duration_ms)
  // Needs-you is the shared attention aggregator (state.needsYou): failed runs
  // the operator hasn't dismissed. It refreshes on every SSE attention event.
  const failed = needsYou(all)
  const finished = all.filter((r) => r.status === 'ok' || r.status === 'retried')
  const longest = running.length ? humanDuration(running[0].duration_ms) : '—'

  return (
    <div class="col" data-tick={tick}>
      <h1 class="sr-only">Home</h1>

      {loadError.value ? <div class="banner err">Couldn't load: {loadError.value}</div> : null}

      {/* At a glance */}
      <section class="tiles" aria-label="At a glance">
        <Tile label="Running" value={String(running.length)} sub={running.length ? `longest ${longest}` : 'nothing running'} href="#/activity" />
        <Tile label="Runs · last 12 h" value={String(last12.length)} sub={`${failed12(last12)} failed`} />
        <Tile label="Needs you" value={String(failed.length)} sub={failed.length ? 'failed runs' : 'all clear'} tone={failed.length ? 'attn' : undefined} />
        <Tile label="Version" value={st?.version ?? '…'} sub={st ? `up ${uptime(st.uptime_sec)}` : ''} mono />
      </section>

      {/* Needs you — fed by failed runs (one of its real sources; the full
          aggregator is Phase 2). Leads the page. */}
      <NeedsYou failed={failed} paused={st?.paused ?? false} />

      <PurchasesWaiting />

      <div class="split">
        <RunningNow running={running} />
        <Last12 runs={last12} />
      </div>

      {/* Activity: the grouped run table. */}
      <Activity needs={failed} running={running} finished={finished} />

      <Health />
    </div>
  )
}

function failed12(last12: RunSummary[]): number {
  return last12.filter((r) => r.status === 'failed').length
}

// PurchasesWaiting surfaces pending purchases in Needs you (design §4.5.1 — a
// purchase waits here as its own kind, Purchases). Self-contained: it loads once
// and shows nothing until there is something to approve.
function PurchasesWaiting(): JSX.Element | null {
  const [pending, setPending] = useState<Purchase[]>([])
  useEffect(() => {
    browser
      .purchases('pending')
      .then((r) => setPending(r.purchases || []))
      .catch(() => setPending([]))
  }, [])
  if (pending.length === 0) return null
  return (
    <section class="panel" aria-label="Purchases waiting for you">
      <div class="panel-head">
        <h2 class="panel-title serif">Purchases</h2>
        <span class="count hot">{pending.length}</span>
      </div>
      <ul class="dl-list">
        {pending.map((p) => (
          <PurchaseRow key={p.id} p={p} />
        ))}
      </ul>
    </section>
  )
}

function Tile({
  label,
  value,
  sub,
  href,
  tone,
  mono,
}: {
  label: string
  value: string
  sub?: string
  href?: string
  tone?: 'attn'
  mono?: boolean
}): JSX.Element {
  const body = (
    <>
      <div class="tile-label">{label}</div>
      <div class={`tile-value${mono ? ' mono' : ''}`} data-tone={tone}>
        {value}
      </div>
      {sub ? <div class="tile-sub">{sub}</div> : null}
    </>
  )
  return href ? (
    <a class="tile" href={href}>
      {body}
    </a>
  ) : (
    <div class="tile">{body}</div>
  )
}

function runTitle(r: RunSummary): string {
  const target = r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : ''
  return target ? `${r.workflow} · ${target}` : r.workflow
}

function NeedsYou({ failed, paused }: { failed: RunSummary[]; paused: boolean }): JSX.Element {
  return (
    <section class="panel" aria-label="Needs you">
      <div class="panel-head">
        <h2 class="panel-title serif">Needs you</h2>
      </div>
      {paused ? <p class="calm">All automation is paused.</p> : null}
      {failed.length === 0 ? (
        <p class="calm">Nothing needs you right now.</p>
      ) : (
        <ul class="rows">
          {failed.slice(0, 5).map((r) => (
            <li class="row" data-row="fail">
              <StatusMark s="ember" mkOnly />
              <div class="row-main">
                <div class="row-title">{runTitle(r)}</div>
                <div class="why-line">{r.error || `failed at ${r.failed_step || 'a step'}`}</div>
                <div class="row-meta mono">{r.id}</div>
              </div>
              <div class="row-age">{ago(r.started)}</div>
              <a class="btn sm" href={'#/runs/' + r.id}>
                Open
              </a>
              <button class="btn sm ghost" type="button" title="Dismiss from Needs you" onClick={() => dismiss(r.id)}>
                Dismiss
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function RunningNow({ running }: { running: RunSummary[] }): JSX.Element | null {
  if (running.length === 0) return null
  const top = running.slice(0, 3)
  return (
    <section class="panel" aria-label="Running now">
      <div class="panel-head">
        <h2 class="panel-title serif">Running now</h2>
      </div>
      <ul class="rows">
        {top.map((r) => (
          <li class="row">
            <StatusMark s="run" mkOnly />
            <div class="row-main">
              <div class="row-title">{runTitle(r)}</div>
              <div class="row-meta mono">{r.id}</div>
            </div>
            <div class="row-age">{humanDuration(r.duration_ms)}</div>
          </li>
        ))}
      </ul>
      {running.length > 3 ? <a class="more" href="#/">{`+${running.length - 3} more running →`}</a> : null}
    </section>
  )
}

// Last12 is the runs-per-hour strip: every hour a bar, passed soft green with a
// full-green cap, running blue, failed red on top, the rest neutral. Static on
// Home (no hover), with a legend and the failures named.
function Last12({ runs: rs }: { runs: RunSummary[] }): JSX.Element {
  const now = Date.now()
  const buckets: { passed: number; failed: number; running: number; other: number }[] = []
  for (let i = 11; i >= 0; i--) {
    const end = now - i * HOUR
    const startB = end - HOUR
    const inB = rs.filter((r) => {
      const t = startedMs(r)
      return t >= startB && t < end
    })
    buckets.push({
      passed: inB.filter((r) => r.status === 'ok').length,
      failed: inB.filter((r) => r.status === 'failed').length,
      running: inB.filter((r) => r.status === 'running').length,
      other: inB.filter((r) => r.status === 'retried').length,
    })
  }
  const max = Math.max(1, ...buckets.map((b) => b.passed + b.failed + b.running + b.other))
  const total = rs.length
  const counts = {
    passed: rs.filter((r) => r.status === 'ok').length,
    failed: rs.filter((r) => r.status === 'failed').length,
    running: rs.filter((r) => r.status === 'running').length,
  }

  return (
    <section class="panel" aria-label="Last 12 hours">
      <div class="panel-head">
        <h2 class="panel-title serif">Last 12 hours</h2>
        <span class="panel-sub">{total} runs</span>
      </div>
      <div class="chart" role="img" aria-label={`${total} runs over the last 12 hours`}>
        {buckets.map((b) => {
          const h = (n: number) => `${(n / max) * 100}%`
          return (
            <div class="bar">
              <div class="bar-stack">
                {b.failed ? <span class="seg failed" style={{ height: h(b.failed) }} /> : null}
                {b.running ? <span class="seg running" style={{ height: h(b.running) }} /> : null}
                {b.other ? <span class="seg other" style={{ height: h(b.other) }} /> : null}
                {b.passed ? <span class="seg passed" style={{ height: h(b.passed) }} /> : null}
              </div>
            </div>
          )
        })}
      </div>
      <div class="legend">
        <LegendItem cls="passed" word="Passed" n={counts.passed} />
        <LegendItem cls="running" word="Running" n={counts.running} />
        <LegendItem cls="failed" word="Failed" n={counts.failed} />
      </div>
    </section>
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

function Activity({
  needs,
  running,
  finished,
}: {
  needs: RunSummary[]
  running: RunSummary[]
  finished: RunSummary[]
}): JSX.Element {
  const [shown, setShown] = useState(20)
  const sentence = `Last 12 hours: ${needs.length + running.length + finished.length} runs. ${needs.length} need attention, ${running.length} running, ${finished.length} finished.`
  return (
    <section class="panel" aria-label="Activity">
      <div class="panel-head">
        <h2 class="panel-title serif">Activity</h2>
      </div>
      <p class="panel-say">{sentence}</p>
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
          {needs.map((r) => (
            <Row r={r} rowState="fail" />
          ))}
          {running.length ? <GroupHead title="Running now" n={running.length} /> : null}
          {running.map((r) => (
            <Row r={r} />
          ))}
          {finished.length ? <GroupHead title="Finished" n={finished.length} /> : null}
          {finished.slice(0, shown).map((r) => (
            <Row r={r} quiet />
          ))}
        </tbody>
      </table>
      </div>
      {finished.length > shown ? (
        <button class="more-btn" type="button" onClick={() => setShown((s) => s + 20)}>
          Show 20 more of {finished.length}
        </button>
      ) : null}
      {needs.length + running.length + finished.length === 0 ? <p class="calm">No runs recorded yet.</p> : null}
    </section>
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
    <tr class={quiet ? 'quiet clickable' : 'clickable'} data-row={rowState} onClick={() => (location.hash = '#/runs/' + r.id)}>
      <td>
        <div class="cell-title">{r.workflow}</div>
        {r.repo ? <div class="cell-sub mono">{r.repo}{r.number ? `#${r.number}` : ''}</div> : null}
      </td>
      <td>
        <StatusMark s={m.s} word={m.word} />
      </td>
      <td>{ago(r.started)}</td>
      <td>{r.status === 'running' ? humanDuration(r.duration_ms) : humanDuration(r.duration_ms)}</td>
      <td class="mono id">{r.id}</td>
    </tr>
  )
}

function Health(): JSX.Element | null {
  const st = status.value
  if (!st) return null
  return (
    <section class="panel" aria-label="For your information">
      <div class="panel-head">
        <h2 class="panel-title serif">Machine</h2>
      </div>
      <dl class="facts">
        <div>
          <dt>Version</dt>
          <dd class="mono">{st.version}</dd>
        </div>
        <div>
          <dt>PID</dt>
          <dd class="mono">{st.pid}</dd>
        </div>
        <div>
          <dt>Uptime</dt>
          <dd>{uptime(st.uptime_sec)}</dd>
        </div>
        <div>
          <dt>Config</dt>
          <dd class="mono">{st.config_sha || '—'}</dd>
        </div>
        <div>
          <dt>Dispatch</dt>
          <dd>{st.paused ? 'Paused' : 'Running'}</dd>
        </div>
      </dl>
    </section>
  )
}
