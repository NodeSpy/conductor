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
import { BrowserTabs, BrowserCrumbs, RegisterPasskey } from './Browsers'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { humanError } from '../api/errors'

export function Logins(): JSX.Element {
  const [logins, setLogins] = useState<LoginRecord[] | null>(null)
  const [err, setErr] = useState('')
  const [testing, setTesting] = useState<Record<string, string>>({})

  function load() {
    browser.logins().then((r) => setLogins(r.logins || [])).catch((e) => setErr(humanError(e)))
  }
  useEffect(load, [])

  async function test(name: string) {
    setTesting((t) => ({ ...t, [name]: 'signing in…' }))
    try {
      const r = (await walletOp('browser-login-test', { name })) as { status?: string; reason?: string }
      const word = r.status === 'ok' ? 'signed in' : r.status === 'needs-you' ? 'needs you: approve on your phone' : 'sign-in refused'
      setTesting((t) => ({ ...t, [name]: word }))
    } catch (e) {
      setTesting((t) => ({ ...t, [name]: humanError(e) }))
    }
  }

  const credentials = (logins || []).filter((l) => l.method !== 'session')
  const sessions = (logins || []).filter((l) => l.method === 'session')

  // vaultWord names where a credential's fields come from, never the value
  // (design §4.5.1); "one click away" is this line plus Test, not a value.
  const vaultWord = (l: LoginRecord): string => {
    const srcs = Object.values(l.fields || {})
    const vault = srcs.find((f) => f.kind === 'vault')
    return vault ? vaultSrcWord(vault.source) : srcs.length ? 'typed in, stored write-only' : ''
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrowserCrumbs here="Logins" />
          <h1>Logins</h1>
        </div>
        <div class="actions">
          {/* With no login yet the empty state below carries the one Add a login. */}
          {credentials.length || logins === null ? <a class="btn primary" href="#/browser/add"><Icon name="key" />Add a login</a> : null}
          <RegisterPasskey />
        </div>
      </div>
      <BrowserTabs active="logins" />
      {err ? <div class="banner err">{err}</div> : null}

      <section class="dl-card">
        <header class="dl-ch"><h2>Sign-ins</h2></header>
        {logins === null ? (
          <p class="dl-sub" style="padding:0 var(--pad-card) var(--pad-card)">{err ? 'Nothing to show until conductor answers.' : 'Loading logins…'}</p>
        ) : credentials.length === 0 ? (
          <EmptyState hero icon="key" title="No logins yet" testid="logins-empty" action={{ label: 'Add a login', href: '#/browser/add', testid: 'logins-empty-add' }}>
            <p>Add one so a bot can sign in to a site you choose.</p>
          </EmptyState>
        ) : (
          <ul class="dl-list dl-conns wl-flushl">
            {credentials.map((l) => (
              <li class="dl-conn" data-login={l.name} key={l.name}>
                <span class="dl-ng"><Icon name="lock" /></span>
                <span class="dl-nt">
                  <b>{l.name} <span class="tgt">{(l.origins && l.origins[0] && hostOf(l.origins[0])) || l.site || ''}</span></b>
                  <span class="dl-why">
                    For {(l.bindings || []).map(who).join(', ') || 'nobody yet'}
                    {' · '}
                    {vaultWord(l) || 'a login'}
                    {l.approval ? ' · approved on your phone' : ''}
                    {l.last_used ? ` · last used ${rel(l.last_used)}` : ' · never used'}
                    {testing[l.name] ? ` · ${testing[l.name]}` : ''}
                  </span>
                </span>
                <span class="dl-na">
                  {!l.editable ? <span class="tag">In the config</span> : null}
                  <button class="btn sm secondary" type="button" onClick={() => void test(l.name)}>Test</button>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section class="dl-card wl-gap">
        <header class="dl-ch">
          <h2>Saved sessions</h2>
          <span class="dl-sub">signed in yourself · state, not config</span>
        </header>
        {sessions.length === 0 ? (
          <p class="dl-sub" style="padding:0 var(--pad-card) var(--pad-card)">
            None yet. To add one, run <code>conductor browser credential add NAME --session --site SITE --bind WHO</code> on
            the conductor machine, then <code>conductor browser seed</code> to sign in once yourself.
          </p>
        ) : (
          <ul class="dl-list dl-conns wl-flushl">
            {sessions.map((l) => (
              <li class="dl-conn" data-login={l.name} key={l.name}>
                <span class="dl-ng"><Icon name="cookie" /></span>
                <span class="dl-nt"><b>{l.site || l.name} <span class="tgt">session</span></b>
                  <span class="dl-why">{(l.bindings || []).map(who).join(', ')}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function hostOf(origin: string): string {
  return origin.replace(/^https?:\/\//, '').replace(/\/$/, '')
}

// vaultSrcWord names a {{ vault "<v>" "<k>" }} reference's vault in words,
// never the value (mirrors the prototype's srcWord).
function vaultSrcWord(ref: string): string {
  const m = /vault\s+"([^"]+)"\s+"([^"]+)"/.exec(ref)
  if (!m) return ref
  const vault = m[1] === 'op' ? '1Password' : m[1] === 'house' ? 'the conductor vault' : m[1]
  return `${vault} · ${m[2]}`
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
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <a class="back" href="#/browser/logins">← Logins</a>
          <h1>Add a login</h1>
          <p class="sub">A site a bot may sign in to, and who may use it. Every method ends the same way: the exact origins, who may use it, then the config diff.</p>
        </div>
      </div>
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

const METHOD_ICON: Record<Method, string> = { password: 'key', vault: 'lock', takeover: 'hand' }

function MethodCard({ m, cur, set, title, desc, disabled, disabledWhy }: { m: Method; cur: Method; set: (m: Method) => void; title: string; desc: string; disabled?: boolean; disabledWhy?: string }): JSX.Element {
  const on = cur === m
  return (
    <button type="button" class="choice" aria-pressed={on} aria-disabled={disabled} disabled={disabled} onClick={() => !disabled && set(m)}>
      <span class="choice-ic"><Icon name={METHOD_ICON[m]} /></span>
      <span class="choice-body">
        <span class="choice-title">{title}</span>
        <span class="choice-desc">{disabled ? disabledWhy : desc}</span>
      </span>
      <span class="choice-check" aria-hidden="true"><Icon name="check" /></span>
    </button>
  )
}
