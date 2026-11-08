// Home (the Overview), ported from the approved prototype (design/dashboard-
// prototype js/screens/overview.js UI.route('overview')): At a glance, Needs
// you, Running now, the last 12 hours and Get started. Everything here is
// live, from the real daemon (design §8) — not the prototype's fixtures. The
// Shell already wraps this in main > .page.dl-page.
//
// Deliberate differences from the prototype, both noted in CONFORMANCE:
// Activity (run history) is its own page (design: a structural decision) and
// no longer duplicated here; the machine's health moved to the rail's host
// card (Shell.tsx).
//
// Left out this phase (prototype-only, no backend yet): the usage/cost panel
// (usage display is off by default and has no server-side toggle here).
//
// A fresh box is never a single centred message: the layout (the tiles, Needs
// you, the runs strip) stays up, each panel empty on its own, so the page
// never collapses to less than the approved design shows (design review note).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { status, runs, loadError, liveTick, needItems, needBreakdown, fyi } from '../state'
import type { NeedItem } from '../state'
import { botsApi, type BotSummary, type ProposalRow } from '../api/bots'
import { api } from '../api/client'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { EmptyState } from '../components/EmptyState'
import { Fyi } from '../components/Fyi'
import { FactsLine } from '../components/Facts'
import { Icon } from '../components/Icon'
import { TIME_RANGES, rangeMinutes } from '../lib/filter'
import { ago, plural } from '../lib/format'
import { gsHidden, setGsHidden } from '../lib/gs'
import { toast, ToastHost } from '../components/ChatBits'
import { hm, runningFor, runTarget, runTitle } from '../lib/runwords'

const HOUR = 3_600_000

function startedMs(r: RunSummary): number {
  return new Date(r.started).getTime()
}

export function Overview(): JSX.Element {
  // Read liveTick so the whole screen re-renders on each SSE event (the pulse).
  const tick = liveTick.value
  const all = runs.value
  const st = status.value
  const now = Date.now()
  const last12 = all.filter((r) => startedMs(r) >= now - 12 * HOUR)

  const running = all.filter((r) => r.status === 'running').sort((a, b) => startedMs(a) - startedMs(b))
  // Needs you: one aggregator (state.needItems) for every kind the daemon can
  // tell us about today — paused, pending approvals, pending purchases and
  // failed runs the operator hasn't dismissed.
  const need = needItems()

  // Get started's own signals (connectors, bots, decided approvals): light,
  // best-effort reads that just leave a step undone rather than blocking Home.
  const [bots, setBots] = useState<BotSummary[]>([])
  const [botsConfigured, setBotsConfigured] = useState(true)
  const [connected, setConnected] = useState(0)
  const [decided, setDecided] = useState(false)
  useEffect(() => {
    botsApi
      .bots()
      .then((r) => {
        setBots(r.bots)
        setBotsConfigured(r.configured ?? true)
      })
      .catch(() => setBots([]))
    api
      .connections()
      .then((r) => setConnected(r.connectors.length))
      .catch(() => setConnected(0))
    botsApi
      .proposals()
      .then((r) => setDecided(r.proposals.some((p: ProposalRow) => p.status !== 'pending')))
      .catch(() => setDecided(false))
  }, [])

  return (
    <div data-tick={tick}>
      <h1 class="sr-only">Home</h1>

      {loadError.value ? <div class="banner err">Couldn't load: {loadError.value}</div> : null}

      {st === null ? null : (
        // Always the full layout, even on a genuinely fresh box: the tiles
        // read zero, Needs you and For your information show their own small
        // empty state, and Running now simply has nothing to list (as in the
        // prototype). Get started carries PR 242's first-time guidance ("See
        // your workflows" lives at Activity's own empty state; "Meet your
        // bots" is this card's "Make a bot" step).
        <>
          <Glance need={need} running={running} last12={last12} bots={bots} />
          <div class="dl-cols">
            <div class="dl-col-main">
              <NeedsYou need={need} />
            </div>
            <div class="dl-col-side">
              <RunningNow running={running} />
              <RunsCard runs={all} />
              <Fyi items={fyi.value} />
            </div>
          </div>
          <GetStarted connected={connected} bots={bots} botsConfigured={botsConfigured} decided={decided} />
          <ToastHost />
        </>
      )}
    </div>
  )
}

// Glance is the mock's "At a glance": four quiet figures, Needs you first and
// agreeing with its own card and the tab bar's badge (UI.statTile/UI.glance).
function Glance({ need, running, last12, bots }: { need: NeedItem[]; running: RunSummary[]; last12: RunSummary[]; bots: BotSummary[] }): JSX.Element {
  const longest = running.length ? runningFor(new Date(Math.min(...running.map(startedMs))).toISOString()) : ''
  const breakdown = needBreakdown(need)
  const working = bots.filter((b) => !b.disabled && !b.shadow).length
  return (
    <section class="dl-glance" aria-label="At a glance">
      <Tile label="Needs you" value={String(need.length)} detail={breakdown || 'nothing'} hot={need.length > 0} href="#/attention" />
      <Tile label="Running" value={String(running.length)} detail={running.length ? `longest ${longest}` : 'nothing right now'} href="#/activity?status=running" />
      <Tile label="Runs, last 12 h" value={String(last12.length)} detail={runParts(last12) || 'none'} href="#/activity" />
      <Tile label="Bots" value={String(bots.length)} detail={`${working} working now`} href="#/bots" />
    </section>
  )
}

function runParts(rs: RunSummary[]): string {
  const parts: [number, string][] = [
    [rs.filter((r) => r.status === 'ok').length, 'went fine'],
    [rs.filter((r) => r.status === 'failed').length, 'failed'],
    [rs.filter((r) => r.status === 'running').length, 'running'],
    [rs.filter((r) => r.status === 'retried').length, 'retried'],
  ]
  return parts
    .filter(([n]) => n)
    .map(([n, t]) => `${n} ${t}`)
    .join(' · ')
}

function Tile({ label, value, detail, href, hot }: { label: string; value: string; detail?: string; href: string; hot?: boolean }): JSX.Element {
  return (
    <a class={'dl-tile' + (hot ? ' hot' : '')} href={href}>
      <span class="dl-lbl">{label}</span>
      <b>{value}</b>
      {detail ? <span class="d">{detail}</span> : null}
    </a>
  )
}

// NeedsYou: the mock's list (mark, plain title, why, the age, one verb). Every
// kind the daemon exposes today shares this one row shape and vocabulary.
function NeedsYou({ need }: { need: NeedItem[] }): JSX.Element {
  return (
    <section class="dl-card dl-needs-card" aria-label="Needs you">
      <header class="dl-ch">
        <h2>Needs you</h2>
        {need.length ? <span class="count hot">{need.length}</span> : null}
        <span class="dl-sp" />
        <a class="dl-link" href="#/attention">See all</a>
      </header>
      {need.length === 0 ? (
        <EmptyState
          small
          title="Nothing needs you"
          testid="needs-you-empty"
          action={{ label: "Choose how you're told", href: '#/preferences' }}
        >
          <p>When a bot asks for approval or a run fails, it shows up here.</p>
        </EmptyState>
      ) : (
        <ul class="dl-list dl-needs">
          {need.slice(0, 5).map((n) => (
            <li class="dl-need" data-s={n.mark} key={n.href}>
              <a class="dl-need-a" href={n.href}>
                <span class="dl-ng">
                  <StatusMark s={n.mark} mkOnly lg label={n.mark === 'fail' ? 'failed' : 'needs you'} />
                </span>
                <span class="dl-nt">
                  <b>{n.title}</b>
                  {n.why ? <span class="dl-why">{n.why}</span> : null}
                  <FactsLine facts={n.facts || []} />
                </span>
                <span class="dl-age">{n.at ? ago(n.at) : ''}</span>
                <span class="dl-na">
                  <span class="btn sm secondary">{n.verb}</span>
                </span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {need.length > 5 ? (
        <p class="dl-foot-note dl-more">
          <a class="dl-link" href="#/attention">{`${need.length - 5} more need${need.length - 5 === 1 ? 's' : ''} you →`}</a>
        </p>
      ) : null}
    </section>
  )
}


function RunningNow({ running }: { running: RunSummary[] }): JSX.Element | null {
  if (running.length === 0) return null
  const top = running.slice(0, 3)
  return (
    <section class="dl-card dl-running" aria-label="Running now">
      <header class="dl-ch">
        <h2>Running now</h2>
        <span class="dl-sub">{running.length}</span>
      </header>
      <ul class="dl-list">
        {top.map((r) => (
          <li key={r.id}>
            <a class="dl-row-a" href={'#/runs/' + r.id}>
              <span class="dl-ng">
                <StatusMark s="run" mkOnly lg label="Running" />
              </span>
              <span class="dl-nt">
                <b>{runTitle(r)}</b>
                <span class="dl-why">{r.workflow.startsWith('bot:') ? runTarget(r) || 'its own work' : `${r.workflow} · ${runTarget(r) || r.kind}`}</span>
              </span>
              <span class="dl-age">{runningFor(r.started)}</span>
            </a>
          </li>
        ))}
      </ul>
      {running.length > 3 ? (
        <a class="dl-link dl-more" href="#/activity?status=run">{`+${running.length - 3} more running →`}</a>
      ) : null}
    </section>
  )
}

// RunsCard: the prototype's "Runs" card (overview.js last12()) — a title with
// its own time-range dropdown, a "N of M runs went fine" stat line, the
// per-bucket bars (passed soft green with a full-green cap, running blue,
// failed red on top, the rest neutral) and a failed-runs footnote. Static on
// Home (no hover, no tap, no per-bar readout — the owner's call, unlike
// Activity's interactive strip).
function RunsCard({ runs: all }: { runs: RunSummary[] }): JSX.Element {
  const [since, setSince] = useState('12h')
  const span = rangeMinutes(since) || 43200
  const now = Date.now()
  const rs = all.filter((r) => since === 'all' || now - startedMs(r) <= span * 60000)

  const n = span <= 60 ? 12 : span <= 1440 ? Math.round(span / 60) : span <= 10080 ? 7 : 30
  const bucketMin = span / n
  const buckets: { ok: number; fail: number; run: number; other: number }[] = []
  for (let i = 0; i < n; i++) buckets.push({ ok: 0, fail: 0, run: 0, other: 0 })
  rs.forEach((r) => {
    const ago2 = (now - startedMs(r)) / 60000
    if (ago2 > span) return
    const i = Math.min(n - 1, Math.max(0, n - 1 - Math.floor(ago2 / bucketMin)))
    const b = buckets[i]
    if (r.status === 'ok') b.ok++
    else if (r.status === 'failed') b.fail++
    else if (r.status === 'running') b.run++
    else b.other++
  })

  const max = Math.max(1, ...buckets.map((b) => b.ok + b.fail + b.run + b.other))
  const total = rs.length
  const ok = rs.filter((r) => r.status === 'ok').length
  const counts = {
    ok,
    fail: rs.filter((r) => r.status === 'failed').length,
    run: rs.filter((r) => r.status === 'running').length,
    other: rs.filter((r) => r.status === 'retried').length,
  }
  const fails = rs.filter((r) => r.status === 'failed').sort((a, b) => startedMs(b) - startedMs(a))
  const w = 300
  const h = 96
  const base = h - 17
  const bw = w / n - 6
  const label = TIME_RANGES.find(([k]) => k === since)?.[1] || since
  const agoLabel = span <= 1440 ? `${Math.round(span / 60) || 1} h ago` : span === 1440 ? '24 h ago' : `${Math.round(span / 1440)} days ago`

  return (
    <section class="dl-card ov-runs" aria-label="Runs">
      <header class="dl-ch">
        <h2>Runs</h2>
        <TimeRangeMenu since={since} onChange={setSince} />
      </header>
      <div class="dl-stat-line">
        <b>{ok}</b>
        <span>of {total} runs went fine</span>
        {localStorage.getItem('usage-mode') === 'api' ? (
          <>
            <b class="r">${rs.reduce((a, r) => a + (r.cost_usd || 0), 0).toFixed(2)}</b>
            <span>spent</span>
          </>
        ) : null}
      </div>
      <svg class="dl-hours" viewBox={`0 0 ${w} ${h}`} role="img" aria-label={`${plural(total, 'run')}, ${label.toLowerCase()}: ${plural(fails.length, 'failure')}`}>
        {buckets.map((b, i) => {
          const x = i * (w / n) + 3
          let y = base
          // from the axis up: passed, waiting or parked, running, failed on top (UI.stackBars' SEG)
          const order: ['fail' | 'run' | 'other' | 'ok', string][] = [
            ['ok', 'hb-ok'],
            ['other', 'hb-other'],
            ['run', 'hb-run'],
            ['fail', 'hb-fail'],
          ]
          const segs = order.filter(([k]) => b[k])
          return (
            <g class="hb" key={i}>
              {segs.map(([k, cls], j) => {
                const hh = (b[k] / max) * (h - 22)
                y -= hh
                const gap = j ? 2 : 0
                return (
                  <>
                    <rect x={x} y={y} width={bw} height={Math.max(0, hh - gap)} rx={2} class={cls} />
                    {k === 'ok' ? <rect x={x} y={y} width={bw} height={Math.min(2, hh - gap)} class="hb-cap" /> : null}
                  </>
                )
              })}
            </g>
          )
        })}
        <line x1={0} x2={w} y1={base} y2={base} class="hb-axis" />
        <text x={0} y={h - 3} class="hb-ax">
          {agoLabel}
        </text>
        <text x={w} y={h - 3} class="hb-ax" text-anchor="end">
          now
        </text>
      </svg>
      <div class="hb-foot">
        <ul class="hb-legend">
          <li>
            <i class="hb-sw hb-sw-ok" aria-hidden="true" />
            Passed <b>{counts.ok}</b>
          </li>
          <li>
            <i class="hb-sw hb-sw-fail" aria-hidden="true" />
            Failed <b>{counts.fail}</b>
          </li>
          <li>
            <i class="hb-sw hb-sw-run" aria-hidden="true" />
            Running <b>{counts.run}</b>
          </li>
          <li>
            <i class="hb-sw hb-sw-other" aria-hidden="true" />
            Waiting or parked <b>{counts.other}</b>
          </li>
        </ul>
      </div>
      {fails.length ? (
        <p class="dl-foot-note">
          <StatusMark s="fail" mkOnly />
          <span>
            {plural(fails.length, 'failed', 'failed')}:{' '}
            {fails.slice(0, 4).map((r, i) => (
              <>
                {i ? ', ' : ''}
                <a class="dl-link" href={'#/runs/' + r.id}>
                  {r.workflow} at {hm(r.finished || r.started)}
                </a>
              </>
            ))}
            {fails.length > 4 ? ` and ${fails.length - 4} more` : ''}
          </span>
        </p>
      ) : (
        <p class="dl-foot-note">
          <StatusMark s="ok" mkOnly />
          Nothing failed.
        </p>
      )}
    </section>
  )
}

// TimeRangeMenu: a small dropdown in a card header (the prototype's
// UI.filterBar({noSearch:true}) used just for its time field) — a details/
// summary chip, not the full search-and-filter bar FilterBar.tsx renders.
function TimeRangeMenu({ since, onChange }: { since: string; onChange: (v: string) => void }): JSX.Element {
  const label = TIME_RANGES.find(([k]) => k === since)?.[1] || since
  // Wrapped in a bare .fb so the shared filter CSS's phone override
  // (`.dl-ch > .fb .fb-time { display: inline-flex }`) keeps this control
  // visible on a phone — the generic `.fb-btn { display: none }` phone rule
  // only backs off for a `.fb-time` that is a descendant of a `.fb` sitting
  // directly in a `.dl-ch` header, same as the full FilterBar's own time button.
  return (
    <span class="fb" data-nosearch style={{ display: 'inline-flex' }}>
      <details class="fb-time-menu" style={{ position: 'relative', display: 'inline-block' }}>
        <summary class={'fb-btn fb-time' + (since !== '12h' ? ' on' : '')} aria-haspopup="listbox" title="Show runs from">
          <Icon name="clock" />
          <span class="fb-v">{label}</span>
          <Icon name="chevd" />
        </summary>
        <div class="fb-menu" role="listbox" aria-label="Time range" style={{ position: 'absolute', top: 'calc(100% + 6px)', right: 0, zIndex: 2 }}>
          <ul class="fb-list" role="presentation">
            {TIME_RANGES.filter(([k]) => k !== 'all').map(([k, l]) => (
              <li key={k} role="option" aria-selected={since === k} class={'fb-opt' + (since === k ? ' sel' : '')} onClick={(e) => { onChange(k); (e.currentTarget.closest('details') as HTMLDetailsElement).open = false }}>
                <Icon name="check" />
                <span class="fb-ol"><b>{l}</b></span>
              </li>
            ))}
          </ul>
        </div>
      </details>
    </span>
  )
}

// GetStarted: four plain steps, only what's left, the next step as the one
// primary button (design: no risk ladder, no progress meter — a quiet ticked
// line for what's done). "Check what your bots can do" and a Preferences page
// don't exist yet, so the safety step isn't offered; the other three read
// real signals (connectors, bots, whether any approval has ever been decided).
function GetStarted({
  connected,
  bots,
  botsConfigured,
  decided,
}: {
  connected: number
  bots: BotSummary[]
  botsConfigured: boolean
  decided: boolean
}): JSX.Element | null {
  const hidden = gsHidden.value
  const [seenSafety, setSeenSafety] = useState(() => localStorage.getItem('seen-safety') === '1')
  useEffect(() => {
    if (seenSafety) return
    const onStorage = () => setSeenSafety(localStorage.getItem('seen-safety') === '1')
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [seenSafety])
  if (hidden) return null
  const steps = [
    { id: 'connect', done: connected > 0, title: 'Connect a service', text: 'GitHub, Slack, PagerDuty… so bots can see events and act.', href: '#/connections', go: 'Plugins' },
    { id: 'watch', done: bots.length > 0, title: 'Make a bot that watches something', text: 'A bot is an AI teammate with memory; tell it what to watch and where to tell you.', href: botsConfigured ? '#/bots?new=1' : '#/bots', go: 'New bot' },
    { id: 'safety', done: seenSafety, title: 'Check what your bots can do', text: 'In plain words: what each bot does on its own, what it asks you first, and what it never does.', href: '#/system/safety', go: 'Safety' },
    { id: 'approve', done: decided, title: 'Try an approval', text: 'Risky actions wait for you. Decide one, and it runs once, exactly as approved.', href: '#/attention', go: 'Needs you' },
  ]
  const left = steps.filter((s) => !s.done)
  const done = steps.filter((s) => s.done)
  if (!left.length) return null
  return (
    <section class="dl-card dl-gs" aria-label="Get started">
      <header class="dl-ch">
        <h2>Get started</h2>
        <span class="dl-sub">{done.length} of {steps.length} done</span>
        <span class="dl-sp" />
        <button
          class="btn sm ghost"
          type="button"
          onClick={() => {
            setGsHidden(true)
            toast('Get started hidden', 'Bring it back from your menu, top right: Show Get started.')
          }}
        >
          Hide
        </button>
      </header>
      <ul class="dl-list">
        {left.map((s) => (
          <li class="dl-gs-step" key={s.id}>
            <div class="dl-nt">
              <b>{s.title}</b>
              <span class="dl-why">{s.text}</span>
            </div>
            <a class="btn sm secondary" href={s.href}>{s.go}</a>
          </li>
        ))}
      </ul>
      {done.length ? (
        <p class="dl-gs-done">
          <StatusMark s="ok" mkOnly />
          Done: {done.map((s) => s.title).join(' · ')}
        </p>
      ) : null}
    </section>
  )
}
