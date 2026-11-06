// The one SSE client, over the daemon's event journal (design: event-journal).
// It wraps the browser's EventSource, which handles Last-Event-ID resume
// natively: it remembers the last `id:` (a journal cursor) and sends it back on
// every automatic reconnect, and the daemon replays everything after it from
// the journal. We add topic routing, the `reset` signal (the cursor couldn't be
// continued: refetch snapshots), and connection state.
import type { AttentionItem, RunEventData } from './types'
import { parseSafe } from './safe'

export interface EventHandlers {
  onRun?: (d: RunEventData) => void
  onAttention?: (type: string, item: AttentionItem) => void
  onReset?: () => void
  onOpen?: () => void
  onError?: () => void
}

// openEvents subscribes to the given topics and returns a closer. cursor (a
// snapshot's) starts the stream right after it, so nothing between the
// snapshot and the stream is missed; without one the stream starts from now.
export function openEvents(topics: string[], h: EventHandlers, cursor = ''): () => void {
  let url = `/api/v1/events?topics=${encodeURIComponent(topics.join(','))}`
  if (cursor) url += `&cursor=${encodeURIComponent(cursor)}`
  const es = new EventSource(url)

  es.addEventListener('open', () => h.onOpen?.())
  es.addEventListener('error', () => h.onError?.())

  const parse = <T>(e: Event): { type: string; data: T } | undefined => {
    try {
      return parseSafe<{ type: string; data: T }>((e as MessageEvent).data)
    } catch {
      return undefined
    }
  }
  es.addEventListener('run', (e) => {
    const ev = parse<RunEventData>(e)
    if (ev?.data) h.onRun?.(ev.data)
  })
  es.addEventListener('attention', (e) => {
    const ev = parse<AttentionItem>(e)
    if (ev?.data) h.onAttention?.(ev.type, ev.data)
  })
  es.addEventListener('reset', () => h.onReset?.())

  return () => es.close()
}
