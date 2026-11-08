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
  // more lists the menu under "More filters" until it's set (filter.js f.more)
  more?: boolean
  // alias is the qualifier key typed in the search field (status:draft)
  alias?: string
  // emptyText is the menu's own line when it has no options (Activity's Bot menu)
  emptyText?: string
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
  qualifiers = false,
  noSearch = false,
  sinceDef = 'all',
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
  // qualifiers: the menus are also typed filters (key:value in the search
  // field), and "Filter by…" lists them (filter.js qf/suggest).
  qualifiers?: boolean
  // noSearch drops the search field: a bar of one control (a workflow's Runs
  // tab time range, filter.js `noSearch`).
  noSearch?: boolean
  // sinceDef: the time range that isn't a filter (not marked on)
  sinceDef?: string
}): JSX.Element {
  const [text, setText] = useState(q)
  const [ac, setAc] = useState<{ ins: string; key: string; hint: string; v?: string }[] | null>(null)
  const [acI, setAcI] = useState(0)
  const [moreOpen, setMoreOpen] = useState(false)
  const [forced, setForced] = useState<string[]>([])
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!moreOpen) return
    const off = () => setMoreOpen(false)
    const t = setTimeout(() => document.addEventListener('click', off), 0)
    return () => {
      clearTimeout(t)
      document.removeEventListener('click', off)
    }
  }, [moreOpen])
  useEffect(() => setText(q), [q])
  const [sheetOpen, setSheetOpen] = useState(false)

  const aliasOf = (a: AttrQualifier) => {
    const dupes = attrs.filter((x) => x.field === a.field).length
    return dupes === 1 ? a.field : a.key
  }
  const findAttr = (token: string): AttrQualifier | undefined =>
    attrs.find((a) => a.key === token || aliasOf(a) === token)

  const menuFor = (k: string) => menus.find((x) => !x.view && ((x.alias || x.key) === k || x.key === k))
  // a typed key:value turns on that menu's option (by value or label); the
  // rest of the text stays the free search
  const applyTyped = (raw: string): string => {
    if (!qualifiers) return raw
    const rest: string[] = []
    for (const tok of raw.split(/\s+/).filter(Boolean)) {
      const m = tok.match(/^([\w.-]+):(.+)$/)
      const menu = m && menuFor(m[1])
      if (!m || !menu) {
        rest.push(tok)
        continue
      }
      for (const want of m[2].split(',')) {
        const o = menu.options.find((x) => x.v.toLowerCase() === want.toLowerCase() || x.label.toLowerCase() === want.toLowerCase())
        if (o && !menu.selected.includes(o.v)) menu.onToggle(o.v)
      }
      setForced((f) => (f.includes(menu.key) ? f : [...f, menu.key]))
    }
    return rest.join(' ')
  }
  const suggest = (raw: string, all = false) => {
    if (!qualifiers) return
    const w = raw.split(/\s+/).pop() || ''
    const m = w.match(/^([\w.-]+):(.*)$/)
    const keyOf = (x: FilterMenu) => x.alias || x.key
    if (m) {
      const menu = menuFor(m[1])
      if (!menu) return setAc(null)
      const cur = m[2].toLowerCase()
      setAc(menu.options.filter((o) => `${o.v} ${o.label}`.toLowerCase().includes(cur)).slice(0, 8).map((o) => ({ ins: `${keyOf(menu)}:${o.v} `, key: keyOf(menu), hint: o.label, v: o.v })))
    } else if (w.length) {
      const lw = w.toLowerCase()
      setAc(liveMenus.filter((x) => keyOf(x).startsWith(lw) || x.label.toLowerCase().startsWith(lw)).map((x) => ({ ins: `${keyOf(x)}:`, key: keyOf(x), hint: x.label })))
    } else if (all || !raw.trim()) setAc(liveMenus.map((x) => ({ ins: `${keyOf(x)}:`, key: keyOf(x), hint: x.label })))
    else setAc(null)
    setAcI(0)
  }
  const accept = (i: number) => {
    const it = ac?.[i]
    if (!it) return
    const words = text.split(/\s+/)
    words.pop()
    let next = [...words, it.ins].join(' ').replace(/^\s+/, '')
    if (it.v !== undefined) {
      next = applyTyped(next)
      onQueryChange(next)
      setAc(null)
    } else suggest(next)
    setText(next)
    setTimeout(() => inputRef.current?.focus())
  }

  const commitSearch = (raw: string) => {
    if (qualifiers) {
      const rest = applyTyped(raw)
      setText(rest)
      onQueryChange(rest)
      setAc(null)
      return
    }
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
    setForced([])
    onQueryChange('')
    setText('')
    if (onAttrsChange) onAttrsChange({})
    liveMenus.forEach((m) => (m.onClear ? m.onClear() : m.selected.forEach((v) => m.onToggle(v))))
  }

  return (
    <div class="fb" role="search" data-nosearch={noSearch ? '' : undefined}>
      {noSearch ? null : <div class={'fb-search' + (any ? ' has-q' : '')}>
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
          ref={inputRef}
          role={qualifiers ? 'combobox' : undefined}
          aria-expanded={qualifiers ? !!ac?.length : undefined}
          autoComplete="off"
          onInput={(e) => {
            const v = (e.target as HTMLInputElement).value
            setText(v)
            // the list follows the search as you type (filter.js: every input commits the free text);
            // a half-typed qualifier waits until it's whole
            if (!qualifiers || !/(^|\s)[\w.-]+:\S*$/.test(v)) onQueryChange(v)
            if (qualifiers) suggest(v)
          }}
          onFocus={(e) => qualifiers && suggest((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (ac?.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
              e.preventDefault()
              setAcI((i) => (i + (e.key === 'ArrowDown' ? 1 : ac.length - 1)) % ac.length)
              return
            }
            if (ac?.length && (e.key === 'Tab' || e.key === 'Enter') && /\S$/.test((e.target as HTMLInputElement).value) && /[\w.-]+:?\S*$/.test((e.target as HTMLInputElement).value) && ((e.target as HTMLInputElement).value.split(/\s+/).pop() || '').length) {
              const last = (e.target as HTMLInputElement).value.split(/\s+/).pop() || ''
              if (last.includes(':') || liveMenus.some((x) => (x.alias || x.key).startsWith(last.toLowerCase()))) {
                e.preventDefault()
                accept(acI)
                return
              }
            }
            if (e.key === 'Escape' && ac) {
              e.preventDefault()
              setAc(null)
              return
            }
            if (e.key === 'Enter') {
              e.preventDefault()
              commitSearch((e.target as HTMLInputElement).value)
            }
          }}
          onBlur={(e) => {
            const v = (e.target as HTMLInputElement).value
            setTimeout(() => setAc(null), 150)
            if (qualifiers) return
            if (v !== q) onQueryChange(v)
          }}
        />
        {attrs.length || filterByHint || qualifiers ? (
          <button
            type="button"
            class="fb-by"
            data-fb-by
            title="Every filter you can type"
            onMouseDown={(e) => qualifiers && e.preventDefault()}
            onClick={() => {
              if (!qualifiers) return (document.activeElement as HTMLElement)?.blur()
              const v = text && !/\s$/.test(text) ? text + ' ' : text
              setText(v)
              inputRef.current?.focus()
              suggest(v, true)
            }}
          >
            Filter by…
          </button>
        ) : null}
        {qualifiers && ac?.length ? (
          <ul class="fb-ac" role="listbox" data-testid="fb-ac">
            {ac.map((x, i) => (
              <li role="option" class={i === acI ? 'act' : ''} aria-selected={i === acI} key={x.ins} onMouseDown={(e) => { e.preventDefault(); accept(i) }}>
                <span class="fb-ak">{x.key}:</span>
                {x.v !== undefined ? <b>{x.v}</b> : null}
                <small>{x.hint}</small>
              </li>
            ))}
          </ul>
        ) : null}
        {any ? (
          <button type="button" class="fb-x" aria-label="Clear the search and every filter" onClick={clearAll}>
            <Icon name="x" />
          </button>
        ) : null}
      </div>}

      <div class="fb-bar" role="group" aria-label="Filters">
        {liveMenus.filter((m) => !m.more || m.selected.length || forced.includes(m.key)).map((m) => (
          <MenuButton key={m.key} menu={m} openNow={forced[forced.length - 1] === m.key && !m.selected.length} />
        ))}

        <button type="button" class={'fb-phone' + (nOn ? ' on' : '')} aria-haspopup="dialog" onClick={() => setSheetOpen(true)}>
          <Icon name="filter" />
          Filter
          {nOn ? <span class="fb-n">{nOn}</span> : null}
        </button>

        {liveMenus.some((m) => m.more && !m.selected.length && !forced.includes(m.key)) ? (
          <span style={{ position: 'relative', display: 'inline-block' }}>
            <button type="button" class="fb-btn fb-more-btn" aria-haspopup="menu" aria-expanded={moreOpen} onClick={() => setMoreOpen((o) => !o)}>
              <span class="fb-l">More filters</span>
              <Icon name="chevd" />
            </button>
            {moreOpen ? (
              <div class="fb-menu fb-more" role="menu" aria-label="More filters" style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0 }}>
                <div class="fb-mh"><b>More filters</b></div>
                <ul class="fb-list">
                  {liveMenus.filter((m) => m.more && !m.selected.length && !forced.includes(m.key)).map((m) => (
                    <li key={m.key}>
                      <button type="button" role="menuitem" class="fb-opt fb-mi" onClick={() => { setMoreOpen(false); setForced((f) => [...f, m.key]) }}>
                        <span></span>
                        <span class="fb-ol"><b>{m.label}</b><small>{m.alias || m.key}:</small></span>
                        <Icon name="chev" />
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
          </span>
        ) : null}

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
          <TimeButton since={since} onChange={onSinceChange} def={sinceDef} alone={noSearch} push={!viewMenus.length} />
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

function MenuButton({ menu, openNow }: { menu: FilterMenu; openNow?: boolean }): JSX.Element {
  const ref = useRef<HTMLDetailsElement>(null)
  useEffect(() => {
    if (openNow && ref.current) setTimeout(() => ref.current && (ref.current.open = true), 0)
  }, [openNow])
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
          {!menu.options.length ? <li class="fb-none" role="presentation">{menu.emptyText || 'Nothing to filter by yet.'}</li> : null}
        </ul>
      </div>
    </details>
  )
}

// def is the range that counts as no filter (the time field's def: '7d' on a
// workflow's Runs tab); alone (a bar with no search) it sits at the far right.
function TimeButton({ since, onChange, def = 'all', alone = false, push = false }: { since: string; onChange: (v: string) => void; def?: string; alone?: boolean; push?: boolean }): JSX.Element {
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
    // the details is the bar's flex item, so it carries the design's
    // `.fb-btn.fb-time { margin-left: auto }`: the range sits at the far right
    <details ref={ref} class="fb-time-d" style={{ position: 'relative', display: 'inline-block', marginLeft: alone || push ? 'auto' : undefined }}>
      <summary class={'fb-btn fb-time' + (since !== def ? ' on' : '')} aria-haspopup="listbox" title="Time range">
        <Icon name="clock" />
        <span class="fb-v">{label}</span>
        <Icon name="chevd" />
      </summary>
      <div class="fb-menu" role="listbox" aria-label="Time range" style={{ position: 'absolute', top: 'calc(100% + 6px)', ...(alone || push ? { right: 0 } : { left: 0 }) }}>
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
                {!m.options.length && m.emptyText ? <li class="fb-none" role="presentation">{m.emptyText}</li> : null}
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
