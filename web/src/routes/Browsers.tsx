// Browsers (design §4.5): the live agent sessions, and the shared tab bar across
// Sessions · Logins · Personal details · Payment cards (js/screens/browser.js
// UI.browserTabs). This component is the entry router for every #/browser/*
// route, so app.tsx keeps one case.
//
// Left out this phase (prototype-only, no backend yet): the "Settings per
// identity" table (browser.identities' allowlist/logins/credentials/evaluate —
// there is no dashboard read model for browser identity config); and the live
// view's back/forward/reload controls (the daemon's Input only forwards mouse
// and keyboard events, not navigation commands). "Register a passkey" itself
// (conductor browser passkey register) IS a CLI-only command with no dashboard
// endpoint to drive its browser ceremony — the button below is honest about
// that: it names the command rather than faking a dashboard flow that would
// not work.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg } from '../router'
import { browser, who, type BrowserSession } from '../api/browser'
import { BrowserSessionView } from './BrowserSession'
import { Logins, AddLogin } from './Logins'
import { Details } from './Details'
import { Cards } from './Cards'
import { Purchase } from './Purchase'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { TabsBar, type TabItem } from '../components/TabsBar'
import { humanError } from '../api/errors'

export type BrowserTab = 'sessions' | 'logins' | 'details' | 'cards'

// counts is a tiny, best-effort fetch of each tab's size (design: the tab bar
// shows a count beside each, UI.browserTabs). A failure on one just leaves
// that tab's count off; it never blocks the page it's drawn on.
function useBrowserCounts(): Partial<Record<BrowserTab, number>> {
  const [counts, setCounts] = useState<Partial<Record<BrowserTab, number>>>({})
  useEffect(() => {
    browser.sessions().then((r) => setCounts((c) => ({ ...c, sessions: (r.sessions || []).length }))).catch(() => {})
    browser.logins().then((r) => setCounts((c) => ({ ...c, logins: (r.logins || []).length }))).catch(() => {})
    browser.wallet().then((r) => setCounts((c) => ({ ...c, details: (r.profiles || []).length, cards: (r.cards || []).length }))).catch(() => {})
  }, [])
  return counts
}

export function BrowserTabs({ active }: { active: BrowserTab }): JSX.Element {
  const counts = useBrowserCounts()
  const tabs: TabItem[] = [
    { id: 'sessions', label: 'Sessions', href: '#/browser', count: counts.sessions },
    { id: 'logins', label: 'Logins', href: '#/browser/logins', count: counts.logins },
    { id: 'details', label: 'Personal details', href: '#/browser/details', count: counts.details },
    { id: 'cards', label: 'Payment cards', href: '#/browser/cards', count: counts.cards },
  ]
  return <TabsBar items={tabs} active={active} ariaLabel="Browsers" />
}

// BrowserCrumbs: "Browsers › <tab>" above every tab page but the root
// (Sessions, which is Browsers' own landing page and carries no crumb, same
// as the prototype's browser.js UI.head call).
export function BrowserCrumbs({ here }: { here: string }): JSX.Element {
  return (
    <nav class="dl-crumbs" aria-label="Where you are">
      <a href="#/browser">Browsers</a>
      <span class="sep" aria-hidden="true">›</span>
      <span class="cur" aria-current="page">{here}</span>
    </nav>
  )
}

// RegisterPasskey: a real entry point — honest about what it can do. There is
// no dashboard endpoint for `conductor browser passkey register` (it drives a
// live WebAuthn ceremony in a real browser window); this names the command
// instead of faking a flow that can't work over HTTP.
export function RegisterPasskey(): JSX.Element {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button class="btn" type="button" onClick={() => setOpen(true)}>
        <Icon name="plus" />Register a passkey
      </button>
      {open ? (
        <div class="scrim" onClick={() => setOpen(false)}>
          <div class="modal" role="dialog" aria-label="Register a passkey" onClick={(e) => e.stopPropagation()}>
            <h2>Register a passkey</h2>
            <div class="modal-body stack">
              <p>A dedicated passkey for one login, registered live on its own site — conductor drives a real sign-in so the site itself creates it. That needs a real browser window, so it runs from the machine conductor is on, not from here:</p>
              <p class="mono wrap-any">conductor browser passkey register CREDENTIAL</p>
              <p class="dl-sub">It is revocable on the site without touching any passkey of your own.</p>
            </div>
            <div class="modal-foot row-actions">
              <button class="btn primary" type="button" onClick={() => setOpen(false)}>Done</button>
            </div>
          </div>
        </div>
      ) : null}
    </>
  )
}

// Browsers routes #/browser/* to its screen.
export function Browsers(): JSX.Element {
  const s1 = seg(1)
  switch (s1) {
    case '':
      return <Sessions />
    case 'logins':
      return <Logins />
    case 'add':
      return <AddLogin />
    case 'details':
      return <Details />
    case 'cards':
      return <Cards />
    case 'purchases':
      return seg(2) ? <Purchase id={seg(2)} /> : <Cards />
    default:
      // a session id
      return <BrowserSessionView id={s1} />
  }
}

function Sessions(): JSX.Element {
  const [sessions, setSessions] = useState<BrowserSession[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    browser.sessions().then((r) => setSessions(r.sessions || [])).catch((e) => setErr(humanError(e)))
  }, [])

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <h1>Browsers</h1>
          <p class="lede-2">conductor's Chromium: one per agent session, sandboxed.</p>
        </div>
        <div class="actions">
          <a class="btn" href="#/browser/add"><Icon name="key" />Add a login</a>
          <RegisterPasskey />
        </div>
      </div>
      <BrowserTabs active="sessions" />

      {err ? <div class="banner err">{err}</div> : null}
      {sessions === null ? (
        <p class="muted">{err ? 'Nothing to show until conductor answers.' : 'Loading live sessions…'}</p>
      ) : sessions.length === 0 ? (
        <EmptyState title="No browser open right now" testid="sessions-empty">
          <p>When an agent opens conductor's browser, its live session shows here — watch it, or take over.</p>
        </EmptyState>
      ) : (
        <div class="grid g3">
          {sessions.map((s) => (
            <a class="panel btile" href={`#/browser/${encodeURIComponent(s.id)}`} data-session={s.id} key={s.id}>
              <div class="thumb">
                <img src={browser.peekURL(s.id)} alt="" loading="lazy" onError={(e) => ((e.target as HTMLImageElement).style.display = 'none')} />
                {s.leased_by ? <span class="ctrl-banner wait">You have control</span> : null}
              </div>
              <div class="panel-body stack tight">
                <div class="row">
                  <span class="ellipsis" style="font-weight:600">{who(s.identity)}</span>
                  <span class="dl-sub" style="margin-left:auto">{s.profile}</span>
                </div>
                <div class="dl-sub ellipsis">
                  {s.agent ? s.agent : s.identity}
                  {s.requester ? ` · asked by ${s.requester}` : ''}
                </div>
              </div>
            </a>
          ))}
        </div>
      )}
    </div>
  )
}
