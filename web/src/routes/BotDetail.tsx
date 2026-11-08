// A bot's page (prototype js/screens/bots.js hero/shell, chat, proposals,
// workers): crumbs, the head (avatar, name, what it's for, what it's working
// on, its status and the ⋯ menu: Edit as YAML, Rotate session, Sweep now,
// Deliver an event, Workers), then its tabs in the design's order — Approvals
// (the default when something waits on you), Chat, Tickets, Memory, What it
// can do, Settings. Chat is the Home session panel (thread + composer) with
// what waits for you and what it's working on beside it; Approvals is what
// waits for you, then past decisions folded. The ⋯ menu's actions are the
// operator API's (internal/bot/httpapi_botops.go), the same ops the CLI runs.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import {
  botsApi,
  botPageApi,
  displayName,
  type BotDetail as BotData,
  type BotPageWorker,
  type BotSummary,
  type ProposalRow,
  type Ticket,
} from '../api/bots'
import { api } from '../api/client'
import { authState } from '../api/auth'
import { Avatar } from '../components/Avatar'
import { ChatThread } from '../components/ChatThread'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FactsLine, type Fact } from '../components/Facts'
import { Menu, type MenuItem } from '../components/Menu'
import { ConfirmModal, FormModal } from '../components/FormModal'
import { ReviewPanel } from '../components/ReviewPanel'
import { stage, staged } from '../configStore'
import { Tickets } from './Tickets'
import { BotMemory } from './BotMemory'
import { BotAccess } from './BotAccess'
import { BotSettings } from './BotSettings'
import { go } from '../router'
import { useFitChat } from '../lib/fitChat'
import { ApiError, humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { TabsBar } from '../components/TabsBar'
import { loadVerbs, type BotVerb } from './botConfig'
import { eventRows, type NbConnection } from './newbotModel'
import '../styles/bots-page.css'

// The read model's extra fields a bot's page reads (internal/bot statusOf).
export interface BotPage extends BotData {
  home?: { session: string; agent: string; cwd?: string; started: string; turns: number }
  proactive?: { every: string; notify_at?: string; last_sweep?: { id: string; status: string; started: string; summary?: string } }
  peers?: string[]
}
export type ProposalRowD = ProposalRow & { approver?: string; decided?: string; reason?: string }

const TABS = ['proposals', 'chat', 'tickets', 'memory', 'access', 'settings'] as const
type Tab = (typeof TABS)[number] | 'workers'
const TAB_LABEL: Record<(typeof TABS)[number], string> = {
  proposals: 'Approvals',
  chat: 'Chat',
  tickets: 'Tickets',
  memory: 'Memory',
  access: 'What it can do',
  settings: 'Settings',
}

/* ---------- facts in words (prototype js/ui.js actionOf / actionFact / envFact) ---------- */

type ActKind = 'read' | 'post' | 'push' | 'change' | 'infra' | 'money' | 'destroy' | 'permission'
export function actionOf(verb = '', hint = ''): ActKind {
  const v = String(verb).toLowerCase() + ' ' + String(hint).toLowerCase()
  if (/purchase|buy|savings_plan|reserve|spend|budget\.raise/.test(v)) return 'money'
  if (/destroy|delete|drop|\.rm\b|remove|purge|revoke/.test(v)) return 'destroy'
  if (/grant|permission|operators|access|widen|allow_secrets|autonomy|sandbox|egress/.test(v)) return 'permission'
  if (/tf-apply|terraform apply|\.apply\b|deploy|rollout|migrate/.test(v)) return 'infra'
  if (/push|commit|merge|git\./.test(v)) return 'push'
  if (/comment|post|reply|review|message|send|notify|page\b/.test(v)) return 'post'
  if (/\.get\b|get_|list|read|search|logs|diff|plan\b|tf-plan|fetch|query|status|watch/.test(v)) return 'read'
  return 'change'
}
const ACT_WORD: Record<ActKind, string> = {
  read: 'Reads',
  post: 'Posts a message',
  push: 'Pushes code',
  change: 'Changes a setting',
  infra: 'Changes infrastructure',
  money: 'Spends money',
  destroy: 'Deletes things',
  permission: 'Changes permissions',
}
const ACT_TONE: Record<ActKind, string | undefined> = { read: undefined, post: 'change', push: 'change', change: 'change', infra: 'warning', money: 'danger', destroy: 'danger', permission: 'danger' }

// The verb a proposal runs and what it carries (a command's text hints at what it does).
function hintOf(p: ProposalRow): string {
  const o = p.options || {}
  return [o.command, o.action, o.method].filter((x) => typeof x === 'string').join(' ')
}
// A plugin's verb description ("run one command") said of the bot: "Runs one command".
const VERB_S: Record<string, string> = { run: 'Runs', post: 'Posts', send: 'Sends', create: 'Creates', delete: 'Deletes', update: 'Updates', read: 'Reads', get: 'Gets', list: 'Lists', open: 'Opens', close: 'Closes', merge: 'Merges', push: 'Pushes', comment: 'Comments', restart: 'Restarts', deploy: 'Deploys', apply: 'Applies', add: 'Adds', set: 'Sets', write: 'Writes', fetch: 'Fetches', search: 'Searches', start: 'Starts', stop: 'Stops', call: 'Calls', make: 'Makes', remove: 'Removes', reply: 'Replies', page: 'Pages' }
function saidOfBot(desc: string): string {
  const d = desc.split(/ \(| — | - |; |\. /)[0].trim()
  const [w, ...rest] = d.split(' ')
  const v = VERB_S[w.toLowerCase()]
  return v ? [v, ...rest].join(' ') : d.replace(/^./, (c) => c.toUpperCase())
}
// What a proposal does, in words with its risk grade: a knowledge promotion
// shares with the team; otherwise the kind of action its verb is, or — where
// that says nothing more than "changes something" — the verb's own declared
// description (GET /api/v1/connections/verbs).
export function actionFact(p: ProposalRow, verbs: BotVerb[] = []): Fact {
  if (p.kind === 'knowledge') return { icon: 'act-push', value: 'Adds to team knowledge', pri: 2, tone: 'change', label: 'What it does' }
  const a = p.terraform ? 'infra' : actionOf(p.uses || p.kind, hintOf(p))
  const d = a === 'change' ? verbs.find((v) => v.verb === p.uses)?.desc : ''
  return { icon: 'act-' + a, value: d ? saidOfBot(d) : ACT_WORD[a], pri: 2, tone: ACT_TONE[a], label: 'What it does' }
}
// useVerbs is the verbs plugins declare, for the action words.
export function useVerbs(): BotVerb[] {
  const [v, setV] = useState<BotVerb[]>([])
  useEffect(() => void loadVerbs().then(setV), [])
  return v
}
// Where it lands: a Terraform workspace, or an env the action names.
export function envOf(p: ProposalRow): string {
  const o = p.options || {}
  const e = p.terraform?.workspace || (typeof o.env === 'string' ? o.env : '') || (typeof o.environment === 'string' ? o.environment : '')
  return /^prod/i.test(e) ? 'production' : e
}
export function envFact(p: ProposalRow): Fact | null {
  const e = envOf(p)
  return e ? { icon: 'server', value: e, pri: 1, tone: e === 'production' ? 'warning' : undefined, label: 'Environment' } : null
}

// A knowledge promotion's parts: the file it shares, the team, and the bot's note.
function promotion(p: ProposalRow): { file: string; team: string; note: string } {
  const o = (p.options || {}) as Record<string, unknown>
  const to = String(o.to || o.from || '')
  const note = (/ — note: (.*)$/.exec(p.action || '') || [])[1] || ''
  return { file: to.split('/').pop() || to, team: String(o.team || ''), note: note.trim() }
}
// A proposal's title in words: what it does, never the raw verb line.
export function proposalTitle(p: ProposalRow): string {
  if (p.kind === 'knowledge') {
    const k = promotion(p)
    return `Share “${k.file}” with ${k.team ? `team ${k.team}` : 'its team'}`
  }
  return p.summary || p.uses || p.kind
}
// Why it asks, in a sentence (the row's second line): a promotion in the bot's
// own note; anything else, who asked for it.
export function proposalWhy(p: ProposalRow, nm: string): string {
  if (p.kind === 'knowledge') {
    const k = promotion(p)
    const t = k.team ? `team ${k.team}` : 'its team'
    return k.note ? `${nm} wants to share what it learned with ${t}: “${k.note.replace(/[.!?]\s*$/, '')}”.` : `${nm} wants to share what it learned with ${t}.`
  }
  return originText(nm, p.requester, p.requester_class)
}

// Who asked for a proposal, in a sentence (also the approval page's origin line).
// nm is the bot's display name.
export function originText(nm: string, requester?: string, cls?: string): string {
  const me = authState.value.operator || ''
  const r = (requester || '').replace(/^operator:/, '').replace(/^\(?unknown\)?$/, '')
  if (cls === 'event') return requester ? `Started by an event (${requester.replace(/^event:/, '')}), not a person.` : 'Started by an event, not a person.'
  if (cls === 'user') return `Asked for in chat by someone who isn't one of your operators${requester ? ` (${requester})` : ''}; it runs with no more trust than they have.`
  if (cls === 'proactive') return `Found on ${nm}'s own routine check; nobody asked.`
  if (cls === 'peer') return requester ? `Asked for by another bot (${requester.replace(/^bot:/, '')}), carrying the original requester's trust.` : "Asked for by another bot, carrying the original requester's trust."
  if (cls === 'operator') return !r || r === me ? (r ? 'Asked for by you.' : 'Asked for by an operator.') : `Asked for by ${r}, an operator.`
  return `${nm} asked for it on its own; nobody asked it to.`
}

// Who asked for a ticket, in words: you, an event, someone in chat, its own check, another bot.
function askedBy(t: Ticket): string {
  const me = authState.value.operator || ''
  const by = t.created_by || ''
  if (/^operator:/.test(by)) return by === `operator:${me}` ? 'you' : by.slice(9)
  if (t.requester_class === 'operator') return t.requester && t.requester !== me ? t.requester : 'you'
  if (t.requester_class === 'event') return 'an event'
  if (t.requester_class === 'user') return 'someone in chat'
  if (t.requester_class === 'proactive') return 'its own routine check'
  if (t.requester_class === 'peer') return `${(t.requester || 'a bot').replace(/^bot:/, '')} (another bot)`
  return t.requester || 'someone'
}

// Times as the prototype says them: "23h 52m" left before it expires (UI's
// expires), and "5 min ago" / "yesterday" / "3 days ago" for an age (UI.agoText).
export function expiresIn(iso?: string): string {
  if (!iso) return ''
  const ms = new Date(iso).getTime() - Date.now()
  if (Number.isNaN(ms) || ms <= 0) return ''
  const m = Math.floor(ms / 60000), h = Math.floor(m / 60), d = Math.floor(h / 24)
  return d ? `${d}d ${h % 24}h` : h ? `${h}h ${m % 60}m` : `${Math.max(1, m)}m`
}
export function agoText(iso?: string): string {
  if (!iso) return ''
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (Number.isNaN(s)) return ''
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  return d === 1 ? 'yesterday' : `${d} days ago`
}

// The events a bot can watch, from what each connection's plugin declares: a
// fixed event as itself; a dynamic one by the names seen so far, plus one row to
// name another ("<conn>.*<i>", which asks for the name).
function watchOptions(conns: NbConnection[]): [string, string][] {
  const out: [string, string][] = []
  for (const r of eventRows(conns)) {
    if (!r.ev.dynamic) { out.push([r.key, `${r.conn.name} · ${r.label}`]); continue }
    for (const n of r.ev.seen || []) out.push([`${r.conn.name}.${n}`, `${r.conn.name} · ${n}`])
    out.push([r.key, `${r.conn.name} · ${r.label} (name it)`])
  }
  return out
}

const OPEN = (t: Ticket) => t.status !== 'done' && t.status !== 'declined'

/* ---------- the page ---------- */

export function BotDetail({ name, tab }: { name: string; tab?: string }): JSX.Element {
  const [bot, setBot] = useState<BotPage | null>(null)
  const [all, setAll] = useState<Record<string, BotSummary>>({})
  const [proposals, setProposals] = useState<ProposalRowD[] | null>(null)
  const [tickets, setTickets] = useState<Ticket[]>([])
  const [triggers, setTriggers] = useState<unknown[] | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [menuAt, setMenuAt] = useState<HTMLElement | null>(null)
  const [modal, setModal] = useState<'rotate' | 'deliver' | 'watch' | 'paseo' | ''>('')
  const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)
  const [watchOpts, setWatchOpts] = useState<[string, string][] | null>(null)
  useEffect(() => {
    if (modal !== 'watch' || watchOpts) return
    fetch('/api/v1/newbot/connections', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
      .then((r) => (r.ok ? (r.json() as Promise<{ connections: NbConnection[] }>) : { connections: [] }))
      .then((r) => setWatchOpts(watchOptions(r.connections || [])))
      .catch(() => setWatchOpts([]))
  }, [modal])

  const reload = () => {
    botsApi.bot(name).then((b) => setBot(b as BotPage)).catch(setErr)
    botsApi.botProposals(name).then((r) => setProposals(r.proposals || [])).catch(() => setProposals([]))
    botsApi.tickets({ assignee: `bot:${name}`, status: 'draft,ready,in_progress,waiting' }).then((r) => setTickets(r.tickets || [])).catch(() => setTickets([]))
  }
  useEffect(() => {
    setBot(null)
    setErr(null)
    setNote(null)
    setProposals(null)
    reload()
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setAll(m)
    }).catch(() => {})
    api.config().then((c) => setTriggers(((c.tree as Record<string, unknown>)?.triggers as unknown[]) || [])).catch(() => setTriggers(null))
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
    return <div class="bot-page"><nav class="dl-crumbs" aria-label="Where you are"><a href="#/bots">Bots</a></nav><div class="banner err" role="alert">{humanError(err)}</div></div>
  }
  if (!bot) return <div class="bot-page"><p class="muted">Loading {name}…</p></div>

  const pending = (proposals || []).filter((p) => p.status === 'pending')
  const pendingN = proposals ? pending.length : bot.pending_proposals
  // #/bots/<n> opens on Approvals while something waits on you, else Chat;
  // #/bots/<n>/approvals is the same tab as the design's /proposals.
  const t0 = tab === 'approvals' ? 'proposals' : tab
  const active: Tab = t0 === 'workers' ? 'workers' : (TABS as readonly string[]).includes(t0 || '') ? (t0 as Tab) : pendingN > 0 ? 'proposals' : 'chat'
  const nm = displayName(bot.identity, name)
  const open = tickets.filter(OPEN)

  // What it watches: a trigger that hands events to it (or its team), or its own sweeps.
  const watched =
    !!bot.proactive ||
    (triggers || []).some((t) =>
      (((t as { steps?: unknown[] }).steps as { bot?: { name?: string; team?: string; action?: string } }[]) || []).some(
        (s) => s?.bot && (!s.bot.action || s.bot.action === 'deliver') && (s.bot.name === name || (!!bot.team && s.bot.team === bot.team)),
      ),
    )
  // Not watching anything: said quietly in the head (where the prototype says what it's doing), not as a banner.
  const watchNote =
    triggers !== null && !watched ? (
      <p class="bot-working bot-unwatched" data-testid="watch-empty">
        <span class="bu-why">Watches nothing yet<span class="bu-long">: it only answers when asked</span></span>
        <span class="bu-sep"> · </span>
        <button class="link" type="button" onClick={() => setModal('watch')}>Choose what it watches</button>
      </p>
    ) : null

  const menuItems: MenuItem[] = [
    { label: 'Edit as YAML…', icon: 'code', onClick: () => go(`bots/${encodeURIComponent(name)}/settings?yaml=1`) },
    '-',
    { label: 'Rotate session…', icon: 'updates', onClick: () => setModal('rotate') },
    {
      label: 'Sweep now',
      icon: 'eye',
      disabled: bot.proactive ? false : 'It has no proactive: block in its definition',
      onClick: () => {
        botPageApi
          .sweep(name)
          .then(() => {
            setNote({ ok: true, text: `${nm} is checking in now. It only reads; it uses the team's budget.` })
            go(`bots/${encodeURIComponent(name)}/workers`)
          })
          .catch((e) => setNote({ ok: false, text: `Couldn't start a sweep: ${humanError(e)}` }))
      },
    },
    { label: 'Deliver an event…', icon: 'send', onClick: () => setModal('deliver') },
    '-',
    { label: 'Workers', icon: 'workflows', onClick: () => go(`bots/${encodeURIComponent(name)}/workers`) },
  ]

  const tabCount: Partial<Record<(typeof TABS)[number], [number, boolean]>> = { proposals: [pendingN, true], tickets: [bot.open_cases || open.length, false] }

  return (
    <div class={'bot-page' + (active === 'tickets' ? ' tk-page' : '')} data-bot={name}>
      <Hero bot={bot} name={name} tickets={open} watch={watchNote} sub={active === 'workers' ? 'Workers' : ''} onMore={(el) => setMenuAt(menuAt ? null : el)} />
      {menuAt ? <Menu anchor={menuAt} items={menuItems} onClose={() => setMenuAt(null)} label={`More for ${name}`} /> : null}
      {note ? (
        <div class={'banner ' + (note.ok ? 'ok' : 'err')} role={note.ok ? 'status' : 'alert'} data-testid="bot-note">
          {note.text} <button class="link" type="button" onClick={() => setNote(null)}>Dismiss</button>
        </div>
      ) : null}

      {active !== 'workers' ? (
        <TabsBar
          className="dl-tabs"
          ariaLabel="Bot"
          active={active}
          items={TABS.map((t) => {
            const c = tabCount[t]
            return { id: t, label: TAB_LABEL[t], href: `#/bots/${name}/${t}`, count: c && c[0] ? c[0] : null, hot: !!(c && c[1]) }
          })}
        />
      ) : null}

      <div class="dl-tab-body">
        {active === 'proposals' && <Approvals name={name} nm={nm} list={proposals} />}
        {active === 'chat' && <ChatTab name={name} nm={nm} bot={bot} all={all} pending={pending} open={open} onPaseo={() => setModal('paseo')} />}
        {active === 'tickets' && <Tickets scope={`bot:${name}`} />}
        {active === 'memory' && <BotMemory bot={bot} />}
        {active === 'access' && <BotAccess bot={bot} />}
        {active === 'settings' && <BotSettings bot={bot} />}
        {active === 'workers' && <Workers name={name} nm={nm} proposals={proposals || []} />}
        {staged.value.some((c) => c.path[0] === 'triggers') && ['chat', 'settings', 'access'].includes(active) ? <ReviewPanel stagedCount={staged.value.length} /> : null}
      </div>

      {modal === 'rotate' ? (
        <ConfirmModal
          title="Rotate the home session?"
          ok="Rotate"
          body={
            <>
              <p style={{ margin: '0 0 8px' }}>conductor asks the current session for a hand-off note, waits for that turn to settle, launches a fresh session, and only then archives the old one. Its token is revoked at once.</p>
              <dl class="kv">
                <dt>current</dt>
                <dd class="mono">{bot.home ? `${bot.home.session} · ${bot.home.turns} turns` : 'none running'}</dd>
                <dt>next</dt>
                <dd class="mono">a fresh session · resumes from its hand-off note + ledger</dd>
              </dl>
            </>
          }
          onOk={async () => {
            try {
              const before = bot.home?.session
              const r = (await botPageApi.rotate(name)) as unknown as BotPage
              setNote({ ok: true, text: before ? `Rotated: ${before} archived · ${r.home?.session || 'a new session'} live.` : `Rotated: ${r.home?.session || 'a new session'} is live.` })
              reload()
            } catch (e) {
              setNote({ ok: false, text: `Couldn't rotate the session: ${humanError(e)}` })
            }
          }}
          onClose={() => setModal('')}
        />
      ) : null}
      {modal === 'deliver' ? (
        <FormModal
          caps={`bot ${name}`}
          title={`Deliver an event to ${name}`}
          testid="deliver-form"
          fields={[
            { k: 'title', type: 'string', req: true, mono: false, ph: 'Investigate elevated 5xx on /refund' },
            { k: 'text', type: 'text', label: 'Text (reaches the bot fenced as untrusted data)' },
            { k: 'requester', type: 'enum', req: true, options: [['operator', `operator:${authState.value.operator || 'you'} (you)`], ['event', 'event']], def: 'operator' },
            { k: 'urgent', type: 'seg', label: 'Urgent', options: [['no', 'No'], ['yes', 'Start a worker now']], def: 'no' },
          ]}
          submit="Deliver"
          submitIcon="send"
          onSubmit={async (v) => {
            try {
              const r = await botPageApi.deliver(name, { title: v.title, text: v.text || '', requester: v.requester === 'event' ? 'event' : 'operator', urgent: v.urgent === 'yes' })
              if (r.ticket) go(`tickets/${encodeURIComponent(r.ticket)}`)
              else {
                setNote({ ok: true, text: `Delivered to ${nm}.` })
                reload()
              }
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal('')}
        />
      ) : null}
      {modal === 'watch' && watchOpts && !watchOpts.length ? (
        <ConfirmModal
          title={`What should ${nm} watch?`}
          body="No connection delivers events yet. Add a connection whose plugin sends events (Connections), then come back and pick one."
          ok="Open Connections"
          onOk={() => { setModal(''); go('connections') }}
          onClose={() => setModal('')}
        />
      ) : null}
      {modal === 'watch' && watchOpts && watchOpts.length ? (
        <FormModal
          caps="Workflows"
          title={`What should ${nm} watch?`}
          intro="Adds a trigger that hands each matching event to the bot (a bot: step). It reads the event, then tells you or proposes what to do."
          fields={[
            {
              k: 'ev',
              type: 'enum',
              req: true,
              label: 'When this happens',
              options: watchOpts,
              help: 'The events your connections deliver, as each plugin describes them.',
            },
            {
              k: 'dyn',
              type: 'string',
              req: true,
              label: 'Event name',
              mono: true,
              pattern: /^[a-z0-9_.-]+$/i,
              patternMsg: 'Letters, digits, dots, dashes and underscores',
              help: 'This connection takes events you name yourself.',
              show: (a) => /\.\*\d+$/.test(a.ev || ''),
            },
          ]}
          submit="Review change"
          submitIcon="check"
          onSubmit={async (v) => {
            v = { ...v, on: /\.\*\d+$/.test(v.ev) ? `${v.ev.split('.')[0]}.${v.dyn.trim()}` : v.ev }
            const tn = `${name}-${v.on.split('.').slice(1).join('-').replace(/[_.]/g, '-')}`.toLowerCase()
            const next = [...((triggers as unknown[]) || []), { name: tn, on: v.on, steps: [{ id: 'tell', bot: { name, title: '{{.title}}' } }] }]
            try {
              await stage(['triggers'], next, `trigger ${tn} hands ${v.on} to ${name}`)
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal('')}
        />
      ) : null}
      {modal === 'paseo' && bot.home ? <PaseoModal name={name} agent={bot.home.agent} onClose={() => setModal('')} /> : null}
    </div>
  )
}

/* ---------- the head ---------- */

function Hero({ bot, name, tickets, watch, sub, onMore }: { bot: BotPage; name: string; tickets: Ticket[]; watch?: JSX.Element | null; sub: string; onMore: (el: HTMLElement) => void }): JSX.Element {
  const nm = displayName(bot.identity, name)
  const role = bot.description || (bot.team ? `A bot on the ${bot.team} team` : 'A bot')
  // working on: a ticket in progress, never one that's only ready
  const cur = tickets.find((t) => t.status === 'in_progress')
  const live = !!bot.home
  const now = bot.shadow ? (
    <StatusMark s="shadow" word="Practice mode" />
  ) : bot.live_workers > 0 ? (
    <StatusMark s="run" word="Working" />
  ) : live ? (
    <span class="dl-sub bot-live">Live</span>
  ) : (
    <StatusMark s="idle" word="Parked" />
  )
  return (
    <>
      <nav class="dl-crumbs" aria-label="Where you are">
        <a href="#/bots">Bots</a>
        {bot.team ? <><span class="sep" aria-hidden="true">›</span><a href={`#/teams/${bot.team}`}>{bot.team}</a></> : null}
        <span class="sep" aria-hidden="true">›</span>
        {sub ? <a href={`#/bots/${name}`}>{nm}</a> : <span class="cur" aria-current="page">{nm}</span>}
        {sub ? <><span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">{sub}</span></> : null}
      </nav>
      <div class="dl-bothead">
        <span class="bav xl"><Avatar id={bot.identity} size={64} /></span>
        <div>
          <h1>
            {nm}
            {nm !== name ? <> <span class="bav-id h1-id">{name}</span></> : null}
          </h1>
          <p class="dl-lead">{role}</p>
          {cur && (live || bot.live_workers > 0) ? (
            <p class="bot-working">Working on: <a href={`#/tickets/${cur.ticket}`}>{cur.title}</a></p>
          ) : watch || null}
        </div>
        {now}
        <button class="icon-btn" type="button" aria-label={`More for ${name}`} data-testid="bot-more" onClick={(e) => onMore(e.currentTarget as HTMLElement)}>
          <Icon name="more" />
        </button>
      </div>
    </>
  )
}

/* ---------- Open in paseo ---------- */

function PaseoModal({ name, agent, onClose }: { name: string; agent: string; onClose: () => void }): JSX.Element {
  const [copied, setCopied] = useState(false)
  return (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true" tabIndex={-1} data-testid="paseo-modal">
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head"><h2>Open in paseo</h2><button class="icon-btn" type="button" aria-label="Close" onClick={onClose}><Icon name="x" /></button></div>
        <div class="modal-body">
          <p style={{ margin: '0 0 10px' }}>{name}'s home session is a paseo agent. paseo shows all of it: every turn, the files it touched and its terminal. The chat here is the conversation only.</p>
          <p class="dl-sub" style={{ margin: 0 }}>Open paseo on the machine conductor runs on and pick the agent <code>{agent}</code>.</p>
        </div>
        <div class="modal-foot">
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button
            class="btn primary"
            type="button"
            onClick={() => {
              void navigator.clipboard?.writeText(agent).then(() => setCopied(true))
            }}
          >
            <Icon name={copied ? 'check' : 'copy'} />
            {copied ? 'Copied' : 'Copy the agent id'}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ---------- Chat ---------- */

function ChatTab({ name, nm, bot, all, pending, open, onPaseo }: { name: string; nm: string; bot: BotPage; all: Record<string, BotSummary>; pending: ProposalRowD[]; open: Ticket[]; onPaseo: () => void }): JSX.Element {
  useFitChat([name, pending.length, open.length])
  const verbs = useVerbs()
  const head = (
    <div class="panel-head cm-head">
      <h3>Home session</h3>
      <span class="dl-sub cm-rule">Your messages reach it between turns, never mid-turn</span>
      <a class="btn sm secondary" href={`#/chat/bot:${name}`}><Icon name="message" />Open in Chat</a>
      {bot.home ? (
        <button class="btn sm secondary" type="button" data-testid="open-paseo" onClick={onPaseo}><Icon name="external" />Open in paseo</button>
      ) : (
        <button class="btn sm secondary" type="button" data-testid="paseo-none" disabled title="Its home session isn't running, so paseo has nothing to open" aria-label="Open in paseo: its home session isn't running, so paseo has nothing to open"><Icon name="external" />Open in paseo</button>
      )}
    </div>
  )
  return (
    <>
      <p class="cm-side-line">
        {pending.length ? <a class="dl-link" href={`#/bots/${name}/proposals`}><StatusMark s="ember" mkOnly label="Needs you" /> {pending.length} waiting for you</a> : null}
        {open.length ? <a class="dl-link" href={`#/bots/${name}/tickets`}>Working on {open.length}</a> : null}
      </p>
      {/* with nothing waiting and nothing open the side column is empty: the chat takes the width */}
      <div class={'grid chat-grid' + (pending.length || open.length ? ' g-side' : '')}>
        <ChatThread convId={`bot:${name}`} kind="bot" bots={all} cls="fit" head={head} placeholder={`Message ${nm}`} empty={
          <div class="empty cm-hero" data-testid="chat-empty">
            <Icon name="message" class="empty-ic" />
            <h2 class="serif">No conversation with {nm} yet</h2>
            <p>Write below and it answers here, between turns of its work.</p>
          </div>
        } />
        <div class="stack cm-side">
          {pending.length ? (
            <section class="panel cm-need">
              <div class="panel-head"><StatusMark s="ember" word="Waiting for you" /><span class="count hot">{pending.length}</span></div>
              <ul class="cm-side-l">
                {pending.map((p) => (
                  <li>
                    <a class="cm-side-a" href={`#/bots/${name}/proposals/${p.proposal}`}>
                      <b>{proposalTitle(p)}</b>
                      <FactsLine facts={[envFact(p), actionFact(p, verbs)]} />
                      <span class="cm-side-f">
                        <span class="btn sm secondary">Review</span>
                        {expiresIn(p.expires) ? <span class="dl-sub">expires in {expiresIn(p.expires)}</span> : null}
                      </span>
                    </a>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          {open.length ? (
          <section class="panel">
            <div class="panel-head"><h3>Working on</h3><a class="btn sm ghost" style={{ marginLeft: 'auto' }} href={`#/bots/${name}/tickets`}>All tickets</a></div>
            <ul class="cm-side-l">
                {open.map((t) => (
                  <li>
                    <a class="cm-side-a" href={`#/tickets/${t.ticket}`}>
                      <b>{t.priority === 'urgent' ? <><StatusMark s="wait" mkOnly label="Urgent" />{' '}</> : null}{t.title}</b>
                      <FactsLine facts={[{ icon: 'message', value: `Asked by ${askedBy(t)}`, pri: 1, label: 'Who asked' }, { icon: 'clock', value: agoText(t.created), pri: 2, label: 'Opened' }]} />
                    </a>
                  </li>
                ))}
            </ul>
          </section>
          ) : null}
        </div>
      </div>
    </>
  )
}

/* ---------- Approvals ---------- */

const PS: Record<string, string> = { pending: 'ember', shadow: 'shadow', executed: 'ok', approved: 'ok', refused: 'fail', denied: 'fail', failed: 'fail' }
const PAGE = 20

function Approvals({ name, nm, list }: { name: string; nm: string; list: ProposalRowD[] | null }): JSX.Element {
  const verbs = useVerbs()
  const [n, setN] = useState(PAGE)
  if (list === null) return <p class="dl-sub">Loading…</p>
  const waiting = list.filter((x) => x.status === 'pending')
  const past = list.filter((x) => x.status !== 'pending')
  return (
    <>
      <section class="dl-card">
        <header class="dl-ch"><h2>Waiting for you</h2>{waiting.length ? <span class="count hot">{waiting.length}</span> : null}</header>
        {waiting.length ? (
          <>
            <ul class="dl-list">{waiting.slice(0, n).map((p) => <ProposalRowView key={p.proposal} p={p} nm={nm} verbs={verbs} />)}</ul>
            {waiting.length > n ? (
              <p class="dl-foot-note dl-more"><button class="link" type="button" onClick={() => setN(n + PAGE)}>Show {Math.min(PAGE, waiting.length - n)} more of {waiting.length - n}</button></p>
            ) : null}
          </>
        ) : (
          <p class="dl-sub">Nothing waits for you. When <code>{name}</code> wants to do something it has to ask about, it shows up here and in Needs you.</p>
        )}
      </section>
      {past.length ? (
        <details class="dl-card dl-files-d past-d" data-testid="past-decisions">
          <summary class="panel-head"><h3>Past decisions</h3><span class="muted small">{past.length}</span></summary>
          <ul class="dl-list">{past.map((p) => <ProposalRowView key={p.proposal} p={p} nm={nm} verbs={verbs} />)}</ul>
        </details>
      ) : null}
    </>
  )
}

// One proposal as a row (also the Needs-you surfaces'): its mark, title, why in
// words (or its decision), its facts, its age and Review.
export function ProposalRowView({ p, nm, verbs }: { p: ProposalRowD; nm: string; verbs: BotVerb[] }): JSX.Element {
  const pending = p.status === 'pending'
  const decided = p.status === 'shadow'
    ? 'Practice only: what it would have done in shadow mode (nothing was done)'
    : `${p.status[0].toUpperCase()}${p.status.slice(1)}${p.approver ? ` by ${p.approver.replace(/^operator:/, '')}` : ''}${p.reason ? `: “${p.reason}”` : ''}`
  return (
    <li class={'dl-need proposal-row ' + p.status} data-proposal={p.proposal} data-status={p.status}>
      <a class="dl-need-a" href={`#/bots/${p.bot}/proposals/${p.proposal}`} data-testid={pending ? 'review-proposal' : undefined}>
        <span class="dl-ng"><StatusMark s={PS[p.status] || 'idle'} mkOnly lg label={pending ? 'Needs you' : p.status} /></span>
        <span class="dl-nt">
          <b>{proposalTitle(p)}</b>
          <span class="dl-why">{pending ? proposalWhy(p, nm) : decided}</span>
          <FactsLine facts={[envFact(p), actionFact(p, verbs)]} />
        </span>
        <span class="dl-age">{agoText(p.created)}</span>
        <span class="dl-na">{pending ? <span class="btn sm secondary">Review</span> : null}</span>
      </a>
    </li>
  )
}

/* ---------- Workers ---------- */

type WorkerRow = BotPageWorker & { history_id?: string; parent?: string; sweep?: string; verbs?: string[]; group?: string; requester_class?: string }

function Workers({ name, nm, proposals }: { name: string; nm: string; proposals: ProposalRowD[] }): JSX.Element {
  const [ws, setWs] = useState<WorkerRow[] | null>(null)
  const [err, setErr] = useState('')
  const [tks, setTks] = useState<Record<string, Ticket>>({})
  useEffect(() => {
    botPageApi.workers(name).then((r) => setWs((r.workers || []) as WorkerRow[])).catch((e) => setErr(humanError(e)))
    botsApi.tickets({ assignee: `bot:${name}`, status: 'draft,ready,in_progress,waiting,done,declined' }).then((r) => {
      const m: Record<string, Ticket> = {}
      for (const t of r.tickets || []) m[t.id] = t
      setTks(m)
    }).catch(() => {})
  }, [name])
  if (err) return <div class="banner err" role="alert">{err}</div>
  if (!ws) return <p class="dl-sub">Loading…</p>
  const live = (w: WorkerRow) => w.status === 'running' || w.status === 'starting'
  const row = (w: WorkerRow) => {
    const s = live(w) ? 'run' : w.status === 'done' ? 'ok' : w.status === 'failed' ? 'fail' : 'idle'
    const brief = (w.brief || 'A helper').replace(/^./, (c) => c.toUpperCase())
    const p = proposals.find((x) => x.case && x.case === w.case_id && x.status === 'pending')
    const svcs = [...new Set((w.verbs || []).filter((v) => v.includes('.')).map((v) => v.split('.')[0]))]
    return (
      <li class="dl-need wk-row" data-w={w.id}>
        <div class="dl-need-a">
          <span class="dl-ng"><StatusMark s={s} mkOnly lg label={s === 'run' ? 'Running' : s === 'ok' ? 'Finished' : s === 'fail' ? 'Stopped' : 'Idle'} /></span>
          <span class="dl-nt">
            <b>{w.history_id ? <a class="dl-link" href={`#/runs/${w.history_id}`} title="See its run">{brief}</a> : brief}</b>
            <span class="dl-why">
              {live(w) ? 'Working' : w.status === 'failed' ? 'Stopped' : 'Finished'}
              {w.case_id && tks[w.case_id] ? <> · for the ticket <a class="dl-link" href={`#/tickets/${tks[w.case_id].ticket}`}>{tks[w.case_id].title}</a></> : null}
              {p ? <> · asked you to approve <a class="dl-link" href={`#/bots/${name}/proposals/${p.proposal}`}>{proposalTitle(p)}</a></> : null}
            </span>
            <FactsLine
              facts={[
                { icon: w.read_only ? 'act-read' : 'act-change', value: w.read_only ? 'Read-only' : 'May change things (it asks first)', pri: 1, label: 'What it may do' },
                ...svcs.map((x) => ({ icon: 'obj-connector', value: x, pri: 2, label: 'Uses' })),
                w.sweep ? { icon: 'eye', value: 'A routine check', pri: 2 } : null,
              ]}
            />
          </span>
          <span class="dl-age">{w.started ? agoText(w.started) : ''}</span>
          <span class="dl-na">{w.history_id ? <a class="btn sm secondary" href={`#/runs/${w.history_id}`}>See its run</a> : null}</span>
        </div>
      </li>
    )
  }
  const sweep = ws.filter((w) => w.sweep)
  const cur = ws.filter((w) => !w.sweep && live(w))
  const past = ws.filter((w) => !w.sweep && !live(w))
  const sec = (title: string, sub: JSX.Element | string, xs: WorkerRow[]) =>
    xs.length ? (
      <section class="dl-card">
        <header class="dl-ch"><h2>{title}</h2><span class="count">{xs.length}</span></header>
        <p class="dl-sub" style={{ margin: '0 0 8px' }}>{sub}</p>
        <ul class="dl-list">{xs.map(row)}</ul>
      </section>
    ) : null
  return (
    <div class="stack">
      {sec('From its current session', `Helpers ${nm} started for its work now. Each may do what ${nm} may, or less, and never trusts more than whoever asked.`, cur)}
      {sec('Routine checks', 'The checks it runs on its own schedule. Read-only.', sweep)}
      {sec('From earlier sessions', 'Kept for the record.', past)}
      {ws.length ? null : <section class="dl-card"><p class="dl-sub">{name} hasn't started any helpers yet.</p></section>}
      {ws.length ? (
        <details class="dl-card dl-audit-d">
          <summary><Icon name="chev" />For audit: worker ids, groups, trust and verbs</summary>
          <dl class="dl-facts">
            {ws.map((w) => (
              <>
                <dt class="mono">{w.id}</dt>
                <dd><span class="mono">{w.parent || '—'}{w.group ? ` · group ${w.group}` : ''} · ticket {w.case_id || '—'} · trust: {w.requester_class || '—'} · verbs: {(w.verbs || []).join(', ') || 'all it may'}</span></dd>
              </>
            ))}
          </dl>
        </details>
      ) : null}
    </div>
  )
}
