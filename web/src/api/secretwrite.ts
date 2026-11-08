// Storing a new secret from a form (the Plugins page's "store a new secret"
// on a credential field): the value goes to the vault through the Secrets
// write path (api.writeSecret: write-only, confirmed with a step-up bound to
// the exact vault, key and length) and only the reference ever reaches the
// config. The value is never kept, shown or staged.
import { api } from './client'

export async function storeSecret(vault: string, key: string, value: string): Promise<string> {
  await api.writeSecret(vault, key, value)
  return `{{ vault "${vault}" "${key}" }}`
}
