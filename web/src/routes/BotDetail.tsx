// A bot's page (design §4.4): the header (home session, mode, sandbox,
// operators), then its tabs — Chat, Tickets, Approvals, Memory, What it can do,
// and Settings (its definition and face). Settings is where you give a bot a
// face: the identity picker stages the change through the config pipeline and the
// new face shows everywhere at once.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { botsApi, displayName, type BotDetail as BotData, type BotSummary, type ProposalRow, type Ticket } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { ChatThread } from '../components/ChatThread'
import { IdentityPicker, emptyChoice, type IdentityChoice } from '../components/IdentityPicker'
import { ReviewPanel } from '../components/ReviewPanel'
import { TicketTable } from './Tickets'
import { staged, loadConfig, stage, cfgError } from '../configStore'
import { go } from '../router'

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
  const [err, setErr] = useState('')

  useEffect(() => {
    botsApi.bot(name).then(setBot).catch((e) => setErr(String(e)))
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setAll(m)
    })
  }, [name])

  if (err) return <div class="col"><div class="banner err">{err}</div></div>
  if (!bot) return <div class="col"><p class="muted">Loading {name}…</p></div>

  return (
    <div class="col bot-detail" data-bot={name}>
      <a class="back" href="#/bots">← Bots</a>
      <div class="bot-detail-head">
        <Avatar id={bot.identity} size={56} />
        <div class="bot-detail-head-main">
          <h1 class="screen-title serif">
            {displayName(bot.identity, name)} <span class="mono id">{name}</span>
          </h1>
          <p class="muted">{bot.description}</p>
          <p class="facts muted">
            {bot.disabled ? 'archived' : bot.shadow ? 'practice mode' : 'live'}
            {bot.team ? ` · team ${bot.team}` : ' · solo'}
            {bot.sandboxed ? ' · sandbox enforcing' : ''}
            {bot.home_runtime ? ` · ${bot.home_runtime}` : ''}
          </p>
        </div>
        <div class="row-actions">
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

      <div class="tab-body">
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
    <section class="panel">
      <div class="panel-head">
        <span class="panel-title">Tickets assigned to {displayName(bots[name]?.identity, name)}</span>
        <button class="btn ghost sm" type="button" onClick={() => go(`tickets?assignee=bot:${name}&new=1`)}>New ticket</button>
      </div>
      <TicketTable tickets={tickets} bots={bots} />
    </section>
  )
}

function BotApprovals({ name }: { name: string }): JSX.Element {
  const [proposals, setProposals] = useState<ProposalRow[]>([])
  const [err, setErr] = useState('')
  useEffect(() => {
    botsApi.botProposals(name).then((r) => setProposals(r.proposals || [])).catch((e) => setErr(String(e)))
  }, [name])
  if (err) return <div class="banner err">{err}</div>
  const pending = proposals.filter((p) => p.status === 'pending')
  const rest = proposals.filter((p) => p.status !== 'pending')
  return (
    <section class="panel">
      <span class="panel-title">Approvals</span>
      {pending.length === 0 ? <p class="muted">Nothing waiting on you.</p> : null}
      <ul class="proposal-list">
        {pending.map((p) => <ProposalRowView key={p.proposal} p={p} />)}
      </ul>
      {rest.length ? (
        <>
          <h3 class="subhead">Earlier</h3>
          <ul class="proposal-list quiet">
            {rest.map((p) => <ProposalRowView key={p.proposal} p={p} />)}
          </ul>
        </>
      ) : null}
    </section>
  )
}

export function ProposalRowView({ p }: { p: ProposalRow }): JSX.Element {
  const tf = p.terraform
  return (
    <li class="proposal-row" data-proposal={p.proposal} data-status={p.status}>
      <div class="proposal-main">
        <div class="proposal-title">
          <span class={'tag tier-' + p.tier}>{p.tier}</span>
          <span class="mono">{p.uses || p.kind}</span>
          <span class={'tag status-' + p.status}>{p.status}</span>
        </div>
        <p class="proposal-action">{p.action || p.summary}</p>
        {tf ? <p class="facts muted">{tf.repo} · {tf.dir} · +{tf.add} ~{tf.change} -{tf.destroy}</p> : null}
      </div>
      {p.status === 'pending' ? (
        <a class="btn primary sm" href={`#/bots/${p.bot}/proposals/${p.proposal}`} data-testid="review-proposal">Review</a>
      ) : null}
    </li>
  )
}

function BotMemory({ bot }: { bot: BotData }): JSX.Element {
  return (
    <section class="panel">
      <span class="panel-title">Memory</span>
      <p class="muted">The bot's memory, by kind (knowledge, runbooks, watch, log, handoff). It keeps writing its own; your edits land as one commit in your name.</p>
      {bot.memory_repo ? <p class="facts muted">Repo <span class="mono">{bot.memory_repo}</span></p> : null}
      <div class="kind-cards">
        {['Knowledge', 'Runbooks', 'Watch', 'Log', 'Handoff'].map((k) => (
          <div class="kind-card">
            <span class="kind-name">{k}</span>
          </div>
        ))}
      </div>
    </section>
  )
}

function BotAccess({ bot }: { bot: BotData }): JSX.Element {
  const does = bot.capabilities?.does || []
  const asks = bot.capabilities?.asks || []
  return (
    <section class="panel">
      <span class="panel-title">What {displayName(bot.identity, bot.bot)} can do</span>
      <div class="access-cols">
        <div class="access-col does">
          <h3>Does on its own</h3>
          {bot.shadow ? <p class="muted">Nothing — it's in practice mode, so it only suggests.</p> : does.length ? <ul>{does.map((v) => <li class="mono">{v}</li>)}</ul> : <p class="muted">Nothing yet.</p>}
        </div>
        <div class="access-col asks">
          <h3>Asks you first</h3>
          {asks.length ? <ul>{asks.map((v) => <li class="mono">{v}</li>)}</ul> : <p class="muted">Nothing.</p>}
        </div>
        <div class="access-col never">
          <h3>Never</h3>
          <p class="muted">Anything it isn't granted.</p>
        </div>
      </div>
      <p class="muted tier-ladder">Tiers: observe &lt; reversible &lt; propose &lt; commit money &lt; never.</p>
    </section>
  )
}

function BotSettings({ bot }: { bot: BotData }): JSX.Element {
  const start = useMemo<IdentityChoice>(() => {
    const a = bot.identity?.avatar
    const base = emptyChoice()
    if (a?.icon) return { ...base, mode: 'icon', icon: a.icon, color: a.color || base.color }
    if (a?.shape) return { ...base, mode: 'face', shape: a.shape, eyes: a.eyes || base.eyes, color: a.color || base.color }
    return { ...base, color: bot.identity?.color || base.color }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bot.bot])
  const [choice, setChoice] = useState<IdentityChoice>(start)

  useEffect(() => { void loadConfig() }, [])

  async function stageFace() {
    await stage(['bots', bot.bot, 'avatar', 'color'], choice.color, `${bot.bot}: its face`)
    if (choice.mode === 'face') {
      await stage(['bots', bot.bot, 'avatar', 'shape'], choice.shape, `${bot.bot}: its face`)
      await stage(['bots', bot.bot, 'avatar', 'eyes'], choice.eyes, `${bot.bot}: its face`)
    } else {
      await stage(['bots', bot.bot, 'avatar', 'icon'], choice.icon, `${bot.bot}: its face`)
    }
  }

  return (
    <section class="panel bot-settings">
      <span class="panel-title">Settings</span>
      <div class="form-field">
        <span>Its face</span>
        <IdentityPicker value={choice} onChange={setChoice} />
        <div class="row-actions">
          <button class="btn" type="button" data-testid="stage-face" onClick={() => void stageFace()}>Use this face</button>
        </div>
      </div>

      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <ReviewPanel stagedCount={staged.value.length} />
    </section>
  )
}
