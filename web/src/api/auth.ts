// The auth API client (design §6). Self-contained — it carries the __Host
// session cookie (same-origin fetch sends it) and the per-session CSRF token on
// every mutation, so it doesn't depend on the shared config client. The CSRF
// token is learned from auth/state and refreshed on sign-in.
import { signal } from '@preact/signals'
import { getAssertion, createCredential } from '../lib/webauthn'

export interface AuthState {
  signed_in: boolean
  required?: boolean
  operator?: string
  listener?: string
  expires?: string
  csrf?: string
  stepup_at?: string
}

export const authState = signal<AuthState>({ signed_in: false })
let csrf = ''

interface APIError {
  error?: { code?: string; message?: string; hint?: string }
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const b = (await res.json()) as APIError
      if (b?.error?.message) message = b.error.message
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

function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  return fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(csrf ? { 'X-Conductor-CSRF': csrf } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => handle<T>(r))
}

// refreshState reads auth/state and caches the CSRF token.
export async function refreshState(): Promise<AuthState> {
  const s = await get<AuthState>('/api/v1/auth/state')
  authState.value = s
  if (s.csrf) csrf = s.csrf
  return s
}

// --- sign-in ---

export async function signIn(): Promise<void> {
  const begin = await send<{ ceremony_id: string; options: unknown }>('POST', '/api/v1/auth/login/begin', {})
  const response = await getAssertion(begin.options as { publicKey: Record<string, unknown> })
  const res = await send<{ operator: string; csrf: string }>('POST', '/api/v1/auth/login/finish', {
    ceremony_id: begin.ceremony_id,
    response,
  })
  csrf = res.csrf
  await refreshState()
}

export async function signOut(): Promise<void> {
  await send('POST', '/api/v1/auth/logout', {})
  await refreshState()
}

// --- step-up ---

// stepUp runs an assertion over {purpose, digest} and returns a single-use token
// to put in X-Conductor-Stepup on the sensitive request.
export async function stepUp(purpose: string, digest: string): Promise<string> {
  const begin = await send<{ ceremony_id: string; options: unknown }>('POST', '/api/v1/auth/stepup/begin', { purpose, digest })
  const response = await getAssertion(begin.options as { publicKey: Record<string, unknown> })
  const res = await send<{ token: string }>('POST', '/api/v1/auth/stepup/finish', { ceremony_id: begin.ceremony_id, response })
  await refreshState() // the secret rotated
  return res.token
}

// --- enrollment ---

export interface RedeemResult {
  ceremony_id: string
  operator: string
  existing: number
}

export function enrollRedeem(t: string, pairingCode: string): Promise<RedeemResult> {
  return send<RedeemResult>('POST', '/api/v1/enroll/redeem', { t, pairing_code: pairingCode })
}

export interface BeginResult {
  need_assertion: boolean
  options: unknown
}

export function enrollBegin(ceremonyID: string, rp: string, name: string, assertion?: unknown): Promise<BeginResult> {
  return send<BeginResult>('POST', '/api/v1/enroll/begin', { ceremony_id: ceremonyID, rp, name, assertion: assertion ?? null })
}

export async function enroll(ceremonyID: string, rp: string, name: string, existing: number): Promise<void> {
  let begin = await enrollBegin(ceremonyID, rp, name)
  if (begin.need_assertion) {
    // Adding a passkey when you already have one: confirm with an existing passkey.
    const assertion = await getAssertion(begin.options as { publicKey: Record<string, unknown> })
    begin = await enrollBegin(ceremonyID, rp, name, assertion)
    if (begin.need_assertion) throw new Error('could not confirm with an existing passkey')
  }
  const response = await createCredential(begin.options as { publicKey: Record<string, unknown> })
  await send('POST', '/api/v1/enroll/finish', { ceremony_id: ceremonyID, name, response })
  void existing
}

// --- Access screen ---

export interface Passkey {
  id: string
  name: string
  rp_id: string
  listener: string
  device_bound: boolean
  created: string
  last_used: string
  uses: number
  locked: boolean
}

export interface SessionRow {
  id: string
  device: string
  addr: string
  listener: string
  created: string
  last_seen: string
  current: boolean
}

export interface ListenerFact {
  listener: string
  rp_id: string
  origins: string[]
}

export interface AccessData {
  operator: string
  passkeys: Passkey[]
  sessions: SessionRow[]
  listeners: ListenerFact[]
  session_policy: { idle: string; max: string }
}

export const access = () => get<AccessData>('/api/v1/access')
export const removePasskey = (id: string) => send('DELETE', `/api/v1/passkeys/${encodeURIComponent(id)}`)
export const endSession = (id: string) => send('DELETE', `/api/v1/sessions/${encodeURIComponent(id)}`)
export const signOutEverywhere = () => send('DELETE', '/api/v1/sessions')

// --- proposal approval (step-up) ---

export interface ApprovalView {
  bot: string
  proposal: string
  action_sha256: string
  digests: { approve: string; deny: string }
  summary: unknown
  operators: string[]
  purpose: string
}

export const approvalView = (bot: string, id: string) =>
  get<ApprovalView>(`/api/v1/bots/${encodeURIComponent(bot)}/proposals/${encodeURIComponent(id)}/approval`)

// decideProposal steps up over the per-decision digest, then submits the
// decision with the step-up token (design §6.6).
export async function decideProposal(bot: string, id: string, decision: 'approve' | 'deny'): Promise<void> {
  const view = await approvalView(bot, id)
  const digest = decision === 'approve' ? view.digests.approve : view.digests.deny
  const token = await stepUp('approve', digest)
  await fetch(`/api/v1/bots/${encodeURIComponent(bot)}/proposals/${encodeURIComponent(id)}/decision`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'X-Conductor-CSRF': csrf,
      'X-Conductor-Stepup': token,
    },
    body: JSON.stringify({ decision }),
  }).then((r) => handle(r))
}
