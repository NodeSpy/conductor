// A bot's "What it can do" tab (prototype js/screens/bots.js access()): what it
// does on its own, asks you first and never does, each item's menu moving it
// or taking it away (each a staged config change you review and apply); the
// sites it can sign in to, its saved browser sessions, and who approves what
// it asks.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import type { BotDetail as BotData } from '../api/bots'
import { displayName } from '../api/bots'
import { browser, walletOp, type LoginRecord } from '../api/browser'
import { authState } from '../api/auth'
import { humanError } from '../api/errors'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { FormModal, ConfirmModal } from '../components/FormModal'
import { config, staged, loadConfig, stage, stageDelete, cfgError } from '../configStore'
import { go } from '../router'
import { ago } from '../lib/format'
import { BotWallet } from './BotWallet'
import {
  type Obj,
  type Mode,
  type BotVerb,
  clone,
  withStaged,
  tierFor,
  ceilingOf,
  verbMode,
  verbsOf,
  shadowOf,
  loadVerbs,
  verbLabel,
  cap,
  getPath,
} from './botConfig'

const GW: Record<Mode, string> = { own: 'Does on its own', asks: 'Asks you first', never: 'Never' }

interface Item {
  v: string
  text: string
  moveTo?: Mode
  removed?: boolean
  added?: boolean
}

function groups(bot: Obj, team: Obj | undefined, verbs: BotVerb[]): Record<Mode, Item[]> {
  const out: Record<Mode, Item[]> = { own: [], asks: [], never: [] }
  for (const v of verbsOf(bot)) out[verbMode(bot, team, v)].push({ v, text: verbLabel(v, verbs) })
  return out
}

const where = (g: Record<Mode, Item[]>, v: string): Mode | undefined => (['own', 'asks', 'never'] as Mode[]).find((k) => g[k].some((i) => i.v === v))

// setMode moves a verb between the groups (the prototype's setMode): never,
// asks first (propose, or commit_money for spending), or on its own
// (reversible + promoted; reading stays observe).
function setMode(c: Obj, team: Obj | undefined, v: string, mode: Mode): void {
  const A = (c.autonomy = c.autonomy || {})
  const T2: Obj = (A.tiers = A.tiers || {})
  const cur = tierFor(c, team, v).tier
  for (const t of Object.keys(T2)) {
    T2[t] = (T2[t] || []).filter((x: string) => x !== v)
    if (!T2[t].length) delete T2[t]
  }
  A.promote = (A.promote || []).filter((x: string) => x !== v)
  const tier = mode === 'never' ? 'never' : mode === 'asks' ? (cur === 'commit_money' ? 'commit_money' : 'propose') : cur === 'observe' ? 'observe' : 'reversible'
  ;(T2[tier] = T2[tier] || []).push(v)
  if (mode === 'own' && tier === 'reversible') A.promote.push(v)
  if (!A.promote.length) delete A.promote
  if (!Object.keys(T2).length) delete A.tiers
}

function addVerb(c: Obj, verb: string): void {
  c.skill = c.skill || {}
  const v = c.skill.verbs
  if (v && !Array.isArray(v)) v[verb] = {}
  else c.skill.verbs = (v || []).concat([verb])
}

function dropVerb(c: Obj, verb: string): void {
  const v = getPath(c, 'skill.verbs') as Obj | string[] | undefined
  if (v && !Array.isArray(v)) delete (v as Obj)[verb]
  else if (v) c.skill.verbs = (v as string[]).filter((x) => x !== verb)
  const A = c.autonomy
  if (!A) return
  for (const t of Object.keys(A.tiers || {})) {
    A.tiers[t] = A.tiers[t].filter((x: string) => x !== verb)
    if (!A.tiers[t].length) delete A.tiers[t]
  }
  if (A.tiers && !Object.keys(A.tiers).length) delete A.tiers
  if (A.promote) {
    A.promote = A.promote.filter((x: string) => x !== verb)
    if (!A.promote.length) delete A.promote
  }
}

const fieldRank = (k: string) => { const i = Object.keys(FIELD_WORDS).indexOf(k); return i < 0 ? 99 : i }
const FIELD_WORDS: Record<string, string> = { username: 'a username', password: 'a password', otp: 'a one-time code', passkey: 'a passkey', email: 'an email address' }

// SavedSession is one saved cookie jar (browser-login-list's saved), no value.
interface SavedSession {
  identity: string
  site: string
  saved: string
  cookies?: number
}

type Modal =
  | { kind: 'add' }
  | { kind: 'bind' }
  | { kind: 'unbind'; login: LoginRecord }
  | { kind: 'logout'; site: string; identity: string }
  | null

export function BotAccess({ bot }: { bot: BotData }): JSX.Element {
  const name = bot.bot
  const id = 'bot:' + name
  const [verbs, setVerbs] = useState<BotVerb[]>([])
  const [logins, setLogins] = useState<LoginRecord[] | null>(null)
  const [saved, setSaved] = useState<SavedSession[]>([])
  const [menu, setMenu] = useState<{ el: HTMLElement; items: MenuItem[] } | null>(null)
  const [modal, setModal] = useState<Modal>(null)
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  const reloadLogins = () =>
    browser
      .logins()
      .then((r) => {
        setLogins(r.logins || [])
        setSaved((r as { saved?: SavedSession[] | null }).saved || [])
      })
      .catch(() => setLogins([]))
  useEffect(() => {
    void loadConfig()
    void loadVerbs().then(setVerbs)
    void reloadLogins()
  }, [name])

  const tree = (config.value?.tree || {}) as Obj
  const next = withStaged(tree, staged.value)
  const liveBot = (tree.bots?.[name] || {}) as Obj
  const nextBot = (next.bots?.[name] || liveBot) as Obj
  const liveTeam = liveBot.team ? (tree.teams?.[liveBot.team] as Obj) : undefined
  const nextTeam = nextBot.team ? (next.teams?.[nextBot.team] as Obj) : undefined
  const teamName = (nextBot.team as string) || ''
  const ceiling = ceilingOf(nextBot, nextTeam)
  const stagedM = staged.value.some((c) => c.path[0] === 'bots' && c.path[1] === name)
  const label = displayName(bot.identity, name)

  // the groups show what's in force; a staged change shows in place, marked
  // not applied yet (a move says where to, a new one sits in its group as new,
  // a removal stays, struck through)
  const live = groups(liveBot, liveTeam, verbs)
  const nxt = groups(nextBot, nextTeam, verbs)
  const x: Record<Mode, Item[]> = { own: [], asks: [], never: [] }
  for (const g of ['own', 'asks', 'never'] as Mode[]) {
    for (const it of live[g]) {
      const to = where(nxt, it.v)
      x[g].push({ ...it, ...(to === g ? {} : to ? { moveTo: to } : { removed: true }) })
    }
  }
  for (const g of ['own', 'asks', 'never'] as Mode[]) for (const it of nxt[g]) if (!where(live, it.v)) x[g].push({ ...it, added: true })
  const shadow = shadowOf(liveBot, liveTeam)

  async function editM(fn: (c: Obj) => void, summary: string) {
    const c = clone(nextBot)
    fn(c)
    setNote('')
    const before = nextBot
    if (JSON.stringify(c.autonomy ?? null) !== JSON.stringify(before.autonomy ?? null)) {
      if (c.autonomy && Object.keys(c.autonomy).length) await stage(['bots', name, 'autonomy'], c.autonomy, summary)
      else await stageDelete(['bots', name, 'autonomy'], summary)
    }
    if (!cfgError.value && JSON.stringify(c.skill ?? null) !== JSON.stringify(before.skill ?? null)) {
      await stage(['bots', name, 'skill'], c.skill, summary)
    }
  }

  function openItemMenu(el: HTMLElement, v: string) {
    const mode = verbMode(nextBot, nextTeam, v)
    const tf = tierFor(nextBot, nextTeam, v)
    const looser = (t: string) => !!tf.team && ['observe', 'reversible', 'propose', 'commit_money', 'never'].indexOf(t) < ['observe', 'reversible', 'propose', 'commit_money', 'never'].indexOf(tf.team)
    const ownOff =
      mode === 'own'
        ? 'It already does this on its own'
        : tf.tier === 'commit_money'
          ? 'Spending money always asks you first'
          : looser('reversible')
            ? `Team ${teamName} keeps this at “asks you first”`
            : ceiling === 'observe' && tf.tier !== 'observe'
              ? `On team ${teamName} a bot does on its own only things that read`
              : ''
    const asksOff = mode === 'asks' ? 'It already asks first' : looser('propose') ? `Team ${teamName} never allows this` : ''
    const move = (m: Mode) => () => void editM((c) => setMode(c, nextTeam, v, m), `${name}: ${v} → ${GW[m].toLowerCase()}`)
    setMenu({
      el,
      items: [
        { label: 'Do it on its own', icon: 'check', disabled: ownOff || false, onClick: move('own') },
        { label: 'Ask me first', icon: 'hand', disabled: asksOff || false, onClick: move('asks') },
        { label: 'Never', icon: 'x', disabled: mode === 'never' ? 'It already never does this' : false, onClick: move('never') },
        '-',
        { label: 'Take it away', icon: 'trash', onClick: () => void editM((c) => dropVerb(c, v), `${name}: take away ${v}`) },
      ],
    })
  }

  const pend = (it: Item) =>
    it.moveTo ? (
      <span class="can-pend"><StatusMark s="staged" mkOnly label="Not applied yet" />→ {GW[it.moveTo]} · not applied yet</span>
    ) : it.removed ? (
      <span class="can-pend"><StatusMark s="staged" mkOnly label="Not applied yet" />Taken away · not applied yet</span>
    ) : it.added ? (
      <span class="can-pend"><StatusMark s="staged" mkOnly label="Not applied yet" />New · not applied yet</span>
    ) : null
  const item = (it: Item) => (
    <li class={'can-item' + (it.moveTo || it.removed || it.added ? ' pending' : '') + (it.removed ? ' removed' : '')} data-verb={it.v}>
      <div>
        <span>{cap(it.text)}</span>
        <span class="can-id mono">{it.v}</span>
        {pend(it)}
      </div>
      <button class="icon-btn" type="button" aria-label={`Change what ${name} does with ${it.v}`} onClick={(e) => openItemMenu(e.currentTarget as HTMLElement, it.v)}>
        <Icon name="more" />
      </button>
    </li>
  )
  const grp = (h: string, s: string, items: Item[], empty: string) => (
    <div class="cang" data-s={s}>
      <h3><StatusMark s={s} mkOnly label={h} />{h}</h3>
      <ul>{items.length ? items.map(item) : <li class="dl-sub">{empty}</li>}</ul>
    </div>
  )

  // --- logins ---
  const all = logins || []
  const nextCreds = ((getPath(next, 'browser.credentials') as Obj[]) || []) as Obj[]
  const credIdx = (n: string) => nextCreds.findIndex((c) => c.name === n)
  const bindingsOf = (l: LoginRecord): string[] => (l.method === 'config' && credIdx(l.name) >= 0 ? nextCreds[credIdx(l.name)].identities || [] : l.bindings || [])
  const sites = all.filter((l) => l.method !== 'session' && bindingsOf(l).includes(id))
  const others = all.filter((l) => l.method !== 'session' && !bindingsOf(l).includes(id))
  const sessions = saved.filter((x) => x.identity === id)
  const ops: string[] = (nextBot.operators as string[])?.length ? nextBot.operators : ((nextTeam?.operators as string[]) || [])
  const me = authState.value.operator || ''

  async function bind(n: string) {
    const l = all.find((x) => x.name === n)
    if (!l) return
    if (l.method === 'config') {
      const i = credIdx(n)
      if (i < 0) return 'That login isn’t in the config any more.'
      await stage(['browser', 'credentials', String(i), 'identities'], [...(nextCreds[i].identities || []), id], `let ${name} use login ${n}`)
      if (cfgError.value) return cfgError.value
      return
    }
    try {
      await walletOp('browser-login-bind', { name: n, bind: [id] })
      setNote(`${label} can use ${n} now.`)
      void reloadLogins()
    } catch (e) {
      return humanError(e)
    }
  }

  async function unbind(l: LoginRecord) {
    setErr('')
    if (l.method === 'config') {
      const i = credIdx(l.name)
      if (i < 0) return
      const rest = (nextCreds[i].identities || []).filter((x: string) => x !== id)
      if (rest.length) await stage(['browser', 'credentials', String(i), 'identities'], rest, `stop ${name} using login ${l.name}`)
      else await stageDelete(['browser', 'credentials', String(i)], `remove login ${l.name}`)
      return
    }
    try {
      await walletOp('browser-login-bind', { name: l.name, unbind: [id] })
      setNote(`${label} no longer uses ${l.name}.`)
      void reloadLogins()
    } catch (e) {
      setErr(humanError(e))
    }
  }

  async function logout(site: string, identity: string) {
    setErr('')
    try {
      await walletOp('browser-logout', { identity, site })
      setNote(`Logged out of ${site}: the saved session is deleted.`)
      void reloadLogins()
    } catch (e) {
      setErr(humanError(e))
    }
  }

  const unbindRest = (l: LoginRecord) => bindingsOf(l).filter((x) => x !== id)
  const addable = verbs
    .map((v) => v.verb)
    .concat([...new Set(verbs.map((v) => v.connection))].map((c) => `${c}.*`))
    .filter((v) => !verbsOf(nextBot).includes(v))
    .sort()

  return (
    <>
      <section class="dl-card dl-can can-edit">
        <header class="dl-ch">
          <h2>What {label} can do</h2>
          <span class="dl-sp" />
          <button class="btn sm secondary" type="button" data-testid="acc-addverb" onClick={() => setModal({ kind: 'add' })}>
            <Icon name="plus" />Allow something new…
          </button>
        </header>
        {stagedM ? (
          <p class="can-pending-bar">
            <StatusMark s="staged" mkOnly label="Not applied yet" />
            <span>Your change isn’t applied yet: what’s below is what {label} may do now.</span>
            <a class="btn sm" href="#/system/config">Review and apply</a>
          </p>
        ) : null}
        {shadow ? <p class="dl-sub">It’s in practice mode: even what it does on its own is only recorded, never done.</p> : null}
        {cfgError.value ? <div class="callout danger small" role="alert">{cfgError.value}</div> : null}
        <div class="dl-can-grid">
          {grp('Does on its own', 'ok', x.own, 'Nothing: it asks first')}
          {grp('Asks you first', 'ember', x.asks, 'Nothing needs your approval')}
          {grp('Never', 'fail', x.never, 'Nothing is refused outright')}
        </div>
        <SpendLine spend={bot.spend} />
        <p class="dl-sub" style={{ margin: '12px 0 0' }}>
          Use an item’s menu to move it or take it away; each change waits for your review
          {teamName ? `, and on the ${teamName} team a bot does on its own only things that ${ceiling === 'observe' ? 'just read' : 'can be undone'}` : ''}.
          {stagedM ? <> <a class="dl-link" href="#/system/config">Review the change</a></> : null}
        </p>
      </section>

      <BotWallet bot={name} team={bot.team} />

      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      {note ? <div class="callout ok small" role="status">{note}</div> : null}

      <div class="acc-stack">
        <div class="stack">
          <div class="panel" data-testid="acc-sites">
            <div class="panel-head">
              <h3>Sites it can sign in to</h3>
              <div class="tools">
                <button class="btn sm" type="button" onClick={() => setModal({ kind: 'bind' })}><Icon name="plus" />Let it use a login</button>
              </div>
            </div>
            <div class="list">
              {sites.length ? (
                sites.map((l) => (
                  <div class="item acc-row" data-login={l.name}>
                    <div class="dl-nt">
                      <b>{l.name} <span class="dl-sub">{(l.origins || [])[0] || l.site || ''}</span></b>
                      <span class="dl-why">
                        {Object.keys(l.fields || {}).length
                          ? 'signs in with ' + Object.keys(l.fields || {}).sort((a, b) => fieldRank(a) - fieldRank(b)).map((k) => FIELD_WORDS[k] || `a ${k}`).join(', ')
                          : 'nothing to sign in with yet'}
                      </span>
                    </div>
                    <span class="dl-na">
                      <a class="btn sm secondary" href="#/browser/logins">Edit the login</a>
                      <button
                        class="icon-btn"
                        type="button"
                        aria-label={`More for ${l.name}`}
                        onClick={(e) =>
                          setMenu({ el: e.currentTarget as HTMLElement, items: [{ label: `Stop ${name} using it…`, icon: 'x', onClick: () => setModal({ kind: 'unbind', login: l }) }] })
                        }
                      >
                        <Icon name="more" />
                      </button>
                    </span>
                  </div>
                ))
              ) : (
                <div class="item small muted">{logins === null ? 'Loading…' : 'No sign-in credentials name this bot.'}</div>
              )}
            </div>
          </div>
          <div class="panel" data-testid="acc-sessions">
            <div class="panel-head">
              <h3>Saved browser sessions</h3>
              <div class="tools">
                <a class="btn sm" href="#/browser/add"><Icon name="key" />Add a login</a>
              </div>
            </div>
            <div class="list">
              {sessions.length ? (
                sessions.map((s) => (
                  <div class="item" data-site={s.site}>
                    <span class="icon-btn" aria-hidden="true"><Icon name="cookie" /></span>
                    <div class="grow">
                      <b>{s.site}</b>
                      <div class="mono tiny muted">
                        {s.cookies != null ? `${s.cookies} cookie${s.cookies === 1 ? '' : 's'} · ` : ''}seeded {ago(s.saved)} · operator requests only
                      </div>
                    </div>
                    <button class="btn sm secondary" type="button" onClick={() => setModal({ kind: 'logout', site: s.site, identity: s.identity })}>Remove…</button>
                  </div>
                ))
              ) : (
                <div class="item small muted">No saved logins.</div>
              )}
            </div>
          </div>
          <div class="panel" data-testid="acc-approvers">
            <div class="panel-head"><h3>Who approves what it asks</h3></div>
            <div class="list">
              {ops.length ? (
                ops.map((o) => (
                  <div class="item">
                    <span class="avatar">{o.slice(0, 2)}</span>
                    <b>{o}</b>
                    <span class="grow" />
                    {o === me ? <span class="tag">you</span> : null}
                  </div>
                ))
              ) : (
                <div class="item small muted">No operator is named yet: set its operators in Settings.</div>
              )}
            </div>
          </div>
        </div>
      </div>

      {menu ? <Menu anchor={menu.el} items={menu.items} onClose={() => setMenu(null)} /> : null}

      {modal?.kind === 'add' ? (
        <FormModal
          caps={`bot ${name}`}
          title="Allow something new"
          intro="Pick what it may do, and whether it asks you first. Giving a bot something new widens what it can do, so applying asks you to confirm it's you."
          testid="acc-add"
          fields={[
            {
              k: 'verb',
              type: 'enum',
              req: true,
              label: 'What it may do',
              options: addable.map((v) => [v, `${cap(verbLabel(v, verbs))} (${v})`] as [string, string]),
              create: { label: 'Add a connection', onCreate: () => go('connections') },
            },
            { k: 'mode', type: 'seg', req: true, label: 'How', options: [['asks', 'Ask me first'], ['own', 'On its own']], def: 'asks' },
          ]}
          submit="Review change"
          onSubmit={async (v) => {
            await editM((c) => {
              addVerb(c, v.verb)
              setMode(c, nextTeam, v.verb, (v.mode as Mode) || 'asks')
            }, `${name}: allow ${v.verb}`)
            if (cfgError.value) return cfgError.value
          }}
          onClose={() => setModal(null)}
        />
      ) : null}

      {modal?.kind === 'bind' ? (
        <FormModal
          caps="browser logins"
          title={`Let ${name} use a login`}
          testid="acc-bind"
          fields={[
            {
              k: 'cred',
              type: 'enum',
              req: true,
              label: 'Login',
              options: others.map((c) => [c.name, `${c.name} · ${(c.origins || [])[0] || c.site || ''}`] as [string, string]),
              create: { label: 'Add a login', onCreate: () => go('browser/add') },
            },
          ]}
          note="Adds the bot to that login’s identities. Used only on work you started. A team that lists its logins must list this one too."
          submit="Review change"
          submitIcon="check"
          onSubmit={(v) => bind(v.cred)}
          onClose={() => setModal(null)}
        />
      ) : null}

      {modal?.kind === 'unbind' ? (
        <ConfirmModal
          title={`Stop ${name} using ${modal.login.name}?`}
          body={
            modal.login.method === 'config'
              ? unbindRest(modal.login).length
                ? `Removes ${id} from its identities; ${unbindRest(modal.login).join(', ')} keep it. The vault entries stay.`
                : 'No other identity uses it, so this removes the login itself. The vault entries stay.'
              : unbindRest(modal.login).length
                ? `Removes ${id} from its identities; ${unbindRest(modal.login).join(', ')} keep it. Its stored values stay.`
                : 'No other identity uses it any more; the login and its stored values stay until you remove it.'
          }
          ok={modal.login.method === 'config' ? 'Stage it' : 'Stop it'}
          cancel="Keep it"
          danger
          onOk={() => unbind(modal.login)}
          onClose={() => setModal(null)}
        />
      ) : null}

      {modal?.kind === 'logout' ? (
        <ConfirmModal
          title={`Log out of ${modal.site}?`}
          body="Deletes the saved cookie jar now (browser logout). Immediate, audited."
          ok="Revoke"
          danger
          onOk={() => logout(modal.site, modal.identity)}
          onClose={() => setModal(null)}
        />
      ) : null}
    </>
  )
}

// SpendLine is the prototype's "Cloud spend it caused this period" sentence,
// from what the daemon recorded against the bot in its spend period. With no
// record it says so in the same spot rather than dropping the line.
function SpendLine({ spend }: { spend: BotData['spend'] }): JSX.Element {
  const money = (n: number, cur: string): string => {
    try {
      return new Intl.NumberFormat(undefined, { style: 'currency', currency: cur }).format(n)
    } catch {
      return `${n.toFixed(2)} ${cur}`
    }
  }
  let body: JSX.Element
  if (!spend) body = <>Cloud spend it caused this period: not available from this daemon.</>
  else if (!spend.entries && !spend.spent) body = <>Cloud spend it caused this period: none recorded{spend.cap == null ? ', and it has no limit for spending money, so it can’t spend any' : ''}.</>
  else {
    const within = spend.cap == null ? '' : spend.spent <= spend.cap ? ', within its limit for spending money' : ', over its limit for spending money'
    body = <>Cloud spend it caused this period: <b>{money(spend.spent, spend.currency)}</b>{within}.</>
  }
  return (
    <p class="small sentence" style={{ margin: '12px 0 0' }} data-cloud-spend data-testid="acc-spend">
      <Icon name="dollar" />{body}
    </p>
  )
}
