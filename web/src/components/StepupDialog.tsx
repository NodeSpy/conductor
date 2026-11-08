// The step-up dialog (dashboard-auth-methods): every sensitive write calls
// stepUp(purpose, digest); this dialog lets the operator confirm with whichever
// browser method they have and policy allows — passkey, authenticator code or
// (only when configured) password with its code — and hands the single-use token
// back. The methods are a segmented choice at the top, the last one used picked
// first. The SSH key is offered only when the operator has nothing else enrolled
// (so nobody is locked out), with a line saying how to stop needing it.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import {
  authState,
  stepupRequest,
  stepupMethods,
  stepupPasskey,
  stepupTOTP,
  stepupPassword,
  stepupSSHBegin,
  stepupSSHFinish,
  stepupSSHPoll,
  type Method,
  type SSHChallenge,
  type StepupDetail,
  type StepupRequest,
} from '../api/auth'
import { SSHPanel, MethodTabs, browserMethods, rememberedMethod, rememberMethod } from './AuthBits'
import { PasskeyGlyph } from './GateChrome'
import { humanError } from '../api/errors'

const purposeLabel: Record<string, string> = {
  approve: 'Approve or deny what a bot asked to do',
  'config.apply': 'Apply a config change',
  'secret.write': 'Store or replace a secret value',
  'browser.takeover': 'Take over a browser a bot is using',
  'browser.write': 'A wallet change that widens what a bot can do',
  'browser.purchase': 'Approve or deny a purchase',
  'credential.change': 'Change how you sign in',
  'update.apply': 'Apply an update',
  'run.replay': 'Re-run steps of a run that already ran',
  'pack.change': 'Add or remove a pack or plugin',
}

// prettyAction renders an action for reading: JSON indented, a JSON string
// parsed first, anything else as text.
export function prettyAction(v: unknown): string {
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2) ?? String(v)
}

// What: the action being confirmed, in full, inside the sheet — the sheet sits
// over the page that showed it, so it has to carry it.
function What({ detail, digest }: { detail?: StepupDetail; digest: string }): JSX.Element {
  return (
    <div class="stepup-what" data-testid="stepup-what">
      {detail ? <p class="stepup-what-title">{detail.what}</p> : null}
      {detail?.description ? (
        <pre class="stepup-action stepup-desc" data-testid="stepup-description" tabIndex={0}>
          {detail.description}
        </pre>
      ) : detail?.lines?.length ? (
        <ul class="stepup-lines">
          {detail.lines.map((l) => (
            <li>{l}</li>
          ))}
        </ul>
      ) : null}
      {detail && !detail.description && detail.action !== undefined && detail.action !== null ? (
        <pre class="stepup-action" data-testid="stepup-action" tabIndex={0}>
          {prettyAction(detail.action)}
        </pre>
      ) : null}
      <p class="acc-note stepup-digest">
        Confirmation digest <code>{digest.slice(0, 12)}</code>… (approve-login shows the same)
      </p>
    </div>
  )
}

export function StepupDialog(): JSX.Element | null {
  const req = stepupRequest.value
  if (!req) return null
  return <Dialog req={req} />
}

const methodKey = 'conductor.stepup.method'

function Dialog({ req }: { req: StepupRequest }): JSX.Element {
  const usable = stepupMethods(authState.value)
  const offered = browserMethods(usable)
  // The SSH key only as the fallback: nothing else is enrolled.
  const sshOnly = offered.length === 0 && usable.includes('ssh')
  const methods: Method[] = sshOnly ? ['ssh'] : offered
  const [method, setMethod] = useState<Method | undefined>(() => rememberedMethod(methodKey, methods))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [pwCode, setPwCode] = useState('')
  const [ch, setCh] = useState<SSHChallenge | null>(null)

  const cancel = () => req.reject(new Error('confirmation cancelled'))
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') cancel()
    }
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [req])

  const run = async (fn: () => Promise<string>) => {
    setErr('')
    setBusy(true)
    try {
      const tok = await fn()
      if (method) rememberMethod(methodKey, method)
      req.resolve(tok)
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  const beginSSH = async () => {
    setErr('')
    setCh(null)
    try {
      setCh(await stepupSSHBegin(req.purpose, req.digest))
    } catch (e) {
      setErr(humanError(e))
    }
  }
  useEffect(() => {
    if (method === 'ssh' && !ch) void beginSSH()
  }, [method])

  return (
    <div class="dialog-backdrop" onClick={(e) => e.target === e.currentTarget && cancel()}>
      <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="stepup-title" data-testid="stepup-dialog">
        <h2 id="stepup-title">Confirm it's you</h2>
        <p class="gate-sub">{purposeLabel[req.purpose] ?? req.purpose}</p>
        <What detail={req.detail} digest={req.digest} />

        {methods.length === 0 ? (
          <p class="gate-err" role="alert" data-testid="stepup-none">
            You have no way to confirm on this connection. Set up an authenticator app or a passkey in System → Access, or
            open the dashboard where your passkey works.
          </p>
        ) : null}

        {methods.length > 1 ? (
          <MethodTabs
            methods={methods}
            value={method}
            label="Confirm with"
            testid="stepup-method"
            onPick={(m) => {
              setErr('')
              setMethod(m)
              rememberMethod(methodKey, m)
            }}
          />
        ) : null}

        {sshOnly ? (
          <p class="gate-callout stepup-ssh-why" data-testid="stepup-ssh-fallback">
            You have no passkey or authenticator yet, so this uses your SSH key. Set one up in System → Access.
          </p>
        ) : null}

        {method === 'passkey' ? (
          <button
            class="btn primary lg block"
            type="button"
            disabled={busy}
            data-testid="stepup-passkey"
            onClick={() => run(() => stepupPasskey(req.purpose, req.digest))}
          >
            <PasskeyGlyph />
            {busy ? 'Confirm on your device…' : 'Confirm with your passkey'}
          </button>
        ) : null}

        {method === 'totp' ? (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void run(() => stepupTOTP(req.purpose, req.digest, code))
            }}
          >
            <label class="gate-field">
              <span class="gate-group-label">Code from your authenticator app</span>
              <input
                class="mono"
                inputMode="numeric"
                autocomplete="one-time-code"
                maxLength={7}
                value={code}
                data-testid="stepup-totp-code"
                onInput={(e) => setCode((e.target as HTMLInputElement).value)}
              />
            </label>
            <button class="btn primary lg block" type="submit" disabled={busy || code.trim().length < 6} data-testid="stepup-totp-submit">
              {busy ? 'Checking…' : 'Confirm'}
            </button>
          </form>
        ) : null}

        {method === 'password' ? (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void run(() => stepupPassword(req.purpose, req.digest, password, pwCode))
            }}
          >
            <label class="gate-field">
              <span class="gate-group-label">Your password</span>
              <input
                type="password"
                autocomplete="current-password"
                value={password}
                data-testid="stepup-password"
                onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
              />
            </label>
            <label class="gate-field">
              <span class="gate-group-label">Authenticator code (if you set one up)</span>
              <input
                class="mono"
                inputMode="numeric"
                autocomplete="one-time-code"
                maxLength={7}
                value={pwCode}
                data-testid="stepup-password-code"
                onInput={(e) => setPwCode((e.target as HTMLInputElement).value)}
              />
            </label>
            <button class="btn primary lg block" type="submit" disabled={busy || !password} data-testid="stepup-password-submit">
              {busy ? 'Checking…' : 'Confirm'}
            </button>
          </form>
        ) : null}

        {method === 'ssh' ? (
          ch ? (
            <SSHPanel
              ch={ch}
              operator={authState.value.operator ?? ''}
              verb="Confirm"
              onFinish={async (sig) => req.resolve(await stepupSSHFinish(ch.challenge_id, sig))}
              onPoll={async () => {
                const tok = await stepupSSHPoll(ch.challenge_id)
                if (tok) req.resolve(tok)
                return !!tok
              }}
              onRestart={() => void beginSSH()}
            />
          ) : (
            <p class="acc-note">Preparing a challenge…</p>
          )
        ) : null}

        {err ? (
          <p class="gate-err" role="alert" data-testid="stepup-error">
            {err}
          </p>
        ) : null}
        <div class="dialog-foot">
          <button class="btn" type="button" onClick={cancel} data-testid="stepup-cancel">
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}
