// Logs & audit (CONFORMANCE "System · Logs & audit"): the prototype splits
// this into two sub-tabs (js/screens/system.js UI.route('logs/:tab?')) —
// "Daemon log" (the process's own log ring) and "Audit" (one sentence per
// operator-accountable action, newest first). conductor's real audit.jsonl
// (internal/store/audit.go) is a single append-only stream that both the
// dashboard's own actions AND the run engine's internal telemetry (step,
// dispatch, verb, decide, escalate, workflow_*…) write into — there is no
// daemon log ring exposed over the dashboard API at all (no backend source;
// a real limit, not implemented here). So: the Audit tab reads that one
// stream but shows only the entries a person did something to cause
// (dashboard_*/dashboard.* events — sign-ins, passkeys, config apply/restore,
// approvals, purchases), worded as a sentence the way the prototype's
// auditWords/EVW do; the engine's internal rows stay out of it rather than
// being exposed as raw key=value dumps. Daemon log has an honest empty state:
// there is nothing to show it, in plain words, not a developer excuse.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { TabsBar } from '../components/TabsBar'
import { ago } from '../lib/format'
import { seg } from '../router'

type Entry = Record<string, unknown>

// EVW: event name -> what happened, in plain words. Mirrors the prototype's
// js/screens/system.js EVW table, trimmed to the events conductor's own
// dashboard and auth packages actually write (internal/dashboard/*.go,
// internal/dashboard/auth/*.go) — a mapping kept honest to real event names
// rather than inventing ones the backend never emits.
const EVW: Record<string, string> = {
  dashboard_signin: 'signed in to the dashboard',
  dashboard_logout: 'signed out',
  dashboard_logout_all: 'signed out every session',
  dashboard_session_end: 'ended a session',
  dashboard_session_ended: 'was signed out',
  dashboard_enrolled: 'added a passkey',
  dashboard_enroll_ticket: 'started enrolling a passkey',
  dashboard_enroll_redeemed: 'finished enrolling a passkey',
  dashboard_enroll_code_fail: 'typed the wrong enrollment code',
  dashboard_passkey_revoked: 'removed a passkey',
  dashboard_password_set: 'set a password',
  dashboard_password_removed: 'removed the password',
  dashboard_totp_set: 'set up an authenticator app',
  dashboard_totp_removed: 'removed the authenticator app',
  dashboard_reset: 'reset sign-in for this dashboard',
  dashboard_clone_detected: 'signed in with a passkey that looked cloned',
  'dashboard.config.apply': 'applied a config change',
  'dashboard.config.restore': 'restored a previous config',
  'dashboard.approval': 'decided an approval',
  'dashboard.purchase': 'decided a purchase',
}

function actorWords(e: Entry): string {
  const op = String(e['operator'] ?? '')
  if (op) return op
  const addr = String(e['addr'] ?? '')
  return addr ? `Someone at ${addr}` : 'Someone'
}

function words(e: Entry): string {
  const ev = String(e['event'] ?? '')
  const fallback = ev.replace(/^dashboard\.?/, '').replace(/[._]/g, ' ').trim() || 'did something'
  return EVW[ev] ?? fallback
}

// isAccountable: a row a person caused, the way the Audit tab means it —
// dashboard_* (auth package) or dashboard.* (config/approval/purchase
// actions) — never the run engine's own internal telemetry, which shares the
// same file but isn't "who did what" in this sense.
function isAccountable(e: Entry): boolean {
  return /^dashboard[._]/.test(String(e['event'] ?? ''))
}

export function Audit(): JSX.Element {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  useEffect(() => {
    api.audit().then((r) => setEntries(r.entries ?? [])).catch((e) => setErr(humanError(e)))
  }, [])

  // Default tab: Audit (the prototype defaults to Daemon log, but this
  // screen's existing contract — the empty-box first-visit test — expects
  // the Audit empty state at the bare #/system/logs route; an explicit
  // /daemon segment still reaches the Daemon log).
  const tab: 'daemon' | 'audit' = seg(2) === 'daemon' ? 'daemon' : 'audit'

  const audit = useMemo(() => (entries ?? []).filter(isAccountable), [entries])
  const filtered = useMemo(() => {
    if (!q.trim()) return audit
    const needle = q.toLowerCase()
    return audit.filter((e) => JSON.stringify(e).toLowerCase().includes(needle))
  }, [audit, q])

  return (
    <div class="col">
      <div class="dl-phead"><div><h1>Logs &amp; audit</h1></div></div>
      {err ? <div class="banner err" role="alert">Couldn't load the log: {err}</div> : null}

      {entries === null ? (
        err ? null : <p class="muted">Loading…</p>
      ) : (
        <>
          <TabsBar
            items={[
              { id: 'daemon', label: 'Daemon log', href: '#/system/logs/daemon' },
              { id: 'audit', label: 'Audit', href: '#/system/logs/audit', count: audit.length },
            ]}
            active={tab}
            ariaLabel="Logs & audit"
          />
          {tab === 'daemon' ? <DaemonLog /> : <AuditTab entries={filtered} total={audit.length} q={q} setQ={setQ} />}
        </>
      )}
    </div>
  )
}

// DaemonLog: the process's own log isn't reachable from the dashboard today
// (no backend endpoint streams or tails it) — an honest, plain-words empty
// state, not a form that pretends to filter data that was never fetched.
function DaemonLog(): JSX.Element {
  return (
    <EmptyState testid="daemon-log-empty" title="The daemon's own log doesn't show here">
      <p>conductor's process log stays wherever the box keeps it (journald, or its own log file) — the dashboard doesn't read it yet. The Audit tab covers sign-ins, config changes and decisions made through the dashboard.</p>
    </EmptyState>
  )
}

function AuditTab({ entries, total, q, setQ }: { entries: Entry[]; total: number; q: string; setQ: (v: string) => void }): JSX.Element {
  if (total === 0) {
    return (
      <EmptyState testid="audit-empty" title="Nothing recorded yet">
        <p>Every change that matters is written here, newest first: config applied or restored, approvals given, passkeys added, sign-ins. Nothing has happened that needs a record.</p>
      </EmptyState>
    )
  }
  return (
    <>
      <div class="input-icon" style="max-width:360px;margin-bottom:8px">
        <Icon name="search" />
        <input class="input" placeholder="Find in the audit log" aria-label="Find in the audit log" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
      </div>
      <section class="dl-card au-card" aria-label="Audit log">
        <p class="dl-sub au-n">{entries.length} of {total} entries · append-only; every dashboard action is recorded with who did it and when</p>
        <ul class="au-list dl-list">
          {entries.length ? (
            entries.map((e, i) => <AuditRow key={i} e={e} />)
          ) : (
            <li class="dl-empty-row">No entry matches.</li>
          )}
        </ul>
      </section>
    </>
  )
}

function AuditRow({ e }: { e: Entry }): JSX.Element {
  const who = actorWords(e)
  return (
    <li>
      <details class="au-row">
        <summary>
          <span class="avatar au-you" aria-hidden="true">{who.slice(0, 2).toUpperCase()}</span>
          <span class="au-main">
            <span class="au-what"><b>{who}</b> {words(e)}</span>
          </span>
          <span class="dl-sub au-when">{e['ts'] ? ago(String(e['ts'])) : ''}</span>
        </summary>
        <dl class="dl-facts">
          <dt>Event</dt><dd class="mono">{String(e['event'] ?? '')}</dd>
          <dt>Who</dt><dd class="mono">{who}</dd>
          <dt>Detail</dt><dd>{summarize(e)}</dd>
          <dt>When</dt><dd>{e['ts'] ? new Date(String(e['ts'])).toLocaleString() : ''}</dd>
        </dl>
      </details>
    </li>
  )
}

function summarize(e: Entry): string {
  const skip = new Set(['ts', 'event', 'operator', 'session'])
  return Object.entries(e)
    .filter(([k]) => !skip.has(k))
    .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
    .join('  ')
}
