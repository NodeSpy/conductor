// Tickets (design §4.4.1): the Board by default (one column per status), or a
// flat List. A ticket is one piece of work for a team or a bot; marking it ready
// (an operator act, with an assignee) is what lets work start. New ticket is a
// small form; the same rows power a bot's and a team's Tickets tabs.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { botsApi, displayName, who, openTicketStream, type Ticket, type BotSummary, type TeamSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { ago } from '../lib/format'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'

export const STATUSES = ['draft', 'ready', 'in_progress', 'waiting', 'done'] as const
export const STATUS_LABEL: Record<string, string> = {
  draft: 'Draft',
  ready: 'Ready',
  in_progress: 'In progress',
  waiting: 'Waiting on you',
  done: 'Done',
  declined: 'Declined',
}

function assigneeId(a: string | undefined, bots: Record<string, BotSummary>): { label: string; identity?: BotSummary['identity'] } {
  if (!a) return { label: 'nobody yet' }
  const w = who(a)
  if (w.kind === 'bot') return { label: displayName(bots[w.name]?.identity, w.name), identity: bots[w.name]?.identity }
  return { label: `team ${w.name}` }
}

export function Tickets(): JSX.Element {
  const params = new URLSearchParams(location.hash.split('?')[1] || '')
  const [tickets, setTickets] = useState<Ticket[] | null>(null)
  const [configured, setConfigured] = useState(true)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [view, setView] = useState<'board' | 'list'>((localStorage.getItem('tickets.view') as 'board' | 'list') || 'board')
  const [err, setErr] = useState('')
  const [newOpen, setNewOpen] = useState(params.get('new') === '1')
  const [fAssignee, setFAssignee] = useState(params.get('assignee') || '')
  const [fPriority, setFPriority] = useState('')

  const load = () =>
    botsApi
      .tickets({ assignee: fAssignee, priority: fPriority })
      .then((r) => {
        setTickets(r.tickets || [])
        setConfigured(r.configured !== false)
        setErr('')
        return r.configured !== false
      })
      .catch((e) => {
        setErr(humanError(e))
        return false
      })
  useEffect(() => void load(), [fAssignee, fPriority])
  useEffect(() => {
    let stop = () => {}
    let gone = false
    // The live stream only exists once there are bots to give tickets to.
    void load().then((live) => {
      if (live && !gone) stop = openTicketStream(() => void load())
    })
    botsApi
      .bots()
      .then((r) => {
        const m: Record<string, BotSummary> = {}
        for (const b of r.bots) m[b.bot] = b
        setBots(m)
        setTeams(r.teams)
      })
      .catch(() => {
        /* assignees fall back to ids; the list's own error is shown */
      })
    return () => {
      gone = true
      stop()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const setViewP = (v: 'board' | 'list') => {
    setView(v)
    localStorage.setItem('tickets.view', v)
  }

  const newTicket = newOpen ? (
    <NewTicket bots={bots} teams={teams} preAssignee={fAssignee} onClose={() => setNewOpen(false)} onCreated={() => { setNewOpen(false); void load() }} />
  ) : null

  if (tickets === null) {
    return (
      <div class="col tickets-screen">
        <h1 class="screen-title serif">Tickets</h1>
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load tickets: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="muted">Loading…</p>
        )}
      </div>
    )
  }

  // Nothing yet, and no filter narrowing it: say what tickets are for.
  if (tickets.length === 0 && !fAssignee && !fPriority) {
    return (
      <div class="col tickets-screen">
        <h1 class="screen-title serif">Tickets</h1>
        {err ? <div class="banner err" role="alert">{err}</div> : null}
        {configured ? (
          <EmptyState
            testid="tickets-empty"
            art={<TicketGlyph />}
            title="No tickets yet"
            action={{ label: 'New ticket', onClick: () => setNewOpen(true), testid: 'new-ticket' }}
          >
            <p>A ticket is one piece of work for a bot or a team: what you want done, and by when. Mark it ready and the work starts.</p>
          </EmptyState>
        ) : (
          <EmptyState
            testid="tickets-empty"
            art={<TicketGlyph />}
            title="Tickets need a bot to work on them"
            action={{ label: 'Create a bot', href: '#/bots', testid: 'tickets-empty-bots' }}
          >
            <p>A ticket is one piece of work you hand to a bot or a team. There are no bots on this conductor yet, so start with one.</p>
          </EmptyState>
        )}
        {newTicket}
      </div>
    )
  }

  return (
    <div class="col tickets-screen">
      <div class="screen-head">
        <h1 class="screen-title serif">Tickets <span class="count">{tickets.length}</span></h1>
        <div class="row-actions">
          <div class="seg-control" role="tablist">
            <button class={'seg' + (view === 'board' ? ' on' : '')} type="button" onClick={() => setViewP('board')}>Board</button>
            <button class={'seg' + (view === 'list' ? ' on' : '')} type="button" onClick={() => setViewP('list')}>List</button>
          </div>
          <button class="btn primary" type="button" data-testid="new-ticket" onClick={() => setNewOpen(true)}>New ticket</button>
        </div>
      </div>

      {err ? <div class="banner err">{err}</div> : null}

      <div class="filter-bar">
        <select class="field" value={fAssignee} onChange={(e) => setFAssignee((e.target as HTMLSelectElement).value)}>
          <option value="">Any assignee</option>
          {Object.keys(bots).map((b) => <option value={`bot:${b}`}>{displayName(bots[b].identity, b)}</option>)}
          {teams.map((t) => <option value={`team:${t.team}`}>team {t.team}</option>)}
        </select>
        <select class="field" value={fPriority} onChange={(e) => setFPriority((e.target as HTMLSelectElement).value)}>
          <option value="">Any priority</option>
          {['urgent', 'high', 'normal', 'low'].map((p) => <option value={p}>{p}</option>)}
        </select>
      </div>

      {view === 'board' ? <Board tickets={tickets} bots={bots} /> : <section class="panel"><TicketTable tickets={tickets} bots={bots} /></section>}

      {newTicket}
    </div>
  )
}

function Board({ tickets, bots }: { tickets: Ticket[]; bots: Record<string, BotSummary> }): JSX.Element {
  const byStatus = useMemo(() => {
    const m: Record<string, Ticket[]> = {}
    for (const s of STATUSES) m[s] = []
    for (const t of tickets) (m[t.status] ||= []).push(t)
    return m
  }, [tickets])
  return (
    <div class="board" data-testid="ticket-board">
      {STATUSES.map((s) => (
        <div class="board-col" data-status={s}>
          <div class="board-col-head">
            <span class="board-col-name">{STATUS_LABEL[s]}</span>
            <span class="count">{byStatus[s].length}</span>
          </div>
          <div class="board-col-body">
            {byStatus[s].map((t) => <TicketCard key={t.ticket} t={t} bots={bots} />)}
          </div>
        </div>
      ))}
    </div>
  )
}

function TicketCard({ t, bots }: { t: Ticket; bots: Record<string, BotSummary> }): JSX.Element {
  const a = assigneeId(t.assignee, bots)
  return (
    <a class="ticket-card" href={`#/tickets/${t.ticket}`} data-ticket={t.ticket} data-priority={t.priority}>
      <div class="ticket-card-top">
        <span class="mono id">{t.ticket}</span>
        {t.priority && t.priority !== 'normal' ? <span class={'tag pri-' + t.priority}>{t.priority}</span> : null}
      </div>
      <div class="ticket-card-title">{t.title}</div>
      <div class="ticket-card-foot">
        {a.identity ? <Avatar id={a.identity} size={22} /> : null}
        <span class="muted">{a.label}</span>
        <span class="muted dot">·</span>
        <span class="muted">{ago(t.updated)}</span>
      </div>
    </a>
  )
}

// TicketTable is the flat list, reused in a bot's and a team's Tickets tab.
export function TicketTable({ tickets, bots }: { tickets: Ticket[]; bots: Record<string, BotSummary> }): JSX.Element {
  if (!tickets.length) return <p class="muted">No tickets here.</p>
  return (
    <ul class="ticket-list" data-testid="ticket-list">
      {tickets.map((t) => {
        const a = assigneeId(t.assignee, bots)
        return (
          <li class="ticket-row" data-ticket={t.ticket} data-status={t.status}>
            <a class="ticket-row-link" href={`#/tickets/${t.ticket}`}>
              <span class={'status-dot s-' + t.status} title={STATUS_LABEL[t.status]} />
              <span class="ticket-row-title">{t.title}</span>
              <span class="mono id">{t.ticket}</span>
              <span class="facts muted">
                {a.label}
                {t.priority && t.priority !== 'normal' ? ` · ${t.priority}` : ''}
                {` · ${STATUS_LABEL[t.status] || t.status}`}
              </span>
            </a>
          </li>
        )
      })}
    </ul>
  )
}

function NewTicket({
  bots,
  teams,
  preAssignee,
  onClose,
  onCreated,
}: {
  bots: Record<string, BotSummary>
  teams: TeamSummary[]
  preAssignee?: string
  onClose: () => void
  onCreated: () => void
}): JSX.Element {
  const [title, setTitle] = useState('')
  const [desc, setDesc] = useState('')
  const [assignee, setAssignee] = useState(preAssignee || '')
  const [priority, setPriority] = useState('normal')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  async function create(ready: boolean) {
    if (!title.trim()) {
      setErr('give it a title')
      return
    }
    if (ready && !assignee) {
      setErr('to be ready it needs an assignee')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const t = await botsApi.createTicket({ title: title.trim(), description: desc, assignee: assignee || undefined, priority })
      if (ready) await botsApi.patchTicket(t.ticket, { status: 'ready' })
      onCreated()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="sheet-backdrop" onClick={onClose}>
      <aside class="sheet" role="dialog" aria-label="New ticket" onClick={(e) => e.stopPropagation()}>
        <div class="sheet-head">
          <h2 class="serif">New ticket</h2>
          <button class="btn ghost sm" type="button" onClick={onClose}>Close</button>
        </div>
        <div class="sheet-body">
          <label class="form-field"><span>Title</span><input class="field" data-testid="ticket-title" value={title} onInput={(e) => setTitle((e.target as HTMLInputElement).value)} /></label>
          <label class="form-field"><span>Description</span><textarea class="field" rows={3} value={desc} onInput={(e) => setDesc((e.target as HTMLTextAreaElement).value)} /></label>
          <label class="form-field">
            <span>Assign to</span>
            <select class="field" data-testid="ticket-assignee" value={assignee} onChange={(e) => setAssignee((e.target as HTMLSelectElement).value)}>
              <option value="">Nobody yet</option>
              {Object.keys(bots).map((b) => <option value={`bot:${b}`}>{displayName(bots[b].identity, b)}</option>)}
              {teams.map((t) => <option value={`team:${t.team}`}>team {t.team}</option>)}
            </select>
          </label>
          <label class="form-field">
            <span>Priority</span>
            <select class="field" value={priority} onChange={(e) => setPriority((e.target as HTMLSelectElement).value)}>
              {['urgent', 'high', 'normal', 'low'].map((p) => <option value={p}>{p}</option>)}
            </select>
          </label>
          {err ? <div class="banner err">{err}</div> : null}
        </div>
        <div class="sheet-foot">
          <button class="btn" type="button" disabled={busy} data-testid="create-draft" onClick={() => void create(false)}>Save as a draft</button>
          <button class="btn primary" type="button" disabled={busy} data-testid="create-ready" onClick={() => void create(true)}>Create, ready to start</button>
        </div>
      </aside>
    </div>
  )
}

// TicketGlyph is a ticket stub: the empty state's picture.
function TicketGlyph(): JSX.Element {
  return (
    <svg width="64" height="44" viewBox="0 0 64 44" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round">
      <path d="M4 6h56v10a6 6 0 0 0 0 12v10H4V28a6 6 0 0 0 0-12z" />
      <path d="M44 8v28" stroke-dasharray="3 4" opacity="0.55" />
      <path d="M14 18h20M14 26h14" opacity="0.55" />
    </svg>
  )
}
