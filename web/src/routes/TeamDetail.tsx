// A team's page, ported line for line from the prototype's teamShell
// (js/screens/bots.js): crumbs, the team's face and name, one sentence of
// what's true of the whole team, ⋯ (picture, route an event, edit, remove),
// New ticket / Route an event here / Edit team, and the tabs Overview ·
// Routing · Ceilings & scopes · Tickets · Knowledge. Every edit stages the
// real config change and leads to Review & apply (System · Config).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, displayName, type BotSummary, type TeamSummary, type Ticket } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { ApiError, humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { Menu } from '../components/Menu'
import { StatusMark } from '../components/StatusMark'
import { TabsBar } from '../components/TabsBar'
import { FormModal, ConfirmModal } from '../components/FormModal'
import { config, staged, loadConfig, stage, stageDelete, cfgError } from '../configStore'
import { ScopedTickets } from './ScopedTickets'
import { RepoView } from '../components/RepoView'
import { TeamForm, TeamPicture } from './TeamForm'
import { overlay, touches, type Edit, type TeamCtx, type Tree } from './teamLib'
import { TeamOverview } from './teamOverview'
import { TeamRouting } from './teamRouting'
import { TeamDefaults } from './teamDefaults'
import '../styles/bots-team.css'

const TABS = ['overview', 'routing', 'defaults', 'tickets', 'knowledge'] as const
type Tab = (typeof TABS)[number]

// What "on their own, its bots may …" says for each ceiling.
const CEIL_WORDS: Record<string, string> = {
  observe: 'only read',
  reversible: 'do things that can be undone',
  commit_money: 'also buy things, inside their caps',
}

export function TeamDetail({ name, tab }: { name: string; tab?: string }): JSX.Element {
  const active: Tab = (TABS as readonly string[]).includes(tab || '') ? (tab as Tab) : 'overview'
  const [team, setTeam] = useState<TeamSummary | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [editable, setEditable] = useState(true)
  const [openTickets, setOpenTickets] = useState<Ticket[] | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [actErr, setActErr] = useState('')
  const [modal, setModal] = useState<JSX.Element | null>(null)
  const [menuAt, setMenuAt] = useState<HTMLElement | null>(null)
  const moreRef = useRef<HTMLButtonElement>(null)

  const load = () => {
    botsApi.team(name).then(setTeam).catch(setErr)
    botsApi
      .bots()
      .then((r) => {
        const m: Record<string, BotSummary> = {}
        for (const b of r.bots) m[b.bot] = b
        setBots(m)
        setEditable(r.editable !== false)
      })
      .catch(() => {
        /* names fall back to ids */
      })
    // The team's open tickets: its own and its bots' (not done or declined,
    // the list's default); counted below once the members are known.
    botsApi
      .tickets()
      .then((r) => setOpenTickets(r.tickets || []))
      .catch(() => setOpenTickets(null))
  }
  useEffect(() => {
    setTeam(null)
    setErr(null)
    setActErr('')
    load()
    void loadConfig()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  if (err) {
    if (err instanceof ApiError && err.status === 404) {
      return (
        <div class="team-page">
          <EmptyState title={`No team named ${name}`} action={{ label: 'Bots', href: '#/bots' }}>
            <p>It may have been removed, renamed, or never existed on this daemon.</p>
          </EmptyState>
        </div>
      )
    }
    return (
      <div class="team-page">
        <nav class="dl-crumbs" aria-label="Where you are">
          <a href="#/bots">Bots</a>
          <span class="sep" aria-hidden="true">›</span>
          <span class="cur" aria-current="page">{name}</span>
        </nav>
        <div class="banner err" role="alert">{humanError(err)}</div>
      </div>
    )
  }
  if (!team) return <div class="team-page"><p class="muted">Loading {name}…</p></div>

  // The config as it will be once the staged edits apply.
  const changes = staged.value
  const live: Tree = config.value?.tree || null
  const tree: Tree = live ? overlay(live, changes) : {}
  const c: Tree = tree.teams?.[name] || (live ? {} : { operators: team.operators, routing: team.routing ? { lead: team.routing.lead, fallback: team.routing.fallback, rules: team.routing.rules.map((r) => ({ name: r.rule, match: r.match, to: r.to })) } : undefined, knowledge: team.knowledge })
  const byName: string[] = live ? Object.keys(tree.bots || {}).filter((b) => tree.bots[b]?.team === name).sort() : (team.bots || []).slice().sort()
  const nm = (b: string) => displayName(bots[b]?.identity, tree.bots?.[b]?.display_name || b)
  const lead: string = c.routing?.lead || ''
  // The lead first, then the rest by name.
  const members: string[] = byName.includes(lead) ? [lead, ...byName.filter((b) => b !== lead)] : byName
  const ticketCount = (openTickets || []).filter((t) => t.assignee === 'team:' + name || members.some((m) => t.assignee === 'bot:' + m)).length
  // A team without a picture shows its initials as the prototype does: the
  // first two letters or digits of its name, lowercase.
  const teamId = team.identity?.avatar || team.identity?.picture ? team.identity : { ...team.identity, initials: name.replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase() }
  const ops: string[] = c.operators || []
  const ceiling: string = c.autonomy?.ceiling || 'reversible'
  const teamStaged = touches(changes, ['teams', name]) || members.some((m) => touches(changes, ['bots', m, 'team'])) || (team.bots || []).some((m) => touches(changes, ['bots', m, 'team']))
  const picStaged = touches(changes, ['teams', name, 'avatar'])
  const rules: unknown[] = c.routing?.rules || []

  const close = () => setModal(null)
  const open = (m: JSX.Element) => setModal(m)

  async function stageEdits(edits: Edit[], summary: string, go = true): Promise<boolean> {
    setActErr('')
    for (const [p, v] of edits) {
      if (v === undefined) await stageDelete(p, summary)
      else await stage(p, v, summary)
      if (cfgError.value) {
        setActErr(cfgError.value)
        return false
      }
    }
    if (go) location.hash = '#/system/config'
    return true
  }

  function openTeamForm() {
    open(<TeamForm team={name} onClose={close} />)
  }

  // Route an event here: a ticket for the team, made ready at once, is what
  // the daemon routes through the team's rules, its lead and its fallback
  // (internal/bot/tickets.go dispatchTicket → RouteTeam).
  function routeHere() {
    open(<RouteForm name={name} onClose={close} />)
  }

  function pictureForm() {
    open(<TeamPicture team={name} onClose={close} />)
  }

  function removeTeam() {
    const ms = members
    open(
      <ConfirmModal
        title={`Remove team ${name}?`}
        body={`${ms.length ? `Its ${ms.length} bot${ms.length > 1 ? 's' : ''} (${ms.join(', ')}) leave the team and keep their own settings. ` : ''}Stages the removal; review and apply it.`}
        ok="Stage removal"
        danger
        onOk={async () => {
          close()
          await stageEdits([...ms.map((m): Edit => [['bots', m, 'team'], undefined]), [['teams', name], undefined]], `remove team ${name}`)
        }}
        onClose={close}
      />,
    )
  }

  const ctx: TeamCtx = { name, team, c, tree, members, bots, editable, nm, stageEdits, open, close, openTeamForm, routeHere }

  let body: JSX.Element
  switch (active) {
    case 'routing':
      body = <TeamRouting ctx={ctx} />
      break
    case 'defaults':
      body = <TeamDefaults ctx={ctx} />
      break
    case 'tickets':
      body = <ScopedTickets assignee={'team:' + name} />
      break
    case 'knowledge':
      body = c.knowledge || team.knowledge ? (
        <RepoView target={'team:' + name} />
      ) : (
        <div class="panel">
          <EmptyState
            title="No shared knowledge repo"
            icon="file"
            hero
            action={editable ? { label: 'Add a knowledge repo', onClick: () => void stageEdits([[['teams', name, 'knowledge'], {}]], `team ${name}: a shared knowledge repo`), testid: 'team-add-knowledge' } : undefined}
          >
            <p>A team can keep one repo every bot on it reads. You write to it directly; bots propose and you approve.</p>
          </EmptyState>
        </div>
      )
      break
    default:
      body = <TeamOverview ctx={ctx} />
  }

  const pend = (team.knowledge_proposals || []).filter((p) => (p as { status?: string }).status === 'pending').length

  return (
    <div class="team-page" data-team={name}>
      <nav class="dl-crumbs" aria-label="Where you are">
        <a href="#/bots">Bots</a>
        <span class="sep" aria-hidden="true">›</span>
        <span class="cur" aria-current="page">{name}</span>
      </nav>
      <div class="dl-bothead">
        <span class="dl-bot-av team-av">
          <Avatar id={teamId} size={64} kind="team" />
        </span>
        <div>
          <h1>{name}</h1>
          {picStaged ? (
            <p class="mk-pend">
              A new picture, not applied yet · <a class="dl-link" href="#/system/config">Review and apply</a>
            </p>
          ) : null}
          <p class="dl-lead">
            {members.length} bot{members.length === 1 ? '' : 's'}
            {lead ? `, led by ${nm(lead)}` : ''}. {ops.length ? `${ops.join(' or ')} approve${ops.length === 1 ? 's' : ''}` : 'Any operator approves'}; on their own, its bots may {CEIL_WORDS[ceiling] || ceiling}.
          </p>
        </div>
        {teamStaged ? <StatusMark s="staged" word="Change not applied" /> : null}
        <button
          ref={moreRef}
          class="icon-btn"
          type="button"
          aria-label={`More for team ${name}`}
          aria-haspopup="menu"
          data-testid="team-more"
          onClick={() => setMenuAt(menuAt ? null : moreRef.current)}
        >
          <Icon name="more" />
        </button>
      </div>
      {menuAt ? (
        <Menu
          anchor={menuAt}
          label={`More for team ${name}`}
          onClose={() => setMenuAt(null)}
          items={[
            { label: 'Change its picture…', icon: 'image', onClick: pictureForm, disabled: editable ? false : 'This dashboard can’t change the config' },
            { label: 'Route an event here…', icon: 'send', onClick: routeHere },
            { label: 'Edit team…', icon: 'edit', onClick: openTeamForm, disabled: editable ? false : 'This dashboard can’t change the config' },
            '-',
            { label: 'Remove this team…', icon: 'trash', onClick: removeTeam, disabled: editable ? false : 'This dashboard can’t change the config' },
          ]}
        />
      ) : null}
      <div class="dl-actions">
        <a class="btn secondary" href={`#/tickets?new=1&assignee=${encodeURIComponent('team:' + name)}`} data-testid="team-new-ticket">
          <Icon name="plus" />
          New ticket
        </a>
        <button class="btn secondary ph-hide" type="button" onClick={routeHere} data-testid="team-deliver">
          <Icon name="send" />
          Route an event here
        </button>
        <button class="btn secondary ph-hide" type="button" onClick={openTeamForm} disabled={!editable} data-testid="team-edit">
          <Icon name="edit" />
          Edit team
        </button>
      </div>
      <TabsBar
        ariaLabel="Team"
        active={active}
        items={[
          { id: 'overview', label: 'Overview', href: `#/teams/${name}` },
          { id: 'routing', label: 'Routing', href: `#/teams/${name}/routing`, count: rules.length || null },
          { id: 'defaults', label: 'Ceilings & scopes', href: `#/teams/${name}/defaults` },
          { id: 'tickets', label: 'Tickets', href: `#/teams/${name}/tickets`, count: ticketCount || null },
          { id: 'knowledge', label: 'Knowledge', href: `#/teams/${name}/knowledge`, count: pend || null },
        ]}
      />
      {actErr ? (
        <div class="callout danger small" role="alert">
          {actErr}
        </div>
      ) : null}
      {body}
      {modal}
    </div>
  )
}

// RouteForm is the prototype's team-deliver form: replay one of the team's
// past tickets, or write a request. Route opens a ticket for the team, ready,
// so the daemon routes it at once; the page goes to that ticket.
function RouteForm({ name, onClose }: { name: string; onClose: () => void }): JSX.Element {
  const [past, setPast] = useState<Ticket[]>([])
  useEffect(() => {
    botsApi
      .tickets({ assignee: 'team:' + name, status: 'draft,ready,in_progress,waiting,done,declined' })
      .then((r) => setPast(r.tickets || []))
      .catch(() => setPast([]))
  }, [name])
  return (
    <FormModal
      caps={`team ${name}`}
      title={`Route work to ${name}`}
      intro="The team's routing rules decide who gets it; with no match it goes to the lead."
      testid="team-route-form"
      fields={[
        {
          k: 'event',
          type: 'enum',
          label: 'Replay a past event',
          req: true,
          options: [['-', 'none: write a request'] as [string, string]].concat(past.map((t) => [t.ticket, `${t.ticket} · ${t.title}`] as [string, string])),
        },
        { k: 'title', type: 'string', mono: false, show: (v) => !v.event || v.event === '-' },
        { k: 'text', type: 'text', label: 'Text (reaches the bot fenced as untrusted data)', show: (v) => !v.event || v.event === '-' },
      ]}
      submit="Route"
      submitIcon="send"
      onSubmit={async (v) => {
        const ev = past.find((t) => t.ticket === v.event)
        const title = ev ? ev.title : (v.title || '').trim()
        if (!title) return 'Give it a title, or pick an event.'
        try {
          const t = await botsApi.createTicket({ title, description: ev ? ev.description : v.text, assignee: 'team:' + name, priority: ev?.priority, labels: ev?.labels, ready: true })
          onClose()
          location.hash = `#/tickets/${t.ticket}`
        } catch (e) {
          return humanError(e)
        }
      }}
      onClose={onClose}
    />
  )
}

