// The shared filter model for the Activity and Workflows lists. The filter bar's
// MENUS are conductor's own concepts (status, workflow); what a connector's
// events carry (github.repo) is a SEARCH QUALIFIER, held in `attrs` and shown as
// a removable pill (design §4.2).
import type { RunSummary, AttrQualifier } from '../api/types'

export interface FilterState {
  q: string
  status: string[]
  workflow: string[]
  since: string // a key from TIME_RANGES, or 'all'
  attrs: Record<string, string[]> // qualifier key (github.repo) -> values
}

export function emptyFilter(since = '12h'): FilterState {
  return { q: '', status: [], workflow: [], since, attrs: {} }
}

// TIME_RANGES: [key, label, minutes]. 0 minutes means "all time".
export const TIME_RANGES: [string, string, number][] = [
  ['1h', 'Last hour', 60],
  ['12h', 'Last 12 hours', 720],
  ['24h', 'Last 24 hours', 1440],
  ['7d', 'Last 7 days', 10080],
  ['30d', 'Last 30 days', 43200],
  ['all', 'All time', 0],
]

export function rangeMinutes(since: string): number {
  const r = TIME_RANGES.find(([k]) => k === since)
  return r ? r[2] : 0
}

// STATUSES maps the filter's status keys to a predicate over a run. "failed"
// also covers a stopped run; the backend surfaces failed/ok/running/retried.
export const STATUSES: [string, string, (r: RunSummary) => boolean][] = [
  ['failed', 'Failed', (r) => r.status === 'failed'],
  ['running', 'Running', (r) => r.status === 'running'],
  ['passed', 'Passed', (r) => r.status === 'ok'],
  ['retried', 'Retried', (r) => r.status === 'retried'],
]

// attrValue reads a connector qualifier off a run (repo/number live on the
// summary). Unknown fields read empty.
export function attrValue(r: RunSummary, attr: AttrQualifier): string {
  if (attr.field === 'repo') return r.repo || ''
  if (attr.field === 'number') return r.number ? String(r.number) : ''
  return ''
}

// matchRun applies the whole filter to one run (client-side; the connector
// qualifiers are also enforced server-side on the /runs fetch, but applying
// them here keeps the list correct against the live SSE-refreshed set too).
export function matchRun(r: RunSummary, f: FilterState, attrs: AttrQualifier[]): boolean {
  if (f.status.length) {
    const ok = STATUSES.some(([k, , pred]) => f.status.includes(k) && pred(r))
    if (!ok) return false
  }
  if (f.workflow.length && !f.workflow.includes(r.workflow)) return false
  for (const [key, vals] of Object.entries(f.attrs)) {
    if (!vals.length) continue
    const attr = attrs.find((a) => a.key === key)
    if (!attr) continue
    if (!vals.includes(attrValue(r, attr))) return false
  }
  const mins = rangeMinutes(f.since)
  const live = r.status === 'running'
  if (mins && !live && Date.now() - new Date(r.started).getTime() > mins * 60000) return false
  if (f.q) {
    const hay = `${r.id} ${r.workflow} ${r.repo || ''} ${r.number || ''} ${r.error || ''}`.toLowerCase()
    if (!hay.includes(f.q.toLowerCase())) return false
  }
  return true
}

// hasAnyFilter reports whether the filter is doing anything (for a "Clear" link).
export function hasAnyFilter(f: FilterState): boolean {
  return !!(
    f.q ||
    f.status.length ||
    f.workflow.length ||
    f.since !== 'all' ||
    Object.values(f.attrs).some((v) => v.length)
  )
}
