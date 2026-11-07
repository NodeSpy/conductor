// The one chat thread + composer (design §4.4.2, prototype js/chat.js), shared
// by Chat and a bot's Chat tab: a scroller anchored to the newest message,
// each message in the writer's identity, and a composer where Enter sends and
// Shift+Enter is a newline. In a team conversation it adds the To row — the
// lead by default, @everyone, or an @mention of one bot — with autocomplete.
//
// Attachments (design §5.3, prototype js/chat.js's attach rules): the
// paperclip opens a file picker, dropping or pasting files onto the composer
// attaches them too, each pending file is a removable chip, and files upload
// with the message in one call through the daemon's real API
// (internal/bot/attachments.go) — the daemon checks them against the bot's
// caps (count, size, type) and stores them; this is just a client-side
// pre-check against the documented defaults so a bad file is caught before
// the round trip, not the real limit. A sent image shows as a thumbnail
// (…/attachments/{id}/thumbnail) that opens the full file; anything else is a
// chip with its name and size, both resolved by fetching the attachment's
// record since a message only carries attachment ids.
//
// Left out of this port (no backend/UI need yet): links, vault/run/ticket
// refs as attachment kinds (only real files have a daemon attachment
// endpoint), "make a ticket from this message", and tool-call/event rows
// (there is no such message kind from this daemon yet). Ticket comments take
// files too (POST …/tickets/{id}/comments supports multipart), but
// TicketDetail renders its own comment list rather than this component, so
// there is no comments composer to wire today; `comments` mode here stays
// text-only.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, who, displayName, type ChatMessage, type BotSummary, type Attachment } from '../api/bots'
import { Avatar, type Identity } from './Avatar'
import { Icon } from './Icon'
import { ago } from '../lib/format'
import { humanError } from '../api/errors'

// The daemon's documented defaults (internal/config/attachments.go): a
// per-bot config can lower or raise these, but the dashboard has no read
// model for a bot's actual limits today, so this is a client-side pre-check
// against the common case, not the real enforcement — the daemon re-checks
// every file against the bot's real caps regardless.
const MAX_FILES = 10
const MAX_FILE_BYTES = 10 << 20
const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,application/pdf,text/plain,text/markdown,text/csv,application/json,application/yaml,.yml,.md'

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${Math.round(n / 1024)} KB`
  return `${(n / 1048576).toFixed(n < 10485760 ? 1 : 0)} MB`
}

interface Pending {
  file: File
  preview?: string // an object URL, for an image chip's thumbnail
}

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
  const [pending, setPending] = useState<Pending[]>([])
  const [attMenu, setAttMenu] = useState(false)
  const [drag, setDrag] = useState(false)
  const [resolved, setResolved] = useState<Record<string, Attachment | null>>({})
  const scroller = useRef<HTMLDivElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const canAttach = !comments

  // botFor resolves an attachment id's owning bot: the direct conversation's
  // own bot, or — in a team conversation, where the same file is stored once
  // per target — whichever of a message's `to` bots actually has that id.
  function botFor(m: ChatMessage): string[] {
    if (kind === 'bot') return [who(convId).name]
    return m.to && m.to.length ? m.to : []
  }

  useEffect(() => {
    let cancelled = false
    for (const m of messages) {
      for (const id of m.attachments || []) {
        if (id in resolved) continue
        const tryBots = botFor(m)
        const tryNext = (i: number): void => {
          if (cancelled) return
          if (i >= tryBots.length) {
            setResolved((r) => (id in r ? r : { ...r, [id]: null }))
            return
          }
          botsApi
            .attachment(tryBots[i], id)
            .then((a) => !cancelled && setResolved((r) => ({ ...r, [id]: a })))
            .catch(() => tryNext(i + 1))
        }
        tryNext(0)
      }
    }
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [messages])

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
    if ((!body && !pending.length) || sending) return
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
      } else if (pending.length) {
        await botsApi.sendMessageWithFiles(convId, payload, pending.map((p) => p.file))
      } else {
        await botsApi.sendMessage(convId, payload)
      }
      for (const p of pending) if (p.preview) URL.revokeObjectURL(p.preview)
      setText('')
      setTo([])
      setEveryone(false)
      setMentionOpen(false)
      setPending([])
      refresh()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setSending(false)
    }
  }

  // addFiles applies the client-side pre-check (count, size) before queuing a
  // file as pending; the daemon's own check, on send, is what really decides.
  function addFiles(files: File[]) {
    if (!canAttach || !files.length) return
    setErr('')
    setPending((cur) => {
      const next = [...cur]
      const problems: string[] = []
      for (const f of files) {
        if (next.length >= MAX_FILES) {
          problems.push(`${f.name}: up to ${MAX_FILES} files per message`)
          continue
        }
        if (f.size > MAX_FILE_BYTES) {
          problems.push(`${f.name}: ${bytes(f.size)}, over the limit of ${bytes(MAX_FILE_BYTES)}`)
          continue
        }
        const p: Pending = { file: f }
        if (f.type.startsWith('image/')) p.preview = URL.createObjectURL(f)
        next.push(p)
      }
      if (problems.length) setErr(problems.join('; '))
      return next
    })
  }

  function removePending(i: number) {
    setPending((cur) => {
      const p = cur[i]
      if (p?.preview) URL.revokeObjectURL(p.preview)
      return cur.filter((_, j) => j !== i)
    })
  }

  function onFilePicked(e: Event) {
    const input = e.target as HTMLInputElement
    addFiles([...(input.files || [])])
    input.value = ''
  }

  function onPaste(e: ClipboardEvent) {
    const files = [...(e.clipboardData?.files || [])]
    if (files.length) {
      e.preventDefault()
      addFiles(files)
    }
  }

  function onDragOver(e: DragEvent) {
    if (!canAttach || !e.dataTransfer?.types.includes('Files')) return
    e.preventDefault()
    setDrag(true)
  }

  function onDragLeave(e: DragEvent) {
    const next = e.relatedTarget as Node | null
    if (!next || !(e.currentTarget as HTMLElement).contains(next)) setDrag(false)
  }

  function onDrop(e: DragEvent) {
    if (!canAttach) return
    e.preventDefault()
    setDrag(false)
    addFiles([...(e.dataTransfer?.files || [])])
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
    <div class={'cv-chat chat' + (drag ? ' drop' : '')} data-conv={convId} onDragOver={onDragOver} onDragLeave={onDragLeave} onDrop={onDrop}>
      <div class="cm-drop" aria-hidden={!drag}><Icon name="attach" /><span>Drop to attach</span></div>
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
            const bubble = m.text ? <div class="cm-bubble">{m.text}</div> : null
            const atts = m.attachments?.length ? (
              <div class="cm-atts">
                <span class="cm-given"><Icon name="attach" />Data you gave it</span>
                {m.attachments.map((id) => {
                  const a = resolved[id]
                  const bot = botFor(m)[0]
                  if (a === undefined) return <span class="cm-file" key={id}><Icon name="file" /><span>…</span></span>
                  if (a === null) return <span class="cm-file" key={id}><Icon name="file" /><span>{id}</span></span>
                  const url = bot ? botsApi.attachmentContentUrl(bot, id) : undefined
                  if (a.media_type.startsWith('image/') && bot) {
                    return (
                      <a class="cm-img" key={id} href={url} target="_blank" rel="noopener noreferrer" aria-label={`Open ${a.name} larger`}>
                        <img src={botsApi.attachmentThumbnailUrl(bot, id)} alt={a.name} />
                      </a>
                    )
                  }
                  return (
                    <a class="cm-file" key={id} href={url} target="_blank" rel="noopener noreferrer">
                      <Icon name="file" /><span>{a.name}</span><span class="dl-sub">{bytes(a.size)}</span>
                    </a>
                  )
                })}
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

        {pending.length ? (
          <div class="cm-pend" data-testid="chat-pending">
            {pending.map((p, i) => (
              <span class="cm-p" key={p.file.name + i}>
                {p.preview ? <img src={p.preview} alt="" /> : <Icon name="file" />}
                <span class="cm-pt">
                  <b>{p.file.name}</b>
                  <span class="dl-sub">{bytes(p.file.size)}</span>
                </span>
                <button type="button" class="icon-btn sm" aria-label={`Remove ${p.file.name}`} onClick={() => removePending(i)}>
                  <Icon name="x" />
                </button>
              </span>
            ))}
          </div>
        ) : null}

        <div class="cm-in">
          <div class="cm-field">
            {canAttach ? (
              <>
                <button
                  type="button"
                  class="cm-attach"
                  aria-label="Attach files or images"
                  title={`Attach up to ${MAX_FILES} files, ${bytes(MAX_FILE_BYTES)} each: images, PDF and text; paste or drop them too`}
                  onClick={() => setAttMenu((v) => !v)}
                >
                  <Icon name="attach" />
                </button>
                {attMenu ? (
                  <div class="mx-pop" data-testid="attach-menu">
                    <button type="button" class="mx-o" onClick={() => { setAttMenu(false); if (fileInput.current) { fileInput.current.accept = ACCEPT; fileInput.current.click() } }}>
                      <Icon name="file" /><span>Upload a file</span>
                    </button>
                    <button type="button" class="mx-o" onClick={() => { setAttMenu(false); if (fileInput.current) { fileInput.current.accept = 'image/*'; fileInput.current.click() } }}>
                      <Icon name="image" /><span>Photo or image</span>
                    </button>
                  </div>
                ) : null}
                <input ref={fileInput} type="file" multiple hidden onChange={onFilePicked} />
              </>
            ) : null}
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
                onPaste={canAttach ? onPaste : undefined}
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
          <button class="btn primary cm-send" type="button" data-testid="chat-send" aria-label={comments ? 'Post comment' : 'Send'} disabled={sending || (!text.trim() && !pending.length)} onClick={() => void doSend()}>
            <Icon name="send" />
          </button>
        </div>
      </div>
    </div>
  )
}
