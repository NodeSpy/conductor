// A facts line (design system's UI.factsLine / ACT_FACT): small icon+value
// pairs after a row's title — the shared money fact is "danger"-toned with
// its icon cut from a solid tile (ui.js: ACT_GRADE.money → tone danger).
import type { JSX } from 'preact'
import { Icon } from './Icon'

export interface Fact {
  icon: string
  value: string
  label?: string
  tone?: 'change' | 'warning' | 'danger'
  pri?: number
}

export function FactsLine({ facts }: { facts: (Fact | null | undefined)[] }): JSX.Element | null {
  const xs = facts.filter((f): f is Fact => !!f && !!f.value)
  if (!xs.length) return null
  return (
    <span class="fx">
      {xs.map((f, i) => (
        <span class={'fx-i' + (f.tone ? ' t-' + f.tone : '')} data-pri={f.pri || 2} title={f.label} key={i}>
          {f.tone === 'danger' ? <span class="fx-tile"><Icon name={f.icon} /></span> : <Icon name={f.icon || 'dot'} />}
          {f.value}
        </span>
      ))}
    </span>
  )
}
