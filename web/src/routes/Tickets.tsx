// Tickets (design §4.4.1): the Board by default (one column per status), or a
// flat List. A ticket is one piece of work for a team or a bot; marking it ready
// (an operator act, with an assignee) is what lets work start. New ticket is a
// small form; the same rows power a bot's and a team's Tickets tabs.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { botsApi, displayName, who, openTicketStream, type Ticket, type BotSummary, type TeamSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { ago } from '../lib/format'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'

// BOARD_STATUSES excludes declined, same as the prototype's board columns
// (ORDER.filter(k => k !== 'declined')); declined tickets still show in the
// flat list and count toward the Status filter.
export const STATUSES = ['draft', 'ready', 'in_progress', 'waiting', 'done'] as const
export const STATUS_LABEL: Record<string, string> = {
  draft: 'Draft',
  ready: 'Ready',
  in_progress: 'In progress',
  waiting: 'Waiting on you',
  done: 'Done',
  declined: 'Declined',
}
// The same vocabulary as every list's mark (design §3.2): draft/ready/progress
// are neutral (marks.css), waiting is the accent ember, done is success.
const STATUS_MARK: Record<string, string> = {
  draft: 'draft',
  ready: 'ready',
  in_progress: 'progress',
  waiting: 'ember',
  done: 'ok',
  declined: 'fail',
}
const PRIORITIES = ['urgent', 'high', 'normal', 'low']

function assigneeId(a: string | undefined, bots: Record<string, BotSummary>): { label: string; identity?: BotSummary['identity']; team?: boolean } {
  if (!a) return { label: 'nobody yet' }
  const w = who(a)
  if (w.kind === 'bot') return { label: displayName(bots[w.name]?.identity, w.name), identity: bots[w.name]?.identity }
  return { label: `team ${w.name}`, team: true }
}

function tally<T>(xs: T[]): Map<T, number> {
  const m = new Map<T, number>()
  for (const x of xs) m.set(x, (m.get(x) || 0) + 1)
  return m
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

  // Filtering is client-side against the full ticket list, same as the
  // prototype's own tv/keep() — one fetch, every menu's counts and the board's
  // columns all read the same data, and "N of M" / Clear match instantly.
  const [q, setQ] = useState('')
  const [fAssignee, setFAssignee] = useState<string[]>(params.get('assignee') ? [params.get('assignee')!] : [])
  const [fStatus, setFStatus] = useState<string[]>([])
  const [fPriority, setFPriority] = useState<string[]>([])
  const [fLabel, setFLabel] = useState<string[]>([])

  const load = () =>
    botsApi
      .tickets({})
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
    <NewTicket bots={bots} teams={teams} preAssignee={fAssignee[0]} onClose={() => setNewOpen(false)} onCreated={() => { setNewOpen(false); void load() }} />
  ) : null

  if (tickets === null) {
    return (
      <div class="dl-phead">
        <div>
          <h1>Tickets</h1>
          {err ? (
            <div class="banner err" role="alert">
              Couldn't load tickets: {err}{' '}
              <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
            </div>
          ) : (
            <p class="muted">Loading…</p>
          )}
        </div>
      </div>
    )
  }

  // Nothing yet at all: say what tickets are for.
  if (tickets.length === 0) {
    return (
      <div class="tk-page">
        <div class="dl-phead"><div><h1>Tickets</h1></div></div>
        {err ? <div class="banner err" role="alert">{err}</div> : null}
        {configured ? (
          <EmptyState
            hero
            testid="tickets-empty"
            art={<TicketGlyph />}
            title="No tickets yet"
            action={{ label: 'New ticket', onClick: () => setNewOpen(true), testid: 'new-ticket' }}
          >
            <p>A ticket is one piece of work for a bot or a team: what you want done, and by when. Mark it ready and the work starts.</p>
          </EmptyState>
        ) : (
          <EmptyState
            hero
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

  const assigneeKey = (t: Ticket): string => t.assignee || 'nobody'
  const keep = (t: Ticket): boolean =>
    (!fAssignee.length || fAssignee.includes(assigneeKey(t))) &&
    (!fStatus.length || fStatus.includes(t.status)) &&
    (!fPriority.length || fPriority.includes(t.priority || 'normal')) &&
    (!fLabel.length || (t.labels || []).some((l) => fLabel.includes(l))) &&
    (!q || `${t.title} ${t.description || ''} ${(t.labels || []).join(' ')}`.toLowerCase().includes(q.toLowerCase()))
  const shown = tickets.filter(keep)
  const filtered = shown.length < tickets.length

  const open = tickets.filter((t) => t.status !== 'done' && t.status !== 'declined').length
  const drafts = tickets.filter((t) => t.status === 'draft').length

  const assigneeOptions = [...tally(tickets.map(assigneeKey))].map(([v, n]) => ({
    v,
    label: v === 'nobody' ? 'Nobody yet' : who(v).kind === 'team' ? `team ${who(v).name}` : displayName(bots[who(v).name]?.identity, who(v).name),
    n,
  }))
  const statusOptions = STATUSES.concat('declined' as (typeof STATUSES)[number]).map((s) => ({ v: s, label: STATUS_LABEL[s], n: tickets.filter((t) => t.status === s).length })).filter((o) => o.n)
  const priorityOptions = PRIORITIES.map((p) => ({ v: p, label: p, n: tickets.filter((t) => (t.priority || 'normal') === p).length })).filter((o) => o.n)
  const labelOptions = [...tally(tickets.flatMap((t) => t.labels || []))].map(([v, n]) => ({ v, label: v, n }))

  const clearFilters = () => {
    setQ('')
    setFAssignee([])
    setFStatus([])
    setFPriority([])
    setFLabel([])
  }

  const toggle = (xs: string[], set: (v: string[]) => void, v: string) => set(xs.includes(v) ? xs.filter((x) => x !== v) : [...xs, v])

  const menus: FilterMenu[] = [
    { key: 'who', label: 'Assignee', multi: true, selected: fAssignee, options: assigneeOptions, onToggle: (v) => toggle(fAssignee, setFAssignee, v), onClear: () => setFAssignee([]) },
    { key: 'status', label: 'Status', multi: true, selected: fStatus, options: statusOptions, onToggle: (v) => toggle(fStatus, setFStatus, v), onClear: () => setFStatus([]) },
    { key: 'priority', label: 'Priority', multi: true, selected: fPriority, options: priorityOptions, onToggle: (v) => toggle(fPriority, setFPriority, v), onClear: () => setFPriority([]) },
    ...(labelOptions.length ? [{ key: 'label', label: 'Label', multi: true, selected: fLabel, options: labelOptions, onToggle: (v: string) => toggle(fLabel, setFLabel, v), onClear: () => setFLabel([]) }] : []),
  ]

  return (
    <div class="tk-page">
      <div class="dl-phead">
        <div>
          <h1>Tickets <span class="h1-count">{open} open{drafts ? ` · ${drafts} draft${drafts > 1 ? 's' : ''}` : ''}</span></h1>
          <p class="lede-2">A ticket is one piece of work for a team or a bot. Drafts are ideas you and the bots shape together; work starts only once someone marks a ticket ready.</p>
        </div>
        <div class="dl-actions">
          <button class="btn primary" type="button" data-testid="new-ticket" onClick={() => setNewOpen(true)}>
            <Icon name="plus" />New ticket
          </button>
        </div>
      </div>

      {err ? <div class="banner err">{err}</div> : null}

      <div class="tk-bar">
        <FilterBar
          q={q}
          onQueryChange={setQ}
          placeholder="Find a ticket, or filter: status:draft"
          ariaLabel="Find and filter tickets"
          menus={menus}
        />
        <div class="seg fb-seg tk-view" role="group" aria-label="Show as">
          <button type="button" aria-pressed={view === 'board'} onClick={() => setViewP('board')}><Icon name="grid" /><span class="tk-vl">Board</span></button>
          <button type="button" aria-pressed={view === 'list'} onClick={() => setViewP('list')}><Icon name="logs" /><span class="tk-vl">List</span></button>
        </div>
      </div>

      {filtered ? (
        <p class="dl-sub tk-showing">Showing {shown.length} of {tickets.length} · <button class="link" type="button" onClick={clearFilters}>Clear</button></p>
      ) : null}

      {shown.length === 0 ? (
        <p class="dl-sub">No ticket matches.</p>
      ) : view === 'board' ? (
        <Board tickets={shown} bots={bots} />
      ) : (
        <FlatList tickets={shown} bots={bots} />
      )}

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
  // On a phone the board shows one column at a time, picked from the status
  // tabs above it (screens.css: `.tk-board > .tk-col:not(.on) { display: none }`
  // at 720px) — without a selection every column is display:none there, which
  // is what made the phone board read as blank.
  const [col, setCol] = useState<string>(() => STATUSES.find((s) => byStatus[s]?.length) || 'draft')
  useEffect(() => {
    if (!byStatus[col]?.length && STATUSES.some((s) => byStatus[s]?.length)) {
      setCol(STATUSES.find((s) => byStatus[s]?.length) || 'draft')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tickets])
  return (
    <>
      <div class="tk-colpick" role="tablist" aria-label="Status">
        {STATUSES.map((s) => (
          <button type="button" role="tab" aria-selected={col === s} onClick={() => setCol(s)}>
            {STATUS_LABEL[s]} <span class="dl-sub">{byStatus[s].length}</span>
          </button>
        ))}
      </div>
      <div class="tk-board" data-testid="ticket-board">
        {STATUSES.map((s) => (
          <section class={'tk-col' + (col === s ? ' on' : '')} data-col={s} aria-label={STATUS_LABEL[s]}>
            <header class="tk-colh">
              <StatusMark s={STATUS_MARK[s]} mkOnly label={STATUS_LABEL[s]} />
              <h2>{STATUS_LABEL[s]}</h2>
              <span class="count">{byStatus[s].length}</span>
            </header>
            <ul class="tk-cards">
              {byStatus[s].length ? (
                byStatus[s].map((t) => <TicketCard key={t.ticket} t={t} bots={bots} />)
              ) : (
                <li class="tk-none dl-sub">{s === 'draft' ? 'No drafts. Write one, or a bot will when it has an idea.' : 'None'}</li>
              )}
            </ul>
          </section>
        ))}
      </div>
    </>
  )
}

function TicketCard({ t, bots }: { t: Ticket; bots: Record<string, BotSummary> }): JSX.Element {
  const a = assigneeId(t.assignee, bots)
  return (
    <li class="tk-card" data-ticket={t.ticket} data-priority={t.priority}>
      <a class="ticket-card" href={`#/tickets/${t.ticket}`}>
        <b>{t.title}</b>
        <span class="dl-why">{a.label}{t.priority && t.priority !== 'normal' ? ` · ${t.priority}` : ''}</span>
        <span class="fx">
          {a.identity ? <span class="fx-i"><Avatar id={a.identity} size={16} /> {a.label}</span> : null}
          <span class="fx-i">{ago(t.updated)}</span>
        </span>
        {t.status === 'draft' ? <span class="tk-card-act"><span class="btn sm secondary">Mark ready…</span></span> : null}
      </a>
    </li>
  )
}

// FlatList is the Board's alternative: one dl-card section, every ticket as a
// dl-need row (the same anatomy as every list in the app: mark, title, a line
// in words, the facts, the age). Reused in a bot's and a team's Tickets tab.
export function TicketTable({ tickets, bots }: { tickets: Ticket[]; bots: Record<string, BotSummary> }): JSX.Element {
  if (!tickets.length) return <p class="muted">No tickets here.</p>
  return (
    <ul class="dl-list" data-testid="ticket-list">
      {tickets.map((t) => <TicketRow key={t.ticket} t={t} bots={bots} />)}
    </ul>
  )
}

function FlatList({ tickets, bots }: { tickets: Ticket[]; bots: Record<string, BotSummary> }): JSX.Element {
  return (
    <section class="dl-card tk-sec tk-flat">
      <header class="dl-ch">
        <h2>All tickets</h2>
        <span class="count">{tickets.length}</span>
      </header>
      <TicketTable tickets={tickets} bots={bots} />
    </section>
  )
}

function TicketRow({ t, bots }: { t: Ticket; bots: Record<string, BotSummary> }): JSX.Element {
  const a = assigneeId(t.assignee, bots)
  return (
    <li class="dl-need tk-row ticket-row" data-ticket={t.ticket} data-status={t.status}>
      <a class="dl-need-a ticket-row-link" href={`#/tickets/${t.ticket}`}>
        <span class="dl-ng"><StatusMark s={STATUS_MARK[t.status]} lg label={STATUS_LABEL[t.status]} /></span>
        <span class="dl-nt">
          <b>{t.title}</b>
          <span class="dl-why">{STATUS_LABEL[t.status] || t.status} · {a.label}</span>
        </span>
        <span class="dl-age">{ago(t.updated)}</span>
        <span class="dl-na">
          {t.status === 'draft' ? <span class="btn sm secondary">Mark ready…</span> : null}
        </span>
      </a>
    </li>
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
              {PRIORITIES.map((p) => <option value={p}>{p}</option>)}
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
