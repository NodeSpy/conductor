// Sign-in (design §4.10, §6.3): a bare passkey gate. One button runs a
// discoverable WebAuthn assertion; on success the daemon sets the session cookie
// and the app loads. Enrolment is a CLI-printed localhost link.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { signIn, authState } from '../api/auth'
import { webauthnSupported } from '../lib/webauthn'
import { GateMark, PasskeyGlyph } from '../components/GateChrome'
import '../styles/auth.css'

export function Signin(): JSX.Element {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const st = authState.value

  const onSignIn = async () => {
    setErr('')
    setBusy(true)
    try {
      await signIn()
      // The app re-renders off authState; drop any leftover path.
      if (location.pathname !== '/') history.replaceState(null, '', '/#/overview')
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const supported = webauthnSupported()

  return (
    <div class="gate">
      <div class="gate-card">
        <div class="gate-brand">
          <GateMark />
          <span class="wordmark">conductor</span>
        </div>
        <h1>Sign in</h1>
        <p class="gate-sub">
          Sign in with your passkey. Reachable only on this computer and your tailnet — no password, no other way in.
          {st.listener ? <span class="mono" style="display:block;margin-top:4px">on {st.listener}</span> : null}
        </p>
        <button
          class="btn primary lg block"
          type="button"
          onClick={onSignIn}
          disabled={busy || !supported}
          aria-disabled={busy || !supported}
          data-testid="signin-button"
        >
          <PasskeyGlyph />
          {busy ? 'Confirm on your device…' : 'Sign in with a passkey'}
        </button>
        {!supported ? <p class="gate-err">This browser doesn't support passkeys.</p> : null}
        {err ? (
          <p class="gate-err" role="alert" data-testid="signin-error">
            {err}
          </p>
        ) : null}
        <p class="acc-note" style="margin-top:14px">
          No passkey on this device? Run <span class="mono">conductor dashboard enroll</span> on the box for a one-time
          link.
        </p>
        <div class="gate-foot">
          <span>TLS · Tailscale cert</span>
        </div>
      </div>
    </div>
  )
}
