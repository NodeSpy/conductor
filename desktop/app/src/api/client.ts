// Typed wrappers over the Tauri commands (desktop/src-tauri/src/lib.rs),
// each mapping to one daemon endpoint. No generic proxy: every call here is
// a distinct command name, matching the plan's "typed Tauri commands, not a
// generic proxy" rule. Errors from the daemon surface as a DaemonError when
// the Rust side can parse one, else a plain message string.
import { invoke } from './invoke'
import type { ApprovalView, AttentionSnapshot, BotDetail, BotSummary, ChatMessage, Conversation, Decision, ProposalRow, TeamSummary } from './types'

export class ApiError extends Error {
  code?: string
  hint?: string
  constructor(message: string, code?: string, hint?: string) {
    super(message)
    this.code = code
    this.hint = hint
  }
}

function wrap<T>(p: Promise<T>): Promise<T> {
  return p.catch((e) => {
    // The Rust commands return Err(String) to Tauri; when that string is a
    // JSON-encoded DaemonError (see client.rs's ClientError -> String path)
    // it's decoded here so screens can show the daemon's own message/hint.
    const raw = e instanceof Error ? e.message : String(e)
    try {
      const parsed = JSON.parse(raw) as { code?: string; message?: string; hint?: string }
      if (parsed && parsed.message) throw new ApiError(parsed.message, parsed.code, parsed.hint)
    } catch {
      /* not JSON: a plain error string */
    }
    throw new ApiError(raw)
  })
}

export const api = {
  attention: () => wrap(invoke<AttentionSnapshot>('attention')),
  dismiss: (key: string) => wrap(invoke<void>('dismiss', { key })),

  approvalView: (kind: 'proposal' | 'purchase', bot: string | null, id: string) => wrap(invoke<ApprovalView>('approval_view', { kind, bot, id })),
  decide: (kind: 'proposal' | 'purchase', bot: string | null, id: string, decision: Decision, reason?: string) =>
    wrap(invoke<void>('decide', { kind, bot, id, decision, reason })),

  conversations: () => wrap(invoke<Conversation[]>('conversations')),
  messages: (id: string, after = 0, mark = true) => wrap(invoke<ChatMessage[]>('messages', { id, after, mark })),
  sendMessage: (id: string, text: string, to?: string[], everyone?: boolean) => wrap(invoke<ChatMessage>('send_message', { id, text, to, everyone })),
  markRead: (id: string, upto: number) => wrap(invoke<void>('mark_read', { id, upto })),

  bots: () => wrap(invoke<{ bots: BotSummary[]; teams: TeamSummary[] }>('bots')),
  bot: (name: string) => wrap(invoke<BotDetail>('bot', { name })),
  botProposals: (name: string) => wrap(invoke<{ bot: string; proposals: ProposalRow[] }>('bot_proposals', { name })),

  openInBrowser: (url: string) => wrap(invoke<void>('open_in_browser', { url })),

  getSettings: () => wrap(invoke<{ daemon_url: string; operator: string; ssh_public_key_path: string }>('get_settings')),
  defaultSshKey: () => wrap(invoke<string | null>('default_ssh_key')),
  saveSettings: (settings: { daemon_url: string; operator: string; ssh_public_key_path: string }) => wrap(invoke<void>('save_settings', { settings })),
  completeSetup: () => wrap(invoke<void>('complete_setup')),
}
