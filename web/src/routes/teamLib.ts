// What a team's page needs beside the daemon's read model: the config tree as
// it will be once the staged edits apply (the prototype's `staged(…) || cfg`),
// the routing-rule match the daemon runs (internal/bot/teams.go ruleMatches,
// path.Match globs), and the narrow-never-widen checks behind the Ceilings &
// scopes table (internal/config/teams.go checkTeamCeilings).
import type { ConfigChange } from '../api/types'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Tree = any

// overlay applies staged edits, in order, to a copy of the live tree.
export function overlay(tree: Tree, changes: ConfigChange[]): Tree {
  const t = tree ? JSON.parse(JSON.stringify(tree)) : {}
  for (const c of changes) {
    if (!c.path?.length) continue
    let cur = t
    for (let i = 0; i < c.path.length - 1; i++) {
      const k = c.path[i]
      if (cur[k] == null || typeof cur[k] !== 'object') {
        if (c.delete) {
          cur = null
          break
        }
        cur[k] = {}
      }
      cur = cur[k]
    }
    if (cur == null) continue
    const last = c.path[c.path.length - 1]
    if (c.delete) delete cur[last]
    else cur[last] = c.value
  }
  return t
}

export function getPath(o: Tree, p: string): Tree {
  return p.split('.').reduce((a, k) => (a == null ? undefined : a[k]), o)
}

// touches reports whether a staged edit changes something under a prefix.
export function touches(changes: ConfigChange[], prefix: string[]): boolean {
  return changes.some((c) => prefix.every((k, i) => c.path[i] === k) || c.path.every((k, i) => prefix[i] === k))
}

// ---- routing rules: the daemon's own match ----

// globRe is Go's path.Match as a RegExp: * any run of non-/ characters, ? one
// non-/ character, [...] a class (^ negates, a-z ranges), \ escapes. A
// malformed pattern matches nothing, as path.Match's error does there.
export function globRe(pat: string): RegExp | null {
  let re = '^'
  for (let i = 0; i < pat.length; i++) {
    const ch = pat[i]
    if (ch === '*') re += '[^/]*'
    else if (ch === '?') re += '[^/]'
    else if (ch === '\\') {
      i++
      if (i >= pat.length) return null
      re += esc(pat[i])
    } else if (ch === '[') {
      let j = i + 1
      let cls = '['
      if (pat[j] === '^') {
        cls += '^'
        j++
      }
      let n = 0
      while (j < pat.length && (pat[j] !== ']' || n === 0)) {
        let c = pat[j]
        if (c === '\\') {
          j++
          if (j >= pat.length) return null
          c = pat[j]
        }
        cls += c === '-' && pat[j - 1] !== '\\' && n > 0 && pat[j + 1] !== ']' ? '-' : esc(c)
        n++
        j++
      }
      if (j >= pat.length || n === 0) return null
      re += cls + ']'
      i = j
    } else re += esc(ch)
  }
  try {
    return new RegExp(re + '$')
  } catch {
    return null
  }
}

function esc(c: string): string {
  return c.replace(/[.+?^${}()|[\]\\*/-]/g, '\\$&')
}

export interface TryEvent {
  id: string
  label: string
  source: string
  title: string
  text: string
  requester: string
  fields: Record<string, unknown>
}

// eventField reads a rule's field the way the daemon does: source, title,
// text, requester, else a flat key of the event's fields, else a dotted path
// into nested maps; maps, lists and nothing never match.
export function eventField(ev: TryEvent, k: string): string | null {
  switch (k) {
    case 'source':
      return ev.source
    case 'title':
      return ev.title
    case 'text':
      return ev.text
    case 'requester':
      return ev.requester
  }
  if (k in ev.fields) return scalar(ev.fields[k])
  let cur: unknown = ev.fields
  for (const seg of k.split('.')) {
    if (!cur || typeof cur !== 'object' || Array.isArray(cur)) return null
    const m = cur as Record<string, unknown>
    if (!(seg in m)) return null
    cur = m[seg]
  }
  return scalar(cur)
}

function scalar(v: unknown): string | null {
  if (v == null || typeof v === 'object') return null
  return String(v)
}

export interface Rule {
  name?: string
  match?: Record<string, string>
  to: string
}

export function ruleMatches(r: Rule, ev: TryEvent): boolean {
  const m = r.match || {}
  const keys = Object.keys(m)
  if (!keys.length) return false
  return keys.every((k) => {
    const v = eventField(ev, k)
    if (v == null) return false
    const re = globRe(String(m[k]))
    return !!re && re.test(v)
  })
}

// ruleLabel is the daemon's RoutingRule.Label: its name, else its position.
export const ruleLabel = (r: Rule, i: number): string => r.name || `rules[${i}]`

// ---- narrow, never widen (the Ceilings & scopes table) ----

export const TIERS = ['observe', 'reversible', 'propose', 'commit_money', 'never']
export const tierRank = (t: string): number => TIERS.indexOf(t)

export type Badge = 'inherits' | 'equal' | 'narrows' | 'own' | 'wider'

export const subset = (a: string[] | undefined, b: string[] | undefined): boolean => !b || !b.length || (a || []).every((x) => b.includes(x))

export function listBadge(own: string[] | undefined, team: string[] | undefined): Badge {
  if (!own || !own.length) return 'inherits'
  if (!team || !team.length) return 'own'
  if (!subset(own, team)) return 'wider'
  return own.length === team.length ? 'equal' : 'narrows'
}

// tierErrors is checkTeamCeilings' tier check: a bot pattern the team
// classifies stricter.
export function tierErrors(team: string, teamTiers: Record<string, string[]> | undefined, botTiers: Record<string, string[]> | undefined): string[] {
  const out: string[] = []
  for (const [tier, pats] of Object.entries(botTiers || {}))
    for (const p of pats || [])
      for (const [tt, tps] of Object.entries(teamTiers || {}))
        for (const tp of tps || []) {
          const re = globRe(tp)
          if (re && re.test(p) && tierRank(tt) > tierRank(tier)) out.push(`${p} is ${tier} here, but team ${team} classifies it ${tt}`)
        }
  return out
}

export function money(n: unknown): string {
  const v = Number(n)
  if (!isFinite(v)) return '—'
  return '$' + (Number.isInteger(v) ? String(v) : v.toFixed(2))
}

// ---- the page's shared context (TeamDetail builds it; each tab reads it) ----

import type { JSX } from 'preact'
import type { BotSummary, TeamSummary } from '../api/bots'

export interface Routed {
  bot: string
  case: string
  ticket?: string
  source?: string
  title: string
  status: string
  via: string
  rule?: string
  lead?: string
  assignee?: string
  state?: string
  requester?: string
  requester_class?: string
  created: string
}

export interface Delegation {
  delegation: string
  from: string
  to: string
  from_case: string
  to_case?: string
  from_ticket?: string
  to_ticket?: string
  title: string
  status: string
  result?: string
  requester?: string
  requester_class?: string
  created?: string
  updated?: string
}

// An edit is a config path and its new value; undefined removes it.
export type Edit = [string[], unknown]

export interface TeamCtx {
  name: string
  team: TeamSummary
  c: Tree // the team's config, staged edits applied
  tree: Tree // the whole config, staged edits applied ({} when unreadable)
  members: string[]
  bots: Record<string, BotSummary>
  editable: boolean
  nm: (bot: string) => string
  // stageEdits stages every edit; go: then open Review & apply.
  stageEdits: (edits: Edit[], summary: string, go?: boolean) => Promise<boolean>
  open: (m: JSX.Element) => void
  close: () => void
  openTeamForm: () => void
  routeHere: () => void
}
