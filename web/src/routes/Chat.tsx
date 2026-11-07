// Chat (design §4.4.2): a main destination laid out like a messaging app. Bots
// are people, teams are group chats; the conversation on the right is the same
// thread as a bot's Chat tab. @mentions, pins and the composer live in the shared
// ChatThread. The list scales by recency + search, not a full roster.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, openChatStream, type Conversation, type BotSummary, type TeamSummary } from '../api/bots'
import { Avatar, StackedTeam, type Identity } from '../components/Avatar'
import { ChatThread } from '../components/ChatThread'
import { Icon } from '../components/Icon'
import { go } from '../router'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'

export function Chat({ convId }: { convId?: string }): JSX.Element {
  const [convs, setConvs] = useState<Conversation[] | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [teams, setTeams] = useState<Record<string, TeamSummary>>({})
  const [q, setQ] = useState('')
  const [err, setErr] = useState('')

  const load = () =>
    botsApi
      .conversations()
      .then((r) => {
        setConvs(r.conversations || [])
        setErr('')
        return r.configured !== false
      })
      .catch((e) => {
        setErr(humanError(e))
        return false
      })
  useEffect(() => {
    let stop = () => {}
    let gone = false
    // The live stream only exists once there are bots to talk to.
    void load().then((live) => {
      if (live && !gone) stop = openChatStream(() => void load())
    })
    botsApi
      .bots()
      .then((r) => {
        const bm: Record<string, BotSummary> = {}
        for (const b of r.bots) bm[b.bot] = b
        setBots(bm)
        const tm: Record<string, TeamSummary> = {}
        for (const t of r.teams) tm[t.team] = t
        setTeams(tm)
      })
      .catch(() => {
        /* names fall back to ids; the list's own error is shown */
      })
    return () => {
      gone = true
      stop()
    }
  }, [])

  const identityOf = (c: Conversation): Identity | undefined => (c.kind === 'bot' ? bots[c.name]?.identity : teams[c.name]?.identity)
  const nameOf = (c: Conversation): string => (c.kind === 'bot' ? displayName(bots[c.name]?.identity, c.name) : displayName(teams[c.name]?.identity, c.name))

  if (convs === null) {
    return (
      <div class="cv-page">
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load your conversations: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="muted">Loading…</p>
        )}
      </div>
    )
  }

  if (convs.length === 0) {
    const anyBot = Object.keys(bots).length > 0
    return (
      <div class="cv-page">
        <h1>Chat</h1>
        <EmptyState
          testid="chat-empty"
          art={<ChatGlyph />}
          title={anyBot ? 'No conversations yet' : 'No one to talk to yet'}
          action={{ label: anyBot ? 'Go to your bots' : 'Create a bot', href: '#/bots', testid: 'chat-empty-bots' }}
        >
          <p>Chat is where you talk to your bots: each bot is a direct conversation and each team a group chat, with @mentions to bring others in.</p>
          {anyBot ? <p>Open a bot to start talking to it.</p> : <p>Once you have a bot, its conversation starts here.</p>}
        </EmptyState>
      </div>
    )
  }

  const shown = convs.filter((c) => !q || nameOf(c).toLowerCase().includes(q.toLowerCase()) || c.name.toLowerCase().includes(q.toLowerCase()))
  const pinned = shown.filter((c) => c.pinned)
  const teamsShown = shown.filter((c) => c.kind === 'team' && !c.pinned)
  const recent = shown.filter((c) => c.kind === 'bot' && !c.pinned)

  const current = convId ? convs.find((c) => c.id === convId) : undefined

  function teamMembers(teamName: string) {
    const t = teams[teamName]
    const lead = t?.routing?.lead
    return (t?.bots || []).map((b) => ({ name: b, lead: b === lead, identity: bots[b]?.identity }))
  }

  const groups: { label: string; items: Conversation[] }[] = [
    { label: 'Pinned', items: pinned },
    { label: 'Teams', items: teamsShown },
    { label: 'Bots', items: recent },
  ].filter((g) => g.items.length)

  return (
    <div class="cv-page">
      <div class="cv-app">
        <aside class="cv-list" aria-label="Conversations" data-testid="conv-list">
          <label class="dl-search cv-s">
            <Icon name="search" />
            <input type="search" placeholder="Find a bot or a team" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} aria-label="Find a bot or a team" />
          </label>
          {err ? <div class="banner err">{err}</div> : null}
          {groups.length === 0 ? <p class="dl-sub cv-none">No bot or team by that name.</p> : null}
          {groups.map((g) => (
            <>
              <h2 class="cv-g">{g.label}</h2>
              <ul class="cv-l">
                {g.items.map((c) => (
                  <ConvItem key={c.id} c={c} name={nameOf(c)} id={identityOf(c)} members={c.kind === 'team' ? teamMembers(c.name).map((m) => m.identity).filter(Boolean) as Identity[] : undefined} active={c.id === convId} />
                ))}
              </ul>
            </>
          ))}
        </aside>

        <section class="cv-main">
          {current ? (
            <>
              <header class="cv-th">
                {current.kind === 'team' ? <StackedTeam team={teams[current.name]?.identity} members={teamMembers(current.name).map((m) => m.identity).filter(Boolean) as Identity[]} size={44} /> : <Avatar id={identityOf(current)} size={44} />}
                <div class="cv-tt">
                  <h2 class="cv-name">{nameOf(current)}</h2>
                  <span class="dl-sub">{current.kind === 'team' ? 'Group chat — the lead picks it up, or @everyone asks all' : 'Direct — delivered when the bot is idle'}</span>
                </div>
                <button class="btn sm secondary" type="button" onClick={() => void botsApi.pin(current.id).then(load)}>{current.pinned ? 'Unpin' : 'Pin'}</button>
              </header>
              <ChatThread
                convId={current.id}
                kind={current.kind}
                bots={bots}
                members={current.kind === 'team' ? teamMembers(current.name) : undefined}
              />
            </>
          ) : (
            <div class="cv-main fit">
              <p class="dl-sub">Pick a conversation. Bots are people; teams are group chats.</p>
            </div>
          )}
        </section>
      </div>
    </div>
  )

  function ConvItem({ c, name, id, members, active }: { c: Conversation; name: string; id?: Identity; members?: Identity[]; active: boolean }): JSX.Element {
    return (
      <li class={'cv-li' + (c.pinned ? ' pinned' : '')} data-key={c.id}>
        <a class={'cv-c' + (active ? ' on' : '')} href={`#/chat/${c.id}`} aria-current={active ? 'page' : undefined} onClick={(e) => { e.preventDefault(); go(`chat/${c.id}`) }}>
          {c.kind === 'team' ? <StackedTeam team={id} members={members} size={40} /> : <Avatar id={id} size={40} />}
          <span class="cv-ct">
            <b>{name}</b>
            <span class="dl-sub cv-at">{c.last?.at ? '' : ''}</span>
            <span class="cv-last">{c.last?.text || 'No messages yet'}</span>
          </span>
          {c.unread > 0 ? <span class="count hot cv-un">{c.unread}</span> : null}
        </a>
      </li>
    )
  }
}

// ChatGlyph is two speech bubbles: the empty state's picture.
function ChatGlyph(): JSX.Element {
  return (
    <svg width="64" height="52" viewBox="0 0 64 52" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round">
      <path d="M6 6h32a4 4 0 0 1 4 4v16a4 4 0 0 1-4 4H20l-8 7v-7H6a4 4 0 0 1-4-4V10a4 4 0 0 1 4-4z" />
      <path d="M48 18h10a4 4 0 0 1 4 4v14a4 4 0 0 1-4 4h-4v7l-8-7H32a4 4 0 0 1-4-4v-2" opacity="0.55" />
    </svg>
  )
}
