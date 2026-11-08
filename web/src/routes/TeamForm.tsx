// The team form (prototype js/screens/bots.js teamForm): New team and Edit
// team are one modal over conductor's teams.<t> keys. It stages only what you
// changed (the node built from the values the form opened with, diffed against
// the node built from what you submit), then leads to Review & apply. Also here:
// a team's picture (identity.js UI.teamPicker: initials, an icon in a colour, or
// an uploaded picture through the avatar plan/apply API) and removing a team.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { authState } from '../api/auth'
import { botsApi, displayName, type BotSummary } from '../api/bots'
import { api } from '../api/client'
import { humanError, readResponse } from '../api/errors'
import { AvatarPreview } from '../components/Avatar'
import { ConfirmModal } from '../components/FormModal'
import { Icon } from '../components/Icon'
import { cfgError, config, loadConfig, stage, stageDelete } from '../configStore'
import { COLORS, ICONS } from '../lib/avatar'

/* eslint-disable @typescript-eslint/no-explicit-any */
type Obj = Record<string, any>
const isObj = (o: unknown): o is Obj => !!o && typeof o === 'object' && !Array.isArray(o)
const clone = <T,>(o: T): T => JSON.parse(JSON.stringify(o ?? {}))
const getPath = (o: Obj | undefined, p: string): any => p.split('.').reduce<any>((a, k) => (a == null ? undefined : a[k]), o)

export function cfgTree(): Obj {
  return ((config.value as any)?.tree as Obj) || {}
}

// stageDiff stages each key of `base` whose built value changed from `a` to
// `b`: only what changed is touched (other keys, and keys the caller didn't
// build, stay). A changed collection is staged whole onto its live value, so a
// config written in flow style (`autonomy: { ceiling: observe }`), which can't
// take an edit at a single leaf, still edits cleanly.
export async function stageDiff(base: string[], a: Obj, b: Obj, summary: string, live: Obj = {}): Promise<number> {
  let n = 0
  for (const k of new Set([...Object.keys(a || {}), ...Object.keys(b || {})])) {
    const x = (a || {})[k]
    let y = (b || {})[k]
    if (JSON.stringify(x) === JSON.stringify(y)) continue
    if (isObj(x) && isObj(y) && isObj(live[k])) y = mergeDiff(clone(live[k]), x, y)
    if (y === undefined) {
      if (live[k] === undefined) continue
      await stageDelete([...base, k], summary)
      n++
    } else {
      await stage([...base, k], y, summary)
      n++
    }
    if (cfgError.value) return n
  }
  return n
}

// mergeDiff applies to `tgt` only what changed from `a` to `b`.
function mergeDiff(tgt: Obj, a: Obj, b: Obj): Obj {
  for (const k of new Set([...Object.keys(a || {}), ...Object.keys(b || {})])) {
    const x = (a || {})[k]
    const y = (b || {})[k]
    if (JSON.stringify(x) === JSON.stringify(y)) continue
    if (isObj(x) && isObj(y) && isObj(tgt[k])) mergeDiff(tgt[k], x, y)
    else if (y === undefined) delete tgt[k]
    else tgt[k] = y
  }
  return tgt
}

// toReview leads to Review & apply once a change is staged.
export function toReview(): void {
  if (!cfgError.value) location.hash = '#/system/config'
}

/* ---------------- the form's fields ---------------- */

interface TF {
  k: string
  section?: string
  type: 'string' | 'number' | 'bool' | 'enum' | 'seg' | 'select-multi' | 'list'
  label: string
  req?: boolean
  mono?: boolean
  on?: string
  ph?: string
  help?: string
  options?: [string, string][]
  create?: { label: string; href: string }
  pattern?: RegExp
  patternMsg?: string
  validate?: (v: any, all: Obj) => string
  show?: (all: Obj) => boolean
}

const empty = (x: unknown) => x == null || x === '' || (Array.isArray(x) && !x.length)
const num = (x: unknown) => (x === '' || x == null ? undefined : /^-?\d+(\.\d+)?$/.test(String(x)) ? Number(x) : x)

export function TeamForm({ team, withBots, onClose }: { team?: string; withBots?: string[]; onClose: () => void }): JSX.Element {
  const isNew = !team
  const [roster, setRoster] = useState<BotSummary[]>([])
  const [secretRefs, setSecretRefs] = useState<string[]>([])
  const [ready, setReady] = useState(!!config.value)
  useEffect(() => {
    if (!config.value) void loadConfig().then(() => setReady(true))
    botsApi.bots().then((r) => setRoster(r.bots || [])).catch(() => setRoster([]))
    api.secrets().then((r) => setSecretRefs((r.secrets || []).map((s) => s.ref))).catch(() => setSecretRefs([]))
  }, [])

  const tree = cfgTree()
  const c: Obj = isNew ? {} : clone(tree.teams?.[team!] || {})
  const g = (p: string) => getPath(c, p)
  const isApi = tree.usage?.mode === 'api'
  const initial: Obj = isNew
    ? { ceiling: 'reversible', sandbox: 'required', peers: 'team', bots: withBots || [], secrets_via: 'none', promotion: 'proposal', handoff: true }
    : {
        description: c.description,
        operators: c.operators || [],
        ceiling: g('autonomy.ceiling') || 'reversible',
        shadow: !!g('autonomy.shadow'),
        sandbox: g('isolation.sandbox') || 'optional',
        handoff: g('isolation.handoff_workers') !== false,
        peers: c.peers || 'team',
        depth: g('delegation.max_depth'),
        pool_runs: g('budget.proactive.max_runs_per_day'),
        pool_turns: g('budget.proactive.max_turns_per_day'),
        pool_tokens: g('budget.proactive.max_tokens_per_day'),
        pool_cost: g('budget.proactive.max_cost_per_day'),
        cap_action: g('budget.spend_cap.per_action'),
        cap_period: g('budget.spend_cap.per_period'),
        secrets_via: g('credentials.secrets_via') || 'none',
        allow_secrets: g('credentials.allow_secrets') || [],
        logins: g('browser.logins') || [],
        credentials: g('browser.credentials') || [],
        repo: g('knowledge.repo'),
        promotion: g('knowledge.promotion') || 'proposal',
        lead: g('routing.lead'),
        fallback: g('routing.fallback'),
      }
  const [vals, setVals] = useState<Obj>(() => clone(initial))
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busyNow, setBusy] = useState(false)
  // the form opens before the config arrives on a cold load: fill it then
  const filled = useRef(isNew || !!config.value)
  useEffect(() => {
    if (!filled.current && ready && config.value) {
      filled.current = true
      setVals(clone(initial))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready])

  const botName = (n: string) => {
    const b = roster.find((x) => x.bot === n)
    return b ? displayName(b.identity, b.bot) : n
  }
  const allBots: [string, string][] = Object.keys(tree.bots || {}).map((n) => [n, botName(n)])
  const teamBots: [string, string][] = isNew ? [] : allBots.filter(([n]) => tree.bots?.[n]?.team === team)
  const credNames: string[] = ((tree.browser?.credentials as Obj[]) || []).map((x) => x.name).filter(Boolean)
  const newBotHref = isNew ? '#/bots/new' : `#/bots/new?team=${encodeURIComponent(team!)}`

  const fields: TF[] = [
    ...(isNew
      ? [{ k: 'name', type: 'string', label: 'Name', req: true, ph: 'e.g. data', pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.', validate: (v: string) => (tree.teams?.[v] ? 'A team by that name exists.' : '') } as TF]
      : []),
    { k: 'description', type: 'string', label: 'Description', mono: false },
    ...(isNew
      ? ([
          { k: 'bots', type: 'select-multi', label: 'Bots', options: allBots, create: { label: 'New bot', href: '#/bots/new' }, help: 'A bot is on at most one team: adding one here moves it.' },
          {
            k: 'lead', type: 'enum', label: 'Lead (optional)',
            options: [['', 'No lead yet: messages and unmatched work go to everyone'] as [string, string]].concat(allBots),
            help: 'One of the bots you add. It reads what no rule matches and picks who does it; you can choose later.',
            validate: (x: string, all: Obj) => (x && !(all.bots || []).includes(x) ? 'Add this bot to the team first.' : ''),
          },
        ] as TF[])
      : []),
    { section: 'Who approves', k: 'operators', type: 'select-multi', label: 'Who approves', options: Object.keys(tree.operators || {}).map((o) => [o, o]), help: 'Who may approve what its bots ask for and what they add to its knowledge. A bot on the team can only narrow this. Empty: every operator.' },
    { section: 'What bots may do on their own', k: 'ceiling', type: 'seg', label: 'On their own, its bots may', options: [['reversible', 'do things that can be undone'], ['observe', 'only read']], help: 'The most you can let a bot do without asking. Anything that can’t be undone, or spends money, always asks you.' },
    { k: 'shadow', type: 'bool', label: 'Practice mode', on: 'every bot on the team says what it would do; nothing it does is real' },
    { k: 'sandbox', type: 'seg', label: 'Sandbox', options: [['required', 'always'], ['optional', 'when it’s on']], help: 'Always: its bots start only while conductor’s sandbox is on.' },
    { k: 'handoff', type: 'bool', label: 'Sessions you can take over', on: 'its bots may start a session a person can take over' },
    { section: 'Talking and handing off', k: 'peers', type: 'seg', label: 'Its bots', options: [['team', 'may message each other'], ['none', 'only explicit links']] },
    { k: 'depth', type: 'number', label: 'Hand-offs · how many times work may be passed on', ph: '3 (default)' },
    { section: 'Budgets', k: 'pool_runs', type: 'number', label: 'Check-ins on their own · a day (shared by its bots)' },
    { k: 'pool_turns', type: 'number', label: 'Check-ins · model replies a day' },
    { k: 'pool_tokens', type: 'string', label: 'Check-ins · tokens a day', ph: '500k' },
    { k: 'pool_cost', type: 'number', label: 'Check-ins · model spend a day (USD)', show: () => isApi },
    { k: 'cap_action', type: 'number', label: 'Cloud spending limit · per action', help: 'Real money a bot may commit in one approved action. Not model spend.' },
    { k: 'cap_period', type: 'number', label: 'Cloud spending limit · per 30 days' },
    { section: 'Shared access', k: 'secrets_via', type: 'seg', label: 'How its bots get secrets', options: [['none', 'they don’t'], ['broker', 'one use at a time, when asked'], ['env', 'in their environment']] },
    { k: 'allow_secrets', type: 'select-multi', label: 'Vault entries its bots may be given', options: secretRefs.map((r) => [r, r]), show: (v) => v.secrets_via === 'broker' },
    { k: 'logins', type: 'list', label: 'Saved logins bots may use (sites)', ph: 'console.example.com' },
    { k: 'credentials', type: 'select-multi', label: 'Sign-in credentials bots may use', options: credNames.map((n) => [n, n]), create: { label: 'Add a login', href: '#/browser/logins' } },
    { section: 'Knowledge and routing', k: 'repo', type: 'string', label: 'Where its knowledge is kept', ph: 'team-knowledge/<team>', help: "Optional. The team's shared knowledge." },
    { k: 'promotion', type: 'seg', label: 'Adding to its knowledge', options: [['proposal', 'an operator approves'], ['direct', 'lands at once']], show: (v) => !!v.repo },
    ...(isNew
      ? []
      : ([
          { k: 'lead', type: 'enum', label: 'Lead (triages what no rule matched)', options: teamBots, create: { label: 'New bot on this team', href: newBotHref } },
          { k: 'fallback', type: 'enum', label: "Fallback (when the lead can't triage)", options: teamBots },
        ] as TF[])),
  ]

  // build is the team's node from a set of values (bots.js teamForm build).
  const build = (v: Obj): Obj => {
    const n: Obj = {}
    if (v.description) n.description = v.description
    if (!isNew && c.collections) n.collections = c.collections
    if ((v.operators || []).length) n.operators = v.operators
    if (v.peers === 'none') n.peers = 'none'
    if (num(v.depth) != null) n.delegation = { max_depth: num(v.depth) }
    n.autonomy = {}
    if (v.ceiling) n.autonomy.ceiling = v.ceiling
    if (v.shadow) n.autonomy.shadow = true
    if (g('autonomy.tiers')) n.autonomy.tiers = g('autonomy.tiers')
    const pool: Obj = {}
    if (num(v.pool_runs) != null) pool.max_runs_per_day = num(v.pool_runs)
    if (num(v.pool_turns) != null) pool.max_turns_per_day = num(v.pool_turns)
    if (v.pool_tokens) pool.max_tokens_per_day = v.pool_tokens
    const cost = isApi ? num(v.pool_cost) : g('budget.proactive.max_cost_per_day')
    if (cost != null) pool.max_cost_per_day = cost
    const cap: Obj = {}
    if (num(v.cap_action) != null) cap.per_action = num(v.cap_action)
    if (num(v.cap_period) != null) cap.per_period = num(v.cap_period)
    if (Object.keys(pool).length || Object.keys(cap).length) {
      n.budget = {}
      if (Object.keys(cap).length) n.budget.spend_cap = cap
      if (Object.keys(pool).length) n.budget.proactive = pool
    }
    n.isolation = { sandbox: v.sandbox || 'optional' }
    if (v.handoff === false) n.isolation.handoff_workers = false
    if (v.secrets_via && v.secrets_via !== 'none') {
      n.credentials = { secrets_via: v.secrets_via }
      if ((v.allow_secrets || []).length) n.credentials.allow_secrets = v.allow_secrets
    }
    if ((v.logins || []).length || (v.credentials || []).length) {
      n.browser = {}
      if ((v.logins || []).length) n.browser.logins = v.logins
      if ((v.credentials || []).length) n.browser.credentials = v.credentials
    }
    if (v.repo) {
      n.knowledge = { repo: v.repo }
      if (v.promotion === 'direct') n.knowledge.promotion = 'direct'
    }
    const ro: Obj = { ...(isNew ? {} : c.routing || {}) }
    if (v.lead) ro.lead = v.lead // never picked for you
    if (v.fallback) ro.fallback = v.fallback
    if (Object.keys(ro).length) n.routing = ro
    return n
  }

  const visible = fields.filter((f) => !f.show || f.show(vals))
  const set = (k: string, v: unknown) => setVals((o) => ({ ...o, [k]: v }))

  async function submit() {
    const e: Record<string, string> = {}
    for (const f of visible) {
      const v = vals[f.k]
      let err = ''
      if (f.req && empty(v)) err = 'Required.'
      else if (f.pattern && v && !f.pattern.test(String(v))) err = f.patternMsg || 'Invalid value.'
      else if (f.type === 'number' && !empty(v) && typeof num(v) !== 'number') err = 'A number.'
      else if (f.validate) err = f.validate(v, vals) || ''
      if (err) e[f.k] = err
    }
    setErrs(e)
    if (Object.keys(e).length) return
    // a field left empty that opened empty keeps its opening value
    const v: Obj = {}
    for (const f of fields) v[f.k] = visible.includes(f) ? vals[f.k] : initial[f.k]
    for (const k of Object.keys(v)) if (empty(v[k]) && empty(initial[k])) v[k] = initial[k]
    setBusy(true)
    setFormErr('')
    try {
      if (isNew) {
        const name = String(v.name).trim()
        await stage(['teams', name], build(v), `new team ${name}`)
        for (const b of v.bots || []) {
          if (cfgError.value) break
          await stage(['bots', b, 'team'], name, `${b} joins team ${name}`)
        }
      } else {
        const n = await stageDiff(['teams', team!], build(initial), build(v), `edit team ${team}`, c)
        if (!n && !cfgError.value) {
          setFormErr('Nothing changed: the team is the same as the live config.')
          return
        }
      }
      if (cfgError.value) {
        setFormErr(cfgError.value)
        return
      }
      onClose()
      toReview()
    } finally {
      setBusy(false)
    }
  }

  return (
    <ModalShell
      wide
      testid="team-form"
      caps={isNew ? 'Bots · new team' : `Team ${team}`}
      title={isNew ? 'New team' : `Edit team ${team}`}
      onClose={onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" class="btn primary" disabled={busyNow} data-testid="team-form-submit" onClick={() => void submit()}>
            <Icon name="check" />Review change
          </button>
        </>
      }
    >
      <form
        class="modal-body stack"
        noValidate
        id="gen-form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <div class="small dim">A team groups bots: they share who approves and what they may do on their own, and a bot on it can only be stricter.</div>
        {visible.map((f) => (
          <>
            {f.section ? <div class="caps form-sec">{f.section}</div> : null}
            <TeamField f={f} v={vals[f.k]} err={errs[f.k]} set={(x) => set(f.k, x)} onClose={onClose} />
          </>
        ))}
        <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">{formErr}</div>
      </form>
    </ModalShell>
  )
}

function TeamField({ f, v, err, set, onClose }: { f: TF; v: any; err?: string; set: (v: any) => void; onClose: () => void }): JSX.Element {
  const id = 'f-' + f.k
  const [add, setAdd] = useState('')
  const go = (href: string) => {
    onClose()
    location.hash = href
  }
  let input: JSX.Element
  switch (f.type) {
    case 'bool':
      input = (
        <label class="row small" style="min-height:30px">
          <input type="checkbox" class="cbx" id={id} data-k={f.k} checked={!!v} onChange={(e) => set((e.target as HTMLInputElement).checked)} /> {f.on || 'on'}
        </label>
      )
      break
    case 'seg':
      input = (
        <div class="seg" role="group" aria-label={f.label} data-segk={f.k}>
          {(f.options || []).map(([ov, ol]) => (
            <button type="button" class={ov === v ? 'on' : ''} aria-pressed={ov === v} data-v={ov} onClick={() => set(ov)}>
              {ol}
            </button>
          ))}
        </div>
      )
      break
    case 'enum': {
      const hasEmpty = (f.options || []).some(([o]) => o === '')
      input = (
        <select
          class="select mono"
          id={id}
          data-k={f.k}
          value={v ?? ''}
          onChange={(e) => {
            const x = (e.target as HTMLSelectElement).value
            if (x === '__new__' && f.create) {
              ;(e.target as HTMLSelectElement).value = v ?? ''
              go(f.create.href)
              return
            }
            set(x)
          }}
        >
          {f.create ? <option value="__new__">+ {f.create.label}…</option> : null}
          {!f.req && !hasEmpty ? <option value="">— none —</option> : null}
          {(f.options || []).map(([ov, ol]) => (
            <option value={ov} selected={ov === (v ?? '')}>{ol}</option>
          ))}
        </select>
      )
      break
    }
    case 'select-multi':
    case 'list': {
      const items: string[] = Array.isArray(v) ? v : v ? String(v).split(/,\s*/) : []
      const labOf = (x: string) => (f.options || []).find(([o]) => o === x)?.[1] ?? x
      const push = (x: string) => {
        x = x.trim()
        if (x && !items.includes(x)) set([...items, x])
      }
      input = (
        <div class="listed" data-listk={f.k} id={id}>
          {items.map((x) => (
            <span class="chip-x">
              <span class={labOf(x) === x ? 'mono' : 'chip-l'} data-v={x}>{labOf(x)}</span>
              <button type="button" aria-label={`Remove ${labOf(x)}`} onClick={() => set(items.filter((y) => y !== x))}>
                <Icon name="x" />
              </button>
            </span>
          ))}
          {f.type === 'select-multi' ? (
            <select
              class="select mono list-add-sel"
              aria-label={`Add ${f.label}`}
              value=""
              onChange={(e) => {
                const x = (e.target as HTMLSelectElement).value
                ;(e.target as HTMLSelectElement).value = ''
                if (x === '__new__' && f.create) go(f.create.href)
                else if (x) push(x)
              }}
            >
              <option value="">+ add…</option>
              {f.create ? <option value="__new__">+ {f.create.label}…</option> : null}
              {(f.options || []).filter(([o]) => !items.includes(o)).map(([o, l]) => (
                <option value={o}>{l}</option>
              ))}
            </select>
          ) : (
            <>
              <input
                class="input mono list-add"
                placeholder={f.ph || 'add, then Enter'}
                value={add}
                onInput={(e) => setAdd((e.target as HTMLInputElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    push(add)
                    setAdd('')
                  }
                }}
              />
              <button type="button" class="btn sm" onClick={() => { push(add); setAdd('') }}>
                <Icon name="plus" />Add
              </button>
            </>
          )}
        </div>
      )
      break
    }
    default:
      input = (
        <input
          class={'input' + (f.mono === false ? '' : ' mono')}
          id={id}
          data-k={f.k}
          inputMode={f.type === 'number' ? 'decimal' : undefined}
          value={v ?? ''}
          placeholder={f.ph || ''}
          autocomplete="off"
          onInput={(e) => set((e.target as HTMLInputElement).value)}
        />
      )
  }
  return (
    <div class={'field' + (f.type === 'bool' ? ' boolf' : '') + (err ? ' bad' : '')} data-field={f.k}>
      <label for={id}>
        {f.label} {f.req ? <span class="req" aria-label="required">*</span> : <span class="opt">optional</span>}
      </label>
      {input}
      {f.help ? <div class="hint">{f.help}</div> : null}
      <div class="ferr" role="alert">{err ?? ''}</div>
    </div>
  )
}

// ModalShell is UI.modal's markup: .scrim > .modal with a head, body, foot.
function ModalShell({ caps, title, wide, testid, onClose, foot, children, between }: {
  caps?: string
  title: string
  wide?: boolean
  testid?: string
  onClose: () => void
  foot: JSX.Element
  children: JSX.Element | JSX.Element[]
  between?: JSX.Element
}): JSX.Element {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
  return (
    <div class="scrim" id="scrim">
      <div class={'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} data-testid={testid}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            {caps ? <div class="caps">{caps}</div> : null}
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        {between}
        {children}
        <div class="modal-foot">{foot}</div>
      </div>
    </div>
  )
}

/* ---------------- a team's picture ---------------- */

const SIZES: Record<string, number> = { xl: 72, lg: 44, md: 32, sm: 22 }
const initialsOf = (t: string) => t.replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase()

function TeamPic({ t, a, size }: { t: string; a: { icon?: string; color?: string; image?: string }; size: string }): JSX.Element {
  const px = SIZES[size]
  if (a.image) return <img class="avatar img" src={a.image} alt="" width={px} height={px} />
  if (a.icon) return <AvatarPreview spec={{ icon: a.icon, color: a.color || 'teal' }} size={px} />
  return (
    <span class="avatar initials team" style={{ width: px, height: px, background: 'var(--color-bg-raised)', color: 'var(--color-text-secondary)', fontSize: Math.round(px * 0.4) }}>
      {initialsOf(t)}
    </span>
  )
}

function csrfHeaders(): Record<string, string> {
  const c = authState.value.csrf || ''
  return c ? { 'X-Conductor-CSRF': c } : {}
}

// TeamPicture is the team picture picker (identity.js UI.teamPicker): its
// initials (no avatar key), an icon in one of the colours, or an uploaded
// picture, planned and staged through /api/v1/teams/<t>/avatar/{plan,apply}.
export function TeamPicture({ team, onClose, onStaged }: { team: string; onClose: () => void; onStaged?: (summary: string) => void }): JSX.Element {
  const cur: Obj = cfgTree().teams?.[team]?.avatar || {}
  const [mode, setMode] = useState<'initials' | 'icon' | 'upload'>(cur.image ? 'upload' : cur.icon ? 'icon' : 'initials')
  const [icon, setIcon] = useState<string>(cur.icon || ICONS[0])
  const [color, setColor] = useState<string>(cur.color || 'teal')
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState(cur.image ? `/api/v1/teams/${encodeURIComponent(team)}/avatar` : '')
  const [err, setErr] = useState('')
  const [busyNow, setBusy] = useState(false)
  useEffect(() => {
    if (!config.value) void loadConfig()
  }, [])

  const prevA = mode === 'upload' ? (preview ? { image: preview } : {}) : mode === 'icon' ? { icon, color } : {}
  const summary = `team ${team}'s picture`

  async function use() {
    setErr('')
    setBusy(true)
    try {
      if (mode === 'initials') {
        if (cur && Object.keys(cur).length) await stageDelete(['teams', team, 'avatar'], summary)
      } else if (mode === 'icon') {
        await stage(['teams', team, 'avatar'], { icon, color }, summary)
      } else {
        if (!file) {
          if (cur.image) {
            onClose()
            return
          }
          setErr('Choose a picture first, or pick initials or an icon.')
          return
        }
        const fd = new FormData()
        fd.append('file', file)
        const base = `/api/v1/teams/${encodeURIComponent(team)}/avatar`
        const plan = await fetch(base + '/plan', { method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json', ...csrfHeaders() }, body: fd }).then((r) => readResponse<Obj>(r))
        if (!plan.token) {
          setErr(String(plan.error || (plan.problems || []).join(' ') || "That picture can't be used. Try a PNG, JPEG or WebP."))
          return
        }
        await fetch(base + '/apply', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded', ...csrfHeaders() },
          body: new URLSearchParams({ token: String(plan.token) }).toString(),
        }).then((r) => readResponse<Obj>(r))
        await loadConfig()
      }
      if (cfgError.value) {
        setErr(cfgError.value)
        return
      }
      onClose()
      onStaged?.(summary)
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  const seg = (
    <div class="seg mk-mode" role="group" aria-label="Kind of picture">
      {([['initials', 'Initials'], ['icon', 'Icon and colour'], ['upload', 'Upload a picture']] as const).map(([v, l]) => (
        <button type="button" class={mode === v ? 'on' : ''} aria-pressed={mode === v} onClick={() => setMode(v)}>{l}</button>
      ))}
    </div>
  )
  return (
    <ModalShell
      wide
      testid="team-picture"
      caps="Picture"
      title={`Team ${team}'s picture`}
      onClose={onClose}
      between={seg}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" class="btn primary" disabled={busyNow} data-testid="team-picture-use" onClick={() => void use()}>Use this picture</button>
        </>
      }
    >
      <div class="modal-body" id="mk-body">
        <div class="mk-pick">
          <div class="mk-prev" id="cr-prev">
            <TeamPic t={team} a={prevA} size="xl" />
            <div class="mk-sizes">
              <TeamPic t={team} a={prevA} size="lg" />
              <TeamPic t={team} a={prevA} size="md" />
              <TeamPic t={team} a={prevA} size="sm" />
            </div>
            <p class="dl-sub">team {team}, as it will show</p>
          </div>
          <div class="mk-steps">
            {mode === 'icon' ? (
              <>
                <h3>Icon</h3>
                <div class="mk-grid">
                  {ICONS.map((ic) => (
                    <button type="button" class={'mk-opt' + (icon === ic ? ' on' : '')} aria-pressed={icon === ic} onClick={() => setIcon(ic)}>
                      <TeamPic t={team} a={{ icon: ic, color }} size="lg" />
                      <span>{ic.replace(/-/g, ' ')}</span>
                    </button>
                  ))}
                </div>
                <h3>Colour</h3>
                <div class="mk-grid mk-cols">
                  {COLORS.map((cl) => (
                    <button type="button" class={'mk-opt' + (color === cl ? ' on' : '')} aria-pressed={color === cl} onClick={() => setColor(cl)}>
                      <TeamPic t={team} a={{ icon, color: cl }} size="lg" />
                      <span>{cl[0].toUpperCase() + cl.slice(1)}</span>
                    </button>
                  ))}
                </div>
              </>
            ) : mode === 'upload' ? (
              <div class="stack">
                <p class="dl-sub">A PNG, JPEG or WebP. It is cropped to a square and kept beside your config.</p>
                <input
                  type="file"
                  accept="image/png,image/jpeg,image/webp"
                  data-testid="team-picture-file"
                  onChange={(e) => {
                    const f = (e.target as HTMLInputElement).files?.[0] || null
                    setFile(f)
                    setPreview(f ? URL.createObjectURL(f) : '')
                  }}
                />
              </div>
            ) : (
              <p class="dl-sub">
                Its first two letters on a tile: <b>{initialsOf(team)}</b>. The default.
              </p>
            )}
            {err ? <div class="callout danger small" role="alert">{err}</div> : null}
          </div>
        </div>
      </div>
    </ModalShell>
  )
}

/* ---------------- removing a team ---------------- */

// TeamDelete stages a team's removal: its bots leave the team first and keep
// their own settings (bots.js team-del).
export function TeamDelete({ team, onClose }: { team: string; onClose: () => void }): JSX.Element {
  const tree = cfgTree()
  const ms = Object.keys(tree.bots || {}).filter((n) => tree.bots[n]?.team === team)
  return (
    <ConfirmModal
      title={`Remove team ${team}?`}
      body={`${ms.length ? `Its ${ms.length} bot${ms.length > 1 ? 's' : ''} (${ms.join(', ')}) leave the team and keep their own settings. ` : ''}Stages the removal; review and apply it next.`}
      ok="Stage removal"
      danger
      onClose={onClose}
      onOk={async () => {
        for (const m of ms) await stageDelete(['bots', m, 'team'], `${m} leaves team ${team}`)
        await stageDelete(['teams', team], `remove team ${team}`)
        toReview()
      }}
    />
  )
}
