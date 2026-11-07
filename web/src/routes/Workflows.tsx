// Workflows (design §4.4): every configured trigger as a workflow, with its step
// count and recent run tally. A workflow opens to its graph, YAML and runs; the
// builder (New workflow) opens the canvas.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { WorkflowSummary } from '../api/types'
import { ago } from '../lib/format'
import { go } from '../router'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'

export function Workflows(): JSX.Element {
  const [wfs, setWfs] = useState<WorkflowSummary[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.workflows().then((r) => setWfs(r.workflows ?? [])).catch((e) => setErr(humanError(e)))
  }, [])

  if (wfs !== null && wfs.length === 0) {
    return (
      <div class="col">
        <h1 class="screen-title serif">Workflows</h1>
        <EmptyState
          testid="workflows-empty"
          art={<FlowGlyph />}
          title="No workflows yet"
          action={{ label: 'Build a workflow', href: '#/workflows/new', testid: 'workflows-empty-new' }}
          secondary={{ label: 'Get one from a pack', href: '#/packs' }}
        >
          <p>A workflow is a trigger and its steps: when something happens (a push, a schedule, a message), conductor runs the steps for you.</p>
        </EmptyState>
      </div>
    )
  }

  return (
    <div class="col">
      <div class="run-head">
        <h1 class="screen-title serif">Workflows</h1>
        <a class="btn primary" href="#/workflows/new">New workflow…</a>
      </div>
      <p class="muted">Each configured trigger is a workflow. Open one to see its steps, its YAML and its recent runs, or test it without dispatching anything.</p>
      {err ? <div class="banner err" role="alert">Couldn't load workflows: {err}</div> : null}

      <section class="panel">
        {wfs === null ? (
          <p class="muted">{err ? 'Nothing to show until conductor answers.' : 'Loading…'}</p>
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

// FlowGlyph is a trigger feeding two steps: the empty state's picture.
function FlowGlyph(): JSX.Element {
  return (
    <svg width="72" height="44" viewBox="0 0 72 44" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" stroke-linecap="round">
      <circle cx="10" cy="22" r="6" />
      <rect x="28" y="6" width="16" height="12" rx="3" opacity="0.8" />
      <rect x="28" y="26" width="16" height="12" rx="3" opacity="0.55" />
      <rect x="54" y="16" width="16" height="12" rx="3" opacity="0.55" />
      <path d="M16 22h6M22 22V12h6M22 22v10h6M44 12h4v10h6M44 32h4v-10" opacity="0.7" />
    </svg>
  )
}
