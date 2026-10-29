// The auth API client (design §6). Self-contained — it carries the __Host
// session cookie (same-origin fetch sends it) and the per-session CSRF token on
// every mutation, so it doesn't depend on the shared config client. The CSRF
// token is learned from auth/state and refreshed on sign-in.
import { signal } from '@preact/signals'
import { getAssertion, createCredential, passkeysUsable } from '../lib/webauthn'

export type Method = 'passkey' | 'ssh' | 'password' | 'totp'

export interface AuthState {
  signed_in: boolean
  required?: boolean
  operator?: string
  listener?: string
  // How this browser reaches the dashboard: https, localhost (both secure
  // contexts) or http (plain HTTP off loopback — not encrypted).
  transport?: 'https' | 'localhost' | 'http'
  // Signed out: the sign-in methods this connection offers, strongest first.
  methods?: Method[]
  password_needs_totp?: boolean
  // Signed in: how this session signed in, and how it can step up.
  method?: Method
  stepup_methods?: Method[]
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

// signInMethods is what the sign-in page offers: the server's list, with
// passkeys dropped when this page can't run WebAuthn (not a secure context, or
// no WebAuthn in this browser).
export function signInMethods(st: AuthState): Method[] {
  return (st.methods ?? []).filter((m) => m !== 'passkey' || passkeysUsable())
}

// stepupMethods is the same filter for a signed-in session's step-up methods.
export function stepupMethods(st: AuthState): Method[] {
  return (st.stepup_methods ?? []).filter((m) => m !== 'passkey' || passkeysUsable())
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

async function signedIn(res: { csrf: string }): Promise<void> {
  csrf = res.csrf
  await refreshState()
}

export async function signInPassword(operator: string, password: string, code: string): Promise<void> {
  await signedIn(await send<{ csrf: string }>('POST', '/api/v1/auth/password/login', { operator, password, code }))
}

// SSHChallenge is a server-issued SSH challenge: the text to sign, its
// namespace, and the login code another device can approve it with.
export interface SSHChallenge {
  challenge_id: string
  challenge: string
  namespace: string
  login_code: string
  expires: string
}

export const sshBegin = (operator: string) => send<SSHChallenge>('POST', '/api/v1/auth/ssh/begin', { operator })

export async function sshFinish(id: string, signature: string): Promise<void> {
  await signedIn(await send<{ csrf: string }>('POST', '/api/v1/auth/ssh/finish', { challenge_id: id, signature }))
}

// sshPoll reports whether approve-login has signed the challenge; when it has,
// the session cookie is set by this response.
export async function sshPoll(id: string): Promise<boolean> {
  const r = await send<{ pending?: boolean; csrf?: string }>('POST', '/api/v1/auth/ssh/poll', { challenge_id: id })
  if (r.pending) return false
  await signedIn(r as { csrf: string })
  return true
}

export async function signOut(): Promise<void> {
  await send('POST', '/api/v1/auth/logout', {})
  await refreshState()
}

// --- step-up ---

// A step-up request the StepupDialog serves: what is being confirmed, and the
// promise to settle with the single-use token (or reject on cancel).
export interface StepupRequest {
  purpose: string
  digest: string
  resolve: (token: string) => void
  reject: (e: Error) => void
}

export const stepupRequest = signal<StepupRequest | null>(null)

// stepUp asks the operator to confirm {purpose, digest} with any step-up method
// they have (the StepupDialog), and returns a single-use token to put in
// X-Conductor-Stepup on the sensitive request. Callers don't know or care which
// method was used.
export function stepUp(purpose: string, digest: string): Promise<string> {
  stepupRequest.value?.reject(new Error('superseded by another confirmation'))
  return new Promise<string>((resolve, reject) => {
    stepupRequest.value = {
      purpose,
      digest,
      resolve: (t) => {
        stepupRequest.value = null
        resolve(t)
      },
      reject: (e) => {
        stepupRequest.value = null
        reject(e)
      },
    }
  })
}

async function stepupToken(res: { token: string }): Promise<string> {
  await refreshState() // the session secret rotated
  return res.token
}

export async function stepupPasskey(purpose: string, digest: string): Promise<string> {
  const begin = await send<{ ceremony_id: string; options: unknown }>('POST', '/api/v1/auth/stepup/begin', { purpose, digest })
  const response = await getAssertion(begin.options as { publicKey: Record<string, unknown> })
  return stepupToken(await send<{ token: string }>('POST', '/api/v1/auth/stepup/finish', { ceremony_id: begin.ceremony_id, response }))
}

export async function stepupTOTP(purpose: string, digest: string, code: string): Promise<string> {
  return stepupToken(await send<{ token: string }>('POST', '/api/v1/auth/stepup/totp', { purpose, digest, code }))
}

export async function stepupPassword(purpose: string, digest: string, password: string): Promise<string> {
  return stepupToken(await send<{ token: string }>('POST', '/api/v1/auth/stepup/password', { purpose, digest, password }))
}

export const stepupSSHBegin = (purpose: string, digest: string) =>
  send<SSHChallenge>('POST', '/api/v1/auth/stepup/ssh/begin', { purpose, digest })

export async function stepupSSHFinish(id: string, signature: string): Promise<string> {
  return stepupToken(await send<{ token: string }>('POST', '/api/v1/auth/stepup/ssh/finish', { challenge_id: id, signature }))
}

// stepupSSHPoll returns the token once approve-login has signed, else ''.
export async function stepupSSHPoll(id: string): Promise<string> {
  const r = await send<{ pending?: boolean; token?: string }>('POST', '/api/v1/auth/stepup/ssh/poll', { challenge_id: id })
  if (r.pending || !r.token) return ''
  return stepupToken(r as { token: string })
}

// sendStepped sends a mutation carrying a step-up token.
function sendStepped<T>(method: string, path: string, token: string, body?: unknown): Promise<T> {
  return fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'X-Conductor-CSRF': csrf,
      'X-Conductor-Stepup': token,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => handle<T>(r))
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

export interface TOTPEnrollment {
  secret: string
  uri: string
  qr: string // data:image/png;base64,…
}

export const enrollPasswordBegin = (ceremonyID: string) =>
  send<{ totp: TOTPEnrollment; totp_required: boolean }>('POST', '/api/v1/enroll/password/begin', { ceremony_id: ceremonyID })

export const enrollPasswordFinish = (ceremonyID: string, password: string, code: string) =>
  send('POST', '/api/v1/enroll/password/finish', { ceremony_id: ceremonyID, password, code })

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
  method: Method
  transport: 'https' | 'localhost' | 'http'
  host?: string
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
  password: { set: boolean; set_at: string }
  totp: { set: boolean; set_at: string }
  passkey_here: boolean
  auth_policy: { methods: Method[]; password_requires_totp: boolean; stepup: Method[] }
  stepup_methods: Method[]
  credential_digests: Record<'password.set' | 'password.remove' | 'totp.set' | 'totp.remove' | 'passkey.add', string>
  transport: 'https' | 'localhost' | 'http'
}

export const access = () => get<AccessData>('/api/v1/access')

// Credential changes while signed in: each confirms with a step-up first
// (purpose credential.change, a per-action digest the Access read supplies).
const credentialPurpose = 'credential.change'

export async function setPassword(d: AccessData, password: string): Promise<void> {
  const token = await stepUp(credentialPurpose, d.credential_digests['password.set'])
  await sendStepped('POST', '/api/v1/auth/password', token, { password })
}

export async function removePassword(d: AccessData): Promise<void> {
  const token = await stepUp(credentialPurpose, d.credential_digests['password.remove'])
  await sendStepped('DELETE', '/api/v1/auth/password', token)
}

export const totpBegin = () => send<TOTPEnrollment>('POST', '/api/v1/auth/totp/begin', {})

export async function totpConfirm(d: AccessData, code: string): Promise<void> {
  // Activating the authenticator is a credential change, so it asks for a confirmation.
  const token = await stepUp(credentialPurpose, d.credential_digests['totp.set'])
  await sendStepped('POST', '/api/v1/auth/totp/confirm', token, { code })
}

export async function removeTOTP(d: AccessData): Promise<void> {
  const token = await stepUp(credentialPurpose, d.credential_digests['totp.remove'])
  await sendStepped('DELETE', '/api/v1/auth/totp', token)
}

// addPasskeyHere registers a passkey for the hostname this page is on.
export async function addPasskeyHere(d: AccessData, name: string): Promise<void> {
  const token = await stepUp(credentialPurpose, d.credential_digests['passkey.add'])
  const begin = await sendStepped<{ ceremony_id: string; options: unknown }>('POST', '/api/v1/passkeys/register/begin', token, { name })
  const response = await createCredential(begin.options as { publicKey: Record<string, unknown> })
  await send('POST', '/api/v1/passkeys/register/finish', { ceremony_id: begin.ceremony_id, name, response })
}
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
  await sendStepped('POST', `/api/v1/bots/${encodeURIComponent(bot)}/proposals/${encodeURIComponent(id)}/decision`, token, { decision })
}
