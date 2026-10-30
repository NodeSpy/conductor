// Logins (design §4.5): every credential and saved session a bot may use —
// method, site, bound bots, last used, last test — never a value. And "Add a
// login": username & password (stored write-only in conductor's vault), from a
// vault (only a reference stored), or sign in yourself (desktop only). Each ends
// in the same binding step and goes through the config/step-up gate.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { go } from '../router'
import { browser, walletOp, who, type LoginRecord } from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs } from './Browsers'

export function Logins(): JSX.Element {
  const [logins, setLogins] = useState<LoginRecord[] | null>(null)
  const [err, setErr] = useState('')
  const [testing, setTesting] = useState<Record<string, string>>({})

  function load() {
    browser.logins().then((r) => setLogins(r.logins || [])).catch((e) => setErr(String(e)))
  }
  useEffect(load, [])

  async function test(name: string) {
    setTesting((t) => ({ ...t, [name]: 'signing in…' }))
    try {
      const r = (await walletOp('browser-login-test', { name })) as { status?: string; reason?: string }
      const word = r.status === 'ok' ? 'signed in' : r.status === 'needs-you' ? 'needs you: approve on your phone' : 'sign-in refused'
      setTesting((t) => ({ ...t, [name]: word }))
    } catch (e) {
      setTesting((t) => ({ ...t, [name]: String(e) }))
    }
  }

  const credentials = (logins || []).filter((l) => l.method !== 'session')
  const sessions = (logins || []).filter((l) => l.method === 'session')

  return (
    <div class="col browsers">
      <div class="page-head">
        <h1 class="screen-title serif">Browsers</h1>
        <div class="row-actions">
          <a class="btn primary sm" href="#/browser/add">Add a login</a>
        </div>
      </div>
      <BrowserTabs active="logins" />
      {err ? <div class="banner err">{err}</div> : null}

      <section class="panel">
        <span class="panel-title">Sign-ins</span>
        {logins === null ? (
          <p class="muted">Loading logins…</p>
        ) : credentials.length === 0 ? (
          <p class="muted">No logins yet. Add one so a bot can sign in to a site you choose.</p>
        ) : (
          <ul class="dl-list logins">
            {credentials.map((l) => (
              <li class="login-row" data-login={l.name}>
                <div class="login-main">
                  <div class="login-title">
                    <b>{l.name}</b> <span class="dl-sub">{(l.origins && l.origins[0]) || l.site || ''}</span>
                    {!l.editable ? <span class="tag">In the config</span> : null}
                  </div>
                  <p class="dl-why muted">
                    For {(l.bindings || []).map(who).join(', ') || 'nobody yet'}
                    {' · '}
                    {l.fields ? Object.keys(l.fields).join(', ') : 'a login'}
                    {l.last_used ? ` · last used ${rel(l.last_used)}` : ' · never used'}
                  </p>
                  {testing[l.name] ? <p class="facts">{testing[l.name]}</p> : null}
                </div>
                <div class="row-actions">
                  <button class="btn ghost sm" type="button" onClick={() => void test(l.name)}>Test</button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section class="panel">
        <div class="panel-head">
          <span class="panel-title">Saved sessions</span>
          <span class="muted small">signed in yourself · state, not config</span>
        </div>
        {sessions.length === 0 ? (
          <p class="muted">No saved sessions.</p>
        ) : (
          <ul class="dl-list">
            {sessions.map((l) => (
              <li class="login-row" data-login={l.name}>
                <div class="login-main">
                  <div class="login-title"><b>{l.site || l.name}</b> <span class="tag">session</span></div>
                  <p class="dl-why muted">{(l.bindings || []).map(who).join(', ')}</p>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function rel(iso: string): string {
  const d = new Date(iso).getTime()
  const s = Math.max(1, Math.round((Date.now() - d) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

// --- Add a login (#/browser/add) ---

type Method = 'password' | 'vault' | 'takeover'

export function AddLogin(): JSX.Element {
  const [step, setStep] = useState<'method' | 'creds' | 'bind'>('method')
  const [method, setMethod] = useState<Method>('password')
  const [username, setUsername] = useState<VSValue>({ ...emptyVS() })
  const [password, setPassword] = useState<VSValue>({ ...emptyVS() })
  const [otp, setOtp] = useState<VSValue>({ ...emptyVS() })
  const [name, setName] = useState('')
  const [origin, setOrigin] = useState('')
  const [bind, setBind] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const phone = typeof window !== 'undefined' && window.innerWidth <= 720

  async function save() {
    setBusy(true)
    setErr('')
    try {
      const values: Record<string, string> = {}
      const refs: Record<string, string> = {}
      const put = (f: string, v: VSValue) => {
        const w = vsToWire(v)
        if (w.value) values[f] = w.value
        if (w.ref) refs[f] = w.ref
      }
      put('username', username)
      put('password', password)
      if (otp.value || otp.ref) put('otp', otp)
      await walletOp('browser-login-add', {
        name,
        method,
        origins: [origin],
        bindings: bind.split(',').map((s) => s.trim()).filter(Boolean),
        values,
        refs,
      })
      go('browser/logins')
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col browsers add-login">
      <a class="back" href="#/browser/logins">← Logins</a>
      <h1 class="screen-title serif">Add a login</h1>
      <p class="muted">A site a bot may sign in to, and who may use it. Every method ends the same way: the exact origins, who may use it, then the config diff.</p>
      {err ? <div class="banner err">{err}</div> : null}

      {step === 'method' ? (
        <section class="panel">
          <span class="panel-title">How it signs in</span>
          <div class="choice-grid">
            <MethodCard m="password" cur={method} set={setMethod} title="Username & password" desc="Type them once. They go to conductor's vault, write-only, with an optional TOTP secret." />
            <MethodCard m="vault" cur={method} set={setMethod} title="From a vault" desc="1Password, pass, HashiCorp or conductor's vault. Pick the item and map its fields; only the reference is stored." />
            <MethodCard m="takeover" cur={method} set={setMethod} title="Sign in yourself" desc="A live browser: you sign in, conductor keeps the session. Best for SSO, CAPTCHA, SMS or email codes." disabled={phone} disabledWhy="Signing in yourself needs a desktop browser; on the phone, add it from a vault." />
          </div>
          <div class="row-actions">
            <button class="btn primary" type="button" onClick={() => setStep(method === 'takeover' ? 'bind' : 'creds')}>Next</button>
          </div>
        </section>
      ) : null}

      {step === 'creds' ? (
        <section class="panel">
          <span class="panel-title">The credentials</span>
          <p class="muted">Each field can be typed, or come from a vault. Only references are kept; a typed password goes to the vault, write-only.</p>
          <ValueSource label="Username" credential value={username} onChange={setUsername} testid="login-username" />
          <ValueSource label="Password" sensitive credential value={password} onChange={setPassword} testid="login-password" />
          <ValueSource label="One-time code (TOTP)" sensitive credential value={otp} onChange={setOtp} testid="login-otp" />
          <div class="row-actions">
            <button class="btn ghost" type="button" onClick={() => setStep('method')}>Back</button>
            <button class="btn primary" type="button" onClick={() => setStep('bind')}>Next: binding</button>
          </div>
        </section>
      ) : null}

      {step === 'bind' ? (
        <section class="panel">
          <span class="panel-title">Binding</span>
          <label class="form-field">
            <span>Login name</span>
            <input value={name} placeholder="grafana" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
          </label>
          <label class="form-field">
            <span>Origin (exact scheme + host)</span>
            <input value={origin} placeholder="https://grafana.example.com" onInput={(e) => setOrigin((e.target as HTMLInputElement).value)} />
          </label>
          <label class="form-field">
            <span>Which bots may use it</span>
            <input value={bind} placeholder="bot:atlas, team:platform" onInput={(e) => setBind((e.target as HTMLInputElement).value)} />
          </label>
          <p class="callout info">Used only on work you started: a bot's own sweep or a request from someone else can't use it. That's conductor's rule, not a setting.</p>
          <div class="row-actions">
            <button class="btn ghost" type="button" onClick={() => setStep(method === 'takeover' ? 'method' : 'creds')}>Back</button>
            <button class="btn primary" type="button" disabled={busy || !name || !origin} onClick={() => void save()}>
              {busy ? 'Confirming…' : 'Review & apply'}
            </button>
          </div>
        </section>
      ) : null}
    </div>
  )
}

function MethodCard({ m, cur, set, title, desc, disabled, disabledWhy }: { m: Method; cur: Method; set: (m: Method) => void; title: string; desc: string; disabled?: boolean; disabledWhy?: string }): JSX.Element {
  return (
    <button type="button" class={'choice' + (cur === m ? ' on' : '') + (disabled ? ' disabled' : '')} disabled={disabled} onClick={() => !disabled && set(m)}>
      <span class="choice-title">{title}</span>
      <span class="choice-desc">{disabled ? disabledWhy : desc}</span>
    </button>
  )
}
