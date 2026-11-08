// Shared pieces of the sign-in, enrolment, step-up and Access screens: a copyable
// command block, the SSH challenge panel (paste a signature, or approve the login
// code from another device), and the plain-HTTP notice.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import type { SSHChallenge, Method } from '../api/auth'
import { humanError } from '../api/errors'

// copyText copies via the Clipboard API where the page is a secure context, and
// falls back to a selected textarea + execCommand on plain HTTP (where the
// Clipboard API doesn't exist).
async function copyText(text: string): Promise<boolean> {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* fall through */
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}

// CopyBlock shows display (default: text) and copies text.
export function CopyBlock(props: { text: string; display?: string; label?: string; testid?: string; words?: boolean }): JSX.Element {
  const { text, label, testid } = props
  const [copied, setCopied] = useState('')
  return (
    <div class="copy-block">
      {label ? <span class="gate-group-label">{label}</span> : null}
      <pre class={'gate-code copy-text' + (props.words ? ' words' : '')} data-testid={testid}>
        {props.words
          ? (props.display ?? text).split(' ').map((w, i) => (
              <>
                {i ? ' ' : ''}
                <span class="w">{w}</span>
              </>
            ))
          : (props.display ?? text)}
      </pre>
      <button
        class="btn sm"
        type="button"
        onClick={async () => setCopied((await copyText(text)) ? 'Copied' : 'Select the text to copy')}
      >
        {copied || 'Copy'}
      </button>
    </div>
  )
}

// shellQuote single-quotes a string for a POSIX shell.
function shellQuote(s: string): string {
  return "'" + s.replace(/'/g, `'\\''`) + "'"
}

export const methodLabel: Record<Method, string> = {
  passkey: 'Passkey',
  ssh: 'SSH key',
  password: 'Password',
  totp: 'Authenticator code',
}

// browserMethods drops the SSH key from a list of methods the browser offers:
// signing a challenge with ssh-keygen and pasting the result is a CLI and
// desktop-app path, so the browser keeps it only as a hidden fallback.
export function browserMethods(methods: Method[]): Method[] {
  return methods.filter((m) => m !== 'ssh')
}

// rememberedMethod is the method last used under key, when it is still one of
// those offered; otherwise the first offered.
export function rememberedMethod(key: string, offered: Method[]): Method | undefined {
  try {
    const m = localStorage.getItem(key) as Method | null
    if (m && offered.includes(m)) return m
  } catch {
    /* private mode */
  }
  return offered[0]
}

export function rememberMethod(key: string, m: Method): void {
  try {
    localStorage.setItem(key, m)
  } catch {
    /* private mode */
  }
}

// MethodTabs is the method choice at the top of the sign-in card and the
// confirmation sheet: one segment per method, the chosen one switching the form
// below in place. Arrow keys move between segments (a tablist).
export function MethodTabs(props: {
  methods: Method[]
  value: Method | undefined
  onPick: (m: Method) => void
  label: string
  testid: string // per-segment test id prefix: `${testid}-${method}`
}): JSX.Element {
  const { methods, value } = props
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const i = Math.max(0, methods.indexOf(value as Method))
    const next = methods[(i + (e.key === 'ArrowRight' ? 1 : methods.length - 1)) % methods.length]
    props.onPick(next)
    const bar = e.currentTarget as HTMLElement
    requestAnimationFrame(() => bar.querySelector<HTMLButtonElement>(`[data-method="${next}"]`)?.focus())
  }
  return (
    <div class="seg method-tabs" role="tablist" aria-label={props.label} onKeyDown={onKey}>
      {methods.map((m) => (
        <button
          type="button"
          role="tab"
          class={m === value ? 'on' : ''}
          aria-selected={m === value}
          tabIndex={m === value ? 0 : -1}
          data-method={m}
          data-testid={`${props.testid}-${m}`}
          onClick={() => props.onPick(m)}
        >
          {methodLabel[m]}
        </button>
      ))}
    </div>
  )
}

// methodPhrase names a method inside a sentence.
export const methodPhrase: Record<Method, string> = {
  passkey: 'passkey',
  ssh: 'SSH key',
  password: 'password',
  totp: 'authenticator code',
}

// SSHPanel shows one SSH challenge two ways: the command to sign it here and a
// box to paste the signature, or the login code to approve from any machine with
// the key (`conductor dashboard approve-login`). It polls for the approval while
// it is on screen.
export function SSHPanel(props: {
  ch: SSHChallenge
  operator: string
  verb: string // "Sign in" | "Confirm"
  onFinish: (signature: string) => Promise<void>
  onPoll: () => Promise<boolean>
  onRestart: () => void
}): JSX.Element {
  const { ch, operator } = props
  const coarse = typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches
  // On plain HTTP off this machine the page (and the command it offers) can be
  // rewritten on the way; approve-login reads the request from the server.
  const plain = location.protocol === 'http:' && !['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)
  const [tab, setTab] = useState<'paste' | 'approve'>(coarse || plain ? 'approve' : 'paste')
  const [sig, setSig] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const done = useRef(false)

  useEffect(() => {
    done.current = false
    const t = setInterval(async () => {
      if (done.current) return
      try {
        if (await props.onPoll()) {
          done.current = true
          clearInterval(t)
        }
      } catch (e) {
        done.current = true
        clearInterval(t)
        setErr(humanError(e))
      }
    }, 2000)
    return () => {
      done.current = true
      clearInterval(t)
    }
  }, [ch.challenge_id])

  const submit = async () => {
    setErr('')
    setBusy(true)
    try {
      await props.onFinish(sig.trim())
      done.current = true
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  const origin = location.origin
  const keygen = `printf '%s' ${shellQuote(ch.challenge)} | ssh-keygen -Y sign -n ${ch.namespace} -f ~/.ssh/id_ed25519`
  const digest = /\bdigest=(\S+)/.exec(ch.challenge)?.[1] ?? ''
  const approve = `conductor dashboard approve-login ${ch.login_code} --url ${origin} --operator ${operator}`

  return (
    <div class="ssh-panel" data-testid="ssh-panel">
      <div class="seg-choice" role="tablist" aria-label="How to sign">
        <button type="button" role="tab" class={tab === 'paste' ? 'on' : ''} aria-selected={tab === 'paste'} onClick={() => setTab('paste')}>
          Sign on this device
        </button>
        <button type="button" role="tab" class={tab === 'approve' ? 'on' : ''} aria-selected={tab === 'approve'} onClick={() => setTab('approve')}>
          Approve from another device
        </button>
      </div>
      {tab === 'paste' ? (
        <div class="ssh-step">
          <p class="acc-note">
            Run this where your SSH key is (use your key's path; a <code>.pub</code> path works when the key
            is in ssh-agent), then paste the whole signature below.
          </p>
          {plain ? (
            <p class="banner attn" role="note" data-testid="ssh-plain-warning">
              This page came over plain HTTP, so anyone who can change traffic on the way could have changed this command.
              Read it before you run it, or use <b>Approve from another device</b>: approve-login reads the request from the
              dashboard itself.
            </p>
          ) : null}
          <CopyBlock text={keygen} testid="ssh-command" />
          <span class="acc-note">
            Or <code>conductor dashboard sign {'<challenge>'}</code>. The challenge on its own:
          </span>
          <pre class="gate-code copy-text" data-testid="ssh-challenge">
            {ch.challenge}
          </pre>
          <label class="gate-field">
            <span class="gate-group-label">Signature</span>
            <textarea
              class="sig-box mono"
              rows={6}
              spellcheck={false}
              autocomplete="off"
              placeholder="-----BEGIN SSH SIGNATURE-----"
              value={sig}
              data-testid="ssh-signature"
              onInput={(e) => setSig((e.target as HTMLTextAreaElement).value)}
            />
          </label>
          <button class="btn primary lg block" type="button" disabled={busy || !sig.trim()} onClick={submit} data-testid="ssh-submit">
            {busy ? 'Checking…' : props.verb}
          </button>
        </div>
      ) : (
        <div class="ssh-step">
          <p class="acc-note">On any computer with your SSH key and conductor installed, run:</p>
          <CopyBlock text={approve} testid="ssh-approve-command" words />
          <div class="login-code" aria-label="Login code" data-testid="login-code">
            {ch.login_code}
          </div>
          <p class="acc-note" role="status">
            Waiting for approval… The command on the other machine shows this request (this device, its address and when
            it started){digest ? <> and the confirmation digest <code>{digest.slice(0, 12)}</code>…</> : null},
            then asks you to confirm. The code works once and expires at {new Date(ch.expires).toLocaleTimeString()}.
          </p>
        </div>
      )}
      {err ? (
        <p class="gate-err" role="alert" data-testid="ssh-error">
          {err}{' '}
          <button class="btn sm ghost" type="button" onClick={props.onRestart}>
            Start again
          </button>
        </p>
      ) : null}
    </div>
  )
}

// PlainHTTPNotice says, plainly, what a plain-HTTP connection gives up.
export function PlainHTTPNotice(): JSX.Element {
  return (
    <div class="banner attn plain-http" role="note" data-testid="plain-http-notice">
      <b>Unencrypted connection.</b> This page is served over plain HTTP: anyone on the network path can read it,
      including your session cookie. Passkeys aren't available here.
    </div>
  )
}
