// Access (design §4.10): who can sign in and from where, which devices can
// approve, what's signed in now, and when conductor asks for your passkey.
// Passkeys and sessions are revocable here; the listeners, certificate and
// lockout numbers fold under Technical details.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { access, removePasskey, endSession, signOutEverywhere, refreshState, type AccessData } from '../api/auth'
import { ago } from '../lib/format'
import '../styles/auth.css'

export function Access(): JSX.Element {
  const [data, setData] = useState<AccessData | null>(null)
  const [err, setErr] = useState('')

  const reload = () => {
    access()
      .then((d) => {
        setData(d)
        setErr('')
      })
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
  }
  useEffect(reload, [])

  if (err) return <div class="banner err">Couldn't load Access: {err}</div>
  if (!data) return <div class="page">Loading…</div>

  const onRemovePasskey = async (id: string, name: string) => {
    if (!confirm(`Remove "${name}"? It can no longer sign in or approve, and any session it started ends now.`)) return
    try {
      await removePasskey(id)
      reload()
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e))
    }
  }
  const onEndSession = async (id: string) => {
    try {
      await endSession(id)
      reload()
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e))
    }
  }
  const onSignOutAll = async () => {
    if (!confirm('Sign out everywhere? Every device is signed out, this one too.')) return
    await signOutEverywhere()
    await refreshState()
    location.assign('/signin')
  }

  return (
    <div class="page">
      <h1 class="screen-title">Access</h1>
      <p class="panel-sub" style="margin:0 0 16px">
        <b>Only you can sign in</b>, with a passkey, from this computer and your tailnet.
      </p>
      <div class="acc">
        <div class="acc-col">
          <section class="acc-card">
            <h2>
              Your passkeys
              <a class="btn sm" href="/enroll">
                Add a passkey
              </a>
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
              Adding a passkey needs a one-time link from the command line on this computer (
              <span class="mono">conductor dashboard enroll</span>), so only someone with your SSH access can add one.
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
                      {s.listener === 'tailscale' ? 'over your tailnet' : 'on this computer'} · signed in{' '}
                      {s.created ? ago(s.created) + ' ago' : ''}
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
            <h2>When it needs your passkey</h2>
            <ul class="acc-asks">
              <li>Approving or denying what a bot asks to do</li>
              <li>Config changes that give bots more access or loosen safety</li>
              <li>Storing or replacing a secret value in a vault</li>
              <li>Taking over a browser a bot is signed in to</li>
            </ul>
            <p class="acc-note">Everything else, including routine config changes, uses your signed-in session.</p>
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
              <dt>Wrong passkey</dt>
              <dd>5 failed tries in 15 minutes locks that client for 15 minutes, doubling each time.</dd>
              <dt>Enrolment links</dt>
              <dd>3 an hour, one live at a time.</dd>
              <dt>Session cookie</dt>
              <dd class="mono">__Host-, Secure, HttpOnly, SameSite=Strict</dd>
              <dt>Certificate-transparency caveat</dt>
              <dd>A Tailscale HTTPS cert puts the machine and tailnet name in public CT logs.</dd>
            </dl>
          </details>
        </div>
      </div>
    </div>
  )
}
