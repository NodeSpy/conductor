// A bot's page (design §4.4): the header (home session, mode, sandbox,
// operators), then its tabs — Chat, Tickets, Approvals, Memory, What it can do,
// and Settings (its definition and face). Settings is where you give a bot a
// face: the identity picker stages the change through the config pipeline and the
// new face shows everywhere at once.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, type BotDetail as BotData, type BotSummary, type ProposalRow, type Ticket } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { ChatThread } from '../components/ChatThread'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { TicketTable } from './Tickets'
import { BotMemory } from './BotMemory'
import { BotAccess } from './BotAccess'
import { BotSettings } from './BotSettings'
import { go } from '../router'
import { ApiError, humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

const TABS = ['chat', 'tickets', 'approvals', 'memory', 'access', 'settings'] as const
type Tab = (typeof TABS)[number]
const TAB_LABEL: Record<Tab, string> = {
  chat: 'Chat',
  tickets: 'Tickets',
  approvals: 'Approvals',
  memory: 'Memory',
  access: 'What it can do',
  settings: 'Settings',
}

export function BotDetail({ name, tab }: { name: string; tab?: string }): JSX.Element {
  const active: Tab = (TABS as readonly string[]).includes(tab || '') ? (tab as Tab) : 'chat'
  const [bot, setBot] = useState<BotData | null>(null)
  const [all, setAll] = useState<Record<string, BotSummary>>({})
  const [err, setErr] = useState<unknown>(null)

  useEffect(() => {
    setBot(null)
    setErr(null)
    botsApi.bot(name).then(setBot).catch(setErr)
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setAll(m)
    })
  }, [name])

  if (err) {
    // A bot that doesn't exist (a stale link, a typo'd id) is a dead end to
    // name and route back from, not a raw error banner.
    if (err instanceof ApiError && err.status === 404) {
      return (
        <div class="col">
          <EmptyState title={`No bot named ${name}`} action={{ label: 'Bots', href: '#/bots' }}>
            <p>It may have been archived, renamed, or never existed on this daemon.</p>
          </EmptyState>
        </div>
      )
    }
    return <div class="col"><a class="back" href="#/bots">← Bots</a><div class="banner err" role="alert">{humanError(err)}</div></div>
  }
  if (!bot) return <div class="col"><p class="muted">Loading {name}…</p></div>

  const facts: string[] = []
  if (bot.disabled) facts.push('archived')
  else if (bot.shadow) facts.push('practice mode')
  facts.push(bot.team ? `team ${bot.team}` : 'solo')
  if (bot.sandboxed) facts.push('sandbox enforcing')
  if (bot.home_runtime) facts.push(bot.home_runtime)

  return (
    <div class="col" data-bot={name}>
      <a class="back" href="#/bots">← Bots</a>
      <div class="dl-bothead">
        <Avatar id={bot.identity} size={56} />
        <div>
          <h1>{displayName(bot.identity, name)} <span class="mono id">{name}</span></h1>
          {bot.description ? <p class="dl-lead">{bot.description}</p> : null}
          <span class="fx">{facts.map((f) => <span class="fx-i">{f}</span>)}</span>
        </div>
        <div class="dl-actions tight ph-hide">
          <a class="btn ghost sm" href={`#/chat/bot:${name}`}>Open in Chat</a>
        </div>
      </div>

      <nav class="tabs" role="tablist" aria-label="Bot">
        {TABS.map((t) => (
          <a class={'tab' + (active === t ? ' on' : '')} role="tab" aria-selected={active === t} href={`#/bots/${name}/${t}`} data-tab={t}>
            {TAB_LABEL[t]}
          </a>
        ))}
      </nav>

      <div class="dl-tab-body">
        {active === 'chat' && <ChatThread convId={`bot:${name}`} kind="bot" bots={all} />}
        {active === 'tickets' && <BotTickets name={name} bots={all} />}
        {active === 'approvals' && <BotApprovals name={name} />}
        {active === 'memory' && <BotMemory bot={bot} />}
        {active === 'access' && <BotAccess bot={bot} />}
        {active === 'settings' && <BotSettings bot={bot} />}
      </div>
    </div>
  )
}

function BotTickets({ name, bots }: { name: string; bots: Record<string, BotSummary> }): JSX.Element {
  const [tickets, setTickets] = useState<Ticket[]>([])
  useEffect(() => {
    botsApi.tickets({ assignee: `bot:${name}` }).then((r) => setTickets(r.tickets || [])).catch(() => setTickets([]))
  }, [name])
  return (
    <section class="tk-in">
      <header class="dl-ch tk-inh">
        <p class="dl-sub">Tickets assigned to {displayName(bots[name]?.identity, name)}: from you, a chat, an event or its own checks.</p>
        <span class="dl-sp" />
        <button class="btn sm secondary" type="button" onClick={() => go(`tickets?assignee=bot:${name}&new=1`)}><Icon name="plus" />New ticket for it</button>
      </header>
      <TicketTable tickets={tickets} bots={bots} />
    </section>
  )
}

function BotApprovals({ name }: { name: string }): JSX.Element {
  const [proposals, setProposals] = useState<ProposalRow[]>([])
  const [err, setErr] = useState('')
  useEffect(() => {
    botsApi.botProposals(name).then((r) => setProposals(r.proposals || [])).catch((e) => setErr(humanError(e)))
  }, [name])
  if (err) return <div class="banner err">{err}</div>
  const pending = proposals.filter((p) => p.status === 'pending')
  const rest = proposals.filter((p) => p.status !== 'pending')
  return (
    <section class="dl-card">
      <header class="dl-ch"><h2>Approvals</h2></header>
      {pending.length === 0 ? <p class="dl-sub">Nothing waiting on you.</p> : (
        <ul class="dl-list dl-needs">
          {pending.map((p) => <ProposalRowView key={p.proposal} p={p} />)}
        </ul>
      )}
      {rest.length ? (
        <>
          <header class="dl-ch" style={{ marginTop: '18px' }}><h3>Earlier</h3></header>
          <ul class="dl-list dl-needs past-d">
            {rest.map((p) => <ProposalRowView key={p.proposal} p={p} />)}
          </ul>
        </>
      ) : null}
    </section>
  )
}

export function ProposalRowView({ p }: { p: ProposalRow }): JSX.Element {
  const tf = p.terraform
  const pending = p.status === 'pending'
  return (
    <li class="dl-need" data-proposal={p.proposal} data-status={p.status}>
      <a class="dl-need-a" href={pending ? `#/bots/${p.bot}/proposals/${p.proposal}` : undefined} data-testid={pending ? 'review-proposal' : undefined}>
        <span class="dl-ng">
          <StatusMark s={pending ? 'ember' : p.status === 'denied' ? 'fail' : 'ok'} mkOnly lg label={p.status} />
        </span>
        <span class="dl-nt">
          <b>{p.summary || p.uses || p.kind}</b>
          <span class="dl-why">
            tier {p.tier}{tf ? ` · ${tf.repo} · +${tf.add} ~${tf.change} -${tf.destroy}` : ''}
          </span>
        </span>
        <span class="dl-age" />
        <span class="dl-na">{pending ? <span class="btn sm secondary">Review</span> : <span class="fx-i">{p.status}</span>}</span>
      </a>
    </li>
  )
}

