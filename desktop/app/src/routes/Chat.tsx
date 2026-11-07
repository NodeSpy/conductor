// Chat: a conversation list (pinned, unread counts) and a thread + composer,
// ported from the prototype's chatpage.js + chat.js. On desktop the list and
// the first (or routed) conversation show side by side (`cv-app`).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import { who } from '../api/types'
import type { ChatMessage, Conversation } from '../api/types'
import { ageText } from '../lib/time'
import { conversations, conversationsLoaded, refreshConversations } from '../state'
import { go, seg } from '../router'
import { Icon } from '../components/icons'

function ConvRow(props: { c: Conversation; on: boolean }): JSX.Element {
  const { c } = props
  return (
    <li class={'cv-li' + (c.pinned ? ' pinned' : '')}>
      <a class={'cv-c' + (props.on ? ' on' : '')} href={`#/chat/${encodeURIComponent(c.id)}`} aria-current={props.on ? 'page' : undefined}>
        <span class="cv-ct">
          <b>{c.name}</b>
          <span class="dl-sub cv-at">{c.last ? ageText(c.last.at) : ''}</span>
          <span class="cv-last">{c.last ? `${who(c.last.from).kind === 'operator' ? 'You: ' : ''}${c.last.text}` : 'No messages yet'}</span>
        </span>
        {c.unread ? (
          <span class="count hot cv-un" aria-label={`${c.unread} unread`}>
            {c.unread}
          </span>
        ) : null}
      </a>
    </li>
  )
}

function ConvList(props: { current: string }): JSX.Element {
  const all = conversations.value
  const pins = all.filter((c: Conversation) => c.pinned)
  const teams = all.filter((c: Conversation) => c.kind === 'team' && !c.pinned)
  const rest = all.filter((c: Conversation) => c.kind === 'bot' && !c.pinned)
  const group = (label: string, xs: Conversation[]) =>
    xs.length ? (
      <>
        <h2 class="cv-g">{label}</h2>
        <ul class="cv-l">
          {xs.map((c) => (
            <ConvRow key={c.id} c={c} on={c.id === props.current} />
          ))}
        </ul>
      </>
    ) : null
  return (
    <aside class="cv-list" aria-label="Conversations">
      {group('Pinned', pins)}
      {group('Teams', teams)}
      {group('Bots', rest)}
      {all.length === 0 ? <p class="dl-sub cv-none">No bot or team to chat with yet.</p> : null}
    </aside>
  )
}

function Bubble(props: { m: ChatMessage }): JSX.Element {
  const { m } = props
  const mine = who(m.from).kind === 'operator'
  return (
    <div class={'cm-row ' + (mine ? 'cm-op' : 'cm-bot')}>
      <div class="cm-meta">
        <span>{mine ? 'You' : who(m.from).name}</span>
        <span class="dl-sub">{new Date(m.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
      </div>
      <div class="cm-bubble">{m.text}</div>
      {m.attachments?.length ? (
        <div class="cm-atts">
          {m.attachments.map((a) => (
            <span class="cm-file" key={a}>
              <Icon name="attach" />
              <span>{a}</span>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function Thread(props: { conv: Conversation }): JSX.Element {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let cancelled = false
    void api.messages(props.conv.id).then((m) => {
      if (!cancelled) setMessages(m)
    })
    void api.markRead(props.conv.id, 0)
    return () => {
      cancelled = true
    }
  }, [props.conv.id])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [messages.length])

  const send = async () => {
    const t = text.trim()
    if (!t) return
    setSending(true)
    try {
      const msg = await api.sendMessage(props.conv.id, t, undefined, props.conv.kind === 'team')
      setMessages((prev) => [...prev, msg])
      setText('')
    } finally {
      setSending(false)
    }
  }

  return (
    <section class="cv-main fit">
      <header class="cv-th">
        <div class="cv-tt">
          <h2 class="cv-name">{props.conv.kind === 'team' ? `team ${props.conv.name}` : props.conv.name}</h2>
        </div>
      </header>
      <div class="panel chat cv-chat">
        <div class="cm-thread" id="msgs" role="log" aria-label={`Conversation with ${props.conv.name}`}>
          {messages.length ? (
            messages.map((m) => <Bubble key={m.seq} m={m} />)
          ) : (
            <p class="cm-empty dl-sub">No conversation yet. Ask {props.conv.name} anything; it answers here.</p>
          )}
          <div ref={bottomRef} />
        </div>
        <div class="cm-comp">
          <div class="cm-in">
            <div class="cm-field">
              <textarea
                class="cm-ta"
                rows={1}
                placeholder={`Message ${props.conv.name}`}
                value={text}
                onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    void send()
                  }
                }}
                data-testid="chat-input"
              />
            </div>
            <button class="btn primary cm-send" disabled={sending || !text.trim()} onClick={() => void send()} aria-label="Send" data-testid="chat-send">
              <Icon name="send" />
            </button>
          </div>
        </div>
      </div>
    </section>
  )
}

export function Chat(): JSX.Element {
  useEffect(() => {
    if (!conversationsLoaded.value) void refreshConversations()
  }, [])

  const routedId = seg(1) ? `${seg(1)}:${seg(2)}` : ''
  const all = conversations.value
  const current = all.find((c: Conversation) => c.id === routedId) || all.find((c: Conversation) => c.pinned) || all[0]

  useEffect(() => {
    if (!routedId && current) go(`chat/${current.id.replace(':', '/')}`)
  }, [routedId, current?.id])

  return (
    <div class="page dl-page cv-page">
      <div class="dl-phead">
        <div>
          <h1>Chat</h1>
        </div>
      </div>
      {!conversationsLoaded.value ? (
        <p class="dl-sub">Loading…</p>
      ) : (
        <div class="cv-app">
          <ConvList current={current?.id || ''} />
          {current ? <Thread conv={current} /> : <section class="cv-main fit"><p class="dl-sub">No conversations yet.</p></section>}
        </div>
      )}
    </div>
  )
}
