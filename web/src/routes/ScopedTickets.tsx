// The Tickets tab of a bot's or a team's page (prototype js/screens/tickets.js
// UI.botTickets / UI.teamTickets): the same rows as Tickets, narrowed to the
// ones assigned to it — a lede, "New ticket for it", the shared find/filter
// bar with the Board/List switch at its end, then the board (one column per
// status; on a phone one at a time, picked from the status pills) or the list.
// The board and list are Tickets' own (Board, FlatList).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, who, openTicketStream, type Ticket, type BotSummary, type TeamSummary } from '../api/bots'
import { Icon } from '../components/Icon'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { humanError } from '../api/errors'
import { Board, FlatList, NewTicket, STATUSES, STATUS_LABEL } from './Tickets'

const PRIORITIES: [string, string][] = [['urgent', 'Urgent'], ['high', 'High'], ['normal', 'Normal'], ['low', 'Low']]

function tally<T>(xs: T[]): Map<T, number> {
  const m = new Map<T, number>()
  for (const x of xs) m.set(x, (m.get(x) || 0) + 1)
  return m
}

export function ScopedTickets({ assignee }: { assignee: string }): JSX.Element {
  const scope = who(assignee)
  const isTeam = scope.kind === 'team'
  const [tickets, setTickets] = useState<Ticket[] | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [err, setErr] = useState('')
  const [view, setView] = useState<'board' | 'list'>((localStorage.getItem('tickets.view') as 'board' | 'list') || 'board')
  const [newOpen, setNewOpen] = useState(false)
  const [q, setQ] = useState('')
  const [fWho, setFWho] = useState<string[]>([])
  const [fStatus, setFStatus] = useState<string[]>([])
  const [fPriority, setFPriority] = useState<string[]>([])
  const [fLabel, setFLabel] = useState<string[]>([])

  // A bot's tickets are the ones assigned to it; a team's are the ones for
  // the team and for each of its bots. Every status, done and declined too
  // (the board's Done column and the Status filter read them).
  const all = 'draft,ready,in_progress,waiting,done,declined'
  const load = async () => {
    try {
      const teamBots = isTeam ? (teams.find((t) => t.team === scope.name)?.bots || []) : []
      const lists = await Promise.all([assignee, ...teamBots.map((b) => `bot:${b}`)].map((a) => botsApi.tickets({ assignee: a, status: all })))
      const seen = new Set<string>()
      const out: Ticket[] = []
      for (const l of lists) for (const t of l.tickets || []) if (!seen.has(t.ticket)) { seen.add(t.ticket); out.push(t) }
      setTickets(out)
      setErr('')
    } catch (e) {
      setErr(humanError(e))
      setTickets((t) => t || [])
    }
  }

  useEffect(() => {
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setBots(m)
      setTeams(r.teams)
    }).catch(() => setTeams([]))
  }, [])
  useEffect(() => {
    if (isTeam && !teams.length) return
    void load()
    const stop = openTicketStream(() => void load())
    return stop
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [assignee, teams])

  const setViewP = (v: 'board' | 'list') => {
    setView(v)
    localStorage.setItem('tickets.view', v)
  }

  const name = isTeam ? scope.name : displayName(bots[scope.name]?.identity, scope.name)
  const lede = isTeam ? (
    <p class="dl-sub">Tickets for team {scope.name} and its bots. One for the team goes to its lead or routing, which picks a bot.</p>
  ) : (
    <p class="dl-sub">Tickets assigned to {name}: from you, a chat, an event or its own checks. It keeps its notes on each and the parts it hands to teammates.</p>
  )

  const list = tickets || []
  const whoKey = (t: Ticket) => t.assignee || 'nobody'
  const keep = (t: Ticket) =>
    (!fWho.length || fWho.includes(whoKey(t))) &&
    (!fStatus.length || fStatus.includes(t.status)) &&
    (!fPriority.length || fPriority.includes(t.priority || 'normal')) &&
    (!fLabel.length || (t.labels || []).some((l) => fLabel.includes(l))) &&
    (!q || `${t.title} ${t.description || ''} ${t.assignee || ''} ${(t.labels || []).join(' ')}`.toLowerCase().includes(q.toLowerCase()))
  const shown = list.filter(keep)
  const filtered = shown.length < list.length
  const toggle = (xs: string[], set: (v: string[]) => void, v: string) => set(xs.includes(v) ? xs.filter((x) => x !== v) : [...xs, v])
  const whoLabel = (v: string) => (v === 'nobody' ? 'Nobody yet' : who(v).kind === 'team' ? `team ${who(v).name}` : displayName(bots[who(v).name]?.identity, who(v).name))
  const menus: FilterMenu[] = [
    { key: 'who', label: 'Assignee', multi: true, selected: fWho, options: [...tally(list.map(whoKey))].map(([v, n]) => ({ v, label: whoLabel(v), n })), onToggle: (v) => toggle(fWho, setFWho, v), onClear: () => setFWho([]) },
    { key: 'status', label: 'Status', multi: true, selected: fStatus, options: [...STATUSES, 'declined'].map((s) => ({ v: s, label: STATUS_LABEL[s], n: list.filter((t) => t.status === s).length })).filter((o) => o.n), onToggle: (v) => toggle(fStatus, setFStatus, v), onClear: () => setFStatus([]) },
    { key: 'priority', label: 'Priority', multi: true, selected: fPriority, options: PRIORITIES.map(([v, label]) => ({ v, label, n: list.filter((t) => (t.priority || 'normal') === v).length })).filter((o) => o.n), onToggle: (v) => toggle(fPriority, setFPriority, v), onClear: () => setFPriority([]) },
    { key: 'label', label: 'Label', multi: true, more: true, selected: fLabel, options: [...tally(list.flatMap((t) => t.labels || []))].map(([v, n]) => ({ v, label: v, n })), onToggle: (v: string) => toggle(fLabel, setFLabel, v), onClear: () => setFLabel([]) },
  ]
  const clear = () => {
    setQ('')
    setFWho([])
    setFStatus([])
    setFPriority([])
    setFLabel([])
  }
  const viewCtl = (
    <div class="seg fb-seg tk-view" role="group" aria-label="Show as">
      <button type="button" aria-pressed={view === 'board'} aria-label="Board" onClick={() => setViewP('board')}><Icon name="grid" /><span class="tk-vl">Board</span></button>
      <button type="button" aria-pressed={view === 'list'} aria-label="List" onClick={() => setViewP('list')}><Icon name="logs" /><span class="tk-vl">List</span></button>
    </div>
  )
  const none = isTeam ? `No tickets for ${scope.name}.` : `Nothing assigned to ${scope.name}.`

  return (
    <section class="tk-in" data-assignee={assignee} data-testid="scoped-tickets">
      <header class="dl-ch tk-inh">
        {lede}
        <span class="dl-sp" />
        <button class="btn sm secondary" type="button" data-testid="scoped-new-ticket" onClick={() => setNewOpen(true)}>
          <Icon name="plus" />
          {isTeam ? 'New ticket for the team' : 'New ticket for it'}
        </button>
      </header>
      {err ? <div class="banner err" role="alert">Couldn't load its tickets: {err}</div> : null}
      <FilterBar q={q} onQueryChange={setQ} placeholder="Find a ticket, or filter: status:draft" ariaLabel="Find and filter tickets" menus={menus} end={viewCtl} filterByHint />
      {filtered ? (
        <p class="dl-sub tk-showing">Showing {shown.length} of {list.length} · <button class="link" type="button" onClick={clear}>Clear</button></p>
      ) : null}
      {tickets === null ? (
        <p class="dl-sub">Loading…</p>
      ) : view === 'board' ? (
        <Board tickets={shown} bots={bots} />
      ) : shown.length ? (
        <FlatList tickets={shown} bots={bots} />
      ) : (
        <p class="dl-sub">{filtered ? 'No ticket matches.' : none}</p>
      )}
      {newOpen ? (
        <NewTicket bots={bots} teams={teams} preAssignee={assignee} onClose={() => setNewOpen(false)} onCreated={() => { setNewOpen(false); void load() }} />
      ) : null}
    </section>
  )
}
