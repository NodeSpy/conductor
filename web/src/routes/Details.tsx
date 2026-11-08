// Personal details (js/screens/wallet.js detailsPage/profilePage/newProfilePage):
// a bot asks for these by what they mean, and conductor types them into a
// form; the bot never receives a value. Every field shows only •••••••• set and
// where it comes from. Adding a set, a site, a bot or widening what a bot may
// learn widens (the daemon asks for your passkey); replacing or removing a
// value is neutral, and removing a site or a bot narrows.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg, go } from '../router'
import { browser, walletOp, type WalletProfile } from '../api/browser'
import { api } from '../api/client'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs } from './Browsers'
import { ReplaceModal } from './Cards'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { FactsLine } from '../components/Facts'
import { Menu } from '../components/Menu'
import { ConfirmModal, FormModal } from '../components/FormModal'
import { BrModal, BrCrumbs, Field, WhoPicker, off } from '../components/BrowserFields'
import { agoText, host, idOptions, listWords, loadRoster, originErr, roster, whoWord } from '../lib/browserwords'
import { FACT, fieldWord, learnWord, siteFieldWord, srcWord, visOpts, visWord } from '../lib/walletwords'
import { humanError } from '../api/errors'

export function Details(): JSX.Element {
  const s2 = seg(2)
  if (s2 === 'new') return <NewProfile />
  if (s2) return <ProfilePage name={s2} />
  return <DetailsList />
}

type AuditEntry = Record<string, unknown>
/** The audit log's fills of a profile (fill-profile, fill-form), newest first. */
function useFills(): AuditEntry[] {
  const [fills, setFills] = useState<AuditEntry[]>([])
  useEffect(() => {
    api.audit().then((r) => setFills((r.entries || []).filter((e) => e.event === 'browser' && (e.action === 'fill-profile' || e.action === 'fill-form') && !e.error))).catch(() => {})
  }, [])
  return fills
}
const learns = (p: WalletProfile): string[] => Object.entries(p.visible || {}).filter(([, v]) => v !== 'hidden').map(([f, v]) => learnWord(f, v))
const fillWords = (e: AuditEntry): string => {
  const what = e.field ? fieldWord(String(e.field)).toLowerCase() : 'a form'
  let where = ''
  try {
    where = e.url ? new URL(String(e.url)).host : ''
  } catch {
    where = ''
  }
  return `${whoWord(String(e.identity || '')).replace(/^./, (c) => c.toUpperCase())} filled ${what}${where ? ` on ${where}` : ''}`
}

function DetailsList(): JSX.Element {
  const [profiles, setProfiles] = useState<WalletProfile[] | null>(null)
  const [err, setErr] = useState('')
  const fills = useFills()
  useEffect(loadRoster, [])
  void roster.value
  useEffect(() => {
    browser.wallet().then((r) => setProfiles(r.profiles || [])).catch((e) => setErr(humanError(e)))
  }, [])
  const none = profiles !== null && profiles.length === 0
  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Browsers', '#/browser'], ['Personal details']]} />
          <h1>Personal details</h1>
          <p class="lede-2">A bot asks for these by what they mean, and conductor types them into the form; the bot never receives a value.</p>
        </div>
        {/* With nothing saved yet the hero below carries the one Add personal details. */}
        {none ? null : (
          <div class="actions">
            <a class="btn primary" href="#/browser/details/new"><Icon name="plus" />Add personal details</a>
          </div>
        )}
      </div>
      <BrowserTabs active="details" />
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      <section class="dl-card">
        {profiles === null ? (
          <p class="dl-sub">{err ? 'Nothing to show until conductor answers.' : 'Loading…'}</p>
        ) : profiles.length === 0 ? (
          <EmptyState hero icon="obj-profile" title="No personal details yet" testid="details-empty" action={{ label: 'Add personal details', href: '#/browser/details/new', testid: 'details-empty-add' }}>
            <p>Add a set of details (a name, an email, an address) for a bot to fill in on the sites you choose.</p>
          </EmptyState>
        ) : (
          <ul class="dl-list">
            {profiles.map((p) => {
              const n = Object.keys(p.fields || {}).length
              const lw = learns(p)
              const used = fills.find((e) => e.profile === p.name)
              return (
                <li class="dl-need" data-profile={p.name} key={p.name}>
                  <a class="dl-need-a" href={`#/browser/details/${encodeURIComponent(p.name)}`}>
                    <span class="dl-ng"><Icon name="obj-profile" /></span>
                    <span class="dl-nt">
                      <b>{p.name} <span class="tgt">{n} detail{n === 1 ? '' : 's'}</span></b>
                      <span class="dl-why">
                        For {listWords((p.identities || []).map(whoWord)) || 'nobody yet'} · on {listWords((p.sites || []).map((s) => host(s.origin))) || 'no site yet'}
                        {lw.length ? ` · may learn ${listWords(lw)}` : ' · learns nothing about them'}
                      </span>
                      <FactsLine facts={[p.method === 'config' ? { icon: 'obj-config', value: 'In the config', pri: 2, label: 'Where it’s defined' } : null]} />
                    </span>
                    <span class="dl-age">{used ? `used ${agoText(String(used.ts))}` : 'not used yet'}</span>
                    <span class="dl-na"><span class="btn sm secondary">Open</span></span>
                  </a>
                </li>
              )
            })}
          </ul>
        )}
      </section>
      <div class="wz-notes wl-notes">
        <div class="wz-note"><Icon name="lock" /><b>Values never come back</b><span>After you save one, the page says set, never the value; the bot asks for a detail by what it means.</span></div>
        <div class="wz-note"><Icon name="globe" /><b>Only where you say</b><span>A detail is filled only on a site you list, and only the details you allow there.</span></div>
        <div class="wz-note"><Icon name="users" /><b>Only for your own requests</b><span>A request from someone else, an event or a bot’s own check gets nothing.</span></div>
      </div>
    </div>
  )
}

const FIELDS_ALL = ['name', 'given_name', 'family_name', 'email', 'phone', 'company', 'birthday', 'address.line1', 'address.line2', 'address.city', 'address.region', 'address.postal_code', 'address.country', 'billing.line1', 'billing.line2', 'billing.city', 'billing.region', 'billing.postal_code', 'billing.country']
const fieldChoices = (p: WalletProfile): string[] => {
  const out: string[] = []
  for (const k of Object.keys(p.fields || {})) {
    const top = /^(address|billing)\./.test(k) ? k.split('.')[0] : k
    if (!out.includes(top)) out.push(top)
  }
  return out
}

type PModal =
  | { k: 'field-add' }
  | { k: 'field-set'; f: string }
  | { k: 'field-rm'; f: string }
  | { k: 'site-add' }
  | { k: 'site-rm'; o: string }
  | { k: 'who' }
  | { k: 'who-rm'; id: string }
  | { k: 'remove' }
  | null

function ProfilePage({ name }: { name: string }): JSX.Element {
  const [p, setP] = useState<WalletProfile | null | undefined>(undefined)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const [modal, setModal] = useState<PModal>(null)
  const [menu, setMenu] = useState<{ el: HTMLElement; f?: string } | null>(null)
  const fills = useFills().filter((e) => e.profile === name).slice(0, 5)
  useEffect(loadRoster, [])
  void roster.value

  function load() {
    browser.wallet().then((r) => setP((r.profiles || []).find((x) => x.name === name) || null)).catch((e) => setErr(humanError(e)))
  }
  useEffect(load, [name])
  const done = (msg: string) => { setModal(null); setOk(msg); load() }
  async function op(cmd: string, req: Record<string, unknown>, msg: string) {
    setErr('')
    try {
      await walletOp(cmd, { name, ...req })
      done(msg)
    } catch (e) {
      setModal(null)
      setErr(humanError(e))
      load()
    }
  }

  const crumbs = <BrCrumbs trail={[['Browsers', '#/browser'], ['Personal details', '#/browser/details'], [name]]} />
  if (p === undefined) return <div>{crumbs}<p class="muted">{err || `Loading ${name}…`}</p></div>
  if (p === null) return <div>{crumbs}<EmptyState icon="obj-profile" title="No such details" action={{ label: 'Personal details', href: '#/browser/details' }}><p>{name} isn’t in the wallet any more.</p></EmptyState></div>
  const ed = p.editable !== false && p.method !== 'config'
  const fields = Object.entries(p.fields || {}).sort(([a], [b]) => FIELDS_ALL.indexOf(a) - FIELDS_ALL.indexOf(b))
  const vis = p.visible || {}
  const cfgOff = ed ? {} : off(`${p.name} is defined in the config: change it there`)
  const act = (m: PModal) => () => (ed ? setModal(m) : undefined)

  return (
    <div class="wl-page" data-profile={name}>
      <div class="page-head">
        <div>
          {crumbs}
          <h1>{p.name}</h1>
          <p class="sub">{fields.length} detail{fields.length === 1 ? '' : 's'} for {listWords((p.identities || []).map(whoWord)) || 'nobody yet'}</p>
        </div>
        <div class="actions">
          <button class="btn secondary" type="button" data-testid="detail-add" {...cfgOff} onClick={act({ k: 'field-add' })}><Icon name="plus" />Add a detail</button>
          <button class="icon-btn ph-more" type="button" aria-label={`More for ${p.name}`} data-testid="profile-more" onClick={(e) => setMenu({ el: e.currentTarget as HTMLElement })}><Icon name="more" /></button>
        </div>
      </div>
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      {ok ? <div class="callout ok small" role="status">{ok}</div> : null}
      {!ed ? (
        <div class="callout small wl-cfg"><Icon name="obj-config" /><span><b>Defined in the config</b> (<code>browser.profiles.{p.name}</code>), so it changes there. <a class="dl-link" href="#/system/config">Open Config</a></span></div>
      ) : null}

      <section class="dl-card dl-flush">
        <header class="dl-ch dl-pad"><h2>Details</h2></header>
        <div class="wl-tblwrap">
          <table class="dl-tbl wl-fields">
            <thead><tr><th>Detail</th><th>Value</th><th>Where it comes from</th><th>A bot may learn</th><th><span class="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {fields.length ? (
                fields.map(([f, s]) => (
                  <tr data-field={f} key={f}>
                    <th scope="row">{fieldWord(f)}</th>
                    <td><span class="wl-masked" aria-label="set, hidden" data-testid="masked-value">••••••••</span> <span class="dl-sub">set</span></td>
                    <td class="wl-src">{srcWord(s)}</td>
                    <td>
                      {ed ? (
                        <select
                          class="select sm wl-vis"
                          aria-label={`What a bot may learn about ${fieldWord(f).toLowerCase()}`}
                          value={vis[f] || 'hidden'}
                          data-testid={`vis-${f}`}
                          onChange={(e) => {
                            const v = (e.target as HTMLSelectElement).value
                            const before = vis[f] || 'hidden'
                            ;(e.target as HTMLSelectElement).value = before
                            void op('browser-profile-bind', { visible: { [f]: v } }, `Saved: a bot may learn ${visWord(f, v)}`)
                          }}
                        >
                          {visOpts(f).map(([k, l]) => <option value={k} key={k}>{k === 'fact' ? `A fact: ${FACT[f]}` : l}</option>)}
                        </select>
                      ) : (
                        visWord(f, vis[f] || 'hidden').replace(/^./, (c) => c.toUpperCase())
                      )}
                    </td>
                    <td class="num"><button class="icon-btn" type="button" aria-label={`More for ${fieldWord(f).toLowerCase()}`} data-testid={`field-more-${f}`} {...cfgOff} onClick={(e) => (ed ? setMenu({ el: e.currentTarget as HTMLElement, f }) : undefined)}><Icon name="more" /></button></td>
                  </tr>
                ))
              ) : (
                <tr><td colSpan={5} class="dl-sub">No details yet.</td></tr>
              )}
            </tbody>
          </table>
        </div>
        <p class="dl-sub wl-foot">A bot asks conductor to fill a detail by what it means; conductor types it into the page and hides it from what the bot sees, even when the page shows it back.</p>
      </section>

      <div class="dl-cols wl-cols">
        <section class="dl-card">
          <header class="dl-ch"><h2>Where it may be filled</h2><span class="dl-sp" /><button class="btn sm secondary" type="button" data-testid="site-add" {...cfgOff} onClick={act({ k: 'site-add' })}><Icon name="plus" />Add a site</button></header>
          {(p.sites || []).length ? (
            <ul class="dl-list dl-conns wl-flushl">
              {(p.sites || []).map((s) => (
                <li class="dl-conn" key={s.origin}>
                  <span class="dl-ng"><Icon name="globe" /></span>
                  <span class="dl-nt"><b>{host(s.origin)}</b><span class="dl-why">Fills {listWords((s.fields || []).map(siteFieldWord))}</span></span>
                  <span class="dl-na"><button class="btn sm secondary" type="button" {...cfgOff} onClick={act({ k: 'site-rm', o: s.origin })}>Remove</button></span>
                </li>
              ))}
            </ul>
          ) : (
            <p class="dl-sub">Nowhere yet: nothing is filled until you add a site.</p>
          )}
          <p class="dl-sub">Exact addresses only: a lookalike, another port or a frame from another site gets nothing.</p>
        </section>
        <section class="dl-card">
          <header class="dl-ch"><h2>Who may use it</h2><span class="dl-sp" /><button class="btn sm secondary" type="button" data-testid="profile-who-add" {...cfgOff} onClick={act({ k: 'who' })}><Icon name="plus" />Add</button></header>
          {(p.identities || []).length ? (
            <ul class="dl-list dl-conns wl-flushl">
              {(p.identities || []).map((id) => (
                <li class="dl-conn" key={id}>
                  <span class="dl-ng"><Icon name={/^team:/.test(id) ? 'obj-team' : /^bot:/.test(id) ? 'obj-bot' : 'obj-sandbox'} /></span>
                  <span class="dl-nt"><b>{whoWord(id)}</b><span class="dl-why mono">{id}</span></span>
                  <span class="dl-na"><button class="btn sm secondary" type="button" {...cfgOff} onClick={act({ k: 'who-rm', id })}>Remove</button></span>
                </li>
              ))}
            </ul>
          ) : (
            <p class="dl-sub">Nobody yet.</p>
          )}
          <p class="dl-sub">Only on work you asked for: a request from someone else, an event or a bot’s own check gets nothing.</p>
        </section>
      </div>
      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>Recently filled</h2><span class="dl-sub">from the audit log; never a value</span></header>
        {fills.length ? (
          <ul class="dl-list">{fills.map((e, i) => <li class="wl-audit" key={i}><span>{fillWords(e)}</span><span class="dl-sub">{agoText(String(e.ts))}</span></li>)}</ul>
        ) : (
          <p class="dl-sub">Not filled anywhere yet.</p>
        )}
      </section>

      {menu && menu.f ? (
        <Menu
          anchor={menu.el}
          label={`More for ${fieldWord(menu.f).toLowerCase()}`}
          onClose={() => setMenu(null)}
          items={[
            { label: 'Replace the value…', icon: 'updates', onClick: () => setModal({ k: 'field-set', f: menu.f! }) },
            '-',
            { label: 'Remove this detail…', icon: 'trash', onClick: () => setModal({ k: 'field-rm', f: menu.f! }) },
          ]}
        />
      ) : menu ? (
        <Menu
          anchor={menu.el}
          label={`More for ${p.name}`}
          onClose={() => setMenu(null)}
          items={[
            { label: 'See it in the audit log', icon: 'logs', onClick: () => go('logs/audit') },
            '-',
            { label: 'Remove these details…', icon: 'trash', disabled: ed ? false : 'Defined in the config: remove it there', onClick: () => setModal({ k: 'remove' }) },
          ]}
        />
      ) : null}
      {modal?.k === 'field-set' ? (
        <ReplaceModal
          title={`Replace ${fieldWord(modal.f).toLowerCase()}`}
          label={fieldWord(modal.f)}
          onClose={() => setModal(null)}
          onSave={(w) => op('browser-profile-set', { values: w.value ? { [modal.f]: w.value } : undefined, refs: w.ref ? { [modal.f]: w.ref } : undefined }, `Saved: ${fieldWord(modal.f)} is set`)}
        />
      ) : null}
      {modal?.k === 'field-rm' ? (
        <ConfirmModal title={`Remove ${fieldWord(modal.f).toLowerCase()} from ${p.name}?`} body="Bots can no longer fill it. A typed value is deleted; a vault keeps its own." ok="Remove" danger onOk={() => op('browser-profile-unset', { field: modal.f }, `Removed: ${fieldWord(modal.f)} is no longer set`)} onClose={() => setModal(null)} />
      ) : null}
      {modal?.k === 'field-add' ? <AddDetail p={p} onClose={() => setModal(null)} onSave={(f, w) => op('browser-profile-set', { values: w.value ? { [f]: w.value } : undefined, refs: w.ref ? { [f]: w.ref } : undefined }, `Added: ${fieldWord(f)} is set`)} /> : null}
      {modal?.k === 'site-add' ? (
        <FormModal
          caps={p.name}
          title="Add a site"
          intro="Where these details may be filled, and which of them."
          fields={[
            { k: 'origin', type: 'string', req: true, mono: true, label: 'Site (its exact address)', ph: 'https://shop.example.com', validate: (v) => originErr(v.trim().replace(/\/$/, '')) },
            ...fieldChoices(p).map((f) => ({ k: 'f_' + f, type: 'seg' as const, label: f === 'address' ? 'The address' : f === 'billing' ? 'The billing address' : fieldWord(f), options: [['', 'Not here'], ['1', 'Fill it here']] as [string, string][], def: '1' })),
          ]}
          note="The browser must already be allowed to reach this site."
          submit="Add with passkey"
          submitIcon="passkey"
          testid="site-form"
          onSubmit={async (v) => {
            const fs = fieldChoices(p).filter((f) => v['f_' + f] === '1')
            if (!fs.length) return 'Pick at least one detail to fill there.'
            const origin = v.origin.trim().replace(/\/$/, '')
            try {
              await walletOp('browser-profile-bind', { name, add_sites: [{ origin, fields: fs }] })
              done(`Added: ${p.name} may be filled on ${host(origin)}`)
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
      {modal?.k === 'site-rm' ? (
        <ConfirmModal title={`Stop filling ${p.name} on ${host(modal.o)}?`} body="Nothing from these details is filled there any more. This takes effect at once." ok="Remove" danger onOk={() => op('browser-profile-bind', { remove_sites: [modal.o] }, `Removed: not filled on ${host(modal.o)} any more`)} onClose={() => setModal(null)} />
      ) : null}
      {modal?.k === 'who' ? (
        <FormModal
          caps={p.name}
          title="Let a bot or team use these details"
          fields={[{ k: 'id', type: 'enum', req: true, label: 'Who', options: idOptions(p.identities || []) }]}
          note="Only on work you asked for."
          submit="Allow with passkey"
          submitIcon="passkey"
          testid="profile-who-form"
          onSubmit={async (v) => {
            if (!v.id) return 'Pick a bot or team.'
            try {
              await walletOp('browser-profile-bind', { name, bind: [v.id] })
              done(`Allowed: ${whoWord(v.id)} may use ${p.name}`)
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
      {modal?.k === 'who-rm' ? (
        <ConfirmModal title={`Stop ${whoWord(modal.id)} using ${p.name}?`} body="It can no longer fill these details." ok="Remove" danger onOk={() => op('browser-profile-bind', { unbind: [modal.id] }, `Removed: ${whoWord(modal.id)} can’t use ${p.name} any more`)} onClose={() => setModal(null)} />
      ) : null}
      {modal?.k === 'remove' ? (
        <ConfirmModal
          title={`Remove ${p.name}?`}
          body="Its stored values are deleted, and no bot can fill these details anywhere. Values from a vault stay in the vault."
          ok="Remove"
          danger
          onOk={async () => {
            try {
              await walletOp('browser-profile-rm', { name })
              go('browser/details')
            } catch (e) {
              setModal(null)
              setErr(humanError(e))
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
    </div>
  )
}

function AddDetail({ p, onClose, onSave }: { p: WalletProfile; onClose: () => void; onSave: (f: string, w: { value?: string; ref?: string }) => Promise<void> | void }): JSX.Element {
  const free = FIELDS_ALL.filter((f) => !(p.fields || {})[f])
  const [f, setF] = useState(free[0] || 'custom')
  const [custom, setCustom] = useState('')
  const [v, setV] = useState<VSValue>({ ...emptyVS() })
  const [errs, setErrs] = useState<Record<string, string>>({})
  async function add() {
    let field = f
    const e: Record<string, string> = {}
    if (f === 'custom') {
      if (!/^[a-z0-9_]{1,40}$/.test(custom.trim())) e.c = 'Letters, digits and _, up to 40.'
      field = 'custom.' + custom.trim()
    }
    const w = vsToWire(v)
    if (!w.value && !w.ref) e.v = 'Required: type it, or pick where it comes from.'
    setErrs(e)
    if (Object.keys(e).length) return
    await onSave(field, w)
  }
  return (
    <BrModal
      caps={p.name}
      title="Add a detail"
      onClose={onClose}
      testid="detail-modal"
      foot={<><button class="btn ghost" type="button" onClick={onClose}>Cancel</button><button class="btn primary" type="button" data-testid="detail-save" onClick={() => void add()}>Add</button></>}
    >
      <Field k="f" label="Which detail" req forId="w-fa-f">
        <select class="select" id="w-fa-f" value={f} onChange={(e) => setF((e.target as HTMLSelectElement).value)}>
          {free.map((x) => <option value={x} key={x}>{fieldWord(x)}</option>)}
          <option value="custom">Something else…</option>
        </select>
      </Field>
      {f === 'custom' ? (
        <Field k="c" label="Its name" req forId="w-fa-c" err={errs.c} hint={<>Letters, digits and _; the bot asks for <code>custom.&lt;name&gt;</code>.</>}>
          <input class="input mono" id="w-fa-c" placeholder="loyalty_number" value={custom} onInput={(e) => setCustom((e.target as HTMLInputElement).value)} />
        </Field>
      ) : null}
      <Field k="v" label="Value" req err={errs.v} hint="Adding a detail doesn’t fill it anywhere yet: add it to a site below.">
        <ValueSource sensitive value={v} onChange={setV} placeholder="Type it once: stored write-only" testid="detail-value" />
      </Field>
    </BrModal>
  )
}

// --- Add personal details ---

const NEWF = ['name', 'email', 'phone', 'address.line1', 'address.city', 'address.region', 'address.postal_code', 'address.country']

function NewProfile(): JSX.Element {
  const [name, setName] = useState('')
  const [vs, setVs] = useState<Record<string, VSValue>>(() => Object.fromEntries(NEWF.map((f) => [f, emptyVS()])))
  const [origin, setOrigin] = useState('')
  const [sitef, setSitef] = useState<Record<string, boolean>>({ name: true, email: true, phone: true, address: true })
  const [ids, setIds] = useState<string[]>([])
  const [learn, setLearn] = useState<Record<string, boolean>>({})
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [existing, setExisting] = useState<string[]>([])
  useEffect(() => { browser.wallet().then((r) => setExisting((r.profiles || []).map((x) => x.name))).catch(() => {}) }, [])

  async function save() {
    const e: Record<string, string> = {}
    if (!/^[A-Za-z0-9_-]{1,40}$/.test(name)) e.name = 'Letters, digits, - and _, up to 40.'
    else if (existing.includes(name)) e.name = 'Personal details by that name exist.'
    const o = origin.trim().replace(/\/$/, '')
    if (originErr(o)) e.origin = originErr(o)
    if (!ids.length) e.who = 'Add at least one bot or team.'
    const sf = Object.keys(sitef).filter((k) => sitef[k])
    if (!sf.length) e.sitef = 'Pick at least one.'
    setErrs(e)
    if (Object.keys(e).length) return
    const values: Record<string, string> = {}
    const refs: Record<string, string> = {}
    for (const f of NEWF) {
      const w = vsToWire(vs[f])
      if (w.value) values[f] = w.value
      if (w.ref) refs[f] = w.ref
    }
    const have = Object.keys({ ...values, ...refs })
    if (!have.length) return setFormErr('Give at least one detail.')
    const visible: Record<string, string> = {}
    for (const k of Object.keys(learn)) if (learn[k] && have.includes(k)) visible[k] = 'fact'
    setBusy(true)
    setFormErr('')
    try {
      await walletOp('browser-profile-add', { name, identities: ids, sites: [{ origin: o, fields: sf }], visible, values, refs })
      go(`browser/details/${encodeURIComponent(name)}`)
    } catch (err) {
      setFormErr(humanError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Browsers', '#/browser'], ['Personal details', '#/browser/details'], ['Add personal details']]} />
          <h1>Add personal details</h1>
        </div>
      </div>
      <form class="dl-card wl-form" id="w-np" noValidate onSubmit={(e) => e.preventDefault()}>
        <Field k="name" label="Name for this set" req forId="w-np-name" err={errs.name} hint="How you and your bots refer to these details. Letters, digits, - and _.">
          <input class="input mono" id="w-np-name" placeholder="home" autocomplete="off" value={name} onInput={(e) => setName((e.target as HTMLInputElement).value.trim())} />
        </Field>
        <h3 class="wl-h">The details</h3>
        <p class="dl-sub">Each one typed once (stored write-only, never shown again) or from a vault item or an environment variable. Leave out what you don’t need.</p>
        <div class="wl-grid2">
          {NEWF.map((f) => (
            <Field k={f} label={fieldWord(f)} key={f}>
              <ValueSource sensitive value={vs[f]} onChange={(v) => setVs({ ...vs, [f]: v })} placeholder="Type it once: stored write-only" testid={`np-${f}`} />
            </Field>
          ))}
        </div>
        <h3 class="wl-h">Where, and which details</h3>
        <Field k="origin" label="Site (its exact address)" req forId="w-np-origin" err={errs.origin} hint="The browser must already be allowed to reach it. Add more sites afterwards.">
          <input class="input mono" id="w-np-origin" placeholder="https://shop.example.com" value={origin} onInput={(e) => setOrigin((e.target as HTMLInputElement).value)} />
        </Field>
        <div class={'field' + (errs.sitef ? ' bad' : '')} data-field="sitef">
          <span class="flabel">Fill there</span>
          <div class="wl-checks">
            {[['name', 'Name'], ['email', 'Email'], ['phone', 'Phone'], ['address', 'The address']].map(([k, l]) => (
              <label class="row small" key={k}><input type="checkbox" class="cbx" checked={!!sitef[k]} onChange={(e) => setSitef({ ...sitef, [k]: (e.target as HTMLInputElement).checked })} /> {l}</label>
            ))}
          </div>
          <div class="ferr" role="alert">{errs.sitef || ''}</div>
        </div>
        <h3 class="wl-h">Who may use it</h3>
        <Field k="who" label="Who may use it" req err={errs.who} hint="Bots and teams; nobody can use it until you add one. Only on work you asked for.">
          <WhoPicker id="w-who" value={ids} onChange={setIds} />
        </Field>
        <h3 class="wl-h">What a bot may learn</h3>
        <p class="dl-sub">Nothing, unless you tick it. A fact helps a bot reason without the value.</p>
        <div class="wl-checks">
          {[['email', 'The email’s domain'], ['address.country', 'The country'], ['phone', 'The phone’s country code']].map(([k, l]) => (
            <label class="row small" key={k}><input type="checkbox" class="cbx" checked={!!learn[k]} onChange={(e) => setLearn({ ...learn, [k]: (e.target as HTMLInputElement).checked })} /> {l}</label>
          ))}
        </div>
        {formErr ? <div class="form-err callout danger small" role="alert" id="w-np-err">{formErr}</div> : null}
        <div class="wizard-foot">
          <a class="btn ghost" href="#/browser/details">Cancel</a>
          <span class="spacer" />
          <button type="button" class="btn primary" disabled={busy} data-testid="profile-save" onClick={() => void save()}><Icon name="passkey" />Save with passkey</button>
        </div>
      </form>
    </div>
  )
}
