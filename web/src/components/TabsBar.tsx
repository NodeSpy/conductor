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
  count?: number | string | null
  hot?: boolean
}

export function TabsBar({
  items,
  active,
  ariaLabel,
  className,
}: {
  items: TabItem[]
  active: string
  ariaLabel: string
  className?: string
}): JSX.Element {
  const ref = useRef<HTMLElement>(null)
  const ghost = useRef<HTMLSpanElement>(null)
  const [over, setOver] = useState<Set<number>>(new Set())
  const [menuOpen, setMenuOpen] = useState(false)
  // What the row's width depends on: every label and count (counts arrive
  // after the first paint, and widen their tabs), and which tab is active.
  const sig = items.map((t) => `${t.id}:${t.label}:${t.count ?? ''}`).join('|') + '#' + active

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    let raf = 0
    const fit = () => {
      const anchors = Array.from(el.querySelectorAll<HTMLAnchorElement>(':scope > a'))
      // Measure every tab at its natural width: show them all first.
      anchors.forEach((a) => a.classList.remove('tab-over'))
      const cs = getComputedStyle(el)
      const gap = parseFloat(cs.columnGap) || 0
      const inner = el.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0)
      const widths = anchors.map((a) => a.getBoundingClientRect().width)
      const total = widths.reduce((x, w) => x + w, 0) + gap * Math.max(0, widths.length - 1)
      let overSet = new Set<number>()
      if (anchors.length > 1 && total > inner + 0.5) {
        // Room for the tabs once "More ▾" (its real width, measured on an
        // always-rendered ghost) and its gap are reserved.
        const room = inner - (ghost.current?.getBoundingClientRect().width ?? 64) - gap
        const activeIdx = items.findIndex((t) => t.id === active)
        const sumOf = (ks: number[]) => ks.reduce((x, k) => x + widths[k], 0) + gap * Math.max(0, ks.length - 1)
        const keep: number[] = []
        for (let k = 0; k < widths.length; k++) {
          // a tab that doesn't fit moves to More; a shorter one after it may still fit
          if (sumOf([...keep, k]) <= room + 1) keep.push(k)
        }
        if (activeIdx >= 0 && !keep.includes(activeIdx)) {
          while (keep.length && sumOf([...keep, activeIdx]) > room + 1) keep.pop()
          keep.push(activeIdx)
          keep.sort((a, b) => a - b)
        }
        overSet = new Set(anchors.map((_, k) => k).filter((k) => !keep.includes(k)))
      }
      // Every tab hidden here is listed under More (the same set drives both).
      anchors.forEach((a, k) => a.classList.toggle('tab-over', overSet.has(k)))
      setOver((prev) => (prev.size === overSet.size && [...prev].every((k) => overSet.has(k)) ? prev : overSet))
    }
    const later = () => {
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(fit)
    }
    fit()
    // Web fonts change every width: measure again once they're in.
    document.fonts?.ready.then(later).catch(() => {})
    const ro = new ResizeObserver(later)
    ro.observe(el)
    window.addEventListener('resize', later)
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      window.removeEventListener('resize', later)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig])

  const overItems = items.filter((_, i) => over.has(i))

  return (
    <nav class={className ?? 'tabs'} role="tablist" aria-label={ariaLabel} ref={ref}>
      {items.map((t) => (
        <a class={[active === t.id ? 'on' : '', over.has(items.indexOf(t)) ? 'tab-over' : ''].filter(Boolean).join(' ')} role="tab" aria-selected={active === t.id} aria-current={active === t.id ? 'page' : undefined} href={t.href} data-tab={t.id}>
          {t.label}
          {t.count != null ? <>{' '}<span class={'count' + (t.hot ? ' hot' : '')}>{t.count}</span></> : null}
        </a>
      ))}
      {/* the ghost is "More ▾" at its real size, never shown: its width is
          what the row keeps free whenever a tab has to move into More */}
      <span ref={ghost} class="tab-more" aria-hidden="true" style="position:absolute;visibility:hidden;pointer-events:none;left:-9999px">
        More<Icon name="chevd" />
      </span>
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
                  {t.count != null ? <>{' '}<span class={'count' + (t.hot ? ' hot' : '')}>{t.count}</span></> : null}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
    </nav>
  )
}
