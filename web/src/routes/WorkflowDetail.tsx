// One workflow (design §4.4): its graph (node→YAML), its YAML, a dry-run Test
// that checks it resolves without dispatching anything, and its recent runs
// (in-flight first). Read-only; the builder edits.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { WorkflowDetailResponse, GraphNode, TestResponse } from '../api/types'
import { Graph } from '../components/Graph'
import { StatusMark } from '../components/StatusMark'
import { statusMark } from '../state'
import { ago, humanDuration } from '../lib/format'
import { go } from '../router'

export function WorkflowDetail({ name }: { name: string }): JSX.Element {
  const [wf, setWf] = useState<WorkflowDetailResponse | null>(null)
  const [err, setErr] = useState('')
  const [sel, setSel] = useState<string>('')
  const [test, setTest] = useState<TestResponse | null>(null)
  const [testing, setTesting] = useState(false)
  const yamlRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    api.workflow(name).then(setWf).catch((e) => setErr(String(e instanceof Error ? e.message : e)))
  }, [name])

  const pick = (n: GraphNode) => {
    setSel(n.id)
    // Reveal the node's line in the YAML pane (node→YAML).
    const pre = yamlRef.current
    const ln = n.line ?? 0
    if (pre && ln > 0) {
      const el = pre.querySelector(`[data-line="${ln}"]`) as HTMLElement | null
      el?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }
  }

  const runTest = async () => {
    setTesting(true)
    try {
      setTest(await api.testWorkflow(name))
    } catch (e) {
      setTest({ ok: false, message: String(e instanceof Error ? e.message : e) })
    } finally {
      setTesting(false)
    }
  }

  if (err) {
    return (
      <div class="col">
        <Crumbs name={name} />
        <div class="empty"><h2>No workflow {name}</h2><a class="btn" href="#/workflows">Back to Workflows</a></div>
      </div>
    )
  }
  if (!wf) return <div class="col"><p class="muted">Loading…</p></div>

  const inflight = wf.runs.filter((r) => r.status === 'running')
  const recent = wf.runs.filter((r) => r.status !== 'running')

  return (
    <div class="col" data-wf-detail={name}>
      <Crumbs name={name} />
      <div class="run-head">
        <div>
          <h1 class="screen-title serif">{wf.name}</h1>
          <p class="run-lead mono">{wf.on || 'manual'}{wf.disabled ? ' · disabled' : ''}</p>
        </div>
        <button class="btn primary" type="button" disabled={testing} onClick={runTest}>
          {testing ? 'Testing…' : 'Test (dry-run)'}
        </button>
      </div>

      {test ? (
        <div class={'banner ' + (test.ok ? 'ok' : 'warn')}>
          <StatusMark s={test.ok ? 'ok' : 'wait'} mkOnly /> {test.message}
        </div>
      ) : null}

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

      <div class="wf-cols">
        <section class="panel">
          <div class="panel-head"><span class="panel-title serif">Steps</span><span class="panel-sub">{wf.nodes.length} steps</span></div>
          <Graph nodes={wf.nodes} selected={sel} onPick={pick} />
        </section>
        <section class="panel">
          <div class="panel-head"><span class="panel-title serif">YAML</span><span class="panel-sub">as loaded</span></div>
          <pre class="code yaml-pane" ref={yamlRef}>
            {wf.yaml.split('\n').map((l, i) => (
              <span class={'yaml-l' + (sel && selLine(wf.nodes, sel) === i + 1 ? ' hot' : '')} data-line={i + 1}>
                <i class="yaml-gutter">{i + 1}</i>
                {l + '\n'}
              </span>
            ))}
          </pre>
        </section>
      </div>

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
    </div>
  )
}

function selLine(nodes: GraphNode[], id: string): number {
  return nodes.find((n) => n.id === id)?.line || 0
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
