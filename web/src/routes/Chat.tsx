// Chat (prototype js/screens/chatpage.js, ported line for line): a main
// destination laid out like a messaging app. On the left, your conversations:
// each bot as a person (its face, its name, its last message, unread) and each
// team as a group (its picture); pinned first, then teams, then the bots by
// recency, with a search. On the right, the conversation (ChatThread). On a
// desktop #/chat opens the first one (a pinned one first); on a phone the list
// and the conversation are two screens, like Messages.
//
// Real: the list and unread counts (GET /api/v1/conversations, live over its
// stream), reading marks read, pins (POST/DELETE …/pin, per operator).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, openChatStream, who, type Conversation, type BotSummary, type TeamSummary } from '../api/bots'
import { ChatThread } from '../components/ChatThread'
import { Icon } from '../components/Icon'
import { Menu } from '../components/Menu'
import { go } from '../router'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'
import { usePhoneLayout } from '../lib/layout'
import { BotAv, TeamAv, BotWho, Count, agoText, nameOf, toast, ToastHost } from '../components/ChatBits'

const plain = (t: string) => String(t || '').replace(/\*\*|`/g, '')

export function Chat({ convId }: { convId?: string }): JSX.Element {
  const phone = usePhoneLayout()
  const [convs, setConvs] = useState<Conversation[] | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [teams, setTeams] = useState<Record<string, TeamSummary>>({})
  const [q, setQ] = useState('')
  const [err, setErr] = useState('')
  const [more, setMore] = useState<{ at: HTMLElement; key: string } | null>(null)

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
        /* names fall back to ids */
      })
    return () => {
      gone = true
      stop()
    }
  }, [])

  const head = (
    <div class="dl-phead">
      <div>
        <h1>Chat</h1>
      </div>
    </div>
  )

  if (convs === null) {
    return (
      <div class="cv-page">
        {head}
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load your conversations: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="dl-sub">Loading…</p>
        )}
      </div>
    )
  }

  if (convs.length === 0) {
    return (
      <div class="cv-page">
        {head}
        <EmptyState
          hero
          testid="chat-empty"
          art={<ChatGlyph />}
          title="No one to talk to yet"
          action={{ label: 'Create a bot', href: '#/bots', testid: 'chat-empty-bots' }}
        >
          <p>Chat is where you talk to your bots: each bot is a direct conversation and each team a group chat. Once you have a bot, its conversation starts here.</p>
        </EmptyState>
      </div>
    )
  }

  const titleOf = (c: Conversation) => (c.kind === 'team' ? c.name : nameOf(bots[c.name]?.identity, c.name))
  const lastOf = (c: Conversation): string => {
    const l = c.last
    if (!l) {
      if (c.kind === 'team') {
        const n = teams[c.name]?.bots.length || 0
        return `${n} bot${n === 1 ? '' : 's'}`
      }
      const tag = (bots[c.name]?.identity as { tagline?: string } | undefined)?.tagline
      return tag || 'No messages yet'
    }
    const w = who(l.from)
    const text = plain(l.text) || (l.attachments?.length ? `${l.attachments.length} file${l.attachments.length === 1 ? '' : 's'}` : '')
    if (c.kind === 'team') return `${w.kind === 'bot' ? nameOf(bots[w.name]?.identity, w.name) : 'You'}: ${text}`
    return `${w.kind === 'bot' ? '' : 'You: '}${text}`
  }
  const ql = q.toLowerCase()
  const hit = (c: Conversation) => !ql || `${titleOf(c)} ${c.kind === 'team' ? 'team' : c.name} ${lastOf(c)}`.toLowerCase().includes(ql)
  const pins = convs.filter((c) => c.pinned).sort((a, b) => (a.position || 0) - (b.position || 0))
  const pinsShown = pins.filter(hit)
  const teamsShown = convs.filter((c) => c.kind === 'team' && !c.pinned && hit(c))
  const recent = convs
    .filter((c) => c.kind === 'bot' && !c.pinned && hit(c))
    .sort((a, b) => (Date.parse(b.last?.at || '') || 0) - (Date.parse(a.last?.at || '') || 0))

  const first = pins[0] || convs[0]
  const curId = convId || (!phone && first ? first.id : '')
  const current = curId ? convs.find((c) => c.id === curId) : undefined

  async function togglePin(key: string) {
    const c = convs!.find((x) => x.id === key)
    if (!c) return
    try {
      if (c.pinned) await botsApi.unpin(key)
      else await botsApi.pin(key)
      await load()
      toast(c.pinned ? 'Unpinned' : 'Pinned', key.replace(/^team:/, 'team ').replace(/^bot:/, ''), 'ok')
    } catch (e) {
      toast('Couldn’t change the pin', humanError(e), 'fail')
    }
  }

  const row = (c: Conversation) => {
    const on = c.id === curId
    const title = titleOf(c)
    return (
      <li class={'cv-li' + (c.pinned ? ' pinned' : '')} data-key={c.id} key={c.id}>
        <a class={'cv-c' + (on ? ' on' : '')} href={`#/chat/${c.id}`} aria-current={on ? 'page' : undefined} onClick={(e) => { e.preventDefault(); go(`chat/${c.id}`) }}>
          {c.kind === 'team' ? <TeamAv name={c.name} id={teams[c.name]?.identity} size="list" /> : <BotAv name={c.name} id={bots[c.name]?.identity} size="list" />}
          <span class="cv-ct">
            <b>{title}</b>
            <span class="dl-sub cv-at">{c.last?.at ? agoText(c.last.at) : ''}</span>
            <span class="cv-last">{lastOf(c)}</span>
          </span>
          {c.unread ? <Count n={c.unread} hot label={`${c.unread} unread`} cls="cv-un" /> : null}
        </a>
        <button
          type="button"
          class="icon-btn cv-pin"
          aria-pressed={c.pinned}
          aria-label={`${c.pinned ? 'Unpin' : 'Pin'} ${title}`}
          title={c.pinned ? 'Unpin' : 'Pin to the top'}
          data-testid="cv-pin"
          onClick={() => void togglePin(c.id)}
        >
          <Icon name="pin" />
        </button>
      </li>
    )
  }
  const grp = (h: string, xs: Conversation[]) =>
    xs.length ? (
      <>
        <h2 class="cv-g">{h}</h2>
        <ul class="cv-l">{xs.map(row)}</ul>
      </>
    ) : null

  const list = (
    <aside class="cv-list" aria-label="Conversations" data-testid="conv-list">
      <label class="dl-search cv-s">
        <Icon name="search" />
        <input id="cv-q" type="search" placeholder="Find a bot or a team" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} aria-label="Find a bot or a team" />
      </label>
      {grp('Pinned', pinsShown)}
      {grp('Teams', teamsShown)}
      {grp('Bots', recent)}
      {pinsShown.length + teamsShown.length + recent.length ? null : <p class="dl-sub cv-none">No bot or team by that name.</p>}
    </aside>
  )

  const moreBtn = (key: string) => (
    <button type="button" class="icon-btn" aria-label="More for this conversation" data-testid="cv-more" onClick={(e) => setMore({ at: e.currentTarget as HTMLElement, key })}>
      <Icon name="more" />
    </button>
  )

  const thread = (c: Conversation) => {
    if (c.kind === 'team') {
      const t = teams[c.name]
      const ms = t?.bots || []
      const lead = t?.routing?.lead || ''
      const leadName = nameOf(bots[lead]?.identity, lead)
      return (
        <section class="cv-main fit" data-team={c.name}>
          <header class="cv-th">
            <TeamAv name={c.name} id={t?.identity} size="lg" />
            <div class="cv-tt">
              <h2 class="cv-name">team {c.name}</h2>
              <span class="dl-sub">{ms.length} bot{ms.length === 1 ? '' : 's'} · {lead ? `${leadName} leads and picks up what you ask` : 'no lead yet: what you write goes to everyone'}</span>
            </div>
            <a class="btn sm secondary" href={`#/teams/${c.name}`}>Team</a>
            {moreBtn(c.id)}
          </header>
          <ChatThread
            key={c.id}
            convId={c.id}
            kind="team"
            bots={bots}
            teams={teams}
            cls="cv-chat"
            empty={`Say hello to team ${c.name}. ${lead ? `${leadName} answers first.` : 'Everyone on it can answer.'}`}
            onRead={() => void load()}
          />
        </section>
      )
    }
    const b = bots[c.name]
    const tag = (b?.identity as { tagline?: string } | undefined)?.tagline || b?.description || ''
    return (
      <section class="cv-main fit">
        <header class="cv-th">
          <BotAv name={c.name} id={b?.identity} size="lg" />
          <div class="cv-tt">
            <h2 class="cv-name"><BotWho name={c.name} id={b?.identity} /></h2>
            <span class="dl-sub">{tag}{b && b.live_workers > 0 && b.open_cases > 0 ? ' · working on a ticket now' : ''}</span>
          </div>
          <a class="btn sm secondary" href={`#/bots/${c.name}`}>Bot page</a>
          {moreBtn(c.id)}
        </header>
        <ChatThread
          key={c.id}
          convId={c.id}
          kind="bot"
          bots={bots}
          teams={teams}
          cls="cv-chat"
          empty={`No conversation yet. Ask ${nameOf(b?.identity, c.name)} anything; it answers here.`}
          onRead={() => void load()}
        />
      </section>
    )
  }

  const missing = (
    <section class="cv-main fit">
      <p class="dl-sub">{/^team:/.test(curId) ? 'No team by that name.' : 'No bot by that name.'}</p>
    </section>
  )

  const menu = more ? (
    <Menu
      anchor={more.at}
      label="More for this conversation"
      onClose={() => setMore(null)}
      items={(() => {
        const c = convs.find((x) => x.id === more.key)
        const team = /^team:/.test(more.key)
        const n = who(more.key).name
        return [
          { label: c?.pinned ? 'Unpin conversation' : 'Pin conversation', icon: 'pin', onClick: () => void togglePin(more.key) },
          { label: 'Mark as unread', icon: 'message', disabled: 'Conductor keeps your place in a conversation: messages you have read stay read' },
          '-',
          { label: team ? 'Open the team' : 'Open the bot page', icon: team ? 'users' : 'bots', onClick: () => go(team ? `teams/${n}` : `bots/${n}`) },
        ]
      })()}
    />
  ) : null

  if (phone) {
    if (convId) {
      const title = current ? (current.kind === 'team' ? `team ${current.name}` : titleOf(current)) : who(convId).name
      return (
        <div class="cv-page cv-one">
          <nav class="dl-crumbs" aria-label="Where you are">
            <a href="#/chat">Chat</a>
            <span class="sep" aria-hidden="true">›</span>
            <span class="cur" aria-current="page">{title}</span>
          </nav>
          {current ? thread(current) : missing}
          {menu}
          <ToastHost />
        </div>
      )
    }
    return (
      <div class="cv-page cv-one">
        {head}
        {list}
        <ToastHost />
      </div>
    )
  }

  return (
    <div class="cv-page">
      {head}
      <div class="cv-app">
        {list}
        {current ? thread(current) : curId ? missing : <section class="cv-main fit"><p class="dl-sub">No conversations yet.</p></section>}
      </div>
      {menu}
      <ToastHost />
    </div>
  )
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
