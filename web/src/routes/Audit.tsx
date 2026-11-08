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
import { Icon } from '../components/Icon'
import { FactsLine, type Fact } from '../components/Facts'
import { agoShort } from '../components/SysBits'
import { authState } from '../api/auth'
import { rangeMinutes } from '../lib/filter'
import { seg, query } from '../router'

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
  'dashboard.pause': 'paused all automation',
  'dashboard.resume': 'resumed automation',
  'dashboard.secret.write': 'stored a secret value',
  'dashboard.secret.delete': 'removed a secret value',
  'dashboard.update.install': 'installed an update',
  dashboard_stepup: 'confirmed it was them',
  dashboard_ssh_approved: 'approved a sign-in from the command line',
}

// actorWords: who did it (the prototype's actorWords): "You" for the
// operator signed in here, else the operator's name.
function actorWords(e: Entry): string {
  const op = String(e['operator'] ?? '')
  if (op) return op === authState.value.operator ? 'You' : op
  const addr = String(e['addr'] ?? '')
  return addr ? `Someone at ${hostOf(addr)}` : 'Someone'
}

const hostOf = (addr: string) => addr.replace(/:\d+$/, '').replace(/^\[|\]$/g, '')

// AREA (the prototype's AREA): what an entry is about, for the About menu and
// the first fact of its row.
const AREA: [string, string, RegExp, string][] = [
  ['signin', 'Sign-ins', /^dashboard_/, 'key'],
  ['bots', 'Bots and tickets', /^dashboard\.approval/, 'bots'],
  ['browser', 'Browsers', /^dashboard\.purchase/, 'globe'],
  ['config', 'Config and updates', /^dashboard\.(config|update)/, 'settings'],
  ['secrets', 'Secrets', /^dashboard\.secret/, 'secrets'],
  ['safety', 'Pausing automation', /^dashboard\.(pause|resume)/, 'shield'],
]
const areaOf = (e: Entry) => (AREA.find(([, , re]) => re.test(String(e['event'] ?? ''))) ?? ['other'])[0]

const METHOD_WORDS: Record<string, string> = { ssh: 'with the SSH key', passkey: 'with a passkey', password: 'with a password', totp: 'with an authenticator code' }

// auditFacts: the facts line under an entry — what it's about, then what
// the record holds (how, where from, what it touched).
function auditFacts(e: Entry): Fact[] {
  const fx: Fact[] = []
  const area = AREA.find(([, , re]) => re.test(String(e['event'] ?? '')))
  if (area) fx.push({ icon: area[3], value: area[1], label: 'About' })
  const addr = String(e['addr'] ?? '')
  if (addr) fx.push({ icon: 'globe', value: `from ${hostOf(addr)}${e['listener'] === 'tailscale' ? ' (tailnet)' : ''}`, label: 'Where from' })
  if (e['backup']) fx.push({ icon: 'clock', value: 'backed up first', label: 'Backup' })
  if (e['stepup'] === true || e['stepup'] === 'yes') fx.push({ icon: 'passkey', value: 'confirmed it was them', label: 'Confirmed' })
  return fx
}

function words(e: Entry): string {
  const ev = String(e['event'] ?? '')
  const fallback = ev.replace(/^dashboard\.?/, '').replace(/[._]/g, ' ').trim() || 'did something'
  const w = EVW[ev] ?? fallback
  const how = METHOD_WORDS[String(e['method'] ?? '')]
  const out = (ev === 'dashboard_signin' || ev === 'dashboard_stepup') && how ? `${w} ${how}` : w
  // "You were signed out", not "You was"
  return actorWords(e) === 'You' ? out.replace(/^was /, 'were ') : out
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
      <EmptyState hero icon="logs" testid="daemon-log-empty" title="Nothing in the log yet">
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
        qualifiers
        menus={[
          { key: 'level', label: 'Level', multi: true, selected: level, options: levelOpts, onToggle: (v) => toggle(setLevel, level, v) },
          { key: 'src', label: 'From', multi: true, selected: src, options: srcOpts, onToggle: (v) => toggle(setSrc, src, v) },
        ]}
        since={since}
        onSinceChange={setSince}
        sinceDef="7d"
        showTime
      />
      <div class="panel log-panel">
        <div class="panel-head">
          <StatusMark s="run" word="Following" sm />
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
  const [area, setArea] = useState<string[]>([])
  const [actor, setActor] = useState<string[]>([])
  const toggle = (set: (v: string[]) => void, cur: string[], v: string) => set(cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])
  const who = (e: Entry) => String(e['operator'] ?? e['addr'] ?? '')
  // ?run=<id> (a run page's "The full audit log for this run"): only entries
  // that name that run
  const [runF, setRunF] = useState(query('run'))
  useEffect(() => {
    const on = () => setRunF(query('run'))
    addEventListener('hashchange', on)
    return () => removeEventListener('hashchange', on)
  }, [])

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return entries.filter(
      (e) =>
        withinRange(String(e['ts'] ?? ''), since) &&
        (!area.length || area.includes(areaOf(e))) &&
        (!actor.length || actor.includes(who(e))) &&
        (!runF || e['run'] === runF || JSON.stringify(e).includes(runF)) &&
        (!needle || `${actorWords(e)} ${words(e)} ${JSON.stringify(e)}`.toLowerCase().includes(needle)),
    )
  }, [entries, since, q, area, actor, runF])

  if (entries.length === 0) {
    return (
      <EmptyState hero icon="logs" testid="audit-empty" title="Nothing recorded yet">
        <p>Every change that matters is written here, newest first: config applied or restored, approvals given, passkeys added, sign-ins. Nothing has happened that needs a record.</p>
      </EmptyState>
    )
  }
  const areaOpts = AREA.map(([v, label]) => ({ v, label, n: entries.filter((e) => areaOf(e) === v).length })).filter((o) => o.n)
  const actorOpts = [...new Set(entries.map(who))].map((v) => ({ v, label: actorWords(entries.find((e) => who(e) === v)!), n: entries.filter((e) => who(e) === v).length }))
  return (
    <>
      <FilterBar
        q={q}
        onQueryChange={setQ}
        placeholder="Find in the audit log"
        ariaLabel="Find and filter the audit log"
        filterByHint
        qualifiers
        menus={[
          { key: 'area', label: 'About', multi: true, selected: area, options: areaOpts, onToggle: (v) => toggle(setArea, area, v) },
          { key: 'actor', label: 'Who', multi: true, selected: actor, options: actorOpts, onToggle: (v) => toggle(setActor, actor, v) },
        ]}
        since={since}
        onSinceChange={setSince}
        sinceDef="7d"
        showTime
      />
      {runF ? <p class="dl-sub tk-showing">Only what concerns one run · <a class="link" href="#/system/logs/audit">Show everything</a></p> : null}
      <section class="dl-card au-card" aria-label="Audit log">
        <p class="dl-sub au-n">{filtered.length} of {entries.length} {entries.length === 1 ? 'entry' : 'entries'} · append-only; every dashboard action is recorded with who did it and when, and a sign-in with how and from where</p>
        <ul class="au-list">
          {filtered.length ? (
            filtered.map((e, i) => <AuditRow key={i} e={e} />)
          ) : (
            <li class="dl-empty-row">No entries match.</li>
          )}
        </ul>
      </section>
    </>
  )
}

function AuditRow({ e }: { e: Entry }): JSX.Element {
  const name = actorWords(e)
  const op = String(e['operator'] ?? '')
  const ts = String(e['ts'] ?? '')
  return (
    <li>
      <details class="au-row">
        <summary>
          {op ? (
            <span class="avatar au-you" aria-hidden="true">{op.slice(0, 2)}</span>
          ) : (
            <span class="sc-ic sm au-wf" aria-hidden="true"><Icon name="dot" /></span>
          )}
          <span class="au-main">
            <span class="au-what"><b>{name}</b> {words(e)}</span>
            <FactsLine facts={auditFacts(e)} />
          </span>
          <span class="dl-sub au-when">{ts ? `${agoShort(ts)} ago` : ''}</span>
        </summary>
        <dl class="dl-facts">
          <dt>Event</dt><dd class="mono">{String(e['event'] ?? '')}</dd>
          <dt>Who</dt><dd class="mono">{op ? `operator:${op}` : String(e['addr'] ?? '')}</dd>
          <dt>Detail</dt><dd>{summarize(e)}</dd>
          <dt>When</dt><dd>{ts ? new Date(ts).toLocaleString() : ''}</dd>
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
