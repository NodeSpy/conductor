// A team's page (design §4.4, CONFORMANCE "Teams take the bot page's header"):
// Overview (its bots and lead, routing decisions, delegations), Routing (the
// ordered rules), Defaults (what bots inherit) and Knowledge (the shared repo
// with bot proposals). Editing routing, defaults and membership are config
// edits through the pipeline.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, type TeamSummary, type BotSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { humanError } from '../api/errors'
import { plural } from '../lib/format'

const TABS = ['overview', 'routing', 'defaults', 'knowledge'] as const
type Tab = (typeof TABS)[number]

export function TeamDetail({ name, tab }: { name: string; tab?: string }): JSX.Element {
  const active: Tab = (TABS as readonly string[]).includes(tab || '') ? (tab as Tab) : 'overview'
  const [team, setTeam] = useState<TeamSummary | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [err, setErr] = useState('')

  useEffect(() => {
    botsApi.team(name).then(setTeam).catch((e) => setErr(humanError(e)))
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setBots(m)
    })
  }, [name])

  if (err) return <div class="col"><a class="back" href="#/bots">← Bots</a><div class="banner err" role="alert">{err}</div></div>
  if (!team) return <div class="col"><p class="muted">Loading {name}…</p></div>

  const lead = team.routing?.lead
  const facts: string[] = [plural(team.bots?.length || 0, 'bot')]
  facts.push(lead ? `led by ${lead}` : 'no lead yet')
  if (team.operators?.length) facts.push(`approved by ${team.operators.join(', ')}`)
  facts.push(team.peers === 'none' ? 'bots kept apart' : 'teammates may ask each other')

  return (
    <div class="col" data-team={name}>
      <a class="back" href="#/bots">← Bots</a>
      <div class="dl-bothead">
        <Avatar id={team.identity} size={56} kind="team" />
        <div>
          <h1>{displayName(team.identity, name)} <span class="mono id">team {name}</span></h1>
          {team.description ? <p class="dl-lead">{team.description}</p> : null}
          <span class="fx">{facts.map((f) => <span class="fx-i">{f}</span>)}</span>
        </div>
      </div>

      <nav class="tabs" role="tablist" aria-label="Team">
        {TABS.map((t) => (
          <a class={'tab' + (active === t ? ' on' : '')} role="tab" aria-selected={active === t} href={`#/teams/${name}/${t}`} data-tab={t}>
            {t[0].toUpperCase() + t.slice(1)}
          </a>
        ))}
      </nav>

      <div class="dl-tab-body">
        {active === 'overview' && (
          <section class="dl-card m-team">
            <ul class="m-list m-flat">
              {(team.bots || []).length ? (team.bots || []).map((b) => (
                <li class="m-row" data-bot={b}>
                  <span class="m-sel" />
                  <Avatar id={bots[b]?.identity} size={32} />
                  <a class="m-main" href={`#/bots/${b}`}>
                    <b>{displayName(bots[b]?.identity, b)}{b === lead ? <span class="bav-id mono">lead</span> : null}</b>
                    <span class="dl-why">{bots[b]?.description || ''}</span>
                  </a>
                  <span class="m-st" />
                </li>
              )) : <li class="dl-empty-row">No bots on this team yet.</li>}
            </ul>
          </section>
        )}
        {active === 'routing' && (
          <section class="dl-card">
            <p class="dl-sub">First matching rule wins; then the lead triages; then the fallback.</p>
            <ol class="rule-list">
              {(team.routing?.rules || []).map((r) => (
                <li class="rule-row"><span class="mono">{r.rule}</span> → <span class="mono">{r.to}</span> <span class="fx-i muted">on {JSON.stringify(r.match)}</span></li>
              ))}
            </ol>
            <p class="dl-sub">Lead: {lead || '—'} · Fallback: {team.routing?.fallback || '—'}</p>
          </section>
        )}
        {active === 'defaults' && (
          <section class="dl-card">
            <p class="dl-sub">What every bot inherits from the team, and narrows (never widens). Peers: {team.peers}.</p>
          </section>
        )}
        {active === 'knowledge' && (
          <section class="dl-card">
            {team.knowledge ? (
              <p class="dl-sub">Shared repo <span class="mono">{team.knowledge.repo}</span> · promotion {team.knowledge.promotion}</p>
            ) : (
              <p class="dl-sub">No shared knowledge repo configured.</p>
            )}
          </section>
        )}
      </div>
    </div>
  )
}
