// Enrolment (design §4.10, §6.3): the page the CLI's one-time localhost link
// opens. The operator types the pairing code the CLI printed and adds either a
// passkey (named, on a chosen RP; a second passkey also confirms with an existing
// one, handled inside enroll()) or a password with an authenticator app.
// Localhost only — the API refuses it off localhost.
import type { ComponentChildren, JSX } from 'preact'
import { useState } from 'preact/hooks'
import { enrollRedeem, enroll, enrollPasswordBegin, enrollPasswordFinish, authState, type TOTPEnrollment } from '../api/auth'
import { passkeysUsable } from '../lib/webauthn'
import { GateMark, PasskeyGlyph } from '../components/GateChrome'
import { CopyBlock } from '../components/AuthBits'
import '../styles/auth.css'

// ticketFromURL reads the one-time secret from the URL fragment (#t=…), where the
// CLI puts it so it never reaches a log.
function ticketFromURL(): string {
  const h = location.hash.startsWith('#') ? location.hash.slice(1) : location.hash
  return new URLSearchParams(h).get('t') ?? ''
}

function Card({ children }: { children: ComponentChildren }): JSX.Element {
  return (
    <div class="gate">
      <div class="gate-card">
        <div class="gate-brand">
          <GateMark />
          <span class="wordmark">conductor</span>
        </div>
        {children}
      </div>
    </div>
  )
}

export function Enroll(): JSX.Element {
  const usable = passkeysUsable()
  const [kind, setKind] = useState<'passkey' | 'password'>(usable ? 'passkey' : 'password')
  const [code, setCode] = useState('')
  const [name, setName] = useState('this device')
  const [rp, setRP] = useState<'tailnet' | 'localhost'>('tailnet')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [done, setDone] = useState<'' | 'passkey' | 'password'>('')
  // Password path: the redeemed ceremony and the authenticator to scan.
  const [ceremony, setCeremony] = useState('')
  const [totp, setTOTP] = useState<TOTPEnrollment | null>(null)
  const [totpRequired, setTOTPRequired] = useState(true)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [otp, setOtp] = useState('')
  const t = ticketFromURL()

  const run = async (fn: () => Promise<void>) => {
    setErr('')
    if (!t) {
      setErr('This enrolment link is missing its token. Run conductor dashboard enroll for a new one.')
      return
    }
    setBusy(true)
    try {
      await fn()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const onPasskey = () =>
    run(async () => {
      const redeemed = await enrollRedeem(t, code.trim())
      await enroll(redeemed.ceremony_id, rp, name.trim() || 'new passkey', redeemed.existing)
      setDone('passkey')
    })

  const onPasswordStart = () =>
    run(async () => {
      const redeemed = await enrollRedeem(t, code.trim())
      const r = await enrollPasswordBegin(redeemed.ceremony_id)
      setCeremony(redeemed.ceremony_id)
      setTOTP(r.totp)
      setTOTPRequired(r.totp_required)
    })

  const onPasswordFinish = () =>
    run(async () => {
      if (pw !== pw2) throw new Error("The two passwords don't match.")
      await enrollPasswordFinish(ceremony, pw, otp.trim())
      setDone('password')
    })

  if (done) {
    return (
      <Card>
        <h1>{done === 'passkey' ? 'Passkey enrolled' : 'Password set'}</h1>
        <p class="gate-sub">
          {done === 'passkey'
            ? `“${name}” is enrolled${rp === 'tailnet' ? ' for this box and your tailnet' : ' for this box'}. You can sign in with it now.`
            : 'You can sign in with your password and a code from your authenticator app.'}
        </p>
        <a class="btn primary lg block" href="/#/overview" onClick={() => history.replaceState(null, '', '/#/overview')}>
          Go to the dashboard
        </a>
      </Card>
    )
  }

  if (totp) {
    return (
      <Card>
        <h1>Set a password</h1>
        <p class="gate-sub">
          Scan this with your authenticator app (or type the key), then choose a password and enter the app's current
          code.
        </p>
        <div class="totp-setup">
          <img class="totp-qr" src={totp.qr} alt="QR code for your authenticator app" width={180} height={180} />
          <CopyBlock text={totp.secret} label="Key" testid="enroll-totp-secret" />
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void onPasswordFinish()
          }}
        >
          <label class="gate-field">
            <span class="gate-group-label">Password (at least 12 characters)</span>
            <input type="password" autocomplete="new-password" value={pw} data-testid="enroll-password" onInput={(e) => setPw((e.target as HTMLInputElement).value)} />
          </label>
          <label class="gate-field">
            <span class="gate-group-label">Password again</span>
            <input type="password" autocomplete="new-password" value={pw2} data-testid="enroll-password2" onInput={(e) => setPw2((e.target as HTMLInputElement).value)} />
          </label>
          <label class="gate-field">
            <span class="gate-group-label">{totpRequired ? 'Code from the app' : 'Code from the app (optional)'}</span>
            <input class="mono" inputMode="numeric" autocomplete="one-time-code" maxLength={7} value={otp} data-testid="enroll-totp-code" onInput={(e) => setOtp((e.target as HTMLInputElement).value)} />
          </label>
          <button class="btn primary lg block" type="submit" disabled={busy || !pw} data-testid="enroll-password-submit">
            {busy ? 'Saving…' : 'Set password'}
          </button>
        </form>
        {err ? (
          <p class="gate-err" role="alert" data-testid="enroll-error">
            {err}
          </p>
        ) : null}
      </Card>
    )
  }

  return (
    <Card>
      <h1>Enrol</h1>
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
      <div class="gate-field" role="group" aria-label="Add">
        <span class="gate-group-label">Add</span>
        <div class="seg-choice">
          <button type="button" class={kind === 'passkey' ? 'on' : ''} aria-pressed={kind === 'passkey'} disabled={!usable} onClick={() => setKind('passkey')}>
            A passkey
          </button>
          <button type="button" class={kind === 'password' ? 'on' : ''} aria-pressed={kind === 'password'} onClick={() => setKind('password')} data-testid="enroll-kind-password">
            A password + authenticator
          </button>
        </div>
      </div>
      {kind === 'passkey' ? (
        <>
          <label class="gate-field">
            <span>Name this passkey</span>
            <input value={name} data-testid="enroll-name" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
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
          <p class="gate-callout">Every enrolment is audited. A second passkey also asks you to confirm with one you already have.</p>
          <button class="btn primary lg block" type="button" onClick={onPasskey} disabled={busy} aria-disabled={busy} data-testid="enroll-button">
            <PasskeyGlyph />
            {busy ? 'Confirm on your device…' : 'Create passkey'}
          </button>
        </>
      ) : (
        <>
          <p class="gate-callout">
            A password signs in only together with a code from an authenticator app, unless the config explicitly allows a
            password alone. Every enrolment is audited.
          </p>
          <button class="btn primary lg block" type="button" onClick={onPasswordStart} disabled={busy} data-testid="enroll-password-start">
            {busy ? 'Checking…' : 'Continue'}
          </button>
        </>
      )}
      {!usable ? <p class="acc-note">Passkeys need a secure context; this page isn't one.</p> : null}
      {err ? (
        <p class="gate-err" role="alert" data-testid="enroll-error">
          {err}
        </p>
      ) : null}
    </Card>
  )
}
