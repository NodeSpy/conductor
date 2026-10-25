// One run in full (design §4.3): the step tree, each step's recorded
// inputs/outputs/diff, and the retry/stop controls. Live runs refresh on each
// SSE event; everything shown comes from the signed history record.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { RunDetailResponse, RunStep } from '../api/types'
import { liveTick, statusMark } from '../state'
import { StatusMark } from '../components/StatusMark'
import { humanDuration, ago } from '../lib/format'
import { go } from '../router'

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
      .catch((e) => setErr(String(e instanceof Error ? e.message : e)))

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
      <div class="col">
        <Crumbs id={id} />
        <div class="empty">
          <h2>There is no run {id}</h2>
          <p class="muted">It may be older than the 14 days runs are kept.</p>
          <a class="btn" href="#/activity">Back to Activity</a>
        </div>
      </div>
    )
  }
  if (!data) return <div class="col"><p class="muted">Loading…</p></div>

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
      setNotice(String(e instanceof Error ? e.message : e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col" data-run-detail={id}>
      <Crumbs id={id} title={runTitle(r)} />
      <div class="run-head">
        <div>
          <h1 class="screen-title serif">{runTitle(r)}</h1>
          <p class="run-lead">{lead(data)}</p>
        </div>
        <StatusMark s={m.s} word={m.word} lg />
      </div>

      {notice ? <div class="banner info">{notice}</div> : null}

      <div class="run-actions">
        {data.live ? (
          <button class="btn" type="button" disabled={busy} onClick={stop}>
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

      <div class="run-cols">
        <div class="run-main">
          <section class="panel">
            <div class="panel-head">
              <span class="panel-title serif">What happened</span>
              <span class="panel-sub">
                {steps.length} step{steps.length === 1 ? '' : 's'} · {data.live ? 'running' : humanDuration(r.duration_ms)}
              </span>
            </div>
            <StepTree steps={steps} sel={current?.id || ''} onSel={(sid) => { setSel(sid); setTab('outputs') }} />
          </section>

          {current ? <StepCard step={current} tab={tab} setTab={setTab} /> : null}
        </div>

        <div class="run-side">
          <section class="panel">
            <div class="panel-head"><span class="panel-title serif">Details</span></div>
            <dl class="facts">
              <div><dt>Started</dt><dd>{ago(r.started)}</dd></div>
              <div><dt>{data.live ? 'Running for' : 'Took'}</dt><dd>{humanDuration(r.duration_ms)}</dd></div>
              <div><dt>Triggered by</dt><dd>{data.trigger || '—'}</dd></div>
              <div><dt>Workflow</dt><dd><a class="link" href={'#/workflows/' + encodeURIComponent(r.workflow)}>{r.workflow}</a></dd></div>
              {r.retry_of ? <div><dt>Retry of</dt><dd><a class="link mono" href={'#/runs/' + r.retry_of}>{r.retry_of}</a></dd></div> : null}
              <div><dt>Run</dt><dd class="mono">{r.id}</dd></div>
              <div><dt>Record</dt><dd>{data.live ? 'signed when it finishes' : 'signed; the signature checks out'}</dd></div>
            </dl>
          </section>
        </div>
      </div>
    </div>
  )
}

function Crumbs({ id, title }: { id: string; title?: string }): JSX.Element {
  return (
    <nav class="crumbs" aria-label="Breadcrumb">
      <a href="#/activity">Activity</a>
      <span class="crumbs-sep">/</span>
      <span>{title || id}</span>
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
    <ol class="step-tree">
      {steps.map((s, i) => {
        const disabled = s.status === 'idle'
        return (
          <li class={s.status === 'failed' ? 'failed' : s.status === 'idle' ? 'skipped' : ''}>
            <button
              type="button"
              class={'step-tl' + (s.id === sel ? ' on' : '')}
              disabled={disabled}
              aria-pressed={s.id === sel}
              onClick={() => !disabled && onSel(s.id)}
            >
              <span class="step-n">{i + 1}</span>
              <StatusMark s={STEP_MARK[s.status] || 'skip'} mkOnly />
              <span class="step-tt">
                <b>{s.id}</b>
                <span class="step-say">{stepSentence(s)}</span>
              </span>
              <span class="step-kind">{s.kind}</span>
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
    <section class="panel step-card">
      <div class="panel-head">
        <span class="panel-title serif">{step.id}</span>
        <StatusMark s={STEP_MARK[step.status] || 'skip'} word={step.status === 'idle' ? 'Not reached' : undefined} />
        <span class="panel-grow" />
        {!isLive && tabs.length > 1 ? (
          <div class="seg-tabs" role="tablist">
            {tabs.map((t) => (
              <button type="button" role="tab" aria-selected={t === active} class={'seg-tab' + (t === active ? ' on' : '')} onClick={() => setTab(t)}>
                {t[0].toUpperCase() + t.slice(1)}
              </button>
            ))}
          </div>
        ) : null}
      </div>
      <StepBody step={step} active={active} isLive={isLive} />
      {(step.runtime || step.model) && !isLive ? (
        <p class="muted small step-env">
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
    return <p class="muted">This step is running now. Its recorded output will appear when it finishes; the page refreshes live.</p>
  }
  if (step.status === 'idle') {
    return <p class="muted">Not reached: the run stopped before this step, so it never ran.</p>
  }
  switch (active) {
    case 'inputs':
      return <pre class="code">{step.inputs_yaml}</pre>
    case 'diff':
      return <DiffView diff={step.diff || ''} />
    case 'error':
      return <pre class="code err-code">{step.error}</pre>
    case 'outputs':
    default:
      return step.outputs_yaml ? <pre class="code">{step.outputs_yaml}</pre> : <p class="muted">No recorded output for this step.</p>
  }
}

function DiffView({ diff }: { diff: string }): JSX.Element {
  return (
    <pre class="code diff">
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
      setErr(String(e instanceof Error ? e.message : e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="panel retry-form">
      <div class="panel-head"><span class="panel-title serif">Retry from a step</span></div>
      <p class="muted">Re-runs the pinned trigger. Steps before the chosen one are replayed from their recorded outputs; nothing they did is repeated.</p>
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
        <span>Run the earlier steps again too <span class="muted">(otherwise they're replayed from what they recorded)</span></span>
      </label>
      {err ? <div class="banner err">{err}</div> : null}
      <div class="run-actions">
        <button class="btn primary" type="button" disabled={busy} onClick={submit}>Retry</button>
        <button class="btn" type="button" onClick={onCancel}>Cancel</button>
      </div>
    </section>
  )
}
