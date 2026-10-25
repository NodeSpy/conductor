// App state as signals: the Overview reads these, and the loader + SSE client
// write them. Signals update only the plates that change (design §8).
import { signal } from '@preact/signals'
import { api } from './api/client'
import { openEvents } from './api/sse'
import type { StatusResponse, RunSummary, AttrQualifier } from './api/types'

export const status = signal<StatusResponse | null>(null)
export const runs = signal<RunSummary[]>([])
export const loadError = signal<string>('')
export const conn = signal<'connecting' | 'live' | 'reconnecting'>('connecting')
// liveTick bumps on every received SSE event, so the UI can flash a "live" pulse.
export const liveTick = signal(0)
// connAttrs are the connector-published search qualifiers (github.repo, …) the
// filter bar offers as pills. Loaded once; refreshed when the run set changes.
export const connAttrs = signal<AttrQualifier[]>([])

// Needs-you / attention aggregator (design §4.1). Runs that need a person —
// failed runs (the backend's attention topic feeds these) — minus the ones the
// operator dismissed. Dismissals are local and reversible (an Undo in the UI).
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

// startLive opens the SSE stream and keeps the snapshots fresh. On each run
// event it debounce-refetches (the record is the source of truth, so a refetch
// is always correct); a reset refetches immediately.
export function startLive(): () => void {
  let timer: number | undefined
  const refresh = () => {
    liveTick.value++
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => void load(), 250) as unknown as number
  }
  return openEvents(['run', 'attention'], {
    onOpen: () => (conn.value = 'live'),
    onError: () => (conn.value = 'reconnecting'),
    onRun: refresh,
    onAttention: refresh,
    onReset: () => void load(),
  })
}
