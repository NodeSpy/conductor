// Logs & audit (CONFORMANCE "System · Logs & audit"): the tail of the audit
// log (JSONL), newest first — including the dashboard's own config.apply /
// config.restore entries, so every change made here is accountable. Read-only.
// Prototype class: css/daylight.css's `.dl-audit` row (already ported).
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'

export function Audit(): JSX.Element {
  const [entries, setEntries] = useState<Record<string, unknown>[] | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  useEffect(() => {
    api.audit().then((r) => setEntries(r.entries ?? [])).catch((e) => setErr(humanError(e)))
  }, [])

  // The daemon doesn't offer a search endpoint for this tail (there is no
  // "daemon log" source either — only the one audit JSONL, see the phase 7
  // report's data-gap list), so this filters client-side over what it
  // already read. Good enough for a 200-entry tail; not a replacement for a
  // real server-side search over the full log.
  const filtered = useMemo(() => {
    if (!entries || !q.trim()) return entries
    const needle = q.toLowerCase()
    return entries.filter((e) => JSON.stringify(e).toLowerCase().includes(needle))
  }, [entries, q])

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Logs &amp; audit</h1>
          <p class="lede-2">The audit log, newest first. Config changes applied from this dashboard appear here.</p>
        </div>
      </div>
      {err ? <div class="banner err" role="alert">Couldn't load the audit log: {err}</div> : null}

      {entries === null ? (
        err ? null : <p class="muted">Loading…</p>
      ) : entries.length === 0 ? (
        <EmptyState testid="audit-empty" title="Nothing recorded yet">
          <p>Every change that matters is written here, newest first: config applied or restored, approvals given, passkeys added, sign-ins. Nothing has happened that needs a record.</p>
        </EmptyState>
      ) : (
        <>
          <div class="input-icon" style="max-width:360px;margin-bottom:8px">
            <Icon name="search" />
            <input class="input" placeholder="Search the log" aria-label="Search the audit log" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
          </div>
          <section class="dl-card" aria-label="Audit log">
            <ul class="dl-list">
              {filtered?.length ? (
                filtered.map((e, i) => (
                  <li class="dl-audit" key={i}>
                    <span>
                      <span class="dl-nt">
                        <b>{String(e['event'] ?? e['kind'] ?? 'entry')}</b>
                        <span class="dl-why mono">{summarize(e)}</span>
                      </span>
                    </span>
                    <span class="mono dl-sub">{String(e['ts'] ?? '')}</span>
                  </li>
                ))
              ) : (
                <li class="dl-empty-row">No entry matches.</li>
              )}
            </ul>
          </section>
        </>
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
