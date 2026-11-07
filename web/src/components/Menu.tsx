// A popover menu, ported from the prototype's UI.menu (js/ui.js): a small
// panel anchored to its trigger button, listing actions with an icon each. On
// a phone it becomes a bottom sheet (css/interact.css .menu.as-sheet) rather
// than a tiny floating panel. Closes on an outside click, Escape, or picking
// an item; Escape and a click both return focus to the trigger.
//
// items: '-' is a separator; otherwise { label, icon, onClick, href, disabled }.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './Icon'

export type MenuItem =
  | '-'
  | {
      label: string
      icon: string
      onClick?: () => void
      disabled?: string | boolean
    }

export function Menu({
  anchor,
  items,
  onClose,
  label,
}: {
  anchor: HTMLElement | null
  items: MenuItem[]
  onClose: () => void
  label?: string
}): JSX.Element | null {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top?: number; left?: number } | null>(null)
  const phone = typeof matchMedia !== 'undefined' && matchMedia('(max-width: 720px)').matches

  useEffect(() => {
    if (!anchor || phone) return
    const b = anchor.getBoundingClientRect()
    const m = ref.current
    const w = m?.offsetWidth ?? 200
    const h = m?.offsetHeight ?? 120
    setPos({
      top: Math.min(window.innerHeight - h - 8, b.bottom + 4),
      left: Math.max(8, Math.min(window.innerWidth - w - 8, b.right - w)),
    })
  }, [anchor, phone])

  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      const t = e.target as Node
      if (ref.current && !ref.current.contains(t) && t !== anchor) onClose()
    }
    function onKey(e: KeyboardEvent) {
      if (!ref.current) return
      const focusables = Array.from(ref.current.querySelectorAll<HTMLElement>('button:not([disabled]), a[href]'))
      const i = focusables.indexOf(document.activeElement as HTMLElement)
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        anchor?.focus()
      } else if (e.key === 'ArrowDown') {
        e.preventDefault()
        focusables[(i + 1) % focusables.length]?.focus()
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        focusables[(i - 1 + focusables.length) % focusables.length]?.focus()
      }
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    const t = setTimeout(() => {
      const first = ref.current?.querySelector<HTMLElement>('button:not([disabled]), a[href]')
      first?.focus()
    }, 0)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
      clearTimeout(t)
    }
  }, [anchor, onClose])

  if (!anchor) return null
  return (
    <div
      ref={ref}
      id="pop-menu"
      class={'menu' + (phone ? ' as-sheet' : '')}
      role="menu"
      aria-label={label}
      style={!phone && pos ? `top:${pos.top}px;left:${pos.left}px` : undefined}
    >
      {items.map((it, i) =>
        it === '-' ? (
          <hr key={i} />
        ) : (
          <button
            key={i}
            type="button"
            role="menuitem"
            disabled={!!it.disabled}
            title={typeof it.disabled === 'string' ? it.disabled : undefined}
            onClick={() => {
              onClose()
              it.onClick?.()
            }}
          >
            <Icon name={it.icon} />
            {it.label}
          </button>
        ),
      )}
    </div>
  )
}
