import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { Shell } from './components/Shell'
import { Overview } from './routes/Overview'
import { Config } from './routes/Config'
import { Secrets } from './routes/Secrets'
import { Packs } from './routes/Packs'
import { Connections } from './routes/Connections'
import { System } from './routes/System'
import { Audit } from './routes/Audit'
import { Signin } from './routes/Signin'
import { Enroll } from './routes/Enroll'
import { Access } from './routes/Access'
import { Activity } from './routes/Activity'
import { RunDetail } from './routes/RunDetail'
import { Workflows } from './routes/Workflows'
import { WorkflowDetail } from './routes/WorkflowDetail'
import { Builder } from './routes/Builder'
import { load, startLive } from './state'
import { route, screen, seg } from './router'
import { refreshState, authState } from './api/auth'

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
    <Shell>
      <Authed />
    </Shell>
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

function Screen(): JSX.Element {
  // Read route.value so this re-renders on every hash change.
  void route.value
  switch (screen()) {
    case 'config':
      return <Config />
    case 'secrets':
      return <Secrets />
    case 'packs':
      return <Packs />
    case 'connections':
      return <Connections />
    case 'system':
      return <System />
    case 'audit':
      return <Audit />
    case 'security':
    case 'access':
      return <Access />
    case 'activity':
      return <Activity />
    case 'runs':
      return seg(1) ? <RunDetail id={seg(1)} /> : <Activity />
    case 'workflows':
      if (seg(1) === 'new') return <Builder />
      return seg(1) ? <WorkflowDetail name={seg(1)} /> : <Workflows />
    default:
      return <Overview />
  }
}
