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
import { status, runs, loadError, liveTick, needItems, needBreakdown, dismiss, fyi } from '../state'
import type { NeedItem } from '../state'
import { botsApi, type BotSummary, type ProposalRow } from '../api/bots'
import { api } from '../api/client'
import type { RunSummary } from '../api/types'
import { StatusMark } from '../components/StatusMark'
import { EmptyState } from '../components/EmptyState'
import { Fyi } from '../components/Fyi'
import { humanDuration, ago, plural } from '../lib/format'

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

  const running = all.filter((r) => r.status === 'running').sort((a, b) => b.duration_ms - a.duration_ms)
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
              <Last12 runs={last12} />
              <Fyi items={fyi.value} />
            </div>
          </div>
          <GetStarted connected={connected} bots={bots} botsConfigured={botsConfigured} decided={decided} />
        </>
      )}
    </div>
  )
}

// Glance is the mock's "At a glance": four quiet figures, Needs you first and
// agreeing with its own card and the tab bar's badge (UI.statTile/UI.glance).
function Glance({ need, running, last12, bots }: { need: NeedItem[]; running: RunSummary[]; last12: RunSummary[]; bots: BotSummary[] }): JSX.Element {
  const longest = running.length ? humanDuration(Math.max(...running.map((r) => r.duration_ms))) : ''
  const breakdown = needBreakdown(need)
  const working = bots.filter((b) => !b.disabled && !b.shadow).length
  return (
    <section class="dl-glance" aria-label="At a glance">
      <Tile label="Needs you" value={String(need.length)} detail={breakdown || 'nothing'} hot={need.length > 0} href="#/attention" />
      <Tile label="Running" value={String(running.length)} detail={running.length ? `longest ${longest}` : 'nothing right now'} href="#/activity" />
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
  const paused = need.some((n) => n.kind === 'paused')
  return (
    <section class="dl-card dl-needs-card" aria-label="Needs you">
      <header class="dl-ch">
        <h2>Needs you</h2>
        {need.length ? <span class="count hot">{need.length}</span> : null}
      </header>
      {need.length === 0 ? (
        <EmptyState small title="Nothing needs you" testid="needs-you-empty">
          <p>When a bot asks for approval or a run fails, it shows up here.</p>
        </EmptyState>
      ) : (
        <ul class="dl-list dl-needs">
          {need.slice(0, 5).map((n) => (
            <li class="dl-need" data-s={n.mark} key={n.href}>
              <a class="dl-need-a" href={n.href}>
                <span class="dl-ng">
                  <StatusMark s={n.mark} mkOnly />
                </span>
                <span class="dl-nt">
                  <b>{n.title}</b>
                  {n.why ? <span class="dl-why">{n.why}</span> : null}
                </span>
                <span class="dl-age">{n.at ? ago(n.at) : ''}</span>
                <span class="dl-na">
                  <span class="btn sm secondary">{n.verb}</span>
                </span>
              </a>
              {n.kind === 'failed' ? (
                <button class="btn sm ghost" type="button" title="Dismiss from Needs you" onClick={() => dismiss(n.href.replace('#/runs/', ''))}>
                  Dismiss
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {paused ? <p class="dl-sub">All automation is paused.</p> : null}
    </section>
  )
}

function runTitle(r: RunSummary): string {
  const target = r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : ''
  return target ? `${r.workflow} · ${target}` : r.workflow
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
                <StatusMark s="run" mkOnly />
              </span>
              <span class="dl-nt">
                <b>{runTitle(r)}</b>
                <span class="dl-why mono">{r.id}</span>
              </span>
              <span class="dl-age">{humanDuration(r.duration_ms)}</span>
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

// Last12 is the runs-per-hour strip: every hour a bar, passed soft green with a
// full-green cap, running blue, failed red on top, the rest neutral. Static on
// Home (no hover, no tap, no per-bar readout — the owner's call).
function Last12({ runs: rs }: { runs: RunSummary[] }): JSX.Element {
  const now = Date.now()
  const buckets: { ok: number; fail: number; run: number; other: number }[] = []
  for (let i = 11; i >= 0; i--) {
    const end = now - i * HOUR
    const startB = end - HOUR
    const inB = rs.filter((r) => {
      const t = startedMs(r)
      return t >= startB && t < end
    })
    buckets.push({
      ok: inB.filter((r) => r.status === 'ok').length,
      fail: inB.filter((r) => r.status === 'failed').length,
      run: inB.filter((r) => r.status === 'running').length,
      other: inB.filter((r) => r.status === 'retried').length,
    })
  }
  const max = Math.max(1, ...buckets.map((b) => b.ok + b.fail + b.run + b.other))
  const total = rs.length
  const counts = {
    ok: rs.filter((r) => r.status === 'ok').length,
    fail: rs.filter((r) => r.status === 'failed').length,
    run: rs.filter((r) => r.status === 'running').length,
  }
  const w = 300
  const h = 96
  const base = h - 17
  const n = buckets.length
  const bw = w / n - 6

  return (
    <section class="dl-card" aria-label="Last 12 hours">
      <header class="dl-ch">
        <h2>Last 12 hours</h2>
        <span class="dl-sub">{plural(total, 'run')}</span>
      </header>
      <svg class="dl-hours" viewBox={`0 0 ${w} ${h}`} role="img" aria-label={`${plural(total, 'run')} over the last 12 hours`}>
        {buckets.map((b, i) => {
          const x = i * (w / n) + 3
          let y = base
          const order: ['fail' | 'run' | 'other' | 'ok', string][] = [
            ['fail', 'hb-fail'],
            ['run', 'hb-run'],
            ['other', 'hb-other'],
            ['ok', 'hb-ok'],
          ]
          return (
            <g class="hb" key={i}>
              {order.map(([k, cls], j) => {
                if (!b[k]) return null
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
          12 h ago
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
        </ul>
      </div>
    </section>
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
  const [hidden, setHidden] = useState(() => localStorage.getItem('gs-hidden') === '1')
  if (hidden) return null
  const steps = [
    { id: 'connect', done: connected > 0, title: 'Connect a service', text: 'GitHub, Slack, PagerDuty… so bots can see events and act.', href: '#/connections', go: 'Plugins' },
    { id: 'watch', done: bots.length > 0, title: 'Make a bot', text: 'A bot is an AI teammate with memory; tell it what to watch and where to tell you.', href: '#/bots', go: botsConfigured ? 'Bots' : 'New bot' },
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
            localStorage.setItem('gs-hidden', '1')
            setHidden(true)
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
