// For your information (design: true, but not waiting on you — the prototype's
// js/screens/overview.js fyi()). Shown on Home and the standalone Needs you
// page, in the side column. Matches the prototype exactly: the card doesn't
// render at all when there's nothing to say (no empty state of its own).
import type { JSX } from 'preact'
import type { FyiItem } from '../state'
import { Icon } from './Icon'

export function Fyi({ items }: { items: FyiItem[] }): JSX.Element | null {
  if (items.length === 0) return null
  return (
    <section class="dl-card" aria-label="For your information">
      <header class="dl-ch">
        <h2>For your information</h2>
      </header>
      <ul class="dl-list">
        {items.map((a) => (
          <li key={a.href + a.title}>
            <a class="dl-row-a" href={a.href}>
              <span class="dl-ng dl-fyi-ic">
                <Icon name={a.icon} />
              </span>
              <span class="dl-nt">
                <b>{a.title}</b>
                {a.why ? <span class="dl-why">{a.why}</span> : null}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  )
}
