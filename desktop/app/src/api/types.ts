// Wire types for the daemon's dashboard API, as surfaced by the Rust Tauri
// commands (desktop/src-tauri/src/client.rs + lib.rs). These mirror the Go
// read models the same way web/src/api/bots.ts does for the web dashboard;
// kept permissive (optional fields) since core may add fields this app
// doesn't act on yet.

export type Severity = 'info' | 'warn' | 'urgent'

export interface AttentionItem {
  key: string
  kind: string
  title: string
  detail?: string
  link?: string
  subject?: string
  severity: Severity
  opened: string
  source?: string
}

export interface AttentionSnapshot {
  items: AttentionItem[]
  cursor: string
}

export interface ApprovalDigests {
  approve: string
  deny: string
}

export interface ApprovalDescriptions {
  approve: string
  deny: string
}

// The approval view, common to a bot proposal (bot+proposal, purpose
// "approve") and a browser purchase (purchase, purpose "browser.purchase").
// The decision's digest always comes from here — never computed
// client-side. `summary` is raw JSON from the daemon (a human-rendered blob,
// not a fixed shape; null when empty).
export interface ApprovalView {
  bot?: string
  proposal?: string
  purchase?: string
  action_sha256: string
  digests: ApprovalDigests
  descriptions: ApprovalDescriptions
  summary: unknown
  operators: string[]
  purpose: string
}

export type Decision = 'approve' | 'deny'

export interface DaemonError {
  code: string
  message: string
  hint?: string
}

export interface Identity {
  name?: string
  display_name?: string
  [key: string]: unknown
}

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

export interface TeamSummary {
  team: string
  description: string
  bots: string[]
  operators: string[]
  identity: Identity
}

export interface BotDetail extends BotSummary {
  memory_repo?: string
  sandboxed?: boolean
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

export interface EventEnvelope {
  id: string
  seq: number
  topic: string
  type: string
  subject: string
  ts: string
  data: unknown
}

// `who` turns "bot:atlas" / "operator:ops" into its parts.
export function who(s: string): { kind: string; name: string } {
  const i = s.indexOf(':')
  return i < 0 ? { kind: '', name: s } : { kind: s.slice(0, i), name: s.slice(i + 1) }
}

export function displayName(id: Identity | undefined, fallback = ''): string {
  return id?.display_name || id?.name || fallback
}
