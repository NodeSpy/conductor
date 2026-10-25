// A team's page (design §4.4): Overview (its bots and lead, routing decisions,
// delegations), Routing (the ordered rules), Defaults (what bots inherit) and
// Knowledge (the shared repo with bot proposals). Editing routing, defaults and
// membership are config edits through the pipeline.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, type TeamSummary, type BotSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'

const TABS = ['overview', 'routing', 'defaults', 'knowledge'] as const
type Tab = (typeof TABS)[number]

export function TeamDetail({ name, tab }: { name: string; tab?: string }): JSX.Element {
  const active: Tab = (TABS as readonly string[]).includes(tab || '') ? (tab as Tab) : 'overview'
  const [team, setTeam] = useState<TeamSummary | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [err, setErr] = useState('')

  useEffect(() => {
    botsApi.team(name).then(setTeam).catch((e) => setErr(String(e)))
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setBots(m)
    })
  }, [name])

  if (err) return <div class="col"><div class="banner err">{err}</div></div>
  if (!team) return <div class="col"><p class="muted">Loading {name}…</p></div>

  const lead = team.routing?.lead

  return (
    <div class="col team-detail" data-team={name}>
      <a class="back" href="#/bots">← Bots</a>
      <div class="bot-detail-head">
        <Avatar id={team.identity} size={56} kind="team" />
        <div class="bot-detail-head-main">
          <h1 class="screen-title serif">{displayName(team.identity, name)} <span class="mono id">team {name}</span></h1>
          <p class="muted">{team.description}</p>
          <p class="facts muted">
            {team.bots?.length || 0} bots{lead ? ` · led by ${lead}` : ' · no lead yet'}
            {team.operators?.length ? ` · approved by ${team.operators.join(', ')}` : ''}
            {` · ${team.peers === 'none' ? 'bots kept apart' : 'teammates may ask each other'}`}
          </p>
        </div>
      </div>

      <nav class="tabs" role="tablist" aria-label="Team">
        {TABS.map((t) => (
          <a class={'tab' + (active === t ? ' on' : '')} role="tab" aria-selected={active === t} href={`#/teams/${name}/${t}`} data-tab={t}>
            {t[0].toUpperCase() + t.slice(1)}
          </a>
        ))}
      </nav>

      <div class="tab-body">
        {active === 'overview' && (
          <section class="panel">
            <span class="panel-title">Bots</span>
            <ul class="bot-list">
              {(team.bots || []).map((b) => (
                <li class="bot-row" data-bot={b}>
                  <a class="bot-row-link" href={`#/bots/${b}`}>
                    <Avatar id={bots[b]?.identity} size={36} />
                    <div class="bot-row-main">
                      <div class="bot-row-name"><span class="name">{displayName(bots[b]?.identity, b)}</span>{b === lead ? <span class="chip tiny">lead</span> : null}</div>
                      <p class="bot-row-desc">{bots[b]?.description || ''}</p>
                    </div>
                  </a>
                </li>
              ))}
            </ul>
          </section>
        )}
        {active === 'routing' && (
          <section class="panel">
            <span class="panel-title">Routing</span>
            <p class="muted">First matching rule wins; then the lead triages; then the fallback.</p>
            <ol class="rule-list">
              {(team.routing?.rules || []).map((r) => (
                <li class="rule-row"><span class="mono">{r.rule}</span> → <span class="mono">{r.to}</span> <span class="facts muted">on {JSON.stringify(r.match)}</span></li>
              ))}
            </ol>
            <p class="facts muted">Lead: {lead || '—'} · Fallback: {team.routing?.fallback || '—'}</p>
          </section>
        )}
        {active === 'defaults' && (
          <section class="panel">
            <span class="panel-title">Defaults</span>
            <p class="muted">What every bot inherits from the team, and narrows (never widens). Peers: {team.peers}.</p>
          </section>
        )}
        {active === 'knowledge' && (
          <section class="panel">
            <span class="panel-title">Knowledge</span>
            {team.knowledge ? (
              <p class="facts muted">Shared repo <span class="mono">{team.knowledge.repo}</span> · promotion {team.knowledge.promotion}</p>
            ) : (
              <p class="muted">No shared knowledge repo configured.</p>
            )}
          </section>
        )}
      </div>
    </div>
  )
}
