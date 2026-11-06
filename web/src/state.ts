// App state as signals: the Overview reads these, and the loader + SSE client
// write them. Signals update only the plates that change (design §8).
import { signal } from '@preact/signals'
import { api } from './api/client'
import { openEvents } from './api/sse'
import type { StatusResponse, RunSummary, AttrQualifier, AttentionItem } from './api/types'

export const status = signal<StatusResponse | null>(null)
export const runs = signal<RunSummary[]>([])
export const loadError = signal<string>('')
export const conn = signal<'connecting' | 'live' | 'reconnecting'>('connecting')
// liveTick bumps on every received SSE event, so the UI can flash a "live" pulse.
export const liveTick = signal(0)
// connAttrs are the connector-published search qualifiers (github.repo, …) the
// filter bar offers as pills. Loaded once; refreshed when the run set changes.
export const connAttrs = signal<AttrQualifier[]>([])

// attention is the daemon's view of what needs the operator (pending
// approvals, failed runs, escalations, …): loaded as a snapshot, then kept
// current from the attention topic. Most urgent first, then newest.
export const attention = signal<AttentionItem[]>([])

const severityRank: Record<string, number> = { urgent: 0, warn: 1, info: 2 }
function sortAttention(items: AttentionItem[]): AttentionItem[] {
  return [...items].sort(
    (a, b) => (severityRank[a.severity] ?? 3) - (severityRank[b.severity] ?? 3) || b.opened.localeCompare(a.opened),
  )
}

// loadAttention refetches the snapshot and returns the event cursor it
// reflects ('' when it couldn't be loaded).
export async function loadAttention(): Promise<string> {
  try {
    const r = await api.attention()
    attention.value = sortAttention(r.items || [])
    return r.cursor
  } catch {
    return '' /* keep what we have; the next event or reset refetches */
  }
}

function applyAttention(type: string, item: AttentionItem): void {
  const rest = attention.value.filter((i) => i.key !== item.key)
  attention.value = sortAttention(type === 'resolved' ? rest : [...rest, item])
}

// dismissAttention closes a raised item on the daemon; the resolved event
// removes it here.
export async function dismissAttention(key: string): Promise<void> {
  await api.dismissAttention(key)
  attention.value = attention.value.filter((i) => i.key !== key)
}

// Runs the operator hid from the Activity table's "Needs attention" group.
// Local and reversible (an Undo in the UI).
const DISMISS_KEY = 'needs-dismissed'
function loadDismissed(): string[] {
  try {
    return JSON.parse(localStorage.getItem(DISMISS_KEY) || '[]') as string[]
  } catch {
    return []
  }
}
export const dismissed = signal<string[]>(loadDismissed())

export function setDismissed(next: string[]): void {
  dismissed.value = next
  try {
    localStorage.setItem(DISMISS_KEY, JSON.stringify(next))
  } catch {
    /* storage unavailable (private mode) — keep it in memory only */
  }
}

export function dismiss(id: string): void {
  if (!dismissed.value.includes(id)) setDismissed([...dismissed.value, id])
}
export function undismiss(id: string): void {
  setDismissed(dismissed.value.filter((d) => d !== id))
}

// needsYou returns the runs needing a person right now, newest first.
export function needsYou(all: RunSummary[] = runs.value): RunSummary[] {
  const skip = new Set(dismissed.value)
  return all.filter((r) => r.status === 'failed' && !skip.has(r.id))
}

// Mark state keys (data-s) and words, from the run record's status. These are
// the brand's status vocabulary (css/marks.css): ok→passed, fail→failed,
// run→running, idle→neutral.
export function statusMark(s: string): { s: string; word: string } {
  switch (s) {
    case 'ok':
      return { s: 'ok', word: 'Passed' }
    case 'failed':
      return { s: 'fail', word: 'Failed' }
    case 'running':
      return { s: 'run', word: 'Running' }
    case 'retried':
      return { s: 'idle', word: 'Retried' }
    default:
      return { s: 'idle', word: s || 'Unknown' }
  }
}

export async function load(): Promise<void> {
  try {
    const [st, r] = await Promise.all([api.status(), api.runs({ limit: 200 })])
    status.value = st
    runs.value = r.runs
    loadError.value = ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
  // Connector qualifiers are cheap and change rarely; a failure just leaves the
  // filter bar without pills, never blocks the page.
  try {
    connAttrs.value = (await api.connectorAttrs()).attrs || []
  } catch {
    /* no qualifiers available */
  }
}

// startLive loads the attention snapshot, then opens the SSE stream from the
// snapshot's cursor so no attention change between the two is missed. On each
// run event it debounce-refetches the runs (the record is the source of truth,
// so a refetch is always correct); a reset refetches everything.
export function startLive(): () => void {
  let timer: number | undefined
  let close: (() => void) | undefined
  let stopped = false
  const refresh = () => {
    liveTick.value++
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => void load(), 250) as unknown as number
  }
  void loadAttention().then((cursor) => {
    if (stopped) return
    close = openEvents(
      ['run', 'attention'],
      {
        onOpen: () => (conn.value = 'live'),
        onError: () => (conn.value = 'reconnecting'),
        onRun: refresh,
        onAttention: (type, item) => {
          liveTick.value++
          applyAttention(type, item)
        },
        onReset: () => {
          void load()
          void loadAttention()
        },
      },
      cursor,
    )
  })
  return () => {
    stopped = true
    close?.()
  }
}
