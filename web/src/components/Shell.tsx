// The app shell: the left rail (logo, nav, theme), and the page container the
// current screen renders into. Nav items with a `to` route via the hash router;
// items still in a later phase are shown disabled so the chrome reads true to
// the approved design without pretending to route somewhere that isn't built.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { conn } from '../state'
import { route, screen } from '../router'

const NAV: { label: string; to?: string; soon?: boolean }[] = [
  { label: 'Overview', to: '' },
  { label: 'Chat', to: 'chat' },
  { label: 'Activity', to: 'activity' },
  { label: 'Workflows', to: 'workflows' },
  { label: 'Bots', to: 'bots' },
  { label: 'Tickets', to: 'tickets' },
  { label: 'Browsers', to: 'browser' },
  { label: 'Config', to: 'config' },
  { label: 'Secrets', to: 'secrets' },
  { label: 'Packs', to: 'packs' },
  { label: 'Plugins & connections', to: 'connections' },
  { label: 'System', to: 'system' },
  { label: 'Logs & audit', to: 'audit' },
  { label: 'Access', to: 'security' },
]

function Mark(): JSX.Element {
  // The Daylight fermata mark (assets/logo/mark-inline.html), token-coloured so
  // it follows the theme. The logo is a swappable asset; this is the one copy.
  return (
    <svg class="logo-mark" viewBox="2.5 6 27 20" role="img" aria-label="conductor">
      <path fill="var(--color-text-primary)" d="M2.5 21a13.5 15 0 0 1 27 0h-5a8.5 9.5 0 0 0-17 0z" />
      <circle cx="16" cy="21" r="5" fill="var(--color-accent-default)" />
    </svg>
  )
}

function setTheme(next: 'light' | 'dark' | 'auto') {
  const root = document.documentElement
  if (next === 'auto') {
    root.removeAttribute('data-theme')
    localStorage.removeItem('theme')
  } else {
    root.setAttribute('data-theme', next)
    localStorage.setItem('theme', next)
  }
}

function currentTheme(): 'light' | 'dark' | 'auto' {
  const v = document.documentElement.getAttribute('data-theme')
  return v === 'light' || v === 'dark' ? v : 'auto'
}

export function Shell({ children }: { children: ComponentChildren }): JSX.Element {
  const theme = currentTheme()
  const cycle = () => setTheme(theme === 'auto' ? 'light' : theme === 'light' ? 'dark' : 'auto')
  // On a phone the nav folds behind a menu button so each page opens on its
  // content; it closes again on any nav tap (the current page's too), on a
  // route change and on Escape.
  const [open, setOpen] = useState(false)
  useEffect(() => setOpen(false), [route.value])
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [open])
  return (
    <div class="app">
      <nav class={'rail' + (open ? ' open' : '')} aria-label="Primary">
        <div class="rail-top">
          <div class="brand">
            <Mark />
            <span class="wordmark">conductor</span>
            <ConnDot where="bar" />
            <button
              class="menu-btn"
              type="button"
              aria-expanded={open}
              aria-controls="primary-nav"
              data-testid="menu-button"
              onClick={() => setOpen(!open)}
            >
              <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true">
                {open ? (
                  <path d="M5 5l10 10M15 5L5 15" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
                ) : (
                  <path d="M3 6h14M3 10h14M3 14h14" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
                )}
              </svg>
              {open ? 'Close' : 'Menu'}
            </button>
          </div>
          <ul class="nav" id="primary-nav">
            {NAV.map((n) => {
              // Highlight the section for its detail pages too (bots/:name keeps
              // Bots lit); Overview ('') only on the exact root.
              const cur = screen()
              const active = n.to !== undefined && (n.to === '' ? route.value === '' : cur === n.to)
              return (
                <li>
                  {n.to !== undefined ? (
                    <a class={'nav-item' + (active ? ' on' : '')} aria-current={active ? 'page' : undefined} href={'#/' + n.to} onClick={() => setOpen(false)}>
                      {n.label}
                    </a>
                  ) : (
                    <span class="nav-item soon" aria-disabled="true" title="Arrives in a later phase">
                      {n.label}
                    </span>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
        <div class="rail-foot">
          <button class="theme-btn" type="button" onClick={cycle} title="Switch theme">
            {theme === 'auto' ? 'Theme: auto' : theme === 'light' ? 'Theme: light' : 'Theme: dark'}
          </button>
          <ConnDot where="rail" />
        </div>
      </nav>
      <main class="page">{children}</main>
    </div>
  )
}

// ConnDot is the event stream's state. It sits in the rail's foot on a wide
// screen and in the top bar on a phone, where the foot is folded away.
function ConnDot({ where }: { where: 'rail' | 'bar' }): JSX.Element {
  const c = conn.value
  const label = c === 'live' ? 'Live' : c === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'
  return (
    <span class={'conn conn-' + where} data-conn={c} data-testid={'conn-' + where} title={`Event stream: ${label}`}>
      <span class="conn-dot" />
      {label}
    </span>
  )
}
