// A team's Routing tab, ported from the prototype's teamRouting
// (js/screens/bots.js): the ordered rules as sentences (Edit, ⋯ move/delete,
// + Add a rule), each a staged config edit; and "Try a rule", which runs the
// daemon's own match (internal/bot/teams.go ruleMatches: every match field a
// path.Match glob) against the team's real past events.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { botsApi, type Ticket } from '../api/bots'
import { Icon } from '../components/Icon'
import { Menu } from '../components/Menu'
import { FormModal } from '../components/FormModal'
import { globRe, ruleLabel, ruleMatches, touches, type Edit, type Routed, type Rule, type TeamCtx, type TryEvent } from './teamLib'
import { staged } from '../configStore'

// ruleWords is a rule as a sentence: "When hooks.deploy arrives, for service
// api*, it goes to Deploy bot."
// Conn is a connection and the events its plugin says it sends (GET
// /api/v1/newbot/connections: learned from the plugin at runtime).
interface Conn {
  name: string
  events: { name: string; desc?: string }[]
}

// eventWords names an event kind the way the prototype does: its plugin's
// description and the connection it comes from, else the quoted name.
function eventWords(on: string, conns: Conn[]): string {
  const i = on.indexOf('.')
  const conn = i > 0 ? conns.find((x) => x.name === on.slice(0, i)) : undefined
  if (!conn) return `When “${on}” arrives`
  const ev = on.slice(i + 1)
  const e = conn.events.find((x) => x.name === ev)
  return `When “${e?.desc ? e.desc.toLowerCase() : ev}” arrives from ${conn.name}`
}

function ruleWords(r: Rule, nm: (b: string) => string, conns: Conn[]): string {
  const m = r.match || {}
  const what = m.on ? eventWords(m.on, conns) : m.source ? `When something arrives from ${m.source}` : 'When anything arrives'
  const conds = Object.entries(m)
    .filter(([k]) => k !== 'on' && !(k === 'source' && !m.on))
    .map(([k, v]) => `${k.replace(/_/g, ' ')} ${v}`)
  return `${what}${conds.length ? `, for ${conds.join(', ')}` : ''}, it goes to ${nm(r.to)}.`
}

// parseConds reads "field = glob" lines.
function parseConds(text: string): [Record<string, string>, string] {
  const out: Record<string, string> = {}
  for (const raw of (text || '').split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const i = line.search(/=|→/)
    if (i <= 0) return [out, `“${line}” needs a field and a glob, like service = api*.`]
    const k = line.slice(0, i).trim()
    const v = line.slice(i + (line[i] === '→' ? 1 : 1)).trim()
    if (!k || !v) return [out, `“${line}” needs a field and a glob, like service = api*.`]
    if (!globRe(v)) return [out, `“${v}” isn't a glob conductor can read.`]
    out[k] = v
  }
  return [out, '']
}

function pastEvents(tickets: Ticket[], routed: Routed[]): TryEvent[] {
  const out: TryEvent[] = tickets.map((t) => ({
    id: 't:' + t.ticket,
    label: `${t.ticket} · ${t.title}`,
    source: 'ticket',
    title: t.title,
    text: t.description || '',
    requester: t.requester || t.created_by || '',
    fields: { title: t.title, text: t.description || '', priority: t.priority || '', labels: (t.labels || []).join(',') },
  }))
  const seen = new Set(tickets.map((t) => t.ticket))
  for (const r of routed) {
    if (r.ticket && seen.has(r.ticket)) continue
    out.push({ id: 'r:' + r.bot + '/' + r.case, label: `${r.ticket || r.case} · ${r.title}`, source: r.source || '', title: r.title, text: '', requester: r.requester || '', fields: {} })
  }
  return out
}

export function TeamRouting({ ctx }: { ctx: TeamCtx }): JSX.Element {
  const { name, c, members, nm, editable } = ctx
  const rules: Rule[] = c.routing?.rules || []
  const lead: string = c.routing?.lead || ''
  const fallback: string = c.routing?.fallback || ''
  const [events, setEvents] = useState<TryEvent[] | null>(null)
  const [ev, setEv] = useState('')
  const [conns, setConns] = useState<Conn[]>([])
  const [menu, setMenu] = useState<{ el: HTMLElement; i: number } | null>(null)
  const moreRefs = useRef<Record<number, HTMLButtonElement | null>>({})
  const teamStaged = touches(staged.value, ['teams', name])
  const off = editable ? undefined : 'This dashboard can’t change the config'

  useEffect(() => {
    fetch('/api/v1/newbot/connections', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : { connections: [] }))
      .then((r: { connections?: Conn[] }) => setConns((r.connections || []).map((x) => ({ name: x.name, events: x.events || [] }))))
      .catch(() => setConns([]))
    // Real past work to try the rules on: tickets for the team and for each of its bots (an event's
    // ticket assigned straight to a bot is still something a rule could have routed), then routed cases.
    botsApi
      .tickets({ status: 'draft,ready,in_progress,waiting,done,declined' })
      .then((r) => setEvents(pastEvents((r.tickets || []).filter((t) => t.assignee === 'team:' + name || members.some((m) => t.assignee === 'bot:' + m)), (ctx.team.routed || []) as Routed[])))
      .catch(() => setEvents(pastEvents([], (ctx.team.routed || []) as Routed[])))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, members.join(',')])

  // stageRules stages the new list; a team with no routing yet gets one (the
  // daemon needs its fallback).
  async function stageRules(next: Rule[], summary: string) {
    let edits: Edit[]
    if (c.routing) edits = [[['teams', name, 'routing', 'rules'], next.length ? next : undefined]]
    else {
      const fb = lead || members[0]
      if (!fb) return ctx.open(<Note title="Add a bot first" body={`Team ${name} has no bots yet: a rule needs a bot on the team to route to.`} onClose={ctx.close} />)
      edits = [[['teams', name, 'routing'], { fallback: fb, rules: next }]]
    }
    await ctx.stageEdits(edits, summary, false)
  }

  function edit(i: number) {
    const r = i >= 0 ? rules[i] : null
    const conds = r ? Object.entries(r.match || {}).filter(([k]) => k !== 'on').map(([k, v]) => `${k} = ${v}`).join('\n') : ''
    if (!members.length) return ctx.open(<Note title="Add a bot first" body={`Team ${name} has no bots yet: a rule needs a bot on the team to route to.`} onClose={ctx.close} />)
    ctx.open(
      <FormModal
        caps={`Team ${name}`}
        title={r ? `Edit rule ${r.name || i + 1}` : 'Add a routing rule'}
        intro="Deterministic: the event's fields matched against globs, no model. First match wins; no match goes to the lead to triage."
        testid="rule-form"
        values={r ? { name: r.name || '', on: r.match?.on || '', conds, to: r.to } : {}}
        fields={[
          { k: 'name', type: 'string', req: true, pattern: /^[a-z][\w-]*$/, patternMsg: 'letters, digits, - and _', validate: (v) => (rules.some((x, j) => j !== i && x.name === v) ? `A rule named ${v} is already here.` : '') },
          { k: 'on', type: 'string', label: 'When the event is', ph: 'hooks.deploy', help: 'Its kind, a glob. Leave it empty to match on its fields alone.', validate: (v) => (v && !globRe(v) ? 'Not a glob conductor can read.' : '') },
          { k: 'conds', type: 'text', mono: true, label: 'and its fields match (globs)', ph: 'service = api*', help: 'One per line, e.g. service = api*, repo = acme/web. Also: source, title, text, requester.' },
          { k: 'to', type: 'enum', req: true, label: 'Route to', options: members.map((m): [string, string] => [m, m]) },
        ]}
        submit="Stage rule"
        onSubmit={async (v) => {
          const [cs, bad] = parseConds(v.conds || '')
          if (bad) return bad
          const match: Record<string, string> = {}
          if (v.on) match.on = v.on
          Object.assign(match, cs)
          if (!Object.keys(match).length) return 'Give it something to match: the event kind, or a field.'
          const nr: Rule = { name: v.name, match, to: v.to }
          const next = rules.slice()
          if (r) next[i] = nr
          else next.push(nr)
          ctx.close()
          await stageRules(next, `team ${name}: rule ${v.name}`)
        }}
        onClose={ctx.close}
      />,
    )
  }

  function move(i: number, d: number) {
    const next = rules.slice()
    const j = i + d
    ;[next[i], next[j]] = [next[j], next[i]]
    void stageRules(next, `team ${name}: rule ${ruleLabel(rules[i], i)} moved ${d < 0 ? 'up' : 'down'}`)
  }

  function del(i: number) {
    const next = rules.filter((_, j) => j !== i)
    void stageRules(next, `team ${name}: rule ${ruleLabel(rules[i], i)} deleted`)
  }

  const evs = events || []
  const cur = evs.find((e) => e.id === ev) || evs[0]
  const hitAt = cur ? rules.findIndex((r) => ruleMatches(r, cur)) : -1
  const hit = hitAt >= 0 ? rules[hitAt] : null

  return (
    <div class="grid g-side">
      <div class="stack">
        <div class="panel" data-testid="team-rules">
          <div class="panel-head">
            <h3>Rules</h3>
            <span class="muted small">first match wins · no match: the lead triages · then the fallback</span>
            <div class="tools">
              <button class="btn sm" type="button" disabled={!!off} title={off} onClick={() => edit(-1)} data-testid="rule-add">
                <Icon name="plus" />
                Add a rule
              </button>
            </div>
          </div>
          <ol class="rule-list">
            {rules.length ? (
              rules.map((r, i) => (
                <li class="rule-row" data-rule={ruleLabel(r, i)}>
                  <span class="rule-n">{i + 1}</span>
                  <div class="rule-t">
                    <b>{ruleWords(r, nm, conns)}</b>
                    <span class="dl-sub">{r.name || 'rule ' + (i + 1)}</span>
                  </div>
                  <span class="dl-na">
                    <button class="btn sm secondary" type="button" disabled={!!off} onClick={() => edit(i)} data-testid="rule-edit">
                      <Icon name="edit" />
                      Edit
                    </button>
                    <button
                      ref={(el) => { moreRefs.current[i] = el }}
                      class="icon-btn"
                      type="button"
                      disabled={!!off}
                      aria-label="More for this rule"
                      aria-haspopup="menu"
                      data-testid="rule-more"
                      onClick={() => setMenu(menu?.i === i ? null : { el: moreRefs.current[i]!, i })}
                    >
                      <Icon name="more" />
                    </button>
                  </span>
                </li>
              ))
            ) : (
              <li class="dl-sub rule-none">No rules: work reaches this team only through its bots directly.</li>
            )}
          </ol>
          <div class="panel-foot small muted">
            Changing a rule is reviewed like any config change. Lead: <b>{lead ? nm(lead) : 'none'}</b> · fallback: <b>{fallback ? nm(fallback) : 'none'}</b>.
            {teamStaged ? (
              <>
                {' '}
                <a class="link" href="#/system/config">
                  Review staged change
                </a>
              </>
            ) : null}
          </div>
        </div>
      </div>
      <div class="stack">
        <div class="panel" data-testid="team-try">
          <div class="panel-head">
            <h3>Try a rule</h3>
          </div>
          <div class="panel-body stack tight small">
            <label class="field">
              <span class="caps">a real past event</span>
              <select class="select mono" aria-label="Event to route" value={cur?.id || ''} disabled={!evs.length} onChange={(e) => setEv((e.target as HTMLSelectElement).value)} data-testid="try-event">
                {evs.length ? evs.map((e) => <option value={e.id}>{e.label}</option>) : <option value="">{events ? 'Nothing has reached this team yet' : 'Loading…'}</option>}
              </select>
            </label>
            {!cur ? (
              <div class="callout small">Once work reaches {name} (a ticket for the team, or a routed event), pick it here to see which rule takes it.</div>
            ) : hit ? (
              <div class="callout ok small" data-testid="try-result">
                Matches <b class="mono">{ruleLabel(hit, hitAt)}</b> → <b>{nm(hit.to)}</b>. The ticket keeps who asked: <span class="mono">{cur.requester || (cur.source ? 'event:' + cur.source : 'unknown')}</span>.
              </div>
            ) : (
              <div class="callout small" data-testid="try-result">
                No rule in <b>{name}</b> matches <span class="mono">{cur.label}</span>; {lead ? <>the lead, <b>{nm(lead)}</b>, triages it</> : fallback ? <>it goes to the fallback, <b>{nm(fallback)}</b></> : 'the team has no lead or fallback to take it'}.
              </div>
            )}
          </div>
        </div>
        <div class="callout info small">
          The lead is a bot: it reads the ticket, then assigns it with <span class="mono">bot assign</span>. The requester never changes on the way, so the assignee runs on no more trust than whoever asked.
        </div>
      </div>
      {menu ? (
        <Menu
          anchor={menu.el}
          label="More for this rule"
          onClose={() => setMenu(null)}
          items={[
            { label: 'Move up', icon: 'chev', onClick: () => move(menu.i, -1), disabled: menu.i ? false : 'Already first' },
            { label: 'Move down', icon: 'chevd', onClick: () => move(menu.i, 1), disabled: menu.i < rules.length - 1 ? false : 'Already last' },
            '-',
            { label: 'Delete rule', icon: 'trash', onClick: () => del(menu.i) },
          ]}
        />
      ) : null}
    </div>
  )
}

function Note({ title, body, onClose }: { title: string; body: string; onClose: () => void }): JSX.Element {
  return (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true">
        <div class="modal-head">
          <div>
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body">
          <p>{body}</p>
        </div>
        <div class="modal-foot">
          <button type="button" class="btn primary" onClick={onClose}>
            OK
          </button>
        </div>
      </div>
    </div>
  )
}
