// The step-up dialog (dashboard-auth-methods): every sensitive write calls
// stepUp(purpose, digest); this dialog lets the operator confirm with whichever
// method they have and policy allows — passkey, SSH key, authenticator code or
// (only when configured) password — and hands the single-use token back. The
// strongest available method is the primary action; the others are one tap away.
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
  type StepupRequest,
} from '../api/auth'
import { SSHPanel, methodLabel } from './AuthBits'
import { PasskeyGlyph } from './GateChrome'

const purposeLabel: Record<string, string> = {
  approve: 'Approve or deny what a bot asked to do',
  'config.apply': 'Apply a config change that gives more access or loosens safety',
  'secret.write': 'Store or replace a secret value',
  'browser.takeover': 'Take over a browser a bot is using',
  'browser.write': 'A wallet change that widens what a bot can do',
  'browser.purchase': 'Approve or deny a purchase',
  'credential.change': 'Change how you sign in',
  'update.apply': 'Apply an update',
  'pack.change': 'Add or remove a pack or plugin',
}

export function StepupDialog(): JSX.Element | null {
  const req = stepupRequest.value
  if (!req) return null
  return <Dialog req={req} />
}

function Dialog({ req }: { req: StepupRequest }): JSX.Element {
  const methods = stepupMethods(authState.value)
  const [method, setMethod] = useState<Method | undefined>(methods[0])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
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
      req.resolve(await fn())
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
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
      setErr(e instanceof Error ? e.message : String(e))
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

        {methods.length === 0 ? (
          <p class="gate-err" role="alert">
            You have no way to confirm on this connection. Set up an authenticator app or a passkey from Access, or sign
            in where your passkey works.
          </p>
        ) : null}

        {methods.length > 1 ? (
          <div class="method-chips" role="tablist" aria-label="Confirm with">
            {methods.map((m) => (
              <button
                type="button"
                role="tab"
                class={'chip' + (m === method ? ' on' : '')}
                aria-selected={m === method}
                data-testid={`stepup-method-${m}`}
                onClick={() => {
                  setErr('')
                  setMethod(m)
                }}
              >
                {methodLabel[m]}
              </button>
            ))}
          </div>
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
              void run(() => stepupPassword(req.purpose, req.digest, password))
            }}
          >
            <label class="gate-field">
              <span class="gate-group-label">Your password</span>
              <input
                type="password"
                autocomplete="current-password"
                value={password}
                onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
              />
            </label>
            <button class="btn primary lg block" type="submit" disabled={busy || !password}>
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
          <button class="btn ghost" type="button" onClick={cancel} data-testid="stepup-cancel">
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}
