// The one SSE client. It wraps the browser's EventSource, which already handles
// Last-Event-ID resume natively: it remembers the last `id:` it received and
// sends it back as the Last-Event-ID header on every automatic reconnect —
// exactly what the daemon's hub replays from its ring (design §5.2). We add
// topic routing, the `reset` signal (refetch snapshots), and connection state.
import type { RunEventData } from './types'
import { parseSafe } from './safe'

export interface EventHandlers {
  onRun?: (d: RunEventData) => void
  onAttention?: (d: RunEventData) => void
  onReset?: () => void
  onOpen?: () => void
  onError?: () => void
}

// openEvents subscribes to the given topics and returns a closer.
export function openEvents(topics: string[], h: EventHandlers): () => void {
  const url = `/api/v1/events?topics=${encodeURIComponent(topics.join(','))}`
  const es = new EventSource(url)

  es.addEventListener('open', () => h.onOpen?.())
  es.addEventListener('error', () => h.onError?.())

  const parse = (e: MessageEvent): RunEventData | undefined => {
    try {
      return parseSafe<{ data: RunEventData }>(e.data).data
    } catch {
      return undefined
    }
  }
  es.addEventListener('run', (e) => {
    const d = parse(e as MessageEvent)
    if (d) h.onRun?.(d)
  })
  es.addEventListener('attention', (e) => {
    const d = parse(e as MessageEvent)
    if (d) h.onAttention?.(d)
  })
  // `reset` carries no topic, so EventSource delivers it as the default message.
  es.addEventListener('message', (e) => {
    try {
      if (parseSafe<{ type?: string }>((e as MessageEvent).data).type === 'reset') h.onReset?.()
    } catch {
      /* ignore */
    }
  })

  return () => es.close()
}
