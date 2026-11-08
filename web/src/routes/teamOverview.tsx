// A team's Overview tab, ported from the prototype's teamOverview
// (js/screens/bots.js): Bots on this team (lead tag, Make lead, Remove, Add a
// bot), Delegations, Routing decisions; and on the side, Links. Membership,
// lead and links are config edits; cancelling a delegation declines the
// delegated ticket (the delegating bot is told, as the daemon does).
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { botsApi } from '../api/bots'
import { humanError } from '../api/errors'
import { Avatar } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FormModal, ConfirmModal } from '../components/FormModal'
import { ago } from '../lib/format'
import type { Delegation, Edit, Routed, TeamCtx, Tree } from './teamLib'

const DST: Record<string, string> = { open: 'run', done: 'ok', declined: 'idle' }
const DWORD: Record<string, string> = { open: 'running', done: 'done', declined: 'cancelled' }

// reqWords is who asked, in words: the requester is carried through every
// hop, so the teammate works with no more trust than whoever asked.
function reqWords(requester = '', cls = ''): string {
  switch (cls) {
    case 'operator':
      return requester ? `operator ${requester}` : 'an operator'
    case 'event':
      return 'an event'
    case 'user':
      return 'someone in chat'
    case 'peer':
      return requester.replace(/^bot:/, '') || 'another bot'
    case 'proactive':
      return 'a routine check'
    case 'bot':
      return 'the bot itself'
  }
  return requester || 'someone'
}

// routeLabel says how a ticket reached its bot.
function routeLabel(r: Routed, team: string, nm: (b: string) => string): JSX.Element {
  if (r.via === 'rule')
    return (
      <>
        routed to <b>{team}</b> by rule <span class="mono">{r.rule}</span> → <b>{nm(r.assignee || r.bot)}</b>
      </>
    )
  if (r.via === 'lead')
    return r.assignee ? (
      <>
        assigned by lead <b>{nm(r.lead || r.bot)}</b> to <b>{nm(r.assignee)}</b>
      </>
    ) : (
      <>
        with lead <b>{nm(r.lead || r.bot)}</b> to triage
      </>
    )
  return (
    <>
      no rule matched → fallback <b>{nm(r.assignee || r.bot)}</b>
    </>
  )
}

// linked is the daemon's Config.BotLinked: a bot_links pair, teammates in a
// team that peers, or both listing each other under peers.
export function linked(tree: Tree, a: string, b: string): 'bot_links' | 'peers' | 'team' | '' {
  if (a === b) return ''
  const A = tree.bots?.[a]
  const B = tree.bots?.[b]
  if (!A || !B || A.disabled || B.disabled) return ''
  if ((tree.bot_links || []).some((l: string[]) => l?.length === 2 && ((l[0] === a && l[1] === b) || (l[0] === b && l[1] === a)))) return 'bot_links'
  if (A.team && A.team === B.team && tree.teams?.[A.team] && tree.teams[A.team].peers !== 'none') return 'team'
  if ((A.peers || []).includes(b) && (B.peers || []).includes(a)) return 'peers'
  return ''
}

export function TeamOverview({ ctx }: { ctx: TeamCtx }): JSX.Element {
  const { name, team, c, tree, members, bots, nm, editable } = ctx
  const lead: string = c.routing?.lead || ''
  const allBots = Object.keys(tree.bots || {}).length ? Object.keys(tree.bots).sort() : Object.keys(bots).sort()
  const others = allBots.filter((b) => !members.includes(b))
  const [pick, setPick] = useState('')
  const [nudge, setNudge] = useState(false)
  const teamOfBot = (b: string): string => tree.bots?.[b]?.team || bots[b]?.team || ''
  const delegations = ((team.delegations || []) as Delegation[]).filter((d) => members.includes(d.from) || members.includes(d.to))
  const routed = (team.routed || []) as Routed[]
  const cross: [string, string, string][] = []
  for (const m of members) for (const o of allBots) if (teamOfBot(o) !== name) { const l = linked(tree, m, o); if (l) cross.push([m, o, l]) }

  // ---- membership ----
  function makeLead(m: string) {
    // A team without routing gets one: the daemon needs a fallback beside a lead.
    const edits: Edit[] = c.routing ? [[['teams', name, 'routing', 'lead'], m]] : [[['teams', name, 'routing'], { lead: m, fallback: m }]]
    void ctx.stageEdits(edits, `team ${name}: ${m} leads`)
  }

  function removeEdits(m: string): Edit[] {
    const edits: Edit[] = [[['bots', m, 'team'], undefined]]
    const ro = c.routing
    if (!ro) return edits
    const rest = members.filter((x) => x !== m)
    if (!rest.length) return [...edits, [['teams', name, 'routing'], undefined]]
    if (ro.lead === m) edits.push([['teams', name, 'routing', 'lead'], undefined])
    if (ro.fallback === m) edits.push([['teams', name, 'routing', 'fallback'], ro.lead && ro.lead !== m ? ro.lead : rest[0]])
    const rules = (ro.rules || []) as { to: string }[]
    const kept = rules.filter((r) => r.to !== m)
    if (kept.length !== rules.length) edits.push([['teams', name, 'routing', 'rules'], kept.length ? kept : undefined])
    return edits
  }

  function remove(m: string) {
    const go = () => void ctx.stageEdits(removeEdits(m), `${m} leaves team ${name}`)
    if (lead !== m) return go()
    ctx.open(
      <ConfirmModal
        title={`Remove ${m}, the team's lead?`}
        body="Work routed to the lead falls to the team's rules or its fallback until you pick another lead."
        ok="Remove the lead"
        danger
        onOk={() => {
          ctx.close()
          go()
        }}
        onClose={ctx.close}
      />,
    )
  }

  function add() {
    if (!pick) {
      setNudge(true)
      setTimeout(() => setNudge(false), 600)
      return
    }
    void ctx.stageEdits([[['bots', pick, 'team'], name]], `${pick} joins team ${name}`)
  }

  // ---- links ----
  function linkForm() {
    const mine = members.map((m): [string, string] => [m, m])
    const theirs = allBots.filter((b) => !members.includes(b)).map((b): [string, string] => [b, `${b}${teamOfBot(b) ? ' · team ' + teamOfBot(b) : ' · solo'}`])
    ctx.open(
      <FormModal
        caps="bot_links"
        title="Link two bots"
        intro="They may message each other and delegate, data only, as if both listed the other under peers. Requester provenance is kept on every hop."
        fields={[
          { k: 'a', type: 'enum', req: true, label: `A bot on ${name}`, options: mine },
          { k: 'b', type: 'enum', req: true, label: 'The bot to link', options: theirs },
        ]}
        submit="Review change"
        submitIcon="link"
        onSubmit={async (v) => {
          if (!v.a || !v.b) return 'Pick both bots.'
          if (linked(tree, v.a, v.b) === 'bot_links') return `${v.a} and ${v.b} are linked already.`
          const pairs = [...(tree.bot_links || []), [v.a, v.b].sort()]
          ctx.close()
          await ctx.stageEdits([[['bot_links'], pairs]], `link ${v.a} ↔ ${v.b}`)
        }}
        onClose={ctx.close}
      />,
    )
  }

  function unlink(a: string, b: string) {
    ctx.open(
      <ConfirmModal
        title={`Unlink ${a} and ${b}?`}
        body="Stages removing the bot_links pair. Delegations already running finish."
        ok="Stage it"
        cancel="Keep it"
        danger
        onOk={async () => {
          ctx.close()
          const pairs = (tree.bot_links || []).filter((l: string[]) => !(l?.length === 2 && l.includes(a) && l.includes(b)))
          await ctx.stageEdits([[['bot_links'], pairs.length ? pairs : undefined]], `unlink ${a} ↔ ${b}`)
        }}
        onClose={ctx.close}
      />,
    )
  }

  // ---- delegations ----
  function cancelDelegation(d: Delegation) {
    ctx.open(
      <ConfirmModal
        title={`Cancel delegation ${d.delegation}?`}
        body="The recipient's child task is closed and the delegating bot is told the task was cancelled. Immediate, audited."
        ok="Cancel delegation"
        danger
        onOk={async () => {
          try {
            await botsApi.patchTicket(d.to_ticket!, { status: 'declined', note: 'delegation cancelled by an operator' })
            ctx.close()
            location.reload()
          } catch (e) {
            ctx.close()
            ctx.open(<ConfirmModal title="Couldn’t cancel it" body={humanError(e)} ok="OK" onOk={ctx.close} onClose={ctx.close} />)
          }
        }}
        onClose={ctx.close}
      />,
    )
  }

  const reach = (b: string) => {
    const ok: string[] = []
    const iso: string[] = []
    for (const o of allBots) {
      if (o === b) continue
      if (linked(tree, b, o)) ok.push(o)
      else if (teamOfBot(o)) iso.push(o)
    }
    return { ok, iso }
  }

  const off = editable ? undefined : 'This dashboard can’t change the config'

  return (
    <div class="grid g-side">
      <div class="stack">
        <div class="panel" data-testid="team-members">
          <div class="panel-head">
            <h3>Bots on this team</h3>
            <span class="muted small">{team.peers === 'none' ? 'kept apart: they talk only when linked' : 'peers of each other by default'}</span>
          </div>
          <div class="list">
            {members.length ? (
              members.map((m) => (
                <div class="item" data-bot={m}>
                  <a href={`#/bots/${m}`} class="row grow" style="gap:10px">
                    <Avatar id={bots[m]?.identity} size={32} />
                    <div class="tm-who">
                      <div class="tm-line"><b>{nm(m)}</b> <span class="bav-id">{m}</span> {lead === m ? <span class="tag">lead</span> : null}</div>
                      <div class="muted tiny">{bots[m]?.description || tree.bots?.[m]?.description || ''}</div>
                    </div>
                  </a>
                  <span class="tm-acts">
                    {lead === m ? null : (
                      <button class="btn sm ghost" type="button" disabled={!!off} title={off} onClick={() => makeLead(m)} data-testid="team-make-lead">
                        Make lead
                      </button>
                    )}
                    <button class="btn sm ghost" type="button" disabled={!!off} title={off} aria-label={`Remove ${m} from team`} onClick={() => remove(m)} data-testid="team-remove">
                      <Icon name="x" />
                      Remove
                    </button>
                  </span>
                </div>
              ))
            ) : (
              <div class="item muted small">No bots yet.</div>
            )}
          </div>
          <div class="panel-foot tm-add">
            <label class="row small grow">
              <span class="muted tm-add-l">Add a bot</span>
              <select
                class={'select mono' + (nudge ? ' nudge' : '')}
                aria-label="Bot to add"
                data-testid="team-add-sel"
                value={pick}
                onChange={(e) => {
                  const v = (e.target as HTMLSelectElement).value
                  if (v === '__new__') {
                    ;(e.target as HTMLSelectElement).value = pick
                    location.hash = `#/bots/new?team=${encodeURIComponent(team.team)}`
                    return
                  }
                  setPick(v)
                }}
              >
                <option value="">Choose a bot…</option>
                {others.map((m) => (
                  <option value={m}>
                    {m}
                    {teamOfBot(m) ? ' (moves from ' + teamOfBot(m) + ')' : ' (solo)'}
                  </option>
                ))}
                <option value="__new__">+ Create a new bot in this team…</option>
              </select>
            </label>
            <button
              class="btn sm"
              type="button"
              disabled={!others.length || !!off}
              title={off || (others.length ? undefined : 'Every existing bot is already here: create a new one from the list')}
              onClick={add}
              data-testid="team-add"
            >
              <Icon name="plus" />
              Add to team
            </button>
          </div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h3>Delegations</h3>
            <span class="muted small">teammates hand each other tasks; results report back</span>
          </div>
          <div class="list">
            {delegations.length ? (
              delegations.map((d) => (
                <div class="item deleg" data-deleg={d.delegation}>
                  <StatusMark s={DST[d.status] || 'idle'} mkOnly label={DWORD[d.status] || d.status} />
                  <div class="grow small deleg-b">
                    <div>
                      delegated by <b>{nm(d.from)}</b> →{' '}
                      {d.to_ticket ? (
                        <a class="dl-link" href={`#/tickets/${d.to_ticket}`}>
                          <b>{nm(d.to)}</b>
                        </a>
                      ) : (
                        <b>{nm(d.to)}</b>
                      )}
                      {teamOfBot(d.from) && teamOfBot(d.to) && teamOfBot(d.from) !== teamOfBot(d.to) ? ' · linked bots' : ''} · <span class="deleg-st">{DWORD[d.status] || d.status}</span>
                    </div>
                    <div class="deleg-task">“{d.title}”</div>
                    {d.result ? <div class="deleg-result">↩ result: {d.result}</div> : null}
                    <div class="dl-sub">
                      For {reqWords(d.requester, d.requester_class)}, whose trust it keeps{d.created ? ` · ${ago(d.created)}` : ''}
                      {d.status !== 'open' && d.updated ? ` · reported back ${ago(d.updated)}` : ''}
                    </div>
                  </div>
                  {d.status === 'open' && d.to_ticket ? (
                    <button class="btn sm ghost" type="button" onClick={() => cancelDelegation(d)}>
                      <Icon name="x" />
                      Cancel
                    </button>
                  ) : null}
                </div>
              ))
            ) : (
              <div class="item small muted">No delegations yet.</div>
            )}
          </div>
          <div class="panel-foot small muted">Beyond the team, a bot delegates only to a bot it's linked with (both list each other as peers, or an operator's bot_links pair).</div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h3>Routing decisions</h3>
            <span class="muted small">requester provenance kept through every hop</span>
          </div>
          <div class="list">
            {routed.length ? (
              routed.slice(0, 8).map((x) => (
                <a class="item click" href={x.ticket ? `#/tickets/${x.ticket}` : `#/bots/${x.bot}/tickets`}>
                  <span class="mono tiny muted">{x.ticket || x.case}</span>
                  <div class="grow small">
                    <div class="ellipsis">
                      <b>{x.title}</b>
                    </div>
                    <div class="tiny muted">
                      {routeLabel(x, name, nm)} · requester{' '}
                      <span class={'tag taint ' + (x.requester_class || '')}>{x.requester || reqWords('', x.requester_class)}</span>
                    </div>
                  </div>
                  <span class="chev">
                    <Icon name="chev" />
                  </span>
                </a>
              ))
            ) : (
              <div class="item muted small">Nothing routed yet.</div>
            )}
          </div>
        </div>
      </div>

      <div class="stack">
        <div class="panel">
          <div class="panel-head">
            <h3>Links</h3>
          </div>
          <div class="panel-body stack tight small">
            <div>
              <span class="caps">inside the team</span>
              <div class="muted tiny" style="margin-top:3px">
                {members.length > 1
                  ? team.peers === 'none'
                    ? 'The team sets peers: none, so its bots talk only when linked.'
                    : members.map(nm).join(' ↔ ') + ' · data-only ask, no extra config'
                  : 'One bot: nobody to ask yet.'}
              </div>
            </div>
            <div>
              <span class="caps">beyond the team</span>
              {cross.length ? (
                cross.map(([a, o, how]) => (
                  <div class="row" style="margin-top:6px">
                    <span class="grow">
                      {nm(a)} ↔ {nm(o)}
                    </span>
                    <span class="muted tiny">{how === 'bot_links' ? 'bot_links' : how === 'team' ? 'teammates' : 'both list each other as peers'}</span>
                    {how === 'bot_links' ? (
                      <button class="btn sm ghost" type="button" disabled={!!off} onClick={() => unlink(a, o)}>
                        Unlink
                      </button>
                    ) : null}
                  </div>
                ))
              ) : (
                <div class="muted tiny" style="margin-top:4px">
                  No links: the team's bots talk only to each other.
                </div>
              )}
              <button class="btn sm" type="button" style="margin-top:8px" disabled={!!off || !members.length} onClick={linkForm} data-testid="team-link">
                <Icon name="link" />
                Link a bot…
              </button>
            </div>
            <div class="muted tiny">A link lets two bots message each other and delegate, data only. It's a pair of bots, never a whole team, and a config edit (widening).</div>
            <div>
              <span class="caps">delegation reach</span>
              {members.map((b) => {
                const r = reach(b)
                return (
                  <div class="tiny" style="margin-top:4px">
                    <b>{nm(b)}</b> → {r.ok.length ? r.ok.map((x, i) => <span>{(i ? ', ' : '') + nm(x)}</span>) : <span class="muted">no one</span>}
                    {r.iso.length ? <span class="muted"> · isolated from {r.iso.map(nm).join(', ')}</span> : null}
                  </div>
                )
              })}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

