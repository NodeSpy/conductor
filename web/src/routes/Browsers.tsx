// Browsers (design §4.5): the live agent sessions, and the shared tab bar across
// Sessions · Logins · Personal details · Payment cards. This component is the
// entry router for every #/browser/* route, so app.tsx keeps one case.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg } from '../router'
import { browser, who, type BrowserSession } from '../api/browser'
import { BrowserSessionView } from './BrowserSession'
import { Logins, AddLogin } from './Logins'
import { Details } from './Details'
import { Cards } from './Cards'
import { Purchase } from './Purchase'

export type BrowserTab = 'sessions' | 'logins' | 'details' | 'cards'

export function BrowserTabs({ active }: { active: BrowserTab }): JSX.Element {
  const tabs: { id: BrowserTab; label: string; href: string }[] = [
    { id: 'sessions', label: 'Sessions', href: '#/browser' },
    { id: 'logins', label: 'Logins', href: '#/browser/logins' },
    { id: 'details', label: 'Personal details', href: '#/browser/details' },
    { id: 'cards', label: 'Payment cards', href: '#/browser/cards' },
  ]
  return (
    <nav class="tabs" role="tablist" aria-label="Browsers">
      {tabs.map((t) => (
        <a class={'tab' + (active === t.id ? ' on' : '')} role="tab" aria-selected={active === t.id} href={t.href} data-tab={t.id}>
          {t.label}
        </a>
      ))}
    </nav>
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
    browser.sessions().then((r) => setSessions(r.sessions || [])).catch((e) => setErr(String(e)))
  }, [])

  return (
    <div class="col browsers">
      <div class="page-head">
        <h1 class="screen-title serif">Browsers</h1>
        <div class="row-actions">
          <a class="btn ghost sm" href="#/browser/add">Add a login</a>
          <a class="btn ghost sm" href="#/browser/logins">Register a passkey</a>
        </div>
      </div>
      <p class="muted">conductor's Chromium: one per agent session, sandboxed, egress through the allowlist proxy only.</p>
      <BrowserTabs active="sessions" />

      {err ? <div class="banner err">{err}</div> : null}
      {sessions === null ? (
        <p class="muted">Loading live sessions…</p>
      ) : sessions.length === 0 ? (
        <div class="empty">
          <h3>No browser open right now</h3>
          <p class="muted">When an agent opens conductor's browser, its live session shows here — watch it, or take over.</p>
        </div>
      ) : (
        <div class="btiles">
          {sessions.map((s) => (
            <a class="panel btile" href={`#/browser/${encodeURIComponent(s.id)}`} data-session={s.id}>
              <div class="btile-thumb">
                <img src={browser.peekURL(s.id)} alt="" loading="lazy" onError={(e) => ((e.target as HTMLImageElement).style.display = 'none')} />
                {s.leased_by ? <span class="ctrl-banner wait">You have control</span> : null}
              </div>
              <div class="btile-body">
                <div class="row">
                  <span class="btile-id ellipsis">{s.identity}</span>
                  <span class="dl-sub" style="margin-left:auto">{s.profile}</span>
                </div>
                <p class="facts muted">
                  Signs in as {who(s.identity)}
                  {s.agent ? ` · ${s.agent}` : ''}
                  {s.requester ? ` · asked by ${s.requester}` : ''}
                </p>
              </div>
            </a>
          ))}
        </div>
      )}
    </div>
  )
}
