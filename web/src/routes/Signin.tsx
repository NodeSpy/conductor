// Sign-in (design §4.10, §6.3; dashboard-auth-methods): the gate offers the
// browser methods this connection supports — a passkey where the page is a
// secure context with a relying party, and a password with an authenticator
// code — as a segmented choice at the top of the form; picking one switches the
// form in place, and the last one used is picked next time. Signing an SSH
// challenge is a CLI and desktop-app path: here it is a quiet fallback link, so
// an operator with only an SSH key is never locked out. On success the daemon
// sets the session cookie and the app loads.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { signIn, signInPassword, sshBegin, sshFinish, sshPoll, authState, signInMethods, type Method, type SSHChallenge } from '../api/auth'
import { GateLogo, PasskeyGlyph } from '../components/GateChrome'
import { SSHPanel, PlainHTTPNotice, MethodTabs, browserMethods, rememberedMethod, rememberMethod, methodPhrase } from '../components/AuthBits'
import { Icon } from '../components/Icon'
import '../styles/auth.css'
import { humanError } from '../api/errors'

const operatorKey = 'conductor.operator'
const methodKey = 'conductor.signin.method'

function rememberedOperator(): string {
  try {
    return localStorage.getItem(operatorKey) ?? ''
  } catch {
    return ''
  }
}

function remember(op: string) {
  try {
    localStorage.setItem(operatorKey, op)
  } catch {
    /* private mode */
  }
}

const enter = () => location.assign('/#/overview')

function lastWasSSH(): boolean {
  try {
    return localStorage.getItem(methodKey) === 'ssh'
  } catch {
    return false
  }
}

// firstMethod: the SSH key when it was the last one used (or the only one this
// connection offers), else the remembered browser method.
function firstMethod(all: Method[], offered: Method[]): Method | undefined {
  if (all.includes('ssh') && (offered.length === 0 || lastWasSSH())) return 'ssh'
  return rememberedMethod(methodKey, offered)
}

const lead: Partial<Record<Method, string>> = {
  passkey: 'Use the passkey on this device, or on your phone.',
  password: 'Your operator name, password and authenticator code.',
  ssh: 'Sign a one-time challenge with your SSH key.',
}

export function Signin(): JSX.Element {
  const st = authState.value
  const all = signInMethods(st)
  const offered = browserMethods(all)
  const [method, setMethod] = useState<Method | undefined>(() => firstMethod(all, offered))
  const [operator, setOperator] = useState(rememberedOperator())
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [ch, setCh] = useState<SSHChallenge | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const run = async (fn: () => Promise<void>) => {
    setErr('')
    setBusy(true)
    try {
      await fn()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  const pick = (m: Method) => {
    setErr('')
    setCh(null)
    setMethod(m)
    rememberMethod(methodKey, m)
  }

  const operatorField = (
    <label class="gate-field">
      <span class="gate-group-label">Operator</span>
      <input
        value={operator}
        autocomplete="username"
        autocapitalize="none"
        spellcheck={false}
        placeholder="your operator name"
        aria-describedby="operator-hint"
        data-testid="signin-operator"
        onInput={(e) => setOperator((e.target as HTMLInputElement).value)}
      />
      <span class="field-hint" id="operator-hint">
        The name you're listed under in conductor's config (<code>operators:</code>).
      </span>
    </label>
  )

  let body: JSX.Element | null = null
  if (method === 'passkey') {
    body = (
      <button
        class="btn primary lg block"
        type="button"
        onClick={() => run(async () => (await signIn(), enter()))}
        disabled={busy}
        data-testid="signin-button"
      >
        <PasskeyGlyph />
        {busy ? 'Confirm on your device…' : 'Sign in with a passkey'}
      </button>
    )
  } else if (method === 'password') {
    body = (
      <form
        data-testid="password-form"
        onSubmit={(e) => {
          e.preventDefault()
          remember(operator.trim())
          void run(async () => (await signInPassword(operator.trim(), password, code), enter()))
        }}
      >
        {operatorField}
        <label class="gate-field">
          <span class="gate-group-label">Password</span>
          <input
            type="password"
            autocomplete="current-password"
            value={password}
            data-testid="signin-password"
            onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
          />
        </label>
        <label class="gate-field">
          <span class="gate-group-label">{st.password_needs_totp ? 'Authenticator code' : 'Authenticator code (if you set one up)'}</span>
          <input
            class="mono"
            inputMode="numeric"
            autocomplete="one-time-code"
            maxLength={7}
            value={code}
            data-testid="signin-code"
            onInput={(e) => setCode((e.target as HTMLInputElement).value)}
          />
        </label>
        <button class="btn primary lg block" type="submit" disabled={busy || !operator.trim() || !password} data-testid="signin-password-submit">
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    )
  } else if (method === 'ssh') {
    body = ch ? (
      <SSHPanel
        ch={ch}
        operator={operator.trim()}
        verb="Sign in"
        onFinish={async (sig) => (await sshFinish(ch.challenge_id, sig), enter())}
        onPoll={async () => {
          const done = await sshPoll(ch.challenge_id)
          if (done) enter()
          return done
        }}
        onRestart={() => setCh(null)}
      />
    ) : (
      <form
        data-testid="ssh-form"
        onSubmit={(e) => {
          e.preventDefault()
          remember(operator.trim())
          void run(async () => setCh(await sshBegin(operator.trim())))
        }}
      >
        {operatorField}
        <button class="btn primary lg block" type="submit" disabled={busy || !operator.trim()} data-testid="ssh-begin">
          {busy ? 'Preparing…' : 'Continue with SSH key'}
        </button>
      </form>
    )
  }

  // The quiet way between the browser methods and the SSH fallback.
  const back = offered.length ? (offered.length === 1 ? `Use your ${methodPhrase[offered[0]]} instead` : 'Use a passkey or password instead') : ''
  const switcher =
    method === 'ssh' ? (
      back ? (
        <button type="button" class="gate-alt" data-testid="ssh-back" onClick={() => pick(offered[0])}>
          {back}
        </button>
      ) : null
    ) : all.includes('ssh') ? (
      <button type="button" class="gate-alt" data-testid="method-ssh" onClick={() => pick('ssh')}>
        Use your SSH key instead
      </button>
    ) : null

  const transport = st.transport ?? 'localhost'
  return (
    <div class="gate">
      <div class="panel card gate-card">
        <div class="gate-brand">
          <GateLogo />
        </div>
        <h1>Sign in</h1>
        {method && lead[method] ? <p class="gate-sub">{lead[method]}</p> : null}
        {transport === 'http' ? <PlainHTTPNotice /> : null}
        {all.length === 0 ? (
          <p class="gate-err" role="alert">
            No sign-in method works on this connection. Open the dashboard over HTTPS or on the box itself (localhost), or
            allow another method under <code>dashboard.auth.methods</code> in the config.
          </p>
        ) : null}
        {offered.length > 1 && method !== 'ssh' ? (
          <MethodTabs methods={offered} value={method} onPick={pick} label="Sign in with" testid="method" />
        ) : null}
        <div class="gate-form" role={offered.length > 1 && method !== 'ssh' ? 'tabpanel' : undefined}>
          {body}
        </div>
        {err ? (
          <p class="gate-err" role="alert" data-testid="signin-error">
            {err}
          </p>
        ) : null}
        {switcher}
        <p class="acc-note gate-hint" data-testid="signin-hint">
          No passkey or password yet? On the box, <code>conductor dashboard enroll</code> prints a one-time
          link to add one.
        </p>
        <div class="gate-foot" data-testid="transport">
          <Icon name={transport === 'http' ? 'warn' : 'shield'} />
          <span>
            {transport === 'https' ? 'HTTPS' : transport === 'localhost' ? 'localhost (this computer)' : 'Plain HTTP — not encrypted'}
            {location.host ? ` · ${location.host}` : ''}
          </span>
        </div>
      </div>
    </div>
  )
}
