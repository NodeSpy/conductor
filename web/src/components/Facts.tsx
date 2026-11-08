// A facts line (design system's UI.factsLine / ACT_FACT): small icon+value
// pairs after a row's title — the shared money fact is "danger"-toned with
// its icon cut from a solid tile (ui.js: ACT_GRADE.money → tone danger). A
// fact with an href is a link of its own (ui.js fxGo): it opens the fact's
// page, never the row's.
import type { JSX } from 'preact'
import { Icon } from './Icon'

export interface Fact {
  icon: string
  value: string
  label?: string
  // change | warning | danger (risk grades); custom | held | update (a
  // plugin's or a pack's origin, policy and update facts, UI.pluginFact)
  tone?: string
  pri?: number
  href?: string
  // a fact that does something on this page (an update badge opens the
  // update) rather than going to another one
  onClick?: () => void
}

export function FactsLine({ facts }: { facts: (Fact | null | undefined)[] }): JSX.Element | null {
  const xs = facts.filter((f): f is Fact => !!f && !!f.value)
  if (!xs.length) return null
  const go = (e: Event, f: Fact) => {
    e.preventDefault()
    e.stopPropagation()
    if (f.onClick) f.onClick()
    else if (f.href) location.hash = f.href.replace(/^#/, '')
  }
  return (
    <span class="fx">
      {xs.map((f, i) => {
        const act = !!(f.href || f.onClick)
        return (
        <span
          class={'fx-i' + (act ? ' ln' : '') + (f.tone ? ' t-' + f.tone : '')}
          data-pri={f.pri || 2}
          title={f.label}
          key={i}
          role={f.onClick ? 'button' : f.href ? 'link' : undefined}
          tabIndex={act ? 0 : undefined}
          onClick={act ? (e) => go(e, f) : undefined}
          onKeyDown={act ? (e) => (e.key === 'Enter' || e.key === ' ') && go(e, f) : undefined}
        >
          {f.tone === 'danger' ? <span class="fx-tile"><Icon name={f.icon} /></span> : <Icon name={f.icon || 'dot'} />}
          {f.value}
        </span>
        )
      })}
    </span>
  )
}
