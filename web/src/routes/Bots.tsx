// The Bots screen (design §4.4, "teams are the page"): each team is a section
// whose header states its shared rules in words, with its bots below; solo bots
// come last. Group by Team or None. New bot opens a sheet with the identity
// picker. Managing a bot's settings and faces is on its page; this is the roster.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { botsApi, displayName, type BotSummary, type TeamSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { NewBotSheet } from './NewBotSheet'

export function Bots(): JSX.Element {
  const [bots, setBots] = useState<BotSummary[]>([])
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [err, setErr] = useState('')
  const [groupBy, setGroupBy] = useState<'team' | 'none'>('team')
  const [q, setQ] = useState('')
  const [mode, setMode] = useState<'all' | 'live' | 'practice' | 'archived'>('all')
  const [newOpen, setNewOpen] = useState(false)

  const load = () => {
    botsApi
      .bots()
      .then((r) => {
        setBots(r.bots || [])
        setTeams(r.teams || [])
      })
      .catch((e) => setErr(String(e)))
  }
  useEffect(load, [])

  const filtered = useMemo(() => {
    return bots.filter((b) => {
      if (q && !(`${b.bot} ${displayName(b.identity, b.bot)} ${b.description}`.toLowerCase().includes(q.toLowerCase()))) return false
      if (mode === 'live' && (b.shadow || b.disabled)) return false
      if (mode === 'practice' && !b.shadow) return false
      if (mode === 'archived' && !b.disabled) return false
      return true
    })
  }, [bots, q, mode])

  const soloCount = bots.filter((b) => !b.team).length

  return (
    <div class="col bots-screen">
      <div class="screen-head">
        <h1 class="screen-title serif">
          Bots <span class="count">{bots.length} bots · {teams.length} teams · {soloCount} solo</span>
        </h1>
        <div class="row-actions">
          <button class="btn primary" type="button" data-testid="new-bot" onClick={() => setNewOpen(true)}>New bot</button>
        </div>
      </div>
      <p class="muted">Each team is a section; its header says what its bots may do, who approves and how work reaches it. Solo bots come last.</p>

      {err ? <div class="banner err">{err}</div> : null}

      <div class="filter-bar" role="search">
        <input class="field" type="search" placeholder="Find a bot" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
        <select class="field" value={mode} onChange={(e) => setMode((e.target as HTMLSelectElement).value as typeof mode)}>
          <option value="all">Any mode</option>
          <option value="live">Live</option>
          <option value="practice">Practice mode</option>
          <option value="archived">Archived</option>
        </select>
        <span class="filter-spacer" />
        <label class="group-by">
          Group by
          <select class="field" value={groupBy} onChange={(e) => setGroupBy((e.target as HTMLSelectElement).value as 'team' | 'none')}>
            <option value="team">Team</option>
            <option value="none">None</option>
          </select>
        </label>
      </div>

      {groupBy === 'none' ? (
        <section class="panel">
          <ul class="bot-list">
            {filtered.map((b) => <BotRow key={b.bot} b={b} showTeam />)}
          </ul>
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

      {newOpen ? <NewBotSheet teams={teams} onClose={() => setNewOpen(false)} onStaged={() => { setNewOpen(false); load() }} /> : null}
    </div>
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
            <span class="count">{t.bots?.length || 0} bot{(t.bots?.length || 0) === 1 ? '' : 's'}</span>
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
