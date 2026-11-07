// The app's shell: rail (brand + nav) and top strip, ported from the
// prototype's `UI.boot`/`UI.navHTML` (js/ui.js) — desktop only, no phone tab
// bar (the plan: "desktop window size only"). `.shell`, `.rail`, `.strip`,
// `.main` and the nav markup are the prototype's own classes so
// app.css/daylight.css style this unmodified.
import type { ComponentChildren, JSX } from 'preact'
import { Icon } from './icons'
import { attention, conversations } from '../state'
import type { Conversation } from '../api/types'
import { route, screen } from '../router'

function unreadTotal(): number {
  return conversations.value.reduce((n: number, c: Conversation) => n + c.unread, 0)
}

const NAV: Array<[string, string, string, () => number]> = [
  ['needs', 'Needs you', 'hand', () => attention.value.length],
  ['chat', 'Chat', 'message', unreadTotal],
  ['bots', 'Bots', 'bots', () => 0],
]

function Count(props: { n: number }): JSX.Element | null {
  if (!props.n) return null
  return (
    <span class="count hot" aria-label={`${props.n}`}>
      {props.n}
    </span>
  )
}

export function Shell(props: { children: ComponentChildren }): JSX.Element {
  const current = screen() || 'needs'
  void route.value // subscribe to route changes so the rail re-renders its "on" state
  return (
    <div class="shell">
      <aside class="rail">
        <a class="brand" href="#/needs" aria-label="conductor: home">
          <img src="/logo/logo-mark.svg" alt="" width={24} height={24} class="logo-light" />
          <img src="/logo/logo-mark-dark.svg" alt="" width={24} height={24} class="logo-dark" />
          <span class="wordmark">conductor</span>
        </a>
        <div class="nav-group">
          <nav class="nav">
            {NAV.map(([key, label, icon, count]) => (
              <a href={`#/${key}`} data-nav={key} class={current === key ? 'on' : ''} aria-current={current === key ? 'page' : undefined}>
                <Icon name={icon} />
                <span class="lbl">{label}</span>
                <Count n={count()} />
              </a>
            ))}
          </nav>
        </div>
      </aside>
      <header class="strip">
        <div style="flex:1" />
        <div style="display:contents">
          <a class="cell" href="#/settings">
            <span class="avatar" style="width:28px;height:28px;border-radius:50%;display:inline-flex;align-items:center;justify-content:center;background:var(--color-bg-sunken)">
              <Icon name="users" />
            </span>
          </a>
        </div>
      </header>
      <main class="main" id="main" tabIndex={-1}>
        {props.children}
      </main>
    </div>
  )
}
