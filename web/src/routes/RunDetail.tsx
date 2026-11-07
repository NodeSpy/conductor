// One run in full (design §4.3, prototype js/screens/runs.js UI.route('runs/:id')):
// the step tree, each step's recorded inputs/outputs/diff, and the retry/stop
// controls. Live runs refresh on each SSE event; everything shown comes from
// the signed history record.
//
// Markup and classes are the prototype's (css/daylight.css .dl-tree, .dl-code,
// .dl-facts, .dl-crumbs): the plain title, the lead sentence, the status mark,
// the step tree, the selected step's card, and Details.
//
// Left out (prototype-only, no backend yet): Compare with an earlier run, the
// ⋯ menu (Dismiss from Needs you already has its own control elsewhere), Open
// in paseo (no live paseo session concept here) and the live log tail (a
// running step says it's working; its recorded output appears once it's
// stepped past, same as the prototype's non-live tabs).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { RunDetailResponse, RunStep } from '../api/types'
import { liveTick, statusMark } from '../state'
import { StatusMark } from '../components/StatusMark'
import { humanDuration, ago } from '../lib/format'
import { go } from '../router'
import { humanError } from '../api/errors'

type Tab = 'outputs' | 'inputs' | 'diff' | 'error'

export function RunDetail({ id }: { id: string }): JSX.Element {
  const [data, setData] = useState<RunDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [sel, setSel] = useState<string>('')
  const [tab, setTab] = useState<Tab>('outputs')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [retryOpen, setRetryOpen] = useState(false)
  const tick = liveTick.value

  const reload = () =>
    api
      .run(id)
      .then((d) => {
        setData(d)
        setErr('')
      })
      .catch((e) => setErr(humanError(e)))

  useEffect(() => {
    void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  // Refresh a live run when SSE pulses.
  useEffect(() => {
    if (data?.live) void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick])

  if (err) {
    return (
      <div>
        <Crumbs id={id} notFound />
        <div class="empty" role="status">
          <h2 class="serif">There is no run {id}</h2>
          <p>It may be older than the 14 days runs are kept.</p>
          <a class="btn" href="#/activity">Back to Activity</a>
        </div>
      </div>
    )
  }
  if (!data) return <p class="dl-sub">Loading…</p>

  const r = data.run
  const m = statusMark(r.status)
  const steps = data.steps
  const current =
    steps.find((s) => s.id === sel) ||
    steps.find((s) => s.status === 'running') ||
    steps.find((s) => s.id === r.failed_step) ||
    steps[steps.length - 1]

  const stop = async () => {
    if (!confirm('Stop this run? It stops mid-step; anything already pushed stays. You can retry it from any step.')) return
    setBusy(true)
    try {
      const ack = await api.stopRun(id)
      setNotice(ack.message)
      await reload()
    } catch (e) {
      setNotice(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-run-detail={id}>
      <Crumbs id={id} title={runTitle(r)} />
      <div class="dl-phead">
        <div>
          <h1 class="dl-h1-long">{runTitle(r)}</h1>
          <p class="dl-lead">{lead(data)}</p>
        </div>
        <StatusMark s={m.s} word={m.word} lg />
      </div>

      {notice ? <div class="banner info">{notice}</div> : null}

      <div class="dl-actions">
        {data.live ? (
          <button class="btn secondary" type="button" disabled={busy} onClick={stop}>
            Stop run…
          </button>
        ) : (
          <button class="btn primary" type="button" onClick={() => setRetryOpen((v) => !v)}>
            Retry…
          </button>
        )}
      </div>

      {retryOpen && !data.live ? (
        <RetryForm
          steps={steps}
          failedStep={r.failed_step}
          onCancel={() => setRetryOpen(false)}
          onDone={(msg, newID) => {
            setRetryOpen(false)
            setNotice(msg)
            if (newID) go('runs/' + newID)
          }}
          id={id}
        />
      ) : null}

      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>What happened</h2>
              <span class="dl-sub">
                {steps.length} step{steps.length === 1 ? '' : 's'} · {data.live ? 'running' : humanDuration(r.duration_ms)}
              </span>
            </header>
            <StepTree steps={steps} sel={current?.id || ''} onSel={(sid) => { setSel(sid); setTab('outputs') }} />
          </section>

          {current ? <StepCard step={current} tab={tab} setTab={setTab} /> : null}
        </div>

        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch"><h2>Details</h2></header>
            <dl class="dl-facts">
              <dt>Started</dt>
              <dd>{ago(r.started)}</dd>
              <dt>{data.live ? 'Running for' : 'Took'}</dt>
              <dd>{humanDuration(r.duration_ms)}</dd>
              <dt>Triggered by</dt>
              <dd>{data.trigger || '—'}</dd>
              <dt>Workflow</dt>
              <dd><a class="dl-link" href={'#/workflows/' + encodeURIComponent(r.workflow)}>{r.workflow}</a></dd>
              {r.retry_of ? (
                <>
                  <dt>Retry of</dt>
                  <dd><a class="mono dl-link" href={'#/runs/' + r.retry_of}>{r.retry_of}</a></dd>
                </>
              ) : null}
            </dl>
            <details class="dl-audit-d">
              <summary>For audit: the run id and its signed record</summary>
              <dl class="dl-facts">
                <dt>Run</dt>
                <dd class="mono">{r.id}</dd>
                <dt>Record</dt>
                <dd>{data.live ? 'signed when it finishes' : 'signed; the signature checks out'}</dd>
              </dl>
            </details>
          </section>
        </div>
      </div>
    </div>
  )
}

function Crumbs({ id, title, notFound }: { id: string; title?: string; notFound?: boolean }): JSX.Element {
  return (
    <nav class="dl-crumbs run-nav" aria-label="Where you are">
      <a href="#/activity">Activity</a>
      <span class="sep" aria-hidden="true">›</span>
      <span class="cur" aria-current="page">{notFound ? "A run that isn't here" : title || id}</span>
    </nav>
  )
}

function runTitle(r: RunDetailResponse['run']): string {
  const target = r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : ''
  return target ? `${r.workflow} on ${target}` : r.workflow
}

function lead(d: RunDetailResponse): string {
  const r = d.run
  if (d.live) {
    const running = d.steps.find((s) => s.status === 'running')
    return `${d.trigger}; now on step “${running?.id || '…'}”.`
  }
  if (r.status === 'ok') return `Finished in ${humanDuration(r.duration_ms)}; every step passed.`
  if (r.status === 'failed') return `Failed${r.failed_step ? ` at “${r.failed_step}”` : ''}${r.error ? `: ${r.error}` : ''}.`
  return d.trigger || ''
}

const STEP_MARK: Record<string, string> = {
  ok: 'ok',
  running: 'run',
  failed: 'fail',
  skipped: 'skip',
  idle: 'skip',
}

function StepTree({ steps, sel, onSel }: { steps: RunStep[]; sel: string; onSel: (id: string) => void }): JSX.Element {
  return (
    <ol class="dl-tree">
      {steps.map((s, i) => {
        const disabled = s.status === 'idle'
        return (
          <li class={s.status === 'failed' ? 'failed' : s.status === 'idle' ? 'skipped' : ''}>
            <button
              type="button"
              class={'tl' + (s.id === sel ? ' on' : '')}
              disabled={disabled}
              aria-pressed={s.id === sel}
              onClick={() => !disabled && onSel(s.id)}
            >
              <span class="tn">{i + 1}</span>
              <StatusMark s={STEP_MARK[s.status] || 'skip'} mkOnly />
              <span class="tt">
                <b>{s.id}</b>
                <span>{stepSentence(s)}</span>
              </span>
              <span />
              <em>{s.duration_ms ? humanDuration(s.duration_ms) : ''}</em>
            </button>
          </li>
        )
      })}
    </ol>
  )
}

function stepSentence(s: RunStep): string {
  switch (s.status) {
    case 'idle':
      return 'Not reached'
    case 'running':
      return 'Working on it now'
    case 'failed':
      return s.error || 'Failed'
    case 'skipped':
      return 'Skipped'
    default:
      return 'Done'
  }
}

function tabsFor(s: RunStep): Tab[] {
  const t: Tab[] = []
  if (s.outputs_yaml) t.push('outputs')
  if (s.inputs_yaml) t.push('inputs')
  if (s.diff) t.push('diff')
  if (s.error) t.push('error')
  return t
}

function StepCard({ step, tab, setTab }: { step: RunStep; tab: Tab; setTab: (t: Tab) => void }): JSX.Element {
  const tabs = tabsFor(step)
  const active = tabs.includes(tab) ? tab : tabs[0]
  const isLive = step.status === 'running'
  return (
    <section class="dl-card">
      <header class="dl-ch">
        <h2>{step.id}</h2>
        <StatusMark s={STEP_MARK[step.status] || 'skip'} word={step.status === 'idle' ? 'Not reached' : undefined} />
        <span class="dl-sp" />
        {!isLive && tabs.length > 1 ? (
          <div class="seg" role="group">
            {tabs.map((t) => (
              <button type="button" aria-pressed={t === active} class={t === active ? 'on' : ''} onClick={() => setTab(t)}>
                {t[0].toUpperCase() + t.slice(1)}
              </button>
            ))}
          </div>
        ) : null}
      </header>
      <StepBody step={step} active={active} isLive={isLive} />
      {(step.runtime || step.model) && !isLive ? (
        <p class="dl-sub step-env">
          {step.runtime ? `runtime ${step.runtime}` : ''}
          {step.runtime && step.model ? ' · ' : ''}
          {step.model ? `model ${step.model}` : ''}
          {step.tokens ? ` · ${step.tokens.toLocaleString()} tokens` : ''}
        </p>
      ) : null}
    </section>
  )
}

function StepBody({ step, active, isLive }: { step: RunStep; active: Tab; isLive: boolean }): JSX.Element {
  if (isLive) {
    return <p class="dl-sub">This step is running now. Its recorded output will appear when it finishes; the page refreshes live.</p>
  }
  if (step.status === 'idle') {
    return <p class="dl-sub">Not reached: the run stopped before this step, so it never ran.</p>
  }
  switch (active) {
    case 'inputs':
      return <pre class="dl-code">{step.inputs_yaml}</pre>
    case 'diff':
      return <DiffView diff={step.diff || ''} />
    case 'error':
      return <pre class="dl-code err-code">{step.error}</pre>
    case 'outputs':
    default:
      return step.outputs_yaml ? <pre class="dl-code">{step.outputs_yaml}</pre> : <p class="dl-sub">No recorded output for this step.</p>
  }
}

function DiffView({ diff }: { diff: string }): JSX.Element {
  return (
    <pre class="dl-code diff">
      {diff.split('\n').map((l) => {
        const cls = l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : l.startsWith('@@') ? 'hunk' : ''
        return <span class={'diff-l ' + cls}>{l + '\n'}</span>
      })}
    </pre>
  )
}

function RetryForm({
  steps,
  failedStep,
  id,
  onCancel,
  onDone,
}: {
  steps: RunStep[]
  failedStep?: string
  id: string
  onCancel: () => void
  onDone: (msg: string, newID: string) => void
}): JSX.Element {
  const runnable = steps.filter((s) => s.status !== 'idle')
  const [from, setFrom] = useState(failedStep || (runnable[0]?.id ?? ''))
  const [force, setForce] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const ack = await api.retryRun(id, { from: from === '$start' ? '' : from, force })
      onDone(ack.message, ack.run_id || '')
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="dl-card retry-form">
      <header class="dl-ch"><h2>Retry from a step</h2></header>
      <p class="dl-sub">Re-runs the pinned trigger. Steps before the chosen one are replayed from their recorded outputs; nothing they did is repeated.</p>
      <label class="field">
        <span class="field-label">Start again from</span>
        <select value={from} onChange={(e) => setFrom((e.target as HTMLSelectElement).value)}>
          {runnable.map((s) => (
            <option value={s.id}>{s.id}{s.id === failedStep ? ' (where it failed)' : ''}</option>
          ))}
          <option value="$start">the beginning</option>
        </select>
      </label>
      <label class="field-check">
        <input type="checkbox" checked={force} onChange={(e) => setForce((e.target as HTMLInputElement).checked)} />
        <span>
          Run the earlier steps again too{' '}
          <span class="dl-sub">(otherwise they're replayed from what they recorded; running them again repeats what they did, so with sign-in on it asks you to confirm)</span>
        </span>
      </label>
      {err ? <div class="banner err">{err}</div> : null}
      <div class="dl-actions tight">
        <button class="btn primary" type="button" disabled={busy} onClick={submit}>Retry</button>
        <button class="btn" type="button" onClick={onCancel}>Cancel</button>
      </div>
    </section>
  )
}
