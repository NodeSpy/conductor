// Run words and pieces shared by Activity, a run's page and Compare (prototype
// js/ui.js UI.durText/UI.agoText/UI.runTitle/UI.runWord, js/screens/runs.js
// trigWords/stepCard, js/screens/env.js effectiveEnvHTML, js/ui.js UI.diffView).
import type { JSX } from 'preact'
import { api } from '../api/client'
import type { EnvVarInfo, RunStep, RunSummary } from '../api/types'
import { Icon } from './Icon'
import { runTitle as sharedRunTitle } from '../lib/runwords'

/** "1 s", "4 min 12 s", "2 h 5 min"; "—" for nothing (UI.durText). */
export function durText(ms?: number): string {
  if (!ms || ms <= 0) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${Math.max(1, s)} s`
  const m = Math.floor(s / 60)
  const r = s % 60
  return m < 10 && r ? `${m} min ${r} s` : m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`
}

/** "just now", "6 min ago", "3 h ago", "yesterday", "4 days ago" (UI.agoText). */
export function agoText(iso?: string): string {
  if (!iso) return '—'
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  return d === 1 ? 'yesterday' : `${d} days ago`
}

/** A run's took: its finished minus started, or "…" while it runs (UI.runDur). */
export function runDur(r: RunSummary): string {
  return r.finished ? durText(new Date(r.finished).getTime() - new Date(r.started).getTime()) : '…'
}

/** A run that ended badly: failed, or stopped by someone. */
export function isFailedRun(r: { status: string }): boolean {
  return r.status === 'failed' || r.status === 'stopped'
}

/** The status vocabulary for a run (UI.runS + UI.runWord). */
export function runMark(status: string): { s: string; word: string } {
  switch (status) {
    case 'ok':
      return { s: 'ok', word: 'Passed' }
    case 'failed':
      return { s: 'fail', word: 'Failed' }
    case 'stopped':
      return { s: 'skip', word: 'Stopped' }
    case 'running':
      return { s: 'run', word: 'Running' }
    case 'retried':
      return { s: 'idle', word: 'Retried' }
    case 'interrupted':
      return { s: 'skip', word: 'Interrupted' }
    default:
      return { s: 'idle', word: status || 'Unknown' }
  }
}

/** A run's plain headline (lib/runwords runTitle, UI.runTitle); a stopped run reads as failed. */
export function runTitle(r: { workflow: string; status: string }): string {
  return sharedRunTitle({ workflow: r.workflow, status: r.status === 'stopped' ? 'failed' : r.status })
}

/** A run's error in plain words, without the engine's `step "x": ` prefix (UI.plainError). */
export function plainError(r: { error?: string }): string {
  const e = (r.error || '').replace(/^step "[^"]*": /, '')
  if (/stopped by operator|context canceled/.test(e)) return 'You stopped it'
  if (/max_cost_usd [\d.]+ exceeded/.test(e)) return 'It hit its budget cap, so conductor stopped it'
  return e
}

/** What a run is about, when no connector fact names it (runs.js target). */
export function runTargetWords(r: RunSummary): string {
  if (r.repo && !r.repo.startsWith('manual:')) return `${r.repo}${r.number ? `#${r.number}` : ''}`
  if (r.workflow.startsWith('bot:')) return 'its own work'
  if (/schedule|cron/.test(r.kind)) return 'on its schedule'
  return 'run by hand'
}

/** The step's environment (prototype stepCard env-tech + UI.effectiveEnvHTML): names and where they come from, never a value. */
export function EnvTech({ step }: { step: RunStep }): JSX.Element | null {
  if (!['agent', 'code', 'decide', 'command', 'verb'].includes(step.kind)) return null
  const rows = step.env_vars || []
  const sandboxed = step.kind === 'agent' || step.kind === 'decide'
  const scope = (s: string) => s.replace(/^host:/, 'host ').replace(/^runtime:/, 'runtime ')
  return (
    <details class="dl-tech-d env-tech">
      <summary>
        <Icon name="chev" />
        Its environment <span class="dl-sub">the variables this step saw, by scope; values are never shown</span>
      </summary>
      <table class="tbl compact responsive env-eff">
        <thead>
          <tr>
            <th>name</th>
            <th>comes from</th>
            <th>overrides</th>
            <th>{sandboxed ? 'reaches the agent' : 'reaches the step'}</th>
          </tr>
        </thead>
        <tbody>
          {rows.length ? (
            rows.map((v: EnvVarInfo) => (
              <tr key={v.scope + ':' + v.name}>
                <td data-l="name" class="mono">{v.name}</td>
                <td data-l="comes from" class="mono">
                  {scope(v.scope)}
                  {v.ref ? <> <span class="muted tiny">({v.ref})</span></> : null}
                </td>
                <td data-l="overrides" class="mono muted">—</td>
                <td data-l="reaches">yes</td>
              </tr>
            ))
          ) : (
            <tr class="empty-row">
              <td colSpan={4}>No env vars reach this context.</td>
            </tr>
          )}
        </tbody>
      </table>
      <div class="muted tiny" style="padding:6px 2px">Names and where they come from only; values are never shown here.</div>
    </details>
  )
}

/** One line of a unified diff (YAML.diff's {t, s, a, b}). */
interface DLine {
  t: ' ' | '+' | '-'
  s: string
  a?: number
  b?: number
}

function lineDiff(A: string, B: string): DLine[] {
  const x = A ? A.split('\n') : []
  const y = B ? B.split('\n') : []
  const n = x.length
  const m = y.length
  const L: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = x[i] === y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1])
  const out: DLine[] = []
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      out.push({ t: ' ', s: x[i], a: i + 1, b: j + 1 })
      i++
      j++
    } else if (j < m && (i >= n || L[i][j + 1] >= L[i + 1][j])) {
      out.push({ t: '+', s: y[j], b: j + 1 })
      j++
    } else {
      out.push({ t: '-', s: x[i], a: i + 1 })
      i++
    }
  }
  return out
}

/** A unified diff of two texts (UI.diffView(A, B, 'unified', { file })), whole when short. */
export function UnifiedDiff({ a, b, file }: { a: string; b: string; file: string }): JSX.Element {
  const d = lineDiff(a, b)
  if (!d.some((r) => r.t !== ' ')) {
    return (
      <div class="empty small">
        <span class="serif">No difference</span>Both runs recorded the same outputs for this step.
      </div>
    )
  }
  const first = d.find((r) => r.t !== ' ') || d[0]
  return (
    <div class="diff unified">
      <div class="h">
        {file} · line {first.t === '-' ? first.a : first.b}
      </div>
      {d.map((r) => (
        <div class={'dl ' + (r.t === '+' ? 'add' : r.t === '-' ? 'del' : '')}>
          <span>{r.a || ''}</span>
          <span>{r.b || ''}</span>
          <span>{r.t === ' ' ? ' ' : r.t}</span>
          <span>{r.s || ' '}</span>
        </div>
      ))}
    </div>
  )
}

/** A step's own patch (UI.stepDiffHTML): its recorded unified diff, one row per line. */
export function StepDiff({ diff }: { diff: string }): JSX.Element {
  let a = 0
  let b = 0
  const rows: JSX.Element[] = []
  for (const l of diff.split('\n')) {
    const m = l.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)/)
    if (m) {
      a = Number(m[1])
      b = Number(m[2])
      rows.push(<div class="h">{l}</div>)
      continue
    }
    if (l.startsWith('+++') || l.startsWith('---') || l.startsWith('diff ') || l.startsWith('index ')) {
      rows.push(<div class="h">{l}</div>)
      continue
    }
    const t = l[0] === '+' ? '+' : l[0] === '-' ? '-' : ' '
    rows.push(
      <div class={'dl ' + (t === '+' ? 'add' : t === '-' ? 'del' : '')}>
        <span>{t === '+' ? '' : a || ''}</span>
        <span>{t === '-' ? '' : b || ''}</span>
        <span>{t}</span>
        <span>{l.slice(1) || ' '}</span>
      </div>,
    )
    if (t !== '+') a++
    if (t !== '-') b++
  }
  return <div class="diff unified">{rows}</div>
}

/**
 * retryAndFind asks the daemon to retry a run and waits briefly for the new
 * run's record (the engine answers before the retry's record exists), so the
 * page can open it, the way the prototype goes straight to the retry.
 */
export async function retryAndFind(id: string, from: string, force: boolean): Promise<{ message: string; newID: string }> {
  const before = new Set((await api.runs({ limit: 200 }).catch(() => ({ runs: [] as RunSummary[] }))).runs.filter((x) => x.retry_of === id).map((x) => x.id))
  const ack = await api.retryRun(id, { from, force })
  let newID = ack.run_id || ''
  for (let i = 0; !newID && i < 20; i++) {
    await new Promise((r) => setTimeout(r, 300))
    const rs = await api.runs({ limit: 50 }).catch(() => ({ runs: [] as RunSummary[] }))
    newID = rs.runs.find((x) => x.retry_of === id && !before.has(x.id))?.id || ''
  }
  return { message: ack.message, newID }
}
