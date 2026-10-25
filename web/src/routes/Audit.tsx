// Logs & audit (design §5.4). The tail of the audit log (JSONL), newest first —
// including the dashboard's own config.apply / config.restore entries, so every
// change made here is accountable. Read-only.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'

export function Audit(): JSX.Element {
  const [entries, setEntries] = useState<Record<string, unknown>[]>([])
  const [err, setErr] = useState('')
  useEffect(() => {
    api.audit().then((r) => setEntries(r.entries ?? [])).catch((e) => setErr(String(e)))
  }, [])

  return (
    <div class="col">
      <h1 class="screen-title serif">Logs & audit</h1>
      <p class="muted">The audit log, newest first. Config changes applied from this dashboard appear here.</p>
      {err ? <div class="banner err">{err}</div> : null}

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
        ) : (
          <p class="muted">No audit entries yet.</p>
        )}
      </section>
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
