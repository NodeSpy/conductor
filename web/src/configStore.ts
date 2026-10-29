// Config-edit state as signals (design §5.3). The Config screen and the typed
// section screens (Secrets, Packs, System) all stage path edits here and apply
// them through one pipeline: stage → review (diff + effects + widening) → apply
// (with the step-up confirm token). The backend holds the staged set, so these
// signals mirror its responses rather than owning the source of truth.
import { signal } from '@preact/signals'
import { api, validateStream } from './api/client'
import { authState, stepUp } from './api/auth'
import type { ConfigResponse, ConfigChange, ReviewResponse, AppliedResponse } from './api/types'

export const config = signal<ConfigResponse | null>(null)
export const staged = signal<ConfigChange[]>([])
export const review = signal<ReviewResponse | null>(null)
export const applyResult = signal<AppliedResponse | null>(null)
export const cfgError = signal<string>('')
export const busy = signal(false)
export const validateMsg = signal<{ type: string; message?: string } | null>(null)

export async function loadConfig(): Promise<void> {
  try {
    const c = await api.config()
    config.value = c
    staged.value = c.staged ?? []
    cfgError.value = ''
  } catch (e) {
    cfgError.value = msg(e)
  }
}

export async function stage(path: string[], value: unknown, summary = ''): Promise<void> {
  try {
    busy.value = true
    const r = await api.stage({ path, value, summary })
    staged.value = r.staged ?? []
    review.value = null // a new edit invalidates the review (and its token)
    applyResult.value = null
    cfgError.value = ''
  } catch (e) {
    cfgError.value = msg(e)
  } finally {
    busy.value = false
  }
}

export async function stageDelete(path: string[], summary = ''): Promise<void> {
  try {
    busy.value = true
    const r = await api.stage({ path, delete: true, summary })
    staged.value = r.staged ?? []
    review.value = null
    cfgError.value = ''
  } catch (e) {
    cfgError.value = msg(e)
  } finally {
    busy.value = false
  }
}

export async function unstage(id: string): Promise<void> {
  const r = await api.unstage(id)
  staged.value = r.staged ?? []
  review.value = null
}

export async function clearStaged(): Promise<void> {
  const r = await api.clearStaged()
  staged.value = r.staged ?? []
  review.value = null
  applyResult.value = null
}

export async function doReview(): Promise<ReviewResponse | null> {
  try {
    busy.value = true
    const r = await api.review()
    review.value = r
    cfgError.value = ''
    return r
  } catch (e) {
    cfgError.value = msg(e)
    return null
  } finally {
    busy.value = false
  }
}

// runValidate streams the validate-on-copy result into validateMsg.
export function runValidate(): () => void {
  validateMsg.value = { type: 'start' }
  return validateStream((ev) => {
    validateMsg.value = ev
  })
}

export async function apply(): Promise<void> {
  const rev = review.value
  if (!rev?.token) {
    cfgError.value = 'review the change first'
    return
  }
  try {
    busy.value = true
    // A change that widens a guardrail or touches who can sign in needs a
    // step-up when sign-in is on; the sheet lists what the apply does.
    let stepupToken: string | undefined
    if (rev.stepup && authState.value.signed_in) {
      const lines = [...(rev.guarded ?? []), ...(rev.widening ?? []), ...(rev.effects ?? [])].map((e) => e.text)
      stepupToken = await stepUp('config.apply', rev.token, { what: 'Apply this config change', lines: [...new Set(lines)] })
    }
    const res = await api.apply(rev.token, stepupToken)
    applyResult.value = res
    review.value = null
    staged.value = []
    cfgError.value = ''
  } catch (e) {
    cfgError.value = msg(e)
  } finally {
    busy.value = false
  }
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
