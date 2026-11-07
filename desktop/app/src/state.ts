// App-wide signals: the attention list, conversations and bots roster, kept
// fresh by the daemon's event stream. `subscribe.rs` emits each event off the
// same resumable journal the tray uses to a window event named
// "conductor://event"; a `reset` event means "refetch everything". In mock
// mode (no Tauri) this listener is simply never triggered, which is fine:
// the mock fixtures are static.
import { signal } from '@preact/signals'
import { api, ApiError } from './api/client'
import type { AttentionItem, BotSummary, Conversation, TeamSummary } from './api/types'
import { isMock } from './api/mock'

export const attention = signal<AttentionItem[]>([])
export const attentionLoaded = signal(false)

export const conversations = signal<Conversation[]>([])
export const conversationsLoaded = signal(false)

export const bots = signal<BotSummary[]>([])
export const teams = signal<TeamSummary[]>([])
export const botsLoaded = signal(false)
export const botsNotConfigured = signal(false)

export async function refreshAttention(): Promise<void> {
  const snap = await api.attention()
  attention.value = snap.items
  attentionLoaded.value = true
}

export async function refreshConversations(): Promise<void> {
  conversations.value = await api.conversations()
  conversationsLoaded.value = true
}

export async function refreshBots(): Promise<void> {
  try {
    const r = await api.bots()
    bots.value = r.bots
    teams.value = r.teams
    botsNotConfigured.value = false
  } catch (e) {
    if (e instanceof ApiError && e.code === 'bots_not_configured') {
      bots.value = []
      teams.value = []
      botsNotConfigured.value = true
    } else {
      throw e
    }
  } finally {
    botsLoaded.value = true
  }
}

// Wires the live event stream (topics attention, chat, ticket) to a refetch
// of the relevant snapshot. Safe to call once at startup; a no-op under
// `?mock`.
export async function wireLiveUpdates(): Promise<void> {
  if (isMock()) return
  const { listen } = await import('@tauri-apps/api/event')
  await listen<{ topic: string; type: string }>('conductor://event', (evt) => {
    const { topic, type } = evt.payload
    if (type === 'reset' || topic === 'attention') void refreshAttention()
    if (type === 'reset' || topic === 'chat') void refreshConversations()
    if (type === 'reset' || topic === 'ticket') void refreshBots()
  })
  await listen<string>('conductor://navigate', (evt) => {
    location.hash = evt.payload.startsWith('#') ? evt.payload : `#/${evt.payload}`
  })
}
