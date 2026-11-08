// A bot's definition as the config holds it, for its "What it can do" and
// Settings tabs: the applied tree, the tree with your staged edits laid over it,
// and what a bot does with each verb it's given (on its own · asks you first ·
// never), worked out the way the daemon's gate does (internal/bot/gate.go
// Classify: the strictest tier any of the bot's or its team's patterns gives;
// unclassified verbs ask first).
import type { ConfigChange } from '../api/types'
import { readResponse } from '../api/errors'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Obj = Record<string, any>

export const clone = <T>(v: T): T => (v === undefined ? v : JSON.parse(JSON.stringify(v)))

export function getPath(o: unknown, path: string | string[]): unknown {
  const segs = Array.isArray(path) ? path : path.split('.')
  let cur: unknown = o
  for (const s of segs) {
    if (cur == null || typeof cur !== 'object') return undefined
    cur = (cur as Obj)[s]
  }
  return cur
}

// setPath sets (or, for undefined, deletes) a value, creating maps on the way;
// a map left empty by a delete is dropped too.
export function setPath(o: Obj, path: string | string[], v: unknown): void {
  const segs = Array.isArray(path) ? path : path.split('.')
  const chain: Obj[] = [o]
  let cur = o
  for (const s of segs.slice(0, -1)) {
    if (cur[s] == null || typeof cur[s] !== 'object') {
      if (v === undefined) return
      cur[s] = {}
    }
    cur = cur[s]
    chain.push(cur)
  }
  const last = segs[segs.length - 1]
  if (v === undefined) {
    if (Array.isArray(cur)) cur.splice(Number(last), 1)
    else delete cur[last]
    for (let i = chain.length - 1; i > 0; i--) {
      const c = chain[i]
      if (!Array.isArray(c) && Object.keys(c).length === 0) delete chain[i - 1][segs[i - 1]]
      else break
    }
  } else cur[last] = v
}

// withStaged is the config tree as it will be once your staged edits apply.
export function withStaged(tree: Obj | undefined, staged: ConfigChange[]): Obj {
  const t = clone(tree || {}) as Obj
  for (const c of staged) {
    if (!c.path?.length) continue
    if (c.delete) setPath(t, c.path, undefined)
    else setPath(t, c.path, clone(c.value))
  }
  return t
}

export const LADDER = ['observe', 'reversible', 'propose', 'commit_money', 'never']
const strict = (t: string) => LADDER.indexOf(t)

// matches is a verb pattern match (path.Match-style `*`, the gate's matcher).
export function matches(pat: string, verb: string): boolean {
  if (pat === verb || pat === '*') return true
  if (!pat.includes('*')) return false
  const re = new RegExp('^' + pat.split('*').map((x) => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('[^/]*') + '$')
  return re.test(verb)
}

function tierOfIn(tiers: Obj | undefined, verb: string): string | null {
  let best: string | null = null
  for (const [t, vs] of Object.entries(tiers || {})) {
    for (const p of (vs as string[]) || []) {
      if (matches(p, verb) && (best == null || strict(t) > strict(best))) best = t
    }
  }
  return best
}

export interface Tier {
  tier: string
  own: string | null
  team: string | null
}

export function tierFor(bot: Obj, team: Obj | undefined, verb: string): Tier {
  const own = tierOfIn(getPath(bot, 'autonomy.tiers') as Obj, verb)
  const tm = team ? tierOfIn(getPath(team, 'autonomy.tiers') as Obj, verb) : null
  const ts = [own, tm].filter(Boolean) as string[]
  return { tier: ts.length ? ts.sort((a, b) => strict(b) - strict(a))[0] : 'propose', own, team: tm }
}

export const ceilingOf = (_bot: Obj, team: Obj | undefined): string =>
  (team && (getPath(team, 'autonomy.ceiling') as string)) || 'reversible'

export type Mode = 'own' | 'asks' | 'never'

export function verbMode(bot: Obj, team: Obj | undefined, v: string): Mode {
  const t = tierFor(bot, team, v).tier
  const promote = (getPath(bot, 'autonomy.promote') as string[]) || []
  if (t === 'never') return 'never'
  if (t === 'observe') return 'own'
  if (t === 'reversible' && promote.some((p) => matches(p, v)) && ceilingOf(bot, team) !== 'observe') return 'own'
  return 'asks'
}

export function verbsOf(bot: Obj): string[] {
  const v = getPath(bot, 'skill.verbs')
  return Array.isArray(v) ? (v as string[]) : v && typeof v === 'object' ? Object.keys(v) : []
}

export function shadowOf(bot: Obj, team: Obj | undefined): boolean {
  const s = getPath(bot, 'autonomy.shadow')
  if (s !== undefined) return !!s
  return !!getPath(team, 'autonomy.shadow')
}

// --- the verbs plugins declare (GET /api/v1/connections/verbs) ---

export interface BotVerb {
  verb: string
  desc: string
  connection: string
  kind: string
}

let verbsCache: Promise<BotVerb[]> | null = null
export function loadVerbs(): Promise<BotVerb[]> {
  if (!verbsCache) {
    verbsCache = fetch('/api/v1/connections/verbs', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
      .then((r) => readResponse<{ verbs: BotVerb[] | null }>(r))
      .then((r) => r.verbs || [])
      .catch(() => {
        verbsCache = null
        return []
      })
  }
  return verbsCache
}

// The words for a verb pattern: gh.get_* → "read gh", box.* → "anything on box".
const PATWORD: [RegExp, string][] = [
  [/^(get|list|read|search|fetch|describe)_?$/, 'read'],
  [/^(delete|remove|destroy)_?$/, 'delete in'],
  [/^(create|add|post|comment)_?$/, 'add to'],
  [/^(update|edit|set)_?$/, 'change things in'],
]

// shortDesc is a plugin's verb description cut to its first phrase.
function shortDesc(d: string): string {
  return d.split(/ \(| — | - |; |\. /)[0].trim()
}

export function verbLabel(v: string, verbs: BotVerb[]): string {
  if (v === 'browser.purchase') return 'buy things with a card you allow'
  const d = verbs.find((x) => x.verb === v)
  if (d && d.desc) return shortDesc(d.desc).replace(/^./, (c) => c.toLowerCase())
  if (v.endsWith('*')) {
    const [inst, rest = ''] = v.replace(/\*$/, '').split('.')
    if (inst === '' || v === '*') return 'anything on any connection'
    if (!rest) return `anything on ${inst}`
    const w = PATWORD.find(([re]) => re.test(rest))
    return w ? `${w[1]} ${inst}` : `${inst}: anything starting “${rest}”`
  }
  return v
}

export const cap = (t: string): string => t.replace(/^./, (c) => c.toUpperCase())
