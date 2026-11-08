// A team's Ceilings & scopes tab, ported from the prototype's teamDefaults
// (js/screens/bots.js) and teamWalletHTML (js/screens/wallet.js): What its bots
// may spend (budget.purchases, Set caps), the stricter-only callout, and the
// table of team values and what each bot does with them. The badges are the
// daemon's own narrow-never-widen checks (internal/config/teams.go
// checkTeamCeilings), run on the config as it will be once staged edits apply.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { Icon } from '../components/Icon'
import { Avatar } from '../components/Avatar'
import { getPath, listBadge, money, subset, tierErrors, type Badge, type TeamCtx, type Tree } from './teamLib'

const RISK: Record<string, string> = { observe: 'only reads', reversible: 'things that can be undone', propose: 'asks you first', commit_money: 'spends money', never: 'never' }
const riskWords = (t: string) => RISK[t] || t

function BadgeTag({ b }: { b: Badge | 'stricter' | 'unset' }): JSX.Element {
  switch (b) {
    case 'unset':
      return <span class="tag inherit inh">unset</span>
    case 'wider':
      return <span class="tag inherit wid">wider ✗</span>
    case 'inherits':
      return <span class="tag inherit inh">inherits</span>
    case 'equal':
      return <span class="tag inherit inh">same as team</span>
    case 'narrows':
      return <span class="tag inherit nar">narrows</span>
    case 'stricter':
      return <span class="tag inherit inh">stricter or equal</span>
    default:
      return <span class="tag inherit inh">own</span>
  }
}

const CAPF: [string, string][] = [
  ['per_purchase', 'A purchase'],
  ['per_day', 'A day (24 hours)'],
  ['per_month', '30 days'],
]

// scopeCell is a list scope's cell: what the bot reaches, and how that sits
// against the team's scope ("any" when the team sets none).
function scopeCell(v: string[], own: string[] | undefined, team: string[] | undefined, everything: string[]): JSX.Element {
  const scope = team && team.length ? team : everything
  const same = scope.length > 0 && v.length === scope.length && v.every((x) => scope.includes(x))
  const b: Badge = own && own.length && team && team.length && !subset(own, team) ? 'wider' : same ? 'equal' : 'narrows'
  return (
    <>
      <span class="mono">{v.join(', ') || 'none'}</span> <BadgeTag b={b} />
    </>
  )
}

const budgetWords = (b: Tree) =>
  Object.entries(b || {})
    .filter(([, v]) => v)
    .map(([k, v]) => `${k.replace(/^max_|_per_day$/g, '')} ${v}`)
    .join(' · ')

export function TeamDefaults({ ctx }: { ctx: TeamCtx }): JSX.Element {
  const { name, c, tree, members, bots, nm, editable } = ctx
  const tc = getPath(c, 'budget.purchases')
  const ceiling: string = getPath(c, 'autonomy.ceiling') || 'reversible'
  const pool = getPath(c, 'budget.proactive')
  const teamOps: string[] = c.operators || []
  const teamShadow = getPath(c, 'autonomy.shadow') === true
  const teamSandbox: string = getPath(c, 'isolation.sandbox') || 'optional'
  const teamSpend = getPath(c, 'budget.spend_cap')
  const teamCreds: string[] | undefined = getPath(c, 'browser.credentials')
  const teamLogins: string[] | undefined = getPath(c, 'browser.logins')
  const cfgOf = (m: string): Tree => tree.bots?.[m] || {}
  const off = editable ? undefined : 'This dashboard can’t change the config'
  // What a bot's browser sessions really reach (internal/browser ForPrincipal):
  // the logins and credentials bound to it or its team, capped by its own list
  // (or, when it sets none, the team's — the narrowing it inherits).
  const keyOf = (kind: 'logins' | 'credentials', e: Tree): string => (kind === 'logins' ? e?.site : e?.name) || ''
  const all = (kind: 'logins' | 'credentials'): string[] => (getPath(tree, 'browser.' + kind) || []).map((e: Tree) => keyOf(kind, e)).filter(Boolean)
  const reach = (kind: 'logins' | 'credentials', m: string): string[] => {
    const cap: string[] | undefined = getPath(cfgOf(m), 'browser.' + kind) || getPath(c, 'browser.' + kind)
    const ids = ['bot:' + m, 'team:' + name]
    return (getPath(tree, 'browser.' + kind) || [])
      .filter((e: Tree) => (e?.identities || []).some((p: string) => ids.includes(p) || (p.endsWith('*') && p.length > 1 && ('bot:' + m).startsWith(p.slice(0, -1)))))
      .map((e: Tree) => keyOf(kind, e))
      .filter((k: string) => k && (!cap || cap.includes(k)))
  }

  const row = (label: string, teamV: string, cell: (m: string, b: Tree) => JSX.Element) => (
    <tr>
      <td data-l="setting">
        <b>{label}</b>
      </td>
      <td data-l="team" class="mono small">
        {teamV}
      </td>
      {members.map((m) => (
        <td data-l={m} class="small">
          {cell(m, cfgOf(m))}
        </td>
      ))}
    </tr>
  )

  return (
    <>
      <section class="dl-card wl-team" data-testid="team-wallet">
        <header class="dl-ch">
          <h2>What its bots may spend</h2>
          <span class="dl-sp"></span>
          <button class="btn sm secondary" type="button" disabled={!!off} title={off} onClick={() => ctx.open(<CapsForm ctx={ctx} />)} data-testid="team-caps">
            {tc ? 'Change' : 'Set caps'}
          </button>
        </header>
        <p>
          {tc ? (
            <>
              <b>Can buy:</b> each bot up to {money(tc.per_purchase)} a purchase, {money(tc.per_day)} a day and {money(tc.per_month)} in 30 days, as a ceiling: a bot’s own caps, a card’s limits and a ticket’s spending limit can only be lower.
            </>
          ) : (
            'No team caps: each bot is held to its own caps and the cards’ limits.'
          )}
        </p>
        {tc ? (
          <ul class="dl-list wl-teamb">
            {members.map((m) => {
              const pc = cfgOf(m).purchases
              return (
                <li class="wl-audit">
                  <span>
                    <Avatar id={bots[m]?.identity} size={20} /> <b>{nm(m)}</b> {pc ? `its own: ${money(pc.per_purchase)} · ${money(pc.per_day)} · ${money(pc.per_month)}` : 'uses the team’s caps'}
                  </span>
                </li>
              )
            })}
          </ul>
        ) : null}
      </section>
      <div class="callout small" style="margin-bottom:12px">
        A bot on a team can only be <b>stricter</b>: approvers from the team's list, promotions no higher than the ceiling, the team's tier classifications and sandbox, sweeps inside the shared pool, logins inside the team's scope. Anything wider is refused when the config is validated, naming the team value it breaks.
      </div>
      <div class="panel">
        <div class="panel-head">
          <h3>Team values and what each bot does with them</h3>
          <div class="tools">
            <button class="btn sm" type="button" disabled={!!off} title={off} onClick={ctx.openTeamForm}>
              <Icon name="edit" />
              Edit team
            </button>
          </div>
        </div>
        <table class="tbl responsive inherit-tbl" data-testid="team-inherit">
          <thead>
            <tr>
              <th>setting</th>
              <th>team</th>
              {members.map((m) => (
                <th>
                  <a href={`#/bots/${m}/settings`}>{nm(m)}</a>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {row('approvers', teamOps.join(', ') || 'every operator', (_m, b) => {
              const own: string[] | undefined = b.operators
              const v = own?.length ? own : teamOps
              return (
                <>
                  <span class="mono">{v.join(', ') || '—'}</span> <BadgeTag b={listBadge(own, teamOps)} />
                </>
              )
            })}
            {row('What its bots may do on their own', riskWords(ceiling), (_m, b) => {
              const pr: string[] = getPath(b, 'autonomy.promote') || []
              const un = getPath(b, 'autonomy.unattended')
              const wider = (ceiling === 'observe' && pr.length > 0) || (un && Object.keys(un).length > 0 && ceiling !== 'commit_money')
              return (
                <>
                  <span class="mono">{pr.join(', ') || 'nothing promoted'}</span> <BadgeTag b={wider ? 'wider' : pr.length ? 'own' : 'inherits'} />
                </>
              )
            })}
            {row(
              'How risky each action is',
              Object.entries(getPath(c, 'autonomy.tiers') || {})
                .map(([k, v]) => `${riskWords(k)}: ${([] as string[]).concat(v as string[]).join(', ')}`)
                .join('; ') || '—',
              (_m, b) => {
                const te = tierErrors(name, getPath(c, 'autonomy.tiers'), getPath(b, 'autonomy.tiers'))
                return te.length ? (
                  <>
                    <BadgeTag b="wider" /> <span class="tiny">{te[0]}</span>
                  </>
                ) : (
                  <BadgeTag b="stricter" />
                )
              },
            )}
            {row('Practice mode', teamShadow ? 'every bot practises' : 'off', (_m, b) => {
              const v = getPath(b, 'autonomy.shadow')
              const src: Badge = v === undefined ? 'inherits' : 'own'
              return (
                <>
                  <span>{v === undefined ? (teamShadow ? 'practising' : '—') : v ? 'practising' : 'off'}</span> <BadgeTag b={teamShadow && v === false ? 'wider' : src} />
                </>
              )
            })}
            {row('agent sandbox', teamSandbox, (_m, b) => {
              const v: string | undefined = getPath(b, 'isolation.sandbox')
              return (
                <>
                  <span class="mono">{v || teamSandbox}</span> <BadgeTag b={teamSandbox === 'required' && v === 'optional' ? 'wider' : !v ? 'inherits' : v === teamSandbox ? 'equal' : 'narrows'} />
                </>
              )
            })}
            {row('proactive sweeps (shared pool)', pool ? budgetWords(pool) || '—' : '—', (_m, b) => {
              const bb = getPath(b, 'proactive.budget')
              if (!bb) return <span class="muted">{getPath(b, 'proactive') ? 'uses the pool' : 'no sweeps'}</span>
              const over = pool && Object.entries(bb).some(([k, v]) => pool[k] && Number(v) > Number(pool[k]))
              return (
                <>
                  <span class="mono">{budgetWords(bb)}</span> <BadgeTag b={over ? 'wider' : pool ? 'narrows' : 'own'} />
                </>
              )
            })}
            {row('cloud spend cap', teamSpend ? `${teamSpend.per_action ?? '—'} / action` : '—', (_m, b) => {
              const own = getPath(b, 'autonomy.spend_cap')
              const v = own || teamSpend
              const over = own && teamSpend && (Number(own.per_action) > Number(teamSpend.per_action) || Number(own.per_period) > Number(teamSpend.per_period))
              return (
                <>
                  <span class="mono">{v ? `${v.per_action ?? '—'} / action` : '—'}</span> <BadgeTag b={over ? 'wider' : !own ? (teamSpend ? 'inherits' : 'unset') : teamSpend ? 'narrows' : 'own'} />
                </>
              )
            })}
            {row('sign-in credentials', (teamCreds || []).join(', ') || 'any', (m, b) => scopeCell(reach('credentials', m), getPath(b, 'browser.credentials'), teamCreds, all('credentials')))}
            {row('saved logins', (teamLogins || []).join(', ') || 'any', (m, b) => scopeCell(reach('logins', m), getPath(b, 'browser.logins'), teamLogins, all('logins')))}
          </tbody>
        </table>
        {members.length ? null : <div class="panel-foot small muted">No bots on this team yet: the team's values apply to each one you add.</div>}
      </div>
    </>
  )
}

// CapsForm is the prototype's w-caps for a team: three caps in dollars, a
// ceiling for every bot of the team; staged as teams.<n>.budget.purchases.
function CapsForm({ ctx }: { ctx: TeamCtx }): JSX.Element {
  const { name, c, tree, members } = ctx
  const cur = getPath(c, 'budget.purchases')
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(CAPF.map(([k]) => [k, cur?.[k] != null ? Number(cur[k]).toFixed(2) : ''])))
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [why, setWhy] = useState('')
  const num = (k: string) => {
    const v = String(vals[k] || '').replace(/[$,\s]/g, '')
    return /^\d+(\.\d{1,2})?$/.test(v) ? Number(v) : NaN
  }
  const stageCaps = async (next: Record<string, number> | null) => {
    ctx.close()
    const path = ['teams', name, 'budget', 'purchases']
    await ctx.stageEdits(
      [[path, next ? { ...(cur?.cards ? { cards: cur.cards } : {}), ...next, currency: 'USD' } : undefined]],
      `team ${name}: purchase caps ${next ? CAPF.map(([k]) => next[k]).join(' / ') : 'removed'}`,
    )
  }
  const go = () => {
    const v: Record<string, number> = {}
    const e: Record<string, string> = {}
    for (const [k] of CAPF) {
      v[k] = num(k)
      if (!(v[k] > 0)) e[k] = 'An amount in dollars, above 0.'
    }
    setErrs(e)
    if (Object.keys(e).length) return
    const floor = members.filter((m) => {
      const p = tree.bots?.[m]?.purchases
      return p && CAPF.some(([k]) => Number(p[k]) > v[k])
    })
    const w =
      v.per_purchase > v.per_day || v.per_day > v.per_month
        ? 'A purchase fits inside a day, and a day inside 30 days.'
        : floor.length
          ? `Below ${floor.map(ctx.nm).join(', ')}’s own caps: lower theirs first.`
          : ''
    setWhy(w)
    if (!w) void stageCaps(v)
  }
  return (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true" data-testid="team-caps-form">
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            <div class="caps">team {name} · budget.purchases</div>
            <h2>What its bots may spend</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={ctx.close}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body stack">
          <p class="dl-sub">A ceiling for every bot on the team: a bot’s own caps can only be lower. All three are needed.</p>
          <div class="wl-grid3">
            {CAPF.map(([k, l]) => (
              <div class={'field' + (errs[k] ? ' bad' : '')} data-field={k}>
                <label for={`w-m-${k}`}>
                  {l}{' '}
                  <span class="req" aria-label="required">
                    *
                  </span>
                </label>
                <div class="wl-money">
                  <span aria-hidden="true">$</span>
                  <input class="input mono" id={`w-m-${k}`} inputMode="decimal" autocomplete="off" placeholder="Amount" value={vals[k]} onInput={(e) => setVals((o) => ({ ...o, [k]: (e.target as HTMLInputElement).value }))} />
                </div>
                <div class="ferr" role="alert">
                  {errs[k] || ''}
                </div>
              </div>
            ))}
          </div>
          <div class={'form-err callout danger small' + (why ? '' : ' hide')} role="alert">
            {why}
          </div>
        </div>
        <div class="modal-foot">
          {cur ? (
            <button class="btn ghost danger-text" type="button" style="margin-right:auto" onClick={() => void stageCaps(null)}>
              No team caps
            </button>
          ) : null}
          <button class="btn ghost" type="button" onClick={ctx.close}>
            Cancel
          </button>
          <button class="btn primary" type="button" onClick={go} data-testid="team-caps-go">
            Review change
          </button>
        </div>
      </div>
    </div>
  )
}
