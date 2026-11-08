// Compare two runs of a workflow (prototype js/screens/runs.js
// UI.route('runs/:id/compare/:other?')): pick an earlier run, then the two
// side by side step by step, and the chosen step's outputs as a diff.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { RunDetailResponse, RunStep, RunSummary } from '../api/types'
import { runs } from '../state'
import { StatusMark } from './StatusMark'
import { Icon } from './Icon'
import { StepDiff, UnifiedDiff, agoText, durText, runMark, runTitle } from './RunBits'
import { runTarget } from '../lib/runwords'
import { go } from '../router'
import { humanError } from '../api/errors'
import '../styles/runs-repass.css'

const S2: Record<string, string> = { ok: 'ok', running: 'run', failed: 'fail', stopped: 'skip', skipped: 'skip', idle: 'skip' }
const SL: Record<string, string> = { ok: 'passed', running: 'running', failed: 'failed', stopped: 'stopped', retried: 'retried' }

function total(steps: RunStep[]): number {
  return steps.reduce((n, s) => n + (s.started ? Math.max(1, s.duration_ms || 0) : 0), 0)
}

function Cell({ s }: { s?: RunStep }): JSX.Element {
  if (!s) return <span class="dl-sub">—</span>
  return (
    <>
      <StatusMark s={S2[s.status] || 'skip'} word={s.status === 'idle' ? 'Not reached' : runMark(s.status).word} />
      {s.started ? <> <span class="dl-sub">{durText(Math.max(1, s.duration_ms || 0))}</span></> : null}
    </>
  )
}

function Delta({ x, y }: { x?: number; y?: number }): JSX.Element | null {
  if (x == null || y == null) return null
  // from the seconds as shown (durText's rounding), so "1 s" against "1 s" is never "+1 s"
  const sec = (ms: number) => (ms > 0 ? Math.max(1, Math.round(ms / 1000)) : 0)
  const d = sec(y) - sec(x)
  if (!d) return <span class="dl-sub">0 s</span>
  return <span class={y > x * 1.2 ? 'warn-t' : 'dl-sub'}>{d > 0 ? '+' : '−'}{durText(Math.abs(d) * 1000)}</span>
}

function RunCell({ x }: { x: RunSummary }): JSX.Element {
  const m = runMark(x.status)
  return (
    <>
      <StatusMark s={m.s} word={m.word} /> <a class="mono dl-link" href={'#/runs/' + x.id}>{x.id}</a>{' '}
      <span class="dl-sub">{[runTarget(x), agoText(x.started)].filter(Boolean).join(' · ')}</span>
    </>
  )
}

export function RunCompare({ id, other }: { id: string; other?: string }): JSX.Element {
  const [a, setA] = useState<RunDetailResponse | null>(null)
  const [b, setB] = useState<RunDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [errB, setErrB] = useState('')
  const [pick, setPick] = useState<string>('')

  useEffect(() => {
    api.run(id).then(setA).catch((e) => setErr(humanError(e)))
  }, [id])
  useEffect(() => {
    setB(null)
    setErrB('')
    if (other) api.run(other).then(setB).catch((e) => setErrB(humanError(e)))
  }, [other])

  if (err) {
    return (
      <div class="empty" role="status">
        <h2 class="serif">There is no run {id}</h2>
        <a class="btn" href="#/activity">Back to Activity</a>
      </div>
    )
  }
  if (!a) return <p class="dl-sub">Loading…</p>
  const ra = a.run
  const same = runs.value.filter((x) => x.workflow === ra.workflow && x.id !== ra.id && x.status !== 'running')
  const crumb = `${runTitle(ra)}${runTarget(ra) ? ` on ${runTarget(ra)}` : ''}`
  const head = (
    <>
      <nav class="dl-crumbs" aria-label="Where you are">
        <a href="#/activity">Activity</a>
        <span class="sep" aria-hidden="true">›</span>
        <a href={'#/runs/' + ra.id}>{crumb}</a>
        <span class="sep" aria-hidden="true">›</span>
        <span class="cur" aria-current="page">Compare</span>
      </nav>
      <div class="dl-phead">
        <div>
          <h1>Compare two {ra.workflow} runs</h1>
        </div>
      </div>
    </>
  )

  if (!other) {
    return (
      <div class="cmp-page">
        {head}
        <section class="dl-card">
          <header class="dl-ch">
            <h2>Pick a run to compare with</h2>
          </header>
          <ul class="dl-list">
            {same.length ? (
              same.map((x) => {
                const m = runMark(x.status)
                return (
                  <li>
                    <a class="dl-row-a" href={`#/runs/${ra.id}/compare/${x.id}`}>
                      <span class="dl-ng">
                        <StatusMark s={m.s} mkOnly label={m.word} />
                      </span>
                      <span class="dl-nt">
                        <b>
                          {x.workflow} <span class="tgt">{runTarget(x)}</span>
                        </b>
                        <span class="dl-why">
                          {m.word} · {agoText(x.started)} · <span class="mono">{x.id}</span>
                        </span>
                      </span>
                    </a>
                  </li>
                )
              })
            ) : (
              <li class="dl-sub">No other run of this workflow to compare with.</li>
            )}
          </ul>
        </section>
      </div>
    )
  }
  if (errB) {
    return (
      <div class="cmp-page">
        {head}
        <div class="empty" role="status">
          <h2 class="serif">There is no run {other}</h2>
        </div>
      </div>
    )
  }
  if (!b) return <div class="cmp-page">{head}<p class="dl-sub">Loading…</p></div>
  const rb = b.run
  const ids = [...new Set(a.steps.map((s) => s.id).concat(b.steps.map((s) => s.id)))]
  const stepA = (i: string) => a.steps.find((s) => s.id === i)
  const stepB = (i: string) => b.steps.find((s) => s.id === i)
  const differs = (i: string) => {
    const x = stepA(i)
    const y = stepB(i)
    return !x || !y || x.status !== y.status || (x.outputs_yaml || '') !== (y.outputs_yaml || '')
  }
  const cur = pick && ids.includes(pick) ? pick : ids.find((i) => { const x = stepA(i); const y = stepB(i); return !x || !y || x.status !== y.status }) || ids[0]
  const sa = stepA(cur)
  const sb = stepB(cur)
  const yA = sa?.outputs_yaml || ''
  const yB = sb?.outputs_yaml || ''
  return (
    <div class="cmp-page">
      {head}
      <div class="dl-filters">
        <span class="dl-sub">This run</span>
        <RunCell x={ra} />
        <span class="dl-sub">compared with</span>
        <label class="dl-chip on">
          <Icon name="split" />
          <select aria-label="Compare with" value={rb.id} onChange={(e) => go(`runs/${ra.id}/compare/${(e.target as HTMLSelectElement).value}`)}>
            {same.map((x) => (
              <option value={x.id} selected={x.id === rb.id}>
                {[x.id, SL[x.status] || x.status, runTarget(x)].filter(Boolean).join(' · ')}
              </option>
            ))}
          </select>
        </label>
      </div>
      <section class="dl-card dl-flush">
        <table class="dl-tbl cmp-tbl">
          <thead>
            <tr>
              <th>Step</th>
              <th>This run</th>
              <th>Compared with</th>
              <th class="num">Difference</th>
            </tr>
          </thead>
          <tbody>
            {ids.map((i) => {
              const x = stepA(i)
              const y = stepB(i)
              return (
                <tr
                  class={'click' + (i === cur ? ' on' : '')}
                  data-row={i === cur ? 'sel' : undefined}
                  data-step={i}
                  data-step-row
                  tabIndex={0}
                  onClick={() => setPick(i)}
                  onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setPick(i)}
                >
                  <td>
                    <b>{i}</b>
                    {differs(i) ? <> <span class="tag" data-s="wait">differs</span></> : null}
                  </td>
                  <td><Cell s={x} /></td>
                  <td><Cell s={y} /></td>
                  <td class="num">{x?.started && y?.started ? <Delta x={x.duration_ms || 0} y={y.duration_ms || 0} /> : null}</td>
                </tr>
              )
            })}
            <tr>
              <td class="dl-sub">Total</td>
              <td>{durText(total(a.steps))}</td>
              <td>{durText(total(b.steps))}</td>
              <td class="num"><Delta x={total(a.steps)} y={total(b.steps)} /></td>
            </tr>
          </tbody>
        </table>
      </section>
      <section class="dl-card">
        <header class="dl-ch">
          <h2>What {cur} produced</h2>
          <span class="dl-sub">this run → compared with</span>
        </header>
        {yA || yB ? <UnifiedDiff a={yA} b={yB} file={`${cur} outputs`} /> : <p class="dl-sub">Neither run recorded outputs for this step.</p>}
      </section>
      {sa && sb && (sa.diff || sb.diff) ? (
        <div class="dl-cols" style="grid-template-columns:1fr 1fr">
          <section class="dl-card">
            <header class="dl-ch"><h2>{ra.id} · {cur} diff</h2></header>
            {sa.diff ? <StepDiff diff={sa.diff} /> : <p class="dl-sub">No diff</p>}
          </section>
          <section class="dl-card">
            <header class="dl-ch"><h2>{rb.id} · {cur} diff</h2></header>
            {sb.diff ? <StepDiff diff={sb.diff} /> : <p class="dl-sub">No diff</p>}
          </section>
        </div>
      ) : null}
    </div>
  )
}
