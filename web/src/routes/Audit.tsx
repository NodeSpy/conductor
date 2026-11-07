// Logs & audit (design §5.4). The tail of the audit log (JSONL), newest first —
// including the dashboard's own config.apply / config.restore entries, so every
// change made here is accountable. Read-only.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

export function Audit(): JSX.Element {
  const [entries, setEntries] = useState<Record<string, unknown>[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.audit().then((r) => setEntries(r.entries ?? [])).catch((e) => setErr(humanError(e)))
  }, [])

  return (
    <div class="col">
      <h1 class="screen-title serif">Logs & audit</h1>
      <p class="muted">The audit log, newest first. Config changes applied from this dashboard appear here.</p>
      {err ? <div class="banner err" role="alert">Couldn't load the audit log: {err}</div> : null}

      {entries === null ? (
        err ? null : <p class="muted">Loading…</p>
      ) : entries.length === 0 ? (
        <EmptyState testid="audit-empty" title="Nothing recorded yet">
          <p>Every change that matters is written here, newest first: config applied or restored, approvals given, passkeys added, sign-ins. Nothing has happened that needs a record.</p>
        </EmptyState>
      ) : (
      <section class="panel" aria-label="Audit log">
        {entries.length ? (
          <ul class="audit-list">
            {entries.map((e) => (
              <li class="audit-row">
                <span class="mono audit-ts">{String(e['ts'] ?? '')}</span>
                <span class="audit-event">{String(e['event'] ?? e['kind'] ?? 'entry')}</span>
                <span class="mono audit-rest">{summarize(e)}</span>
              </li>
            ))}
          </ul>
        ) : null}
      </section>
      )}
    </div>
  )
}

function summarize(e: Record<string, unknown>): string {
  const skip = new Set(['ts', 'event', 'kind'])
  return Object.entries(e)
    .filter(([k]) => !skip.has(k))
    .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
    .join('  ')
}
