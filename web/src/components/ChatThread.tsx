// The one chat thread + composer (design §4.4.2, prototype js/chat.js), shared
// by Chat, a bot's Chat tab and a ticket's comments: a scroller anchored to the
// newest message, each message in the writer's identity, and a composer where
// Enter sends and Shift+Enter is a newline. In a team conversation it adds the
// To row — the lead by default, @everyone, or an @mention of one bot — with
// autocomplete.
//
// Left out of this port (no backend/UI need yet): attachments (files, images,
// links, vault/run/ticket refs — the composer's paperclip menu), "make a
// ticket from this message", and tool-call/event rows (there is no such
// message kind from this daemon yet). Plain text and the existing
// attachments array are all this API sends.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, who, displayName, type ChatMessage, type BotSummary } from '../api/bots'
import { Avatar, type Identity } from './Avatar'
import { Icon } from './Icon'
import { ago } from '../lib/format'
import { humanError } from '../api/errors'

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
      .catch((e) => setErr(humanError(e)))
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
      setErr(humanError(e))
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
    <div class="cv-chat" data-conv={convId}>
      <div class="cm-thread" id="msgs" role="log" ref={scroller} data-testid="chat-scroll">
        {messages.length === 0 ? (
          <p class="cm-empty dl-sub">No messages yet. Say something below.</p>
        ) : (
          messages.map((m) => {
            const w = who(m.from)
            const mine = w.kind === 'operator'
            const id = w.kind === 'bot' ? idOf(w.name) : undefined
            const color = id?.color || id?.avatar?.color
            const style = color ? { ['--bot-rule' as string]: `var(--palette-${color}-500, #888)` } : undefined
            const meta = (
              <div class="cm-meta">
                {mine ? <span>You</span> : <b>{label(m.from)}</b>}
                {m.mode === 'everyone' ? <span class="chip tiny">to everyone</span> : null}
                <span class="dl-sub">{ago(m.at)}</span>
              </div>
            )
            const bubble = <div class="cm-bubble">{m.text}</div>
            const atts = m.attachments?.length ? (
              <div class="cm-atts">
                <span class="cm-given"><Icon name="attach" />Data you gave it</span>
                {m.attachments.map((a) => (
                  <span class="cm-file"><Icon name="file" /><span>{a}</span></span>
                ))}
              </div>
            ) : null

            return mine ? (
              <div class="cm-row cm-op" data-from={m.from} style={style}>
                {meta}
                {bubble}
                {atts}
              </div>
            ) : (
              <div class="cm-row cm-bot" data-from={m.from} style={style}>
                <Avatar id={id} size={32} />
                <div>
                  {meta}
                  {bubble}
                  {atts}
                </div>
              </div>
            )
          })
        )}
      </div>

      <div class="cm-comp">
        {kind === 'team' && !comments ? (
          <div class="cv-to" role="group" aria-label="Who it goes to" data-testid="chat-to">
            <span class="dl-sub">To</span>
            <button class={'cv-chip' + (!everyone && to.length === 0 ? ' on' : '')} type="button" onClick={() => { setEveryone(false); setTo([]) }}>
              Lead
            </button>
            <button class={'cv-chip' + (everyone ? ' on' : '')} type="button" data-testid="to-everyone" onClick={() => { setEveryone(true); setTo([]) }}>
              Everyone on the team
            </button>
            {to.map((t) => (
              <span class="cv-chip on">@{displayName(idOf(t), t)}</span>
            ))}
            <span class="dl-sub cv-at-hint">or @name one bot</span>
          </div>
        ) : null}

        {err ? <p class="cm-err" role="alert">{err}</p> : null}

        <div class="cm-in">
          <div class="cm-field">
            <span class="cm-tw">
              <textarea
                class="cm-ta"
                id="say"
                rows={1}
                data-testid="chat-input"
                value={text}
                placeholder={placeholder || (comments ? 'Add a comment' : kind === 'team' ? 'Message the team… (@ to address one bot)' : 'Message…')}
                aria-label={comments ? 'Comment' : 'Message'}
                onInput={onInput}
                onKeyDown={onKeyDown}
              />
            </span>
          </div>
          {mentionOpen && mentionMatch.length ? (
            <div class="mx-pop" data-testid="mention-menu">
              {mentionMatch.map((x) => (
                <button type="button" class="mx-o" onClick={() => pickMention(x.name)}>
                  <span class="mx-ic"><Avatar id={x.identity} size={20} /></span>
                  <span>{displayName(x.identity, x.name)}</span>
                  {x.lead ? <span class="dl-sub">lead</span> : null}
                </button>
              ))}
            </div>
          ) : null}
          <button class="btn primary cm-send" type="button" data-testid="chat-send" aria-label={comments ? 'Post comment' : 'Send'} disabled={sending || !text.trim()} onClick={() => void doSend()}>
            <Icon name="send" />
          </button>
        </div>
      </div>
    </div>
  )
}
