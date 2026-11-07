// The app shell: rail (desktop/tablet), top strip, phone header + tab bar, and
// the page container the current screen renders into. Structure and classes
// are the prototype's (js/ui.js UI.boot/UI.navHTML/UI.hostCard, css/app.css):
// the rail full height with the brand on top and the host card at its foot,
// the quiet top bar (search, then the avatar), the phone header naming the
// route, and the five-item tab bar.
//
// The rail's Settings group matches the prototype's own four items exactly
// (Plugins/Packs/Secrets/System). Config, Safety, Updates, Logs & audit and
// Access are System's own sub-navigation (CONFORMANCE, shell note), not
// separate rail entries; their old routes (#/config, #/safety, #/updates,
// #/audit, #/security) redirect into the matching System tab (app.tsx).
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { conn, status, needItems, chatUnread } from '../state'
import { route, screen, go } from '../router'
import { Icon } from './Icon'
import { StatusMark } from './StatusMark'
import { Palette, type PaletteItem } from './Palette'
import { usePhoneLayout } from '../lib/layout'
import { Menu, type MenuItem } from './Menu'
import { authState, signOut } from '../api/auth'
import { staged, loadConfig } from '../configStore'
import { themeSignal, setTheme, type Theme } from '../lib/theme'

type NavItem = { key: string; label: string; icon: string; count?: () => number; hot?: boolean }

const NAV_GROUPS: { label: string; items: NavItem[] }[] = [
  {
    label: '',
    items: [
      { key: '', label: 'Home', icon: 'overview' },
      { key: 'chat', label: 'Chat', icon: 'message', count: () => chatUnread.value, hot: true },
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
    ],
  },
]

// PALETTE_ITEMS: every screen reachable from the rail, the tab bar or More —
// the search affordance's index for this phase (see Palette.tsx's note).
const PALETTE_ITEMS: PaletteItem[] = [
  { key: '', label: 'Home', icon: 'overview' },
  { key: 'chat', label: 'Chat', icon: 'message' },
  { key: 'attention', label: 'Needs you', icon: 'attention' },
  { key: 'activity', label: 'Activity', icon: 'live' },
  { key: 'tickets', label: 'Tickets', icon: 'ticket' },
  { key: 'workflows', label: 'Workflows', icon: 'workflows' },
  { key: 'bots', label: 'Bots', icon: 'bots' },
  { key: 'browser', label: 'Browsers', icon: 'globe' },
  { key: 'connections', label: 'Plugins', icon: 'connectors' },
  { key: 'packs', label: 'Packs', icon: 'extensions' },
  { key: 'secrets', label: 'Secrets', icon: 'secrets' },
  { key: 'system', label: 'System', icon: 'settings' },
]

// NAV_ALIAS: pages that live inside another nav destination highlight that
// destination (a run's page highlights Activity, a bot's page highlights
// Bots; System's old standalone routes highlight System, which they redirect
// into — app.tsx's LEGACY_SYSTEM_TABS).
const NAV_ALIAS: Record<string, string> = {
  runs: 'activity',
  teams: 'bots',
  config: 'system',
  safety: 'system',
  updates: 'system',
  audit: 'system',
  security: 'system',
  access: 'system',
}

function navKeyFor(cur: string): string {
  return NAV_ALIAS[cur] ?? cur
}

const TAB_ITEMS: { key: string; label: string; icon: string; count?: () => number }[] = [
  { key: '', label: 'Home', icon: 'overview' },
  { key: 'attention', label: 'Needs you', icon: 'attention', count: () => needItems().length },
  { key: 'chat', label: 'Chat', icon: 'message', count: () => chatUnread.value },
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
  more: 'More',
  preferences: 'Preferences',
  glossary: 'Glossary',
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

export function Shell({ children }: { children: ComponentChildren }): JSX.Element {
  const theme = themeSignal.value
  // On a phone the rail is hidden entirely (the tab bar replaces it); there is
  // no fold-out nav drawer in the approved design — just the avatar's menu.
  const phone = usePhoneLayout()
  const cur = navKeyFor(screen())
  const title = PAGE_TITLES[cur] ?? 'conductor'
  // Re-render the chrome (badge counts, the host card) on every route change
  // and on each SSE tick by reading the signals the counts depend on.
  void route.value
  const need = needItems().length

  // The Settings group (Plugins/Packs/Secrets/System) is what More.tsx also
  // carries on a phone; CSS already hides the rest of the rail there, but
  // leaving this one group out of the DOM too keeps its links from being a
  // second, invisible match for a plain-text locator of More's own.
  const navGroups = phone ? NAV_GROUPS.filter((g) => !g.label) : NAV_GROUPS

  // Load the config once per app session so the top bar's staged-changes cell
  // (UI.refreshChrome's `${D.pending.length} not applied`) reads true from the
  // very first screen, not only after visiting Config or Secrets.
  useEffect(() => {
    void loadConfig()
  }, [])
  const pending = staged.value.length

  return (
    <div class="shell">
      <a class="skip" href="#main">Skip to the page</a>
      <aside class="rail" aria-label="Primary">
        <a class="brand" href="#/" aria-label="conductor: home">
          <Lockup />
        </a>
        <div id="rail-nav">
          {navGroups.map((g) => (
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
        <Palette items={PALETTE_ITEMS} />
        <div class="spacer" />
        {conn.value !== 'live' ? (
          <span class="cell" data-testid="conn-bar" title="Event stream">
            {conn.value === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'}
          </span>
        ) : null}
        {pending ? (
          <a class="cell staged" href="#/system/config">
            <StatusMark s="staged" word={`${pending} not applied`} />
          </a>
        ) : null}
        <AccountMenu theme={theme} />
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

// AccountMenu is the avatar button and its menu (prototype js/ui.js
// UI.act('op-menu')): who you are (links to Access), the theme toggle,
// Preferences, Show Get started (only when it's hidden), Glossary, and Lock.
// There is no separate theme toggle anywhere else in the top bar or the rail
// (CONFORMANCE); the account menu is the one place theme lives.
function AccountMenu({ theme }: { theme: Theme }): JSX.Element {
  const [open, setOpen] = useState(false)
  const [gsHidden, setGsHidden] = useState(() => localStorage.getItem('gs-hidden') === '1')
  const ref = useRef<HTMLButtonElement>(null)
  const operator = authState.value.operator || 'you'

  const dark = theme === 'dark' || (theme === 'auto' && matchMedia('(prefers-color-scheme: dark)').matches)

  const items: MenuItem[] = [
    { label: `Signed in as ${operator}`, icon: 'shield', onClick: () => go('system/access') },
    '-',
    {
      label: dark ? 'Light theme' : 'Dark theme',
      icon: dark ? 'sun' : 'moon',
      onClick: () => setTheme(dark ? 'light' : 'dark'),
    },
    { label: 'Preferences', icon: 'settings', onClick: () => go('preferences') },
    ...(gsHidden
      ? [{ label: 'Show Get started', icon: 'check', onClick: () => { localStorage.removeItem('gs-hidden'); setGsHidden(false); go('') } } as MenuItem]
      : []),
    { label: 'Glossary', icon: 'info', onClick: () => go('glossary') },
    '-',
    { label: 'Lock', icon: 'lock', onClick: () => void doLock() },
  ]

  async function doLock() {
    try {
      await signOut()
    } finally {
      authState.value = { signed_in: false }
      location.hash = ''
      location.assign('/signin')
    }
  }

  return (
    <>
      <button
        ref={ref}
        type="button"
        class="cell op-btn"
        aria-label={`${operator}: account and display`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span class="avatar">{operator.slice(0, 2).toUpperCase()}</span>
      </button>
      {open ? <Menu anchor={ref.current} items={items} onClose={() => setOpen(false)} label="Account and display" /> : null}
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
