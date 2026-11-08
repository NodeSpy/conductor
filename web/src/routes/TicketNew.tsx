// New ticket (prototype js/screens/tickets.js newTicket, ported line for
// line): a page under Tickets with the title, description, attachments,
// who it goes to, priority, due date, labels and an optional spending limit;
// "Create, ready to start" (it needs someone to do it) or "Save as a draft".
// Made from a chat message (#/tickets/new?conv=…&from=<seq>) it starts from
// that message's words. Files go on the ticket as its first comment (the
// daemon keeps them with the bot that holds it); a spending limit asks for a
// passkey first (the daemon's rule: a new limit widens what a bot may spend).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, who, type Ticket } from '../api/bots'
import { Icon } from '../components/Icon'
import { go, query } from '../router'
import { humanError } from '../api/errors'
import { PRI, ALL_STATUSES, whoOptions, loadNames, assigneeWord, type Names } from '../components/TicketKit'
import { SpendFields, emptySpend, readSpend, useCards } from '../components/TicketSpend'
import { bytes, nameOf, toast, ToastHost } from '../components/ChatBits'

const MAX_FILES = 10
const MAX_FILE_BYTES = 10 << 20
const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,application/pdf,text/*,.log,.csv,.json,.yaml,.yml,.md'

export function TicketNew(): JSX.Element {
  const conv = query('conv')
  const from = query('from')
  const pre = query('bot') ? 'bot:' + query('bot') : query('team') ? 'team:' + query('team') : query('assignee')
  const [n, setN] = useState<Names>({ bots: {}, teams: {} })
  const [labelsInUse, setLabelsInUse] = useState<string[]>([])
  const [title, setTitle] = useState('')
  const [desc, setDesc] = useState('')
  const [assignee, setAssignee] = useState(pre || (conv.startsWith('bot:') ? conv : ''))
  const [priority, setPriority] = useState('normal')
  const [due, setDue] = useState('')
  const [labels, setLabels] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const [fileErr, setFileErr] = useState('')
  const [spendOpen, setSpendOpen] = useState(false)
  const [spend, setSpend] = useState(emptySpend())
  const [spendErr, setSpendErr] = useState('')
  const [err, setErr] = useState('')
  const [nudge, setNudge] = useState<'title' | 'who' | ''>('')
  const [busy, setBusy] = useState(false)
  const [fromWho, setFromWho] = useState('')
  const cards = useCards()

  useEffect(() => {
    loadNames(setN)
    botsApi
      .tickets({ status: ALL_STATUSES })
      .then((r) => setLabelsInUse([...new Set((r.tickets || []).flatMap((t) => t.labels || []))].sort()))
      .catch(() => {})
    if (conv && from) {
      botsApi
        .messages(conv, Number(from) - 1, false)
        .then((r) => {
          const m = (r.messages || []).find((x) => String(x.seq) === from)
          if (!m) return
          const text = m.text || ''
          setTitle((text.split(/(?<=[.?!])\s/)[0] || text).replace(/\*\*|`/g, '').slice(0, 90))
          setDesc(text)
          setFromWho(who(conv).name)
        })
        .catch(() => {})
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const convWord = conv ? (who(conv).kind === 'team' ? `team ${who(conv).name}` : nameOf(n.bots[who(conv).name]?.identity, who(conv).name)) : ''

  function addFiles(fs: File[]) {
    const next = [...files]
    const errs: string[] = []
    for (const f of fs) {
      const ok = /^image\/(png|jpeg|gif|webp)$/.test(f.type) || /^(application\/pdf|text\/.*|application\/(json|x-yaml|yaml))$/.test(f.type || '') || /\.(png|jpe?g|gif|webp|pdf|txt|log|csv|json|ya?ml|md)$/i.test(f.name)
      if (!ok) errs.push(`${f.name} wasn’t attached: only images, PDF and text files can be attached`)
      else if (f.size > MAX_FILE_BYTES) errs.push(`${f.name} wasn’t attached: it’s ${bytes(f.size)}, and the limit is ${bytes(MAX_FILE_BYTES)} each`)
      else if (next.length >= MAX_FILES) errs.push(`${f.name} wasn’t attached: up to ${MAX_FILES} files`)
      else next.push(f)
    }
    setFiles(next)
    setFileErr(errs.join('. '))
  }

  async function create(stage: 'ready' | 'draft') {
    const t = title.trim()
    if (!t) {
      setNudge('title')
      setErr('Give it a title')
      return
    }
    if (stage === 'ready' && !assignee) {
      setNudge('who')
      setErr('A ready ticket needs someone to do it: pick a bot or a team, or save it as a draft')
      return
    }
    if (files.length && !assignee.startsWith('bot:')) {
      setErr('Files go on a ticket a bot holds: assign it to a bot, or attach them later in a comment once a bot has it')
      return
    }
    const sl = spendOpen ? readSpend(spend, false) : undefined
    if (typeof sl === 'string') {
      setSpendErr(sl)
      setSpendOpen(true)
      return
    }
    setNudge('')
    setErr('')
    setSpendErr('')
    setBusy(true)
    try {
      const d = fromWho ? `${desc.trim()}\n\nFrom a message in ${convWord}’s chat.`.trim() : desc.trim()
      const body = {
        title: t,
        description: d || undefined,
        assignee: assignee || undefined,
        priority,
        due: due || undefined,
        labels: labels.split(',').map((x) => x.trim()).filter(Boolean),
        ready: stage === 'ready',
        ...(sl ? { spend_limit: sl } : {}),
      }
      const made = (await botsApi.createTicket(body)) as Ticket
      if (files.length) await botsApi.commentWithFiles(made.ticket, '', files)
      toast(stage === 'draft' ? 'Draft saved' : 'Ticket created', stage === 'draft' ? 'Nobody starts on it until you mark it ready' : `${assigneeWord(made, n)} may start on it`, 'ok')
      go(`tickets/${made.ticket}`)
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="tk-page tk-new">
      <nav class="dl-crumbs" aria-label="Where you are">
        <a href="#/tickets">Tickets</a>
        <span class="sep" aria-hidden="true">›</span>
        <span class="cur" aria-current="page">New ticket</span>
      </nav>
      <div class="dl-phead">
        <div>
          <h1>New ticket</h1>
          {fromWho ? <p class="lede-2">From a message in {convWord}’s chat. The ticket notes where it came from.</p> : null}
        </div>
      </div>
      <form class="dl-card tk-form" noValidate onSubmit={(e) => { e.preventDefault(); void create('ready') }}>
        <label class={'tk-l' + (nudge === 'title' ? ' nudge' : '')}>
          <span class="dl-lbl">Title</span>
          <input class="input" id="tk-title" data-testid="ticket-title" required maxLength={140} value={title} placeholder="What needs doing, in a line" onInput={(e) => setTitle((e.target as HTMLInputElement).value)} />
        </label>
        <label class="tk-l">
          <span class="dl-lbl">Description</span>
          <textarea class="textarea" id="tk-desc" rows={5} placeholder="What you know, what done looks like, links" value={desc} onInput={(e) => setDesc((e.target as HTMLTextAreaElement).value)} />
        </label>
        <div class="tk-l">
          <span class="dl-lbl">Attachments</span>
          <div class="tk-att">
            <button type="button" class="btn sm secondary" data-testid="tk-attach" onClick={() => (document.getElementById('tk-file') as HTMLInputElement)?.click()}><Icon name="attach" />Attach files or images</button>
            <input type="file" id="tk-file" multiple hidden accept={ACCEPT} data-testid="tk-file" onChange={(e) => { const el = e.target as HTMLInputElement; addFiles([...(el.files || [])]); el.value = '' }} />
            <span class="dl-sub">Up to {MAX_FILES} files, {bytes(MAX_FILE_BYTES)} each: images, PDF and text</span>
          </div>
          <div class="cm-pend" id="tk-pend" hidden={!files.length}>
            {files.map((f, i) => (
              <span class="cm-p" key={f.name + i}>
                <span><Icon name={f.type.startsWith('image/') ? 'image' : 'file'} /></span>
                <span class="cm-pt"><b>{f.name}</b><span class="dl-sub">{bytes(f.size)}</span></span>
                <button type="button" class="icon-btn sm" aria-label={`Remove ${f.name}`} onClick={() => { setFiles(files.filter((_, j) => j !== i)); setFileErr('') }}><Icon name="x" /></button>
              </span>
            ))}
          </div>
          <p class="cm-err" id="tk-err" hidden={!fileErr}>{fileErr}</p>
        </div>
        <div class="tk-row2">
          <label class={'tk-l' + (nudge === 'who' ? ' nudge' : '')}>
            <span class="dl-lbl">Assign to</span>
            <select class="select" id="tk-who" data-testid="ticket-assignee" value={assignee} onChange={(e) => setAssignee((e.target as HTMLSelectElement).value)}>
              {whoOptions(n).map(([v, l]) => <option value={v} key={v}>{l}</option>)}
            </select>
            <span class="dl-sub">A team’s lead or routing picks the bot.</span>
          </label>
          <label class="tk-l">
            <span class="dl-lbl">Priority</span>
            <select class="select" id="tk-pri" value={priority} onChange={(e) => setPriority((e.target as HTMLSelectElement).value)}>
              {PRI.map(([v, l]) => <option value={v} key={v}>{l}</option>)}
            </select>
          </label>
        </div>
        <div class="tk-row2">
          <label class="tk-l">
            <span class="dl-lbl">Due</span>
            <input class="input" type="date" id="tk-due" value={due} onInput={(e) => setDue((e.target as HTMLInputElement).value)} />
          </label>
          <label class="tk-l">
            <span class="dl-lbl">Labels</span>
            <input class="input" id="tk-labels" placeholder="bug, web" list="tk-label-list" value={labels} onInput={(e) => setLabels((e.target as HTMLInputElement).value)} />
            <datalist id="tk-label-list">{labelsInUse.map((l) => <option value={l} key={l} />)}</datalist>
          </label>
        </div>
        <details class="tk-l wl-tkb-new" id="w-nt-b" open={spendOpen} onToggle={(e) => setSpendOpen((e.target as HTMLDetailsElement).open)}>
          <summary class="dl-lbl"><Icon name="chev" />Spending limit <span class="opt">optional</span></summary>
          <div class="wl-tkb-body"><SpendFields d={spend} set={setSpend} cards={cards} err={spendErr} /></div>
        </details>
        {err ? <p class="cm-err" role="alert" data-testid="tk-new-err">{err}</p> : null}
        <div class="dl-actions">
          <button class="btn primary" type="button" disabled={busy} data-testid="create-ready" onClick={() => void create('ready')}><Icon name="check" />Create, ready to start</button>
          <button class="btn secondary" type="button" disabled={busy} data-testid="create-draft" onClick={() => void create('draft')}><Icon name="edit" />Save as a draft</button>
          <a class="btn ghost" href={conv ? `#/chat/${conv}` : '#/tickets'}>Cancel</a>
        </div>
      </form>
      <ToastHost />
    </div>
  )
}
