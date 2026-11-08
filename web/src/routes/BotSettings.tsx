// A bot's Settings tab (prototype js/screens/bots.js settings()): a form over
// its bots.<name> block — Identity & charter (with its mascot), Autonomy,
// Proactive sweeps, and Sessions, workers, isolation, peers — with the bar that
// sticks while there are unsaved changes: Edit as YAML, Reset, Review changes.
// Review stages the change and opens the one Review & apply; nothing changes
// until you apply it. Made from a pack's template, it says so, with its update.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { parse as parseYAML, stringify as stringifyYAML } from 'yaml'
import type { BotDetail as BotData } from '../api/bots'
import { displayName } from '../api/bots'
import { api } from '../api/client'
import type { PackInfo } from '../api/types'
import { IdentityPicker, emptyChoice, toAvatarSpec, type IdentityChoice } from '../components/IdentityPicker'
import { AvatarPreview } from '../components/Avatar'
import { ReviewPanel } from '../components/ReviewPanel'
import { CodeEditor } from '../components/CodeEditor'
import { ConfirmModal } from '../components/FormModal'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { config, staged, review, applyResult, loadConfig, stage, stageDelete, doReview, cfgError } from '../configStore'
import { query } from '../router'
import { type Obj, clone, getPath, setPath, withStaged, cap } from './botConfig'
import '../styles/bots-settings.css'

type FType = 'string' | 'enum' | 'list' | 'bool' | 'duration' | 'number' | 'seg' | 'select-multi'
interface DF {
  k: string
  type: FType
  label: string
  sec?: string
  mono?: boolean
  on?: string
  help?: string | JSX.Element
  ph?: string
  options?: (ctx: Ctx) => [string, string][]
  create?: { label: string; href: string }
  api?: boolean
}
interface Ctx {
  teams: string[]
  runtimes: string[]
  bots: string[]
}

const opt = (xs: string[]): [string, string][] => xs.map((x) => [x, x])

// The form's fields, in the prototype's order (DF in js/screens/bots.js).
const FIELDS: DF[] = [
  { k: 'description', type: 'string', mono: false, label: 'Description', sec: 'Identity & charter' },
  { k: 'team', type: 'enum', label: 'Team', options: (c) => opt(c.teams), help: 'Empty for a solo bot. On a team, defaults come from the team and this bot can only narrow them.' },
  { k: 'operators', type: 'list', label: 'Operators (approvers)', help: 'Leave empty to inherit the team’s.' },
  { k: 'escalation', type: 'string', mono: false, label: 'Escalation (when to involve you)' },
  { k: 'autonomy.shadow', type: 'bool', label: 'Practice mode', on: 'it says what it would do; nothing is done', sec: 'Autonomy' },
  { k: 'autonomy.approval_ttl', type: 'duration', label: 'Approval expires after', help: 'A request you haven’t answered by then lapses.' },
  { k: 'autonomy.spend_cap.per_action', type: 'number', label: 'Cloud spending limit · per action', help: 'Real money one approved action may commit. Not model spend.' },
  { k: 'autonomy.spend_cap.per_period', type: 'number', label: 'Cloud spending limit · per 30 days' },
  { k: 'proactive.every', type: 'duration', label: 'Sweep every', sec: 'Proactive sweeps', help: 'Empty: no sweeps (a bot opts in).' },
  { k: 'proactive.checklist.section', type: 'string', label: 'Checklist: a charter section', ph: 'Proactive checks', help: 'What every sweep works through: a heading in the charter, or a file below (exactly one).' },
  { k: 'proactive.checklist.file', type: 'string', label: '…or a checklist file', ph: 'bots/<name>-checks.md' },
  { k: 'proactive.quiet_hours', type: 'string', label: 'Quiet hours', ph: '22:00-07:00' },
  { k: 'proactive.notify_at', type: 'enum', label: 'Notify at', options: () => opt(['high', 'medium', 'low']) },
  { k: 'proactive.budget.max_runs_per_day', type: 'number', label: 'Max runs / day' },
  { k: 'proactive.budget.max_turns_per_day', type: 'number', label: 'Max turns / day' },
  { k: 'proactive.budget.max_tokens_per_day', type: 'string', label: 'Max tokens / day', ph: '500k' },
  { k: 'proactive.budget.max_cost_per_day', type: 'number', label: 'Max cost / day (USD, per provider)', api: true },
  { k: 'home.rotate.max_age', type: 'duration', label: 'Rotate after', sec: 'Sessions, workers, isolation, peers' },
  { k: 'home.rotate.max_turns', type: 'number', label: '…or turns' },
  { k: 'worker.runtime', type: 'enum', label: 'Worker runtime', options: (c) => opt(c.runtimes), create: { label: 'New runtime', href: '#/connections/runtimes' } },
  { k: 'worker.max_concurrent', type: 'number', label: 'Max concurrent workers' },
  {
    k: 'isolation.sandbox',
    type: 'seg',
    label: 'Agent sandbox',
    options: () => [['', 'inherit'], ['optional', 'optional'], ['required', 'required']],
    help: 'required: its sessions start only while the agent sandbox is enforcing. On a team, only ever stricter than the team.',
  },
  {
    k: 'peers',
    type: 'select-multi',
    label: 'Peers (bots it may message)',
    options: (c) => opt(c.bots),
    help: <>Teammates are already peers. A link needs both bots to list each other (or an operator <code>bot_links</code> pair).</>,
  },
]

// A bot setting's team counterpart, shown under it as "team x: value".
const INHERIT: Record<string, string> = {
  operators: 'operators',
  'autonomy.approval_ttl': 'autonomy.approval_ttl',
  'isolation.sandbox': 'isolation.sandbox',
  'proactive.budget.max_runs_per_day': 'budget.proactive.max_runs_per_day',
  'proactive.budget.max_cost_per_day': 'budget.proactive.max_cost_per_day',
}

// A mascot in the prototype's words (js/mascots.js SHAPES/EYES names) where
// conductor's shape or eyes is the same one; otherwise its own name.
const SHAPE_WORDS: Record<string, string> = { hexagon: 'Hex', diamond: 'Lozenge', shield: 'Shield', circle: 'Circle', squircle: 'Squircle', blob: 'Blob', drop: 'Drop', star: 'Star' }
const EYE_WORDS: Record<string, string> = { round: 'Round dots', dots: 'Small dots', wide: 'Wide-set dots', sleepy: 'Heavy lids', happy: 'Happy arcs', wink: 'Wink', visor: 'Visor', focused: 'Focused rings' }

const fid = (k: string) => 'f-df_' + k.replace(/[^\w-]/g, '_')
const DUR = /^\d+(ms|s|m|h|d)$/

function choiceOf(av: Obj | undefined, color?: string): IdentityChoice {
  const base = emptyChoice()
  if (av?.icon) return { ...base, mode: 'icon', icon: av.icon, color: av.color || base.color }
  if (av?.shape) return { ...base, mode: 'face', shape: av.shape, eyes: av.eyes || base.eyes, color: av.color || base.color }
  return { ...base, color: color || base.color }
}

function checkErrs(d: Obj): string[] {
  const e: string[] = []
  for (const f of FIELDS) {
    const v = getPath(d, f.k)
    if (v === undefined || v === '') continue
    if (f.type === 'duration' && !DUR.test(String(v))) e.push(`${f.label}: a duration such as 30s, 10m or 2h.`)
    if (f.type === 'number' && typeof v !== 'number') e.push(`${f.label}: a number.`)
  }
  if (d.charter && d.charter_file) e.push('A charter is either written here or read from a file, not both.')
  const ck = getPath(d, 'proactive.checklist') as Obj | undefined
  if (ck && ck.section && ck.file) e.push('A sweep works through a charter section or a checklist file, not both.')
  return e
}

export function BotSettings({ bot }: { bot: BotData }): JSX.Element {
  const name = bot.bot
  useEffect(() => {
    void loadConfig()
  }, [name])

  const tree = (config.value?.tree || {}) as Obj
  const next = withStaged(tree, staged.value)
  const nextBot = (next.bots?.[name] || {}) as Obj
  const loaded = !!config.value

  // the form's draft over the bot's block (as staged); re-read when the bot or
  // the applied config changes and nothing is being edited
  const [base, setBase] = useState<Obj | null>(null)
  const [draft, setDraft] = useState<Obj>({})
  const [fromYaml, setFromYaml] = useState(false)
  const sha = config.value?.sha || ''
  useEffect(() => {
    if (!loaded) return
    if (base && JSON.stringify(draft) !== JSON.stringify(base)) return
    const b = clone(nextBot)
    setBase(b)
    setDraft(clone(b))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, loaded, sha])
  useEffect(() => {
    if (applyResult.value) void loadConfig()
  }, [applyResult.value])

  const [yamlOpen, setYamlOpen] = useState(() => query('yaml') === '1')
  useEffect(() => {
    if (query('yaml') === '1') setYamlOpen(true)
  }, [location.hash])
  const [faceOpen, setFaceOpen] = useState(false)
  const [tplConfirm, setTplConfirm] = useState(false)
  const [packs, setPacks] = useState<PackInfo[]>([])
  const [latest, setLatest] = useState<Record<string, string>>({})
  useEffect(() => {
    if (!nextBot.use) return
    api.packs().then((r) => setPacks(r.packs || [])).catch(() => {})
    api
      .packCatalog('')
      .then((r) => {
        const m: Record<string, string> = {}
        for (const p of r.packs || []) if (p.installed && p.pack_instance && p.latest) m[p.pack_instance] = p.latest
        setLatest(m)
      })
      .catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, nextBot.use])

  const ctx: Ctx = useMemo(
    () => ({ teams: Object.keys(next.teams || {}).sort(), runtimes: Object.keys(next.runtimes || {}).sort(), bots: Object.keys(next.bots || {}).filter((b) => b !== name).sort() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [config.value, staged.value, name],
  )
  const team = draft.team ? (next.teams?.[draft.team] as Obj | undefined) : undefined
  const apiRuntime = (() => {
    const rt = (getPath(draft, 'worker.runtime') as string) || ''
    const r = rt ? (next.runtimes?.[rt] as Obj | undefined) : undefined
    return !!r && r.use && r.use !== 'cli'
  })()

  const changed = !!base && JSON.stringify(draft) !== JSON.stringify(base)
  const errs = checkErrs(draft)
  const stagedForBot = staged.value.filter((c) => c.path[0] === 'bots' && c.path[1] === name)
  const pendPic = JSON.stringify(nextBot.avatar ?? null) !== JSON.stringify(((tree.bots?.[name] || {}) as Obj).avatar ?? null)

  const set = (k: string, v: unknown) =>
    setDraft((d) => {
      const n = clone(d)
      setPath(n, k, v === '' || (Array.isArray(v) && !v.length) ? undefined : v)
      if (k === 'autonomy.shadow' && v === false) setPath(n, k, false)
      if (!getPath(n, 'proactive.every')) delete n.proactive
      return n
    })

  async function reviewChanges() {
    if (!base) return
    if (fromYaml) {
      await stage(['bots', name], draft, `${name}: its settings`)
    } else {
      const keys = [...new Set([...Object.keys(base), ...Object.keys(draft)])]
      for (const k of keys) {
        if (JSON.stringify(base[k] ?? null) === JSON.stringify(draft[k] ?? null)) continue
        if (draft[k] === undefined) await stageDelete(['bots', name, k], `${name}: ${k}`)
        else await stage(['bots', name, k], draft[k], `${name}: ${k}`)
        if (cfgError.value) return
      }
    }
    if (cfgError.value) return
    setBase(clone(draft))
    setFromYaml(false)
    await doReview()
    setTimeout(() => document.querySelector('section.review')?.scrollIntoView({ block: 'start', behavior: 'smooth' }), 50)
  }

  async function stageFace(c: IdentityChoice) {
    const spec = toAvatarSpec(c)
    await stage(['bots', name, 'avatar'], spec, `${name}: its face`)
    if (cfgError.value) return
    setDraft((d) => ({ ...d, avatar: spec }))
    setBase((b) => (b ? { ...b, avatar: spec } : b))
    setFaceOpen(false)
  }

  // --- the template banner ---
  const use = (nextBot.use as string) || ''
  const inst = use.split('/')[0]
  const tplName = use.split('/').slice(1).join('/')
  const pk = packs.find((p) => p.name === inst)
  const upd = pk && latest[inst] && pk.version && cmpVer(latest[inst], pk.version) > 0 ? latest[inst] : ''
  async function updatePack() {
    const c = (next.packs?.[inst] || {}) as Obj
    const key = c.source ? 'source' : 'use'
    const ref = String(c[key] || '')
    if (ref.includes('@')) await stage(['packs', inst, key], ref.split('@')[0], `update ${inst} to ${upd}`)
    if (!cfgError.value) await stage(['packs', inst, 'version'], String(c.version || '').startsWith('=') ? `=${upd}` : upd, `update ${inst} to ${upd}`)
    if (!cfgError.value) {
      await doReview()
      setTimeout(() => document.querySelector('section.review')?.scrollIntoView({ block: 'start', behavior: 'smooth' }), 50)
    }
  }

  // --- the fields ---
  const inh = (k: string) => {
    if (!team || !INHERIT[k]) return null
    const v = getPath(team, INHERIT[k])
    if (v === undefined) return null
    return (
      <div class="hint inh-hint">
        <Icon name="link" /> team {draft.team}: <span class="mono">{Array.isArray(v) ? v.join(', ') : String(v)}</span>
      </div>
    )
  }
  const field = (f: DF) => <Field f={f} v={getPath(draft, f.k)} ctx={ctx} set={(v) => set(f.k, v)} inh={inh(f.k)} />
  const charterFile = (draft.charter_file as string) || ''
  const charter = (
    <div class="field" data-field="df.charter">
      <label for="df-charter">
        Charter <span class="opt"><code>{charterFile || "config.yaml"}</code> · config only</span>
      </label>
      <textarea
        class="textarea"
        rows={6}
        id="df-charter"
        data-df-charter="1"
        value={(draft.charter as string) || ''}
        placeholder={charterFile ? `Read from ${charterFile}` : ''}
        disabled={!!charterFile && !draft.charter}
        onInput={(e) => set('charter', (e.target as HTMLTextAreaElement).value)}
      />
      <div class="hint">
        {charterFile && !draft.charter ? (
          <>Its charter is read from <code>{charterFile}</code> on the daemon’s machine; change it there. </>
        ) : null}
        Nothing on the agent-facing surface writes it.
      </div>
    </div>
  )
  const sections: { title: string; fields: DF[] }[] = []
  for (const f of FIELDS) {
    if (f.sec) sections.push({ title: f.sec, fields: [] })
    if (f.api && !apiRuntime) continue
    sections[sections.length - 1].fields.push(f)
  }

  const av = (draft.avatar || bot.identity?.avatar) as Obj | undefined
  const choice = choiceOf(av, bot.identity?.color)
  const mkWords = choice.mode === 'icon' ? `${cap(choice.icon)} icon · ${choice.color}` : `${SHAPE_WORDS[choice.shape] || cap(choice.shape)} · ${(EYE_WORDS[choice.eyes] || choice.eyes).toLowerCase()} · ${choice.color}`

  if (!loaded) return <p class="muted">Loading its settings…</p>

  return (
    <>
      {use ? (
        <div class="callout info tpl-from" data-testid="tpl-from">
          <Icon name="bots" />
          <span class="grow">
            Made from <b>{tplName || use}</b>, in <b>{inst}{pk?.version ? ` ${pk.version}` : ''}</b>
            {pk ? (pk.official ? ' (official)' : pk.local ? ' (your own)' : ' (custom)') : ''}
            {upd ? <> · <b>{upd}</b> is available</> : null}. Your changes to its charter, knowledge and permissions stay when it updates.{' '}
            <a class="link" href={`#/packs/item/${encodeURIComponent(inst)}`}>The pack’s steps and runbooks</a>
          </span>
          {upd ? (
            <button type="button" class="btn sm secondary" onClick={() => setTplConfirm(true)}>
              Update to {upd}…
            </button>
          ) : null}
        </div>
      ) : null}

      <div class="stack def-form" data-bot={name}>
        {sections.map((s, i) => (
          <div class="panel" data-sec={s.title}>
            <div class="panel-head"><h3>{s.title}</h3></div>
            {i === 0 ? (
              <div class="panel-body mk-row">
                <label class="mk-lbl">Mascot</label>
                <div class="mk-field">
                  <AvatarPreview spec={toAvatarSpec(choice)} size={40} />
                  <span class="dl-sub">{mkWords}</span>
                  <button type="button" class="btn sm secondary" data-testid="mascot-change" onClick={() => setFaceOpen(true)}>Change…</button>
                </div>
                {pendPic ? (
                  <p class="mk-pend">
                    <StatusMark s="staged" mkOnly label="Not applied yet" />
                    Not applied yet: the picture in force is still the one shown elsewhere. <a class="dl-link" href="#/system/config">Review and apply</a>
                  </p>
                ) : null}
              </div>
            ) : null}
            <div class="panel-body def-grid">
              {s.fields.map(field)}
              {i === 0 ? charter : null}
            </div>
          </div>
        ))}
      </div>

      {errs.length ? (
        <div class="def-issues">
          {errs.map((e) => (
            <div class="callout danger small">{e}</div>
          ))}
        </div>
      ) : null}
      {cfgError.value ? <div class="def-issues"><div class="callout danger small" role="alert">{cfgError.value}</div></div> : null}

      <div class={'def-bar' + (changed ? ' dirty' : '')}>
        <span class="def-state">
          {changed ? (errs.length ? <StatusMark s="fail" word={`${errs.length} thing${errs.length > 1 ? 's' : ''} to fix`} /> : 'Unsaved changes') : 'No changes'}
        </span>
        <button type="button" class="link def-yaml-btn" data-testid="df-yaml" onClick={() => setYamlOpen(true)}>
          <Icon name="code" />Edit as YAML
        </button>
        <button
          type="button"
          class="btn ghost"
          aria-disabled={changed ? undefined : 'true'}
          data-tip={changed ? undefined : 'No edits'}
          onClick={() => {
            if (!changed) return
            if (base) setDraft(clone(base))
            setFromYaml(false)
          }}
        >
          <Icon name="undo" />Reset
        </button>
        <button
          type="button"
          class="btn primary"
          data-testid="df-review"
          aria-disabled={!changed || errs.length > 0 ? 'true' : undefined}
          data-tip={!changed ? 'Change something first' : errs.length ? 'Fix the errors first' : undefined}
          onClick={() => {
            if (changed && !errs.length) void reviewChanges()
          }}
        >
          <Icon name="check" />Review changes
        </button>
      </div>

      {stagedForBot.length || review.value || applyResult.value ? <ReviewPanel stagedCount={staged.value.length} /> : null}

      {yamlOpen ? (
        <YamlSheet
          name={name}
          draft={draft}
          onApply={(node) => {
            setDraft(node)
            setFromYaml(true)
            setYamlOpen(false)
            if (query('yaml') === '1') history.replaceState(null, '', `#/bots/${name}/settings`)
          }}
          onClose={() => {
            setYamlOpen(false)
            if (query('yaml') === '1') history.replaceState(null, '', `#/bots/${name}/settings`)
          }}
        />
      ) : null}

      {faceOpen ? <FaceSheet name={name} title={`${displayName(bot.identity, name)}’s picture`} start={choice} onUse={(c) => void stageFace(c)} onClose={() => setFaceOpen(false)} /> : null}

      {tplConfirm ? (
        <ConfirmModal
          title={`Update ${inst} to ${upd}?`}
          body="Its template, runbooks and knowledge update; your changes to the bot stay. You review the change before it applies."
          ok="Stage the update"
          onOk={() => updatePack()}
          onClose={() => setTplConfirm(false)}
        />
      ) : null}
    </>
  )
}

function cmpVer(a: string, b: string): number {
  const pa = String(a).replace(/^v/, '').split(/[.-]/).map((x) => parseInt(x, 10) || 0)
  const pb = String(b).replace(/^v/, '').split(/[.-]/).map((x) => parseInt(x, 10) || 0)
  for (let i = 0; i < 3; i++) if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0)
  return 0
}

// Field is one form row, the prototype's UI.fieldHTML for the types the bot
// form uses.
function Field({ f, v, ctx, set, inh }: { f: DF; v: unknown; ctx: Ctx; set: (v: unknown) => void; inh: JSX.Element | null }): JSX.Element {
  const id = fid(f.k)
  const [text, setText] = useState('')
  const val = v == null ? '' : v
  let input: JSX.Element
  switch (f.type) {
    case 'bool':
      input = (
        <label class="row small" style={{ minHeight: '30px' }}>
          <input type="checkbox" class="cbx" id={id} data-k={f.k} checked={!!val} onChange={(e) => set((e.target as HTMLInputElement).checked)} /> {f.on || 'on'}
        </label>
      )
      break
    case 'number':
      input = (
        <input
          class="input mono"
          id={id}
          data-k={f.k}
          inputMode="decimal"
          value={String(val)}
          placeholder={f.ph || ''}
          onInput={(e) => {
            const s = (e.target as HTMLInputElement).value.trim()
            set(s === '' ? undefined : /^-?\d+(\.\d+)?$/.test(s) ? Number(s) : s)
          }}
        />
      )
      break
    case 'enum': {
      const opts = f.options ? f.options(ctx) : []
      const cur = String(val)
      if (cur && !opts.some(([o]) => o === cur)) opts.push([cur, cur])
      input = (
        <select
          class="select mono"
          id={id}
          data-k={f.k}
          value={cur}
          onChange={(e) => {
            const x = (e.target as HTMLSelectElement).value
            if (x === '__new__' && f.create) {
              ;(e.target as HTMLSelectElement).value = cur
              location.hash = f.create.href
              return
            }
            set(x || undefined)
          }}
        >
          {f.create ? <option value="__new__">+ {f.create.label}…</option> : null}
          <option value="">— none —</option>
          {opts.map(([o, l]) => (
            <option value={o} selected={o === cur}>{l}</option>
          ))}
        </select>
      )
      break
    }
    case 'seg': {
      const opts = f.options ? f.options(ctx) : []
      input = (
        <div class="seg" role="group" data-segk={f.k} aria-label={f.label}>
          {opts.map(([o, l]) => (
            <button type="button" aria-pressed={String(val) === o} class={String(val) === o ? 'on' : ''} onClick={() => set(o || undefined)}>
              {l}
            </button>
          ))}
        </div>
      )
      break
    }
    case 'list':
    case 'select-multi': {
      const items = Array.isArray(val) ? (val as string[]) : []
      const add = (x: string) => {
        const t = x.trim()
        if (t && !items.includes(t)) set([...items, t])
      }
      input = (
        <div class="listed" data-listk={f.k} id={id}>
          {items.map((x) => (
            <span class="chip-x">
              <span class="mono" data-v={x}>{x}</span>
              <button type="button" aria-label={`Remove ${x}`} onClick={() => set(items.filter((y) => y !== x))}>
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
                add((e.target as HTMLSelectElement).value)
                ;(e.target as HTMLSelectElement).value = ''
              }}
            >
              <option value="">+ add…</option>
              {(f.options ? f.options(ctx) : [])
                .filter(([o]) => !items.includes(o))
                .map(([o, l]) => (
                  <option value={o}>{l}</option>
                ))}
            </select>
          ) : (
            <>
              <input
                class="input mono list-add"
                placeholder={f.ph || 'add, then Enter'}
                data-k-list={f.k}
                value={text}
                onInput={(e) => setText((e.target as HTMLInputElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    add(text)
                    setText('')
                  }
                }}
              />
              <button
                type="button"
                class="btn sm"
                onClick={() => {
                  add(text)
                  setText('')
                }}
              >
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
          value={String(val)}
          placeholder={f.ph || (f.type === 'duration' ? 'e.g. 30m' : '')}
          onInput={(e) => set((e.target as HTMLInputElement).value.trim() === '' ? undefined : (e.target as HTMLInputElement).value)}
        />
      )
  }
  return (
    <div class={'field' + (f.type === 'bool' ? ' boolf' : '')} data-field={'df.' + f.k}>
      <label for={id}>
        {f.label} <span class="opt">optional</span>
      </label>
      {input}
      {f.help ? <div class="hint">{f.help}</div> : null}
      {inh}
      <div class="ferr" role="alert" />
    </div>
  )
}

// YamlSheet is Edit as YAML: the bot's block in a sheet, checked as you type;
// applying puts it back in the form (nothing changes until Review changes).
function YamlSheet({ name, draft, onApply, onClose }: { name: string; draft: Obj; onApply: (node: Obj) => void; onClose: () => void }): JSX.Element {
  const [text, setText] = useState(() => stringifyYAML({ bots: { [name]: draft } }))
  const [err, setErr] = useState('')
  const parse = (t: string): Obj => {
    const v = parseYAML(t) as Obj
    const node = v && v.bots && v.bots[name]
    if (!node || typeof node !== 'object' || Array.isArray(node)) throw new Error(`Keep it under bots: ${name}:`)
    return node
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
  return (
    <div class="scrim" id="scrim">
      <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1} data-testid="df-yaml-sheet">
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            <div class="caps">config.yaml · bots.{name}</div>
            <h2>Edit as YAML</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body">
          <p class="dl-sub" style={{ margin: '0 0 8px' }}>
            The same settings as the form, as they’ll be written. Apply puts them back in the form; nothing changes until you review it.
          </p>
          <CodeEditor
            id="df-yaml-ed"
            text={text}
            minRows={18}
            label={`bots.${name} as YAML`}
            onChange={(t) => {
              setText(t)
              try {
                const n = parse(t)
                setErr(checkErrs(n).join(' '))
              } catch (e) {
                setErr(`Not valid YAML: ${(e as Error).message}`)
              }
            }}
          />
          <div id="df-yaml-err" class="def-issues" role="alert">
            {err ? <div class="callout danger small">{err}</div> : null}
          </div>
        </div>
        <div class="modal-foot">
          <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
          <button
            type="button"
            class="btn primary"
            data-testid="df-yaml-apply"
            onClick={() => {
              try {
                onApply(parse(text))
              } catch (e) {
                setErr(`Not valid YAML: ${(e as Error).message}`)
              }
            }}
          >
            <Icon name="check" />Apply to the form
          </button>
        </div>
      </div>
    </div>
  )
}

// FaceSheet is the mascot picker behind Mascot · Change…: picking stages the
// face at once (it shows everywhere once applied).
function FaceSheet({ name, title, start, onUse, onClose }: { name: string; title: string; start: IdentityChoice; onUse: (c: IdentityChoice) => void; onClose: () => void }): JSX.Element {
  const [c, setC] = useState<IdentityChoice>(start)
  return (
    <div class="scrim" id="scrim">
      <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            <div class="caps">bot {name}</div>
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body">
          <IdentityPicker value={c} onChange={setC} />
        </div>
        <div class="modal-foot">
          <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" data-testid="stage-face" onClick={() => onUse(c)}>
            <Icon name="check" />Use this face
          </button>
        </div>
      </div>
    </div>
  )
}
