// Enrolment (design §4.10, §6.3): the page the CLI's one-time localhost link
// opens. The operator types the pairing code the CLI printed, names the passkey,
// chooses the RP, and registers. A second passkey additionally confirms with an
// existing one (handled inside enroll()). Localhost only — the API refuses it off
// localhost.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { enrollRedeem, enroll, authState } from '../api/auth'
import { webauthnSupported } from '../lib/webauthn'
import { GateMark, PasskeyGlyph } from '../components/GateChrome'
import '../styles/auth.css'

// ticketFromURL reads the one-time secret from the URL fragment (#t=…), where the
// CLI puts it so it never reaches a log.
function ticketFromURL(): string {
  const h = location.hash.startsWith('#') ? location.hash.slice(1) : location.hash
  return new URLSearchParams(h).get('t') ?? ''
}

export function Enroll(): JSX.Element {
  const [code, setCode] = useState('')
  const [name, setName] = useState('this device')
  const [rp, setRP] = useState<'tailnet' | 'localhost'>('tailnet')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [done, setDone] = useState(false)
  const t = ticketFromURL()
  const supported = webauthnSupported()

  const onEnroll = async () => {
    setErr('')
    if (!t) {
      setErr('This enrolment link is missing its token. Run conductor dashboard enroll for a new one.')
      return
    }
    setBusy(true)
    try {
      const redeemed = await enrollRedeem(t, code.trim())
      await enroll(redeemed.ceremony_id, rp, name.trim() || 'new passkey', redeemed.existing)
      setDone(true)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (done) {
    return (
      <div class="gate">
        <div class="gate-card">
          <div class="gate-brand">
            <GateMark />
            <span class="wordmark">conductor</span>
          </div>
          <h1>Passkey enrolled</h1>
          <p class="gate-sub">
            “{name}” is enrolled{rp === 'tailnet' ? ' for this box and your tailnet' : ' for this box'}. You can sign in
            with it now.
          </p>
          <a class="btn primary lg block" href="/#/overview" onClick={() => history.replaceState(null, '', '/#/overview')}>
            Go to the dashboard
          </a>
        </div>
      </div>
    )
  }

  return (
    <div class="gate">
      <div class="gate-card">
        <div class="gate-brand">
          <GateMark />
          <span class="wordmark">conductor</span>
        </div>
        <h1>Enrol a passkey</h1>
        <p class="gate-sub">
          You opened the one-time link <span class="mono">conductor dashboard enroll</span> printed. It works once, on
          localhost, for 10 minutes.
        </p>
        <div class="gate-code">
          $ conductor dashboard enroll --operator {authState.value.operator || 'ops'}
          {'\n'}open this on THIS machine within 10m (single use)
          {'\n'}pairing code: NNN-NNN
        </div>
        <label class="gate-field">
          <span>Pairing code</span>
          <input
            class="mono"
            value={code}
            placeholder="000-000"
            inputMode="numeric"
            data-testid="enroll-code"
            onInput={(e) => setCode((e.target as HTMLInputElement).value)}
          />
        </label>
        <label class="gate-field">
          <span>Name this passkey</span>
          <input
            value={name}
            data-testid="enroll-name"
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
          />
        </label>
        <div class="gate-field" role="group" aria-label="Register on">
          <span class="gate-group-label">Register on</span>
          <div class="seg-choice">
            <button type="button" class={rp === 'tailnet' ? 'on' : ''} aria-pressed={rp === 'tailnet'} onClick={() => setRP('tailnet')}>
              Tailnet (this box + your phone)
            </button>
            <button type="button" class={rp === 'localhost' ? 'on' : ''} aria-pressed={rp === 'localhost'} onClick={() => setRP('localhost')}>
              Localhost only
            </button>
          </div>
        </div>
        <p class="gate-callout">
          Every enrolment is audited. A second passkey also asks you to confirm with one you already have.
        </p>
        <button
          class="btn primary lg block"
          type="button"
          onClick={onEnroll}
          disabled={busy || !supported}
          aria-disabled={busy || !supported}
          data-testid="enroll-button"
        >
          <PasskeyGlyph />
          {busy ? 'Confirm on your device…' : 'Create passkey'}
        </button>
        {!supported ? <p class="gate-err">This browser doesn't support passkeys.</p> : null}
        {err ? (
          <p class="gate-err" role="alert" data-testid="enroll-error">
            {err}
          </p>
        ) : null}
      </div>
    </div>
  )
}
