// Personal details (design §4.5.1): a bot asks for these by what they mean, and
// conductor types them into a form; the bot never receives a value. Every field
// shows only •••••••• set and where it comes from — never a value. Adding a
// profile, a site or widening "what a bot may learn" is a widening change (a
// passkey step-up); replacing or removing a value is not.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg, go } from '../router'
import { browser, walletOp, who, type WalletProfile, type FieldView } from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs } from './Browsers'

const FIELD_WORD: Record<string, string> = {
  name: 'Name', given_name: 'First name', family_name: 'Last name', email: 'Email', phone: 'Phone',
  company: 'Company', birthday: 'Birthday', 'address.line1': 'Street', 'address.line2': 'Street line 2',
  'address.city': 'City', 'address.region': 'Region', 'address.postal_code': 'Postcode', 'address.country': 'Country',
}
function fieldWord(f: string): string {
  if (FIELD_WORD[f]) return FIELD_WORD[f]
  if (f.startsWith('billing.')) return 'Billing ' + f.slice(8)
  if (f.startsWith('custom.')) return f.slice(7)
  return f
}

// srcWord renders a field's source in words, never a value (design §4.5.1).
export function srcWord(fv: FieldView): string {
  switch (fv.kind) {
    case 'stored':
    case 'literal':
      return 'typed in, stored write-only'
    case 'env':
      return 'environment variable ' + fv.source.replace(/^\$\{env\.|\}$/g, '')
    case 'secret':
      return 'secret ' + fv.source
    case 'vault': {
      const m = /vault\s+"([^"]+)"\s+"([^"]+)"/.exec(fv.source)
      if (m) {
        const vault = m[1] === 'op' ? '1Password' : m[1] === 'house' ? 'the conductor vault' : m[1]
        return `${vault} · ${m[2]}`
      }
      return fv.source
    }
    default:
      return fv.source
  }
}

export function Details(): JSX.Element {
  const s2 = seg(2)
  if (s2 === 'new') return <NewProfile />
  if (s2) return <ProfilePage name={s2} />
  return <DetailsList />
}

function DetailsList(): JSX.Element {
  const [profiles, setProfiles] = useState<WalletProfile[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    browser.wallet().then((r) => setProfiles(r.profiles || [])).catch((e) => setErr(String(e)))
  }, [])
  return (
    <div class="col browsers">
      <div class="page-head">
        <h1 class="screen-title serif">Browsers</h1>
        <a class="btn primary sm" href="#/browser/details/new">Add personal details</a>
      </div>
      <p class="muted">A bot asks for these by what they mean, and conductor types them into the form; the bot never receives a value.</p>
      <BrowserTabs active="details" />
      {err ? <div class="banner err">{err}</div> : null}
      {profiles === null ? (
        <p class="muted">Loading…</p>
      ) : profiles.length === 0 ? (
        <div class="empty">
          <h3>No personal details yet</h3>
          <p class="muted">Add a set of details (a name, an email, an address) for a bot to fill in on the sites you choose.</p>
          <a class="btn primary sm" href="#/browser/details/new">Add personal details</a>
        </div>
      ) : (
        <ul class="dl-list">
          {profiles.map((p) => (
            <li class="dl-conn" data-profile={p.name}>
              <a class="dl-conn-main" href={`#/browser/details/${encodeURIComponent(p.name)}`}>
                <b>{p.name}</b> <span class="dl-sub">{p.fields ? Object.keys(p.fields).length : 0} detail(s)</span>
                <p class="dl-why muted">
                  For {(p.identities || []).map(who).join(', ') || 'nobody yet'}
                  {' · on '}
                  {(p.sites || []).map((s) => s.origin.replace(/^https?:\/\//, '')).join(', ') || 'no site yet'}
                </p>
              </a>
              {p.method === 'config' ? <span class="tag">In the config</span> : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ProfilePage({ name }: { name: string }): JSX.Element {
  const [p, setP] = useState<WalletProfile | null>(null)
  const [err, setErr] = useState('')
  const [replace, setReplace] = useState<string | null>(null)

  function load() {
    browser
      .wallet()
      .then((r) => setP((r.profiles || []).find((x) => x.name === name) || null))
      .catch((e) => setErr(String(e)))
  }
  useEffect(load, [name])

  if (err) return <div class="col"><a class="back" href="#/browser/details">← Personal details</a><div class="banner err">{err}</div></div>
  if (!p) return <div class="col"><p class="muted">Loading {name}…</p></div>
  const fields = p.fields || {}
  const editable = p.method !== 'config'

  return (
    <div class="col browsers wl-profile" data-profile={name}>
      <a class="back" href="#/browser/details">← Personal details</a>
      <h1 class="screen-title serif">{name}</h1>
      <p class="muted">{Object.keys(fields).length} detail(s) for {(p.identities || []).map(who).join(', ') || 'nobody yet'}</p>
      {!editable ? <p class="callout info">Defined in the config (browser.profiles.{name}). Its values change there.</p> : null}

      <section class="panel">
        <span class="panel-title">Details</span>
        <table class="wl-fields">
          <thead>
            <tr><th>Detail</th><th>Value</th><th>Where it comes from</th><th></th></tr>
          </thead>
          <tbody>
            {Object.keys(fields).sort().map((f) => (
              <tr data-field={f}>
                <td>{fieldWord(f)}</td>
                <td><span class="wl-masked" aria-label="set, hidden" data-testid="masked-value">••••••••</span> <span class="dl-sub">set</span></td>
                <td class="wl-src">{srcWord(fields[f])}</td>
                <td>{editable ? <button class="btn ghost sm" type="button" onClick={() => setReplace(f)}>Replace</button> : null}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p class="muted small">A bot asks conductor to fill a detail by what it means; conductor types it into the page and hides it from what the bot sees, even when the page shows it back.</p>
      </section>

      <div class="dl-cols wl-cols">
        <section class="panel">
          <span class="panel-title">Where it may be filled</span>
          {(p.sites || []).length === 0 ? (
            <p class="muted">Nowhere yet: nothing is filled until you add a site.</p>
          ) : (
            <ul class="dl-list">
              {(p.sites || []).map((s) => (
                <li class="wl-site"><b>{s.origin.replace(/^https?:\/\//, '')}</b><span class="dl-sub"> Fills {s.fields.map(fieldWord).join(', ')}</span></li>
              ))}
            </ul>
          )}
          <p class="muted small">Exact addresses only: a lookalike, another port or a frame from another site gets nothing.</p>
        </section>
        <section class="panel">
          <span class="panel-title">Who may use it</span>
          {(p.identities || []).length === 0 ? (
            <p class="muted">Nobody yet.</p>
          ) : (
            <ul class="dl-list">
              {(p.identities || []).map((id) => (<li class="wl-who">{who(id)} <span class="dl-sub mono">{id}</span></li>))}
            </ul>
          )}
          <p class="muted small">Only on work you asked for: a request from someone else, an event or a bot's own check gets nothing.</p>
        </section>
      </div>

      {replace ? (
        <ReplaceValue profile={name} field={replace} onClose={() => { setReplace(null); load() }} />
      ) : null}
    </div>
  )
}

function ReplaceValue({ profile, field, onClose }: { profile: string; field: string; onClose: () => void }): JSX.Element {
  const [v, setV] = useState<VSValue>({ ...emptyVS() })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  async function save() {
    setBusy(true)
    try {
      const w = vsToWire(v)
      await walletOp('browser-profile-set', {
        name: profile,
        values: w.value ? { [field]: w.value } : undefined,
        refs: w.ref ? { [field]: w.ref } : undefined,
      })
      onClose()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div class="modal-scrim" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <h3>Replace {fieldWord(field)}</h3>
        <p class="muted">The page will say set, never the value.</p>
        {err ? <div class="banner err">{err}</div> : null}
        <ValueSource sensitive value={v} onChange={setV} testid="replace-value" />
        <div class="row-actions">
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" disabled={busy} onClick={() => void save()}>Save</button>
        </div>
      </div>
    </div>
  )
}

function NewProfile(): JSX.Element {
  const [name, setName] = useState('')
  const [site, setSite] = useState('')
  const [bind, setBind] = useState('')
  const [email, setEmail] = useState<VSValue>({ ...emptyVS() })
  const [phone, setPhone] = useState<VSValue>({ ...emptyVS() })
  const [pname, setPName] = useState<VSValue>({ ...emptyVS() })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

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
      put('name', pname)
      put('email', email)
      put('phone', phone)
      const fields = Object.keys({ ...values, ...refs })
      await walletOp('browser-profile-add', {
        name,
        identities: bind.split(',').map((s) => s.trim()).filter(Boolean),
        sites: site ? [{ origin: site, fields }] : [],
        values,
        refs,
      })
      go(`browser/details/${encodeURIComponent(name)}`)
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col browsers">
      <a class="back" href="#/browser/details">← Personal details</a>
      <h1 class="screen-title serif">Add personal details</h1>
      {err ? <div class="banner err">{err}</div> : null}
      <section class="panel">
        <label class="form-field"><span>Name for this set</span>
          <input value={name} placeholder="home" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
        </label>
        <h3 class="subhead">The details</h3>
        <p class="muted">Each one typed once (stored write-only, never shown again) or from a vault item or an environment variable. Leave out what you don't need.</p>
        <ValueSource label="Name" value={pname} onChange={setPName} testid="new-name" />
        <ValueSource label="Email" value={email} onChange={setEmail} testid="new-email" />
        <ValueSource label="Phone" value={phone} onChange={setPhone} testid="new-phone" />
        <h3 class="subhead">Where, and who</h3>
        <label class="form-field"><span>Site</span>
          <input value={site} placeholder="https://shop.example.com" onInput={(e) => setSite((e.target as HTMLInputElement).value)} />
        </label>
        <label class="form-field"><span>Who may use it</span>
          <input value={bind} placeholder="bot:shopper, team:platform" onInput={(e) => setBind((e.target as HTMLInputElement).value)} />
        </label>
        <div class="row-actions">
          <button class="btn primary" type="button" disabled={busy || !name} onClick={() => void save()}>
            {busy ? 'Touch your passkey…' : 'Save with passkey'}
          </button>
        </div>
      </section>
    </div>
  )
}
