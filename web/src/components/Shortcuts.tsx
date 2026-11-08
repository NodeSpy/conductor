// Single-key shortcuts and their sheet (prototype js/palette.js, ported line
// for line). Keys anywhere outside a text field and with no dialog open:
// ? the shortcut sheet · / focus the page's filter (a page with none opens the
// ⌘K palette) · g then h/o/l/a/r/w/b/c/s to jump · j/k move through a list ·
// Enter opens the selected row · x ticks its checkbox. ⌘K itself is the
// palette's own (Palette.tsx).
import type { JSX } from 'preact'
import { useEffect, useRef } from 'preact/hooks'
import { go } from '../router'
import { Icon } from './Icon'
import { paletteOpen, shortcutsOpen } from '../lib/overlays'

const KEYS: [string, string][] = [
  ['⌘K / Ctrl K', 'command line: find anything, run actions'],
  ['?', 'this sheet'],
  ['/', "focus the page's search or filter"],
  ['g h', 'Home'],
  ['g a', 'Needs you'],
  ['g r', 'Activity (every run)'],
  ['g w', 'Workflows'],
  ['g b', 'Bots'],
  ['g c', 'Config'],
  ['g s', 'Secrets'],
  ['j / k', 'next / previous row in a list'],
  ['Enter', 'open the selected row'],
  ['x', 'tick the selected row (bulk select)'],
  ['Esc', 'close a dialog, drawer or menu'],
]

// GO: the prototype's g-key targets at this app's routes (Home is #/, its
// live view is Activity, Config is System's Config tab).
const GO: Record<string, string> = { h: '', o: '', l: 'activity', a: 'attention', r: 'activity', w: 'workflows', b: 'bots', c: 'system/config', s: 'secrets' }

const rows = (): HTMLElement[] =>
  [...document.querySelectorAll<HTMLElement>('#main tr[data-href], #main tr.click, #main a.item, #main .item.click, #main .dl-list > li > a, #main .ftree .frow a, #main .kind-entries .entry, #main .m-row[data-wf]')].filter((r) => r.offsetParent)

const mark = (rs: HTMLElement[], i: number) => {
  rs.forEach((r) => r.classList.remove('kbd-sel'))
  const r = rs[i]
  if (!r) return
  r.classList.add('kbd-sel')
  r.scrollIntoView({ block: 'nearest' })
}

export function Shortcuts(): JSX.Element | null {
  useEffect(() => {
    let gAt = 0
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as Element | null
      if ((t && t.closest && t.closest('input, textarea, select, [contenteditable="true"]')) || document.querySelector('.scrim, .drawer-scrim, .palette')) return
      if (e.key === '?') {
        e.preventDefault()
        shortcutsOpen.value = true
        return
      }
      if (e.key === '/') {
        // a page with no filter of its own: / opens the search
        const f = document.querySelector<HTMLElement>('#main .fb-in, #main .dl-filters input, #main .filters input, #main input[type="search"], #main .input-icon input, #main [data-on-change="rp-q"]')
        e.preventDefault()
        if (f) f.focus()
        else paletteOpen.value = true
        return
      }
      if (e.key === 'g') {
        gAt = Date.now()
        return
      }
      if (gAt && Date.now() - gAt < 1200 && e.key in GO) {
        e.preventDefault()
        gAt = 0
        go(GO[e.key])
        return
      }
      gAt = 0
      if (e.key === 'j' || e.key === 'k') {
        const rs = rows()
        if (!rs.length) return
        const cur = rs.findIndex((r) => r.classList.contains('kbd-sel'))
        const kSel = cur < 0 ? (e.key === 'j' ? 0 : rs.length - 1) : Math.max(0, Math.min(rs.length - 1, cur + (e.key === 'j' ? 1 : -1)))
        mark(rs, kSel)
        e.preventDefault()
        return
      }
      if (e.key === 'Enter') {
        const r = document.querySelector<HTMLElement>('#main .kbd-sel')
        if (r) {
          e.preventDefault()
          const a = r.matches('a') ? r : r.querySelector<HTMLElement>('a[href]')
          if (r.dataset.href) location.hash = r.dataset.href
          else if (a) a.click()
          else r.click()
        }
        return
      }
      if (e.key === 'x') {
        const r = document.querySelector('#main .kbd-sel')
        const cb = r && r.querySelector<HTMLInputElement>('input[type="checkbox"]')
        if (cb) {
          e.preventDefault()
          cb.click()
        }
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  return shortcutsOpen.value ? <ShortcutSheet /> : null
}

// ShortcutSheet is UI.shortcuts: a UI.modal (.scrim > .modal) headed "help" /
// "Keyboard shortcuts", the keys as a dl.gloss.keys. Esc, the close button or
// a click on the scrim closes it and hands focus back.
function ShortcutSheet(): JSX.Element {
  const opener = useRef<Element | null>(document.activeElement)
  const modal = useRef<HTMLDivElement>(null)
  const close = () => {
    shortcutsOpen.value = false
    const o = opener.current as HTMLElement | null
    if (o && o.isConnected && typeof o.focus === 'function') o.focus()
  }
  useEffect(() => {
    modal.current?.focus()
    const onEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        close()
      }
    }
    document.addEventListener('keydown', onEsc, true)
    return () => document.removeEventListener('keydown', onEsc, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return (
    <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && close()}>
      <div class="modal" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" tabIndex={-1} ref={modal}>
        <span class="sheet-grip only-phone" aria-hidden="true" />
        <div class="modal-head">
          <div>
            <div class="caps">help</div>
            <h2>Keyboard shortcuts</h2>
          </div>
          <button class="icon-btn" type="button" data-close aria-label="Close" onClick={close}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body">
          <dl class="gloss keys">
            {KEYS.map(([k, d]) => (
              <div>
                <dt><span class="kbd">{k}</span></dt>
                <dd>{d}</dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </div>
  )
}
