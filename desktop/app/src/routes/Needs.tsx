// Needs you: attention items by severity, one plain-sentence row each, one
// action. Ported from the prototype's overview.js Needs you list (dl-need
// rows) and js/screens/bots.js's approval page for the detail. See the plan:
// "Needs you (default): attention items by severity ... one action each."
import type { JSX } from 'preact'
import { useEffect } from 'preact/hooks'
import { api } from '../api/client'
import { actionFor, markFor, verbFor } from '../lib/attention'
import { ageText } from '../lib/time'
import { attention, attentionLoaded, refreshAttention } from '../state'
import type { AttentionItem } from '../api/types'
import { go, seg } from '../router'
import { Mark } from '../components/icons'
import { Approval } from '../components/Approval'

function NeedRow(props: { item: (typeof attention.value)[number] }): JSX.Element {
  const { item } = props
  const action = actionFor(item)
  const verb = verbFor(item)
  const onAct = async (e: Event) => {
    if (action.kind === 'proposal' || action.kind === 'purchase') {
      e.preventDefault()
      go(`needs/${item.key}`)
    } else if (action.kind === 'dismiss') {
      e.preventDefault()
      await api.dismiss(item.key)
      await refreshAttention()
    } else if (action.kind === 'browser') {
      e.preventDefault()
      await api.openInBrowser(action.url)
    }
  }
  return (
    <li class="dl-need" data-s={markFor(item)}>
      <a class="dl-need-a" href="#" onClick={onAct} data-testid="need-row">
        <span class="dl-ng">
          <Mark s={markFor(item)} lg label />
        </span>
        <span class="dl-nt">
          <b>{item.title}</b>
          {item.detail ? <span class="dl-why">{item.detail}</span> : null}
        </span>
        <span class="dl-age">{ageText(item.opened)}</span>
        <span class="dl-na">{verb ? <span class="btn sm secondary">{verb}</span> : null}</span>
      </a>
    </li>
  )
}

export function Needs(): JSX.Element {
  useEffect(() => {
    if (!attentionLoaded.value) void refreshAttention()
  }, [])

  const key = seg(1)
  if (key) return <NeedDetail itemKey={key} />

  const items = attention.value
  return (
    <div class="page dl-page">
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card dl-needs-card">
            <header class="dl-ch">
              <h2>Needs you</h2>
              {items.length ? (
                <span class="count hot" aria-label={`${items.length}`}>
                  {items.length}
                </span>
              ) : null}
            </header>
            {!attentionLoaded.value ? (
              <p class="dl-sub">Loading…</p>
            ) : items.length === 0 ? (
              <div class="empty small">
                <span class="serif">Nothing needs you</span>
                <p>When a bot asks for approval or a run fails, it shows up here.</p>
              </div>
            ) : (
              <ul class="dl-list dl-needs">
                {items.map((item: AttentionItem) => (
                  <NeedRow key={item.key} item={item} />
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
    </div>
  )
}

function NeedDetail(props: { itemKey: string }): JSX.Element {
  const item = attention.value.find((a: AttentionItem) => a.key === props.itemKey)
  if (!item) return <p class="dl-sub">Loading…</p>
  const action = actionFor(item)
  return (
    <div class="page dl-page">
      <div class="prop-single">
        {action.kind === 'proposal' ? (
          <Approval kind="proposal" bot={action.bot} id={action.id} onDecided={() => void refreshAttention()} />
        ) : action.kind === 'purchase' ? (
          <Approval kind="purchase" bot={null} id={action.id} onDecided={() => void refreshAttention()} />
        ) : (
          <ErrFallback message="This item has nothing to review in the app." />
        )}
      </div>
    </div>
  )
}

function ErrFallback(props: { message: string }): JSX.Element {
  return <p class="dl-sub">{props.message}</p>
}
