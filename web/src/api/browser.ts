// The typed client for the browsers + wallet surface (design §4.5): live agent
// sessions with a screencast and takeover, personal details (profiles), payment
// cards and purchase approvals. It wraps the daemon's /api/v1/browser routes,
// same-origin, carrying the session cookie and the CSRF token on every write; a
// widening change or a purchase decision also carries a step-up token. No answer
// ever carries a value — only each field's source (design §4.5.1).
import { authState, stepUp } from './auth'

function csrf(): string {
  return authState.value.csrf || ''
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const b = (await res.json()) as { error?: string | { message?: string } }
      if (typeof b.error === 'string') message = b.error
      else if (b.error?.message) message = b.error.message
    } catch {
      /* non-JSON */
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

function get<T>(path: string): Promise<T> {
  return fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' }).then((r) => handle<T>(r))
}

// --- shapes (mirror the Go JSON) ---

export interface BrowserSession {
  id: string
  identity: string
  agent?: string
  requester?: string
  profile: string
  last_used: string
  leased_by?: string
}

export interface FieldView {
  kind: string // stored | vault | env | secret | literal
  source: string
}

// CardLimits marshals with Go field names (no json tags on the config type).
export interface CardLimits {
  PerPurchase: number
  PerDay: number
  PerMonth: number
  Currency?: string
}

export interface WalletProfile {
  name: string
  method: string // config | stored
  identities: string[] | null
  sites: { origin: string; fields: string[] }[] | null
  visible: Record<string, string> | null
  fields: Record<string, FieldView> | null
  editable: boolean
}

export interface WalletCard {
  name: string
  method: string
  kind: string
  identities: string[] | null
  merchants: string[] | null
  limits: CardLimits
  identity_limits: Record<string, CardLimits> | null
  frozen: boolean
  frozen_at?: string
  fields: Record<string, FieldView> | null
  editable: boolean
  brand: string
  last4: string
}

export interface SpendSummary {
  card?: string
  identity?: string
  bot?: string
  team?: string
  currency: string
  day: number
  month: number
}

export interface Wallet {
  profiles: WalletProfile[] | null
  cards: WalletCard[] | null
  spend?: SpendSummary[]
}

export interface PurchaseItem {
  name: string
  qty?: number
  amount?: number
}

export interface Receipt {
  order_number?: string
  total?: number
  currency?: string
  url?: string
  at: string
  note?: string
}

export interface LayerState {
  layer: string
  window: string // purchase | day | 30 days | ticket
  limit: number
  used: number
  remaining: number
  currency: string
  note?: string
  binding?: boolean
}

export interface Purchase {
  id: string
  card: string
  merchant: string
  items?: PurchaseItem[]
  total: number
  currency: string
  recurring?: boolean
  interval?: string
  why?: string
  identity: string
  requester?: string
  bot?: string
  case?: string
  status: string // pending | approved | denied | expired | used | refused | completed
  created: string
  decided?: string
  approver?: string
  expires: string
  digest: string
  bot_proposal?: string
  supersedes?: string
  reason?: string
  receipt?: Receipt
  case_ref?: string
  layers?: LayerState[]
  binding?: string
}

export interface PurchasesResponse {
  purchases: Purchase[] | null
  approvers?: string[] | null
}

export interface PurchaseDetail {
  purchase: Purchase
  approvers: string[] | null
  remaining_by_layer: LayerState[] | null
  would_refuse?: string
}

export interface LoginRecord {
  name: string
  method: string // password | vault | session | config
  origins?: string[]
  site?: string
  bindings: string[] | null
  login_url?: string
  fields?: Record<string, FieldView>
  item?: string
  added?: string
  last_used?: string
  last_test?: { status: string; reason?: string; at?: string }
  editable: boolean
  approval?: boolean
}

export interface SessionsResponse {
  sessions: BrowserSession[] | null
}
export interface LoginsResponse {
  logins: LoginRecord[] | null
}

// --- reads ---

export const browser = {
  sessions: () => get<SessionsResponse>('/api/v1/browser/sessions'),
  session: (id: string) => get<{ session: BrowserSession }>('/api/v1/browser/sessions/' + encodeURIComponent(id)),
  wallet: () => get<Wallet>('/api/v1/browser/wallet'),
  logins: () => get<LoginsResponse>('/api/v1/browser/logins'),
  purchases: (status = '') =>
    get<PurchasesResponse>('/api/v1/browser/purchases' + (status ? '?status=' + encodeURIComponent(status) : '')),
  purchase: (id: string) => get<PurchaseDetail>('/api/v1/browser/purchases/' + encodeURIComponent(id)),
  peekURL: (id: string) => '/api/v1/browser/sessions/' + encodeURIComponent(id) + '/peek',
  screencastURL: (id: string) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${proto}//${location.host}/api/v1/browser/sessions/${encodeURIComponent(id)}/screencast`
  },
}

// op runs a wallet mutation. The server classifies the change; a widening one
// comes back 401 with the digest to step up over, so we sign and retry once
// (design §4.5 — add a profile/card, a binding, a site, a shop, a higher limit,
// an unfreeze). Narrowing and neutral changes go through first time.
export async function walletOp(cmd: string, req: Record<string, unknown>): Promise<unknown> {
  const body = JSON.stringify({ cmd, req })
  let res = await fetch('/api/v1/browser/op', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-Conductor-CSRF': csrf() },
    body,
  })
  if (res.status === 401) {
    const b = (await res.clone().json().catch(() => ({}))) as { stepup?: { purpose: string; digest: string } }
    if (b.stepup) {
      const token = await stepUp(b.stepup.purpose, b.stepup.digest)
      res = await fetch('/api/v1/browser/op', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          'X-Conductor-CSRF': csrf(),
          'X-Conductor-Stepup': token,
        },
        body,
      })
    }
  }
  return handle<unknown>(res)
}

// freeze/unfreeze a card. Freeze narrows (no passkey); unfreeze widens (passkey),
// handled transparently by walletOp.
export const freezeCard = (name: string) => walletOp('browser-card-freeze', { name })
export const unfreezeCard = (name: string) => walletOp('browser-card-unfreeze', { name })

// --- takeover ---

export async function takeover(id: string): Promise<void> {
  let res = await fetch(`/api/v1/browser/sessions/${encodeURIComponent(id)}/takeover`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'X-Conductor-CSRF': csrf() },
  })
  if (res.status === 401) {
    const b = (await res.clone().json().catch(() => ({}))) as { stepup?: { purpose: string; digest: string } }
    if (b.stepup) {
      const token = await stepUp(b.stepup.purpose, b.stepup.digest)
      res = await fetch(`/api/v1/browser/sessions/${encodeURIComponent(id)}/takeover`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { Accept: 'application/json', 'X-Conductor-CSRF': csrf(), 'X-Conductor-Stepup': token },
      })
    }
  }
  await handle(res)
}

export function releaseTakeover(id: string): Promise<unknown> {
  return fetch(`/api/v1/browser/sessions/${encodeURIComponent(id)}/release`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'X-Conductor-CSRF': csrf() },
  }).then((r) => handle(r))
}

export function sendInput(id: string, ev: Record<string, unknown>): Promise<unknown> {
  return fetch(`/api/v1/browser/sessions/${encodeURIComponent(id)}/input`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-Conductor-CSRF': csrf() },
    body: JSON.stringify(ev),
  }).then((r) => handle(r))
}

// --- purchase approval (passkey step-up, mirrors a bot proposal) ---

interface PurchaseApproval {
  purchase: string
  action_sha256: string
  digests: { approve: string; deny: string }
  summary: unknown
  operators: string[] | null
  purpose: string
}

export const purchaseApproval = (id: string) =>
  get<PurchaseApproval>('/api/v1/browser/purchases/' + encodeURIComponent(id) + '/approval')

export async function decidePurchase(id: string, decision: 'approve' | 'deny', reason = ''): Promise<void> {
  const view = await purchaseApproval(id)
  const digest = decision === 'approve' ? view.digests.approve : view.digests.deny
  const token = await stepUp(view.purpose, digest)
  await fetch(`/api/v1/browser/purchases/${encodeURIComponent(id)}/decision`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'X-Conductor-CSRF': csrf(),
      'X-Conductor-Stepup': token,
    },
    body: JSON.stringify({ decision, reason }),
  }).then((r) => handle(r))
}

// --- small formatters shared by the wallet screens ---

export function money(amount: number, currency = 'USD'): string {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount)
  } catch {
    return `${currency} ${amount.toFixed(2)}`
  }
}

// who renders an identity id (bot:atlas, team:platform, agent:triage@acme/app)
// as words.
export function who(id: string): string {
  if (id.startsWith('bot:')) return id.slice(4)
  if (id.startsWith('team:')) return 'team ' + id.slice(5)
  if (id.startsWith('agent:')) return 'the ' + id.slice(6) + ' agent'
  return id
}

// hostOf strips scheme and trailing slash from an origin for display.
export function hostOf(origin: string): string {
  return origin.replace(/^https?:\/\//, '').replace(/\/$/, '')
}
