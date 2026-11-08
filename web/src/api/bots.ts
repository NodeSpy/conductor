// The typed client for the bots' surface (design §4.4): the roster and one bot,
// teams, chat conversations, tickets and proposals. It wraps the daemon's
// operator API, mounted under /api; same-origin, carrying the session cookie and
// the CSRF token on every write (design §5.1, §6.4).
import { authState } from './auth'
import type { Identity } from '../components/Avatar'
import { parseSafe } from './safe'
import { readResponse } from './errors'

function csrf(): string {
  return authState.value.csrf || ''
}

// handle reads an answer as data, or throws an ApiError worded for a person.
function handle<T>(res: Response): Promise<T> {
  return readResponse<T>(res)
}

function get<T>(path: string): Promise<T> {
  return fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' }).then((r) => handle<T>(r))
}

function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  return fetch(path, {
    method,
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(csrf() ? { 'X-Conductor-CSRF': csrf() } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => handle<T>(r))
}

// sendForm posts multipart/form-data (a message or comment with files): the
// browser sets its own Content-Type (with the boundary), so only Accept and
// the CSRF header are added here.
function sendForm<T>(path: string, form: FormData): Promise<T> {
  return fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', ...(csrf() ? { 'X-Conductor-CSRF': csrf() } : {}) },
    body: form,
  }).then((r) => handle<T>(r))
}

// --- shapes (mirror the Go read models) ---

export interface Capabilities {
  does: string[] | null
  asks: string[] | null
  shadow: boolean
}

export interface BotSummary {
  bot: string
  description: string
  team: string
  shadow: boolean
  disabled: boolean
  home_runtime: string
  identity: Identity
  open_cases: number
  pending_proposals: number
  live_workers: number
  capabilities: Capabilities
}

export interface RoutingRule {
  rule: string
  match: Record<string, unknown>
  to: string
}

export interface TeamSummary {
  team: string
  description: string
  bots: string[]
  operators: string[]
  peers: string
  identity: Identity
  routing?: { lead: string; fallback: string; rules: RoutingRule[]; timeout: string }
  knowledge?: { repo: string; promotion: string }
  proactive_pool?: unknown
  routed?: unknown[]
  delegations?: unknown[]
  knowledge_proposals?: unknown[]
}

export interface BotDetail extends BotSummary {
  memory_repo?: string
  sandboxed?: boolean
  /** What it spent in its spend period; cap is set only when it has a spend cap. */
  spend?: { spent: number; currency: string; period_hours: number; entries: number; cap?: number }
}

export interface ProposalRow {
  bot: string
  proposal: string
  kind: string
  tier: string
  uses: string
  status: string
  digest: string
  case?: string
  requester?: string
  requester_class?: string
  summary?: string
  action?: string
  created?: string
  expires?: string
  options?: Record<string, unknown>
  terraform?: {
    repo: string
    dir: string
    workspace: string
    add: number
    change: number
    destroy: number
    plan?: string
    plan_sha256?: string
    replaced?: string[]
    destroyed?: string[]
  }
}

export interface ChatMessage {
  seq: number
  at: string
  from: string // "operator:<name>" | "bot:<name>"
  text: string
  to?: string[]
  mode?: string
  reply_to?: number
  cases?: Record<string, string>
  attachments?: string[]
}

export interface Conversation {
  id: string // "team:<n>" | "bot:<n>"
  kind: 'team' | 'bot'
  name: string
  last?: ChatMessage
  unread: number
  pinned: boolean
  position?: number
}

// Attachment is one file on a message or a ticket comment (internal/store/
// bots.go's Attachment, as attachmentView projects it).
export interface Attachment {
  id: string
  message?: string
  case_id?: string
  name: string
  media_type: string
  size: number
  digest?: string
  source?: string
  requester?: string
  requester_class?: string
  created: string
}

export interface TicketComment {
  at: string
  by: string
  text: string
}

export interface TicketHistory {
  at: string
  by: string
  what: string
}

export interface Ticket {
  id: string
  ticket: string // the public number, e.g. "T-1" — the path id
  title: string
  description?: string
  status: string // draft|ready|in_progress|waiting|done|declined
  priority?: string
  assignee?: string // "bot:<n>" | "team:<n>"
  due?: string
  labels?: string[]
  created: string
  updated: string
  created_by?: string
  ready_by?: string
  requester?: string
  requester_class?: string
  holder?: string
  comments?: TicketComment[]
  history?: TicketHistory[]
}

export interface TicketQuery {
  assignee?: string
  status?: string
  priority?: string
  label?: string
  q?: string
}

// --- methods ---

export const botsApi = {
  // configured is false on a daemon with no bots yet (every list then empty);
  // editable says whether this dashboard can stage the first one.
  bots: () => get<{ bots: BotSummary[]; teams: TeamSummary[]; configured?: boolean; editable?: boolean }>('/api/v1/bots'),
  bot: (name: string) => get<BotDetail>(`/api/v1/bots/${encodeURIComponent(name)}`),
  botProposals: (name: string) => get<{ bot: string; proposals: ProposalRow[] }>(`/api/v1/bots/${encodeURIComponent(name)}/proposals`),
  proposals: () => get<{ proposals: ProposalRow[]; configured?: boolean }>('/api/v1/proposals'),

  teams: () => get<{ teams: TeamSummary[]; configured?: boolean }>('/api/v1/teams'),
  team: (name: string) => get<TeamSummary>(`/api/v1/teams/${encodeURIComponent(name)}`),

  conversations: () => get<{ conversations: Conversation[]; configured?: boolean }>('/api/v1/conversations'),
  messages: (id: string, after = 0, mark = true) =>
    get<{ conversation: string; messages: ChatMessage[] }>(
      `/api/v1/conversations/${encodeURIComponent(id)}/messages?after=${after}${mark ? '&mark=1' : ''}`,
    ),
  sendMessage: (id: string, body: { text: string; to?: string[]; everyone?: boolean }) =>
    send<ChatMessage>('POST', `/api/v1/conversations/${encodeURIComponent(id)}/messages`, body),
  // sendMessageWithFiles uploads files with the message in one call — the
  // daemon stores them (internal/bot/attachments.go) and checks them against
  // the target bot's caps before anything is recorded, all or nothing.
  sendMessageWithFiles: (id: string, body: { text: string; to?: string[]; everyone?: boolean }, files: File[]) => {
    const f = new FormData()
    f.set('text', body.text)
    for (const t of body.to || []) f.append('to', t)
    if (body.everyone) f.set('everyone', '1')
    for (const file of files) f.append('file', file, file.name)
    return sendForm<ChatMessage>(`/api/v1/conversations/${encodeURIComponent(id)}/messages`, f)
  },
  // attachment is one attachment's record; bot is whichever target bot it was
  // stored under (a message's `to`, or the conversation's own bot).
  attachment: (bot: string, id: string) => get<Attachment>(`/api/v1/bots/${encodeURIComponent(bot)}/attachments/${encodeURIComponent(id)}`),
  attachmentContentUrl: (bot: string, id: string) => `/api/v1/bots/${encodeURIComponent(bot)}/attachments/${encodeURIComponent(id)}/content`,
  attachmentThumbnailUrl: (bot: string, id: string, size?: number) =>
    `/api/v1/bots/${encodeURIComponent(bot)}/attachments/${encodeURIComponent(id)}/thumbnail${size ? `?size=${size}` : ''}`,
  pin: (id: string, position?: number) => send('POST', `/api/v1/conversations/${encodeURIComponent(id)}/pin`, { position: position ?? 0 }),
  unpin: (id: string) => send('DELETE', `/api/v1/conversations/${encodeURIComponent(id)}/pin`),

  tickets: (q: TicketQuery = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v))
    const qs = p.toString()
    return get<{ tickets: Ticket[] | null; configured?: boolean }>('/api/v1/tickets' + (qs ? `?${qs}` : ''))
  },
  ticket: (num: string) => get<Ticket>(`/api/v1/tickets/${encodeURIComponent(num)}`),
  createTicket: (body: {
    title: string
    description?: string
    assignee?: string
    priority?: string
    due?: string
    labels?: string[]
    ready?: boolean
  }) => send<Ticket>('POST', '/api/v1/tickets', body),
  patchTicket: (num: string, body: Record<string, unknown>) => send<Ticket>('PATCH', `/api/v1/tickets/${encodeURIComponent(num)}`, body),
  comment: (num: string, text: string) => send<Ticket>('POST', `/api/v1/tickets/${encodeURIComponent(num)}/comments`, { text }),
  commentWithFiles: (num: string, text: string, files: File[]) => {
    const f = new FormData()
    f.set('text', text)
    for (const file of files) f.append('file', file, file.name)
    return sendForm<Ticket>(`/api/v1/tickets/${encodeURIComponent(num)}/comments`, f)
  },
}

// A bot/team's display name from an identity (design §4.4.2: one name per bot).
export function displayName(id?: Identity, fallback = ''): string {
  return id?.display_name || id?.name || fallback
}

// who turns "bot:atlas" / "operator:ops" into its parts.
export function who(s: string): { kind: string; name: string } {
  const i = s.indexOf(':')
  return i < 0 ? { kind: '', name: s } : { kind: s.slice(0, i), name: s.slice(i + 1) }
}

// openChatStream subscribes to live chat messages (the __Host cookie rides the
// same-origin EventSource). Returns a closer.
export function openChatStream(on: (m: ChatMessage & { conversation?: string }) => void): () => void {
  const es = new EventSource('/api/v1/conversations/stream')
  es.addEventListener('message', (e) => {
    try {
      on(parseSafe((e as MessageEvent).data))
    } catch {
      /* ignore */
    }
  })
  return () => es.close()
}

// openTicketStream subscribes to live ticket changes. Returns a closer.
export function openTicketStream(on: (t: Ticket) => void): () => void {
  const es = new EventSource('/api/v1/tickets/stream')
  es.addEventListener('ticket', (e) => {
    try {
      on(parseSafe((e as MessageEvent).data))
    } catch {
      /* ignore */
    }
  })
  return () => es.close()
}

// --- a bot's page: its ⋯ menu (internal/bot/httpapi_botops.go) ---

export interface BotPageWorker {
  id: string
  status: string
  case_id?: string
  brief?: string
  started?: string
  read_only?: boolean
}

export const botPageApi = {
  rotate: (bot: string) => send<Record<string, unknown>>('POST', `/api/v1/bots/${encodeURIComponent(bot)}/rotate`, {}),
  sweep: (bot: string) => send<{ sweep: string; worker: string; status: string }>('POST', `/api/v1/bots/${encodeURIComponent(bot)}/sweep`, {}),
  deliver: (bot: string, body: { title: string; text?: string; urgent?: boolean; requester?: 'operator' | 'event' }) =>
    send<{ case: string; ticket?: string }>('POST', `/api/v1/bots/${encodeURIComponent(bot)}/deliver`, body),
  workers: (bot: string) => get<{ workers: BotPageWorker[] }>(`/api/v1/bots/${encodeURIComponent(bot)}/workers`),
}
