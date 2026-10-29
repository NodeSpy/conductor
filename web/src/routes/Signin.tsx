// Sign-in (design §4.10, §6.3; dashboard-auth-methods): the gate offers the
// methods this connection supports, strongest first — a passkey where the page
// is a secure context with a relying party, then an SSH-signed challenge, then a
// password with an authenticator code. The primary action is the strongest; the
// others are one tap away. On success the daemon sets the session cookie and the
// app loads.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { signIn, signInPassword, sshBegin, sshFinish, sshPoll, authState, signInMethods, type Method, type SSHChallenge } from '../api/auth'
import { GateMark, PasskeyGlyph } from '../components/GateChrome'
import { SSHPanel, PlainHTTPNotice, methodLabel } from '../components/AuthBits'
import '../styles/auth.css'

const operatorKey = 'conductor.operator'

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

export function Signin(): JSX.Element {
  const st = authState.value
  const methods = signInMethods(st)
  const [method, setMethod] = useState<Method | undefined>(methods[0])
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
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const pick = (m: Method) => {
    setErr('')
    setCh(null)
    setMethod(m)
  }

  const operatorField = (
    <label class="gate-field">
      <span class="gate-group-label">Operator</span>
      <input
        value={operator}
        autocomplete="username"
        autocapitalize="none"
        spellcheck={false}
        data-testid="signin-operator"
        onInput={(e) => setOperator((e.target as HTMLInputElement).value)}
      />
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
        onSubmit={(e) => {
          e.preventDefault()
          remember(operator.trim())
          void run(async () => setCh(await sshBegin(operator.trim())))
        }}
      >
        {operatorField}
        <p class="acc-note">
          You'll sign a one-time challenge with the SSH key configured for you (<span class="mono">operators.&lt;name&gt;.ssh_key</span>) —
          here, or from another device.
        </p>
        <button class="btn primary lg block" type="submit" disabled={busy || !operator.trim()} data-testid="ssh-begin">
          {busy ? 'Preparing…' : 'Continue with SSH key'}
        </button>
      </form>
    )
  }

  const transport = st.transport ?? 'localhost'
  return (
    <div class="gate">
      <div class="gate-card">
        <div class="gate-brand">
          <GateMark />
          <span class="wordmark">conductor</span>
        </div>
        <h1>Sign in</h1>
        {transport === 'http' ? <PlainHTTPNotice /> : null}
        {methods.length === 0 ? (
          <p class="gate-err" role="alert">
            No sign-in method is available on this connection. Check dashboard.auth.methods, or open the dashboard over
            HTTPS or on localhost.
          </p>
        ) : null}
        {body}
        {err ? (
          <p class="gate-err" role="alert" data-testid="signin-error">
            {err}
          </p>
        ) : null}
        {methods.length > 1 ? (
          <div class="other-ways" data-testid="other-ways">
            <span class="gate-group-label">Other ways to sign in</span>
            <div class="method-chips">
              {methods
                .filter((m) => m !== method)
                .map((m) => (
                  <button type="button" class="chip" data-testid={`method-${m}`} onClick={() => pick(m)}>
                    {methodLabel[m]}
                  </button>
                ))}
            </div>
          </div>
        ) : null}
        <p class="acc-note" style="margin-top:14px">
          First time? Sign in with your SSH key — it needs no setup. On the box itself,{' '}
          <span class="mono">conductor dashboard enroll</span> prints a one-time link to add a passkey or password.
        </p>
        <div class="gate-foot" data-testid="transport">
          <span>
            {transport === 'https' ? 'HTTPS' : transport === 'localhost' ? 'localhost (this computer)' : 'Plain HTTP — not encrypted'}
            {st.listener ? ` · ${st.listener}` : ''}
          </span>
        </div>
      </div>
    </div>
  )
}
