// Bots: teams are the page (ported from botlist.js, without the config
// editing this app doesn't do — no pause/resume, no bulk changes; the daemon
// has no such endpoints, per the plan). A team is a card with its bots
// below; solo bots get their own card.
import type { JSX } from 'preact'
import { useEffect } from 'preact/hooks'
import { bots, botsLoaded, botsNotConfigured, refreshBots, teams } from '../state'
import type { BotSummary, TeamSummary } from '../api/types'
import { displayName } from '../api/types'
import { Icon, Mark } from '../components/icons'

function BotRow(props: { b: BotSummary }): JSX.Element {
  const { b } = props
  return (
    <li class={'m-row' + (b.disabled ? ' off' : '')} data-bot={b.bot}>
      <a class="m-main" href={`#/bots/${encodeURIComponent(b.bot)}`}>
        <b>{displayName(b.identity, b.bot)}</b>
        <span class="dl-why">{b.description}</span>
      </a>
      <span class="m-st">
        {b.pending_proposals ? (
          <a class="q-st need" href="#/needs">
            <Mark s="ember" />
            {b.pending_proposals} need{b.pending_proposals > 1 ? '' : 's'} you
          </a>
        ) : b.live_workers ? (
          <a class="q-st" href="#/bots">
            <Mark s="run" />
            Working
          </a>
        ) : null}
      </span>
      <a class="btn sm secondary" href={`#/chat/bot/${encodeURIComponent(b.bot)}`}>
        <Icon name="message" />
        Message
      </a>
    </li>
  )
}

export function Bots(): JSX.Element {
  useEffect(() => {
    if (!botsLoaded.value) void refreshBots()
  }, [])

  if (!botsLoaded.value) return <div class="page dl-page"><p class="dl-sub">Loading…</p></div>

  if (botsNotConfigured.value) {
    return (
      <div class="page dl-page">
        <div class="empty">
          <span class="serif">No bots are configured</span>
          <p>Add a bots: section to conductor's config and restart it.</p>
        </div>
      </div>
    )
  }

  const all = bots.value
  const teamOf = (b: BotSummary) => b.team || ''
  const solo = all.filter((b: BotSummary) => !teamOf(b))

  return (
    <div class="page dl-page bot-list m-page">
      <div class="dl-phead">
        <div>
          <h1>
            Bots <span class="h1-count">{all.length} bot{all.length === 1 ? '' : 's'} · {teams.value.length} team{teams.value.length === 1 ? '' : 's'} · {solo.length} solo</span>
          </h1>
        </div>
      </div>
      {teams.value.map((t: TeamSummary) => {
        const ms = all.filter((b: BotSummary) => teamOf(b) === t.team)
        if (!ms.length) return null
        return (
          <section class="dl-card m-team" key={t.team}>
            <header class="m-team-h">
              <div class="m-team-t">
                <div class="m-team-n">
                  <h2>{displayName(t.identity, t.team)}</h2>
                </div>
                <span class="dl-sub m-team-sum">
                  {ms.length} bot{ms.length === 1 ? '' : 's'}
                </span>
              </div>
            </header>
            <ul class="m-list">
              {ms.map((b: BotSummary) => (
                <BotRow key={b.bot} b={b} />
              ))}
            </ul>
          </section>
        )
      })}
      {solo.map((b: BotSummary) => (
        <section class="dl-card m-team m-solo" key={b.bot}>
          <ul class="m-list">
            <BotRow b={b} />
          </ul>
        </section>
      ))}
      {all.length === 0 ? (
        <div class="empty">
          <span class="serif">No bots yet</span>
        </div>
      ) : null}
    </div>
  )
}
