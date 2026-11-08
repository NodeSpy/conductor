// Small pieces Chat and Tickets share, ported from the prototype's
// js/identity.js and js/ui.js: a bot's face (UI.botAv, `.bav`), a team's
// picture (UI.teamAv, `.bav.tav`), a bot's name with its id (UI.botWho), the
// clock and "ago" words (UI.clock, UI.agoText), the count badge (UI.count) and
// the toast (UI.toast). Kept apart from Avatar.tsx so the chat and ticket
// screens carry the prototype's own classes without changing other screens.
import type { JSX } from 'preact'
import { signal } from '@preact/signals'
import { useEffect } from 'preact/hooks'
import type { Identity } from './Avatar'
import { avatarVNode } from '../lib/avatar'
import { StatusMark } from './StatusMark'
import { Icon } from './Icon'

// The identity colours the dashboard stores, as the prototype's bot palette
// (screens.css [data-bc]): the bubble's rule and the face tint come from it.
const BC: Record<string, string> = {
  teal: 'teal', ocean: 'lagoon', indigo: 'lagoon', violet: 'plum', plum: 'plum', magenta: 'rose',
  sand: 'olive', slate: 'stone', graphite: 'stone',
}
export const botColor = (id?: Identity): string => BC[id?.color || id?.avatar?.color || ''] || 'stone'

const PX: Record<string, number> = { xs: 20, sm: 24, md: 32, msg: 36, list: 40, lg: 44, xl: 64 }

export function BotAv({ name, id, size = 'sm' }: { name: string; id?: Identity; size?: string }): JSX.Element {
  const bc = botColor(id)
  if (id?.picture) {
    return (
      <span class={`bav img ${size}`} data-bc={bc} aria-hidden="true">
        <img src={'/api' + id.picture} alt="" />
      </span>
    )
  }
  const spec = id?.avatar
  if (spec && (spec.shape || spec.icon)) {
    return (
      <span class={`bav mascot ${size}`} data-bc={bc} aria-hidden="true">
        {avatarVNode(spec.color ? spec : { ...spec, color: id?.color }, PX[size] || 24)}
      </span>
    )
  }
  const ini = name.split(/[-_]/).map((w) => w[0]).join('').slice(0, 2)
  return (
    <span class={`bav ${size}`} data-bc={bc} aria-hidden="true">
      <i>{ini}</i>
    </span>
  )
}

export function TeamAv({ name, id, size = 'sm' }: { name: string; id?: Identity; size?: string }): JSX.Element {
  if (id?.picture) {
    return (
      <span class={`bav tav img ${size}`} aria-hidden="true">
        <img src={'/api' + id.picture} alt="" />
      </span>
    )
  }
  if (id?.avatar && (id.avatar.icon || id.avatar.shape)) {
    return (
      <span class={`bav tav ${size}`} data-bc={botColor(id)} aria-hidden="true">
        {avatarVNode(id.avatar.color ? id.avatar : { ...id.avatar, color: id.color }, PX[size] || 24)}
      </span>
    )
  }
  return (
    <span class={`bav tav ini ${size}`} aria-hidden="true">
      <i>{name.replace(/[^a-z0-9]/gi, '').slice(0, 2).toLowerCase()}</i>
    </span>
  )
}

export const nameOf = (id: Identity | undefined, fallback: string): string => id?.display_name || fallback

// BotWho: the bot's name, and its id beside it when the name differs.
export function BotWho({ name, id }: { name: string; id?: Identity }): JSX.Element {
  const n = nameOf(id, name)
  return (
    <>
      <b class="bav-name">{n}</b>
      {n !== name ? <> <span class="bav-id">{name}</span></> : null}
    </>
  )
}

export const clock = (iso: string): string => new Date(iso).toTimeString().slice(0, 5)

export function agoText(iso?: string): string {
  if (!iso) return ''
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (Number.isNaN(s)) return ''
  if (s < 60) return 'just now'
  if (s < 3600) return `${(s / 60) | 0} min ago`
  if (s < 86400) return `${(s / 3600) | 0} h ago`
  const d = (s / 86400) | 0
  return d === 1 ? 'yesterday' : `${d} days ago`
}

export function Count({ n, hot, label, cls }: { n: number; hot?: boolean; label?: string; cls?: string }): JSX.Element {
  return (
    <span class={'count' + (hot ? ' hot' : '') + (cls ? ' ' + cls : '')} aria-label={label}>
      {n}
    </span>
  )
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${Math.round(n / 1024)} KB`
  return `${(n / 1048576).toFixed(n < 10485760 ? 1 : 0)} MB`
}

// The toast (UI.toast): one at a time, gone after a few seconds; a failure
// stays until dismissed.
interface ToastMsg { title: string; sub?: string; s: 'ok' | 'fail' | 'wait' | 'idle' | 'run' | 'ember'; at: number }
export const chatToast = signal<ToastMsg | null>(null)
export function toast(title: string, sub = '', s: ToastMsg['s'] = 'ok'): void {
  chatToast.value = { title, sub, s, at: Date.now() }
}
const MARK: Record<string, string> = { ok: 'success', fail: 'fail', wait: 'wait', idle: 'idle', run: 'run', ember: 'ember' }
export function ToastHost(): JSX.Element | null {
  const t = chatToast.value
  useEffect(() => {
    if (!t || t.s === 'fail') return
    const h = setTimeout(() => {
      if (chatToast.value === t) chatToast.value = null
    }, 5000)
    return () => clearTimeout(h)
  }, [t])
  if (!t) return null
  return (
    <div class="toasts" aria-live="polite">
      <div class="toast" data-s={t.s === 'ok' ? 'success' : t.s} role={t.s === 'fail' ? 'alert' : 'status'} data-testid="toast">
        <StatusMark s={MARK[t.s] || 'idle'} />
        <div>
          <b>{t.title}</b>
          {t.sub ? <p>{t.sub}</p> : null}
        </div>
        {t.s === 'fail' ? (
          <button class="icon-btn toast-x" type="button" aria-label="Dismiss" onClick={() => (chatToast.value = null)}>
            <Icon name="x" />
          </button>
        ) : (
          <span />
        )}
      </div>
    </div>
  )
}
