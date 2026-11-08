// A facts line (design system's UI.factsLine / ACT_FACT): small icon+value
// pairs after a row's title — the shared money fact is "danger"-toned with
// its icon cut from a solid tile (ui.js: ACT_GRADE.money → tone danger). A
// fact with an href is a link of its own (ui.js fxGo): it opens the fact's
// page, never the row's.
import type { JSX } from 'preact'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './Icon'

export interface Fact {
  icon: string
  value: string
  label?: string
  // change | warning | danger (risk grades); custom | held | update (a
  // plugin's or a pack's origin, policy and update facts, UI.pluginFact)
  tone?: string
  pri?: number
  // glyph replaces the icon with the fact's own mark (the plugin facts'
  // built-in mark, ui.js UI.pluginFact.origin).
  glyph?: JSX.Element
  href?: string
  // a fact that does something on this page (an update badge opens the
  // update) rather than going to another one
  onClick?: () => void
  // risky marks a fact that needs attention (UI.factsLine's f.risky → .hi)
  risky?: boolean
}

// one keeps the line to one line (UI.fitFacts, a board card): what doesn't fit
// is hidden from the end and a quiet +N says how many, opening them in place.
export function FactsLine({ facts, one }: { facts: (Fact | null | undefined)[]; one?: boolean }): JSX.Element | null {
  const xs = facts.filter((f): f is Fact => !!f && !!f.value)
  const ref = useRef<HTMLSpanElement>(null)
  const [cut, setCut] = useState(0)
  const [all, setAll] = useState(false)
  const sig = xs.map((f) => f.value).join('|')
  useLayoutEffect(() => {
    if (!one || all) return
    const fit = () => {
      const fx = ref.current
      if (!fx || fx.offsetParent === null) return
      const items = [...fx.children].filter((x) => x.classList.contains('fx-i') && !x.classList.contains('fx-more')) as HTMLElement[]
      items.forEach((x) => (x.hidden = false))
      const shown = items.filter((x) => getComputedStyle(x).display !== 'none')
      if (!shown.length) return setCut(0)
      const top = shown[0].offsetTop
      const wrapped = () => shown.some((x) => !x.hidden && x.offsetTop > top + 2)
      let k = 0
      if (wrapped()) {
        const room = fx.getBoundingClientRect().right - 34
        for (let i = shown.length - 1; i > 0; i--) {
          const last = shown.filter((x) => !x.hidden).pop()
          if (!wrapped() && last && last.getBoundingClientRect().right <= room) break
          shown[i].hidden = true
          k++
        }
      }
      setCut(k)
    }
    fit()
    addEventListener('resize', fit)
    return () => removeEventListener('resize', fit)
  }, [one, all, sig])
  if (!xs.length) return null
  const go = (e: Event, f: Fact) => {
    e.preventDefault()
    e.stopPropagation()
    if (f.onClick) f.onClick()
    else if (f.href) location.hash = f.href.replace(/^#/, '')
  }
  return (
    <span class={'fx' + (one && all ? ' fx-all' : '')} ref={ref}>
      {xs.map((f, i) => {
        const act = !!(f.href || f.onClick)
        return (
        <span
          class={'fx-i' + (act ? ' ln' : '') + (f.risky ? ' hi' : '') + (f.tone ? ' t-' + f.tone : '')}
          data-pri={f.pri || 2}
          title={f.label}
          key={i}
          hidden={one && !all && cut > 0 && i >= xs.length - cut}
          role={f.onClick ? 'button' : f.href ? 'link' : undefined}
          tabIndex={act ? 0 : undefined}
          onClick={act ? (e) => go(e, f) : undefined}
          onKeyDown={act ? (e) => (e.key === 'Enter' || e.key === ' ') && go(e, f) : undefined}
        >
          {f.glyph ? f.glyph : f.tone === 'danger' ? <span class="fx-tile"><Icon name={f.icon} /></span> : <Icon name={f.icon || 'dot'} />}
          {f.value}
        </span>
        )
      })}
      {one && !all && cut > 0 ? (
        <button
          type="button"
          class="fx-i fx-more"
          title={xs.slice(xs.length - cut).map((f) => f.value).join(' · ')}
          aria-label={`${cut} more: ${xs.slice(xs.length - cut).map((f) => f.value).join(' · ')}`}
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            setAll(true)
          }}
        >
          +{cut}
        </button>
      ) : null}
    </span>
  )
}
