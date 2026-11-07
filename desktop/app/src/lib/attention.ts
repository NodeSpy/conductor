// What to do with one attention item: a proposal or purchase opens the
// in-app approval; a raised item (no source) can be dismissed; anything else
// with a link opens in the system browser (e.g. a failed run's page on the
// daemon's own dashboard). Mirrors the prototype's VERB/MARK tables
// (js/screens/overview.js) at the level this app's data actually carries.
import type { AttentionItem, Severity } from '../api/types'

export type NeedAction =
  | { kind: 'proposal'; bot: string; id: string }
  | { kind: 'purchase'; id: string }
  | { kind: 'dismiss' }
  | { kind: 'browser'; url: string }
  | { kind: 'none' }

const PROPOSAL_RE = /bots\/([^/]+)\/proposals\/([^/]+)/
const PURCHASE_RE = /browser\/purchases\/([^/]+)/

export function actionFor(item: AttentionItem): NeedAction {
  const link = item.link || ''
  const m1 = link.match(PROPOSAL_RE)
  if (m1) return { kind: 'proposal', bot: m1[1], id: m1[2] }
  const m2 = link.match(PURCHASE_RE)
  if (m2) return { kind: 'purchase', id: m2[1] }
  if (!item.source) return { kind: 'dismiss' }
  if (link) return { kind: 'browser', url: link }
  return { kind: 'none' }
}

export function verbFor(item: AttentionItem): string {
  const a = actionFor(item)
  switch (a.kind) {
    case 'proposal':
    case 'purchase':
      return 'Review'
    case 'dismiss':
      return 'Dismiss'
    case 'browser':
      return 'Open in browser'
    default:
      return ''
  }
}

// Every item here waits on a person; only a run failure gets its own mark.
export function markFor(item: AttentionItem): string {
  if (item.kind === 'run_failed') return 'fail'
  const bySeverity: Record<Severity, string> = { urgent: 'ember', warn: 'ember', info: 'idle' }
  return bySeverity[item.severity] || 'ember'
}
