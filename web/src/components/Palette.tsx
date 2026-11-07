// The top bar's search affordance (design: js/palette.js, ⌘K). The prototype
// indexes pages, bots, runs, secrets, config paths and commands; the daemon
// has no search endpoint, so this phase ports the affordance and its keyboard
// path (click, Enter, ⌘K/Ctrl+K, Escape) but only indexes navigation — every
// screen the rail/tab bar/More can reach. Typing filters that list; there is
// no action index, run/bot/secret lookup or config-key search yet (noted in
// the phase report as a data gap, not a missing feature to hide).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { go } from '../router'
import { Icon } from './Icon'

export interface PaletteItem {
  key: string
  label: string
  icon: string
}

function filterItems(items: PaletteItem[], q: string): PaletteItem[] {
  const needle = q.trim().toLowerCase()
  if (!needle) return items
  return items.filter((i) => i.label.toLowerCase().includes(needle))
}

export function Palette({ items }: { items: PaletteItem[] }): JSX.Element {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const openPalette = () => {
    setQ('')
    setSel(0)
    setOpen(true)
  }
  const close = () => setOpen(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        openPalette()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [open])

  if (!open) {
    return (
      <div class="search" role="button" tabIndex={0} aria-label="Search runs, bots and settings" onClick={openPalette} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), openPalette())}>
        <Icon name="search" />
        <span class="lbl">Search runs, bots and settings</span>
        <span class="kbd">⌘K</span>
      </div>
    )
  }

  const results = filterItems(items, q)
  const open_ = (i: PaletteItem) => {
    close()
    go(i.key)
  }

  return (
    <div class="scrim" style={{ placeItems: 'start center' }} onClick={close}>
      <div class="palette" role="dialog" aria-label="Command line" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          placeholder="Search a page, run, bot or setting…"
          aria-label="Search or run a command"
          autocomplete="off"
          spellcheck={false}
          value={q}
          onInput={(e) => {
            setQ((e.target as HTMLInputElement).value)
            setSel(0)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault()
              close()
            } else if (e.key === 'ArrowDown') {
              e.preventDefault()
              setSel((s) => Math.min(results.length - 1, s + 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setSel((s) => Math.max(0, s - 1))
            } else if (e.key === 'Enter' && results[sel]) {
              e.preventDefault()
              open_(results[sel])
            }
          }}
        />
        <div class="res" role="listbox">
          {results.length ? (
            results.map((r, i) => (
              <a
                href={'#/' + r.key}
                role="option"
                class={i === sel ? 'on' : ''}
                aria-selected={i === sel}
                onClick={(e) => {
                  e.preventDefault()
                  open_(r)
                }}
                onMouseEnter={() => setSel(i)}
              >
                <Icon name={r.icon} />
                <span class="grow ellipsis">{r.label}</span>
                <span class="pal-kind">page</span>
              </a>
            ))
          ) : (
            <div class="empty small pal-empty">
              <b>No results for "{q}".</b> Search only covers pages this phase — there is no run, bot or secret lookup yet.
            </div>
          )}
        </div>
        <div class="pal-foot muted tiny">
          <span><span class="kbd">↑↓</span> move</span>
          <span><span class="kbd">↵</span> open</span>
          <span><span class="kbd">esc</span> close</span>
        </div>
      </div>
    </div>
  )
}
