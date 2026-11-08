// Needs you (design §4.1): the aggregator's own page — one tile per kind, a
// search + Kind/Bot/Workflow filter bar, the aggregator's full list, and the
// same For your information side panel as Home. Source: the prototype's
// js/screens/runs.js (UI.route('attention')), reusing UI.filterBar's markup
// (components/FilterBar.tsx). Bot/Workflow options are built from the items
// that actually carry one (failed runs today) — a paused state or a purchase
// has neither, same as the prototype's tally() over empty facts.
import type { JSX } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import { needItems, fyi, type NeedItem } from '../state'
import { StatusMark } from '../components/StatusMark'
import { EmptyState } from '../components/EmptyState'
import { Fyi } from '../components/Fyi'
import { FactsLine } from '../components/Facts'
import { FilterBar } from '../components/FilterBar'
import { ago } from '../lib/format'

const KIND_LABEL: Record<NeedItem['kind'], [string, string]> = {
  approval: ['approval', 'approvals'],
  purchase: ['purchase', 'purchases'],
  failed: ['failed run', 'failed runs'],
  paused: ['pause', 'pauses'],
}

function oldestAgo(items: NeedItem[]): string {
  const ats = items.map((i) => i.at).filter(Boolean).sort()
  return ats.length ? ago(ats[0]) : ''
}

export function Attention(): JSX.Element {
  const items = needItems()
  const [q, setQ] = useState('')
  const [kinds, setKinds] = useState<string[]>([])
  const [bots, setBots] = useState<string[]>([])
  const [workflows, setWorkflows] = useState<string[]>([])

  const byQuery = (i: NeedItem) =>
    !q || i.title.toLowerCase().includes(q.toLowerCase()) || (i.why || '').toLowerCase().includes(q.toLowerCase())
  const shown = items.filter(
    (i) =>
      (kinds.length === 0 || kinds.includes(i.kind)) &&
      (bots.length === 0 || (i.bot ? bots.includes(i.bot) : false)) &&
      (workflows.length === 0 || (i.workflow ? workflows.includes(i.workflow) : false)) &&
      byQuery(i),
  )

  const kindOptions = useMemo(
    () =>
      (Object.keys(KIND_LABEL) as NeedItem['kind'][])
        .map((k) => ({ v: k, label: KIND_LABEL[k][1][0].toUpperCase() + KIND_LABEL[k][1].slice(1), n: items.filter((i) => i.kind === k).length }))
        .filter((o) => o.n > 0),
    [items],
  )

  // tally() over the facts the items actually carry today (failed runs): a
  // paused state or a purchase has neither, same as the prototype.
  const tally = (vs: string[]): { v: string; label: string; n: number }[] => {
    const m = new Map<string, number>()
    for (const v of vs) if (v) m.set(v, (m.get(v) || 0) + 1)
    return [...m].map(([v, n]) => ({ v, label: v, n }))
  }
  const botOptions = useMemo(() => tally(items.map((i) => i.bot || '')), [items])
  const workflowOptions = useMemo(() => tally(items.map((i) => i.workflow || '')), [items])

  return (
    <>
      <div class="dl-phead">
        <div>
          <h1>Needs you</h1>
        </div>
      </div>
      {items.length ? (
        <section class="dl-glance" id="need-tiles" aria-label="What needs you, by kind">
          {(Object.keys(KIND_LABEL) as NeedItem['kind'][]).map((k) => {
            const kItems = items.filter((i) => i.kind === k)
            const n = kItems.length
            if (!n) return null
            const [one, many] = KIND_LABEL[k]
            const on = kinds.includes(k)
            return (
              <button
                type="button"
                class={'dl-tile' + (on ? ' on' : '')}
                aria-pressed={on}
                onClick={() => setKinds(on ? kinds.filter((x) => x !== k) : [...kinds, k])}
              >
                <span class="dl-lbl">{n === 1 ? one[0].toUpperCase() + one.slice(1) : many[0].toUpperCase() + many.slice(1)}</span>
                <b>{n}</b>
                <span class="d">{on ? 'showing only these' : `oldest ${oldestAgo(kItems)}`}</span>
              </button>
            )
          })}
        </section>
      ) : null}
      {items.length ? (
        <FilterBar
          q={q}
          onQueryChange={setQ}
          placeholder="Search, or filter: kind:approval"
          ariaLabel="Search and filter what needs you"
          filterByHint
          menus={[
            {
              key: 'kind',
              label: 'Kind',
              multi: true,
              selected: kinds,
              options: kindOptions,
              onToggle: (v) => setKinds((ks) => (ks.includes(v) ? ks.filter((x) => x !== v) : [...ks, v])),
              onClear: () => setKinds([]),
            },
            ...(botOptions.length
              ? [
                  {
                    key: 'bot',
                    label: 'Bot',
                    multi: true,
                    selected: bots,
                    options: botOptions,
                    onToggle: (v: string) => setBots((bs) => (bs.includes(v) ? bs.filter((x) => x !== v) : [...bs, v])),
                    onClear: () => setBots([]),
                  },
                ]
              : []),
            ...(workflowOptions.length
              ? [
                  {
                    key: 'workflow',
                    label: 'Workflow',
                    multi: true,
                    selected: workflows,
                    options: workflowOptions,
                    onToggle: (v: string) => setWorkflows((ws) => (ws.includes(v) ? ws.filter((x) => x !== v) : [...ws, v])),
                    onClear: () => setWorkflows([]),
                  },
                ]
              : []),
          ]}
        />
      ) : null}
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            {(kinds.length || bots.length || workflows.length || q) && shown.length < items.length ? (
              <p class="need-shown">
                Showing {shown.length} of {items.length} ·{' '}
                <button
                  class="link"
                  type="button"
                  onClick={() => {
                    setKinds([])
                    setBots([])
                    setWorkflows([])
                    setQ('')
                  }}
                >
                  Clear
                </button>
              </p>
            ) : null}
            {NeedList(shown, items.length === 0)}
          </section>
        </div>
        <div class="dl-col-side">
          <Fyi items={fyi.value} />
        </div>
      </div>
    </>
  )
}

function NeedList(items: NeedItem[], none: boolean): JSX.Element {
  if (!items.length) {
    return none ? (
      <EmptyState hero icon="check" title="Nothing needs you" testid="attention-empty">
        <p>When a bot asks for approval or a run fails, it shows up here.</p>
      </EmptyState>
    ) : (
      <EmptyState small title="Nothing matches these filters" />
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
              <FactsLine facts={a.facts || []} />
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
