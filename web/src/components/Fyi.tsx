// For your information (design: true, but not waiting on you — the prototype's
// js/screens/overview.js fyi()). Shown on Home and the standalone Needs you
// page, in the side column. Unlike the prototype (which hides the card when
// there's nothing to say), this phase always shows the card with its own
// small empty state, so the panel's presence is conformance-checkable even on
// a quiet box — see the phase report for the gap this covers for.
import type { JSX } from 'preact'
import type { FyiItem } from '../state'
import { Icon } from './Icon'
import { EmptyState } from './EmptyState'

export function Fyi({ items }: { items: FyiItem[] }): JSX.Element {
  return (
    <section class="dl-card" aria-label="For your information">
      <header class="dl-ch">
        <h2>For your information</h2>
      </header>
      {items.length === 0 ? (
        <EmptyState small title="Nothing to report">
          <p>Safety and update notes that don't need a decision show up here.</p>
        </EmptyState>
      ) : (
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
      )}
    </section>
  )
}
