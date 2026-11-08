// The workflow builder (CONFORMANCE "The workflow builder"): crumbs, the name,
// what it does, Undo / Test / Save and apply; tabs (Recipe, Config file, Runs,
// Settings); the When box in words; step cards (number, title over summary,
// the kind with its icon, chevron). Save and apply goes through the same
// review diff → passkey → apply pipeline every config edit does (configStore).
//
// Left out, with no small backend addition that would cover it cleanly: a
// step card's own generated field-by-field form (the prototype builds it from
// each connector verb's declared schema; the dashboard has no such catalog
// endpoint), the step picker, insert-from-event references, and testing a
// step against a real past event. A step opens to its raw YAML instead, and
// "Add a step" jumps to the Config file tab at the right place — real editing,
// through a smaller surface.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { parse as parseYAML } from 'yaml'
import { api } from '../api/client'
import type { WorkflowDetailResponse, GraphNode, TestResponse } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { CodeEditor } from '../components/CodeEditor'
import { ReviewPanel } from '../components/ReviewPanel'
import { statusMark } from '../state'
import { ago, humanDuration } from '../lib/format'
import { go } from '../router'
import { humanError } from '../api/errors'
import { stage, staged, review, applyResult, busy, loadConfig } from '../configStore'

const KIND: Record<string, { icon: string; label: string }> = {
  agent: { icon: 'sparkle', label: 'Agent' },
  command: { icon: 'code', label: 'Command' },
  code: { icon: 'code', label: 'Code' },
  verb: { icon: 'zap', label: 'Action' },
  decide: { icon: 'obj-question', label: 'Decide' },
  team: { icon: 'users', label: 'Team' },
  parallel: { icon: 'workflows', label: 'Parallel' },
  handoff: { icon: 'hand', label: 'Hand-off' },
  call: { icon: 'link', label: 'Call' },
  bot: { icon: 'obj-bot', label: 'Bot' },
  sleep: { icon: 'clock', label: 'Wait' },
  wait_for: { icon: 'clock', label: 'Wait' },
  log: { icon: 'logs', label: 'Log' },
  set: { icon: 'edit', label: 'Set' },
  assert: { icon: 'check', label: 'Assert' },
  fail: { icon: 'x', label: 'Fail' },
}
const kindOf = (k: string) => KIND[k] || { icon: 'warn', label: k || 'Step' }

type Tab = 'recipe' | 'config' | 'runs' | 'settings'

export function WorkflowDetail({ name }: { name: string }): JSX.Element {
  const [wf, setWf] = useState<WorkflowDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [sel, setSel] = useState<string>('')
  const [tab, setTab] = useState<Tab>('recipe')
  const [text, setText] = useState('')
  const [test, setTest] = useState<TestResponse | null>(null)
  const [testing, setTesting] = useState(false)
  const [parseErr, setParseErr] = useState('')
  const [saveErr, setSaveErr] = useState('')
  const [hlLine, setHlLine] = useState<number | undefined>(undefined)
  const yamlRef = useRef<HTMLElement>(null)

  const load = () =>
    api
      .workflow(name)
      .then((r) => {
        setWf(r)
        setText(r.yaml)
        setErr('')
      })
      .catch((e) => setErr(humanError(e)))
  useEffect(() => {
    load()
    void loadConfig()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  // pick marks a step selected (for the card's own highlight); jumpToYAML is
  // the explicit "show it in the config file" action, since the two tabs
  // don't render at once — it switches there and scrolls once mounted.
  const pick = (n: GraphNode) => setSel(n.id)
  const jumpToYAML = (n: GraphNode) => {
    setSel(n.id)
    setTab('config')
    const ln = n.line ?? 0
    setHlLine(ln > 0 ? ln : undefined)
    if (ln > 0) {
      requestAnimationFrame(() => {
        const el = yamlRef.current?.querySelector(`[data-ln="${ln}"]`) as HTMLElement | null
        el?.scrollIntoView({ block: 'center', behavior: 'smooth' })
      })
    }
  }

  const runTest = async () => {
    setTesting(true)
    try {
      setTest(await api.testWorkflow(name))
    } catch (e) {
      setTest({ ok: false, message: humanError(e) })
    } finally {
      setTesting(false)
    }
  }

  const dirty = !!wf && text !== wf.yaml
  const stagedForThis = wf ? staged.value.filter((c) => c.path[0] === 'triggers' && c.path[1] === String(wf.index)) : []
  const hasStaged = stagedForThis.length > 0

  const saveAndApply = () => {
    if (!wf) return
    setSaveErr('')
    let value: unknown
    try {
      value = parseYAML(text)
    } catch (e) {
      setParseErr(e instanceof Error ? e.message : String(e))
      return
    }
    setParseErr('')
    void stage(['triggers', String(wf.index)], value, `edit workflow ${wf.name}`)
  }

  const toggleOn = () => {
    if (!wf) return
    void stage(['triggers', String(wf.index), 'enabled'], wf.disabled, `${wf.disabled ? 'switch on' : 'switch off'} ${wf.name}`)
  }

  if (err) {
    return (
      <div class="col m-page">
        <Crumbs name={name} />
        <div class="empty"><h2>No workflow {name}</h2><a class="btn" href="#/workflows">Back to Workflows</a></div>
      </div>
    )
  }
  if (!wf) return <div class="col m-page"><p class="muted">Loading…</p></div>

  const runs = wf.runs || []
  const inflight = runs.filter((r) => r.status === 'running')
  const recent = runs.filter((r) => r.status !== 'running')
  const status = applyResult.value && hasStaged === false ? 'live' : hasStaged ? 'staged' : dirty ? 'draft' : 'live'
  const readOnly = wf.index < 0

  return (
    <div class="col wfb-page m-page" data-wf-detail={name}>
      <Crumbs name={name} />
      <div class="wfb">
      <div class="dl-phead">
        <div>
          <h1 class="wfb-title">{wf.name}</h1>
          <p class="dl-lead">{wf.does}</p>
          <p class="wfb-owner">
            {wf.source?.startsWith('pack:') ? <>From the pack <b>{wf.source.slice(5)}</b>: your changes are saved as overrides.</> : null}
            {status === 'staged' ? (
              <span class="dl-sub wf-state staged">Staged, not applied</span>
            ) : status === 'draft' ? (
              <span class="dl-sub wf-state draft">Unsaved changes</span>
            ) : (
              <span class="dl-sub wf-state live">Live</span>
            )}
          </p>
        </div>
        <div class="dl-actions tight">
          <button class="btn ghost" type="button" disabled={!dirty} onClick={() => { setText(wf.yaml); setParseErr('') }}>
            <Icon name="undo" />Undo
          </button>
          <button class="btn secondary" type="button" disabled={testing} onClick={() => void runTest()}>
            <Icon name="play" />{testing ? 'Testing…' : 'Test'}
          </button>
          <button class="btn primary" type="button" disabled={readOnly || !dirty || busy.value} onClick={saveAndApply}>
            Save and apply
          </button>
        </div>
      </div>

      <nav class="dl-tabs" role="tablist">
        {([
          ['recipe', 'Recipe'],
          ['config', 'Config file'],
          ['runs', 'Runs', runs.length],
          ['settings', 'Settings'],
        ] as [Tab, string, number?][]).map(([k, l, n]) => (
          <a href="#" role="tab" class={tab === k ? 'on' : ''} aria-selected={tab === k} onClick={(e) => { e.preventDefault(); setTab(k) }}>
            {l}{n ? ` (${n})` : ''}
          </a>
        ))}
      </nav>

      {readOnly ? (
        <div class="banner attn" role="status">This trigger has no name, so the dashboard can't address it for a config edit safely. Give it a name: in the config to edit it here.</div>
      ) : null}
      {saveErr ? <div class="banner err" role="alert">{saveErr}</div> : null}
      {hasStaged ? (
        <div class="banner attn" role="status">
          Staged, not applied: review the change below, then apply with your passkey.
        </div>
      ) : null}

      {test ? (
        <div class={'banner ' + (test.ok ? 'ok' : 'warn')}>
          <StatusMark s={test.ok ? 'ok' : 'wait'} mkOnly /> {test.message}
        </div>
      ) : null}

      {tab === 'recipe' ? (
        <div class="wfb-body">
          <RecipeTab wf={wf} sel={sel} onPick={pick} onShowYaml={jumpToYAML} onAddStep={() => setTab('config')} />
          <RecipeSide wf={wf} testing={testing} onTest={() => void runTest()} onOpenConfig={() => setTab('config')} />
        </div>
      ) : tab === 'config' ? (
        <section class="dl-card wfb-side-card" ref={yamlRef}>
          <header class="dl-ch"><h2>Config file</h2><span class="dl-sub">YAML is canonical · edits are reviewed before they apply</span></header>
          {parseErr ? <div class="banner err" role="alert">That doesn't parse as YAML: {parseErr}</div> : null}
          <CodeEditor id="wf-yaml" text={text} onChange={(t) => { setText(t); setHlLine(undefined) }} readOnly={readOnly} minRows={16} label={`${wf.name} YAML`} hlLine={hlLine} />
        </section>
      ) : tab === 'runs' ? (
        <>
          {inflight.length ? (
            <section class="panel" aria-label="In flight">
              <div class="panel-head"><span class="panel-title serif">In flight</span><span class="panel-sub">{inflight.length} running</span></div>
              <ul class="rows">
                {inflight.map((r) => (
                  <li class="row clickable" onClick={() => go('runs/' + r.id)}>
                    <StatusMark s="run" mkOnly />
                    <div class="row-main">
                      <div class="row-title">{r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : r.id}</div>
                      <div class="row-meta mono">{r.id}</div>
                    </div>
                    <div class="row-age">{humanDuration(r.duration_ms)}</div>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          <section class="panel" aria-label="Recent runs">
            <div class="panel-head"><span class="panel-title serif">Recent runs</span></div>
            {recent.length === 0 ? (
              <p class="muted">No runs recorded for this workflow yet.</p>
            ) : (
              <div class="tbl-wrap">
                <table class="tbl">
                  <thead><tr><th>Status</th><th>When</th><th>Took</th><th>ID</th></tr></thead>
                  <tbody>
                    {recent.map((r) => {
                      const m = statusMark(r.status)
                      return (
                        <tr class="clickable" onClick={() => go('runs/' + r.id)}>
                          <td><StatusMark s={m.s} word={m.word} /></td>
                          <td>{ago(r.started)}</td>
                          <td>{humanDuration(r.duration_ms)}</td>
                          <td class="mono id">{r.id}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      ) : (
        <section class="dl-card">
          <header class="dl-ch"><h2>Settings</h2></header>
          <div class="field-row">
            <label class="row" style="gap:10px">
              {!readOnly ? (
                <button type="button" role="switch" class={'switch' + (!wf.disabled ? ' on' : '')} aria-checked={!wf.disabled} aria-label={wf.disabled ? 'Switch on' : 'Switch off'} onClick={toggleOn} />
              ) : null}
              <span>{wf.disabled ? 'Off' : 'On'}</span>
            </label>
            <p class="muted">{wf.when}</p>
          </div>
          <p class="dl-sub">Budgets, isolation and the rest of the workflow's keys are in the <a class="dl-link" href="#" onClick={(e) => { e.preventDefault(); setTab('config') }}>config file</a>.</p>
        </section>
      )}
      </div>

      {hasStaged || review.value ? <ReviewPanel stagedCount={staged.value.length} /> : null}
    </div>
  )
}

function RecipeTab({
  wf,
  sel,
  onPick,
  onShowYaml,
  onAddStep,
}: {
  wf: WorkflowDetailResponse
  sel: string
  onPick: (n: GraphNode) => void
  onShowYaml: (n: GraphNode) => void
  onAddStep: () => void
}): JSX.Element {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  return (
    <div class="wfb-list">
      <div class="staff">
        <div class={'trig-card' + (sel === '$trigger' ? ' open' : '')}>
          <div class="sc-head" role="button" tabindex={0} onClick={() => setOpen((o) => ({ ...o, $trigger: !o.$trigger }))}>
            <span class="wl"><Icon name="zap" />When</span>
            <div class="grow"><b>{wf.when || wf.on || 'no trigger yet'}</b></div>
            <span class="chev"><Icon name={open.$trigger ? 'chevd' : 'chev'} /></span>
          </div>
          {open.$trigger ? <div class="sc-body"><p class="muted">{wf.does}</p></div> : null}
        </div>

        <div class="spine-add">
          <button type="button" class="plus" aria-label="Add a step" onClick={onAddStep}><Icon name="plus" /></button>
        </div>

        {wf.nodes.map((n, i) => {
          const k = kindOf(n.kind)
          const isOpen = !!open[n.id]
          return (
            <>
              <div class={'step-card' + (isOpen ? ' open' : '') + (sel === n.id ? ' sel' : '')} id={`card-${n.id}`}>
                <div class="sc-head" role="button" tabindex={0} onClick={() => { onPick(n); setOpen((o) => ({ ...o, [n.id]: !o[n.id] })) }}>
                  <span class="sc-num">{i + 1}</span>
                  <span class="sc-ic"><Icon name={k.icon} /></span>
                  <div class="grow sc-text">
                    <div class="sc-title"><b>{n.id}</b></div>
                    <div class="sc-sum">{k.label}{n.label ? ` · ${n.label}` : ''}</div>
                  </div>
                  <span class="chev"><Icon name={isOpen ? 'chevd' : 'chev'} /></span>
                </div>
                {n.if ? (
                  <div class="sc-body" style="padding-top:0;border-top:0">
                    <span class="dl-sub"><Icon name="filter" /> Only if <code>{n.if}</code></span>
                  </div>
                ) : null}
                {isOpen ? (
                  <div class="sc-body">
                    <p class="muted">{k.label}{n.label ? `: ${n.label}` : ''}{n.needs?.length ? ` · after ${n.needs.join(', ')}` : ''}</p>
                    <button type="button" class="btn sm ghost" onClick={() => onShowYaml(n)}><Icon name="code" />Show in the config file</button>
                  </div>
                ) : null}
              </div>
              <div class="spine-add">
                <button type="button" class="plus" aria-label={`Add a step after ${n.id}`} onClick={onAddStep}><Icon name="plus" /></button>
              </div>
            </>
          )
        })}
        {wf.nodes.length === 0 ? <p class="muted">No steps yet.</p> : null}
      </div>
    </div>
  )
}

// RecipeSide: the recipe tab's right column (CONFORMANCE's builder — Try it,
// At a glance and the config file beside). Everything here comes from data
// the dashboard already has (wf.runs, wf.source, wf.yaml): no backend change.
// Left out: a budget figure (no per-workflow budget in config.TriggerSpec
// today) and Try it's event picker (dry-running against a specific past
// event needs an events catalog endpoint the daemon doesn't have — Try it
// here is the same safe, no-side-effect Test as the header's button).
function RecipeSide({
  wf,
  testing,
  onTest,
  onOpenConfig,
}: {
  wf: WorkflowDetailResponse
  testing: boolean
  onTest: () => void
  onOpenConfig: () => void
}): JSX.Element {
  const since = Date.now() - 24 * 60 * 60 * 1000
  const last24 = (wf.runs || []).filter((r) => new Date(r.started).getTime() >= since)
  const parts: [number, string][] = [
    [last24.filter((r) => r.status === 'ok').length, 'ok'],
    [last24.filter((r) => r.status === 'failed').length, 'failed'],
    [last24.filter((r) => r.status === 'running').length, 'running'],
    [last24.filter((r) => r.status === 'retried').length, 'retried'],
  ]
  const breakdown = parts.filter(([n]) => n).map(([n, t]) => `${n} ${t}`).join(' · ') || 'none'
  const owner = wf.source?.startsWith('pack:') ? `pack ${wf.source.slice(5)}` : 'your config'
  const preview = wf.yaml.split('\n').slice(0, 7).join('\n')

  return (
    <div class="wfb-side-stack">
      <section class="dl-card" aria-label="Try it">
        <header class="dl-ch"><h2>Try it</h2></header>
        <p class="dl-sub">Runs every step as configured, without dispatching anything.</p>
        <button class="btn wide" type="button" disabled={testing} onClick={onTest}>
          <Icon name="play" />{testing ? 'Testing…' : 'Run a test'}
        </button>
      </section>

      <section class="dl-card" aria-label="At a glance">
        <header class="dl-ch"><h2>At a glance</h2></header>
        <div class="field-row"><span class="dl-sub">Owner</span><span>{owner}</span></div>
        <div class="field-row"><span class="dl-sub">Last 24 h</span><span>{last24.length} run{last24.length === 1 ? '' : 's'} · {breakdown}</span></div>
      </section>

      <section class="dl-card wfb-side-card" aria-label="Config file">
        <header class="dl-ch"><h2>Config file</h2><button type="button" class="link" onClick={onOpenConfig}>Open</button></header>
        <div class="wfb-side"><div class="side-body"><pre class="dl-code small">{preview}</pre></div></div>
      </section>
    </div>
  )
}

function Crumbs({ name }: { name: string }): JSX.Element {
  return (
    <nav class="crumbs" aria-label="Breadcrumb">
      <a href="#/workflows">Workflows</a>
      <span class="crumbs-sep">/</span>
      <span>{name}</span>
    </nav>
  )
}
