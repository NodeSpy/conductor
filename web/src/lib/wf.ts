// The workflow model for the builder, ported from the prototype's js/wf.js:
// how conductor workflows really work. A trigger has `on:` (one or several
// sources), an optional `filter:`, and an ordered `steps:` list; a
// `parallel: [[…],[…]]` step runs its branches at once and joins before the
// next step. Steps read the trigger's fields at the top level ({{.repo}}) and
// every earlier step's outputs by id ({{.triage.kind}}).
//
// These are pure functions over the parsed YAML value. What a connection can
// do comes from its plugin's own declaration (the workflows catalog), never
// from a list kept here.
import type { CatalogConnection, CatalogVerb, CatalogEvent } from '../api/wfbuilder'

export type Obj = Record<string, unknown>
export const isObj = (v: unknown): v is Obj => !!v && typeof v === 'object' && !Array.isArray(v)

export interface WfCtx {
  conns: CatalogConnection[]
  runtimes: string[]
  defaultRuntime?: string
  workflows: { name: string; inputs: Record<string, { type?: string; required?: boolean; default?: unknown }> }[]
  engines: string[]
  bots: string[]
  teams: string[]
  checks: string[]
}

/* ---------- kinds ---------- */
// a step's kind is told by its icon and word, in ink; colour is kept for status
export const KINDS: Record<string, { icon: string; label: string; color: string }> = {
  agent: { icon: 'sparkle', label: 'Agent', color: 'var(--color-icon-default)' },
  code: { icon: 'code', label: 'Code', color: 'var(--color-icon-default)' },
  decide: { icon: 'obj-question', label: 'Decide', color: 'var(--color-icon-default)' },
  ask: { icon: 'hand', label: 'Hand-off', color: 'var(--color-icon-default)' },
  verb: { icon: 'zap', label: 'Action', color: 'var(--color-icon-default)' },
  call: { icon: 'link', label: 'Call', color: 'var(--color-icon-default)' },
  wait: { icon: 'clock', label: 'Wait', color: 'var(--color-icon-default)' },
  log: { icon: 'logs', label: 'Log', color: 'var(--color-icon-default)' },
  set: { icon: 'edit', label: 'Set', color: 'var(--color-icon-default)' },
  assert: { icon: 'check', label: 'Assert', color: 'var(--color-icon-default)' },
  fail: { icon: 'x', label: 'Fail', color: 'var(--color-status-danger-solid)' },
  bot: { icon: 'members', label: 'Bot', color: 'var(--color-icon-default)' },
  parallel: { icon: 'workflows', label: 'Parallel', color: 'var(--color-icon-default)' },
  unknown: { icon: 'warn', label: 'Unknown', color: 'var(--color-status-danger-solid)' },
}

/** Find a verb's declaration from a `uses:` value, through the connection. */
export function verbOf(ctx: WfCtx, uses: unknown): { conn: CatalogConnection; verb: CatalogVerb } | null {
  const [inst, vn] = String(uses || '').split('.')
  const conn = ctx.conns.find((c) => c.name === inst)
  const verb = conn?.verbs.find((v) => v.name === vn)
  return conn && verb ? { conn, verb } : null
}
export function eventOf(ctx: WfCtx, on: string): { conn: CatalogConnection; event: CatalogEvent } | null {
  const [inst, ev] = String(on || '').split(/\.(.*)/s)
  const conn = ctx.conns.find((c) => c.name === inst)
  const event = conn?.events.find((e) => e.name === ev) || conn?.events.find((e) => e.dynamic)
  return conn && event ? { conn, event } : null
}

export function kindOf(ctx: WfCtx, s: unknown): string {
  if (!isObj(s)) return 'unknown'
  if (Array.isArray(s.parallel)) return 'parallel'
  if (s.bot) return 'bot'
  if (s.uses) return verbOf(ctx, s.uses)?.verb.ask ? 'ask' : 'verb'
  if (s.decide) return 'decide'
  if (s.call || s.workflow) return 'call'
  if ('sleep' in s || s.wait_for) return 'wait'
  if ('log' in s) return 'log'
  if (s.set) return 'set'
  if ('assert' in s) return 'assert'
  if ('fail' in s) return 'fail'
  if (s.code != null || s.command || s.use || s.run || s.type === 'command') return 'code'
  if (s.type === 'agent' || 'prompt' in s || s.agent || s['<<']) return 'agent'
  return 'unknown'
}

const trunc = (x: unknown, n = 60) => {
  const t = String(x == null ? '' : x).replace(/\s+/g, ' ').trim()
  return t.length > n ? t.slice(0, n - 1) + '…' : t
}

export function summary(ctx: WfCtx, s: unknown): string {
  const k = kindOf(ctx, s)
  const o = (isObj(s) ? s : {}) as Obj
  switch (k) {
    case 'verb':
    case 'ask': {
      const v = verbOf(ctx, o.uses)
      return `${o.uses}${v?.verb.desc ? ' · ' + v.verb.desc : ''}`
    }
    case 'agent':
      return `${o.runtime || 'default runtime'} · ${o.model || (o['<<'] ? 'from ' + o['<<'] : 'bare launch')}${o.prompt ? ' · “' + trunc(o.prompt, 42) + '”' : ''}`
    case 'code':
      return o.command ? `${o.use || o.run || 'cli'} · ${([] as unknown[]).concat(o.command).join(' ')}` : `${o.use || o.run} · ${trunc(String(o.code || '').split('\n')[0], 50)}`
    case 'decide': {
      const q = isObj(o.decide) && isObj(o.decide.questions) ? Object.keys(o.decide.questions) : []
      return `${o.model || 'light'} · ${q.join(', ') || 'no questions'}`
    }
    case 'call':
      return `call ${o.call || o.workflow}${isObj(o.with) ? ' · ' + Object.keys(o.with).length + ' inputs' : ''}`
    case 'wait': {
      const w = isObj(o.wait_for) ? o.wait_for : null
      return w ? `wait for ${w.uses || '…'} until ${w.until || '…'}` : `sleep ${o.sleep}`
    }
    case 'log':
      return `log “${trunc(o.log, 50)}”`
    case 'set':
      return `set ${Object.keys(isObj(o.set) ? o.set : {}).join(', ')}`
    case 'assert':
      return `assert ${trunc(o.assert, 50)}`
    case 'fail':
      return `fail “${trunc(o.fail, 50)}”`
    case 'parallel':
      return `${(o.parallel as unknown[]).length} branches, joined before the next step`
    case 'bot': {
      const b = isObj(o.bot) ? o.bot : { name: o.bot }
      return b.team ? `to team ${b.team} · its rules, else the lead` : `to ${b.name}${b.title ? ' · “' + trunc(b.title, 40) + '”' : ''}`
    }
    default:
      return 'no step type: add uses:, type: agent, use:, decide:, call: …'
  }
}

export interface Entry {
  step: Obj
  path: (string | number)[] // its location in the value: ['steps', 0, 'parallel', 1, 0]
  list: (string | number)[] // the list it's in
  index: number
  branch: { parent: Entry; b: number } | null
  kind: string
  id: string
  order: number
}

export const idOf = (ctx: WfCtx, s: unknown, i: number) => (isObj(s) && typeof s.id === 'string' && s.id ? s.id : `${kindOf(ctx, s)}#${i + 1}`)

/** Walk every step with its location. */
export function walk(ctx: WfCtx, value: unknown): Entry[] {
  const out: Entry[] = []
  const rec = (list: unknown, loc: (string | number)[], branch: Entry['branch']) =>
    (Array.isArray(list) ? list : []).forEach((s, i) => {
      const e: Entry = { step: (isObj(s) ? s : {}) as Obj, path: loc.concat([i]), list: loc, index: i, branch, kind: kindOf(ctx, s), id: idOf(ctx, s, out.length), order: out.length }
      out.push(e)
      if (e.kind === 'parallel') (e.step.parallel as unknown[]).forEach((br, b) => rec(br, loc.concat([i, 'parallel', b]), { parent: e, b }))
    })
  rec(isObj(value) ? value.steps : null, ['steps'], null)
  return out
}

export const at = (value: unknown, loc: (string | number)[]): unknown =>
  loc.reduce<unknown>((a, k) => (a == null ? a : (a as Record<string | number, unknown>)[k]), value)

export function getPath(o: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((a, k) => (a == null ? undefined : (a as Obj)[k]), o)
}
export function setPath(o: Obj, path: string, v: unknown): void {
  const ks = path.split('.')
  let cur = o
  ks.slice(0, -1).forEach((k) => {
    if (!isObj(cur[k])) cur[k] = {}
    cur = cur[k] as Obj
  })
  const last = ks[ks.length - 1]
  if (v === undefined) delete cur[last]
  else cur[last] = v
}

/** Steps whose outputs are visible to `entry`. */
export function before(ctx: WfCtx, value: unknown, me: Entry | null): Entry[] {
  const all = walk(ctx, value)
  const top = (e: Entry) => {
    let x = e
    while (x.branch) x = x.branch.parent
    return x
  }
  const myTop = me ? top(me) : null
  return all.filter((e) => {
    if ((me && e.id === me.id) || e.kind === 'parallel') return false
    const t = top(e)
    if (myTop && t.order < myTop.order) return true
    if (me && me.branch && e.branch && e.branch.parent.id === me.branch.parent.id && e.branch.b === me.branch.b && e.order < me.order) return true
    return !me
  })
}

/* ---------- trigger sources ---------- */
export interface Source {
  on: string
  block?: unknown
}
export function sources(v: unknown): Source[] {
  const on = isObj(v) ? v.on : null
  if (!on) return []
  return (Array.isArray(on) ? on : [on]).map((x) => (typeof x === 'string' ? { on: x } : isObj(x) ? { on: Object.keys(x)[0], block: x[Object.keys(x)[0]] } : { on: String(x) }))
}

/** What a step declares it returns (for the data tree and reference checks). */
export function outputsOf(ctx: WfCtx, s: Obj): Obj | null {
  const k = kindOf(ctx, s)
  if (k === 'verb' || k === 'ask') {
    const v = verbOf(ctx, s.uses)
    return v ? Object.fromEntries((v.verb.outputs || []).map((f) => [f.key, f.desc || f.type])) : null
  }
  if (k === 'agent') {
    const o: Obj = { text: "(the agent's final message)", diff: '(its proposed change)', tokens: '(tokens used)' }
    const props = isObj(s.output_schema) && isObj(s.output_schema.properties) ? s.output_schema.properties : null
    if (props) Object.keys(props).forEach((p) => (o[p] = '…'))
    return o
  }
  if (k === 'decide') {
    const q = isObj(s.decide) && isObj(s.decide.questions) ? s.decide.questions : {}
    return Object.fromEntries(Object.entries(q).map(([n, d]) => [n, isObj(d) && d.type === 'choice' ? '(one of its choices)' : isObj(d) && d.type === 'score' ? '(a score, 0..1)' : '(a probability)']))
  }
  if (k === 'set') return isObj(s.set) ? { ...s.set } : {}
  if (k === 'wait' && isObj(s.wait_for)) {
    const v = verbOf(ctx, s.wait_for.uses)
    return v ? Object.fromEntries((v.verb.outputs || []).map((f) => [f.key, f.desc || f.type])) : null
  }
  if (k === 'code') return null // known only after a run
  return {}
}

/* ---------- references ---------- */
const REF = /\{\{-?\s*([^}]*?)\s*-?\}\}/g
export function refsIn(str: unknown): string[] {
  const out: string[] = []
  if (typeof str !== 'string') return out
  let m: RegExpExecArray | null
  REF.lastIndex = 0
  while ((m = REF.exec(str))) {
    const rs = m[1].match(/(^|[\s(|])\.([A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*)*)/g) || []
    rs.forEach((r) => out.push(r.trim().replace(/^[(|]\s*/, '').slice(1)))
  }
  return out
}
const strings = (o: unknown, pre = '', acc: [string, string][] = []): [string, string][] => {
  if (typeof o === 'string') acc.push([pre, o])
  else if (Array.isArray(o)) o.forEach((x, i) => strings(x, pre + '.' + i, acc))
  else if (isObj(o)) Object.entries(o).forEach(([k, v]) => k !== 'parallel' && strings(v, pre ? pre + '.' + k : k, acc))
  return acc
}

/* ---------- validation ---------- */
export interface Issue {
  level: 'error' | 'warn' | 'info'
  msg: string
  path: (string | number)[]
  step?: string
  field?: string
  trigger?: boolean
  todo?: string
  line?: number
}

export function validate(ctx: WfCtx, v: unknown, opts: { reusable?: boolean; eventFields?: Obj } = {}): Issue[] {
  const out: Issue[] = []
  const add = (level: Issue['level'], path: (string | number)[], msg: string, extra: Partial<Issue> = {}) => out.push({ level, path, msg, ...extra })
  if (!isObj(v)) {
    add('error', [], 'a workflow is a map with on: and steps:')
    return out
  }
  const reusable = !!v.inputs || !!opts.reusable
  if (!v.on && !reusable) add('error', [], 'on: is missing: choose what starts this workflow', { field: 'on', trigger: true, todo: 'Choose what starts this workflow' })
  sources(v).forEach((s, i) => {
    const path = Array.isArray(v.on) ? ['on', i] : ['on']
    if (s.on === 'manual' || /^conductor\.[\w_]+$/.test(s.on)) return
    const [inst, ev] = s.on.split(/\.(.*)/s)
    const c = ctx.conns.find((x) => x.name === inst)
    if (!c) return add('error', path, `on: ${s.on}: no connection named "${inst}"`, { trigger: true })
    if (c.unavailable) return
    if (!c.events.find((e) => e.name === ev || e.dynamic)) add('error', path, `on: ${s.on}: the ${c.use} plugin declares no "${ev}" event`, { trigger: true })
  })
  if (isObj(v.filter)) {
    const declared = new Set<string>()
    sources(v).forEach((x) => (eventOf(ctx, x.on)?.event.filters || []).forEach((k) => declared.add(k)))
    if (declared.size)
      Object.keys(v.filter).forEach((k) => {
        const base = k.replace(/^not_/, '')
        if (k === 'expr' || k === 'not_expr' || declared.has(base)) return
        add('error', ['filter', k], `filter: "${k}" isn't a match key of this event (${[...declared].join(', ')}, each also not_…; or expr:)`, { trigger: true, field: 'filter' })
      })
  }
  if (!Array.isArray(v.steps)) {
    if (v.steps != null) add('error', ['steps'], 'steps: must be a list')
    else add('warn', [], 'no steps yet: add one with +')
    return out
  }
  if (!v.steps.length) add('warn', ['steps'], 'no steps yet: add one with +')
  const all = walk(ctx, v)
  const ids: Record<string, number> = {}
  const fields = opts.eventFields || {}
  all.forEach((e) => {
    const s = e.step
    const P = e.path
    const err = (msg: string, field?: string, level: Issue['level'] = 'error', extra: Partial<Issue> = {}) =>
      add(level, field ? P.concat(field.split('.')) : P, msg, { step: e.id, field, ...extra })
    if (s.id != null) {
      if (typeof s.id !== 'string' || !/^[a-z][\w-]*$/i.test(s.id)) err(`id "${s.id}": letters, digits, _ and -`, 'id')
      else if (ids[s.id]) err(`duplicate id "${s.id}" (also step ${ids[s.id]})`, 'id')
      else ids[s.id] = e.order + 1
    }
    switch (e.kind) {
      case 'unknown':
        err('this step has no type: add uses:, type: agent, use:, decide:, call:, log:, sleep: …')
        break
      case 'verb':
      case 'ask': {
        const [inst, verb] = String(s.uses).split('.')
        const c = ctx.conns.find((x) => x.name === inst)
        if (!c) {
          err(`uses: ${s.uses}: no connection named "${inst}"`, 'uses')
          break
        }
        if (c.unavailable) break
        const d = c.verbs.find((x) => x.name === verb)
        if (!d) {
          err(`uses: ${s.uses}: ${c.use} declares no verb "${verb}"`, 'uses')
          break
        }
        const o = isObj(s.options) ? s.options : {}
        ;(d.options || []).forEach((f) => {
          const x = o[f.key]
          if (f.required && (x == null || x === '' || (Array.isArray(x) && !x.length))) err(`options.${f.key} is required by ${s.uses}`, `options.${f.key}`, 'error', { todo: `Fill in ${f.key.replace(/_/g, ' ')}` })
        })
        if (!d.open) Object.keys(o).forEach((k) => !(d.options || []).find((f) => f.key === k) && err(`options.${k}: ${s.uses} has no option "${k}" (typo?)`, `options.${k}`))
        break
      }
      case 'agent':
        if (s.runtime && !ctx.runtimes.includes(String(s.runtime))) err(`runtime "${s.runtime}": no runtime by that name`, 'runtime')
        if (isObj(s.isolation) && s.isolation.mode === 'none') err('isolation: mode none runs this agent unsandboxed, as you (widening: applying asks for your passkey)', 'isolation.mode', 'warn')
        if (s.handoff && !ctx.conns.find((c) => c.name === s.handoff)) err(`handoff: no connection "${s.handoff}"`, 'handoff')
        break
      case 'code': {
        const eng = String(s.use || s.run || (s.type === 'command' ? 'cli' : ''))
        if (s.use && s.run) err('set use: or run:, never both', 'use')
        if (eng === 'cli' || s.type === 'command' || s.command) {
          if (!s.command || ![].concat(s.command as never).length) err('command: is required for use: cli', 'command')
        } else if (!s.code || !String(s.code).trim()) err('code: is empty', 'code')
        break
      }
      case 'decide': {
        const q = isObj(s.decide) && isObj(s.decide.questions) ? s.decide.questions : {}
        if (!Object.keys(q).length) err('decide.questions: ask at least one question', 'decide.questions')
        Object.entries(q).forEach(([n, d]) => isObj(d) && d.type === 'choice' && !(Array.isArray(d.choices) && d.choices.length) && err(`decide.questions.${n}: a choice question needs choices`, 'decide.questions'))
        break
      }
      case 'call': {
        const name = String(s.call || s.workflow)
        const w = ctx.workflows.find((x) => x.name === name)
        if (!w) {
          err(`call: no reusable workflow "${name}"`, 'call')
          break
        }
        const wi = isObj(s.with) ? s.with : {}
        Object.entries(w.inputs).forEach(([k, d]) => d?.required && (wi[k] == null || wi[k] === '') && err(`with.${k} is required by ${name}`, `with.${k}`, 'error', { todo: `Fill in ${k.replace(/_/g, ' ')}` }))
        Object.keys(wi).forEach((k) => !(k in w.inputs) && err(`with.${k}: ${name} has no input "${k}"`, `with.${k}`))
        break
      }
      case 'wait':
        if ('sleep' in s && !/^\d+(ms|s|m|h)$/.test(String(s.sleep))) err(`sleep: "${s.sleep}" is not a positive duration`, 'sleep')
        break
      case 'bot': {
        const b = s.bot
        if (!isObj(b)) {
          err('bot: a map with name: (a bot) or team: (a team), plus title/text', 'bot')
          break
        }
        if (!!b.name === !!b.team) err('bot: exactly one of name: (the bot) or team: (a team, whose routing picks the bot)', 'bot')
        else if (b.name && !ctx.bots.includes(String(b.name))) err(`bot.name: no bot "${b.name}"`, 'bot.name')
        else if (b.team && !ctx.teams.includes(String(b.team))) err(`bot.team: no team "${b.team}"`, 'bot.team')
        break
      }
      case 'parallel':
        if ((s.parallel as unknown[]).length < 2) err('parallel: needs at least two branches', 'parallel', 'warn')
        ;(s.parallel as unknown[]).forEach((br, b) => (!Array.isArray(br) || !br.length) && err(`parallel branch ${b + 1} is empty`, 'parallel'))
        break
    }
    // references: {{.x}} must name an event field or an earlier step
    const vis = before(ctx, v, e)
    const visIds = new Set(vis.map((x) => x.step.id).filter(Boolean) as string[])
    const later = new Set(all.filter((x) => x.order > e.order && x.step.id).map((x) => String(x.step.id)))
    const known = Object.keys(fields).length > 0
    const scopeOk = (head: string) => head in fields || ['inputs', 'item', 'index', 'group', 'error', 'failed_step', 'gate', 'vars', 'options', 'settings'].includes(head) || visIds.has(head)
    const check = (ref: string, field: string) => {
      const head = ref.split('.')[0]
      if (scopeOk(head)) return
      if (s.id === head) err(`{{.${ref}}}: a step can't read its own outputs`, field)
      else if (later.has(head)) err(`{{.${ref}}}: ${head} runs later, so it has no outputs yet`, field)
      else if (known) err(`{{.${ref}}}: no earlier step or event field named "${head}"`, field, 'warn')
    }
    strings(Object.fromEntries(Object.entries(s).filter(([k]) => k !== 'parallel'))).forEach(([field, str]) => refsIn(str).forEach((r) => check(r, field)))
  })
  return out
}

/** The data a step can read: the event's fields, the inputs, and every earlier step's outputs. */
export interface ScopeGroup {
  key: string
  label: string
  sub: string
  icon: string
  data: unknown
  tested?: boolean
  unknown?: boolean
}
export function scope(ctx: WfCtx, v: unknown, me: Entry | null, eventFields: Obj | null): ScopeGroup[] {
  const out: ScopeGroup[] = []
  const vv = isObj(v) ? v : {}
  if (isObj(vv.inputs))
    out.push({ key: 'inputs', label: 'inputs', sub: 'what the caller passes', icon: 'link', data: Object.fromEntries(Object.entries(vv.inputs).map(([k, d]) => [k, isObj(d) ? d.default ?? d.type ?? 'string' : 'string'])) })
  const srcs = sources(vv)
  if (srcs.length) {
    const declared: Obj = {}
    srcs.forEach((s) => (eventOf(ctx, s.on)?.event.fields || []).forEach((f) => (declared[f.key] = f.desc || f.type)))
    out.push({ key: '', label: 'the event', sub: srcs.map((s) => s.on).join(' · '), icon: 'zap', data: eventFields || declared, tested: !!eventFields, unknown: !eventFields && !Object.keys(declared).length })
  }
  before(ctx, v, me).forEach((e) => {
    if (!e.step.id) return
    const o = outputsOf(ctx, e.step)
    out.push({ key: String(e.step.id), label: String(e.step.id), sub: summary(ctx, e.step), icon: KINDS[e.kind]?.icon || 'dot', data: o || {}, unknown: o == null })
  })
  return out
}

/** Every leaf path in the scope, for {{. autocomplete. */
export function paths(groups: ScopeGroup[]): { path: string; value: unknown }[] {
  const out: { path: string; value: unknown }[] = []
  const rec = (o: unknown, base: string, d: number) => {
    if (!isObj(o) || d > 3) return
    Object.entries(o).forEach(([k, x]) => {
      const p = base ? `${base}.${k}` : k
      out.push({ path: p, value: x })
      rec(x, p, d + 1)
    })
  }
  groups.forEach((g) => {
    if (g.key) out.push({ path: g.key, value: g.data })
    rec(g.data, g.key, 0)
  })
  return out
}
