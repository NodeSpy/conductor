// The one chat thread + composer (prototype js/chat.js, ported line for line),
// shared by Chat, a bot's Chat tab and a ticket's comments: a scroller anchored
// to the newest message, each message in the writer's identity (UI.botAv,
// UI.botWho, the clock), what you attached as "Data you gave it" (images as
// thumbnails that open larger, files as chips, links as cards), a button on
// each message that makes a ticket from it, and the composer: the paperclip's
// menu, pending chips, @mentions with the list and the tokens behind the
// field, Enter to send, Shift+Enter for a new line, drop or paste to attach.
// In a team's conversation the To row says who it goes to: the lead, everyone
// on the team, or the bots you @mention.
//
// Everything goes through the daemon's real API: a message (with its files in
// the same multipart call) to POST …/conversations/{id}/messages, reading it
// marks it read (…/messages?mark=1), a ticket comment to POST
// …/tickets/{id}/comments. The daemon checks each file against the bot's
// caps; the checks here are the documented defaults, so a bad file is refused
// in words before the round trip.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, who, type ChatMessage, type BotSummary, type Attachment, type TeamSummary } from '../api/bots'
import type { Identity } from './Avatar'
import { Icon } from './Icon'
import { Menu, type MenuItem } from './Menu'
import { FormModal } from './FormModal'
import { BotAv, BotWho, bytes, clock, agoText, nameOf, botColor } from './ChatBits'
import { humanError } from '../api/errors'
import { go } from '../router'
import { authState } from '../api/auth'

// The daemon's documented defaults (internal/config/attachments.go).
const MAX_FILES = 10
const MAX_FILE_BYTES = 10 << 20
const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,application/pdf,text/*,.log,.csv,.json,.yaml,.yml,.md,.ndjson'
const TYPE_OK = /^(image\/(png|jpeg|gif|webp)|application\/pdf|text\/.*|application\/(json|x-yaml|yaml|x-ndjson))$/
const EXT_OK = /\.(png|jpe?g|gif|webp|pdf|txt|log|csv|json|ya?ml|md|ndjson)$/i
const limitWords = `Attach up to ${MAX_FILES} files, ${bytes(MAX_FILE_BYTES)} each: images, PDF and text; paste or drop them too`
const URL_RE = /\bhttps?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)]/g

interface Pending {
  file: File
  preview?: string
}

// A row of a thread, whatever it came from (a conversation's message, a
// ticket's comment or history line).
export interface Row {
  key: string
  who: 'op' | 'bot' | 'sys'
  bot?: string
  opName?: string
  at: string
  text: string
  attachments?: string[]
  seq?: number
}

// md is the prototype's UI.md: **bold** and `code`, links as links, and
// @mentions as tokens.
export function Md({ text, mention }: { text: string; mention?: (w: string) => string | null }): JSX.Element {
  const out: ComponentChildren[] = []
  const re = /\*\*(.+?)\*\*|`([^`]+)`|(\bhttps?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)])|@([A-Za-z][\w-]*)/g
  let last = 0
  let m: RegExpExecArray | null
  let i = 0
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index))
    if (m[1]) out.push(<b key={i++}>{m[1]}</b>)
    else if (m[2]) out.push(<code class="mono" key={i++}>{m[2]}</code>)
    else if (m[3]) out.push(<a class="dl-link" href={m[3]} target="_blank" rel="noopener noreferrer" key={i++}>{m[3]}</a>)
    else if (m[4]) {
      const bc = mention ? mention(m[4]) : null
      out.push(bc === null ? m[0] : <span class="mx-tok" data-bc={bc || undefined} key={i++}>@{m[4]}</span>)
    }
    last = re.lastIndex
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}

function linksIn(text: string): { url: string; host: string; path: string }[] {
  const seen = new Set<string>()
  const out: { url: string; host: string; path: string }[] = []
  for (const u of text.match(URL_RE) || []) {
    if (seen.has(u)) continue
    seen.add(u)
    try {
      const x = new URL(u)
      out.push({ url: u, host: x.host, path: (x.pathname + x.search).replace(/^\/$/, '') })
    } catch {
      /* not a link */
    }
  }
  return out
}

// The thread's rows (UI.chatRows).
export function ChatRows({
  rows,
  bots,
  attBot,
  ticketFrom,
  mention,
}: {
  rows: Row[]
  bots: Record<string, BotSummary>
  attBot: (r: Row) => string[]
  ticketFrom?: (r: Row) => void
  mention?: (w: string) => string | null
}): JSX.Element {
  const [resolved, setResolved] = useState<Record<string, (Attachment & { bot: string }) | null>>({})
  const [big, setBig] = useState<{ a: Attachment & { bot: string }; at: string; text?: string } | null>(null)

  useEffect(() => {
    let cancelled = false
    for (const r of rows) {
      for (const id of r.attachments || []) {
        if (id in resolved) continue
        const tryBots = attBot(r)
        const tryNext = (i: number): void => {
          if (cancelled) return
          if (i >= tryBots.length) {
            setResolved((x) => (id in x ? x : { ...x, [id]: null }))
            return
          }
          botsApi
            .attachment(tryBots[i], id)
            .then((a) => !cancelled && setResolved((x) => ({ ...x, [id]: { ...a, bot: tryBots[i] } })))
            .catch(() => tryNext(i + 1))
        }
        tryNext(0)
      }
    }
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows])

  const open = (a: Attachment & { bot: string }, at: string) => {
    if (a.media_type.startsWith('text/') || /json|yaml/.test(a.media_type)) {
      fetch(botsApi.attachmentContentUrl(a.bot, a.id), { credentials: 'same-origin' })
        .then((r) => r.text())
        .then((t) => setBig({ a, at, text: t.slice(0, 4000) }))
        .catch(() => setBig({ a, at }))
    } else setBig({ a, at })
  }

  const mk = (r: Row) =>
    ticketFrom ? (
      <button type="button" class="cm-mk" aria-label="Make a ticket from this message" title="Make a ticket from this message" onClick={() => ticketFrom(r)}>
        <Icon name="ticket" />
      </button>
    ) : null

  const atts = (r: Row) => {
    const ls = r.who === 'op' ? linksIn(r.text) : []
    if (!(r.attachments || []).length && !ls.length) return null
    return (
      <div class="cm-atts">
        <span class="cm-given"><Icon name="attach" />Data you gave it</span>
        {(r.attachments || []).map((id) => {
          const a = resolved[id]
          if (a === undefined) return <span class="cm-file" key={id}><Icon name="file" /><span>…</span></span>
          if (a === null) return <span class="cm-file" key={id}><Icon name="file" /><span>A file that's no longer kept</span></span>
          if (a.media_type.startsWith('image/')) {
            return (
              <button type="button" class="cm-img" key={id} aria-label={`Open ${a.name} larger`} onClick={() => open(a, r.at)}>
                <img src={botsApi.attachmentThumbnailUrl(a.bot, id)} alt={a.name} />
              </button>
            )
          }
          return (
            <button type="button" class="cm-file" key={id} onClick={() => open(a, r.at)}>
              <Icon name="file" /><span>{a.name}</span><span class="dl-sub">{bytes(a.size)}</span>
            </button>
          )
        })}
        {ls.map((l) => (
          <a class="cm-link" href={l.url} target="_blank" rel="noopener noreferrer" key={l.url}>
            <Icon name="globe" /><span><b>{l.host}</b><span class="dl-sub">{l.path || '/'}</span></span>
          </a>
        ))}
      </div>
    )
  }

  return (
    <>
      {rows.map((r) => {
        if (r.who === 'sys') {
          return (
            <div class="cm-row cm-sys" key={r.key}>
              <span>{r.text}</span>
              <span class="dl-sub">{clock(r.at)}</span>
            </div>
          )
        }
        if (r.who === 'op') {
          return (
            <div class="cm-row cm-op" key={r.key} data-seq={r.seq}>
              <div class="cm-meta">
                {mk(r)}
                <span>{r.opName ? r.opName : 'You'}</span>
                <span class="dl-sub">{clock(r.at)}</span>
              </div>
              {r.text ? <div class="cm-bubble"><Md text={r.text} mention={mention} /></div> : null}
              {atts(r)}
            </div>
          )
        }
        const b = r.bot || ''
        const id = bots[b]?.identity
        return (
          <div class="cm-row cm-bot" key={r.key} data-bc={botColor(id)} data-seq={r.seq}>
            <BotAv name={b} id={id} size="msg" />
            <div>
              <div class="cm-meta">
                <BotWho name={b} id={id} />
                <span class="dl-sub">{clock(r.at)}</span>
                {mk(r)}
              </div>
              <div class="cm-bubble"><Md text={r.text} mention={mention} /></div>
              {atts(r)}
            </div>
          </div>
        )
      })}
      {big ? (
        <div class="scrim" id="scrim" onClick={() => setBig(null)}>
          <div class="modal wide" role="dialog" aria-modal="true" aria-label={big.a.name} onClick={(e) => e.stopPropagation()}>
            <div class="modal-head">
              <h2>{big.a.name}</h2>
              <button class="icon-btn" type="button" aria-label="Close" onClick={() => setBig(null)}><Icon name="x" /></button>
            </div>
            <div class="modal-body">
              <p class="dl-sub" style={{ margin: '0 0 10px' }}>
                <Icon name="attach" /> Data you gave {nameOf(bots[big.a.bot]?.identity, big.a.bot)} · {bytes(big.a.size)} · {big.a.media_type} · {agoText(big.at)}
              </p>
              {big.a.media_type.startsWith('image/') ? (
                <img class="cm-big" src={botsApi.attachmentContentUrl(big.a.bot, big.a.id)} alt={big.a.name} />
              ) : big.text !== undefined ? (
                <pre class="cm-out cm-prev">{big.text}</pre>
              ) : (
                <p class="dl-sub">
                  No preview for this kind of file. {nameOf(bots[big.a.bot]?.identity, big.a.bot)} reads it from its inbox.{' '}
                  <a class="dl-link" href={botsApi.attachmentContentUrl(big.a.bot, big.a.id)} target="_blank" rel="noopener noreferrer">Open the file</a>
                </p>
              )}
            </div>
          </div>
        </div>
      ) : null}
    </>
  )
}

interface Person {
  key: string // a bot name, "team:<n>", or "everyone"
  word: string
  sub: string
  kind: 'bot' | 'team' | 'all'
}

// The composer (UI.chatComposer), with its paperclip menu, pending chips and
// @mentions. onSend gets the text and files; it throws to keep them.
export function Composer({
  label,
  placeholder,
  ticket,
  people,
  bots,
  onSend,
  onMentions,
  canAttach = true,
  noFilesWhy,
  dropped,
}: {
  dropped?: File[]
  label: string
  placeholder: string
  ticket?: boolean
  people: Person[]
  bots: Record<string, BotSummary>
  onSend: (text: string, files: File[]) => Promise<void>
  onMentions?: (keys: string[]) => void
  canAttach?: boolean
  noFilesWhy?: string
}): JSX.Element {
  const [text, setText] = useState('')
  const [pending, setPending] = useState<Pending[]>([])
  const [gone, setGone] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [sending, setSending] = useState(false)
  const [menuAt, setMenuAt] = useState<HTMLElement | null>(null)
  const [form, setForm] = useState<'link' | 'obj' | null>(null)
  const [objs, setObjs] = useState<[string, string][]>([])
  const [mx, setMx] = useState<{ open: boolean; i: number; from: number; items: Person[] }>({ open: false, i: 0, from: 0, items: [] })
  const ta = useRef<HTMLTextAreaElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  const words = (): Map<string, string> => {
    const m = new Map<string, string>()
    for (const p of people) {
      m.set(p.word.toLowerCase(), p.key)
      m.set(p.key.replace(/^team:/, '').toLowerCase(), p.key)
    }
    m.set('everyone', 'everyone')
    return m
  }
  const mentionsIn = (t: string): string[] => {
    const w = words()
    const out: string[] = []
    t.replace(/@([A-Za-z][\w-]*)/g, (_a, n: string) => {
      const k = w.get(n.toLowerCase())
      if (k && !out.includes(k)) out.push(k)
      return ''
    })
    return out
  }
  useEffect(() => onMentions?.(mentionsIn(text)), [text])
  useEffect(() => {
    if (dropped?.length) addFiles(dropped)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dropped])

  const links = linksIn(text).filter((l) => !gone.includes(l.url))

  function addFiles(files: File[]) {
    if (!files.length) return
    if (!canAttach) {
      setErr(noFilesWhy || 'Files can’t be attached here')
      return
    }
    setPending((cur) => {
      const next = [...cur]
      const problems: string[] = []
      for (const f of files) {
        const name = f.name && f.name !== 'image.png' ? f.name : `Pasted image ${clock(new Date().toISOString()).replace(':', '.')}.png`
        const file = name === f.name ? f : new File([f], name, { type: f.type })
        if (next.length >= MAX_FILES) { problems.push(`${name} wasn’t attached: up to ${MAX_FILES} files per message`); continue }
        if (!TYPE_OK.test(f.type || '') && !EXT_OK.test(name)) { problems.push(`${name} wasn’t attached: only images, PDF and text files can be attached`); continue }
        if (f.size > MAX_FILE_BYTES) { problems.push(`${name} wasn’t attached: it’s ${bytes(f.size)}, and the limit is ${bytes(MAX_FILE_BYTES)} each`); continue }
        const p: Pending = { file }
        if (f.type.startsWith('image/')) p.preview = URL.createObjectURL(f)
        next.push(p)
      }
      setErr(problems.join('. '))
      return next
    })
  }

  async function send() {
    const body = text.trim()
    if (sending) return
    if (!body && !pending.length) {
      setErr(`Type a message first`)
      ta.current?.focus()
      return
    }
    setSending(true)
    setErr('')
    try {
      await onSend(body, pending.map((p) => p.file))
      for (const p of pending) if (p.preview) URL.revokeObjectURL(p.preview)
      setText('')
      setPending([])
      setGone([])
      if (ta.current) ta.current.style.height = ''
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setSending(false)
    }
  }

  // @mentions: the list opens on @ at a word's start, filtered as you type.
  function update(el: HTMLTextAreaElement) {
    const before = el.value.slice(0, el.selectionStart)
    const m = before.match(/(^|\s)@([\w-]*)$/)
    if (!m) return setMx((s) => ({ ...s, open: false }))
    const q = m[2].toLowerCase()
    const items = people
      .filter((x) => !q || x.word.toLowerCase().startsWith(q) || x.key.toLowerCase().replace(/^team:/, '').startsWith(q) || (x.kind === 'all' && 'everyone'.startsWith(q)))
      .slice(0, 8)
    setMx((s) => ({ open: true, i: Math.min(s.i, Math.max(0, items.length - 1)), from: before.length - m[2].length - 1, items }))
  }
  function pick(i: number) {
    const x = mx.items[i]
    const el = ta.current
    if (!x || !el) return
    const word = '@' + (x.kind === 'all' ? 'everyone' : x.word.replace(/\s+/g, '-')) + ' '
    const end = el.selectionStart
    const v = el.value.slice(0, mx.from) + word + el.value.slice(end)
    setText(v)
    setMx((s) => ({ ...s, open: false }))
    const at = mx.from + word.length
    setTimeout(() => {
      el.focus()
      el.setSelectionRange(at, at)
    })
  }

  function onKeyDown(e: KeyboardEvent) {
    const el = e.target as HTMLTextAreaElement
    if (mx.open) {
      const n = mx.items.length
      if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && n) {
        e.preventDefault()
        setMx((s) => ({ ...s, i: (s.i + (e.key === 'ArrowDown' ? 1 : n - 1)) % n }))
        return
      }
      if ((e.key === 'Enter' || e.key === 'Tab') && n) {
        e.preventDefault()
        pick(mx.i)
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setMx((s) => ({ ...s, open: false }))
        return
      }
    }
    if (e.key === 'Backspace' && el.selectionStart === el.selectionEnd) {
      const before = el.value.slice(0, el.selectionStart)
      const m = before.match(/@([A-Za-z][\w-]*)\s?$/)
      if (m && words().has(m[1].toLowerCase())) {
        e.preventDefault()
        const at = before.length - m[0].length
        setText(el.value.slice(0, at) + el.value.slice(el.selectionStart))
        setTimeout(() => el.setSelectionRange(at, at))
        return
      }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault()
      void send()
    }
  }

  const w = words()
  const mirror = text.split(/(@[A-Za-z][\w-]*)/g).map((part, i) => {
    const k = part.startsWith('@') ? w.get(part.slice(1).toLowerCase()) : undefined
    if (!k) return part
    const bc = k.startsWith('team:') || k === 'everyone' ? undefined : botColor(bots[k]?.identity)
    return <mark class="mx-tok" data-bc={bc} key={i}>{part}</mark>
  })

  const openObjForm = () => {
    Promise.all([botsApi.tickets({}).catch(() => ({ tickets: [] })), fetch('/api/v1/runs?limit=12', { credentials: 'same-origin' }).then((r) => (r.ok ? r.json() : { runs: [] })).catch(() => ({ runs: [] }))]).then(([t, r]) => {
      const ts = ((t.tickets || []) as { ticket: string; title: string; status: string }[]).filter((x) => x.status !== 'done' && x.status !== 'declined').slice(0, 12)
      const rs = (((r as { runs?: { id: string; workflow?: string; trigger?: string }[] }).runs) || []).slice(0, 12)
      setObjs([...ts.map((x): [string, string] => [`ticket ${x.ticket} (“${x.title}”)`, `Ticket · ${x.title}`]), ...rs.map((x): [string, string] => [`run ${x.id}`, `Run · ${x.workflow || x.trigger || x.id}`])])
      setForm('obj')
    })
  }

  const attItems: MenuItem[] = [
    { label: 'Upload a file', icon: 'file', onClick: () => { if (fileInput.current) { fileInput.current.accept = ACCEPT; fileInput.current.click() } }, disabled: canAttach ? undefined : noFilesWhy },
    { label: 'Photo or image', icon: 'image', onClick: () => { if (fileInput.current) { fileInput.current.accept = 'image/*'; fileInput.current.click() } }, disabled: canAttach ? undefined : noFilesWhy },
    { label: 'Paste a link', icon: 'globe', onClick: () => setForm('link') },
    '-',
    { label: 'From a vault item', icon: 'key', disabled: 'A message carries files and links; a vault item’s value never goes into a chat' },
    { label: 'Attach a run or ticket', icon: 'workflows', onClick: openObjForm },
  ]

  return (
    <div class="cm-comp">
      <div class="cm-pend" id="cm-pend" hidden={!pending.length && !links.length} data-testid="chat-pending">
        {pending.map((p, i) => (
          <span class="cm-p" key={p.file.name + i} data-kind={p.preview ? 'image' : 'file'}>
            {p.preview ? <img src={p.preview} alt="" /> : <Icon name="file" />}
            <span class="cm-pt"><b>{p.file.name}</b><span class="dl-sub">{bytes(p.file.size)}</span></span>
            <button type="button" class="icon-btn sm" aria-label={`Remove ${p.file.name}`} onClick={() => { if (p.preview) URL.revokeObjectURL(p.preview); setPending((c) => c.filter((_, j) => j !== i)); setErr('') }}>
              <Icon name="x" />
            </button>
          </span>
        ))}
        {links.map((l) => (
          <span class="cm-p" key={l.url} data-kind="link">
            <Icon name="globe" />
            <span class="cm-pt"><b>{l.host}</b><span class="dl-sub">link · shown as a card</span></span>
            <button type="button" class="icon-btn sm" aria-label={`Remove ${l.url}`} onClick={() => setGone((g) => [...g, l.url])}><Icon name="x" /></button>
          </span>
        ))}
      </div>
      <p class="cm-err" id="cm-err" role="alert" hidden={!err}>{err}</p>
      <div class="cm-in">
        <div class="cm-field">
          <button type="button" class="cm-attach" aria-label="Attach files or images" title={limitWords} data-testid="chat-attach" onClick={(e) => setMenuAt(menuAt ? null : (e.currentTarget as HTMLElement))}>
            <Icon name="attach" />
          </button>
          <input ref={fileInput} type="file" id="cm-file" multiple accept={ACCEPT} hidden data-testid="chat-file" onChange={(e) => { const el = e.target as HTMLInputElement; addFiles([...(el.files || [])]); el.value = '' }} />
          <span class="cm-tw">
            <span class="cm-mirror" aria-hidden="true">{mirror}{'​'}</span>
            <textarea
              ref={ta}
              class="cm-ta"
              id="say"
              rows={1}
              role="combobox"
              aria-autocomplete="list"
              aria-expanded={mx.open}
              aria-controls="mx-pop"
              data-testid="chat-input"
              value={text}
              placeholder={placeholder}
              aria-label={label}
              aria-describedby="cm-hint"
              onInput={(e) => {
                const el = e.target as HTMLTextAreaElement
                setText(el.value)
                el.style.height = 'auto'
                el.style.height = Math.min(el.scrollHeight, 180) + 'px'
                update(el)
              }}
              onKeyDown={onKeyDown}
              onPaste={(e) => {
                const fs = [...(e.clipboardData?.files || [])]
                if (fs.length) {
                  e.preventDefault()
                  addFiles(fs)
                }
              }}
            />
          </span>
        </div>
        <button class="btn primary cm-send" type="button" data-testid="chat-send" aria-label={ticket ? 'Post comment' : 'Send'} title={`${ticket ? 'Post comment' : 'Send'} (Enter; Shift+Enter for a new line)`} disabled={sending} onClick={() => void send()}>
          <Icon name="send" />
        </button>
      </div>
      <span class="sr-only" id="cm-hint">Enter sends; Shift+Enter starts a new line. {limitWords}.</span>
      {mx.open ? (
        <div class="mx-pop" id="mx-pop" role="listbox" aria-label="Mention" data-testid="mention-menu">
          {mx.items.length ? (
            mx.items.map((x, i) => (
              <button type="button" class={'mx-o' + (i === mx.i ? ' on' : '')} role="option" id={`mx-o${i}`} aria-selected={i === mx.i} key={x.key} onMouseDown={(e) => { e.preventDefault(); pick(i) }}>
                {x.kind === 'bot' ? <BotAv name={x.key} id={bots[x.key]?.identity} size="sm" /> : <span class="mx-ic"><Icon name="users" /></span>}
                <b>{x.kind === 'all' ? '@everyone' : x.word}</b>
                <span class="dl-sub">{x.sub}</span>
              </button>
            ))
          ) : (
            <p class="dl-sub mx-none">Nobody by that name.</p>
          )}
        </div>
      ) : null}
      {menuAt ? <Menu anchor={menuAt} items={attItems} onClose={() => setMenuAt(null)} label="Attach" /> : null}
      {form === 'link' ? (
        <FormModal
          caps="Attach"
          title="Paste a link"
          fields={[{ k: 'url', type: 'string', req: true, label: 'Link', ph: 'https://…', pattern: /^https?:\/\/\S+$/, patternMsg: 'A web address, starting https://' }]}
          submit="Attach"
          onSubmit={(v) => { setText((t) => `${t ? t.replace(/\s*$/, ' ') : ''}${v.url} `); setTimeout(() => ta.current?.focus()) }}
          onClose={() => setForm(null)}
        />
      ) : null}
      {form === 'obj' ? (
        <FormModal
          caps="Attach"
          title="Attach a run or ticket"
          intro="It goes in your message as a reference the bot can look up."
          fields={[{ k: 'o', type: 'enum', req: true, label: 'Run or ticket', options: objs.length ? objs : [['', 'No open ticket or recent run yet', 'none']] }]}
          submit="Attach"
          onSubmit={(v) => { if (!v.o) return 'There’s no open ticket or recent run to attach yet.'; setText((t) => `${t ? t.replace(/\s*$/, ' ') : ''}${v.o} `) }}
          onClose={() => setForm(null)}
        />
      ) : null}
    </div>
  )
}

// people a composer can mention: in a team's chat, everyone and its bots; in a
// bot's chat, every bot and team.
export function peopleFor(kind: 'bot' | 'team', name: string, bots: Record<string, BotSummary>, teams: Record<string, TeamSummary>): Person[] {
  if (kind === 'team') {
    const t = teams[name]
    const lead = t?.routing?.lead
    return [
      { key: 'everyone', word: 'everyone', sub: `everyone on team ${name}`, kind: 'all' },
      ...(t?.bots || []).map((b): Person => ({ key: b, word: nameOf(bots[b]?.identity, b), sub: b + (b === lead ? ' · lead' : ''), kind: 'bot' })),
    ]
  }
  return [
    ...Object.values(bots).map((b): Person => ({ key: b.bot, word: nameOf(b.identity, b.bot), sub: b.bot + (b.team ? ` · team ${b.team}` : ''), kind: 'bot' })),
    ...Object.values(teams).map((t): Person => ({ key: 'team:' + t.team, word: t.team, sub: `team · ${t.bots.length} bot${t.bots.length === 1 ? '' : 's'}`, kind: 'team' })),
  ]
}

// toRows turns a conversation's messages into thread rows.
export function messageRows(ms: ChatMessage[], me?: string): Row[] {
  return ms.map((m): Row => {
    const w = who(m.from)
    if (w.kind === 'bot') return { key: 'm' + m.seq, who: 'bot', bot: w.name, at: m.at, text: m.text, attachments: m.attachments, seq: m.seq }
    // another operator's message carries their name; yours says You
    const other = me && w.kind === 'operator' && w.name && w.name !== me ? w.name : undefined
    return { key: 'm' + m.seq, who: 'op', at: m.at, text: m.text, attachments: m.attachments, seq: m.seq, opName: other }
  })
}

// ChatThread is a conversation (a bot's or a team's): the thread, the team's
// To row, the composer and the drop target, in the prototype's `.panel.chat`.
export function ChatThread({
  convId,
  kind,
  bots,
  teams = {},
  members,
  placeholder,
  empty,
  me,
  onRead,
  cls = '',
  head,
}: {
  convId: string
  kind: 'bot' | 'team'
  bots: Record<string, BotSummary>
  teams?: Record<string, TeamSummary>
  members?: { name: string; lead?: boolean; identity?: Identity }[]
  placeholder?: string
  /** What an empty thread shows: a sentence, or a whole element (a bot's Chat tab draws a small hero). */
  empty?: string | JSX.Element
  me?: string
  onRead?: () => void
  cls?: string
  /** Drawn first inside the panel (a bot page's Chat tab: its Home session panel head). */
  head?: JSX.Element
}): JSX.Element {
  const name = who(convId).name
  const [messages, setMessages] = useState<ChatMessage[] | null>(null)
  const seen = useRef(-1)
  const [err, setErr] = useState('')
  const [mode, setMode] = useState<'lead' | 'all'>('lead')
  const [named, setNamed] = useState<string[]>([])
  const [drag, setDrag] = useState(false)
  const [dropped, setDropped] = useState<File[]>([])
  const scroller = useRef<HTMLDivElement>(null)
  const team = kind === 'team' ? teams[name] : undefined
  const lead = team?.routing?.lead || ''
  const memberNames = team?.bots || (members || []).map((m) => m.name)
  useEffect(() => setMode(lead ? 'lead' : 'all'), [convId, lead])

  const refresh = () =>
    botsApi
      .messages(convId, 0, true)
      .then((r) => {
        const next = r.messages || []
        const tip = next.length ? next[next.length - 1].seq : 0
        if (tip !== seen.current) {
          seen.current = tip
          setMessages(next)
          onRead?.()
        } else setMessages((old) => old || next)
        setErr('')
      })
      .catch((e) => setErr(humanError(e)))

  useEffect(() => {
    setMessages(null)
    seen.current = -1
    void refresh()
    const t = setInterval(() => void refresh(), 2500)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [convId])

  useEffect(() => {
    const el = scroller.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages])

  const people = peopleFor(kind, name, bots, teams)
  const mentionColor = (w: string): string | null => {
    const p = people.find((x) => x.word.toLowerCase() === w.toLowerCase() || x.key.replace(/^team:/, '').toLowerCase() === w.toLowerCase()) || (w.toLowerCase() === 'everyone' ? { kind: 'all', key: 'everyone' } : null)
    if (!p) return null
    return p.kind === 'bot' ? botColor(bots[p.key]?.identity) : ''
  }
  const label = kind === 'team' ? `team ${name}` : nameOf(bots[name]?.identity, name)

  async function onSend(text: string, files: File[]) {
    const payload: { text: string; to?: string[]; everyone?: boolean } = { text }
    if (kind === 'team') {
      const ments = named
      const bs = ments.filter((k) => memberNames.includes(k))
      if (ments.includes('everyone')) payload.everyone = true
      else if (bs.length) payload.to = bs
      else if (mode === 'all') payload.everyone = true
    }
    if (files.length) await botsApi.sendMessageWithFiles(convId, payload, files)
    else await botsApi.sendMessage(convId, payload)
    await refresh()
  }

  const ticketFrom = (r: Row) => go(`tickets/new?conv=${encodeURIComponent(convId)}&from=${r.seq}`)
  const rows = messages ? messageRows(messages, me || authState.value.operator) : []
  const mentionBots = named.filter((k) => memberNames.includes(k))
  const allMentioned = named.includes('everyone')
  const chipMode = allMentioned ? 'all' : mentionBots.length ? 'named' : mode

  return (
    <div
      class={'panel chat ' + cls + (drag ? ' drop' : '')}
      data-conv={convId}
      onDragOver={(e) => { if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); setDrag(true) } }}
      onDragLeave={(e) => { const n = e.relatedTarget as Node | null; if (!n || !(e.currentTarget as HTMLElement).contains(n)) setDrag(false) }}
      onDrop={(e) => { e.preventDefault(); setDrag(false); setDropped([...(e.dataTransfer?.files || [])]) }}
    >
      {head}
      <div class="cm-thread" id="msgs" role="log" aria-label={`Conversation with ${label}`} ref={scroller} data-testid="chat-scroll">
        {messages === null ? (
          err ? <p class="cm-empty dl-sub" role="alert">Couldn’t load this conversation: {err}</p> : null
        ) : rows.length ? (
          <ChatRows rows={rows} bots={bots} attBot={(r) => (kind === 'bot' ? [name] : messages.find((m) => m.seq === r.seq)?.to || memberNames)} ticketFrom={ticketFrom} mention={mentionColor} />
        ) : (
          typeof empty === 'object' && empty ? empty : <p class="cm-empty dl-sub">{empty || `No conversation yet. Ask ${label} anything; it answers here.`}</p>
        )}
      </div>
      {kind === 'team' ? (
        <div class="cv-to" role="group" aria-label="Who it goes to" data-testid="chat-to">
          <span class="dl-sub">To</span>
          {lead ? (
            <button type="button" class={'cv-chip' + (chipMode === 'lead' ? ' on' : '')} aria-pressed={chipMode === 'lead'} data-testid="to-lead" onClick={() => setMode('lead')}>
              <BotAv name={lead} id={bots[lead]?.identity} size="xs" />
              {nameOf(bots[lead]?.identity, lead)}, the lead
            </button>
          ) : (
            <button type="button" class="cv-chip" aria-disabled="true" title="This team has no lead yet: pick one on the team page" data-tip="This team has no lead yet: pick one on the team page">No lead yet</button>
          )}
          <button type="button" class={'cv-chip' + (chipMode === 'all' ? ' on' : '')} aria-pressed={chipMode === 'all'} data-testid="to-everyone" onClick={() => setMode('all')}>
            Everyone on the team
          </button>
          {chipMode === 'named' ? (
            <span class="cv-chip on cv-named">
              <BotAv name={mentionBots[0]} id={bots[mentionBots[0]]?.identity} size="xs" />
              {mentionBots.map((b) => nameOf(bots[b]?.identity, b)).join(', ')}, as you mentioned
            </span>
          ) : null}
          <span class="dl-sub cv-at-hint">or @name one bot</span>
        </div>
      ) : null}
      <Composer
        dropped={dropped}
        label={`Message ${label}`}
        placeholder={placeholder || `Message ${label}`}
        people={people}
        bots={bots}
        onSend={onSend}
        onMentions={setNamed}
      />
      <div class="cm-drop" aria-hidden="true"><Icon name="attach" />Drop to attach to your message</div>
    </div>
  )
}
