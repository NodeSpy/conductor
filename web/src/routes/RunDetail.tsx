// One run in full (design §4.3, prototype js/screens/runs.js UI.route('runs/:id')):
// the step tree, each step's recorded inputs/outputs/diff, and the retry/stop
// controls. Live runs refresh on each SSE event; everything shown comes from
// the signed history record.
//
// Markup and classes are the prototype's (css/daylight.css .dl-tree, .dl-code,
// .dl-facts, .dl-crumbs): the plain title, the lead sentence, the status mark,
// the step tree, the selected step's card, and Details.
//
// Left out (prototype-only, no backend yet): Compare with an earlier run
// (no "other runs of this workflow" read model wired here yet), Open in paseo
// (no live paseo session concept here) and a true Log tab/live log tail (the
// store never keeps a step's raw line-by-line log once it's finished — only
// its scrubbed inputs/outputs/error — so a running step just says it's
// working, and a finished one has no separate Log tab, same as the
// prototype's own tabsFor when a step recorded no log lines).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { EnvVarInfo, RunDetailResponse, RunStep } from '../api/types'
import { dismiss, dismissed, liveTick, statusMark } from '../state'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { Yaml } from '../components/Yaml'
import { humanDuration, ago, plural } from '../lib/format'
import { go } from '../router'
import { humanError } from '../api/errors'

type Tab = 'outputs' | 'inputs' | 'diff'

type AuditEntry = Record<string, unknown>

function hm(iso: string): string {
  return new Date(iso).toTimeString().slice(0, 5)
}

export function RunDetail({ id }: { id: string }): JSX.Element {
  const [data, setData] = useState<RunDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [sel, setSel] = useState<string>('')
  const [tab, setTab] = useState<Tab>('outputs')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [retryOpen, setRetryOpen] = useState(false)
  const [retryFrom, setRetryFrom] = useState<string | undefined>(undefined)
  const [audit, setAudit] = useState<AuditEntry[]>([])
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
    // The run's own slice of the audit trail (design: a separate collapsible
    // card, "N events for this run"): every real audit write that touches a
    // run carries its id in a "run" field (internal/engine's Audit calls), so
    // this filters the one audit feed client-side rather than adding a
    // server-side ?run= filter for a card that only ever shows a handful.
    api
      .audit()
      .then((r) => setAudit((r.entries || []).filter((e) => String((e as AuditEntry)['run'] ?? '') === id)))
      .catch(() => setAudit([]))
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

  // inNeeds/wasDismissed: whether this failed run is currently one of Home's
  // "Needs you" items (prototype's run-dismiss ⋯ item) — the one entry this
  // phase's ⋯ menu carries (Compare/Open-in-paseo are the two left-out items).
  const isFailed = r.status === 'failed'
  const wasDismissed = dismissed.value.includes(id)
  const inNeeds = isFailed && !wasDismissed

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
            <Icon name="x" />
            Stop run…
          </button>
        ) : r.status !== 'ok' ? (
          <button
            class="btn primary"
            type="button"
            onClick={() => {
              setRetryFrom(r.failed_step)
              setRetryOpen((v) => !v)
            }}
          >
            <Icon name="updates" />
            Retry{r.failed_step ? ` from "${r.failed_step}"` : ''}…
          </button>
        ) : null}
        {inNeeds ? <RunMoreMenu runID={id} onDismiss={() => dismiss(id)} /> : null}
        {data.live || r.status === 'ok' ? null : (
          <p class="dl-sub run-next-why">
            {r.failed_step
              ? `Steps before "${r.failed_step}" are replayed from their recorded outputs; only it runs again.`
              : 'Steps already recorded are replayed from their outputs; nothing they did repeats.'}
          </p>
        )}
      </div>

      {retryOpen && !data.live ? (
        <RetryForm
          steps={steps}
          failedStep={r.failed_step}
          initialFrom={retryFrom}
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

          {current ? (
            <StepCard
              step={current}
              tab={tab}
              setTab={setTab}
              onRetryFrom={
                !data.live && current.status !== 'idle'
                  ? () => {
                      setRetryFrom(current.id)
                      setRetryOpen(true)
                    }
                  : undefined
              }
            />
          ) : null}
        </div>

        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch"><h2>Details</h2></header>
            <dl class="dl-facts">
              <dt>Started</dt>
              <dd>{ago(r.started)} <span class="dl-sub">({hm(r.started)})</span></dd>
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
              <summary><Icon name="chev" />For audit: the run id and its signed record</summary>
              <dl class="dl-facts">
                <dt>Run</dt>
                <dd class="mono">{r.id}</dd>
                <dt>Record</dt>
                <dd>{data.live ? 'signed when it finishes' : 'signed; the signature checks out'}</dd>
              </dl>
            </details>
          </section>
          <AuditTrailCard entries={audit} runID={id} />
        </div>
      </div>
    </div>
  )
}

// AuditTrailCard: the run's own slice of the audit log, a separate
// collapsible card (prototype's run-audit details). A plain word-for-word
// rendering (event name, its detail line, how long ago) — the full Audit tab
// (System → Logs & audit) has the richer "who did what" formatting.
function AuditTrailCard({ entries, runID }: { entries: AuditEntry[]; runID: string }): JSX.Element {
  const shown = entries.slice(0, 6)
  return (
    <details class="dl-card dl-files-d run-audit">
      <summary class="panel-head">
        <h3>Audit trail</h3>
        <span class="muted small">{plural(entries.length, 'event')} for this run</span>
      </summary>
      {shown.length ? (
        <ul class="dl-list">
          {shown.map((a, i) => (
            <li class="dl-audit" key={i}>
              <span class="dl-nt">
                <b>{String(a['event'] ?? '').replace(/_/g, ' ')}</b>
                <span class="dl-why">{String(a['detail'] ?? '')}</span>
              </span>
              <span class="dl-age">{a['ts'] ? ago(String(a['ts'])) : ''}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p class="dl-sub">No audit entries recorded for this run.</p>
      )}
      <p class="dl-foot-note">
        <a class="dl-link" href={'#/system/logs?run=' + encodeURIComponent(runID)}>
          The full audit log for this run →
        </a>
      </p>
    </details>
  )
}

// RunMoreMenu: the run page's "⋯" (prototype's run-more), one item today —
// dismissing a failed run from Needs you is reversible, so it asks nothing
// (the toast there carries Undo; here a plain confirm-free action is enough).
function RunMoreMenu({ runID, onDismiss }: { runID: string; onDismiss: () => void }): JSX.Element {
  const [open, setOpen] = useState(false)
  const btnRef = useRef<HTMLButtonElement>(null)
  const items: MenuItem[] = [{ label: 'Dismiss from Needs you', icon: 'check', onClick: onDismiss }]
  return (
    <>
      <button ref={btnRef} class="icon-btn" type="button" aria-label={`More for this run`} onClick={() => setOpen((o) => !o)}>
        <Icon name="more" />
      </button>
      {open ? <Menu anchor={btnRef.current} items={items} onClose={() => setOpen(false)} label={`More for ${runID}`} /> : null}
    </>
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

// tabsFor matches the prototype's own set exactly (outputs, inputs, a real
// Log — never present here, see the header note — and diff): no separate
// "error" tab. A failed step's error is already the step-tree's one-line
// sentence (stepSentence) and the run's own lead sentence; StepBody falls
// back to showing it in the body only when there is nothing else to tab to.
function tabsFor(s: RunStep): Tab[] {
  const t: Tab[] = []
  if (s.outputs_yaml) t.push('outputs')
  if (s.inputs_yaml) t.push('inputs')
  if (s.diff) t.push('diff')
  return t
}

const STEP_WORD: Record<string, string> = {
  ok: 'Passed',
  running: 'Running',
  failed: 'Failed',
  skipped: 'Stopped',
  idle: 'Not reached',
}

function StepCard({
  step,
  tab,
  setTab,
  onRetryFrom,
}: {
  step: RunStep
  tab: Tab
  setTab: (t: Tab) => void
  onRetryFrom?: () => void
}): JSX.Element {
  const tabs = tabsFor(step)
  const active = tabs.includes(tab) ? tab : tabs[0]
  const isLive = step.status === 'running'
  return (
    <section class="dl-card">
      <header class="dl-ch">
        <h2>{step.id}</h2>
        <StatusMark s={STEP_MARK[step.status] || 'skip'} word={STEP_WORD[step.status] || 'Stopped'} />
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
      {isLive ? (
        <div class="dl-step-open">
          <button class="btn sm secondary" type="button" disabled title="Restarting a running step isn't wired up in this phase">
            <Icon name="undo" />
            Restart this step…
          </button>
        </div>
      ) : onRetryFrom ? (
        <div class="dl-step-open">
          <button class="btn sm secondary" type="button" onClick={onRetryFrom}>
            <Icon name="undo" />
            Retry from here…
          </button>
        </div>
      ) : null}
      <StepBody step={step} active={active} isLive={isLive} />
      {(step.runtime || step.model) && !isLive ? (
        <p class="dl-sub step-env">
          {step.runtime ? `runtime ${step.runtime}` : ''}
          {step.runtime && step.model ? ' · ' : ''}
          {step.model ? `model ${step.model}` : ''}
          {step.tokens ? ` · ${step.tokens.toLocaleString()} tokens` : ''}
        </p>
      ) : null}
      <EnvDisclosure step={step} isLive={isLive} />
    </section>
  )
}

function StepBody({ step, active, isLive }: { step: RunStep; active?: Tab; isLive: boolean }): JSX.Element {
  if (isLive) {
    return <p class="dl-sub">This step is running now. Its recorded output will appear when it finishes; the page refreshes live.</p>
  }
  if (step.status === 'idle') {
    return <p class="dl-sub">Not reached: the run stopped before this step, so it never ran.</p>
  }
  switch (active) {
    case 'inputs':
      return <Yaml text={step.inputs_yaml || ''} />
    case 'diff':
      return <DiffView diff={step.diff || ''} />
    case 'outputs':
      return step.outputs_yaml ? <Yaml text={step.outputs_yaml} /> : <p class="dl-sub">No recorded output for this step.</p>
    default:
      // No outputs/inputs/diff at all (e.g. a failed assert, which records
      // neither): the error is the only thing to show, same text as the
      // step tree's own sentence.
      return step.error ? <p class="dl-sub">{step.error}</p> : <p class="dl-sub">No recorded output for this step.</p>
  }
}

// EnvDisclosure: the run page's "Its environment" (prototype env-tech) —
// offered only for a step kind the config can name a real scope for (agent,
// code, decide; see internal/dashboard's stepEnvVars). Names and where they
// come from only, never a value.
function EnvDisclosure({ step, isLive }: { step: RunStep; isLive: boolean }): JSX.Element | null {
  if (isLive || !['agent', 'code', 'decide'].includes(step.kind)) return null
  const rows = step.env_vars || []
  return (
    <details class="dl-tech-d env-tech">
      <summary>
        <Icon name="chev" />
        Its environment <span class="dl-sub">the variables this step saw, by scope; values are never shown</span>
      </summary>
      {rows.length ? (
        <table class="tbl compact responsive">
          <thead>
            <tr>
              <th>name</th>
              <th>comes from</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((v: EnvVarInfo) => (
              <tr key={v.scope + ':' + v.name}>
                <td class="mono" data-l="name">{v.name}</td>
                <td class="mono" data-l="comes from">{v.scope}{v.ref ? ` (${v.ref})` : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p class="dl-sub">No env vars reach this step.</p>
      )}
    </details>
  )
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
  initialFrom,
  id,
  onCancel,
  onDone,
}: {
  steps: RunStep[]
  failedStep?: string
  initialFrom?: string
  id: string
  onCancel: () => void
  onDone: (msg: string, newID: string) => void
}): JSX.Element {
  const runnable = steps.filter((s) => s.status !== 'idle')
  const [from, setFrom] = useState(initialFrom || failedStep || (runnable[0]?.id ?? ''))
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
