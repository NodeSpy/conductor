// Words the Browsers screens share (the prototype's js/screens/wallet.js and
// logins.js helpers): who an identity is (a bot by its display name), a list
// in words, a host, an age, and the bots/teams a login, profile or card may be
// given to. The bot roster is read once from /api/v1/bots and kept here.
import { signal } from '@preact/signals'
import { botsApi, type BotSummary, type TeamSummary } from '../api/bots'

export const roster = signal<{ bots: BotSummary[]; teams: TeamSummary[] } | null>(null)
let loading = false

/** Loads the bot and team roster once (display names, the pickers). */
export function loadRoster(): void {
  if (roster.value || loading) return
  loading = true
  botsApi
    .bots()
    .then((r) => (roster.value = { bots: r.bots || [], teams: r.teams || [] }))
    .catch(() => (roster.value = { bots: [], teams: [] }))
    .finally(() => (loading = false))
}

/** A bot's display name (UI.botName), its id when it has none. */
export function botName(name: string): string {
  const b = roster.value?.bots.find((x) => x.bot === name)
  return b?.identity?.display_name || name
}

/** Who an identity is, in words: a bot by its name, a team, an agent you start in a directory, a repo's agent. */
export function whoWord(id: string): string {
  const s = String(id || '')
  if (s.startsWith('bot:')) return botName(s.slice(4))
  if (s.startsWith('team:')) return `team ${s.slice(5)}`
  if (s.startsWith('dir:')) return `agents you start in ${s.slice(4)}`
  if (s.startsWith('agent:')) {
    const [p, r] = s.slice(6).split('@')
    return `the ${p} agent${r ? ` on ${r}` : ''}`
  }
  return s
}

/** "a", "a and b", "a, b and c". */
export function listWords(xs: string[]): string {
  return xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`
}

export function host(o: string): string {
  return String(o || '').replace(/^https?:\/\//, '').replace(/\/$/, '')
}

/** UI.agoText: "just now", "12 min ago", "3 h ago", "yesterday", "4 days ago". */
export function agoText(iso?: string): string {
  if (!iso) return ''
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (s < 60) return 'just now'
  if (s < 3600) return `${(s / 60) | 0} min ago`
  if (s < 86400) return `${(s / 3600) | 0} h ago`
  const d = (s / 86400) | 0
  return d === 1 ? 'yesterday' : `${d} days ago`
}

/** The bots and teams one may be given to, minus those it already has (wallet.js idOpts). */
export function idOptions(have: string[] = []): [string, string][] {
  const r = roster.value || { bots: [], teams: [] }
  return r.bots
    .map((b): [string, string] => ['bot:' + b.bot, `${b.identity?.display_name || b.bot} (${b.bot})${b.team ? ` · team ${b.team}` : ''}`])
    .concat(r.teams.map((t): [string, string] => ['team:' + t.team, `Team ${t.team}: every bot on it`]))
    .filter(([v]) => !have.includes(v))
}

/** An exact origin: https:// and the host, with a port if it has one; no path. */
export const ORIGIN = /^https?:\/\/[a-z0-9.-]+(:\d{1,5})?$/i
export const originErr = (v: string): string =>
  ORIGIN.test(v || '') ? '' : 'The exact address: https:// and the host, with a port if it has one; no path.'
