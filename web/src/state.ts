// App state as signals: the Overview reads these, and the loader + SSE client
// write them. Signals update only the plates that change (design §8).
import { signal } from '@preact/signals'
import { api } from './api/client'
import { openEvents } from './api/sse'
import type { StatusResponse, RunSummary } from './api/types'

export const status = signal<StatusResponse | null>(null)
export const runs = signal<RunSummary[]>([])
export const loadError = signal<string>('')
export const conn = signal<'connecting' | 'live' | 'reconnecting'>('connecting')
// liveTick bumps on every received SSE event, so the UI can flash a "live" pulse.
export const liveTick = signal(0)

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
