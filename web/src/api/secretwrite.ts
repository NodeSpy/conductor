// Storing a new secret from a form (the Plugins page's "store a new secret"
// on a credential field): the value goes to the vault through the Secrets
// write path (POST /api/v1/secrets/write) — write-only, confirmed with a
// step-up bound to the exact vault, key and length — and only the reference
// ever reaches the config. The value is never kept, shown or staged.
import { authState, stepUp } from './auth'
import { readJSON } from './safe'
import { readResponse } from './errors'

export async function storeSecret(vault: string, key: string, value: string): Promise<string> {
  const path = '/api/v1/secrets/write'
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
      body: JSON.stringify({ vault, key, value }),
    })
  }
  let res = await send()
  if (res.status === 401 || res.status === 403) {
    const b = (await readJSON(res.clone()).catch(() => ({}))) as { stepup?: { purpose: string; digest: string; description?: string } }
    if (b.stepup) {
      const token = await stepUp(b.stepup.purpose, b.stepup.digest, { what: `Store a value in ${vault}/${key}`, description: b.stepup.description })
      res = await send(token)
    }
  }
  await readResponse<unknown>(res)
  return `{{ vault "${vault}" "${key}" }}`
}
