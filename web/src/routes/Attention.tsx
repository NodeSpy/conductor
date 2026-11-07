// Needs you (design §4.1): the aggregator's own page — one tile per kind, the
// aggregator's full list. Source: overview() in the prototype's js/screens/runs.js
// (UI.route('attention')). Left out for now: the search/filter bar (UI.filterBar)
// and "For your information" (no backend source yet) — noted in the phase report.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { needItems, type NeedItem } from '../state'
import { StatusMark } from '../components/StatusMark'
import { EmptyState } from '../components/EmptyState'
import { ago } from '../lib/format'

const KIND_LABEL: Record<NeedItem['kind'], [string, string]> = {
  approval: ['approval', 'approvals'],
  purchase: ['purchase', 'purchases'],
  failed: ['failed run', 'failed runs'],
  paused: ['pause', 'pauses'],
}

export function Attention(): JSX.Element {
  const items = needItems()
  const [kind, setKind] = useState<NeedItem['kind'] | null>(null)
  const shown = kind ? items.filter((i) => i.kind === kind) : items

  return (
    <>
      <div class="dl-phead">
        <div>
          <h1>Needs you</h1>
        </div>
      </div>
      {items.length ? (
        <section class="dl-glance" aria-label="What needs you, by kind">
          {(Object.keys(KIND_LABEL) as NeedItem['kind'][]).map((k) => {
            const n = items.filter((i) => i.kind === k).length
            if (!n) return null
            const [one, many] = KIND_LABEL[k]
            const on = kind === k
            return (
              <button
                type="button"
                class={'dl-tile' + (on ? ' on' : '')}
                aria-pressed={on}
                onClick={() => setKind(on ? null : k)}
              >
                <span class="dl-lbl">{n === 1 ? one[0].toUpperCase() + one.slice(1) : many[0].toUpperCase() + many.slice(1)}</span>
                <b>{n}</b>
                <span class="d">{on ? 'showing only these' : 'waiting on you'}</span>
              </button>
            )
          })}
        </section>
      ) : null}
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            {kind && shown.length < items.length ? (
              <p class="need-shown">
                Showing {shown.length} of {items.length} · <button class="link" type="button" onClick={() => setKind(null)}>Clear</button>
              </p>
            ) : null}
            {NeedList(shown)}
          </section>
        </div>
      </div>
    </>
  )
}

function NeedList(items: NeedItem[]): JSX.Element {
  if (!items.length) {
    return (
      <EmptyState small title="Nothing needs you">
        <p>When a bot asks for approval or a run fails, it shows up here.</p>
      </EmptyState>
    )
  }
  return (
    <ul class="dl-list dl-needs">
      {items.map((a) => (
        <li class="dl-need" data-s={a.mark}>
          <a class="dl-need-a" href={a.href}>
            <span class="dl-ng">
              <StatusMark s={a.mark} mkOnly lg label={a.mark === 'fail' ? 'failed' : 'needs you'} />
            </span>
            <span class="dl-nt">
              <b>{a.title}</b>
              {a.why ? <span class="dl-why">{a.why}</span> : null}
            </span>
            <span class="dl-age">{a.at ? ago(a.at) : ''}</span>
            <span class="dl-na">
              <span class="btn sm secondary">{a.verb}</span>
            </span>
          </a>
        </li>
      ))}
    </ul>
  )
}
