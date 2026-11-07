// Logs & audit (CONFORMANCE "System · Logs & audit"): the prototype splits
// this into two sub-tabs (js/screens/system.js UI.route('logs/:tab?')),
// defaulting to Daemon log — "Daemon log" (the process's own log ring,
// internal/logring, read over GET /api/v1/system/log) and "Audit" (one
// sentence per operator-accountable action, newest first). conductor's real
// audit.jsonl (internal/store/audit.go) is a single append-only stream that
// both the dashboard's own actions AND the run engine's internal telemetry
// (step, dispatch, verb, decide, escalate, workflow_*…) write into, so the
// Audit tab reads that stream but shows only the entries a person did
// something to cause (dashboard_*/dashboard.* events — sign-ins, passkeys,
// config apply/restore, approvals, purchases), worded as a sentence the way
// the prototype's auditWords/EVW do; the engine's internal rows stay out of
// it rather than being exposed as raw key=value dumps.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { LogLine } from '../api/types'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { FilterBar } from '../components/FilterBar'
import { StatusMark } from '../components/StatusMark'
import { TabsBar } from '../components/TabsBar'
import { ago } from '../lib/format'
import { rangeMinutes } from '../lib/filter'
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

function withinRange(iso: string, since: string): boolean {
  const mins = rangeMinutes(since)
  if (!mins) return true
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return true
  return Date.now() - t <= mins * 60_000
}

export function Audit(): JSX.Element {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.audit().then((r) => setEntries(r.entries ?? [])).catch((e) => setErr(humanError(e)))
  }, [])

  // The prototype defaults Logs & audit to Daemon log; the legacy #/audit
  // shortcut (app.tsx's LEGACY_SYSTEM_TABS) asks for the Audit tab
  // explicitly, so it still lands there.
  const tab: 'daemon' | 'audit' = seg(2) === 'audit' ? 'audit' : 'daemon'

  const audit = useMemo(() => (entries ?? []).filter(isAccountable), [entries])

  // Plain div, not .col: .col's flex `gap` would add its stack gap between
  // the title and the Daemon log/Audit sub-tabs that the design doesn't have
  // — the same page-title gap bug fixed on System's own tab bar (System.tsx).
  // The sub-tab row's own margin-bottom (.tabs) is spacing enough before the
  // tab body below it.
  return (
    <div class="m-page">
      <div class="dl-phead" style="margin-bottom:4px"><div><h1>Logs &amp; audit</h1></div></div>
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
          {tab === 'daemon' ? <DaemonLog /> : <AuditTab entries={audit} />}
        </>
      )}
    </div>
  )
}

// DaemonLog reads the process's own recent log lines (internal/logring, over
// GET /api/v1/system/log) — not a stand-in: an empty ring (no backend
// wired, e.g. an old daemon) says so honestly instead of pretending to
// filter data that was never fetched. It polls while mounted, matching the
// design's "Following".
function DaemonLog(): JSX.Element {
  const [lines, setLines] = useState<LogLine[] | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [level, setLevel] = useState<string[]>([])
  const [src, setSrc] = useState<string[]>([])
  const [since, setSince] = useState('7d')

  useEffect(() => {
    let stop = false
    const poll = () => api.daemonLog().then((r) => { if (!stop) setLines(r.lines ?? []) }).catch((e) => { if (!stop) setErr(humanError(e)) })
    void poll()
    const id = setInterval(poll, 4000)
    return () => { stop = true; clearInterval(id) }
  }, [])

  const toggle = (set: (v: string[]) => void, cur: string[], v: string) => set(cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])

  const all = lines ?? []
  const filtered = useMemo(
    () =>
      all.filter(
        (l) =>
          withinRange(l.time, since) &&
          (!level.length || level.includes(l.level)) &&
          (!src.length || src.includes(l.source)) &&
          (!q.trim() || (l.source + ' ' + l.message).toLowerCase().includes(q.toLowerCase())),
      ),
    [all, level, src, since, q],
  )

  if (lines === null) return err ? <div class="banner err" role="alert">Couldn't load the daemon log: {err}</div> : <p class="muted">Loading…</p>

  if (!all.length) {
    return (
      <EmptyState testid="daemon-log-empty" title="Nothing in the log yet">
        <p>conductor's own recent log lines show here once it has written any since it started. The Audit tab covers sign-ins, config changes and decisions made through the dashboard.</p>
      </EmptyState>
    )
  }

  const levelOpts = [
    ['info', 'Info'],
    ['warn', 'Warnings'],
    ['err', 'Errors'],
  ].map(([v, label]) => ({ v, label, n: all.filter((l) => l.level === v).length })).filter((o) => o.n)
  const srcOpts = [...new Set(all.map((l) => l.source))].map((v) => ({ v, label: v, n: all.filter((l) => l.source === v).length }))

  return (
    <>
      <FilterBar
        q={q}
        onQueryChange={setQ}
        placeholder="Find in the log"
        ariaLabel="Find and filter the daemon log"
        filterByHint
        menus={[
          { key: 'level', label: 'Level', multi: true, selected: level, options: levelOpts, onToggle: (v) => toggle(setLevel, level, v) },
          { key: 'src', label: 'From', multi: true, selected: src, options: srcOpts, onToggle: (v) => toggle(setSrc, src, v) },
        ]}
        since={since}
        onSinceChange={setSince}
        showTime
      />
      <div class="panel log-panel">
        <div class="panel-head">
          <StatusMark s="run" word="Following" />
          <span class="dl-sub">{filtered.length} of {all.length} lines</span>
        </div>
        <div class="log">
          {filtered.length ? (
            filtered.map((l, i) => (
              <div key={i} class={'l' + (l.level === 'err' ? ' err' : l.level === 'warn' ? ' warn' : '')}>
                <span class="t">{new Date(l.time).toTimeString().slice(0, 8)}</span>
                <span class="src">{l.source}</span>
                <span class="m">{l.message}</span>
              </div>
            ))
          ) : (
            <div class="l"><span class="m muted">No lines match.</span></div>
          )}
        </div>
        <div class="panel-foot small muted">From the daemon's own log ring (journald / launchd log is unchanged). Secret values are redacted at the source.</div>
      </div>
    </>
  )
}

function AuditTab({ entries }: { entries: Entry[] }): JSX.Element {
  const [q, setQ] = useState('')
  const [since, setSince] = useState('7d')

  const filtered = useMemo(() => {
    const byRange = entries.filter((e) => withinRange(String(e['ts'] ?? ''), since))
    if (!q.trim()) return byRange
    const needle = q.toLowerCase()
    return byRange.filter((e) => JSON.stringify(e).toLowerCase().includes(needle))
  }, [entries, since, q])

  if (entries.length === 0) {
    return (
      <EmptyState testid="audit-empty" title="Nothing recorded yet">
        <p>Every change that matters is written here, newest first: config applied or restored, approvals given, passkeys added, sign-ins. Nothing has happened that needs a record.</p>
      </EmptyState>
    )
  }
  return (
    <>
      <FilterBar q={q} onQueryChange={setQ} placeholder="Find in the audit log" ariaLabel="Find and filter the audit log" since={since} onSinceChange={setSince} showTime />
      <section class="dl-card au-card" aria-label="Audit log">
        <p class="dl-sub au-n">{filtered.length} of {entries.length} entries · append-only; every dashboard action is recorded with who did it and when</p>
        <ul class="au-list dl-list">
          {filtered.length ? (
            filtered.map((e, i) => <AuditRow key={i} e={e} />)
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
