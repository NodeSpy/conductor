import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { Shell } from './components/Shell'
import { Overview } from './routes/Overview'
import { Secrets } from './routes/Secrets'
import { Packs } from './routes/Packs'
import { Connections } from './routes/Connections'
import { System } from './routes/System'
import { Signin } from './routes/Signin'
import { Enroll } from './routes/Enroll'
import { Activity } from './routes/Activity'
import { RunDetail } from './routes/RunDetail'
import { Workflows } from './routes/Workflows'
import { WorkflowDetail } from './routes/WorkflowDetail'
import { Builder } from './routes/Builder'
import { Bots } from './routes/Bots'
import { BotDetail } from './routes/BotDetail'
import { TeamDetail } from './routes/TeamDetail'
import { Chat } from './routes/Chat'
import { Tickets } from './routes/Tickets'
import { TicketDetail } from './routes/TicketDetail'
import { TicketNew } from './routes/TicketNew'
import { Approval } from './routes/Approval'
import { Browsers } from './routes/Browsers'
import { Attention } from './routes/Attention'
import { More } from './routes/More'
import { Preferences } from './routes/Preferences'
import { Glossary } from './routes/Glossary'
import { load, startLive } from './state'
import { route, screen, seg } from './router'
import { refreshState, authState } from './api/auth'
import { StepupDialog } from './components/StepupDialog'
import { PlainHTTPNotice } from './components/AuthBits'

export function App(): JSX.Element {
  // ready gates the first paint on knowing whether sign-in is required, so the
  // dashboard never flashes before the Sign-in gate (design §6).
  const [ready, setReady] = useState(false)
  useEffect(() => {
    void refreshState().finally(() => setReady(true))
  }, [])

  const path = typeof location !== 'undefined' ? location.pathname.replace(/\/$/, '') : ''

  // Enrolment is a bare, path-routed page the CLI link opens (/enroll#t=…); it
  // needs no session.
  if (path === '/enroll') return <Enroll />

  if (!ready) return <div class="gate" />

  const st = authState.value
  // Sign-in gate: show Sign-in when we're not signed in and either sign-in is
  // required (operators configured) or the operator asked for it (/signin). Once
  // signed in, fall through even on /signin so the dashboard shows.
  if (!st.signed_in && (st.required || path === '/signin')) return <Signin />

  return (
    <>
      {/* Outside Shell, not inside it: Shell wraps its children in
          `.page.dl-page` for the current route, and a page's own button
          sizing (`.dl-page .btn`) would otherwise win over the dialog's own
          44px tap target (auth.css's `.dialog-foot .btn`) by tie-break of
          equal specificity plus source order — exactly what made Cancel a
          36px target (authmethods.spec.ts). The dialog is a fixed overlay
          that positions itself over everything regardless of where it sits
          in the tree, so it loses nothing by sitting beside Shell instead of
          inside it. */}
      <Shell>
        {st.transport === 'http' ? <PlainHTTPNotice /> : null}
        <Authed />
      </Shell>
      <StepupDialog />
    </>
  )
}

// Authed is the signed-in (or open) app: it starts the live data once past the
// gate and routes the hash to a screen.
function Authed(): JSX.Element {
  useEffect(() => {
    void load()
    const stop = startLive()
    return stop
  }, [])
  return <Screen />
}

// LEGACY_SYSTEM_TABS: routes that used to be their own screens before System
// absorbed Config, Safety, Updates, Logs & audit and Access as its own
// sub-navigation (CONFORMANCE, shell note). Each keeps working as a redirect
// into the matching System tab so old links and bookmarks don't break.
const LEGACY_SYSTEM_TABS: Record<string, string> = {
  config: 'config',
  safety: 'safety',
  updates: 'updates',
  audit: 'logs/audit',
  security: 'access',
  access: 'access',
}

function Redirect({ to }: { to: string }): JSX.Element {
  useEffect(() => {
    location.replace('#/' + to)
  }, [to])
  return <div class="col" />
}

function Screen(): JSX.Element {
  // Read route.value so this re-renders on every hash change.
  void route.value
  const s = screen()
  if (s in LEGACY_SYSTEM_TABS) return <Redirect to={`system/${LEGACY_SYSTEM_TABS[s]}`} />
  switch (s) {
    case 'secrets':
      return <Secrets />
    case 'packs':
      return <Packs />
    case 'connections':
      return <Connections />
    case 'system':
      return <System />
    case 'activity':
      return <Activity />
    case 'runs':
      return seg(1) ? <RunDetail id={seg(1)} /> : <Activity />
    case 'workflows':
      if (seg(1) === 'new') return <Builder />
      return seg(1) ? <WorkflowDetail name={seg(1)} /> : <Workflows />
    case 'bots':
      // #/bots, #/bots/<bot>[/<tab>], #/bots/<bot>/proposals/<id> (the approval)
      if (seg(1) && seg(2) === 'proposals' && seg(3)) return <Approval bot={seg(1)} id={seg(3)} />
      if (seg(1)) return <BotDetail name={seg(1)} tab={seg(2)} />
      return <Bots />
    case 'teams':
      if (seg(1)) return <TeamDetail name={seg(1)} tab={seg(2)} />
      return <Bots />
    case 'chat':
      // #/chat or #/chat/bot:atlas (the conversation id carries its own colon)
      return <Chat convId={seg(1) || undefined} />
    case 'tickets':
      if (seg(1) === 'new') return <TicketNew />
      if (seg(1)) return <TicketDetail num={seg(1)} />
      return <Tickets />
    case 'browser':
      // #/browser, #/browser/<id|logins|add|details|cards>, #/browser/purchases/<id>
      return <Browsers />
    case 'attention':
      return <Attention />
    case 'more':
      return <More />
    case 'preferences':
      return <Preferences />
    case 'glossary':
      return <Glossary />
    default:
      return <Overview />
  }
}
