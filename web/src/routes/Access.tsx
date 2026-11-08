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
  authState,
  type AccessData,
  type SessionRow,
  type TOTPEnrollment,
} from '../api/auth'
import { passkeysUsable } from '../lib/webauthn'
import { CopyBlock, methodPhrase, browserMethods } from '../components/AuthBits'
import { Icon } from '../components/Icon'
import { ago, goDuration } from '../lib/format'
import '../styles/auth.css'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

function errText(e: unknown): string {
  return humanError(e)
}

// groupKey shows a base32 key in 4-character blocks for typing; copying takes
// the key without the spaces.
function groupKey(k: string): string {
  return k.replace(/(.{4})(?=.)/g, '$1 ')
}

// sessionWhere is the design's dl-why shape for a session row ("over your
// tailnet · signed in X ago · last active Y ago"): just where the session
// reaches the dashboard from, in plain words. A session's sign-in method is
// already named in the page's lead sentence, so it doesn't repeat here.
function sessionWhere(s: SessionRow): string {
  return s.transport === 'http'
    ? `plain HTTP${s.host ? ' to ' + s.host : ''} (not encrypted)`
    : s.listener === 'tailscale'
      ? 'over your tailnet'
      : s.listener === 'localhost'
        ? 'on this computer'
        : `HTTPS${s.host ? ' to ' + s.host : ''}`
}

// listenerPhrase reads the real listeners for the lead sentence's "from …"
// clause ("from this computer and your tailnet"), in the design's words for
// the listener kinds this box actually has.
function listenerPhrase(listeners: AccessData['listeners']): string {
  const words = Array.from(
    new Set(
      listeners.map((l) => (l.listener === 'tailscale' ? 'your tailnet' : l.listener === 'localhost' ? 'this computer' : l.listener)),
    ),
  )
  if (!words.length) return 'nowhere configured'
  if (words.length === 1) return words[0]
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
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

  if (err && !data) {
    const st = authState.value
    return (
      <div class="col">
        <div class="dl-phead"><div><h1>Access</h1></div></div>
        {!st.signed_in && !st.required ? (
          // An open dashboard: no operators, so nobody signs in and there is
          // no one's access to show.
          <EmptyState testid="access-empty" title="Nobody signs in here yet">
            <p>This dashboard is open: it runs without sign-in, so there are no passkeys or sessions to manage.</p>
            <p>To require sign-in, add yourself under <code>operators:</code> in the config with your SSH public key, then run <code>conductor dashboard enroll --operator &lt;name&gt;</code> for a link that sets up your passkey.</p>
          </EmptyState>
        ) : !st.signed_in ? (
          <EmptyState testid="access-signed-out" title="You're signed out" action={{ label: 'Sign in', href: '/signin' }}>
            <p>Sign in to see your passkeys and the devices signed in as you.</p>
          </EmptyState>
        ) : (
          <div class="banner err" role="alert">
            Couldn't load Access: {err}{' '}
            <button class="btn sm" type="button" onClick={reload}>Try again</button>
          </div>
        )}
      </div>
    )
  }
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
    const done = data.totp.set ? 'Authenticator app replaced. The old one no longer works.' : 'Authenticator app set up.'
    if (await act('totp', () => totpConfirm(data, otp.trim()), done)) setTOTP(null)
  }
  const onAddPasskey = async () => {
    const name = prompt('Name this passkey', 'this device')
    if (name === null) return
    await act('passkey-add', () => addPasskeyHere(data, name.trim() || 'this device'), 'Passkey added.')
  }

  const needsTOTP = data.auth_policy.password_requires_totp
  const canPassword = data.auth_policy.methods.includes('password')
  // What the browser offers: the SSH key signs in and confirms from the CLI and
  // the desktop app; in the browser it is only a fallback.
  // The browser methods this operator really has set up (and the policy allows).
  const haveBrowser = browserMethods(data.auth_policy.methods).filter((m) =>
    m === 'passkey' ? data.passkeys.length > 0 : m === 'password' ? data.password.set && (!needsTOTP || data.totp.set) : false,
  )
  const confirmWith = browserMethods(data.stepup_methods)
  const sshFallback = confirmWith.length === 0 && data.stepup_methods.includes('ssh')

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Access</h1>
          <p class="dl-lead">
            <b>Only you can sign in</b>
            {haveBrowser.length ? (
              <>
                , with your {haveBrowser.map((m) => methodPhrase[m]).join(' or your ')}
                {haveBrowser.includes('password') && (needsTOTP || data.totp.set) ? ' and authenticator code' : ''}, from {listenerPhrase(data.listeners)}.
              </>
            ) : data.auth_policy.methods.includes('ssh') ? (
              <>
                , for now with your SSH key, from {listenerPhrase(data.listeners)}. Add a passkey or a password below to sign in
                without it.
              </>
            ) : (
              <>, from {listenerPhrase(data.listeners)}. Add a passkey or a password below to sign in.</>
            )}
          </p>
        </div>
      </div>
      {msg ? (
        <div class="banner ok acc-banner" role="status" data-testid="access-msg">
          {msg}
        </div>
      ) : null}
      {err ? (
        <div class="banner err acc-banner" role="alert" data-testid="access-error">
          {err}
        </div>
      ) : null}
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card">
            <header class="dl-ch">
              <h2>Your passkeys</h2>
              <span class="dl-sp"></span>
              {data.passkey_here && passkeysUsable() ? (
                <button class="btn sm secondary" type="button" disabled={!!busy} onClick={onAddPasskey} data-testid="passkey-add">
                  <Icon name="plus" />Add a passkey
                </button>
              ) : null}
            </header>
            <ul class="dl-list" data-testid="passkey-list">
              {data.passkeys.length === 0 ? <li class="acc-row dl-sub">No passkeys yet.</li> : null}
              {data.passkeys.map((k) => (
                <li class="acc-row">
                  <div class="dl-nt">
                    <b>{k.name}</b>
                    {k.locked ? <span class="acc-locked"> · locked (possible clone)</span> : null}
                    <span class="dl-why">
                      {k.device_bound ? 'Stays on this device' : 'Synced to your other devices'} · {k.rp_id} · last
                      used {k.last_used ? ago(k.last_used) : 'never'}
                    </span>
                  </div>
                  <span class="dl-na">
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
            <p class="dl-sub acc-note">
              {data.passkey_here
                ? 'A passkey added here works on this hostname. '
                : 'Passkeys need HTTPS (or localhost) on a hostname conductor serves; this connection has none. '}
              On the box, <code>conductor dashboard enroll</code> prints a one-time link to add one on
              localhost.
            </p>
          </section>

          <section class="dl-card" data-testid="password-card">
            <header class="dl-ch"><h2>Password and authenticator app</h2></header>
            <ul class="dl-list">
              <li class="acc-row">
                <div class="dl-nt">
                  <b>Password</b>
                  <span class="dl-why" data-testid="password-state">
                    {data.password.set ? `Set ${ago(data.password.set_at)}` : 'Not set'}
                    {!canPassword ? ' · password sign-in is off in the config' : ''}
                  </span>
                </div>
                <span class="dl-na">
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
                <div class="dl-nt">
                  <b>Authenticator app</b>
                  <span class="dl-why" data-testid="totp-state">
                    {data.totp.set ? `Set up ${ago(data.totp.set_at)}` : 'Not set up'}
                  </span>
                </div>
                <span class="dl-na">
                  {data.totp.set ? (
                    <>
                      <button class="btn sm" type="button" disabled={!!busy || !!totp} onClick={onTOTPStart} data-testid="totp-replace">
                        Replace…
                      </button>
                      <button class="btn sm ghost" type="button" disabled={!!busy} onClick={() => act('totp', () => removeTOTP(data), 'Authenticator app removed.')}>
                        Remove
                      </button>
                    </>
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
                      {data.totp.set ? (
                        <p class="dl-sub acc-note" data-testid="totp-replace-note">
                          A new key for your app. The one you have keeps working until you confirm this one with a code.
                        </p>
                      ) : null}
                      <p class="dl-sub acc-note">Scan it, or type this key into the app (time-based, 6 digits, 30 seconds):</p>
                      <CopyBlock text={totp.secret} display={groupKey(totp.secret)} testid="totp-secret" words />
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
                        {busy === 'totp' ? 'Checking…' : data.totp.set ? 'Replace' : 'Turn on'}
                      </button>
                      <button class="btn ghost" type="button" onClick={() => setTOTP(null)}>
                        Cancel
                      </button>
                    </div>
                  </form>
                </li>
              ) : null}
            </ul>
            <p class="dl-sub acc-note">
              {needsTOTP
                ? 'A password signs in only together with a code from the authenticator app.'
                : 'This config lets a password sign in on its own (password_requires_totp: false); with an app set up, its code is still asked for.'}{' '}
              Lost the app? <code>conductor dashboard reset</code>, signed with your SSH key, clears it.
            </p>
          </section>

          <section class="dl-card">
            <header class="dl-ch">
              <h2>Signed in now</h2>
              <span class="dl-sp"></span>
              <button class="btn sm secondary" onClick={onSignOutAll}>
                Sign out everywhere…
              </button>
            </header>
            <ul class="dl-list" data-testid="session-list">
              {data.sessions.map((s) => (
                <li class="acc-row">
                  <div class="dl-nt">
                    <b>
                      {s.device}
                      {s.current ? ' (this one)' : ''}
                    </b>
                    <span class="dl-why">
                      {sessionWhere(s)} · signed in {s.created ? ago(s.created) : ''} · last active{' '}
                      {s.last_seen ? ago(s.last_seen) : ''}
                    </span>
                  </div>
                  <span class="dl-na">
                    {s.current ? null : (
                      <button class="btn sm ghost" onClick={() => onEndSession(s.id)}>
                        Sign out
                      </button>
                    )}
                  </span>
                </li>
              ))}
            </ul>
            <p class="dl-sub acc-note">
              A session ends after {goDuration(data.session_policy.idle)} idle, or {goDuration(data.session_policy.max)} at most.
            </p>
          </section>
        </div>

        <div class="dl-col-side">
          <section class="dl-card">
            <header class="dl-ch"><h2>When it needs your passkey</h2></header>
            <ul class="acc-asks">
              <li>Approving or denying what a bot asks to do, a purchase included</li>
              {data.auth_policy.config_stepup === 'guarded' ? (
                <li data-testid="config-asks">
                  Every restore, and config changes (a new bot avatar too) except: setting{' '}
                  <code>usage.mode</code> or raising an existing price under{' '}
                  <code>pricing</code> (at most tenfold), up to 5 in 10 minutes (
                  <code>config_stepup: guarded</code>)
                </li>
              ) : (
                <li data-testid="config-asks">Every config change (a new bot avatar too) and every restore</li>
              )}
              <li>
                Loosening a browser wallet limit or a ticket's spend limit, or giving a spend limit to another bot
                (assigning or reopening its ticket)
              </li>
              <li>Retrying a run so that steps that already succeeded run again</li>
              <li>Taking over a browser a bot is signed in to</li>
              <li>Adding or removing a way to sign in</li>
            </ul>
            <p class="dl-sub" data-testid="stepup-methods">
              {confirmWith.length ? (
                <>You can confirm with your {confirmWith.map((m) => methodPhrase[m]).join(' or your ')}.</>
              ) : sshFallback ? (
                <>
                  For now you confirm with your SSH key. Add a passkey or an authenticator app here and you won't need it
                  in the browser.
                </>
              ) : (
                <>You have nothing to confirm with on this connection yet: add a passkey or an authenticator app here.</>
              )}{' '}
              Everything else, including routine config changes, uses your signed-in session.
            </p>
          </section>

          <section class="dl-card">
            <header class="dl-ch"><h2>Approvals</h2></header>
            <p class="dl-sub">
              Any of your passkeys can approve. Requiring one that never leaves its device (never synced to another
              of your devices) isn't configurable yet.
            </p>
          </section>

          <details class="dl-card dl-tech-d acc-tech">
            <summary><Icon name="chev" />Technical details</summary>
            <dl class="dl-facts">
              <dt>Addresses</dt>
              {data.listeners.map((l) => (
                <dd class="mono">
                  {l.rp_id}: {l.origins.join(', ')}
                </dd>
              ))}
              <dt>Wrong passkey, password or code</dt>
              <dd>
                5 failed tries in 15 minutes locks that client (its address) for 15 minutes, doubling each time; tries
                running at once count before they finish. Wrong passwords and authenticator codes also lock that
                operator's password and code sign-in the same way, and are throttled per network (50 in 15 minutes);
                passkey and SSH sign-in stay open. On localhost only the operator lock and the throttle apply.
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
