// The Bots screen (design §4.4, "teams are the page"): each team is a section
// whose header states its shared rules in words, with its bots below; solo bots
// come last. Group by Team or None. New bot opens a sheet with the identity
// picker. Managing a bot's settings and faces is on its page; this is the roster.
// With no bots yet it is a welcome instead: what a bot is for and one way to
// make the first, staged and applied right here.
import type { JSX } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { botsApi, displayName, type BotSummary, type TeamSummary } from '../api/bots'
import { humanError } from '../api/errors'
import { Avatar, AvatarPreview } from '../components/Avatar'
import { EmptyState } from '../components/EmptyState'
import { ReviewPanel } from '../components/ReviewPanel'
import { apply, applyResult, busy, doReview, loadConfig, review, staged } from '../configStore'
import { api } from '../api/client'
import { NewBotSheet } from './NewBotSheet'
import { plural } from '../lib/format'

export function Bots(): JSX.Element {
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [editable, setEditable] = useState(true)
  const [err, setErr] = useState('')
  const [groupBy, setGroupBy] = useState<'team' | 'none'>('team')
  const [q, setQ] = useState('')
  const [mode, setMode] = useState<'all' | 'live' | 'practice' | 'archived'>('all')
  const [newOpen, setNewOpen] = useState(false)
  // The cli runtimes a bot's workers can run on (null: not known yet).
  const [workers, setWorkers] = useState<string[] | null>(null)

  const load = () =>
    botsApi
      .bots()
      .then((r) => {
        setBots(r.bots || [])
        setTeams(r.teams || [])
        setEditable(r.editable !== false)
        setErr('')
        return (r.bots || []).length
      })
      .catch((e) => {
        setErr(humanError(e))
        return -1
      })
  useEffect(() => {
    void load()
    void loadConfig()
    api
      .connections()
      .then((c) => setWorkers((c.runtimes ?? []).filter((r) => r.type === 'cli').map((r) => r.name)))
      .catch(() => setWorkers(null))
  }, [])

  // An apply made here reloads the daemon; a new bot shows once it is back.
  // (applyResult is shared, so one left from another screen doesn't count.)
  const appliedAtMount = useRef(applyResult.value)
  const applied = applyResult.value && applyResult.value !== appliedAtMount.current ? applyResult.value : null
  const before = useRef(0)
  const [gaveUp, setGaveUp] = useState(false)
  useEffect(() => {
    if (!applied) return
    before.current = bots?.length ?? 0
    setGaveUp(false)
    let tries = 0
    let stop = false
    const poll = () => {
      if (stop) return
      void load().then((n) => {
        if (stop || n > before.current) return
        if (++tries < 20) setTimeout(poll, 1000)
        else setGaveUp(true)
      })
    }
    const t = setTimeout(poll, 800)
    return () => {
      stop = true
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applied])

  const all = bots || []
  const filtered = useMemo(() => {
    return all.filter((b) => {
      if (q && !(`${b.bot} ${displayName(b.identity, b.bot)} ${b.description}`.toLowerCase().includes(q.toLowerCase()))) return false
      if (mode === 'live' && (b.shadow || b.disabled)) return false
      if (mode === 'practice' && !b.shadow) return false
      if (mode === 'archived' && !b.disabled) return false
      return true
    })
  }, [all, q, mode])

  const soloCount = all.filter((b) => !b.team).length
  const pendingBots = staged.value.filter((c) => c.path[0] === 'bots').length
  const noWorkers = workers !== null && workers.length === 0
  const sheet = newOpen ? <NewBotSheet teams={teams} workers={workers} onClose={() => setNewOpen(false)} onStaged={() => setNewOpen(false)} /> : null

  if (bots === null) {
    return (
      <div class="col bots-screen">
        <h1 class="screen-title serif">Bots</h1>
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load your bots: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="muted">Loading…</p>
        )}
      </div>
    )
  }

  if (all.length === 0) {
    return (
      <div class="col bots-screen">
        <h1 class="screen-title serif">Bots</h1>
        {err ? <div class="banner err" role="alert">{err}</div> : null}
        {applied ? (
          <>
            <ReviewPanel stagedCount={staged.value.length} />
            {gaveUp ? (
              <div class="banner attn" role="status" data-testid="bots-waiting">
                Your bot is applied but hasn't shown up yet. conductor may still be reloading: reload this page in a moment. If it is still missing, the daemon log says why.
              </div>
            ) : (
              <p class="muted" data-testid="bots-waiting">Starting your new bot…</p>
            )}
          </>
        ) : pendingBots ? (
          <>
            {/* The next step is this card's one button: review, then apply. */}
            <EmptyState
              testid="bots-staged"
              title="Your first bot is ready to apply"
              action={
                review.value?.token
                  ? { label: 'Apply and start your bot', onClick: () => void apply(), testid: 'bots-apply', disabled: busy.value }
                  : { label: 'Review and apply', onClick: () => void doReview(), testid: 'bots-review', disabled: busy.value }
              }
            >
              {review.value?.token ? (
                <p>What changes is listed below. Apply it and conductor reloads with your new bot, in practice mode.</p>
              ) : (
                <p>Check what changes, then apply it. conductor reloads its config and your bot starts in practice mode.</p>
              )}
            </EmptyState>
            <ReviewPanel stagedCount={staged.value.length} />
          </>
        ) : (
          <EmptyState
            hero
            testid="bots-empty"
            art={<WelcomeFaces />}
            title="Meet your first bot"
            action={
              !editable
                ? undefined
                : noWorkers
                  ? { label: 'Open the config', href: '#/config', testid: 'bots-empty-runtime' }
                  : { label: 'Create your first bot', onClick: () => setNewOpen(true), testid: 'create-first-bot' }
            }
          >
            <p>A bot is a teammate you hand work to. It picks up what comes in, does the jobs you allow on its own, and asks you before anything that matters.</p>
            <p>Every new bot starts in practice mode: it only suggests, until you let it act.</p>
            {!editable ? (
              <p>This dashboard was started without a config file, so it can't add one. Add a bot under <code>bots:</code> in conductor's config instead.</p>
            ) : noWorkers ? (
              <p data-testid="bots-need-runtime">
                First, a bot needs somewhere to do its work: an agent command-line runtime on this machine. Add one under <code>runtimes:</code> in the config (for example <code>claude: {'{'} use: cli, tool: claude-code {'}'}</code>), then come back here.
              </p>
            ) : null}
          </EmptyState>
        )}
        {sheet}
      </div>
    )
  }

  return (
    <div class="col bots-screen">
      <div class="screen-head">
        <h1 class="screen-title serif">
          Bots <span class="count">{rosterCount(all.length, teams.length, soloCount)}</span>
        </h1>
        <div class="row-actions">
          {editable ? <button class="btn primary" type="button" data-testid="new-bot" onClick={() => setNewOpen(true)}>New bot</button> : null}
        </div>
      </div>
      {teams.length ? <p class="muted">Each team is a section; its header says what its bots may do, who approves and how work reaches it. Solo bots come last.</p> : null}

      {err ? <div class="banner err" role="alert">{err}</div> : null}
      {pendingBots || applied ? <ReviewPanel stagedCount={staged.value.length} /> : null}

      {all.length > 1 ? (
        <div class="filter-bar" role="search">
          <input class="field" type="search" placeholder="Find a bot" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
          <select class="field" value={mode} onChange={(e) => setMode((e.target as HTMLSelectElement).value as typeof mode)}>
            <option value="all">Any mode</option>
            <option value="live">Live</option>
            <option value="practice">Practice mode</option>
            <option value="archived">Archived</option>
          </select>
          <span class="filter-spacer" />
          {teams.length ? (
            <label class="group-by">
              Group by
              <select class="field" value={groupBy} onChange={(e) => setGroupBy((e.target as HTMLSelectElement).value as 'team' | 'none')}>
                <option value="team">Team</option>
                <option value="none">None</option>
              </select>
            </label>
          ) : null}
        </div>
      ) : null}

      {groupBy === 'none' || !teams.length ? (
        <section class="panel">
          <ul class="bot-list">
            {filtered.map((b) => <BotRow key={b.bot} b={b} showTeam={groupBy === 'none'} />)}
          </ul>
          {filtered.length ? null : <p class="muted empty-row">No bot matches.</p>}
        </section>
      ) : (
        <>
          {teams.map((t) => {
            const teamBots = filtered.filter((b) => b.team === t.team)
            return <TeamSection key={t.team} t={t} bots={teamBots} />
          })}
          <SoloSection bots={filtered.filter((b) => !b.team)} />
        </>
      )}

      {sheet}
    </div>
  )
}

// rosterCount is the header's count: teams and solo bots only once there are
// teams (without any, every bot is solo).
function rosterCount(bots: number, teams: number, solo: number): string {
  const parts = [plural(bots, 'bot')]
  if (teams) parts.push(plural(teams, 'team'), `${solo} solo`)
  return parts.join(' · ')
}

// WelcomeFaces is three sample faces from the identity picker: what a bot
// looks like here, before there is one.
function WelcomeFaces(): JSX.Element {
  return (
    <>
      <AvatarPreview spec={{ shape: 'blob', eyes: 'happy' }} color="violet" size={52} />
      <AvatarPreview spec={{ shape: 'hexagon', eyes: 'round' }} color="teal" size={68} />
      <AvatarPreview spec={{ shape: 'squircle', eyes: 'focused' }} color="ocean" size={52} />
    </>
  )
}

function TeamSection({ t, bots }: { t: TeamSummary; bots: BotSummary[] }): JSX.Element {
  const lead = t.routing?.lead
  const rules: string[] = []
  rules.push(lead ? `led by ${lead}` : 'no lead yet — what you write goes to everyone')
  if (t.operators?.length) rules.push(`approved by ${t.operators.join(', ')}`)
  if (t.routing?.rules?.length) rules.push(`${t.routing.rules.length} routing rule${t.routing.rules.length === 1 ? '' : 's'}`)
  rules.push(t.peers === 'none' ? 'bots kept apart' : 'teammates may ask each other')
  return (
    <section class="panel team-section" data-team={t.team}>
      <div class="team-head">
        <Avatar id={t.identity} size={40} kind="team" />
        <div class="team-head-main">
          <h2 class="team-name">
            <a href={`#/teams/${t.team}`}>{displayName(t.identity, t.team)}</a>
            <span class="count">{plural(t.bots?.length || 0, 'bot')}</span>
          </h2>
          <p class="team-rules muted">{t.description ? t.description + ' — ' : ''}{rules.join(' · ')}</p>
        </div>
        <div class="row-actions">
          <a class="btn ghost sm" href={`#/teams/${t.team}`}>Shape the team</a>
        </div>
      </div>
      <ul class="bot-list">
        {bots.length ? bots.map((b) => <BotRow key={b.bot} b={b} />) : <li class="muted empty-row">No bots in this team match.</li>}
      </ul>
    </section>
  )
}

function SoloSection({ bots }: { bots: BotSummary[] }): JSX.Element {
  if (!bots.length) return <></>
  return (
    <section class="panel team-section solo" data-team="(solo)">
      <div class="team-head">
        <div class="team-head-main">
          <h2 class="team-name">Solo bots <span class="count">{bots.length}</span></h2>
          <p class="team-rules muted">Not on a team — each with its own rules.</p>
        </div>
      </div>
      <ul class="bot-list">
        {bots.map((b) => <BotRow key={b.bot} b={b} />)}
      </ul>
    </section>
  )
}

function BotRow({ b, showTeam }: { b: BotSummary; showTeam?: boolean }): JSX.Element {
  const does = b.capabilities?.does || []
  const asks = b.capabilities?.asks || []
  const facts: string[] = []
  facts.push(b.disabled ? 'archived' : b.shadow ? 'practice mode' : 'live')
  if (showTeam) facts.push(b.team ? `team ${b.team}` : 'solo')
  if (does.length) facts.push(`does ${does.length} on its own`)
  if (asks.length) facts.push(`asks for ${asks.length}`)
  if (b.home_runtime) facts.push(b.home_runtime)
  const needs = b.pending_proposals > 0
  return (
    <li class="bot-row" data-bot={b.bot}>
      <a class="bot-row-link" href={`#/bots/${b.bot}`}>
        <Avatar id={b.identity} size={40} />
        <div class="bot-row-main">
          <div class="bot-row-name">
            <span class="name">{displayName(b.identity, b.bot)}</span>
            <span class="mono id">{b.bot}</span>
          </div>
          <p class="bot-row-desc">{b.description || 'A bot.'}</p>
          <p class="facts muted">{facts.join(' · ')}</p>
        </div>
      </a>
      <div class="bot-row-status">
        {needs ? (
          <a class="status-mark needs" href={`#/bots/${b.bot}/proposals`} title="Needs you">{b.pending_proposals} needs you</a>
        ) : b.live_workers > 0 ? (
          <span class="status-mark run" title="Working now">working</span>
        ) : (
          <span class="status-mark idle" title="Idle">idle</span>
        )}
      </div>
    </li>
  )
}
