// How you're told (Preferences): a desktop notification from this browser
// when something new needs you, per kind, while the dashboard is open in a
// tab. A display preference of this browser only (localStorage), like the
// rest of Preferences; nothing in conductor changes.
import type { NeedItem } from '../state'

export const NOTIFY_PREFS = {
  approvals: 'pref-notify-approvals',
  failed: 'pref-notify-failed',
} as const

// the prototype's defaults (D.prefs): told about approvals, not about every failed run
export function notifyPref(key: string): boolean {
  const v = localStorage.getItem(key)
  return v === null ? key === NOTIFY_PREFS.approvals : v === '1'
}

/** What this browser lets us do: 'granted', 'denied', 'default', or 'unsupported'. */
export function notifyPermission(): NotificationPermission | 'unsupported' {
  return typeof Notification === 'undefined' ? 'unsupported' : Notification.permission
}

/** Asks this browser for permission to show notifications (only from a click). */
export async function askNotifyPermission(): Promise<NotificationPermission | 'unsupported'> {
  if (typeof Notification === 'undefined') return 'unsupported'
  if (Notification.permission === 'default') return Notification.requestPermission()
  return Notification.permission
}

let seen: Set<string> | null = null

/** Shows one notification per item that is new since the last call (the first call only remembers). */
export function notifyNew(items: NeedItem[]): void {
  const hrefs = new Set(items.map((i) => i.href))
  const first = seen === null
  const before = seen || new Set<string>()
  seen = hrefs
  if (first || notifyPermission() !== 'granted') return
  for (const it of items) {
    if (before.has(it.href)) continue
    const want = it.kind === 'approval' || it.kind === 'purchase' ? notifyPref(NOTIFY_PREFS.approvals) : it.kind === 'failed' ? notifyPref(NOTIFY_PREFS.failed) : false
    if (!want) continue
    const n = new Notification(it.title, { body: it.why || '', tag: it.href })
    n.onclick = () => {
      window.focus()
      location.hash = it.href.replace(/^#/, '')
    }
  }
}
