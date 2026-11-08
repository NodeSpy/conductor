// New bot (the guided wizard, design botwizard.js): the answers and how they
// become config. Each answer becomes real config: bots.<name> (description,
// escalation, team, its face, skill.verbs, autonomy tiers/promote/shadow) plus
// one trigger per watched event whose step hands the event to the bot
// (bot: { name, title }). What a connection delivers and does is learned from
// its plugin at runtime (GET /api/v1/newbot/connections); nothing here names a
// plugin, an event or a verb.
import YAML from 'yaml'
import { toAvatarSpec, type IdentityChoice } from '../components/IdentityPicker'

export interface NbEvent {
  name: string
  desc?: string
  dynamic?: boolean
  seen?: string[]
  filters?: string[]
  context?: string[]
}
export interface NbVerb {
  name: string
  desc?: string
  posts?: boolean
  options?: string[]
}
export interface NbConnection {
  name: string
  plugin: string
  unavailable?: string
  events: NbEvent[]
  verbs: NbVerb[]
}
export interface NbTemplate {
  name: string
  description?: string
  pack: string
  version?: string
  official: boolean
  instance?: string
  install_key?: string
  install_ref?: string
}

export const STEPS = ['Purpose', 'Watches', 'Tells you where', 'What it may do', 'Review']

export const PURPOSES: Record<string, { icon: string; title: string; desc: string; name: string; description: string }> = {
  incidents: { icon: 'warn', title: 'Watch incidents and triage them', desc: 'Reads the alert, checks what changed, tells you what it thinks is wrong.', name: 'incident-bot', description: 'Triages incidents' },
  reviews: { icon: 'nb-branch', title: 'Review pull requests', desc: 'Reads each PR when you ask for a review and leaves comments.', name: 'review-bot', description: 'Reviews pull requests' },
  ci: { icon: 'nb-obj-check', title: 'Look into failing CI', desc: 'When a check fails, reads the logs and explains or proposes a fix.', name: 'ci-bot', description: 'Looks into failing CI checks' },
  custom: { icon: 'sparkle', title: 'Something else', desc: 'Start blank and pick what it watches yourself.', name: 'my-bot', description: '' },
}

export const PRESETS: Record<string, { title: string; desc: string }> = {
  read: { title: 'Read only', desc: 'Looks things up and tells you where you chose. Anything that changes something is never done.' },
  post: { title: 'Can comment and post', desc: 'Looks things up, comments and posts on its own, and tells you where you chose. Asks you before anything else.' },
  ask: { title: 'Asks before anything else', desc: 'Looks things up and tells you where you chose, on its own. Comments, posts anywhere else and every other change wait for your approval.' },
}

export const CHANNEL = /^#[a-z0-9][a-z0-9_-]{0,79}$/

export interface Answers {
  at: number
  avatar: IdentityChoice | null
  template: NbTemplate | null
  purpose: string | null
  name: string
  description: string
  team: string
  events: Set<string> // event row keys (see eventRows)
  dyn: Record<string, string> // a dynamic row's chosen event name
  repos: string
  sweep: boolean
  every: string
  tell: 'dashboard' | 'chat'
  chat: string // the connection it tells you through
  channel: string
  preset: 'read' | 'post' | 'ask'
  shadow: boolean
  runtime: string
  operators: string[]
  ttl: string
}

export function fresh(): Answers {
  return { at: 0, avatar: null, template: null, purpose: null, name: '', description: '', team: '', events: new Set(), dyn: {}, repos: '', sweep: false, every: '4h', tell: 'dashboard', chat: '', channel: '#ops', preset: 'ask', shadow: true, runtime: '', operators: [], ttl: '24h' }
}

/** One event row of step 2: a fixed event, or a dynamic one whose name you give. */
export interface EventRow {
  key: string
  conn: NbConnection
  ev: NbEvent
  label: string
}

const sentence = (s: string) => s.replace(/^./, (c) => c.toUpperCase())

export function eventRows(conns: NbConnection[]): EventRow[] {
  const out: EventRow[] = []
  for (const c of conns) {
    if (c.unavailable) continue
    c.events.forEach((e, i) => {
      out.push({ key: e.dynamic ? `${c.name}.*${i}` : `${c.name}.${e.name}`, conn: c, ev: e, label: sentence(e.desc || e.name.replace(/_/g, ' ')) })
    })
  }
  return out
}

/** The event a chosen row listens on (`on:`), or '' while a dynamic row has no name yet. */
export function onOf(a: Answers, r: EventRow): string {
  if (!r.ev.dynamic) return r.key
  const n = (a.dyn[r.key] || '').trim()
  return n ? `${r.conn.name}.${n}` : ''
}

export function chosenRows(a: Answers, rows: EventRow[]): EventRow[] {
  return rows.filter((r) => a.events.has(r.key) && onOf(a, r))
}

export const repoList = (a: Answers) => a.repos.split(/[\s,]+/).filter(Boolean)

/** The connections a bot can tell you through: ones with a verb that posts into a conversation and takes a channel. */
export function chatConns(conns: NbConnection[]): NbConnection[] {
  return conns.filter((c) => !c.unavailable && c.verbs.some((v) => v.posts && (v.options || []).includes('channel')))
}
export const tellVerbOf = (c?: NbConnection) => c?.verbs.find((v) => v.posts && (v.options || []).includes('channel'))?.name || ''

// A verb that only looks things up, by its name (the design's own words for a
// verb pattern: get/list/read/search/fetch/describe read).
const READ = /^(get|list|read|search|fetch|describe)(_|$)/

export interface Ctx {
  conns: NbConnection[]
  rows: EventRow[]
  ceiling: string // the team's autonomy ceiling ('' = none set)
}

/** The verbs and tiers a preset gives, for the services it watches (and the chat it tells you in). */
export function grants(a: Answers, x: Ctx) {
  const uses = new Set(chosenRows(a, x.rows).map((r) => r.conn.name))
  const tellConn = a.tell === 'chat' && a.chat ? x.conns.find((c) => c.name === a.chat) : undefined
  if (tellConn) uses.add(tellConn.name)
  const tellVerb = tellConn ? `${tellConn.name}.${tellVerbOf(tellConn)}` : null
  const reads: string[] = []
  const acts: string[] = []
  for (const u of uses) {
    const c = x.conns.find((k) => k.name === u)
    if (!c || c.unavailable) continue
    const pats = new Set<string>()
    for (const v of c.verbs) {
      const m = v.name.match(READ)
      if (m) pats.add(m[2] ? `${c.name}.${m[1]}_*` : `${c.name}.${v.name}`)
      else if (v.posts && `${c.name}.${v.name}` !== tellVerb) acts.push(`${c.name}.${v.name}`)
    }
    reads.push(...pats)
  }
  const promoteOK = x.ceiling !== 'observe'
  const tiers: Record<string, string[]> = { observe: reads.slice() }
  const promote: string[] = []
  if (a.preset === 'read') {
    if (acts.length) tiers.never = acts.slice()
  } else if (a.preset === 'post') {
    tiers.reversible = acts.slice()
    if (promoteOK) promote.push(...acts)
  } else if (acts.length) tiers.propose = acts.slice()
  if (tellVerb) {
    tiers.reversible = (tiers.reversible || []).concat(tellVerb)
    if (promoteOK) promote.push(tellVerb)
  }
  for (const k of Object.keys(tiers)) if (!tiers[k].length) delete tiers[k]
  return { verbs: [...reads, ...acts, ...(tellVerb ? [tellVerb] : [])], tiers, promote, promoteOK, tellVerb }
}

export const CHECKIN = 'Check-ins'

/** bots.<name>: what step 5 stages, exactly. */
export function botCfg(a: Answers, x: Ctx): Record<string, unknown> {
  const g = grants(a, x)
  const c: Record<string, unknown> = {}
  if (a.template) c.use = `${a.template.instance || a.template.pack}/${a.template.name}`
  if (a.description) c.description = a.description
  if (a.sweep) c.charter = `${a.description || `What ${a.name} is for.`}\n\n## ${CHECKIN}\n\nLook over what you watch and tell the operator about anything that needs attention.\n`
  if (a.team) c.team = a.team
  if (a.avatar) c.avatar = toAvatarSpec(a.avatar)
  c.escalation = g.tellVerb ? `Tell the operator in ${a.channel} (${g.tellVerb}). Anything urgent also goes to Needs you.` : 'Tell the operator in the dashboard: its chat and Needs you.'
  if (a.operators.length) c.operators = a.operators.slice()
  if (a.runtime) c.worker = { runtime: a.runtime }
  if (g.verbs.length) c.skill = { verbs: g.verbs }
  const au: Record<string, unknown> = { shadow: a.shadow }
  if (Object.keys(g.tiers).length) au.tiers = g.tiers
  if (g.promote.length) au.promote = g.promote
  if (a.ttl && a.ttl !== '24h') au.approval_ttl = a.ttl
  c.autonomy = au
  if (a.sweep) c.proactive = { every: a.every, quiet_hours: '22:00-07:00', budget: { max_runs_per_day: 6 }, checklist: { section: CHECKIN } }
  return c
}

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')

/** The triggers step 5 adds: one per watched event, each handing the event to the bot. */
export function triggers(a: Answers, x: Ctx, taken: string[]): Record<string, unknown>[] {
  const used = new Set(taken)
  const rs = repoList(a)
  return chosenRows(a, x.rows).map((r) => {
    const on = onOf(a, r)
    let name = `${a.name}-${slug(on.split('.').slice(1).join('-'))}`
    for (let i = 2; used.has(name); i++) name = `${a.name}-${slug(on.split('.').slice(1).join('-'))}-${i}`
    used.add(name)
    const t: Record<string, unknown> = { name, on }
    if (rs.length && (r.ev.filters || []).includes('repo')) t.filter = { repo: rs }
    const step: Record<string, unknown> = { name: a.name }
    if ((r.ev.context || []).includes('title')) step.title = '{{.title}}'
    t.steps = [{ id: 'tell', bot: step }]
    return t
  })
}

/** The packs: entry a template from a pack you don't have yet installs, or null. */
export function packInstall(a: Answers): { name: string; value: Record<string, string> } | null {
  const t = a.template
  if (!t || t.instance || !t.install_key || !t.install_ref) return null
  return { name: t.pack, value: { [t.install_key]: t.install_ref } }
}

const Y = { blockQuote: 'literal' as const, lineWidth: 0 }

/** "The config this writes": the same values step 5 stages, as YAML. */
export function previewYAML(a: Answers, x: Ctx, taken: string[]): string {
  const pk = packInstall(a)
  let out = pk ? YAML.stringify({ packs: { [pk.name]: pk.value } }, Y) : ''
  out += YAML.stringify({ bots: { [a.name]: botCfg(a, x) } }, Y)
  const ts = triggers(a, x, taken)
  if (ts.length) out += YAML.stringify({ triggers: ts }, Y)
  return out.trimEnd()
}
