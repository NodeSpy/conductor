// The shared filter bar (design §4.2, prototype js/filter.js UI.filterBar,
// css/filter.css): a search field that holds qualifier pills, plain text
// buttons with a chevron (each a searchable menu), a time-range button with a
// clock icon, and on a phone a single "Filter" button. Every screen that
// filters a list (Activity, Bots, …) renders the same `.fb` markup so they
// pick up one fix together; only the menus and qualifiers differ per screen.
//
// Left out of this port (no backend/UI need yet): the "More filters" overflow
// menu, recent-picks ranking and the qualifier autocomplete dropdown under the
// search field. Typing `qualifier:value` and pressing Enter still turns it
// into a pill; it is just not suggested as you type.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import type { AttrQualifier } from '../api/types'
import { api } from '../api/client'
import { TIME_RANGES } from '../lib/filter'
import { Icon } from './Icon'

export interface Option {
  v: string
  label: string
  n?: number
}

// A menu button in the bar (Status, Workflow, Mode, …) or, with `view: true`,
// a view control (Group by) drawn at the far right — a view never counts
// toward "N filters on" and Clear filters leaves it alone.
export interface FilterMenu {
  key: string
  label: string
  multi?: boolean
  selected: string[]
  options: Option[]
  onToggle: (v: string) => void
  onClear?: () => void
  view?: boolean
  // title heads the open menu (default "Filter by <label>")
  title?: string
}

export function FilterBar({
  q,
  onQueryChange,
  placeholder = 'Search',
  ariaLabel = 'Search and filter',
  menus = [],
  attrs = [],
  attrsValue = {},
  onAttrsChange,
  since,
  onSinceChange,
  showTime = false,
  filterByHint = false,
  end,
}: {
  q: string
  onQueryChange: (q: string) => void
  placeholder?: string
  ariaLabel?: string
  menus?: FilterMenu[]
  attrs?: AttrQualifier[]
  attrsValue?: Record<string, string[]>
  onAttrsChange?: (next: Record<string, string[]>) => void
  since?: string
  onSinceChange?: (v: string) => void
  showTime?: boolean
  // filterByHint shows the "Filter by…" chip in the search field even with
  // no connector-published qualifiers (attrs) — a screen whose own menus
  // (Level, From, …) are the only filters still gets the design's chip
  // pointing at them.
  filterByHint?: boolean
  // end is drawn last in the bar (the catalog picker's "Add from a git URL or
  // custom source…", filter.js's `end`).
  end?: JSX.Element
}): JSX.Element {
  const [text, setText] = useState(q)
  useEffect(() => setText(q), [q])
  const [sheetOpen, setSheetOpen] = useState(false)

  const aliasOf = (a: AttrQualifier) => {
    const dupes = attrs.filter((x) => x.field === a.field).length
    return dupes === 1 ? a.field : a.key
  }
  const findAttr = (token: string): AttrQualifier | undefined =>
    attrs.find((a) => a.key === token || aliasOf(a) === token)

  const commitSearch = (raw: string) => {
    const m = raw.match(/^([\w.]+):(.+)$/)
    if (m) {
      const attr = findAttr(m[1])
      if (attr && onAttrsChange) {
        const vals = attrsValue[attr.key] || []
        const v = m[2].trim()
        if (v && !vals.includes(v)) onAttrsChange({ ...attrsValue, [attr.key]: [...vals, v] })
        setText('')
        onQueryChange('')
        return
      }
    }
    onQueryChange(raw)
  }

  const removePill = (key: string, v: string) => {
    if (!onAttrsChange) return
    const vals = (attrsValue[key] || []).filter((x) => x !== v)
    const next = { ...attrsValue }
    if (vals.length) next[key] = vals
    else delete next[key]
    onAttrsChange(next)
  }

  const liveMenus = menus.filter((m) => !m.view)
  const viewMenus = menus.filter((m) => m.view)
  const anyPills = Object.values(attrsValue).some((v) => v.length)
  const any = !!q || anyPills || liveMenus.some((m) => m.selected.length > 0)
  const nOn = (q ? 1 : 0) + liveMenus.filter((m) => m.selected.length > 0).length + Object.keys(attrsValue).filter((k) => attrsValue[k]?.length).length

  const clearAll = () => {
    onQueryChange('')
    setText('')
    if (onAttrsChange) onAttrsChange({})
    liveMenus.forEach((m) => (m.onClear ? m.onClear() : m.selected.forEach((v) => m.onToggle(v))))
  }

  return (
    <div class="fb" role="search">
      <div class={'fb-search' + (any ? ' has-q' : '')}>
        <Icon name="search" class="fb-ico" />
        {anyPills ? (
          <span class="fb-pills">
            {Object.entries(attrsValue).flatMap(([key, vals]) =>
              vals.map((v) => {
                const attr = attrs.find((a) => a.key === key)
                return <AttrPill key={key + v} label={attr ? attr.label : key} value={v} onRemove={() => removePill(key, v)} />
              }),
            )}
          </span>
        ) : null}
        <input
          class="fb-in"
          type="search"
          placeholder={placeholder}
          aria-label={ariaLabel}
          value={text}
          onInput={(e) => setText((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              commitSearch((e.target as HTMLInputElement).value)
            }
          }}
          onBlur={(e) => {
            const v = (e.target as HTMLInputElement).value
            if (v !== q) onQueryChange(v)
          }}
        />
        {attrs.length || filterByHint ? (
          <button type="button" class="fb-by" title="Every filter you can type" onClick={() => (document.activeElement as HTMLElement)?.blur()}>
            Filter by…
          </button>
        ) : null}
        {any ? (
          <button type="button" class="fb-x" aria-label="Clear the search and every filter" onClick={clearAll}>
            <Icon name="x" />
          </button>
        ) : null}
      </div>

      <div class="fb-bar" role="group" aria-label="Filters">
        {liveMenus.map((m) => (
          <MenuButton key={m.key} menu={m} />
        ))}

        <button type="button" class={'fb-phone' + (nOn ? ' on' : '')} aria-haspopup="dialog" onClick={() => setSheetOpen(true)}>
          <Icon name="filter" />
          Filter
          {nOn ? <span class="fb-n">{nOn}</span> : null}
        </button>

        {any ? (
          <button type="button" class="fb-reset" onClick={clearAll}>
            <Icon name="x" />
            Clear filters
          </button>
        ) : null}

        {viewMenus.map((m) => (
          <MenuButton key={m.key} menu={m} />
        ))}

        {showTime && since !== undefined && onSinceChange ? (
          <TimeButton since={since} onChange={onSinceChange} />
        ) : null}
        {end ?? null}
      </div>

      {sheetOpen ? (
        <Sheet
          q={text}
          onQueryChange={(v) => {
            setText(v)
            onQueryChange(v)
          }}
          placeholder={placeholder}
          menus={menus}
          since={since}
          onSinceChange={onSinceChange}
          showTime={showTime}
          any={any}
          onClear={clearAll}
          onClose={() => setSheetOpen(false)}
        />
      ) : null}
    </div>
  )
}

function btnLabel(m: FilterMenu): JSX.Element {
  if (!m.selected.length) return <span class="fb-l">{m.label}</span>
  const first = m.options.find((o) => o.v === m.selected[0])?.label ?? m.selected[0]
  return (
    <>
      <span class="fb-l">{m.label}:</span> <span class="fb-v">{first}{m.selected.length > 1 ? ` +${m.selected.length - 1}` : ''}</span>
    </>
  )
}

function MenuButton({ menu }: { menu: FilterMenu }): JSX.Element {
  const ref = useRef<HTMLDetailsElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const onDoc = (e: MouseEvent) => {
      if (el.open && !el.contains(e.target as Node)) el.open = false
    }
    document.addEventListener('click', onDoc)
    return () => document.removeEventListener('click', onDoc)
  }, [])
  return (
    <details ref={ref} style={{ position: 'relative', display: 'inline-block', ...(menu.view ? { marginLeft: 'auto' } : {}) }}>
      <summary class={'fb-btn' + (menu.selected.length && !menu.view ? ' on' : '') + (menu.view ? ' fb-view' : '')} aria-haspopup="listbox">
        {btnLabel(menu)}
        <Icon name="chevd" />
      </summary>
      <div class="fb-menu" role="listbox" aria-label={menu.label} aria-multiselectable={menu.multi} style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0 }}>
        <div class="fb-mh">
          <b>{menu.title || `Filter by ${menu.label.toLowerCase()}`}</b>
          {menu.multi && menu.selected.length ? (
            <button type="button" class="link" onClick={() => (menu.onClear ? menu.onClear() : menu.selected.forEach((v) => menu.onToggle(v)))}>
              Clear
            </button>
          ) : null}
        </div>
        <ul class="fb-list" role="presentation">
          {menu.options.map((o) => {
            const on = menu.selected.includes(o.v)
            return (
              <li key={o.v} role="option" aria-selected={on} class={'fb-opt' + (on ? ' sel' : '')} onClick={() => menu.onToggle(o.v)}>
                <Icon name="check" />
                <span class="fb-ol">
                  <b>{o.label}</b>
                </span>
                {o.n != null ? <span class="fb-on">{o.n}</span> : null}
              </li>
            )
          })}
          {!menu.options.length ? <li class="fb-none" role="presentation">Nothing to filter by yet.</li> : null}
        </ul>
      </div>
    </details>
  )
}

function TimeButton({ since, onChange }: { since: string; onChange: (v: string) => void }): JSX.Element {
  const ref = useRef<HTMLDetailsElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const onDoc = (e: MouseEvent) => {
      if (el.open && !el.contains(e.target as Node)) el.open = false
    }
    document.addEventListener('click', onDoc)
    return () => document.removeEventListener('click', onDoc)
  }, [])
  const label = TIME_RANGES.find(([k]) => k === since)?.[1] || since
  return (
    <details ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <summary class={'fb-btn fb-time' + (since !== 'all' ? ' on' : '')} aria-haspopup="listbox" title="Time range">
        <Icon name="clock" />
        <span class="fb-v">{label}</span>
        <Icon name="chevd" />
      </summary>
      <div class="fb-menu" role="listbox" aria-label="Time range" style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0 }}>
        <div class="fb-mh">
          <b>Time range</b>
        </div>
        <ul class="fb-list" role="presentation">
          {TIME_RANGES.map(([k, l]) => (
            <li key={k} role="option" aria-selected={since === k} class={'fb-opt' + (since === k ? ' sel' : '')} onClick={() => { onChange(k); if (ref.current) ref.current.open = false }}>
              <Icon name="check" />
              <span class="fb-ol">
                <b>{l}</b>
              </span>
            </li>
          ))}
        </ul>
      </div>
    </details>
  )
}

function AttrPill({ label, value, onRemove }: { label: string; value: string; onRemove: () => void }): JSX.Element {
  return (
    <span class="fb-pill">
      <button type="button" class="fb-pv">{label}: {value}</button>
      <button type="button" class="fb-px" aria-label={`Remove ${label} ${value}`} onClick={onRemove}>
        <Icon name="x" />
      </button>
    </span>
  )
}

// Sheet is the phone's single Filter button's sheet: every menu and the time
// range stacked, with Done. It does not drill a row into its own screen (the
// prototype's fb-rows pattern) — a short, fixed set of menus fits in one
// scroll on a phone, and this keeps one component instead of two parallel
// menu renderers.
function Sheet({
  q,
  onQueryChange,
  placeholder,
  menus,
  since,
  onSinceChange,
  showTime,
  any,
  onClear,
  onClose,
}: {
  q: string
  onQueryChange: (v: string) => void
  placeholder: string
  menus: FilterMenu[]
  since?: string
  onSinceChange?: (v: string) => void
  showTime?: boolean
  any: boolean
  onClear: () => void
  onClose: () => void
}): JSX.Element {
  return (
    <>
      <div class="fb-scrim" onClick={onClose} />
      <div class="fb-menu fb-sheet" role="dialog" aria-modal="true" aria-label="Filter">
        <div class="fb-handle" />
        <div class="fb-mh">
          <b>Filter</b>
          <button type="button" class="btn sm" onClick={onClose}>Done</button>
        </div>
        <div class="fb-fs">
          <Icon name="search" />
          <input class="fb-find" type="search" placeholder={placeholder} value={q} onInput={(e) => onQueryChange((e.target as HTMLInputElement).value)} />
        </div>
        <ul class="fb-rows">
          {menus.map((m) => (
            <li key={m.key} class="fb-gh">
              {m.label}
              <ul class="fb-list" role="listbox" aria-multiselectable={m.multi}>
                {m.options.map((o) => {
                  const on = m.selected.includes(o.v)
                  return (
                    <li key={o.v} role="option" aria-selected={on} class={'fb-opt' + (on ? ' sel' : '')} onClick={() => m.onToggle(o.v)}>
                      <Icon name="check" />
                      <span class="fb-ol"><b>{o.label}</b></span>
                      {o.n != null ? <span class="fb-on">{o.n}</span> : null}
                    </li>
                  )
                })}
              </ul>
            </li>
          ))}
          {showTime && since !== undefined && onSinceChange ? (
            <li class="fb-gh">
              Time range
              <ul class="fb-list" role="listbox">
                {TIME_RANGES.map(([k, l]) => (
                  <li key={k} role="option" aria-selected={since === k} class={'fb-opt' + (since === k ? ' sel' : '')} onClick={() => onSinceChange(k)}>
                    <Icon name="check" />
                    <span class="fb-ol"><b>{l}</b></span>
                  </li>
                ))}
              </ul>
            </li>
          ) : null}
        </ul>
        {any ? (
          <button type="button" class="btn sm secondary fb-sheet-clear" onClick={onClear}>Clear filters</button>
        ) : null}
      </div>
    </>
  )
}

// attrValuesFor fetches autocomplete values for a qualifier (used by callers
// that want a datalist; exported for reuse/tests).
export async function attrValuesFor(attr: string): Promise<string[]> {
  try {
    return (await api.attrValues(attr)).values || []
  } catch {
    return []
  }
}
