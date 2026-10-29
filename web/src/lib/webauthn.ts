// WebAuthn browser glue (design §6.3/§6.5): turn the server's PublicKey options
// (base64url strings, as go-webauthn marshals them) into the ArrayBuffers
// navigator.credentials wants, and serialize the credential back into the JSON
// the server parses. Kept tiny and dependency-free.

function fromB64url(s: string): Uint8Array {
  const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4))
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + pad)
  const out = new Uint8Array(b.length)
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i)
  return out
}

function toB64url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// webauthnSupported reports whether this browser can do passkeys.
export function webauthnSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

// passkeysUsable reports whether a passkey ceremony can run on this page: the
// browser has WebAuthn AND the page is a secure context (HTTPS or localhost) —
// browsers refuse WebAuthn on plain HTTP elsewhere.
export function passkeysUsable(): boolean {
  return typeof window !== 'undefined' && window.isSecureContext === true && webauthnSupported()
}

// PkOptions is the server's PublicKey options (base64url strings). We keep it
// loose because the shape is the WebAuthn spec's and we only rewrite a few fields.
export interface PkOptions {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  publicKey: any
}

// getAssertion runs navigator.credentials.get for a sign-in or step-up and
// returns the response JSON the server expects.
export async function getAssertion(options: PkOptions): Promise<unknown> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const pk: any = { ...options.publicKey }
  pk.challenge = fromB64url(pk.challenge)
  if (Array.isArray(pk.allowCredentials)) {
    pk.allowCredentials = pk.allowCredentials.map((c: { id: string }) => ({ ...c, id: fromB64url(c.id) }))
  }
  const cred = (await navigator.credentials.get({ publicKey: pk as PublicKeyCredentialRequestOptions })) as PublicKeyCredential
  const resp = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: toB64url(resp.authenticatorData),
      clientDataJSON: toB64url(resp.clientDataJSON),
      signature: toB64url(resp.signature),
      userHandle: resp.userHandle ? toB64url(resp.userHandle) : '',
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  }
}

// createCredential runs navigator.credentials.create for enrollment and returns
// the attestation response JSON the server expects.
export async function createCredential(options: PkOptions): Promise<unknown> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const pk: any = { ...options.publicKey }
  pk.challenge = fromB64url(pk.challenge)
  pk.user = { ...pk.user, id: fromB64url(pk.user.id) }
  if (Array.isArray(pk.excludeCredentials)) {
    pk.excludeCredentials = pk.excludeCredentials.map((c: { id: string }) => ({ ...c, id: fromB64url(c.id) }))
  }
  const cred = (await navigator.credentials.create({ publicKey: pk as PublicKeyCredentialCreationOptions })) as PublicKeyCredential
  const resp = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: toB64url(resp.attestationObject),
      clientDataJSON: toB64url(resp.clientDataJSON),
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  }
}
