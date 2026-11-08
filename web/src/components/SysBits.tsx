// Small shared pieces for the System tabs (Config, Safety, Updates, Logs &
// audit, Access): the prototype's UI.confirm as a promise-returning hook (the
// app's own ConfirmModal, never the browser's confirm()), and the prototype's
// UI.agoText / UI.dateTime / UI.ago time words.
import type { ComponentChildren, JSX } from 'preact'
import { useState } from 'preact/hooks'
import { ConfirmModal } from './FormModal'
import '../styles/system-repass.css'

export interface ConfirmOpts {
  title: ComponentChildren
  body: ComponentChildren
  ok?: string
  cancel?: string
  danger?: boolean
}

// useConfirm is UI.confirm: ask(opts) opens the dialog and resolves true on
// the action, false on Cancel/close. Render `node` somewhere in the page.
export function useConfirm(): [JSX.Element | null, (o: ConfirmOpts) => Promise<boolean>] {
  const [st, setSt] = useState<{ o: ConfirmOpts; done: (v: boolean) => void } | null>(null)
  const ask = (o: ConfirmOpts) => new Promise<boolean>((done) => setSt({ o, done }))
  const node = st ? (
    <ConfirmModal
      title={st.o.title}
      body={st.o.body}
      ok={st.o.ok}
      cancel={st.o.cancel}
      danger={st.o.danger}
      onOk={() => {
        st.done(true)
        st.done = () => {}
      }}
      onClose={() => {
        st.done(false)
        setSt(null)
      }}
    />
  ) : null
  return [node, ask]
}

/** UI.agoText: "just now", "50 min ago", "4 h ago", "yesterday", "3 days ago". */
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

/** UI.dateTime: "Nov 7, 23:07". */
export function dateTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.toLocaleString('en', { month: 'short' })} ${d.getDate()}, ${d.toTimeString().slice(0, 5)}`
}

/** UI.ago, compact: "12s", "5m", "4h", "2d" (callers add " ago"). */
export function agoShort(iso?: string): string {
  if (!iso) return ''
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (Number.isNaN(s)) return ''
  if (s < 60) return Math.max(1, s | 0) + 's'
  if (s < 3600) return ((s / 60) | 0) + 'm'
  if (s < 86400) return ((s / 3600) | 0) + 'h'
  return ((s / 86400) | 0) + 'd'
}
