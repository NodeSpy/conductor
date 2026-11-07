// The app shell: rail (desktop/tablet), top strip, phone header + tab bar, and
// the page container the current screen renders into. Structure and classes
// are the prototype's (js/ui.js UI.boot/UI.navHTML/UI.hostCard, css/app.css):
// the rail full height with the brand on top and the host card at its foot,
// the quiet top bar, the phone header naming the route, and the five-item tab
// bar. Settings still lists Config, Logs & audit and Access as their own
// items (a later phase folds them into System, design §5.4) — a deliberate,
// noted departure from the approved nav for now.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { conn, status, needItems } from '../state'
import { route, screen } from '../router'
import { Icon } from './Icon'
import { StatusMark } from './StatusMark'

type NavItem = { key: string; label: string; icon: string; count?: () => number; hot?: boolean }

const NAV_GROUPS: { label: string; items: NavItem[] }[] = [
  {
    label: '',
    items: [
      { key: '', label: 'Home', icon: 'overview' },
      { key: 'chat', label: 'Chat', icon: 'message' },
      { key: 'attention', label: 'Needs you', icon: 'attention', count: () => needItems().length, hot: true },
      { key: 'activity', label: 'Activity', icon: 'live' },
      { key: 'tickets', label: 'Tickets', icon: 'ticket' },
      { key: 'workflows', label: 'Workflows', icon: 'workflows' },
      { key: 'bots', label: 'Bots', icon: 'bots' },
      { key: 'browser', label: 'Browsers', icon: 'globe' },
    ],
  },
  {
    label: 'Settings',
    items: [
      { key: 'connections', label: 'Plugins', icon: 'connectors' },
      { key: 'packs', label: 'Packs', icon: 'extensions' },
      { key: 'secrets', label: 'Secrets', icon: 'secrets' },
      { key: 'system', label: 'System', icon: 'settings' },
      // Interim, pending the System merge (design §5.4 phase): their own rail
      // entries so they stay reachable.
      { key: 'config', label: 'Config', icon: 'settings' },
      { key: 'audit', label: 'Logs & audit', icon: 'live' },
      { key: 'security', label: 'Access', icon: 'secrets' },
    ],
  },
]

// NAV_ALIAS: pages that live inside another nav destination highlight that
// destination (a run's page highlights Activity, a bot's page highlights Bots).
const NAV_ALIAS: Record<string, string> = { runs: 'activity', teams: 'bots', access: 'security' }

function navKeyFor(cur: string): string {
  return NAV_ALIAS[cur] ?? cur
}

const TAB_ITEMS: { key: string; label: string; icon: string; count?: () => number }[] = [
  { key: '', label: 'Home', icon: 'overview' },
  { key: 'attention', label: 'Needs you', icon: 'attention', count: () => needItems().length },
  { key: 'chat', label: 'Chat', icon: 'message' },
  { key: 'activity', label: 'Activity', icon: 'live' },
  { key: 'more', label: 'More', icon: 'more' },
]

const PAGE_TITLES: Record<string, string> = {
  '': 'Home',
  chat: 'Chat',
  attention: 'Needs you',
  activity: 'Activity',
  tickets: 'Tickets',
  workflows: 'Workflows',
  bots: 'Bots',
  browser: 'Browsers',
  connections: 'Plugins',
  packs: 'Packs',
  secrets: 'Secrets',
  system: 'System',
  config: 'Config',
  audit: 'Logs & audit',
  security: 'Access',
  more: 'More',
}

function Mark(): JSX.Element {
  // Fermata, strengthened (assets/logo/mark-inline.html): the one inline copy,
  // token-coloured so it follows the theme. Swapping the logo is a file drop
  // over design/dashboard-prototype/assets/logo/, ported here verbatim.
  return (
    <span class="logo-slot">
      <svg class="logo-mark" viewBox="2.5 6 27 20" aria-hidden="true" focusable="false">
        <g fill="var(--color-logo-ink, var(--color-text-primary))">
          <path d="M2.5 21a13.5 15 0 0 1 27 0h-5a8.5 9.5 0 0 0-17 0z" />
        </g>
        <g fill="var(--color-accent-default)">
          <circle cx="16" cy="21" r="5" />
        </g>
      </svg>
    </span>
  )
}

function Lockup(): JSX.Element {
  return (
    <span class="lockup" role="img" aria-label="conductor">
      <Mark />
      <span class="wordmark" aria-hidden="true">conductor</span>
    </span>
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
  // On a phone the rail is hidden entirely (the tab bar replaces it); there is
  // no fold-out nav drawer in the approved design, so there is nothing to
  // open or close here beyond the avatar's own small menu.
  const cur = navKeyFor(screen())
  const title = PAGE_TITLES[cur] ?? 'conductor'
  // Re-render the chrome (badge counts, the host card) on every route change
  // and on each SSE tick by reading the signals the counts depend on.
  void route.value
  const need = needItems().length

  return (
    <div class="shell">
      <a class="skip" href="#main">Skip to the page</a>
      <aside class="rail" aria-label="Primary">
        <a class="brand" href="#/" aria-label="conductor: home">
          <Lockup />
        </a>
        <div id="rail-nav">
          {NAV_GROUPS.map((g) => (
            <div class="nav-group">
              {g.label ? <span class="grp">{g.label}</span> : null}
              <nav class="nav">
                {g.items.map((n) => {
                  const on = cur === n.key
                  const n2 = n.count ? n.count() : 0
                  return (
                    <a href={'#/' + n.key} data-nav={n.key} class={on ? 'on' : ''} aria-current={on ? 'page' : undefined}>
                      <Icon name={n.icon} />
                      <span class="lbl">{n.label}</span>
                      {n2 ? <span class={'count' + (n.hot ? ' hot' : '')}>{n2}</span> : null}
                    </a>
                  )
                })}
              </nav>
            </div>
          ))}
        </div>
        <div class="rail-host">
          <HostCard />
        </div>
      </aside>

      <header class="strip">
        <a class="brand ph-brand" href="#/" aria-label="conductor: home">
          <Mark />
          <b>{title}</b>
        </a>
        <div class="spacer" />
        <ThemeCell theme={theme} cycle={cycle} />
        <button type="button" class="cell op-btn" aria-label="Account">
          <span class="avatar">OP</span>
        </button>
      </header>

      <main class="main" id="main" tabindex={-1}>
        <div class="page dl-page">{children}</div>
      </main>

      <nav class="tabbar" aria-label="Primary">
        {TAB_ITEMS.map((t) => {
          const on = cur === t.key
          const n = t.count ? t.count() : 0
          return (
            <a href={'#/' + t.key} data-nav={t.key} class={on ? 'on' : ''}>
              <Icon name={t.icon} />
              <span>{t.label}</span>
              {n ? <span class="count hot">{n}</span> : null}
            </a>
          )
        })}
      </nav>
      {/* keep the aggregator warm so the badge above reads true */}
      <span hidden aria-hidden="true">{need}</span>
    </div>
  )
}

// ThemeCell is the quiet top-bar theme cycle (auto → light → dark), plus a
// small live-connection word when the event stream isn't live (the approved
// design has no place for this signal; it is surfaced only when it matters).
function ThemeCell({ theme, cycle }: { theme: 'light' | 'dark' | 'auto'; cycle: () => void }): JSX.Element {
  const c = conn.value
  return (
    <>
      {c !== 'live' ? (
        <span class="cell" data-testid="conn-bar" title="Event stream">
          {c === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'}
        </span>
      ) : null}
      <button type="button" class="cell" onClick={cycle} title="Switch theme">
        <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
      </button>
    </>
  )
}

// HostCard is the machine card at the rail's foot (More on a phone): how the
// machine is, in one word, and quietly its version — the one place daemon
// health shows (UI.hostCard). No update block yet: UpdateInfo doesn't carry
// whether an update needs a person (design gap, phase 7).
function HostCard(): JSX.Element {
  const st = status.value
  const [host, setHost] = useState('this machine')
  useEffect(() => setHost(location.hostname || 'this machine'), [])
  if (!st) return <div class="host" />
  const paused = st.paused
  const mark = paused ? 'wait' : 'ok'
  const line = paused ? 'Paused: nothing new starts' : `Healthy · up ${upFor(st.uptime_sec)}`
  return (
    <a class={'host' + (paused ? ' need' : '')} href="#/system" aria-label={`${host}: ${line}`}>
      <StatusMark s={mark} mkOnly label={paused ? 'needs a look' : 'healthy'} />
      <span class="who">
        <b>{host}</b>
        <span id="host-line">{line}</span>
      </span>
    </a>
  )
}

function upFor(sec: number): string {
  if (sec < 3600) return `${Math.max(1, Math.round(sec / 60))} min`
  if (sec < 86400) return `${Math.round(sec / 3600)} h`
  return `${Math.round(sec / 86400)} d`
}
