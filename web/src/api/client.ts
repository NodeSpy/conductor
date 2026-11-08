// The dashboard's typed fetch client. Shapes come from the tygo-generated
// types.ts (the Go API is the source of truth). Same-origin only (design §5.1:
// no CORS); errors arrive as {error:{code,message,hint}}.
import type {
  StatusResponse,
  RunsResponse,
  ConfigResponse,
  ConfigChange,
  ReviewResponse,
  AppliedResponse,
  HistoryItem,
  HistoryDiffResponse,
  StageRequest,
  SecretsResponse,
  PacksResponse,
  PackManifestResponse,
  PluginsResponse,
  ConnectionsResponse,
  CatalogResponse,
  AddReview,
  SystemResponse,
  LogResponse,
  AuditResponse,
  RunDetailResponse,
  ActionResponse,
  RetryRequest,
  WorkflowsResponse,
  WorkflowDetailResponse,
  TestResponse,
  AttrsResponse,
  AttrValuesResponse,
} from './types'
import { authState, stepUp } from './auth'
import { parseSafe, readJSON } from './safe'
import { readResponse } from './errors'

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
  return handle<T>(res)
}

async function sendJSON<T>(method: string, path: string, body: unknown): Promise<T> {
  // Mutations carry the per-session CSRF token when signed in (design §6.4); the
  // __Host session cookie rides along on the same-origin request.
  const csrf = authState.value.csrf
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(csrf ? { 'X-Conductor-CSRF': csrf } : {}),
    },
    body: JSON.stringify(body ?? {}),
  })
  return handle<T>(res)
}

// retryRun retries a run. A forced retry (re-running steps that already
// succeeded) comes back 401 with a step-up to confirm, described by the
// server with the steps it replays; we confirm and send it once more.
async function retryRun(id: string, req: RetryRequest): Promise<ActionResponse> {
  const path = '/api/v1/runs/' + encodeURIComponent(id) + '/retry'
  const send = (stepup?: string) => {
    const csrf = authState.value.csrf
    return fetch(path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...(csrf ? { 'X-Conductor-CSRF': csrf } : {}),
        ...(stepup ? { 'X-Conductor-Stepup': stepup } : {}),
      },
      body: JSON.stringify(req),
    })
  }
  let res = await send()
  if (res.status === 401) {
    const b = (await readJSON(res.clone()).catch(() => ({}))) as { stepup?: { purpose: string; digest: string; description?: string } }
    if (b.stepup) {
      const token = await stepUp(b.stepup.purpose, b.stepup.digest, {
        what: `Re-run ${id}, repeating steps that already ran`,
        description: b.stepup.description,
      })
      res = await send(token)
    }
  }
  return handle<ActionResponse>(res)
}

// installUpdate installs (or rolls back to) a published version. This
// changes what code the daemon runs, so — like config apply — a signed-in
// session confirms it's them first: the first attempt comes back 403 with a
// step-up description bound to this exact version, we ask for it, then retry
// once carrying the token. There is no `force`: the dashboard can never skip
// the updater's own preflight.
async function installUpdate(version: string): Promise<{ installed: string }> {
  const path = '/api/v1/system/update/install'
  const send = (stepup?: string) => {
    const csrf = authState.value.csrf
    return fetch(path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...(csrf ? { 'X-Conductor-CSRF': csrf } : {}),
        ...(stepup ? { 'X-Conductor-Stepup': stepup } : {}),
      },
      body: JSON.stringify({ version }),
    })
  }
  let res = await send()
  if (res.status === 403) {
    const b = (await readJSON(res.clone()).catch(() => ({}))) as { stepup?: { purpose: string; digest: string; description?: string } }
    if (b.stepup) {
      const token = await stepUp(b.stepup.purpose, b.stepup.digest, {
        what: `Install ${version}`,
        description: b.stepup.description,
      })
      res = await send(token)
    }
  }
  return handle<{ installed: string }>(res)
}

// handle reads an answer as data, or throws an ApiError worded for a person.
function handle<T>(res: Response): Promise<T> {
  return readResponse<T>(res)
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
  apply: (token: string, stepupToken?: string) =>
    sendJSON<AppliedResponse>('POST', '/api/v1/config/apply', { token, ...(stepupToken ? { stepup_token: stepupToken } : {}) }),
  history: () => getJSON<{ history: HistoryItem[] }>('/api/v1/config/history'),
  historyDiff: (id: string) => getJSON<HistoryDiffResponse>('/api/v1/config/history/' + encodeURIComponent(id) + '/diff'),
  restoreReview: (id: string) => sendJSON<ReviewResponse>('POST', '/api/v1/config/restore/review', { id }),
  restore: (id: string, token: string, stepupToken?: string) =>
    sendJSON<AppliedResponse>('POST', '/api/v1/config/restore', { id, token, ...(stepupToken ? { stepup_token: stepupToken } : {}) }),

  // --- run detail + controls ---
  run: (id: string) => getJSON<RunDetailResponse>('/api/v1/runs/' + encodeURIComponent(id)),
  retryRun: (id: string, req: RetryRequest = {}) => retryRun(id, req),
  stopRun: (id: string) =>
    sendJSON<ActionResponse>('POST', '/api/v1/runs/' + encodeURIComponent(id) + '/stop', {}),

  // --- workflows ---
  workflows: () => getJSON<WorkflowsResponse>('/api/v1/workflows'),
  workflow: (name: string) => getJSON<WorkflowDetailResponse>('/api/v1/workflows/' + encodeURIComponent(name)),
  testWorkflow: (name: string) =>
    sendJSON<TestResponse>('POST', '/api/v1/workflows/' + encodeURIComponent(name) + '/test', {}),

  // --- connector-published filters ---
  connectorAttrs: () => getJSON<AttrsResponse>('/api/v1/connectors/attrs'),
  attrValues: (attr: string) =>
    getJSON<AttrValuesResponse>('/api/v1/connectors/attrs/values?attr=' + encodeURIComponent(attr)),

  // --- section reads ---
  secrets: () => getJSON<SecretsResponse>('/api/v1/secrets'),
  packs: () => getJSON<PacksResponse>('/api/v1/packs'),
  packManifest: (name: string) => getJSON<PackManifestResponse>('/api/v1/packs/' + encodeURIComponent(name) + '/manifest'),
  plugins: () => getJSON<PluginsResponse>('/api/v1/plugins'),
  connections: () => getJSON<ConnectionsResponse>('/api/v1/connections'),
  pluginCatalog: (kind: 'connector' | 'runtime' | 'engine') => getJSON<CatalogResponse>('/api/v1/plugins/catalog?kind=' + kind),
  pluginReview: (kind: 'connector' | 'runtime' | 'engine', use: string, as = '') =>
    getJSON<AddReview>(`/api/v1/plugins/review?kind=${kind}&use=${encodeURIComponent(use)}&as=${encodeURIComponent(as)}`),
  system: () => getJSON<SystemResponse>('/api/v1/system'),
  setPaused: (paused: boolean) => sendJSON<{ paused: boolean }>('POST', '/api/v1/system/pause', { paused }),
  checkUpdate: () => sendJSON<SystemResponse>('POST', '/api/v1/system/update/check', {}),
  installUpdate: (version: string) => installUpdate(version),
  daemonLog: () => getJSON<LogResponse>('/api/v1/system/log'),
  audit: () => getJSON<AuditResponse>('/api/v1/audit'),
}

// validateStream opens the SSE validate stream and calls back with each event.
// Returns a stop() that closes the stream.
export function validateStream(on: (ev: { type: string; message?: string }) => void): () => void {
  const es = new EventSource('/api/v1/config/validate')
  es.onmessage = (e) => {
    try {
      on(parseSafe(e.data))
    } catch {
      /* ignore malformed */
    }
  }
  es.onerror = () => {
    es.close()
  }
  return () => es.close()
}
