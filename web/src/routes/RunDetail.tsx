// One run in full (design §4.3), a line-for-line port of the prototype's
// js/screens/runs.js UI.route('runs/:id'): the crumbs, the plain title and its
// lead sentence, one recommended next step (everything else in ⋯), the step
// tree, the selected step's card (Outputs / Inputs / Log / Diff, Retry from
// here or Restart this step, its environment), Details with the For-audit
// fold, and the run's own Audit trail. #/runs/:id/compare[/:other] is Compare.
//
// Everything is the daemon's: the signed run record (GET /api/v1/runs/:id),
// the run events over SSE while it runs, the audit log. The daemon keeps no
// step's raw output stream, so a step's Log is what conductor recorded about
// it (when it started, how it ended, its error), and the live log streams the
// run's own events as they arrive.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import { openEvents } from '../api/sse'
import type { RunDetailResponse, RunStep, RunSummary } from '../api/types'
import { dismiss, dismissed, liveTick, runs } from '../state'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { Yaml } from '../components/Yaml'
import { ConfirmModal, FormModal } from '../components/FormModal'
import { toast, ToastHost } from '../components/ChatBits'
import { EnvTech, StepDiff, agoText, durText, isFailedRun, plainError, retryAndFind, runDur, runMark, runTitle } from '../components/RunBits'
import { RunCompare } from '../components/RunCompare'
import { hm, runTarget } from '../lib/runwords'
import { plural } from '../lib/format'
import { go, seg } from '../router'
import { humanError } from '../api/errors'
import '../styles/runs-repass.css'

type Tab = 'outputs' | 'inputs' | 'log' | 'diff'
type AuditEntry = Record<string, unknown>
type LogLine = [string, string, string, string] // [time, source, kind, text]

const STEP_S: Record<string, string> = { ok: 'ok', running: 'run', failed: 'fail', stopped: 'skip', skipped: 'skip', idle: 'skip' }

function hms(iso?: string | number): string {
  return iso ? new Date(iso).toTimeString().slice(0, 8) : ''
}

/** A step's own elapsed time: recorded, or so far while it runs. */
function stepMs(s: RunStep): number {
  if (s.status === 'running' && s.started) return Date.now() - new Date(s.started).getTime()
  return s.duration_ms || 0
}

/** What conductor recorded about a step, as log lines: when it started, how it ended. */
function stepLog(s: RunStep): [string, string][] {
  if (!s.started) return []
  const out: [string, string][] = [['', `${hms(s.started)} started (${s.kind})`]]
  const end = s.duration_ms ? new Date(new Date(s.started).getTime() + s.duration_ms).toISOString() : s.started
  if (s.status === 'ok') out.push(['', `${hms(end)} passed in ${s.duration_ms ? `${s.duration_ms} ms` : 'under a millisecond'}`])
  else if (s.status === 'failed') out.push(['err', `${hms(end)} failed: ${s.error || 'no error recorded'}`])
  else if (s.status === 'stopped' || s.status === 'skipped') out.push(['', `${hms(end)} stopped${s.error ? `: ${s.error}` : ''}`])
  if (s.continued_on_error) out.push(['', 'the run went on past this step’s failure (continue_on_error)'])
  return out
}

/** A failed step with no outputs: its error is what it produced. */
function outputsOf(s: RunStep): string {
  if (s.outputs_yaml) return s.outputs_yaml
  if ((s.status === 'failed' || s.status === 'stopped') && s.error) return `error: ${JSON.stringify(s.error)}`
  return ''
}

function tabsFor(s: RunStep): [Tab, string][] {
  const t: [Tab, string][] = []
  if (outputsOf(s)) t.push(['outputs', 'Outputs'])
  if (s.inputs_yaml) t.push(['inputs', 'Inputs'])
  if (stepLog(s).length) t.push(['log', 'Log'])
  if (s.diff) t.push(['diff', 'Diff'])
  return t
}

function stepSentence(r: RunSummary, s: RunStep): string {
  if (s.status === 'idle') return 'Not reached'
  if (s.status === 'running') return 'Working on it now'
  if (s.status === 'failed' || s.status === 'stopped') return (r.failed_step === s.id && r.error ? plainError(r) : '') || s.error || 'Failed'
  const out = s.outputs_yaml || ''
  if (/^exit_code: 0$/m.test(out)) return 'Passed: every check came back clean'
  const sum = out.match(/^summary: (.+)$/m)
  return sum ? sum[1].replace(/^"|"$/g, '') : 'Done'
}

function auditDetail(a: AuditEntry): string {
  return Object.entries(a)
    .filter(([k, v]) => !['event', 'ts', 'run'].includes(k) && v !== '' && v !== 0 && v !== null && v !== false && typeof v !== 'object')
    .map(([k, v]) => `${k.replace(/_/g, ' ')} ${String(v)}`)
    .join(' · ')
}

export function RunDetail({ id }: { id: string }): JSX.Element {
  if (seg(2) === 'compare') return <RunCompare id={id} other={seg(3)} />
  return <RunPage id={id} />
}

function RunPage({ id }: { id: string }): JSX.Element {
  const [data, setData] = useState<RunDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [sel, setSel] = useState<string>('')
  const [tab, setTab] = useState<Tab>('outputs')
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [retryFrom, setRetryFrom] = useState<string | null>(null)
  const [ask, setAsk] = useState<null | { kind: 'stop' } | { kind: 'restart'; step: string }>(null)
  const [events, setEvents] = useState<LogLine[]>([])
  const [, setNow] = useState(0)
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
    setData(null)
    setSel('')
    setEvents([])
    void reload()
    api
      .audit()
      .then((r) => setAudit((r.entries || []) as AuditEntry[]))
      .catch(() => setAudit([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  // A live run refreshes on each SSE pulse; its elapsed times tick each second.
  useEffect(() => {
    if (data?.live) void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick])
  useEffect(() => {
    if (!data?.live) return
    const t = setInterval(() => setNow((n) => n + 1), 1000)
    // the run's own events, as they arrive: each one a log line
    const close = openEvents(['run'], {
      onRun: (d) => {
        if (d.run !== id) return
        const text =
          d.type === 'gate'
            ? `gate ${d.status || ''}${d.detail ? `: ${d.detail}` : ''}`
            : d.type === 'step_started'
              ? 'started'
              : d.type === 'step_done'
                ? `${d.status === 'ok' ? 'passed' : d.status || 'done'}${d.duration_ms ? ` in ${durText(d.duration_ms)}` : ''}${d.detail ? `: ${d.detail}` : ''}`
                : d.type === 'run_done'
                  ? `run ${d.status === 'ok' ? 'passed' : d.status || 'ended'}${d.detail ? `: ${d.detail}` : ''}`
                  : `${d.type.replace(/_/g, ' ')}${d.detail ? `: ${d.detail}` : ''}`
        setEvents((xs) => [...xs, [hms(Date.now()), d.step || '', d.status === 'failed' ? 'err' : '', text]])
      },
    })
    return () => {
      clearInterval(t)
      close()
    }
  }, [data?.live, id])

  if (err) {
    return (
      <div>
        <nav class="dl-crumbs run-nav" aria-label="Where you are">
          <a href="#/activity">Activity</a>
          <span class="sep" aria-hidden="true">›</span>
          <span class="cur" aria-current="page">A run that isn’t here</span>
        </nav>
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
  const live = data.live || r.status === 'running'
  const m = runMark(live ? 'running' : r.status)
  const steps = data.steps
  const failAt = r.failed_step || steps.find((s) => s.status === 'failed')?.id || ''
  const runningStep = steps.find((s) => s.status === 'running')
  const s =
    steps.find((x) => x.id === sel) ||
    runningStep ||
    steps.find((x) => x.id === failAt) ||
    steps.find((x) => x.kind === 'agent') ||
    steps[steps.length - 1]
  const target = runTarget(r)
  const title = `${runTitle({ workflow: r.workflow, status: live ? 'running' : r.status })}${target ? ` on ${target}` : ''}`
  const all = runs.value
  const same = all.filter((x) => x.workflow === r.workflow && x.id !== r.id && x.status !== 'running')
  const retry = all.filter((x) => x.retry_of === r.id).sort((a, b) => new Date(b.started).getTime() - new Date(a.started).getTime())[0]
  const orig = r.retry_of ? all.find((x) => x.id === r.retry_of) : undefined
  const inNeeds = r.status === 'failed' && !dismissed.value.includes(id)
  const runAudit = audit.filter((a) => String(a['run'] ?? '') === id)
  const retryFromStep = (rid: string) => String(audit.find((a) => a['event'] === 'retry_from_step' && a['run'] === rid)?.['from_step'] ?? '')
  const trig = data.trigger || 'Started by hand'

  // the lead: what happened, in a sentence (runs.js lead + UI.runStory)
  const at = steps.findIndex((x) => x.id === failAt)
  const maker = at > 0 ? steps.slice(0, at).reverse().find((x) => x.diff) : undefined
  const failed = steps[at]
  let lead: JSX.Element | string = ''
  if (r.retry_of && live) {
    const from = retryFromStep(r.retry_of)
    lead = (
      <>
        You retried it{from ? ` from “${from}”` : ''} {agoText(r.started)}. The first run:{' '}
        <a class="dl-link" href={'#/runs/' + r.retry_of}>{orig ? runTitle(orig) : r.retry_of}</a>
        {orig ? `, ${agoText(orig.started)}` : ''}. Now on step <b>{runningStep?.id || s.id}</b>.
      </>
    )
  } else if (isFailedRun(r) && !live) {
    const e = plainError(r)
    lead = (
      <>
        {r.status === 'stopped' ? 'Stopped' : 'Failed'} at step <b>{failAt}</b>
        {e ? `: ${e}` : ''}
        {/\.$/.test(e) ? '' : '.'}
      </>
    )
  } else if (live) {
    lead = (
      <>
        {trig} {agoText(r.started)}; now on step <b>{runningStep?.id || s.id}</b>.
      </>
    )
  } else if (r.status === 'ok') lead = `Finished in ${runDur(r)}; every step passed.`

  // one recommended next step, with its reason in a line; everything else in ⋯
  type Rec = { href?: string; from?: string; label: string; why: string }
  let rec1: Rec | null = null
  if (retry && !live) {
    const rf = retryFromStep(r.id)
    rec1 = {
      href: '#/runs/' + retry.id,
      label: retry.status === 'running' ? 'Open the retry' : 'Open its retry',
      why: `You retried it ${agoText(retry.started)}${rf ? ` from “${rf}”` : ''}; it ${retry.status === 'running' ? 'is running now' : retry.status === 'ok' ? 'passed' : isFailedRun(retry) ? 'failed too' : 'stopped'}.`,
    }
  } else if (!live && (isFailedRun(r) || r.status === 'retried')) {
    rec1 =
      maker && failed && failed.kind !== 'agent'
        ? { from: maker.id, label: `Retry from “${maker.id}”…`, why: `“${failAt}” failed on the change “${maker.id}” made, so redo that step; its agent sees the failure.` }
        : { from: failAt, label: `Retry${failAt ? ` from “${failAt}”` : ''}…`, why: '' }
  }
  const more: MenuItem[] = [
    rec1 && maker && failed && failed.kind !== 'agent' ? { label: `Rerun “${failAt}” only…`, icon: 'updates', onClick: () => setRetryFrom(failAt) } : null,
    same.length ? { label: 'Compare with an earlier run', icon: 'split', onClick: () => go(`runs/${id}/compare`) } : null,
    inNeeds
      ? {
          label: 'Dismiss from Needs you',
          icon: 'check',
          onClick: () => {
            dismiss(id)
            toast('Dismissed from Needs you', 'the run and its record stay in Activity', 'ok')
          },
        }
      : null,
  ].filter(Boolean) as MenuItem[]

  const openRetry = (from: string) => {
    const busy = all.find((x) => x.retry_of === r.id && x.status === 'running')
    if (busy) {
      toast('It’s already being retried', `opened the retry that’s running (${busy.id})`, 'run')
      go('runs/' + busy.id)
      return
    }
    setRetryFrom(from)
  }

  const facts: [string, JSX.Element | string][] = [
    ['Started', <>{agoText(r.started)} <span class="dl-sub">({hm(r.started)})</span></>],
    live ? ['Running for', durText(Date.now() - new Date(r.started).getTime())] : ['Took', runDur(r)],
    ['Triggered by', trig],
    ['Workflow', <a class="dl-link" href={'#/workflows/' + encodeURIComponent(r.workflow)}>{r.workflow}</a>],
  ]
  if (r.retry_of) facts.push(['Retry of', <a class="mono dl-link" href={'#/runs/' + r.retry_of}>{r.retry_of}</a>])

  return (
    <div data-run-detail={id}>
      <nav class="dl-crumbs run-nav" aria-label="Where you are">
        <a href="#/activity">Activity</a>
        <span class="sep" aria-hidden="true">›</span>
        <span class="cur" aria-current="page">{title}</span>
      </nav>
      <div class="dl-phead">
        <div>
          <h1 class="dl-h1-long">{title}</h1>
          {lead ? <p class="dl-lead">{lead}</p> : null}
        </div>
        <StatusMark s={m.s} word={m.word} lg />
      </div>

      <div class="dl-actions">
        {live ? (
          <>
            <button class="btn secondary" type="button" onClick={() => setAsk({ kind: 'stop' })}>
              <Icon name="x" />
              Stop run…
            </button>
            {more.length ? <MoreButton items={more} /> : null}
          </>
        ) : (
          <>
            {rec1 ? (
              rec1.href ? (
                <a class="btn primary" href={rec1.href}>{rec1.label}</a>
              ) : (
                <button class="btn primary" type="button" onClick={() => openRetry(rec1!.from || '')}>
                  <Icon name="updates" />
                  {rec1.label}
                </button>
              )
            ) : null}
            {more.length ? <MoreButton items={more} /> : null}
            {rec1 && rec1.why ? <p class="dl-sub run-next-why">{rec1.why}</p> : null}
          </>
        )}
      </div>

      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>What happened</h2>
              <span class="dl-sub">
                {plural(steps.length, 'step')} · {live ? 'running' : durText(r.duration_ms)}
              </span>
            </header>
            <ol class="dl-tree">
              {steps.map((x, i) => (
                <li class={x.status === 'failed' ? 'failed' : x.status === 'idle' ? 'skipped' : ''}>
                  <button
                    type="button"
                    class={'tl' + (x.id === s?.id ? ' on' : '')}
                    aria-pressed={x.id === s?.id}
                    aria-disabled={x.status === 'idle' ? 'true' : undefined}
                    data-tip={x.status === 'idle' ? 'Not reached: the run stopped before this step' : undefined}
                    data-step={x.id}
                    onClick={() => {
                      if (x.status === 'idle') {
                        toast(`“${x.id}” wasn’t reached`, 'the run stopped before it; the step that stopped it stays in view', 'idle')
                        return
                      }
                      setSel(x.id)
                    }}
                    onKeyDown={(e) => {
                      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
                      const btns = Array.from(document.querySelectorAll<HTMLButtonElement>('.dl-tree .tl'))
                      const next = btns[btns.indexOf(e.currentTarget as HTMLButtonElement) + (e.key === 'ArrowDown' ? 1 : -1)]
                      if (!next) return
                      e.preventDefault()
                      next.click()
                      next.focus()
                    }}
                  >
                    <span class="tn">{i + 1}</span>
                    <StatusMark s={STEP_S[x.status] || 'skip'} mkOnly label={runMark(x.status === 'running' ? 'running' : x.status === 'idle' ? '' : x.status).word} />
                    <span class="tt">
                      <b>{x.id}</b>
                      <span>{stepSentence(r, x)}</span>
                    </span>
                    <span />
                    <em>{x.started ? durText(Math.max(1, stepMs(x))) : ''}</em>
                  </button>
                </li>
              ))}
            </ol>
          </section>

          {s ? (
            <StepCard
              r={r}
              live={live}
              s={s}
              tab={tab}
              setTab={setTab}
              events={events}
              steps={steps}
              onRetry={() => openRetry(s.id)}
              onRestart={() => setAsk({ kind: 'restart', step: s.id })}
            />
          ) : null}
        </div>

        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Details</h2>
            </header>
            <dl class="dl-facts">
              {facts.map(([k, v]) => (
                <>
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </>
              ))}
            </dl>
            <details class="dl-audit-d">
              <summary>
                <Icon name="chev" />
                For audit: the run id and its signed record
              </summary>
              <dl class="dl-facts">
                <dt>Run</dt>
                <dd class="mono">{r.id}</dd>
                <dt>Record</dt>
                <dd>{live ? 'signed when it finishes' : 'signed; the signature checks out'}</dd>
              </dl>
            </details>
          </section>
          <details class="dl-card dl-files-d run-audit">
            <summary class="panel-head">
              <h3>Audit trail</h3>
              <span class="muted small">{plural(runAudit.length, 'event')} for this run</span>
            </summary>
            <ul class="dl-list">
              {runAudit.slice(0, 6).map((a) => (
                <li class="dl-audit">
                  <span class="dl-nt">
                    <b>{String(a['event'] ?? '').replace(/_/g, ' ')}</b>
                    <span class="dl-why">{auditDetail(a)}</span>
                  </span>
                  <span class="dl-age">{a['ts'] ? agoText(String(a['ts'])) : ''}</span>
                </li>
              ))}
              {runAudit.length ? null : <li class="dl-sub">Nothing about this run has been audited yet.</li>}
            </ul>
            <p class="dl-foot-note">
              <a class="dl-link" href={'#/system/logs/audit?run=' + encodeURIComponent(id)}>
                The full audit log for this run →
              </a>
            </p>
          </details>
        </div>
      </div>

      {retryFrom !== null ? (
        <FormModal
          caps={`${r.workflow.replace(/^bot:/, '')}${target ? ' on ' + target : ''}`}
          title="Retry from a step"
          intro="Re-runs the pinned trigger. Steps before the chosen one are replayed from their recorded outputs; nothing they did is repeated."
          testid="retry-form"
          fields={[
            {
              k: 'from',
              type: 'enum',
              label: 'Start again from',
              req: true,
              options: [...steps.map((x): [string, string] => [x.id, x.id + (x.id === failAt ? ' (where it failed)' : '')]), ['$start', 'the beginning']],
              def: retryFrom || failAt || steps[0]?.id,
            },
            { k: 'note', label: 'Tell the agent', readonly: true, mono: false, ph: 'A retry can’t carry a note yet', help: 'Optional. conductor can’t pass a note to the agent on a retry yet; it starts again from what the run recorded.' },
            { k: 'force', type: 'bool', label: 'Run the earlier steps again too', on: 'otherwise they’re replayed from what they recorded' },
          ]}
          submit="Retry"
          submitIcon="play"
          onClose={() => setRetryFrom(null)}
          onSubmit={async (v) => {
            const from = v.from === '$start' ? '' : v.from || ''
            try {
              const { newID } = await retryAndFind(id, from, v.force === 'true')
              toast(`Retrying from “${from || 'the beginning'}”`, 'the first run links to it', 'run')
              if (newID) go('runs/' + newID)
              else void reload()
            } catch (e) {
              return humanError(e)
            }
          }}
        />
      ) : null}

      {ask?.kind === 'stop' ? (
        <ConfirmModal
          title="Stop this run?"
          body="It stops mid-step; anything already pushed stays. You can retry it from any step."
          ok="Stop run"
          danger
          onClose={() => setAsk(null)}
          onOk={async () => {
            try {
              await api.stopRun(id)
              toast(`Stopped ${id}`, 'retry it from any step', 'fail')
              await waitStopped(id)
              await reload()
            } catch (e) {
              toast('Couldn’t stop it', humanError(e), 'fail')
            }
          }}
        />
      ) : null}
      {ask?.kind === 'restart' ? (
        <ConfirmModal
          title={`Restart ${ask.step}?`}
          body={
            <>
              This stops {id} first (the step gets a short grace to checkpoint), then starts a retry from <b>{ask.step}</b>. Steps before it are replayed from their recorded outputs; what this step did so far is discarded.
            </>
          }
          ok="Stop and restart"
          cancel="Keep it running"
          danger
          onClose={() => setAsk(null)}
          onOk={async () => {
            const step = ask.step
            try {
              await api.stopRun(id)
              await waitStopped(id)
              const { newID } = await retryAndFind(id, step, true)
              toast(`Restarted “${step}”`, 'the stopped run links to it', 'run')
              if (newID) go('runs/' + newID)
              else await reload()
            } catch (e) {
              toast('Couldn’t restart it', humanError(e), 'fail')
              await reload()
            }
          }}
        />
      ) : null}
      <ToastHost />
    </div>
  )
}

/** waitStopped waits (briefly) until the daemon says the run has ended. */
async function waitStopped(id: string): Promise<void> {
  for (let i = 0; i < 40; i++) {
    const d = await api.run(id).catch(() => null)
    if (d && !d.live && d.run.status !== 'running') return
    await new Promise((r) => setTimeout(r, 250))
  }
}

function MoreButton({ items }: { items: MenuItem[] }): JSX.Element {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLButtonElement>(null)
  return (
    <>
      <button ref={ref} class="icon-btn" type="button" aria-label="More for this run" onClick={() => setOpen((o) => !o)}>
        <Icon name="more" />
      </button>
      {open ? <Menu anchor={ref.current} items={items} onClose={() => setOpen(false)} label="More for this run" /> : null}
    </>
  )
}

function StepCard({
  r,
  live,
  s,
  tab,
  setTab,
  events,
  steps,
  onRetry,
  onRestart,
}: {
  r: RunSummary
  live: boolean
  s: RunStep
  tab: Tab
  setTab: (t: Tab) => void
  events: LogLine[]
  steps: RunStep[]
  onRetry: () => void
  onRestart: () => void
}): JSX.Element {
  const tabs = tabsFor(s)
  const active = tabs.some(([v]) => v === tab) ? tab : (tabs[0] || [''])[0]
  const running = s.status === 'running'
  const word = s.status === 'idle' ? 'Not reached' : s.status === 'stopped' || s.status === 'skipped' ? 'Stopped' : undefined
  let body: JSX.Element
  if (running) body = <LiveLog r={r} steps={steps} step={s} events={events} />
  else if (s.status === 'idle')
    body = <p class="dl-sub">Not reached: the run stopped at {r.failed_step || steps.find((x) => x.status === 'failed')?.id || 'an earlier step'}, so this step never ran.</p>
  else if (active === 'outputs') body = <Yaml text={outputsOf(s)} />
  else if (active === 'inputs') body = <Yaml text={s.inputs_yaml || ''} />
  else if (active === 'log')
    body = (
      <pre class="dl-code" style="--gut:5ch">
        {stepLog(s).map(([k, t], i) => (
          <span class={'ln' + (k === 'err' ? ' fail' : '')}>
            <i>{i + 1}</i>
            {t}
          </span>
        ))}
      </pre>
    )
  else if (active === 'diff') body = <StepDiff diff={s.diff || ''} />
  else body = <p class="dl-sub">This step recorded nothing to show.</p>
  return (
    <section class="dl-card" id="run-step">
      <header class="dl-ch">
        <h2>{s.id}</h2>
        <StatusMark s={STEP_S[s.status] || 'skip'} word={word || runMark(s.status === 'running' ? 'running' : s.status).word} />
        <span class="dl-sp" />
        {!running && tabs.length > 1 ? (
          <div class="seg" role="group">
            {tabs.map(([v, l]) => (
              <button type="button" data-v={v} aria-pressed={v === active} class={v === active ? 'on' : ''} onClick={() => setTab(v)}>
                {l}
              </button>
            ))}
          </div>
        ) : null}
      </header>
      {running ? (
        <div class="dl-step-open">
          <button class="btn sm secondary" type="button" onClick={onRestart}>
            <Icon name="undo" />
            Restart this step…
          </button>
        </div>
      ) : !live && s.status !== 'idle' ? (
        <div class="dl-step-open">
          <button class="btn sm secondary" type="button" onClick={onRetry}>
            <Icon name="undo" />
            Retry from here…
          </button>
        </div>
      ) : null}
      {body}
      {!running ? <EnvTech step={s} /> : null}
    </section>
  )
}

// LiveLog: the running step's log (prototype stepCard's dl-log): what conductor
// recorded for the steps so far, then the run's events as they stream in, and
// the blinking cursor while it works.
function LiveLog({ steps, step, events }: { r: RunSummary; steps: RunStep[]; step: RunStep; events: LogLine[] }): JSX.Element {
  const ref = useRef<HTMLPreElement>(null)
  const lines: LogLine[] = []
  for (const x of steps) {
    if (!x.started) continue
    lines.push([hms(x.started), x.id, '', x.id === step.id ? `started (${x.kind}); working on it` : `started (${x.kind})`])
    if (x.status !== 'running' && x.status !== 'idle') {
      const end = new Date(new Date(x.started).getTime() + (x.duration_ms || 0)).toISOString()
      lines.push([hms(end), x.id, x.status === 'failed' ? 'err' : '', x.status === 'ok' ? `passed in ${durText(x.duration_ms)}` : `${x.status}${x.error ? `: ${x.error}` : ''}`])
    }
  }
  const all = [...lines, ...events.filter((e) => !/^started$|^passed|^run /.test(e[3]) || e[3].includes(':'))]
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [all.length])
  return (
    <pre class="dl-code dl-log" id="log" data-volatile ref={ref}>
      {all.map(([t, src, k, text]) => (
        <span class={'ln' + (k === 'err' ? ' fail' : '')}>
          <i>{t}</i>
          <span class="src">{src}</span> {text}
        </span>
      ))}
      <span class="ln">
        <i />
        <span class="cursor-blink" />
      </span>
    </pre>
  )
}
