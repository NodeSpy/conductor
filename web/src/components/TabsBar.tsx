// A tab bar that never cuts a label (design system's UI.fitTabs, ui.js): the
// tabs that don't fit the row go into a trailing "More" menu instead of
// clipping; the active tab always stays visible (it takes the place of the
// last one that would have been dropped). Generic — any screen's .tabs row
// (not just Browsers) can use this.
import type { JSX } from 'preact'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './Icon'

export interface TabItem {
  id: string
  label: string
  href: string
  count?: number | null
  hot?: boolean
}

export function TabsBar({ items, active, ariaLabel }: { items: TabItem[]; active: string; ariaLabel: string }): JSX.Element {
  const ref = useRef<HTMLElement>(null)
  const [over, setOver] = useState<Set<number>>(new Set())
  const [menuOpen, setMenuOpen] = useState(false)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const fit = () => {
      const anchors = Array.from(el.querySelectorAll<HTMLAnchorElement>(':scope > a'))
      anchors.forEach((a) => (a.style.display = ''))
      if (anchors.length < 3 || el.scrollWidth <= el.clientWidth + 1) {
        setOver(new Set())
        return
      }
      const gap = 16
      const room = el.clientWidth - 76 // leaves room for the More button
      const widths = anchors.map((a) => a.getBoundingClientRect().width)
      const activeIdx = items.findIndex((t) => t.id === active)
      let used = 0
      const keep: number[] = []
      widths.forEach((w, i) => {
        if (used + w <= room) {
          keep.push(i)
          used += w + gap
        }
      })
      if (activeIdx >= 0 && !keep.includes(activeIdx)) {
        while (keep.length && keep.reduce((x, i) => x + widths[i] + gap, 0) + widths[activeIdx] > room) keep.pop()
        keep.push(activeIdx)
        keep.sort((a, b) => a - b)
      }
      const overSet = new Set(anchors.map((_, i) => i).filter((i) => !keep.includes(i)))
      anchors.forEach((a, i) => (a.style.display = overSet.has(i) ? 'none' : ''))
      setOver(overSet)
    }
    fit()
    const ro = new ResizeObserver(fit)
    ro.observe(el)
    return () => ro.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length, active])

  const overItems = items.filter((_, i) => over.has(i))

  return (
    <nav class="tabs" role="tablist" aria-label={ariaLabel} ref={ref}>
      {items.map((t) => (
        <a class={active === t.id ? 'on' : ''} role="tab" aria-selected={active === t.id} aria-current={active === t.id ? 'page' : undefined} href={t.href} data-tab={t.id}>
          {t.label}
          {t.count != null ? <span class={'count' + (t.hot ? ' hot' : '')}>{t.count}</span> : null}
        </a>
      ))}
      {overItems.length ? (
        <div class="tab-more-wrap" style="position:relative;margin-left:auto">
          <button
            type="button"
            class={'tab-more' + (overItems.some((t) => t.hot) ? ' hot' : '')}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label={`More tabs: ${overItems.map((t) => t.label).join(', ')}`}
            onClick={() => setMenuOpen((v) => !v)}
          >
            More<Icon name="chevd" />
          </button>
          {menuOpen ? (
            <div class="menu" role="menu" style="position:absolute;right:0;top:100%">
              {overItems.map((t) => (
                <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); location.hash = t.href.replace(/^#/, '') }}>
                  {t.label}
                  {t.count != null ? <span class={'count' + (t.hot ? ' hot' : '')}>{t.count}</span> : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
    </nav>
  )
}
