// The one chat thread + composer (design §4.4.2), shared by Chat, a bot's Chat
// tab and a ticket's comments: a scroller anchored to the newest message, each
// message in the writer's identity, and a composer where Enter sends and
// Shift+Enter is a newline. In a team conversation it adds the To row — the
// lead by default, @everyone, or an @mention of one bot — with autocomplete.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, who, displayName, type ChatMessage, type BotSummary } from '../api/bots'
import { Avatar, type Identity } from './Avatar'
import { ago } from '../lib/format'

interface Mentionable {
  name: string
  lead?: boolean
  identity?: Identity
}

export function ChatThread({
  convId,
  kind,
  bots,
  members,
  placeholder,
  comments,
}: {
  convId: string
  kind: 'bot' | 'team'
  bots: Record<string, BotSummary>
  members?: Mentionable[]
  placeholder?: string
  comments?: boolean // a ticket's comments use the same component with a plainer composer
}): JSX.Element {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [text, setText] = useState('')
  const [everyone, setEveryone] = useState(false)
  const [to, setTo] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [sending, setSending] = useState(false)
  const [mentionOpen, setMentionOpen] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)

  const refresh = () => {
    botsApi
      .messages(convId, 0, true)
      .then((r) => setMessages(r.messages || []))
      .catch((e) => setErr(String(e)))
  }

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 2500)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [convId])

  useEffect(() => {
    const el = scroller.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages])

  const idOf = (botName: string): Identity | undefined => bots[botName]?.identity
  const label = (from: string): string => {
    const w = who(from)
    if (w.kind === 'bot') return displayName(idOf(w.name), w.name)
    return 'You'
  }

  async function doSend() {
    const body = text.trim()
    if (!body || sending) return
    setSending(true)
    setErr('')
    try {
      const payload: { text: string; to?: string[]; everyone?: boolean } = { text: body }
      if (kind === 'team') {
        if (everyone) payload.everyone = true
        else if (to.length) payload.to = to
      }
      if (comments) {
        await botsApi.comment(convId, body)
      } else {
        await botsApi.sendMessage(convId, payload)
      }
      setText('')
      setTo([])
      setEveryone(false)
      setMentionOpen(false)
      refresh()
    } catch (e) {
      setErr(String(e))
    } finally {
      setSending(false)
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void doSend()
    }
  }

  function onInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value
    setText(v)
    if (kind === 'team') {
      const m = /@(\w*)$/.exec(v)
      setMentionOpen(!!m)
    }
  }

  function pickMention(name: string) {
    setText((t) => t.replace(/@\w*$/, `@${name} `))
    setTo([name])
    setEveryone(false)
    setMentionOpen(false)
  }

  const mentionMatch = (() => {
    const m = /@(\w*)$/.exec(text)
    const q = (m?.[1] || '').toLowerCase()
    return (members || []).filter((x) => x.name.toLowerCase().includes(q) || displayName(x.identity, x.name).toLowerCase().includes(q))
  })()

  return (
    <div class="chat-thread" data-conv={convId}>
      <div class="chat-scroll" ref={scroller} data-testid="chat-scroll">
        {messages.length === 0 ? (
          <p class="muted chat-empty">No messages yet. Say something below.</p>
        ) : (
          messages.map((m) => {
            const w = who(m.from)
            const mine = w.kind === 'operator'
            const id = w.kind === 'bot' ? idOf(w.name) : undefined
            const color = id?.color || id?.avatar?.color
            return (
              <div class={'chat-msg' + (mine ? ' mine' : ' theirs')} data-from={m.from} style={color ? { ['--bot-rule' as string]: `var(--palette-${color}-500, #888)` } : undefined}>
                {!mine ? <Avatar id={id} size={36} /> : null}
                <div class="chat-bubble">
                  <div class="chat-meta">
                    <span class="chat-who">{label(m.from)}</span>
                    {m.mode === 'everyone' ? <span class="chip tiny">to everyone</span> : null}
                    <span class="chat-at">{ago(m.at)}</span>
                  </div>
                  <div class="chat-text">{m.text}</div>
                  {m.attachments?.length ? (
                    <div class="chat-attach">
                      {m.attachments.map((a) => (
                        <span class="file-chip">{a}</span>
                      ))}
                    </div>
                  ) : null}
                </div>
              </div>
            )
          })
        )}
      </div>

      {err ? <div class="banner err">{err}</div> : null}

      <div class="chat-composer">
        {kind === 'team' && !comments ? (
          <div class="chat-to" data-testid="chat-to">
            <span class="chat-to-label">To</span>
            <button class={'chip toggle' + (!everyone && to.length === 0 ? ' on' : '')} type="button" onClick={() => { setEveryone(false); setTo([]) }}>
              Lead
            </button>
            <button class={'chip toggle' + (everyone ? ' on' : '')} type="button" data-testid="to-everyone" onClick={() => { setEveryone(true); setTo([]) }}>
              @everyone
            </button>
            {to.map((t) => (
              <span class="chip token">@{displayName(idOf(t), t)}</span>
            ))}
          </div>
        ) : null}

        <div class="chat-input-wrap">
          <textarea
            class="chat-input"
            data-testid="chat-input"
            value={text}
            placeholder={placeholder || (kind === 'team' ? 'Message the team… (@ to address one bot)' : 'Message…')}
            onInput={onInput}
            onKeyDown={onKeyDown}
            rows={2}
          />
          {mentionOpen && mentionMatch.length ? (
            <ul class="mention-menu" data-testid="mention-menu">
              {mentionMatch.map((x) => (
                <li>
                  <button type="button" class="mention-opt" onClick={() => pickMention(x.name)}>
                    <Avatar id={x.identity} size={22} />
                    <span>{displayName(x.identity, x.name)}</span>
                    {x.lead ? <span class="chip tiny">lead</span> : null}
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
          <button class="btn primary chat-send" type="button" data-testid="chat-send" disabled={sending || !text.trim()} onClick={() => void doSend()}>
            Send
          </button>
        </div>
        <p class="chat-hint muted">Enter sends · Shift+Enter for a new line{kind === 'bot' ? ' · delivered when the bot is idle' : ''}</p>
      </div>
    </div>
  )
}
