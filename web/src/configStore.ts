// Config-edit state as signals (design §5.3). The Config screen and the typed
// section screens (Secrets, Packs, System) all stage path edits here and apply
// them through one pipeline: stage → review (diff + effects + widening) → apply
// (with the step-up confirm token). The backend holds the staged set, so these
// signals mirror its responses rather than owning the source of truth.
import { signal } from '@preact/signals'
import { api, validateStream } from './api/client'
import { authState, stepUp } from './api/auth'
import type { ConfigResponse, ConfigChange, ReviewResponse, AppliedResponse, HistoryItem } from './api/types'
import { humanError } from './api/errors'

export const config = signal<ConfigResponse | null>(null)
export const staged = signal<ConfigChange[]>([])
export const review = signal<ReviewResponse | null>(null)
export const applyResult = signal<AppliedResponse | null>(null)
export const cfgError = signal<string>('')
export const busy = signal(false)
export const validateMsg = signal<{ type: string; message?: string } | null>(null)

// Applied changes (design "System · Config": "applied changes full width,
// readable at a glance"). restoreId names which history entry `review` is
// currently reviewing, when it came from restoreToReview rather than a staged
// edit — so applyReview knows which endpoint to call.
export const history = signal<HistoryItem[]>([])
export const restoreId = signal<string>('')

export async function loadHistory(): Promise<void> {
  try {
    const r = await api.history()
    history.value = r.history ?? []
  } catch (e) {
    cfgError.value = msg(e)
  }
}

export async function restoreToReview(id: string): Promise<void> {
  try {
    busy.value = true
    const r = await api.restoreReview(id)
    review.value = r
    applyResult.value = null
    restoreId.value = id
    cfgError.value = ''
  } catch (e) {
    cfgError.value = msg(e)
  } finally {
    busy.value = false
  }
}

export function cancelRestore(): void {
  restoreId.value = ''
  review.value = null
}

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
    restoreId.value = ''
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
    restoreId.value = ''
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
  restoreId.value = ''
}

export async function clearStaged(): Promise<void> {
  const r = await api.clearStaged()
  staged.value = r.staged ?? []
  review.value = null
  restoreId.value = ''
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
  // A restore under review (restoreToReview set restoreId) goes through the
  // restore endpoint instead of apply, but the same review/step-up gate.
  if (restoreId.value) {
    return applyRestore(rev, restoreId.value)
  }
  try {
    busy.value = true
    // With sign-in on, an apply needs a step-up (every apply by default, see
    // dashboard.auth.config_stepup); the sheet shows the server's description
    // of what it does, guarded and widening effects first.
    let stepupToken: string | undefined
    if (rev.stepup && authState.value.signed_in) {
      stepupToken = await stepUp('config.apply', rev.token, { what: 'Apply this config change', description: rev.description })
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

async function applyRestore(rev: ReviewResponse, id: string): Promise<void> {
  try {
    busy.value = true
    let stepupToken: string | undefined
    if (rev.stepup && authState.value.signed_in) {
      stepupToken = await stepUp('config.restore', rev.token, { what: 'Restore this backup', description: rev.description })
    }
    const res = await api.restore(id, rev.token, stepupToken)
    applyResult.value = res
    review.value = null
    restoreId.value = ''
    staged.value = []
    cfgError.value = ''
    void loadHistory()
  } catch (e) {
    cfgError.value = msg(e)
  } finally {
    busy.value = false
  }
}

function msg(e: unknown): string {
  return humanError(e)
}
