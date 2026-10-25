// The dashboard's typed fetch client. Shapes come from the tygo-generated
// types.ts (the Go API is the source of truth). Same-origin only (design §5.1:
// no CORS); errors arrive as {error:{code,message,hint}}.
import type {
  StatusResponse,
  RunsResponse,
  ErrorResponse,
  ConfigResponse,
  ConfigChange,
  ReviewResponse,
  AppliedResponse,
  HistoryItem,
  StageRequest,
  SecretsResponse,
  PacksResponse,
  PluginsResponse,
  ConnectionsResponse,
  SystemResponse,
  AuditResponse,
} from './types'

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  return handle<T>(res)
}

async function sendJSON<T>(method: string, path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body ?? {}),
  })
  return handle<T>(res)
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as ErrorResponse
      if (body?.error?.message) message = body.error.message
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

export interface RunsQuery {
  since?: string
  limit?: number
  cursor?: string
}

export const api = {
  status: () => getJSON<StatusResponse>('/api/v1/status'),
  runs: (q: RunsQuery = {}) => {
    const p = new URLSearchParams()
    if (q.since) p.set('since', q.since)
    if (q.limit) p.set('limit', String(q.limit))
    if (q.cursor) p.set('cursor', q.cursor)
    const qs = p.toString()
    return getJSON<RunsResponse>('/api/v1/runs' + (qs ? `?${qs}` : ''))
  },

  // --- config-edit pipeline ---
  config: () => getJSON<ConfigResponse>('/api/v1/config'),
  stage: (req: StageRequest) => sendJSON<{ staged: ConfigChange[] }>('POST', '/api/v1/config/stage', req),
  unstage: (id: string) => sendJSON<{ staged: ConfigChange[] }>('POST', '/api/v1/config/unstage', { id }),
  clearStaged: () => sendJSON<{ staged: ConfigChange[] }>('POST', '/api/v1/config/clear', {}),
  review: () => getJSON<ReviewResponse>('/api/v1/config/review'),
  apply: (token: string) => sendJSON<AppliedResponse>('POST', '/api/v1/config/apply', { token }),
  history: () => getJSON<{ history: HistoryItem[] }>('/api/v1/config/history'),
  restoreReview: (id: string) => sendJSON<ReviewResponse>('POST', '/api/v1/config/restore/review', { id }),
  restore: (id: string, token: string) => sendJSON<AppliedResponse>('POST', '/api/v1/config/restore', { id, token }),

  // --- section reads ---
  secrets: () => getJSON<SecretsResponse>('/api/v1/secrets'),
  packs: () => getJSON<PacksResponse>('/api/v1/packs'),
  plugins: () => getJSON<PluginsResponse>('/api/v1/plugins'),
  connections: () => getJSON<ConnectionsResponse>('/api/v1/connections'),
  system: () => getJSON<SystemResponse>('/api/v1/system'),
  audit: () => getJSON<AuditResponse>('/api/v1/audit'),
}

// validateStream opens the SSE validate stream and calls back with each event.
// Returns a stop() that closes the stream.
export function validateStream(on: (ev: { type: string; message?: string }) => void): () => void {
  const es = new EventSource('/api/v1/config/validate')
  es.onmessage = (e) => {
    try {
      on(JSON.parse(e.data))
    } catch {
      /* ignore malformed */
    }
  }
  es.onerror = () => {
    es.close()
  }
  return () => es.close()
}
