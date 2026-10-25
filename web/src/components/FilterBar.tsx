// The shared filter bar (design §4.2). A search box, filter MENUS for
// conductor's own concepts (Status, Workflow), and a time range. What a
// connector's events carry (github.repo) is a SEARCH QUALIFIER — typed into the
// search as `repo:acme/web` and shown as a removable pill, never given its own
// menu. Qualifier values autocomplete from what the run history carries.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import type { AttrQualifier } from '../api/types'
import { api } from '../api/client'
import { TIME_RANGES, STATUSES, type FilterState } from '../lib/filter'

export interface Option {
  v: string
  label: string
  n?: number
}

export function FilterBar({
  value,
  onChange,
  workflows,
  attrs,
  showTime = true,
}: {
  value: FilterState
  onChange: (next: FilterState) => void
  workflows: Option[]
  attrs: AttrQualifier[]
  showTime?: boolean
}): JSX.Element {
  const set = (patch: Partial<FilterState>) => onChange({ ...value, ...patch })
  const toggle = (key: 'status' | 'workflow', v: string) => {
    const cur = value[key]
    set({ [key]: cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v] } as Partial<FilterState>)
  }

  // Search box. A `qualifier:value` token whose qualifier matches a connector
  // attr (by key or its bare alias) becomes a pill; anything else is free text.
  const [text, setText] = useState(value.q)
  useEffect(() => setText(value.q), [value.q])

  const aliasOf = (a: AttrQualifier) => {
    // Bare alias (repo) when no other connector publishes the same field.
    const dupes = attrs.filter((x) => x.field === a.field).length
    return dupes === 1 ? a.field : a.key
  }
  const findAttr = (token: string): AttrQualifier | undefined =>
    attrs.find((a) => a.key === token || aliasOf(a) === token)

  const commitSearch = (raw: string) => {
    const m = raw.match(/^([\w.]+):(.+)$/)
    if (m) {
      const attr = findAttr(m[1])
      if (attr) {
        const vals = value.attrs[attr.key] || []
        const v = m[2].trim()
        if (v && !vals.includes(v)) set({ attrs: { ...value.attrs, [attr.key]: [...vals, v] }, q: '' })
        setText('')
        return
      }
    }
    set({ q: raw })
  }

  const removePill = (key: string, v: string) => {
    const vals = (value.attrs[key] || []).filter((x) => x !== v)
    const next = { ...value.attrs }
    if (vals.length) next[key] = vals
    else delete next[key]
    set({ attrs: next })
  }

  const statusCounts = STATUSES.map(([v, label]) => ({ v, label }))

  const statusLabel = value.status.length ? `Status · ${value.status.length}` : 'Status'
  const wfLabel = value.workflow.length ? `Workflow · ${value.workflow.length}` : 'Workflow'

  return (
    <div class="filterbar" role="search">
      <div class="fb-search">
        <SearchGlyph />
        <input
          class="fb-input"
          type="search"
          placeholder={placeholder(attrs)}
          aria-label="Search and filter runs"
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
            if (v !== value.q) set({ q: v })
          }}
        />
      </div>

      <Menu label={statusLabel} on={value.status.length > 0}>
        {statusCounts.map((o) => (
          <Check checked={value.status.includes(o.v)} onToggle={() => toggle('status', o.v)} label={o.label} />
        ))}
      </Menu>

      {workflows.length > 0 ? (
        <Menu label={wfLabel} on={value.workflow.length > 0}>
          {workflows.map((o) => (
            <Check
              checked={value.workflow.includes(o.v)}
              onToggle={() => toggle('workflow', o.v)}
              label={o.label}
              n={o.n}
            />
          ))}
        </Menu>
      ) : null}

      {showTime ? (
        <select class="fb-time" aria-label="Time range" value={value.since} onChange={(e) => set({ since: (e.target as HTMLSelectElement).value })}>
          {TIME_RANGES.map(([k, label]) => (
            <option value={k}>{label}</option>
          ))}
        </select>
      ) : null}

      <div class="fb-pills">
        {Object.entries(value.attrs).flatMap(([key, vals]) =>
          vals.map((v) => {
            const attr = attrs.find((a) => a.key === key)
            return (
              <AttrPill key={key + v} label={attr ? attr.label : key} value={v} onRemove={() => removePill(key, v)} />
            )
          }),
        )}
      </div>
    </div>
  )
}

function placeholder(attrs: AttrQualifier[]): string {
  const ex = attrs.find((a) => a.field === 'repo')
  return ex ? `Search, or filter: status:failed, repo:acme/web` : 'Search, or filter: status:failed'
}

function Menu({ label, on, children }: { label: string; on: boolean; children: JSX.Element | JSX.Element[] }): JSX.Element {
  const ref = useRef<HTMLDetailsElement>(null)
  // Close on outside click (native <details> stays open otherwise).
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
    <details class="fb-menu" ref={ref}>
      <summary class={'fb-menu-btn' + (on ? ' on' : '')}>{label}</summary>
      <div class="fb-menu-pop" role="group" aria-label={label}>
        {children}
      </div>
    </details>
  )
}

function Check({ checked, onToggle, label, n }: { checked: boolean; onToggle: () => void; label: string; n?: number }): JSX.Element {
  return (
    <label class="fb-check">
      <input type="checkbox" checked={checked} onChange={onToggle} />
      <span>{label}</span>
      {n != null ? <span class="fb-n">{n}</span> : null}
    </label>
  )
}

function AttrPill({ label, value, onRemove }: { label: string; value: string; onRemove: () => void }): JSX.Element {
  return (
    <span class="fb-pill">
      <span class="fb-pill-k">{label}</span>
      <span class="fb-pill-v mono">{value}</span>
      <button type="button" class="fb-pill-x" aria-label={`Remove ${label} ${value}`} onClick={onRemove}>
        ✕
      </button>
    </span>
  )
}

function SearchGlyph(): JSX.Element {
  return (
    <svg class="fb-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
      <circle cx="7" cy="7" r="4.5" />
      <path d="M10.5 10.5 14 14" stroke-linecap="round" />
    </svg>
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
