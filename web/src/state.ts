// App state as signals: the Overview reads these, and the loader + SSE client
// write them. Signals update only the plates that change (design §8).
import { signal } from '@preact/signals'
import { api } from './api/client'
import { openEvents } from './api/sse'
import type { StatusResponse, RunSummary, AttrQualifier } from './api/types'
import { humanError } from './api/errors'
import { botsApi, type ProposalRow } from './api/bots'
import { browser, type Purchase } from './api/browser'

export const status = signal<StatusResponse | null>(null)
export const runs = signal<RunSummary[]>([])
export const loadError = signal<string>('')
export const conn = signal<'connecting' | 'live' | 'reconnecting'>('connecting')
// liveTick bumps on every received SSE event, so the UI can flash a "live" pulse.
export const liveTick = signal(0)
// connAttrs are the connector-published search qualifiers (github.repo, …) the
// filter bar offers as pills. Loaded once; refreshed when the run set changes.
export const connAttrs = signal<AttrQualifier[]>([])

// Needs you's other real sources, besides failed runs: pending bot approvals
// and pending purchases (design §4.4, §4.5.1). Loaded alongside runs; a
// failure leaves the list without that kind rather than blocking the page.
export const proposalsPending = signal<ProposalRow[]>([])
export const purchasesPending = signal<Purchase[]>([])

// Needs-you / attention aggregator (design §4.1). Runs that need a person —
// failed runs (the backend's attention topic feeds these) — minus the ones the
// operator dismissed. Dismissals are local and reversible (an Undo in the UI).
const DISMISS_KEY = 'needs-dismissed'
function loadDismissed(): string[] {
  try {
    return JSON.parse(localStorage.getItem(DISMISS_KEY) || '[]') as string[]
  } catch {
    return []
  }
}
export const dismissed = signal<string[]>(loadDismissed())

export function setDismissed(next: string[]): void {
  dismissed.value = next
  try {
    localStorage.setItem(DISMISS_KEY, JSON.stringify(next))
  } catch {
    /* storage unavailable (private mode) — keep it in memory only */
  }
}

export function dismiss(id: string): void {
  if (!dismissed.value.includes(id)) setDismissed([...dismissed.value, id])
}
export function undismiss(id: string): void {
  setDismissed(dismissed.value.filter((d) => d !== id))
}

// needsYou returns the runs needing a person right now, newest first.
export function needsYou(all: RunSummary[] = runs.value): RunSummary[] {
  const skip = new Set(dismissed.value)
  return all.filter((r) => r.status === 'failed' && !skip.has(r.id))
}

// Mark state keys (data-s) and words, from the run record's status. These are
// the brand's status vocabulary (css/marks.css): ok→passed, fail→failed,
// run→running, idle→neutral.
export function statusMark(s: string): { s: string; word: string } {
  switch (s) {
    case 'ok':
      return { s: 'ok', word: 'Passed' }
    case 'failed':
      return { s: 'fail', word: 'Failed' }
    case 'running':
      return { s: 'run', word: 'Running' }
    case 'retried':
      return { s: 'idle', word: 'Retried' }
    default:
      return { s: 'idle', word: s || 'Unknown' }
  }
}

export async function load(): Promise<void> {
  try {
    const [st, r] = await Promise.all([api.status(), api.runs({ limit: 200 })])
    status.value = st
    runs.value = r.runs
    loadError.value = ''
  } catch (e) {
    loadError.value = humanError(e)
  }
  // Connector qualifiers are cheap and change rarely; a failure just leaves the
  // filter bar without pills, never blocks the page.
  try {
    connAttrs.value = (await api.connectorAttrs()).attrs || []
  } catch {
    /* no qualifiers available */
  }
  // Pending approvals and purchases feed Needs you alongside failed runs; a
  // daemon with no bots configured (or wallet unset) just has none of either.
  try {
    proposalsPending.value = (await botsApi.proposals()).proposals.filter((p) => p.status === 'pending')
  } catch {
    proposalsPending.value = []
  }
  try {
    purchasesPending.value = (await browser.purchases('pending')).purchases || []
  } catch {
    purchasesPending.value = []
  }
}

// --- Needs you (design §4.1): one aggregator, one vocabulary, every kind the
// daemon can tell us about today. A kind the daemon doesn't expose yet (a
// connector failure, a sign-in waiting for approval, an update that needs a
// person) is left out here and noted in the phase report, not faked.
export interface NeedItem {
  kind: 'paused' | 'approval' | 'purchase' | 'failed'
  mark: string // the status vocabulary's state key (css/marks.css)
  verb: string // the one thing you'll do next, same word for every item of a kind
  title: string
  why?: string
  href: string
  at: string // RFC3339, for the age column
}

function proposalItem(p: ProposalRow): NeedItem {
  return {
    kind: 'approval',
    mark: 'ember',
    verb: 'Review',
    title: p.summary || p.action || `${p.bot} · ${p.kind}`,
    why: p.requester ? `Asked by ${p.requester}` : undefined,
    href: `#/bots/${p.bot}/proposals/${p.proposal}`,
    at: p.created || '',
  }
}

function purchaseItem(p: Purchase): NeedItem {
  return {
    kind: 'purchase',
    mark: 'ember',
    verb: 'Review',
    title: `${p.merchant} · ${p.currency} ${p.total.toFixed(2)}`,
    why: p.why,
    href: `#/browser/purchases/${p.id}`,
    at: p.created,
  }
}

function failedItem(r: RunSummary): NeedItem {
  return {
    kind: 'failed',
    mark: 'fail',
    verb: 'See why',
    title: r.repo ? `${r.workflow} · ${r.repo}${r.number ? `#${r.number}` : ''}` : r.workflow,
    why: r.error || (r.failed_step ? `failed at ${r.failed_step}` : undefined),
    href: `#/runs/${r.id}`,
    at: r.finished || r.started,
  }
}

// needItems returns everything that needs a person right now, newest first:
// the daemon paused, pending approvals, pending purchases and failed runs the
// operator hasn't dismissed. One list feeds Home's tile, Home's Needs you
// card, the rail's badge and the Needs you page (design: one source of truth).
export function needItems(): NeedItem[] {
  const items: NeedItem[] = []
  const st = status.value
  if (st?.paused) {
    items.push({ kind: 'paused', mark: 'ember', verb: 'Resume', title: 'All automation is paused', why: 'Nothing new starts until you resume it.', href: '#/system', at: st.started_at })
  }
  items.push(...proposalsPending.value.map(proposalItem))
  items.push(...purchasesPending.value.map(purchaseItem))
  items.push(...needsYou().map(failedItem))
  return items.sort((a, b) => new Date(b.at || 0).getTime() - new Date(a.at || 0).getTime())
}

// needBreakdown reads like the prototype's: "2 approvals · 1 failed run".
export function needBreakdown(items: NeedItem[]): string {
  const counts: Record<string, number> = {}
  for (const it of items) counts[it.kind] = (counts[it.kind] || 0) + 1
  const nouns: Record<string, [string, string]> = {
    approval: ['approval', 'approvals'],
    purchase: ['purchase', 'purchases'],
    failed: ['failed run', 'failed runs'],
    paused: ['pause', 'pauses'],
  }
  return Object.entries(counts)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${n === 1 ? nouns[k][0] : nouns[k][1]}`)
    .join(' · ')
}

// startLive opens the SSE stream and keeps the snapshots fresh. On each run
// event it debounce-refetches (the record is the source of truth, so a refetch
// is always correct); a reset refetches immediately.
export function startLive(): () => void {
  let timer: number | undefined
  const refresh = () => {
    liveTick.value++
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => void load(), 250) as unknown as number
  }
  return openEvents(['run', 'attention'], {
    onOpen: () => (conn.value = 'live'),
    onError: () => (conn.value = 'reconnecting'),
    onRun: refresh,
    onAttention: refresh,
    onReset: () => void load(),
  })
}
