// Access (design §4.10; dashboard-auth-methods): how you sign in (passkeys, a
// password, an authenticator app), what's signed in now and how, and when
// conductor asks you to confirm. Adding or removing a sign-in credential asks
// for a confirmation first. The listeners, cookie and lockout numbers fold under
// Technical details.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import {
  access,
  removePasskey,
  endSession,
  signOutEverywhere,
  refreshState,
  setPassword,
  removePassword,
  totpBegin,
  totpConfirm,
  removeTOTP,
  addPasskeyHere,
  type AccessData,
  type SessionRow,
  type TOTPEnrollment,
} from '../api/auth'
import { passkeysUsable } from '../lib/webauthn'
import { CopyBlock, methodLabel, methodPhrase } from '../components/AuthBits'
import { ago } from '../lib/format'
import '../styles/auth.css'

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function sessionHow(s: SessionRow): string {
  const where =
    s.transport === 'http'
      ? `plain HTTP${s.host ? ' to ' + s.host : ''} (not encrypted)`
      : s.listener === 'tailscale'
        ? 'over your tailnet'
        : s.listener === 'localhost'
          ? 'on this computer'
          : `HTTPS${s.host ? ' to ' + s.host : ''}`
  return `${methodLabel[s.method] ?? s.method} · ${where}`
}

export function Access(): JSX.Element {
  const [data, setData] = useState<AccessData | null>(null)
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [pwOpen, setPwOpen] = useState(false)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [totp, setTOTP] = useState<TOTPEnrollment | null>(null)
  const [otp, setOtp] = useState('')
  const [busy, setBusy] = useState('')

  const reload = () => {
    access()
      .then((d) => {
        setData(d)
        setErr('')
      })
      .catch((e) => setErr(errText(e)))
  }
  useEffect(reload, [])

  if (err && !data) return <div class="banner err">Couldn't load Access: {err}</div>
  if (!data) return <div class="page">Loading…</div>

  // act runs a credential change (each asks for a confirmation first), then
  // reloads; a cancelled confirmation is not an error worth a banner.
  const act = async (what: string, fn: () => Promise<void>, ok: string) => {
    setBusy(what)
    setMsg('')
    setErr('')
    try {
      await fn()
      setMsg(ok)
      reload()
      void refreshState()
      return true
    } catch (e) {
      const t = errText(e)
      if (!/cancelled/.test(t)) setErr(t)
      return false
    } finally {
      setBusy('')
    }
  }

  const onRemovePasskey = async (id: string, name: string) => {
    if (!confirm(`Remove "${name}"? It can no longer sign in or approve, and any session it started ends now.`)) return
    await act('passkey', () => removePasskey(id).then(() => undefined), `Removed “${name}”.`)
  }
  const onEndSession = async (id: string) => {
    await act('session', () => endSession(id).then(() => undefined), 'Signed that session out.')
  }
  const onSignOutAll = async () => {
    if (!confirm('Sign out everywhere? Every device is signed out, this one too.')) return
    await signOutEverywhere()
    await refreshState()
    location.assign('/signin')
  }
  const onSetPassword = async () => {
    if (pw !== pw2) {
      setErr("The two passwords don't match.")
      return
    }
    if (await act('password', () => setPassword(data, pw), data.password.set ? 'Password changed.' : 'Password set.')) {
      setPwOpen(false)
      setPw('')
      setPw2('')
    }
  }
  const onTOTPStart = async () => {
    setErr('')
    try {
      setTOTP(await totpBegin())
      setOtp('')
    } catch (e) {
      setErr(errText(e))
    }
  }
  const onTOTPConfirm = async () => {
    if (await act('totp', () => totpConfirm(data, otp.trim()), 'Authenticator app set up.')) setTOTP(null)
  }
  const onAddPasskey = async () => {
    const name = prompt('Name this passkey', 'this device')
    if (name === null) return
    await act('passkey-add', () => addPasskeyHere(data, name.trim() || 'this device'), 'Passkey added.')
  }

  const needsTOTP = data.auth_policy.password_requires_totp
  const canPassword = data.auth_policy.methods.includes('password')

  return (
    <div class="page">
      <h1 class="screen-title">Access</h1>
      <p class="panel-sub" style="margin:0 0 16px">
        <b>Only operators can sign in</b>, with{' '}
        {data.auth_policy.methods.map((m) => methodPhrase[m]).join(', ')}
        {data.auth_policy.methods.includes('password') && needsTOTP ? ' (with an authenticator code)' : ''}.
      </p>
      {msg ? (
        <div class="banner ok" role="status" data-testid="access-msg">
          {msg}
        </div>
      ) : null}
      {err ? (
        <div class="banner err" role="alert" data-testid="access-error">
          {err}
        </div>
      ) : null}
      <div class="acc">
        <div class="acc-col">
          <section class="acc-card">
            <h2>
              Your passkeys
              {data.passkey_here && passkeysUsable() ? (
                <button class="btn sm" type="button" disabled={!!busy} onClick={onAddPasskey} data-testid="passkey-add">
                  Add a passkey here
                </button>
              ) : null}
            </h2>
            <ul class="acc-list" data-testid="passkey-list">
              {data.passkeys.length === 0 ? <li class="acc-row">No passkeys yet.</li> : null}
              {data.passkeys.map((k) => (
                <li class="acc-row">
                  <div class="acc-main">
                    <b>{k.name}</b>
                    {k.locked ? <span class="acc-locked"> · locked (possible clone)</span> : null}
                    <span class="acc-why">
                      {k.device_bound ? 'Stays on this device' : 'Synced to your other devices'} · {k.rp_id} · last
                      used {k.last_used ? ago(k.last_used) + ' ago' : 'never'}
                    </span>
                  </div>
                  <span class="acc-act">
                    {data.passkeys.length > 1 ? (
                      <button class="btn sm ghost" onClick={() => onRemovePasskey(k.id, k.name)}>
                        Remove…
                      </button>
                    ) : (
                      <button class="btn sm ghost" disabled aria-disabled="true" title="Your only passkey: add another first">
                        Remove…
                      </button>
                    )}
                  </span>
                </li>
              ))}
            </ul>
            <p class="acc-note">
              {data.passkey_here
                ? 'A passkey added here works on this hostname. '
                : 'Passkeys need HTTPS (or localhost) on a hostname conductor serves; this connection has none. '}
              On the box, <span class="mono">conductor dashboard enroll</span> prints a one-time link to add one on
              localhost.
            </p>
          </section>

          <section class="acc-card" data-testid="password-card">
            <h2>Password and authenticator app</h2>
            <ul class="acc-list">
              <li class="acc-row">
                <div class="acc-main">
                  <b>Password</b>
                  <span class="acc-why" data-testid="password-state">
                    {data.password.set ? `Set ${ago(data.password.set_at)} ago` : 'Not set'}
                    {!canPassword ? ' · password sign-in is off in the config' : ''}
                  </span>
                </div>
                <span class="acc-act">
                  <button class="btn sm" type="button" disabled={!!busy} onClick={() => setPwOpen(!pwOpen)} data-testid="password-open">
                    {data.password.set ? 'Change…' : 'Set…'}
                  </button>
                  {data.password.set ? (
                    <button class="btn sm ghost" type="button" disabled={!!busy} onClick={() => act('password', () => removePassword(data), 'Password removed.')}>
                      Remove
                    </button>
                  ) : null}
                </span>
              </li>
              {pwOpen ? (
                <li class="acc-row acc-form">
                  <form
                    onSubmit={(e) => {
                      e.preventDefault()
                      void onSetPassword()
                    }}
                  >
                    <label class="gate-field">
                      <span class="gate-group-label">New password (at least 12 characters)</span>
                      <input type="password" autocomplete="new-password" value={pw} data-testid="password-new" onInput={(e) => setPw((e.target as HTMLInputElement).value)} />
                    </label>
                    <label class="gate-field">
                      <span class="gate-group-label">Again</span>
                      <input type="password" autocomplete="new-password" value={pw2} data-testid="password-new2" onInput={(e) => setPw2((e.target as HTMLInputElement).value)} />
                    </label>
                    <button class="btn primary" type="submit" disabled={!!busy || !pw} data-testid="password-save">
                      {busy === 'password' ? 'Saving…' : 'Save password'}
                    </button>
                  </form>
                </li>
              ) : null}
              <li class="acc-row">
                <div class="acc-main">
                  <b>Authenticator app</b>
                  <span class="acc-why" data-testid="totp-state">
                    {data.totp.set ? `Set up ${ago(data.totp.set_at)} ago` : 'Not set up'}
                  </span>
                </div>
                <span class="acc-act">
                  {data.totp.set ? (
                    <button class="btn sm ghost" type="button" disabled={!!busy} onClick={() => act('totp', () => removeTOTP(data), 'Authenticator app removed.')}>
                      Remove
                    </button>
                  ) : (
                    <button class="btn sm" type="button" disabled={!!busy || !!totp} onClick={onTOTPStart} data-testid="totp-start">
                      Set up…
                    </button>
                  )}
                </span>
              </li>
              {totp ? (
                <li class="acc-row acc-form">
                  <div class="totp-setup">
                    <img class="totp-qr" src={totp.qr} alt="QR code for your authenticator app" width={180} height={180} />
                    <div class="totp-side">
                      <p class="acc-note">Scan it, or type this key into the app (time-based, 6 digits, 30 seconds):</p>
                      <CopyBlock text={totp.secret} testid="totp-secret" />
                    </div>
                  </div>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault()
                      void onTOTPConfirm()
                    }}
                  >
                    <label class="gate-field">
                      <span class="gate-group-label">The app's current code</span>
                      <input class="mono" inputMode="numeric" autocomplete="one-time-code" maxLength={7} value={otp} data-testid="totp-code" onInput={(e) => setOtp((e.target as HTMLInputElement).value)} />
                    </label>
                    <div class="row-actions">
                      <button class="btn primary" type="submit" disabled={!!busy || otp.trim().length < 6} data-testid="totp-confirm">
                        {busy === 'totp' ? 'Checking…' : 'Turn on'}
                      </button>
                      <button class="btn ghost" type="button" onClick={() => setTOTP(null)}>
                        Cancel
                      </button>
                    </div>
                  </form>
                </li>
              ) : null}
            </ul>
            <p class="acc-note">
              {needsTOTP
                ? 'A password signs in only together with a code from the authenticator app.'
                : 'This config lets a password sign in on its own (password_requires_totp: false); with an app set up, its code is still asked for.'}{' '}
              Lost the app? <span class="mono">conductor dashboard reset</span>, signed with your SSH key, clears it.
            </p>
          </section>

          <section class="acc-card">
            <h2>
              Signed in now
              <button class="btn sm" onClick={onSignOutAll}>
                Sign out everywhere…
              </button>
            </h2>
            <ul class="acc-list" data-testid="session-list">
              {data.sessions.map((s) => (
                <li class="acc-row">
                  <div class="acc-main">
                    <b>
                      {s.device}
                      {s.current ? ' (this one)' : ''}
                    </b>
                    <span class="acc-why">
                      {sessionHow(s)} · signed in {s.created ? ago(s.created) + ' ago' : ''}
                    </span>
                  </div>
                  <span class="acc-act">
                    {s.current ? null : (
                      <button class="btn sm ghost" onClick={() => onEndSession(s.id)}>
                        Sign out
                      </button>
                    )}
                  </span>
                </li>
              ))}
            </ul>
            <p class="acc-note">
              A session ends after {data.session_policy.idle} idle, or {data.session_policy.max} at most.
            </p>
          </section>
        </div>

        <div class="acc-col">
          <section class="acc-card">
            <h2>When it asks you to confirm</h2>
            <ul class="acc-asks">
              <li>Approving or denying what a bot asks to do</li>
              <li>Config changes that give bots more access or loosen safety</li>
              <li>Storing or replacing a secret value in a vault</li>
              <li>Taking over a browser a bot is signed in to</li>
              <li>Adding or removing a way to sign in</li>
            </ul>
            <p class="acc-note" data-testid="stepup-methods">
              You can confirm with:{' '}
              {data.stepup_methods.length ? data.stepup_methods.map((m) => methodPhrase[m]).join(', ') : 'nothing on this connection'}
              . Everything else uses your signed-in session.
            </p>
          </section>

          <details class="acc-card acc-tech">
            <summary>Technical details</summary>
            <dl>
              <dt>Where it listens</dt>
              {data.listeners.map((l) => (
                <dd class="mono">
                  {l.rp_id}: {l.origins.join(', ')}
                </dd>
              ))}
              <dt>Wrong passkey, password or code</dt>
              <dd>
                5 failed tries in 15 minutes locks that client for 15 minutes, doubling each time. Wrong passwords and
                authenticator codes also lock that operator's password and code sign-in the same way.
              </dd>
              <dt>Enrolment links</dt>
              <dd>3 an hour, one live at a time.</dd>
              <dt>Session cookie</dt>
              <dd class="mono">
                {data.transport === 'http' ? 'conductor_session, HttpOnly, SameSite=Strict (not Secure: plain HTTP)' : '__Host-, Secure, HttpOnly, SameSite=Strict'}
              </dd>
              <dt>Password storage</dt>
              <dd>argon2id (19 MiB, 2 passes); authenticator codes are RFC 6238 (SHA-1, 30 s, 6 digits), each usable once.</dd>
              <dt>Certificate-transparency caveat</dt>
              <dd>A Tailscale or public HTTPS cert puts the machine name in public CT logs.</dd>
            </dl>
          </details>
        </div>
      </div>
    </div>
  )
}
