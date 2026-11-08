// Logins (js/screens/logins.js): one compact line per sign-in — its state, the
// site and who it's for, what it signs in with in words, when it last worked —
// with Test and a ⋯ menu (where it was used, who may use it, replace the
// password, remove it); then the saved sessions (signed in yourself). And "Add
// a login": a username and password (stored write-only), from a vault (only a
// reference), or signing in yourself; each ends in the same binding step. A
// change that widens who may use a login asks for your passkey (the daemon
// classifies it; walletOp steps up). Values never come back.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { go } from '../router'
import { browser, walletOp, type LoginRecord, type FieldView } from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs, BrowserCrumbs, RegisterPasskey } from './Browsers'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { Menu, type MenuItem } from '../components/Menu'
import { ConfirmModal } from '../components/FormModal'
import { BrModal, Field, Stepper, WhoPicker } from '../components/BrowserFields'
import { agoText, host, loadRoster, roster, whoWord } from '../lib/browserwords'
import { humanError } from '../api/errors'

type LState = 'idle' | 'ok' | 'wait' | 'fail'

interface SavedSession {
  identity: string
  saved: string
  expires?: string
  note?: string
  cookies?: number
}
type LoginRow = LoginRecord & { sessions?: SavedSession[] }

const VN: Record<string, string> = { house: 'the conductor vault', op: '1Password', store: 'pass' }
const PART: Record<string, string> = { otp: 'a one-time code', username: 'a username', password: 'a password' }
const PART_LABEL: Record<string, string> = { otp: 'One-time code', passkey: 'Passkey', username: 'Username', password: 'Password' }

function stateOf(l: LoginRecord): LState {
  const t = l.last_test
  if (!t) return 'idle'
  return t.status === 'ok' ? 'ok' : t.status === 'needs-you' ? 'wait' : 'fail'
}

// vaultOf names the vault a field's value lives in (a typed value is kept in
// conductor's own vault).
function vaultOf(f: FieldView): string {
  if (f.kind === 'stored' || f.kind === 'literal') return 'house'
  const m = /vault\s+"([^"]+)"/.exec(f.source || '')
  return m ? m[1] : ''
}

function methodOf(l: LoginRecord): 'passkey' | 'password' | 'vault' | 'session' {
  if (l.method === 'session') return 'session'
  const f = l.fields || {}
  if (f.passkey) return 'passkey'
  if (f.password && (f.password.kind === 'stored' || f.password.kind === 'literal')) return 'password'
  return l.method === 'password' ? 'password' : 'vault'
}

function originOf(l: LoginRecord): string {
  return (l.origins && l.origins[0]) || l.site || ''
}

function whyWords(l: LoginRecord, s: LState): string {
  if (s === 'wait') return 'Needs you: a push approval on your phone'
  if (s === 'fail') return `The last sign-in failed${l.last_test?.reason ? `: ${l.last_test.reason}` : ''}`
  const f = l.fields || {}
  const parts = ['username', 'password', 'otp', 'passkey'].filter((k) => f[k])
  const from = [...new Set(parts.map((k) => vaultOf(f[k])).filter(Boolean))].map((v) => VN[v] || v).join(' and ')
  const what = methodOf(l) === 'passkey' ? 'a passkey' : parts.map((k) => PART[k]).filter(Boolean).join(', ') || 'a login'
  const forWho = (l.bindings || []).map(whoWord).join(', ') || 'nobody yet'
  return `${s === 'ok' ? 'Works · ' : ''}For ${forWho} · ${what}${from ? ` from ${from}` : ''}${l.approval ? ' · approved on your phone' : ''} · last used ${l.last_used ? agoText(l.last_used) : 'never'}`
}

const CONFIG_ONLY = 'Defined in the config: change it there'

export function Logins(): JSX.Element {
  const [logins, setLogins] = useState<LoginRow[] | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [uses, setUses] = useState<Record<string, { identity: string; dispatch?: string; action: string; outcome: string; at: string }[]>>({})
  const [menu, setMenu] = useState<{ el: HTMLElement; l: LoginRow } | null>(null)
  const [testing, setTesting] = useState<LoginRow | null>(null)
  const [binding, setBinding] = useState<LoginRow | null>(null)
  const [replacing, setReplacing] = useState<LoginRow | null>(null)
  const [removing, setRemoving] = useState<LoginRow | null>(null)
  const [revoking, setRevoking] = useState<{ l: LoginRow; s: SavedSession } | null>(null)

  function load() {
    browser.logins().then((r) => setLogins((r.logins || []) as LoginRow[])).catch((e) => setErr(humanError(e)))
  }
  useEffect(load, [])
  useEffect(loadRoster, [])
  void roster.value

  async function toggleUses(name: string) {
    if (open === name) return setOpen(null)
    setOpen(name)
    try {
      const r = (await walletOp('browser-login-usage', { name, limit: 5 })) as { usage?: typeof uses[string] }
      setUses((u) => ({ ...u, [name]: r.usage || [] }))
    } catch (e) {
      setErr(humanError(e))
    }
  }

  const credentials = (logins || []).filter((l) => l.method !== 'session')
  const sessionLogins = (logins || []).filter((l) => l.method === 'session')
  const none = logins !== null && logins.length === 0

  const menuItems = (l: LoginRow): MenuItem[] => {
    const cfg = !l.editable
    const items: MenuItem[] = [
      { label: 'Where it was used, and its vault entries', icon: 'runs', onClick: () => void toggleUses(l.name) },
      { label: 'Edit which bots may use it…', icon: 'edit', disabled: cfg ? CONFIG_ONLY : false, onClick: () => setBinding(l) },
    ]
    if (methodOf(l) === 'password') items.push({ label: 'Replace the password…', icon: 'updates', disabled: cfg ? CONFIG_ONLY : false, onClick: () => setReplacing(l) })
    items.push('-', { label: 'Remove this login…', icon: 'trash', disabled: cfg ? 'Defined in the config: remove it there' : false, onClick: () => setRemoving(l) })
    return items
  }

  return (
    <div class="lg-page">
      <div class="page-head">
        <div>
          <BrowserCrumbs here="Logins" />
          <h1>Logins</h1>
        </div>
        {/* With no login yet the hero below is the page: no header actions beside it. */}
        {none ? null : (
          <div class="actions">
            <a class="btn primary" href="#/browser/add"><Icon name="key" />Add a login</a>
            <RegisterPasskey />
          </div>
        )}
      </div>
      <BrowserTabs active="logins" />
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}

      {none ? (
        <EmptyState hero icon="key" title="No logins yet" testid="logins-empty" action={{ label: 'Add a login', href: '#/browser/add', testid: 'logins-empty-add' }}>
          <p>Add one so a bot can sign in to a site you choose: a username and password, an item from a vault, or a session you sign in to yourself.</p>
        </EmptyState>
      ) : (
        <div class="panel">
          <div class="panel-head"><h3>Sign-ins</h3></div>
          {logins === null ? (
            <div class="list"><div class="item small muted">{err ? 'Nothing to show until conductor answers.' : 'Loading logins…'}</div></div>
          ) : credentials.length === 0 ? (
            <div class="list"><div class="item small muted">No sign-ins yet: only saved sessions below.</div></div>
          ) : (
            <ul class="dl-list dl-conns">
              {credentials.map((l) => {
                const s = stateOf(l)
                const attn = s === 'wait' || s === 'fail'
                const f = l.fields || {}
                const parts = ['username', 'password', 'otp', 'passkey'].filter((k) => f[k])
                return (
                  <li class={'dl-conn login-row' + (attn ? ' attn' : '')} data-s={s} data-login={l.name} key={l.name}>
                    <span class="dl-ng">{attn ? <StatusMark s={s === 'wait' ? 'ember' : 'fail'} mkOnly lg label={s === 'wait' ? 'Needs you' : 'Failed'} /> : <Icon name="obj-login" />}</span>
                    <span class="dl-nt">
                      <b>{l.name} <span class="tgt">{host(originOf(l))}</span> </b>
                      <span class={attn ? 'reason' : 'dl-why'}>{whyWords(l, s)}</span>
                    </span>
                    <span class="dl-na">
                      <button class="btn sm secondary" type="button" data-testid="login-test" onClick={() => setTesting(l)}>Test</button>
                      <button class="icon-btn" type="button" aria-label={`More for ${l.name}`} data-testid="login-more" onClick={(e) => setMenu({ el: e.currentTarget as HTMLElement, l })}>
                        <Icon name="more" />
                      </button>
                    </span>
                    {open === l.name ? (
                      <div class="login-detail">
                        <dl class="dl-facts">
                          {parts.map((k) => (
                            <>
                              <dt>{PART_LABEL[k]}</dt>
                              <dd class="mono">{f[k].kind === 'stored' || f[k].kind === 'literal' ? 'typed in, stored write-only' : f[k].source}</dd>
                            </>
                          ))}
                          <dt>Used by</dt>
                          <dd>
                            {!uses[l.name]
                              ? 'Loading…'
                              : uses[l.name].length
                              ? uses[l.name].map((u, i) => (
                                  <>
                                    {i ? <br /> : null}
                                    {whoWord(u.identity)}
                                    {u.dispatch ? ` on ${u.dispatch}` : ''}, {agoText(u.at)}
                                  </>
                                ))
                              : 'Not used yet.'}
                          </dd>
                        </dl>
                      </div>
                    ) : null}
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      )}

      {none ? null : (
      <div class="panel wl-gap">
        <div class="panel-head">
          <h3>Saved sessions</h3>
          <span class="muted small">signed in yourself · state, not config</span>
        </div>
        <div class="list">
          {sessionLogins.length === 0 ? (
            <div class="item small muted">No saved sessions.</div>
          ) : (
            sessionLogins.flatMap((l) =>
              (l.sessions && l.sessions.length ? l.sessions : [null]).map((s) => (
                <div class="item" data-session-login={l.name} key={l.name + (s?.identity || '')}>
                  <span class="sc-ic sm"><Icon name="cookie" /></span>
                  <div class="grow small">
                    <b>{l.site || l.name}</b> <span class="tag">session</span>
                    <div class="mono tiny muted">
                      {s
                        ? `${s.identity} · ${s.cookies ?? 0} cookie${s.cookies === 1 ? '' : 's'} · seeded ${agoText(s.saved)} · ${s.expires ? `expires in ${daysUntil(s.expires)}` : 'expiry unknown'}`
                        : `${(l.bindings || []).join(', ')} · not signed in yet`}
                    </div>
                  </div>
                  {s ? (
                    <button class="btn sm secondary" type="button" data-testid="session-revoke" onClick={() => setRevoking({ l, s })}>Revoke</button>
                  ) : (
                    <button class="btn sm secondary" type="button" disabled={!l.editable} title={!l.editable ? CONFIG_ONLY : undefined} onClick={() => setRemoving(l)}>Remove</button>
                  )}
                </div>
              )),
            )
          )}
        </div>
      </div>
      )}

      {menu ? <Menu anchor={menu.el} items={menuItems(menu.l)} label={`More for ${menu.l.name}`} onClose={() => setMenu(null)} /> : null}
      {testing ? <TestLogin l={testing} onClose={() => { setTesting(null); load() }} /> : null}
      {binding ? <EditBinding l={binding} onClose={() => setBinding(null)} onSaved={load} /> : null}
      {replacing ? <ReplacePassword l={replacing} onClose={() => setReplacing(null)} onSaved={load} /> : null}
      {removing ? (
        <ConfirmModal
          title={`Remove ${removing.name}?`}
          body={removing.method === 'session'
            ? 'Bots can no longer use this site signed in, and its saved sessions are deleted. Takes effect at once.'
            : 'Bots can no longer use it. Values typed in for it are deleted from conductor’s vault; a vault item stays in its vault. Takes effect at once.'}
          ok="Remove"
          danger
          onOk={async () => {
            try {
              await walletOp('browser-login-remove', { name: removing.name })
              load()
            } catch (e) {
              setErr(humanError(e))
            }
          }}
          onClose={() => setRemoving(null)}
        />
      ) : null}
      {revoking ? (
        <ConfirmModal
          title={`Log out of ${revoking.l.site || revoking.l.name}?`}
          body="Deletes the saved cookie jar now. Immediate, audited."
          ok="Revoke"
          danger
          onOk={async () => {
            try {
              await walletOp('browser-logout', { identity: revoking.s.identity, site: revoking.l.site })
              load()
            } catch (e) {
              setErr(humanError(e))
            }
          }}
          onClose={() => setRevoking(null)}
        />
      ) : null}
    </div>
  )
}

function daysUntil(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'expired'
  const d = Math.round(ms / 86_400_000)
  return d >= 1 ? `${d}d` : `${Math.max(1, Math.round(ms / 3_600_000))}h`
}

// TestLogin: sign in once in a throwaway, sandboxed browser (no values and no
// screenshots of filled fields are recorded).
function TestLogin({ l, onClose }: { l: LoginRow; onClose: () => void }): JSX.Element {
  const [res, setRes] = useState<{ status: string; reason?: string } | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    walletOp('browser-login-test', { name: l.name })
      .then((r) => setRes(r as { status: string; reason?: string }))
      .catch((e) => setErr(humanError(e)))
  }, [])
  return (
    <BrModal caps="test login · sandboxed browser" title={`Testing ${l.name}`} onClose={onClose} testid="login-test-modal" foot={<button class="btn" type="button" onClick={onClose}>Close</button>}>
      <div class="small" id="lt-body">
        {err ? (
          <>
            <StatusMark s="fail" word="couldn’t test it" />
            <div class="muted tiny">{err}</div>
          </>
        ) : !res ? (
          <>
            <StatusMark s="run" word={`signing in to ${originOf(l)}…`} />
            <div class="muted tiny" style="margin-top:6px">No values and no screenshots of filled fields are recorded.</div>
          </>
        ) : res.status === 'ok' ? (
          <>
            <StatusMark s="ok" word="signed in" />
            <div class="muted tiny">origin ✓ · frame ✓ · fields ✓ · logged out again</div>
          </>
        ) : res.status === 'needs-you' ? (
          <StatusMark s="wait" word="needs you: approve the push on your phone" />
        ) : (
          <>
            <StatusMark s="fail" word="sign-in refused" />
            <div class="muted tiny">{res.reason || 'the site rejected the credentials'} · nothing was saved</div>
          </>
        )}
      </div>
    </BrModal>
  )
}

// EditBinding: the origin and who may use it. Adding a bot or a site widens, so
// the daemon asks for your passkey; removing one narrows and goes straight in.
function EditBinding({ l, onClose, onSaved }: { l: LoginRow; onClose: () => void; onSaved: () => void }): JSX.Element {
  const before = l.origins || []
  const [origin, setOrigin] = useState(before[0] || '')
  const [ids, setIds] = useState<string[]>(l.bindings || [])
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  async function save() {
    const e: Record<string, string> = {}
    if (!/^https:\/\/[\w.-]+(:\d+)?(\/[\w./-]*)?$/.test(origin.trim())) e.origin = 'https:// plus a host'
    if (!ids.length) e.who = 'Add at least one bot or team.'
    setErrs(e)
    if (Object.keys(e).length) return
    const o = origin.trim().replace(/\/$/, '')
    const was = l.bindings || []
    setBusy(true)
    setFormErr('')
    try {
      await walletOp('browser-login-bind', {
        name: l.name,
        bind: ids.filter((x) => !was.includes(x)),
        unbind: was.filter((x) => !ids.includes(x)),
        add_origins: before.includes(o) ? [] : [o],
        remove_origins: before.filter((x) => x !== o),
      })
      onSaved()
      onClose()
    } catch (err) {
      setFormErr(humanError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <BrModal
      caps={l.name}
      title={`Binding for ${l.name}`}
      onClose={onClose}
      testid="login-bind"
      foot={
        <>
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" disabled={busy} data-testid="login-bind-submit" onClick={() => void save()}><Icon name="check" />Save</button>
        </>
      }
    >
      <Field k="origin" label="Origin" req forId="lb-origin" err={errs.origin}>
        <input class="input mono" id="lb-origin" value={origin} autocomplete="off" onInput={(e) => setOrigin((e.target as HTMLInputElement).value)} />
      </Field>
      <Field k="who" label="Which bots may use it" req err={errs.who} hint="Adding a bot or a site asks for your passkey; removing one doesn’t.">
        <WhoPicker id="lb-who" value={ids} onChange={setIds} label="Which bots may use it" />
      </Field>
      <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">{formErr}</div>
    </BrModal>
  )
}

// ReplacePassword: a new value, write-only; it replaces the stored one in
// conductor's vault and never comes back.
function ReplacePassword({ l, onClose, onSaved }: { l: LoginRow; onClose: () => void; onSaved: () => void }): JSX.Element {
  const [pw, setPw] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  async function save() {
    if (!pw) return setErr('Required.')
    setBusy(true)
    try {
      await walletOp('browser-login-set', { name: l.name, field: 'password', values: { password: pw } })
      setPw('')
      onSaved()
      onClose()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <BrModal
      caps="write-only"
      title={`Replace ${l.name}'s password`}
      onClose={onClose}
      testid="login-replace"
      foot={
        <>
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" disabled={busy} data-testid="login-replace-submit" onClick={() => void save()}><Icon name="check" />Save</button>
        </>
      }
    >
      <Field k="password" label="New password" req forId="lr-pw" err={err} hint="Stored write-only in conductor’s vault; the page will say set, never the value.">
        <input class="input mono" type="password" id="lr-pw" value={pw} autocomplete="new-password" onInput={(e) => setPw((e.target as HTMLInputElement).value)} />
      </Field>
    </BrModal>
  )
}

// --- Add a login (#/browser/add) ---

type Method = 'password' | 'vault' | 'takeover'

const METHODS: Record<Method, { icon: string; title: string; desc: string }> = {
  password: { icon: 'key', title: 'Username & password', desc: "Type them once. They go to conductor's vault, write-only, with an optional TOTP secret." },
  vault: { icon: 'lock', title: 'From a vault', desc: "1Password, pass, HashiCorp or conductor's vault. Pick the item and map its fields; only the reference is stored." },
  takeover: { icon: 'hand', title: 'Sign in yourself', desc: 'A live browser: you sign in, conductor keeps the session. Best for SSO, CAPTCHA, SMS or email codes.' },
}

export function AddLogin(): JSX.Element {
  const [step, setStep] = useState<'method' | 'creds' | 'bind'>('method')
  const [method, setMethod] = useState<Method | null>(null)
  const [username, setUsername] = useState<VSValue>({ ...emptyVS() })
  const [password, setPassword] = useState<VSValue>({ ...emptyVS() })
  const [otp, setOtp] = useState<VSValue>({ ...emptyVS() })
  const [name, setName] = useState('')
  const [origin, setOrigin] = useState('')
  const [ids, setIds] = useState<string[]>([])
  const [approval, setApproval] = useState(false)
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [counts, setCounts] = useState<{ creds: number; sessions: number } | null>(null)
  const phone = typeof window !== 'undefined' && window.innerWidth <= 720
  const session = method === 'takeover'

  useEffect(() => {
    browser.logins().then((r) => {
      const ls = r.logins || []
      setCounts({ creds: ls.filter((l) => l.method !== 'session').length, sessions: ls.filter((l) => l.method === 'session').length })
    }).catch(() => {})
  }, [])

  function choose(m: Method) {
    setMethod(m)
    setStep(m === 'takeover' ? 'bind' : 'creds')
  }

  function nextFromCreds() {
    const e: Record<string, string> = {}
    const has = (v: VSValue) => !!(v.source === 'typed' ? v.value : v.ref)
    if (!has(username)) e.username = 'Required: type it, or pick where it comes from.'
    if (!has(password)) e.password = 'Required: type it, or pick where it comes from.'
    setErrs(e)
    if (Object.keys(e).length) return
    if (!name && origin) setName(host(origin).split('.')[0])
    setStep('bind')
  }

  async function save() {
    const e: Record<string, string> = {}
    const o = origin.trim().replace(/\/$/, '')
    if (!session && !/^[a-z][\w-]*$/.test(name)) e.name = 'letters, digits, - and _'
    if (session ? !/^(https:\/\/)?[\w.-]+$/.test(o) : !/^https:\/\/[\w.-]+(:\d+)?(\/[\w./-]*)?$/.test(o)) e.origin = session ? 'a host such as app.example.com' : 'https:// plus a host (and an optional path prefix)'
    if (!ids.length) e.identities = 'Add at least one bot or team.'
    setErrs(e)
    if (Object.keys(e).length) return
    setBusy(true)
    setErr('')
    try {
      if (session) {
        const site = host(o)
        await walletOp('browser-login-add', { name: name || site.split('.')[0], method: 'session', site, bindings: ids })
      } else {
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
        await walletOp('browser-login-add', { name, method: method === 'vault' ? 'vault' : 'password', origins: [o], bindings: ids, approval, values, refs })
      }
      go('browser/logins')
    } catch (er) {
      setErr(humanError(er))
    } finally {
      setBusy(false)
    }
  }

  const at = { method: 0, creds: 1, bind: 2 }[step]
  const chosen = method ? (
    <div class="choice-chosen">
      <span class="choice-ic"><Icon name={METHODS[method].icon} /></span>
      <div class="grow"><span class="caps">method</span><b>{METHODS[method].title}</b></div>
      <button class="btn sm ghost" type="button" onClick={() => setStep('method')}>Change</button>
    </div>
  ) : null

  return (
    <div class="lg-page">
      <div class="page-head">
        <div>
          <nav class="dl-crumbs" aria-label="Where you are"><a href="#/browser">Browsers</a><span class="sep" aria-hidden="true">›</span><a href="#/browser/logins">Logins</a><span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">Add a login</span></nav>
          <h1>Add a login</h1>
          <p class="sub">A site a bot may sign in to, and who may use it</p>
        </div>
      </div>
      <div class="wizard">
        <Stepper steps={['Method', 'Credentials', 'Binding', 'Review']} at={at} />
        {step === 'method' ? (
          <>
            <div class="wz-card">
              <h3>How it signs in</h3>
              <p class="lede">Every method ends the same way: exact origins, who may use it, then the config diff.</p>
              <div class="choice-grid">
                {(['password', 'vault', 'takeover'] as Method[]).map((m) => {
                  const off = m === 'takeover' && phone ? 'Signing in yourself needs a desktop browser; on the phone, add it from a vault' : ''
                  return (
                    <button type="button" class={'choice' + (method === m ? ' on' : '')} aria-pressed={method === m} aria-disabled={off ? 'true' : undefined} data-reason={off || undefined} data-testid={`method-${m}`} onClick={() => !off && choose(m)} key={m}>
                      <span class="choice-ic"><Icon name={METHODS[m].icon} /></span>
                      <span class="choice-body">
                        <span class="choice-title">{METHODS[m].title}</span>
                        <span class="choice-desc">{METHODS[m].desc}</span>
                        {off ? <span class="choice-note">{off}</span> : null}
                      </span>
                      <span class="choice-check" aria-hidden="true"><Icon name="check" /></span>
                    </button>
                  )
                })}
              </div>
              <div class="wz-notes">
                <div class="wz-note"><Icon name="lock" /><b>Values never come back</b><span>After you save, the page shows set, never the value.</span></div>
                <div class="wz-note"><Icon name="play" /><b>Test before you trust it</b><span>Test login signs in once, in a throwaway browser.</span></div>
                <div class="wz-note"><Icon name="shield" /><b>Narrow by default</b><span>Exact origins, and operator-started cases only unless you widen it.</span></div>
              </div>
            </div>
            {counts ? (
              <div class="muted small">
                {counts.creds} sign-in credential{counts.creds === 1 ? '' : 's'} and {counts.sessions} saved session{counts.sessions === 1 ? '' : 's'} already · <a class="link" href="#/browser/logins">manage them</a>
              </div>
            ) : null}
          </>
        ) : null}

        {step === 'creds' ? (
          <>
            {chosen}
            <div class="wz-card" id="la-form">
              <h3>The credentials</h3>
              <p class="lede">Each field can be typed, or come from a vault. Only references are kept; a typed password goes to the vault, write-only.</p>
              <Field k="username" label="Username" req err={errs.username}><ValueSource credential value={username} onChange={setUsername} testid="login-username" /></Field>
              <Field k="password" label="Password" req err={errs.password}><ValueSource sensitive credential value={password} onChange={setPassword} testid="login-password" /></Field>
              <Field k="otp" label="One-time code (TOTP)" hint="Optional. A typed base32 seed or otpauth:// URI; from 1Password, pick the item's one-time password field.">
                <ValueSource sensitive credential value={otp} onChange={setOtp} testid="login-otp" />
              </Field>
              <div class="wizard-foot">
                <button class="btn ghost" type="button" onClick={() => setStep('method')}>Back</button>
                <span class="spacer" />
                <button class="btn primary" type="button" data-testid="login-next" onClick={nextFromCreds}>Next: binding <Icon name="chev" /></button>
              </div>
            </div>
          </>
        ) : null}

        {step === 'bind' ? (
          <>
            {chosen}
            <div class="wz-card" id="la-bind">
              <h3>Where, and for whom</h3>
              <p class="lede">{session ? 'The saved session is used only on this site, only by these bots. Sign in to it afterwards from the machine conductor runs on.' : 'The login is filled only on this origin, only for these bots.'}</p>
              {session ? null : (
                <Field k="name" label="Login name" req forId="la-name" err={errs.name} hint={<>What a bot asks for: <code>conductor browser login --credential &lt;name&gt;</code>.</>}>
                  <input class="input mono" id="la-name" placeholder="grafana" value={name} autocomplete="off" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
                </Field>
              )}
              <Field k="origin" label={session ? 'Site' : 'Origin (exact scheme + host)'} req forId="la-origin" err={errs.origin} hint={session ? 'The host the saved session belongs to.' : 'Filled only here, checked atomically with the fill: a lookalike host, a frame or a redirect elsewhere is refused.'}>
                <input class="input mono" id="la-origin" placeholder="https://grafana.example.com" value={origin} autocomplete="off" onInput={(e) => setOrigin((e.target as HTMLInputElement).value)} />
              </Field>
              <Field k="identities" label="Which bots may use it" req err={errs.identities} hint="Access goes to bots by name. A team entry covers every bot on it.">
                <WhoPicker id="la-who" value={ids} onChange={setIds} label="Which bots may use it" />
              </Field>
              {session ? null : (
                <div class="field" data-field="approval">
                  <label class="row small"><input type="checkbox" class="cbx" checked={approval} onChange={(e) => setApproval((e.target as HTMLInputElement).checked)} /> Phone approval: the site asks for an approval on your phone after sign-in (the bot waits; you get a Needs-you item)</label>
                </div>
              )}
              <div class="callout small"><Icon name="shield" /> Used only on work <b>you</b> started: a bot's own sweep or a request from someone else can't use it. That's conductor's rule, not a setting.</div>
              <div class={'form-err callout danger small' + (err ? '' : ' hide')} role="alert">{err}</div>
              <div class="wizard-foot">
                <button class="btn ghost" type="button" onClick={() => setStep(session ? 'method' : 'creds')}>Back</button>
                <span class="spacer" />
                <button class="btn primary" type="button" disabled={busy} data-testid="login-save" onClick={() => void save()}><Icon name="check" />{busy ? 'Saving…' : 'Review & apply'}</button>
              </div>
            </div>
          </>
        ) : null}
      </div>
    </div>
  )
}
