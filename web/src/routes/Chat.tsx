// Chat (design §4.4.2): a main destination laid out like a messaging app. Bots
// are people, teams are group chats; the conversation on the right is the same
// thread as a bot's Chat tab. @mentions, pins and the composer live in the shared
// ChatThread. The list scales by recency + search, not a full roster.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, openChatStream, type Conversation, type BotSummary, type TeamSummary } from '../api/bots'
import { Avatar, StackedTeam, type Identity } from '../components/Avatar'
import { ChatThread } from '../components/ChatThread'
import { go } from '../router'

export function Chat({ convId }: { convId?: string }): JSX.Element {
  const [convs, setConvs] = useState<Conversation[]>([])
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [teams, setTeams] = useState<Record<string, TeamSummary>>({})
  const [q, setQ] = useState('')
  const [err, setErr] = useState('')

  const load = () => botsApi.conversations().then((r) => setConvs(r.conversations || [])).catch((e) => setErr(String(e)))
  useEffect(() => {
    load()
    botsApi.bots().then((r) => {
      const bm: Record<string, BotSummary> = {}
      for (const b of r.bots) bm[b.bot] = b
      setBots(bm)
      const tm: Record<string, TeamSummary> = {}
      for (const t of r.teams) tm[t.team] = t
      setTeams(tm)
    })
    const stop = openChatStream(() => load())
    return stop
  }, [])

  const identityOf = (c: Conversation): Identity | undefined => (c.kind === 'bot' ? bots[c.name]?.identity : teams[c.name]?.identity)
  const nameOf = (c: Conversation): string => (c.kind === 'bot' ? displayName(bots[c.name]?.identity, c.name) : displayName(teams[c.name]?.identity, c.name))

  const shown = convs.filter((c) => !q || nameOf(c).toLowerCase().includes(q.toLowerCase()) || c.name.toLowerCase().includes(q.toLowerCase()))
  const pinned = shown.filter((c) => c.pinned)
  const rest = shown.filter((c) => !c.pinned)

  const current = convId ? convs.find((c) => c.id === convId) : undefined

  function teamMembers(teamName: string) {
    const t = teams[teamName]
    const lead = t?.routing?.lead
    return (t?.bots || []).map((b) => ({ name: b, lead: b === lead, identity: bots[b]?.identity }))
  }

  return (
    <div class="col chat-screen">
      <div class="chat-layout">
        <aside class="conv-list" data-testid="conv-list">
          <div class="conv-search">
            <input class="field" type="search" placeholder="Find a bot or team" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
          </div>
          {err ? <div class="banner err">{err}</div> : null}
          {pinned.length ? <div class="conv-group-label">Pinned</div> : null}
          {pinned.map((c) => <ConvItem key={c.id} c={c} name={nameOf(c)} id={identityOf(c)} members={c.kind === 'team' ? teamMembers(c.name).map((m) => m.identity).filter(Boolean) as Identity[] : undefined} active={c.id === convId} />)}
          {rest.map((c) => <ConvItem key={c.id} c={c} name={nameOf(c)} id={identityOf(c)} members={c.kind === 'team' ? teamMembers(c.name).map((m) => m.identity).filter(Boolean) as Identity[] : undefined} active={c.id === convId} />)}
        </aside>

        <section class="conv-main">
          {current ? (
            <>
              <div class="conv-header">
                {current.kind === 'team' ? <StackedTeam team={teams[current.name]?.identity} members={teamMembers(current.name).map((m) => m.identity).filter(Boolean) as Identity[]} size={44} /> : <Avatar id={identityOf(current)} size={44} />}
                <div class="conv-header-main">
                  <h2 class="conv-title">{nameOf(current)}</h2>
                  <p class="muted">{current.kind === 'team' ? 'Group chat — the lead picks it up, or @everyone asks all' : 'Direct — delivered when the bot is idle'}</p>
                </div>
                <button class="btn ghost sm" type="button" onClick={() => void botsApi.pin(current.id).then(load)}>{current.pinned ? 'Unpin' : 'Pin'}</button>
              </div>
              <ChatThread
                convId={current.id}
                kind={current.kind}
                bots={bots}
                members={current.kind === 'team' ? teamMembers(current.name) : undefined}
              />
            </>
          ) : (
            <div class="conv-empty">
              <p class="muted">Pick a conversation. Bots are people; teams are group chats.</p>
            </div>
          )}
        </section>
      </div>
    </div>
  )

  function ConvItem({ c, name, id, members, active }: { c: Conversation; name: string; id?: Identity; members?: Identity[]; active: boolean }): JSX.Element {
    return (
      <button class={'conv-item' + (active ? ' on' : '')} type="button" data-conv={c.id} onClick={() => go(`chat/${c.id}`)}>
        {c.kind === 'team' ? <StackedTeam team={id} members={members} size={40} /> : <Avatar id={id} size={40} />}
        <div class="conv-item-main">
          <div class="conv-item-top">
            <span class="conv-item-name">{name}</span>
            {c.kind === 'team' ? <span class="chip tiny">team</span> : null}
          </div>
          <p class="conv-item-last muted">{c.last?.text || 'No messages yet'}</p>
        </div>
        {c.unread > 0 ? <span class="unread">{c.unread}</span> : null}
      </button>
    )
  }
}
