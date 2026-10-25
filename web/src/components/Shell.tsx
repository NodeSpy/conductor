// The app shell: the left rail (logo, nav, theme), and the page container the
// current screen renders into. Nav items with a `to` route via the hash router;
// items still in a later phase are shown disabled so the chrome reads true to
// the approved design without pretending to route somewhere that isn't built.
import type { ComponentChildren, JSX } from 'preact'
import { conn } from '../state'
import { route } from '../router'

const NAV: { label: string; to?: string; soon?: boolean }[] = [
  { label: 'Overview', to: '' },
  { label: 'Workflows', soon: true },
  { label: 'Bots', soon: true },
  { label: 'Browsers', soon: true },
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
  return (
    <div class="app">
      <nav class="rail" aria-label="Primary">
        <div class="rail-top">
          <div class="brand">
            <Mark />
            <span class="wordmark">conductor</span>
          </div>
          <ul class="nav">
            {NAV.map((n) => {
              const active = n.to !== undefined && route.value === n.to
              return (
                <li>
                  {n.to !== undefined ? (
                    <a class={'nav-item' + (active ? ' on' : '')} aria-current={active ? 'page' : undefined} href={'#/' + n.to}>
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
          <ConnDot />
        </div>
      </nav>
      <main class="page">{children}</main>
    </div>
  )
}

function ConnDot(): JSX.Element {
  const c = conn.value
  const label = c === 'live' ? 'Live' : c === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'
  return (
    <span class="conn" data-conn={c} title={`Event stream: ${label}`}>
      <span class="conn-dot" />
      {label}
    </span>
  )
}
