// Workflows (design §4.4): every configured trigger as a workflow, with its step
// count and recent run tally. A workflow opens to its graph, YAML and runs; the
// builder (New workflow) opens the canvas.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { WorkflowSummary } from '../api/types'
import { ago } from '../lib/format'
import { go } from '../router'

export function Workflows(): JSX.Element {
  const [wfs, setWfs] = useState<WorkflowSummary[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.workflows().then((r) => setWfs(r.workflows)).catch((e) => setErr(String(e instanceof Error ? e.message : e)))
  }, [])

  return (
    <div class="col">
      <div class="run-head">
        <h1 class="screen-title serif">Workflows</h1>
        <a class="btn primary" href="#/workflows/new">New workflow…</a>
      </div>
      <p class="muted">Each configured trigger is a workflow. Open one to see its steps, its YAML and its recent runs, or test it without dispatching anything.</p>
      {err ? <div class="banner err">{err}</div> : null}

      <section class="panel">
        {wfs === null ? (
          <p class="muted">Loading…</p>
        ) : wfs.length === 0 ? (
          <p class="muted">No workflows configured yet.</p>
        ) : (
          <div class="tbl-wrap">
            <table class="tbl">
              <thead>
                <tr>
                  <th>Workflow</th>
                  <th>Trigger</th>
                  <th>Steps</th>
                  <th>Runs</th>
                  <th>Last run</th>
                </tr>
              </thead>
              <tbody>
                {wfs.map((w) => (
                  <tr class="clickable" data-wf={w.name} onClick={() => go('workflows/' + encodeURIComponent(w.name))}>
                    <td>
                      <div class="cell-title">
                        {w.name}
                        {w.disabled ? <span class="tag warn">disabled</span> : null}
                        {w.manual ? <span class="tag">manual</span> : null}
                      </div>
                    </td>
                    <td class="mono">{w.on || '—'}</td>
                    <td>{w.steps}</td>
                    <td>
                      {w.runs ? (
                        <>
                          {w.runs}
                          {w.failed ? <span class="tag danger">{w.failed} failed</span> : null}
                        </>
                      ) : (
                        <span class="muted">—</span>
                      )}
                    </td>
                    <td>{w.last_run ? <a class="link" href={'#/runs/' + w.last_key}>{ago(w.last_run)}</a> : <span class="muted">never</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
