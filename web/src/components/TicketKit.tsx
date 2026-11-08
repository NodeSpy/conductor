// The ticket pieces every ticket screen shares (prototype js/screens/
// tickets.js): where a ticket stands in words and marks, who asked, its facts
// line, its row (the same anatomy as every list) and its board card, the
// "Mark ready…" act, and its history in words. A ticket's status is the
// daemon's own status machine (internal/bot/tickets.go ticketTransitions).
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { botsApi, who, type Ticket, type BotSummary, type TeamSummary, type ProposalRow } from '../api/bots'
import { authState } from '../api/auth'
import { StatusMark } from './StatusMark'
import { FactsLine, type Fact } from './Facts'
import { FormModal } from './FormModal'
import { agoText, nameOf, toast } from './ChatBits'
import { humanError } from '../api/errors'

export const STATUS: Record<string, [string, string]> = {
  draft: ['Draft', 'draft'],
  ready: ['Ready', 'ready'],
  in_progress: ['In progress', 'progress'],
  waiting: ['Waiting on you', 'ember'],
  done: ['Done', 'ok'],
  declined: ['Declined', 'fail'],
}
export const ORDER = ['draft', 'ready', 'in_progress', 'waiting', 'done', 'declined']
export const PRI: [string, string][] = [['urgent', 'Urgent'], ['high', 'High'], ['normal', 'Normal'], ['low', 'Low']]
export const priWord = (p?: string) => (PRI.find(([k]) => k === p) || PRI[2])[1]
export const statusWord = (s: string) => (STATUS[s] || [s])[0]
export const statusMark = (s: string) => (STATUS[s] || [s, 'idle'])[1]
export const isOpen = (t: Ticket) => t.status !== 'done' && t.status !== 'declined'

export interface Names {
  bots: Record<string, BotSummary>
  teams: Record<string, TeamSummary>
}

// actorWord is who did something, in words: you, another operator, a bot.
export function actorWord(s: string | undefined, n: Names): string {
  const r = String(s || '')
  const w = who(r)
  if (w.kind === 'operator') return !w.name || w.name === authState.value.operator ? 'you' : w.name
  if (w.kind === 'bot') return nameOf(n.bots[w.name]?.identity, w.name)
  if (r === 'conductor') return 'conductor'
  if (r.startsWith('chat')) return 'someone in chat'
  if (r) return 'an event'
  return 'someone'
}
export const askedBy = (t: Ticket, n: Names) => {
  const w = actorWord(t.created_by || t.requester, n)
  return who(t.created_by || '').kind === 'bot' ? `${w} (a bot)` : w
}
export const by = (t: Ticket, n: Names) => (t.status === 'draft' ? `drafted by ${askedBy(t, n).replace(' (a bot)', '')}` : `asked by ${askedBy(t, n)}`)
export const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

export const assigneeWord = (t: Ticket, n: Names): string => {
  if (!t.assignee) return 'Nobody yet'
  const w = who(t.assignee)
  return w.kind === 'team' ? `team ${w.name}` : nameOf(n.bots[w.name]?.identity, w.name)
}

export function dueWord(d?: string): string {
  if (!d) return ''
  const day = d.slice(0, 10)
  const days = Math.round((Date.parse(day + 'T12:00:00') - Date.now()) / 86400000)
  return days < 0 ? `overdue by ${-days} day${days === -1 ? '' : 's'}` : days === 0 ? 'due today' : days === 1 ? 'due tomorrow' : `due in ${days} days`
}

// teamOf is the team a ticket belongs to: its own team assignee, or its bot's.
export const teamOf = (t: Ticket, n: Names): string => {
  if (!t.assignee) return ''
  const w = who(t.assignee)
  return w.kind === 'team' ? w.name : n.bots[w.name]?.team || ''
}

// ticketFacts: who has it, priority when it isn't normal, when it's due, its
// labels (UI.ticketFacts).
export function ticketFacts(t: Ticket, n: Names, o: { noAssignee?: boolean } = {}): Fact[] {
  const w = t.assignee ? who(t.assignee) : null
  const out: (Fact | null)[] = [
    o.noAssignee || !w ? null : w.kind === 'bot' ? { icon: 'bots', value: nameOf(n.bots[w.name]?.identity, w.name), href: `#/bots/${w.name}`, pri: 1, label: 'Assigned to' } : { icon: 'users', value: `team ${w.name}`, href: `#/teams/${w.name}`, pri: 1, label: 'Assigned to the team' },
    t.priority === 'urgent' || t.priority === 'high' ? { icon: 'warn', value: priWord(t.priority), risky: t.priority === 'urgent', pri: 1, label: 'Priority' } : null,
    t.due && isOpen(t) ? { icon: 'calendar', value: dueWord(t.due), risky: /overdue|today/.test(dueWord(t.due)), pri: 2, label: 'Due' } : null,
    ...liveFacts(t),
    ...(t.labels || []).map((l): Fact => ({ icon: 'tag', value: l, pri: 3, label: 'Label' })),
  ]
  return out.filter((f): f is Fact => !!f)
}
// liveFacts: an in-progress ticket's last activity, so a stalled one shows.
function liveFacts(t: Ticket): Fact[] {
  if (t.status !== 'in_progress') return []
  const at = t.updated || t.created
  const old = at && Date.now() - Date.parse(at) > 864e5
  if (!at || at === t.created) return []
  return [{ icon: 'clock', value: `last activity ${agoText(at).replace(/ ago$/, '')} ago`.replace('just now ago', 'just now'), risky: !!old, pri: 2, label: old ? 'Nothing has happened on it for over a day' : 'Its last activity' }]
}

// whoOptions: who a ticket can go to (the prototype's whoOptions).
export function whoOptions(n: Names): [string, string][] {
  return [['', 'Nobody yet'] as [string, string]].concat(
    Object.values(n.teams).map((t): [string, string] => ['team:' + t.team, `team ${t.team}: its lead or routing picks a bot`]),
    Object.values(n.bots).map((b): [string, string] => ['bot:' + b.bot, `${nameOf(b.identity, b.bot)}${b.team ? ' · team ' + b.team : ' · no team'}`]),
  )
}

// MarkReady is the "Mark ready" act: straight away with an assignee; without
// one, it asks who should do it first (a ready ticket needs someone).
export function useMarkReady(n: Names, done: () => void): [(t: Ticket) => void, JSX.Element | null] {
  const [ask, setAsk] = useState<Ticket | null>(null)
  const go = async (t: Ticket, assignee?: string) => {
    try {
      await botsApi.patchTicket(t.ticket, assignee ? { assignee, status: 'ready' } : { status: 'ready' })
      toast('Ready', `${assignee ? assigneeWord({ ...t, assignee }, n) : assigneeWord(t, n)} may start on it`, 'ok')
      done()
    } catch (e) {
      toast('Couldn’t mark it ready', humanError(e), 'fail')
    }
  }
  const start = (t: Ticket) => (t.assignee ? void go(t) : setAsk(t))
  const modal = ask ? (
    <FormModal
      caps="ticket"
      title="Who should do it?"
      intro="A ready ticket needs someone to do it: a bot, or a team whose lead or routing picks one."
      fields={[{ k: 'who', type: 'enum', req: true, label: 'Assign to', options: whoOptions(n).filter(([v]) => v) }]}
      submit="Assign and mark ready"
      testid="tk-ready-form"
      onSubmit={async (v) => {
        if (!v.who) return 'Pick a bot or a team.'
        await go(ask, v.who)
      }}
      onClose={() => setAsk(null)}
    />
  ) : null
  return [start, modal]
}

// TicketAct: a row's one real next action (a draft is marked ready; a ticket
// waiting on you opens what its bot asks).
export function TicketAct({ t, onReady, review }: { t: Ticket; onReady: (t: Ticket) => void; review?: ProposalRow }): JSX.Element | null {
  if (t.status === 'waiting' && review) {
    const open = (e: Event) => {
      e.preventDefault()
      e.stopPropagation()
      location.hash = `#/bots/${review.bot}/proposals/${review.proposal}`
    }
    return (
      <span class="btn sm secondary" role="link" tabIndex={0} data-testid="tk-review" onClick={open} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && open(e)}>
        Review
      </span>
    )
  }
  if (t.status !== 'draft') return null
  const act = (e: Event) => {
    e.preventDefault()
    e.stopPropagation()
    onReady(t)
  }
  return (
    <span class="btn sm secondary" role="button" tabIndex={0} data-testid="tk-ready" onClick={act} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && act(e)}>
      Mark ready…
    </span>
  )
}

// TicketRow: the same anatomy as every list (mark, title, a line in words, the
// facts, the age, the action).
export function TicketRow({ t, n, statusFact, base = '#/tickets', noAssignee, onReady, review }: { t: Ticket; n: Names; statusFact?: boolean; base?: string; noAssignee?: boolean; onReady: (t: Ticket) => void; review?: ProposalRow }): JSX.Element {
  const facts = (statusFact ? [{ glyph: <StatusMark s={statusMark(t.status)} mkOnly />, icon: '', value: statusWord(t.status), pri: 1, label: 'Status' } as Fact] : []).concat(ticketFacts(t, n, { noAssignee }))
  return (
    <li class="dl-need tk-row ticket-row" data-s={statusMark(t.status)} data-t={t.ticket} data-status={t.status}>
      <a class="dl-need-a" href={`${base}/${t.ticket}`}>
        <span class="dl-ng"><StatusMark s={statusMark(t.status)} lg mkOnly label={statusWord(t.status)} /></span>
        <span class="dl-nt">
          <b>{t.title}</b>
          <span class="dl-why">{statusFact ? cap(by(t, n)) : `${statusWord(t.status)} · ${by(t, n)}`}</span>
          <FactsLine facts={facts} />
        </span>
        <span class="dl-age">{agoText(t.created)}</span>
        <span class="dl-na"><TicketAct t={t} onReady={onReady} review={review} /></span>
      </a>
    </li>
  )
}

// histWord: a history line in words ("Marked ready by you").
export function histWord(h: { by: string; what: string }, n: Names): string {
  const a = actorWord(h.by, n)
  const w = h.what
  const name = (x: string) => {
    if (!x || x === 'none' || x === '—') return 'nobody'
    const v = who(x)
    return v.kind === 'team' ? `team ${v.name}` : v.kind === 'bot' ? nameOf(n.bots[v.name]?.identity, v.name) : x
  }
  let m: RegExpMatchArray | null
  if ((m = w.match(/^created \((.*), draft\)$/))) return `Drafted by ${a}${m[1] && !/^(none|unassigned)$/.test(m[1]) ? `, for ${name(m[1])}` : ''}`
  if ((m = w.match(/^created \((\w+)\)$/))) return `${m[1] === 'draft' ? 'Drafted' : 'Opened'} by ${a}${m[1] === 'draft' || m[1] === 'ready' ? '' : `, ${statusWord(m[1]).toLowerCase()}`}`
  if ((m = w.match(/^migrated from a case \((\w+) → (\w+)\)$/))) return `Opened by ${a}, before tickets had stages`
  if ((m = w.match(/^created ready and picked up by (.*)$/))) return `Opened by ${a} and picked up by ${name(m[1])}`
  if ((m = w.match(/^assigned (.*) → (.*)$/))) return m[2] === 'none' || m[2] === '' ? `Unassigned by ${a}` : `Assigned to ${name(m[2])} by ${a}`
  if ((m = w.match(/^(\w+) → (\w+)(?:: (.*))?$/))) {
    const [, from, to, note] = m
    const tail = note && note !== 'picked up' ? `: ${note}` : ''
    if (to === 'in_progress' && from === 'ready') return `Picked up by ${a}`
    if (to === 'ready' && from === 'draft') return `Marked ready by ${a}${tail}`
    if (to === 'ready') return `Reopened by ${a}${tail}`
    if (to === 'draft' && (from === 'done' || from === 'declined')) return `Reopened as a draft by ${a}${tail}`
    if (to === 'draft') return `Moved back to draft by ${a}${tail}`
    if (to === 'waiting') return `Waiting on you: ${a} asked${tail}`
    if (to === 'in_progress') return `Back in progress${tail}`
    if (to === 'done') return `Marked done by ${a}${tail}`
    if (to === 'declined') return `Declined by ${a}${tail}`
  }
  if ((m = w.match(/^edited: (.*)$/))) return `Edited by ${a}: ${m[1]}`
  if (/^spend limit held/.test(w)) return 'Spending limit held until you confirm it again'
  if ((m = w.match(/^spend limit (.*)$/))) return `Spending limit ${m[1].replace(/confirmed for bot:(\S+)/, (_x, b: string) => `confirmed for ${name('bot:' + b)}`)} (${a})`
  return cap(w) + (a ? ` (${a})` : '')
}

// TicketGlyph is a ticket stub: the empty state's picture.
export function TicketGlyph(): JSX.Element {
  return (
    <svg width="64" height="44" viewBox="0 0 64 44" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round">
      <path d="M4 6h56v10a6 6 0 0 0 0 12v10H4V28a6 6 0 0 0 0-12z" />
      <path d="M44 8v28" stroke-dasharray="3 4" opacity="0.55" />
      <path d="M14 18h20M14 26h14" opacity="0.55" />
    </svg>
  )
}

// useNames loads the bots and teams a ticket screen names.
export function loadNames(set: (n: Names) => void): void {
  botsApi
    .bots()
    .then((r) => {
      const bots: Record<string, BotSummary> = {}
      for (const b of r.bots) bots[b.bot] = b
      const teams: Record<string, TeamSummary> = {}
      for (const t of r.teams) teams[t.team] = t
      set({ bots, teams })
    })
    .catch(() => {
      /* names fall back to ids */
    })
}

// pendingByCase: each ticket's pending approval, for its Review act.
export function pendingByCase(ps: ProposalRow[]): Record<string, ProposalRow> {
  const out: Record<string, ProposalRow> = {}
  for (const p of ps) if (p.status === 'pending' && p.case && !out[p.case]) out[p.case] = p
  return out
}

// ALL_STATUSES asks the ticket list for every ticket (with no status the
// daemon leaves out done and declined ones).
export const ALL_STATUSES = ORDER.join(',')
