// Tickets (prototype js/screens/tickets.js, ported line for line): the work
// conductor's bots do, in conductor's own ticket system. The Board by default
// (one column per status, Draft to Done; Done holds the last 7 days; drag a
// card to move it, within the daemon's status machine), or a flat List, 20 to
// a page, its status a fact. The shared filter bar finds and filters both:
// Assignee, Status, Priority, and Label under More filters; each is also a
// typed qualifier (status:draft) that "Filter by…" lists. On a phone the board
// is one column at a time. Every move goes through PATCH /api/v1/tickets/{id}.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, openTicketStream, type Ticket, type ProposalRow } from '../api/bots'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { FactsLine } from '../components/Facts'
import { EmptyState } from '../components/EmptyState'
import { humanError } from '../api/errors'
import { Count, ToastHost, toast } from '../components/ChatBits'
import {
  ORDER, PRI, STATUS, ALL_STATUSES, statusWord, statusMark, by, cap, ticketFacts, teamOf, TicketAct, TicketRow, TicketGlyph, pendingByCase,
  useMarkReady, loadNames, type Names,
} from '../components/TicketKit'
import { who, displayName } from '../api/bots'

export const STATUS_LABEL: Record<string, string> = Object.fromEntries(Object.entries(STATUS).map(([k, v]) => [k, v[0]]))
const COLS = ['draft', 'ready', 'in_progress', 'waiting', 'done']
const WEEK = 7 * 864e5
const PAGE = 20
const byPri = (a: Ticket, b: Ticket) =>
  PRI.findIndex(([k]) => k === (a.priority || 'normal')) - PRI.findIndex(([k]) => k === (b.priority || 'normal')) || Date.parse(b.created) - Date.parse(a.created)
const recent = (t: Ticket) => Date.now() - Date.parse(t.updated || t.created) <= WEEK

function tally<T>(xs: T[]): Map<T, number> {
  const m = new Map<T, number>()
  for (const x of xs) m.set(x, (m.get(x) || 0) + 1)
  return m
}

// scope narrows the page to one assignee, for a bot's or a team's Tickets tab
// ('bot:<name>' or 'team:<name>'; a team's also holds its bots' tickets): the
// same board, list and filters, under the tab's own lede instead of the page head.
export function Tickets({ scope }: { scope?: string } = {}): JSX.Element {
  const [allTickets, setTickets] = useState<Ticket[] | null>(null)
  const [configured, setConfigured] = useState(true)
  const [n, setN] = useState<Names>({ bots: {}, teams: {} })
  const [view, setView] = useState<'board' | 'list'>((localStorage.getItem('tickets.view') as 'board' | 'list') || 'board')
  const [err, setErr] = useState('')
  const params = new URLSearchParams(location.hash.split('?')[1] || '')
  const [q, setQ] = useState('')
  const [fWho, setFWho] = useState<string[]>(params.get('assignee') ? [params.get('assignee')!] : [])
  const [fStatus, setFStatus] = useState<string[]>([])
  const [fPriority, setFPriority] = useState<string[]>([])
  const [fLabel, setFLabel] = useState<string[]>([])
  const [page, setPage] = useState(0)
  const [older, setOlder] = useState(false)
  const [col, setCol] = useState('')
  const [over, setOver] = useState('')

  const load = () =>
    botsApi
      .tickets({ status: ALL_STATUSES })
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
    void load().then((live) => {
      if (live && !gone) stop = openTicketStream(() => void load())
    })
    loadNames(setN)
    botsApi.proposals().then((r) => setReview(pendingByCase(r.proposals || []))).catch(() => {})
    return () => {
      gone = true
      stop()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const [review, setReview] = useState<Record<string, ProposalRow>>({})
  const sc = scope ? who(scope) : null
  const inScope = (t: Ticket) => !sc || t.assignee === scope || (sc.kind === 'team' && teamOf(t, n) === sc.name)
  const tickets = allTickets && sc ? allTickets.filter(inScope) : allTickets
  const scName = sc ? (sc.kind === 'team' ? sc.name : displayName(n.bots[sc.name]?.identity, sc.name)) : ''
  const newHref = sc ? `#/tickets/new?${sc.kind}=${encodeURIComponent(sc.name)}` : '#/tickets/new'
  const wrap = (children: JSX.Element) =>
    sc ? <section class="tk-in" data-assignee={scope} data-testid="scoped-tickets">{children}</section> : <div class="tk-page">{children}</div>
  const [markReady, readyModal] = useMarkReady(n, () => void load())

  const head = (count?: JSX.Element) => sc ? (
    <header class="dl-ch tk-inh">
      {sc.kind === 'team' ? (
        <p class="dl-sub">Tickets for team {sc.name} and its bots. One for the team goes to its lead or routing, which picks a bot.</p>
      ) : (
        <p class="dl-sub">Tickets assigned to {scName}: from you, a chat, an event or its own checks. It keeps its notes on each and the parts it hands to teammates.</p>
      )}
      <span class="dl-sp" />
      {count ? (
        <a class="btn sm secondary" href={newHref} data-testid="scoped-new-ticket"><Icon name="plus" />{sc.kind === 'team' ? 'New ticket for the team' : 'New ticket for it'}</a>
      ) : null}
    </header>
  ) : (
    <div class="dl-phead">
      <div>
        <h1>Tickets {count}</h1>
        {count ? <p class="lede-2">A ticket is one piece of work for a team or a bot. Drafts are ideas you and the bots shape together; work starts only once someone marks a ticket ready.</p> : null}
      </div>
      {count ? (
        <div class="dl-actions">
          <a class="btn primary" href="#/tickets/new" data-testid="new-ticket"><Icon name="plus" />New ticket</a>
        </div>
      ) : null}
    </div>
  )

  if (tickets === null) {
    return (
      wrap(<>
        {head()}
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load tickets: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="dl-sub">Loading…</p>
        )}
      </>)
    )
  }

  if (sc && tickets.length === 0) {
    return (
      wrap(<>
        <EmptyState hero testid="tickets-empty" art={<TicketGlyph />} title={sc.kind === 'team' ? `No tickets for team ${sc.name} yet` : `No tickets for ${scName} yet`} action={{ label: sc.kind === 'team' ? 'New ticket for the team' : 'New ticket for it', href: newHref, testid: 'scoped-new-ticket' }}>
          <p>{sc.kind === 'team' ? `Tickets for team ${sc.name} and its bots show up here once someone asks them for something.` : `Tickets for ${scName} show up here once someone asks it for something.`}</p>
        </EmptyState>
      </>)
    )
  }

  if (tickets.length === 0) {
    return (
      <div class="tk-page">
        {head()}
        {configured ? (
          <EmptyState hero testid="tickets-empty" art={<TicketGlyph />} title="No tickets yet" action={{ label: 'New ticket', href: '#/tickets/new', testid: 'new-ticket' }}>
            <p>A ticket is one piece of work you hand a bot or a team, and work on it starts once you mark it ready.</p>
          </EmptyState>
        ) : (
          <EmptyState hero testid="tickets-empty" art={<TicketGlyph />} title="Tickets need a bot to work on them" action={{ label: 'Create a bot', href: '#/bots', testid: 'tickets-empty-bots' }}>
            <p>A ticket is work you hand to a bot, and this conductor has no bots yet, so start with one.</p>
          </EmptyState>
        )}
      </div>
    )
  }

  const whoKey = (t: Ticket) => t.assignee || 'nobody'
  const keep = (t: Ticket): boolean =>
    (!fWho.length || fWho.includes(whoKey(t)) || (!!teamOf(t, n) && fWho.includes('team:' + teamOf(t, n)))) &&
    (!fStatus.length || fStatus.includes(t.status)) &&
    (!fPriority.length || fPriority.includes(t.priority || 'normal')) &&
    (!fLabel.length || (t.labels || []).some((l) => fLabel.includes(l))) &&
    (!q || `${t.title} ${t.description || ''} ${t.assignee || ''} ${(t.labels || []).join(' ')}`.toLowerCase().includes(q.toLowerCase()))
  const shown = tickets.filter(keep)
  const filtered = shown.length < tickets.length
  const drafts = tickets.filter((t) => t.status === 'draft').length
  const open = tickets.filter((t) => t.status !== 'done' && t.status !== 'declined').length - drafts

  const whoM = tally(tickets.map(whoKey))
  for (const t of Object.keys(n.teams)) if (!whoM.has('team:' + t)) whoM.set('team:' + t, 0)
  const whoOptions = [...whoM]
    .map(([v, c]) => ({
      v,
      label: v === 'nobody' ? 'Nobody yet' : who(v).kind === 'team' ? `team ${who(v).name}` : displayName(n.bots[who(v).name]?.identity, who(v).name),
      n: who(v).kind === 'team' ? tickets.filter((t) => teamOf(t, n) === who(v).name).length : c,
    }))
    .filter((o) => o.n)
  const toggle = (xs: string[], set: (v: string[]) => void, v: string) => {
    set(xs.includes(v) ? xs.filter((x) => x !== v) : [...xs, v])
    setPage(0)
  }
  const menus: FilterMenu[] = [
    { key: 'who', alias: 'assignee', label: 'Assignee', multi: true, selected: fWho, options: whoOptions, onToggle: (v) => toggle(fWho, setFWho, v), onClear: () => setFWho([]) },
    { key: 'status', label: 'Status', multi: true, selected: fStatus, options: ORDER.map((v) => ({ v, label: STATUS[v][0], n: tickets.filter((t) => t.status === v).length })).filter((o) => o.n), onToggle: (v) => toggle(fStatus, setFStatus, v), onClear: () => setFStatus([]) },
    { key: 'priority', label: 'Priority', multi: true, selected: fPriority, options: PRI.map(([v, label]) => ({ v, label, n: tickets.filter((t) => (t.priority || 'normal') === v).length })).filter((o) => o.n), onToggle: (v) => toggle(fPriority, setFPriority, v), onClear: () => setFPriority([]) },
    { key: 'label', label: 'Label', multi: true, more: true, selected: fLabel, options: [...tally(tickets.flatMap((t) => t.labels || []))].map(([v, c]) => ({ v, label: v, n: c })), onToggle: (v) => toggle(fLabel, setFLabel, v), onClear: () => setFLabel([]) },
  ]
  const clear = () => {
    setQ('')
    setFWho([])
    setFStatus([])
    setFPriority([])
    setFLabel([])
    setPage(0)
  }
  const setViewP = (v: 'board' | 'list') => {
    setView(v)
    localStorage.setItem('tickets.view', v)
  }
  const viewCtl = (
    <div class="seg fb-seg tk-view" role="group" aria-label="Show as">
      {(['board', 'list'] as const).map((v) => (
        <button type="button" aria-pressed={view === v} aria-label={v === 'list' ? 'List' : 'Board'} data-testid={`tk-view-${v}`} onClick={() => setViewP(v)}>
          <Icon name={v === 'list' ? 'logs' : 'grid'} />
          <span class="tk-vl">{v === 'list' ? 'List' : 'Board'}</span>
        </button>
      ))}
    </div>
  )

  // a drag between columns: within the rules, or a word on why not
  async function move(num: string, to: string) {
    const t = tickets!.find((x) => x.ticket === num)
    if (!t) return
    const from = t.status
    if (from === to || (to === 'done' && from === 'declined')) return
    if (to === 'in_progress') return toast('conductor moves it there', 'a ticket is in progress once its bot starts on it', 'idle')
    if (to === 'waiting') return toast('conductor moves it there', 'a ticket waits on you when its bot asks you something', 'idle')
    let body: Record<string, unknown> | null = null
    if (to === 'draft') {
      if (from !== 'ready') return toast('Only a ready ticket goes back to draft', 'work has started on this one', 'idle')
      body = { status: 'draft' }
    } else if (to === 'ready') {
      if (from === 'draft') return markReady(t)
      if (from !== 'done' && from !== 'declined') return toast('It’s already under way', 'conductor moves it back when its work is done', 'idle')
      body = { status: 'reopen' }
    } else if (to === 'done') {
      if (from === 'draft') return toast('A draft hasn’t started', 'mark it ready, or decline it from its page', 'idle')
      body = { status: 'done' }
    }
    if (!body) return
    try {
      await botsApi.patchTicket(num, body)
      toast(STATUS[to === 'ready' && body.status === 'reopen' ? 'ready' : to][0], t.title, 'ok')
      void load()
    } catch (e) {
      toast('Couldn’t move it', humanError(e), 'fail')
    }
  }

  let body: JSX.Element | null = null
  if (view === 'board') {
    const of = (k: string) => shown.filter((t) => t.status === k || (k === 'done' && t.status === 'declined'))
    const cur = col && COLS.includes(col) ? col : COLS.find((k) => of(k).length) || 'draft'
    const done = of('done')
    const doneShown = older ? done : done.filter(recent)
    const olderN = done.length - doneShown.length
    const more = olderN ? (
      <li class="tk-older"><button type="button" class="btn sm ghost" onClick={() => setOlder(true)}>Show {olderN} older</button></li>
    ) : older && done.some((t) => !recent(t)) ? (
      <li class="tk-older"><button type="button" class="btn sm ghost" onClick={() => setOlder(false)}>Only the last 7 days</button></li>
    ) : null
    const card = (t: Ticket) => (
      <li
        class="tk-card"
        data-t={t.ticket}
        data-ticket={t.ticket}
        draggable
        key={t.ticket}
        onDragStart={(e) => {
          e.dataTransfer?.setData('text/x-ticket', t.ticket)
          if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
          ;(e.currentTarget as HTMLElement).classList.add('drag')
        }}
        onDragEnd={(e) => {
          ;(e.currentTarget as HTMLElement).classList.remove('drag')
          setOver('')
        }}
      >
        <a class="ticket-card" href={`#/tickets/${t.ticket}`} draggable={false}>
          <b>{t.title}</b>
          <span class="dl-why">{cap(by(t, n))}</span>
          <FactsLine one facts={ticketFacts(t, n)} />
          {t.status === 'draft' || (t.status === 'waiting' && review[t.id]) ? <span class="tk-card-act"><TicketAct t={t} onReady={markReady} review={review[t.id]} /></span> : null}
        </a>
      </li>
    )
    const column = (k: string, xs: Ticket[], extra: JSX.Element | null = null) => (
      <section
        class={'tk-col' + (cur === k ? ' on' : '') + (over === k ? ' over' : '')}
        data-col={k}
        aria-label={STATUS[k][0]}
        key={k}
        onDragOver={(e) => {
          e.preventDefault()
          setOver(k)
        }}
        onDrop={(e) => {
          e.preventDefault()
          setOver('')
          const id = e.dataTransfer?.getData('text/x-ticket')
          if (id) void move(id, k)
        }}
      >
        <header class="tk-colh">
          <StatusMark s={STATUS[k][1]} mkOnly />
          <h2>{STATUS[k][0]}</h2>
          <Count n={xs.length} hot={k === 'waiting' && xs.length > 0} />
        </header>
        <ul class="tk-cards">
          {xs.length ? [...xs].sort(byPri).map(card) : <li class="tk-none dl-sub">{k === 'draft' ? 'No drafts. Write one, or a bot will when it has an idea.' : 'None'}</li>}
          {extra}
        </ul>
      </section>
    )
    body = (
      <>
        <div class="tk-colpick" role="tablist" aria-label="Status">
          {COLS.map((k) => (
            <button type="button" role="tab" aria-selected={cur === k} key={k} onClick={() => setCol(k)}>
              {STATUS[k][0]} <span class="dl-sub">{(k === 'done' ? doneShown : of(k)).length}</span>
            </button>
          ))}
        </div>
        <div class="tk-board" data-base="#/tickets" data-testid="ticket-board">
          {COLS.map((k) => (k === 'done' ? column(k, doneShown, more) : column(k, of(k))))}
        </div>
      </>
    )
  } else {
    const all = [...shown].sort((a, b) => ORDER.indexOf(a.status) - ORDER.indexOf(b.status) || byPri(a, b))
    const pages = Math.max(1, Math.ceil(all.length / PAGE))
    const pg = Math.min(page, pages - 1)
    const from = pg * PAGE
    const xs = all.slice(from, from + PAGE)
    body = all.length ? (
      <section class="dl-card tk-sec tk-flat">
        <ul class="dl-list" data-testid="ticket-list">
          {xs.map((t) => <TicketRow key={t.ticket} t={t} n={n} statusFact onReady={markReady} review={review[t.id]} />)}
        </ul>
        {pages > 1 ? (
          <div class="pk-pages">
            <span class="dl-sub">{from + 1}–{from + xs.length} of {all.length}</span>
            <button class="btn sm" type="button" disabled={!pg} title={pg ? undefined : 'The first page'} onClick={() => setPage(pg - 1)}><Icon name="chev" class="flip" />Previous</button>
            <button class="btn sm" type="button" disabled={pg >= pages - 1} title={pg < pages - 1 ? undefined : 'The last page'} onClick={() => setPage(pg + 1)}>Next<Icon name="chev" /></button>
          </div>
        ) : null}
      </section>
    ) : null
  }

  return (
    wrap(<>
      {head(<span class="h1-count">{open} open{drafts ? ` · ${drafts} draft${drafts > 1 ? 's' : ''}` : ''}</span>)}
      {err ? <div class="banner err">{err}</div> : null}
      <FilterBar q={q} onQueryChange={(v) => { setQ(v); setPage(0) }} placeholder="Find a ticket, or filter: status:draft" ariaLabel="Find and filter tickets" menus={menus} qualifiers end={viewCtl} />
      {filtered ? (
        <p class="dl-sub tk-showing">
          Showing {shown.length} of {tickets.length} · <button class="link" type="button" onClick={clear}>Clear</button>
        </p>
      ) : null}
      {body ? body : <p class="dl-sub">{filtered ? 'No ticket matches.' : 'No tickets yet.'}</p>}
      {readyModal}
      <ToastHost />
    </>)
  )
}

// TicketTable: a bot's and a team's Tickets tab, the same rows narrowed.
export function TicketTable({ tickets, bots }: { tickets: Ticket[]; bots: Names['bots'] }): JSX.Element {
  const n: Names = { bots, teams: {} }
  const [markReady, modal] = useMarkReady(n, () => location.reload())
  if (!tickets.length) return <p class="dl-sub">No tickets here.</p>
  return (
    <>
      <ul class="dl-list" data-testid="ticket-list">
        {tickets.map((t) => <TicketRow key={t.ticket} t={t} n={n} statusFact onReady={markReady} />)}
      </ul>
      {modal}
    </>
  )
}

export { statusWord, statusMark }
