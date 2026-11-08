// The client for a bot's memory and a team's knowledge (the repo browser,
// components/RepoView.tsx): the operator API routes in
// internal/bot/httpapi_repo.go, mounted under /api. A target is "bot:<name>"
// (its memory) or "team:<name>" (its knowledge). Every write names the head it
// was made against; a stale one comes back 409 and the browser re-reads.
import { authState, stepUp } from './auth'
import { readResponse } from './errors'

export interface RepoField {
  name: string
  type: string
  required: boolean
  values?: string[]
}

export interface RepoKind {
  kind: string
  dir: string
  path: string
  writers: string
  fields: RepoField[]
  skeleton: string
  template: string
  free_paths: boolean
  builtin: boolean
  // operator: an operator writes it directly; annotate: an operator may add a
  // note or flag it wrong (a log entry) without rewriting it
  operator: boolean
  annotate: boolean
  max_bytes?: number
}

export interface RepoFile {
  path: string
  kind?: string
  status: string // ok | legacy | invalid | unmapped
  problem?: string
  title?: string
  bytes?: number
  text?: string
}

export interface RepoData {
  target: string
  head: string
  kinds: RepoKind[]
  files: RepoFile[]
  readers: string[]
  operators: string[]
  // a bot's: its team, and whether that team keeps knowledge
  team?: string
  team_knowledge?: boolean
  // a team's
  promotion?: string
  // the repo's directory, by its last two parts (team-knowledge/platform)
  repo?: string
}

export interface RepoCommit {
  commit: string
  short: string
  author: string
  kind: string // operator | bot | team | other
  who: string
  date: string
  subject: string
  case?: string
  proposal?: string
  bot?: string
  reverts?: string
  files?: string[]
}

export interface CommitView {
  commit: string
  short: string
  kind: string
  who: string
  date: string
  subject: string
  trailers: string
  files: {
    path: string
    before: string
    after: string
    added: boolean
    deleted: boolean
  }[]
}

export interface BlameGroup {
  commit: string
  short: string
  kind: string
  who: string
  date: string
  lines: string[]
}

export interface KnowledgeProposal {
  bot: string
  proposal: string
  status: string
  summary: string
  from: string
  to: string
  kind: string
  note?: string
  created: string
  expires: string
  text?: string
  current?: string
  case?: string
  case_title?: string
  approver?: string
  reason?: string
  decided?: string
  edited_by?: string
  landed?: string
}

export interface EditReq {
  op: 'put' | 'rm' | 'mv' | 'revert' | 'annotate' | 'flag'
  path?: string
  to?: string
  text?: string
  kind?: string
  create?: boolean
  commit?: string
  base: string
  message?: string
}

export interface WriteResult {
  commit: string
  changed: boolean
  operator: string
  path?: string
}

const csrf = () => authState.value.csrf || ''

function get<T>(path: string): Promise<T> {
  return fetch(path, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
  }).then((r) => readResponse<T>(r))
}

function send<T>(path: string, body: unknown, stepup?: string): Promise<T> {
  return fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(csrf() ? { 'X-Conductor-CSRF': csrf() } : {}),
      ...(stepup ? { 'X-Conductor-Stepup': stepup } : {}),
    },
    body: JSON.stringify(body ?? {}),
  }).then((r) => readResponse<T>(r))
}

// base is a target's API path: /api/v1/bots/<n>/memory or /api/v1/teams/<n>/knowledge.
export function repoBase(target: string): string {
  const [kind, name] = target.split(':')
  return kind === 'team' ? `/api/v1/teams/${encodeURIComponent(name)}/knowledge` : `/api/v1/bots/${encodeURIComponent(name)}/memory`
}

const q = (path: string) => encodeURIComponent(path)

export const repoApi = {
  load: (t: string) => get<RepoData>(repoBase(t)),
  file: (t: string, path: string) => get<{ head: string; path: string; text: string }>(`${repoBase(t)}/file?path=${q(path)}`),
  history: (t: string, path?: string, limit = 60) =>
    get<{ head: string; commits: RepoCommit[] | null }>(`${repoBase(t)}/history?limit=${limit}${path ? `&path=${q(path)}` : ''}`),
  commit: (t: string, sha: string) => get<CommitView>(`${repoBase(t)}/commits/${encodeURIComponent(sha)}`),
  blame: (t: string, path: string) => get<{ groups: BlameGroup[] | null }>(`${repoBase(t)}/blame?path=${q(path)}`),
  edit: (t: string, e: EditReq) => send<WriteResult>(`${repoBase(t)}/edit`, e),
  create: (
    t: string,
    d: {
      kind: string
      title?: string
      text: string
      base: string
      message?: string
      slug?: string
    },
  ) => send<WriteResult>(`${repoBase(t)}/new`, d),
  consolidate: (bot: string) => send<{ worker?: string; case?: string; skipped?: string }>(`/api/v1/bots/${encodeURIComponent(bot)}/memory/consolidate`, {}),
  proposals: (team: string) => get<{ proposals: KnowledgeProposal[] | null }>(`/api/v1/teams/${encodeURIComponent(team)}/knowledge/proposals`),
}

// decideKnowledge approves or denies a team knowledge proposal behind the
// approval step-up (the same endpoints as the Approval screen), with the
// denial's reason going back to the proposing bot.
export async function decideKnowledge(bot: string, id: string, decision: 'approve' | 'deny', reason = ''): Promise<void> {
  const base = `/api/v1/bots/${encodeURIComponent(bot)}/proposals/${encodeURIComponent(id)}`
  const view = await get<{
    digests: { approve: string; deny: string }
    descriptions: { approve: string; deny: string }
  }>(`${base}/approval`)
  const token = await stepUp('approve', view.digests[decision], {
    what: `${decision === 'approve' ? 'Approve' : 'Deny'} ${bot}'s suggestion ${id} for the team's knowledge`,
    description: view.descriptions[decision],
  })
  await send(`${base}/decision`, { decision, reason }, token)
}

// approveKnowledgeWithChanges lands the operator's edited text in place of
// what the bot proposed: one step-up bound to that exact text.
export async function approveKnowledgeWithChanges(bot: string, id: string, edit: string): Promise<void> {
  const base = `/api/v1/bots/${encodeURIComponent(bot)}/proposals/${encodeURIComponent(id)}`
  const view = await send<{ digest: string; description: string }>(`${base}/approval-edit`, { edit })
  const token = await stepUp('approve', view.digest, {
    what: `Approve ${bot}'s suggestion ${id} with your changes`,
    description: view.description,
  })
  await send(`${base}/decision-edit`, { edit }, token)
}
