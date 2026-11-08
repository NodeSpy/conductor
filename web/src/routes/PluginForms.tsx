// The Plugins page's sheets, ported from the prototype (js/screens/catalog.js
// UI.catalogPicker, js/screens/system.js connForm/rtForm/hostForm, js/ui.js
// UI.form/UI.confirm): the "Add a …" picker, the form a kind's own
// declaration generates, the review of a plugin from a source, and the small
// confirm/permissions sheets. Every one of them ends by STAGING config (the
// operator then reviews and applies it, with a passkey for anything that
// widens); none of them installs anything or writes a value into config.
//
// Left out on purpose: the prototype's Slack/GitHub "Connect with …" app
// manifest paths. They're service-specific; the plugin contract has no way
// for a plugin to declare such a connect flow yet.
import type { ComponentChildren, JSX } from 'preact'
import { createPortal } from 'preact/compat'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import type { AddReview, CatalogField, CatalogItem, SecretsResponse } from '../api/types'
import { storeSecret } from '../api/secretwrite'
import { FactsLine, type Fact } from '../components/Facts'
import { FilterBar } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { ValueSource, type VSValue } from '../components/ValueSource'
import { cfgError, stage } from '../configStore'

export type Kind = 'connector' | 'runtime' | 'engine'

// ---------- shared bits ----------

export function Modal({ children, wide, cls, onClose, label }: { children: ComponentChildren; wide?: boolean; cls?: string; onClose: () => void; label?: string }): JSX.Element {
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('keydown', k)
    return () => document.removeEventListener('keydown', k)
  }, [onClose])
  // On <body>, as UI.modal appends it: inside the page, the page's own type
  // and button rules would restyle the sheet.
  return createPortal(
    <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div class={'modal' + (wide ? ' wide' : '') + (cls ? ' ' + cls : '')} role="dialog" aria-modal="true" aria-label={label} tabIndex={-1}>
        <span class="sheet-grip only-phone" aria-hidden="true" />
        {children}
      </div>
    </div>,
    document.body,
  )
}

export function ModalHead({ caps, title, onClose }: { caps?: string; title: string; onClose: () => void }): JSX.Element {
  return (
    <div class="modal-head">
      <div>
        {caps ? <div class="caps">{caps}</div> : null}
        <h2>{title}</h2>
      </div>
      <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
        <Icon name="x" />
      </button>
    </div>
  )
}

export function Confirm({ title, body, ok, danger, onOk, onClose }: { title: string; body: ComponentChildren; ok: string; danger?: boolean; onOk: () => void; onClose: () => void }): JSX.Element {
  return (
    <Modal onClose={onClose} label={title}>
      <ModalHead title={title} onClose={onClose} />
      <div class="modal-body small dim">{body}</div>
      <div class="modal-foot">
        <button class="btn ghost" type="button" id="cf-cancel" onClick={onClose} autoFocus={danger}>Cancel</button>
        <button class={'btn ' + (danger ? 'danger' : 'primary')} type="button" id="cf-ok" onClick={() => { onClose(); onOk() }}>{ok}</button>
      </div>
    </Modal>
  )
}

export function Permissions({ name, lines, onClose }: { name: string; lines: string[]; onClose: () => void }): JSX.Element {
  return (
    <Modal onClose={onClose} label={`What ${name} may do`}>
      <ModalHead caps="Plugins" title={`What ${name} may do`} onClose={onClose} />
      <div class="modal-body stack">
        <p class="dl-sub" style="margin:0">It runs jailed to what it declares. Anything else is denied, and an update that asks for more waits for your approval.</p>
        <ul class="dl-list perm-list" data-testid="plugin-permissions">
          {lines.map((l) => <li class="small mono" key={l}>{l}</li>)}
        </ul>
      </div>
      <div class="modal-foot"><button class="btn ghost" type="button" onClick={onClose}>Close</button></div>
    </Modal>
  )
}

// MARK16 is the conductor mark the "Built in" fact carries (ui.js MARK16).
const MARK16 = (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" class="fx-mark">
    <path fill="currentColor" d="M1 11a7 8 0 0 1 14 0h-3a4 4.8 0 0 0-8 0z" />
    <circle cx="8" cy="11" r="2.6" fill="currentColor" />
  </svg>
)

// pluginFact mirrors ui.js UI.pluginFact: each fact a plugin carries.
export const pluginFact = {
  service: (u: string): Fact => ({ icon: 'plug', value: u, label: 'Service' }),
  origin: (o: string, src?: string): Fact =>
    o === 'builtin'
      ? { icon: 'dot', glyph: MARK16, value: 'Built in', label: 'Ships with conductor' }
      : o === 'official'
        ? { icon: 'badge-check', value: 'Official', label: 'Published and verified by conductor’s maintainers' }
        : o === 'local'
          ? { icon: 'terminal', value: 'On this machine', label: src ? `From ${src}` : 'A program or build on this machine' }
          : { icon: 'git-fork', value: src ? `Custom · ${src}` : 'Custom source', tone: 'custom', label: 'From a source you added: its trust differs' },
  policy: (kind?: string, ver?: string, constraint?: string): Fact | null =>
    !kind
      ? null
      : kind === 'pinned'
        ? { icon: 'pin', value: `Held at ${ver || constraint}`, tone: 'held', label: 'Held at this version until you change it' }
        : kind === 'range'
          ? { icon: 'brackets', value: `Updates within ${constraint}`, label: `Takes new releases within ${constraint}` }
          : { icon: 'refresh-cw', value: 'Stays current', label: 'Takes every new release' },
  version: (ver?: string): Fact | null => (ver ? { icon: 'tag', value: ver, label: 'The installed version' } : null),
  latest: (ver?: string): Fact | null => (ver ? { icon: 'tag', value: ver, label: 'The installed version' } : null),
  waiting: (ver?: string): Fact | null => (ver ? { icon: 'circle-arrow-up', value: `${ver} waiting for approval`, tone: 'update', label: 'A newer build asks for more than this one: approve it with conductor plugin approve' } : null),
}

// fieldLabel is ui.js UI.fieldLabel: a declared key in words.
export const fieldLabel = (k: string) =>
  k.split('.').pop()!.replace(/_/g, ' ').replace(/\b(id|url|api|ssh|otp|tls|sha|uri|dns|ip)\b/gi, (x) => x.toUpperCase()).replace(/^./, (c) => c.toUpperCase())

const NAME_RE = /^[a-z][a-z0-9-]*$/

function Field({ k, label, req, help, err, children }: { k: string; label?: string; req?: boolean; help?: ComponentChildren; err?: string; children: ComponentChildren }): JSX.Element {
  return (
    <div class={'field' + (err ? ' bad' : '')} data-field={k}>
      <label for={'f-' + k}>
        {label ?? fieldLabel(k)} {req ? <span class="req" aria-label="required">*</span> : <span class="opt">optional</span>}
      </label>
      {children}
      {help ? <div class="hint">{help}</div> : null}
      <div class="ferr" role="alert">{err || ''}</div>
    </div>
  )
}

// KVEdit is ui.js's `map` field: key/value rows, a fresh row at the end.
function KVEdit({ k, value, onChange }: { k: string; value: [string, string][]; onChange: (v: [string, string][]) => void }): JSX.Element {
  const rows = [...value, ['', ''] as [string, string]]
  return (
    <div class="kvedit" data-mapk={k} id={'f-' + k}>
      {rows.map(([a, b], i) => (
        <div class="kvrow" key={i}>
          <input class="input mono" placeholder="key" value={a} onInput={(e) => { const n = [...value]; n[i] = [(e.target as HTMLInputElement).value, b]; onChange(n.filter((r, j) => j < value.length || r[0] || r[1])) }} />
          <input class="input mono" placeholder="value" value={b} onInput={(e) => { const n = [...value]; n[i] = [a, (e.target as HTMLInputElement).value]; onChange(n.filter((r, j) => j < value.length || r[0] || r[1])) }} />
          {i < value.length ? (
            <button type="button" class="icon-btn" aria-label="Remove row" onClick={() => onChange(value.filter((_, j) => j !== i))}><Icon name="trash" /></button>
          ) : <span style="width:30px" />}
        </div>
      ))}
    </div>
  )
}
// A value that was an object or a list in the config is shown as JSON; it
// goes back as that structure, never as a string of it.
const kvVal = (b: string): unknown => {
  if (/^\s*[[{]/.test(b)) {
    try {
      return JSON.parse(b)
    } catch {
      return b
    }
  }
  return b
}
const kvToMap = (rows: [string, string][]) => Object.fromEntries(rows.filter(([a]) => a.trim()).map(([a, b]) => [a.trim(), kvVal(b)]))
// same says two config values are equal regardless of key order.
const stable = (v: unknown): unknown => (Array.isArray(v) ? v.map(stable) : v && typeof v === 'object' ? Object.fromEntries(Object.keys(v as object).sort().map((k) => [k, stable((v as Record<string, unknown>)[k])])) : v)
const same = (a: unknown, b: unknown) => JSON.stringify(stable(a)) === JSON.stringify(stable(b))
const NO_CHANGE = 'Nothing has changed yet.'
const mapToKV = (m: unknown): [string, string][] => (m && typeof m === 'object' ? Object.entries(m as Record<string, unknown>).map(([a, b]) => [a, typeof b === 'string' ? b : JSON.stringify(b)]) : [])

// ---------- the vaults a credential can point at ----------

interface VaultSet {
  vaults: string[]
  refs: string[]
}
function useVaults(): VaultSet {
  const [v, setV] = useState<VaultSet>({ vaults: [], refs: [] })
  useEffect(() => {
    api.secrets().then((r: SecretsResponse) => {
      const refs = (r.secrets ?? []).map((s) => {
        const i = s.ref.indexOf('/')
        return i > 0 ? `{{ vault "${s.ref.slice(0, i)}" "${s.ref.slice(i + 1)}" }}` : s.ref
      })
      setV({ vaults: (r.vaults ?? []).map((x) => x.name), refs })
    }).catch(() => {})
  }, [])
  return v
}

// ---------- the form a declaration generates ----------

type FVal = string | boolean | VSValue | [string, string][]

// isRef says a config string is a reference rather than a value.
const isRef = (s: string) => /^\s*\{\{.*\}\}\s*$/.test(s) || /^\$\{(env|settings)\./.test(s.trim()) || /^env:/.test(s.trim())

function vsFrom(cur: unknown, secret: boolean): VSValue {
  if (typeof cur === 'string' && cur) {
    if (isRef(cur)) return { source: /vault/.test(cur) ? 'vault' : /settings/.test(cur) ? 'setting' : 'env', ref: cur }
    return secret ? { source: 'typed', set: true } : { source: 'typed', value: cur }
  }
  return secret ? { source: 'vault' } : { source: 'typed' }
}

// DeclFields renders the declared fields and reports values to stage.
export function declInitial(fields: CatalogField[], cur: Record<string, unknown> | null): Record<string, FVal> {
  const o: Record<string, FVal> = {}
  for (const f of fields) {
    const v = cur?.[f.key]
    if (f.secret || (f.type === 'string' && !f.enum?.length && !/path|listen|url$/.test(f.key))) o[f.key] = vsFrom(v, !!f.secret)
    else if (f.type === 'boolean') o[f.key] = v === true
    else if (f.type === 'map') o[f.key] = mapToKV(v)
    else if (f.type === 'list') o[f.key] = Array.isArray(v) ? v.join(', ') : ''
    else o[f.key] = v == null ? '' : String(v)
  }
  return o
}

function DeclField({ f, value, onChange, err, vaults }: { f: CatalogField; value: FVal; onChange: (v: FVal) => void; err?: string; vaults: VaultSet }): JSX.Element {
  let input: JSX.Element
  if (typeof value === 'object' && !Array.isArray(value)) {
    input = (
      <ValueSource value={value} onChange={onChange} sensitive={!!f.secret} testid={'vs-' + f.key} suggest={vaults.refs}
        placeholder={f.secret ? 'stored write-only in your vault; the config keeps the reference' : undefined} />
    )
  } else if (f.type === 'boolean') {
    input = <label class="row small" style="min-height:30px"><input type="checkbox" class="cbx" id={'f-' + f.key} checked={value === true} onChange={(e) => onChange((e.target as HTMLInputElement).checked)} /> on</label>
  } else if (f.enum?.length) {
    input = (
      <select class="select mono" id={'f-' + f.key} value={value as string} onChange={(e) => onChange((e.target as HTMLSelectElement).value)}>
        {!f.required ? <option value="">— none —</option> : null}
        {f.enum.map((o) => <option value={o} key={o}>{o}</option>)}
      </select>
    )
  } else if (f.type === 'map') {
    input = <KVEdit k={f.key} value={value as [string, string][]} onChange={onChange} />
  } else {
    input = (
      <input class="input mono" id={'f-' + f.key} value={value as string} inputMode={f.type === 'integer' || f.type === 'number' ? 'decimal' : undefined}
        placeholder={f.type === 'duration' ? 'e.g. 30m' : f.type === 'list' ? 'separate several with commas' : ''}
        onInput={(e) => onChange((e.target as HTMLInputElement).value)} />
    )
  }
  return (
    <Field k={f.key} req={f.required} help={f.desc} err={err}>
      {input}
    </Field>
  )
}

function checkDecl(fields: CatalogField[], vals: Record<string, FVal>): Record<string, string> {
  const errs: Record<string, string> = {}
  for (const f of fields) {
    const v = vals[f.key]
    if (typeof v === 'object' && !Array.isArray(v)) {
      if (f.secret && v.source === 'setting') errs[f.key] = "A sensitive value can't come from a setting."
      else if (f.required && !(v.ref || v.value || v.set)) errs[f.key] = 'Required.'
    } else if (f.required && (v === '' || v == null || (Array.isArray(v) && !v.length))) errs[f.key] = 'Required.'
    else if (typeof v === 'string' && v && (f.type === 'integer' || f.type === 'number') && !/^-?\d+(\.\d+)?$/.test(v) && !isRef(v)) errs[f.key] = 'A number, or a {{ reference }}.'
    else if (typeof v === 'string' && v && f.type === 'duration' && !/^\d+(ms|s|m|h|d)$/.test(v)) errs[f.key] = 'A duration such as 30s, 10m or 2h.'
  }
  return errs
}

// declValues turns the form into what goes in config. A typed secret is
// stored in the vault first (write-only, with a passkey) and only its
// reference comes back; nothing typed into a credential field is staged.
async function declValues(fields: CatalogField[], vals: Record<string, FVal>, cur: Record<string, unknown> | null, inst: string, vault: string): Promise<Record<string, unknown>> {
  const out: Record<string, unknown> = {}
  for (const f of fields) {
    const v = vals[f.key]
    if (typeof v === 'object' && !Array.isArray(v)) {
      if (v.source === 'typed') {
        if (f.secret) {
          if (v.value) out[f.key] = await storeSecret(vault, `${inst}-${f.key.replace(/_/g, '-')}`, v.value)
          else if (v.set && cur?.[f.key] !== undefined) out[f.key] = cur[f.key]
        } else if (v.value) out[f.key] = v.value
      } else if (v.ref) out[f.key] = v.ref
    } else if (Array.isArray(v)) {
      const m = kvToMap(v)
      if (Object.keys(m).length) out[f.key] = m
    } else if (typeof v === 'boolean') {
      if (v) out[f.key] = true
    } else if (v !== '') {
      out[f.key] = f.type === 'list' ? v.split(/,\s*/).filter(Boolean) : (f.type === 'integer' || f.type === 'number') && /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : v
    }
  }
  return out
}

// ---------- adding or configuring a connection ----------

export interface ConnTarget {
  use: string // the use: reference (a kind's name, or a source)
  label: string // how the service is named
  name?: string // set when configuring an existing connection
  cur?: Record<string, unknown> | null // its config entry
  fields?: CatalogField[]
  hasSchema: boolean
  review?: AddReview | null
}

export function ConnForm({ t, taken, onClose, onStaged }: { t: ConnTarget; taken: string[]; onClose: () => void; onStaged: (summary: string) => void }): JSX.Element {
  const editing = !!t.name
  const fields = t.fields ?? []
  const base = t.use.split('@')[0].split('/').pop()!.replace(/[^a-z0-9-]/g, '-')
  const firstFree = () => { let n = base, i = 2; while (taken.includes(n)) n = `${base}-${i++}`; return n }
  const [name, setName] = useState(t.name ?? firstFree())
  const [vals, setVals] = useState<Record<string, FVal>>(() => declInitial(fields, t.cur ?? null))
  const [extra, setExtra] = useState<[string, string][]>(() => (t.hasSchema ? [] : mapToKV(Object.fromEntries(Object.entries(t.cur ?? {}).filter(([k]) => k !== 'use')))))
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  const vaults = useVaults()
  const [vault, setVault] = useState('')
  const needsVault = fields.some((f) => f.secret && (vals[f.key] as VSValue)?.source === 'typed' && (vals[f.key] as VSValue)?.value)
  const rev = t.review

  async function submit() {
    const e = checkDecl(fields, vals)
    if (!editing && !NAME_RE.test(name)) e.name = 'lowercase, digits, dashes'
    else if (!editing && taken.includes(name)) e.name = 'A connection by that name exists.'
    setErrs(e)
    if (Object.keys(e).length) return
    if (needsVault && !(vault || vaults.vaults[0])) {
      setFormErr('A typed secret is stored in a vault, and there is none yet. Add a vault on the Secrets page, or point this field at an existing secret.')
      return
    }
    setBusy(true)
    setFormErr('')
    try {
      const declared = await declValues(fields, vals, t.cur ?? null, name, vault || vaults.vaults[0])
      const keep = Object.fromEntries(Object.entries(t.cur ?? {}).filter(([k]) => k !== 'use' && !fields.some((f) => f.key === k) && t.hasSchema))
      const entry: Record<string, unknown> = { use: t.cur?.use ?? t.use, ...keep, ...(t.hasSchema ? {} : kvToMap(extra)), ...declared }
      if (rev && !rev.trusted && rev.trust_entry) {
        await stage(['plugin_trust', 'allow'], [...trustAllow(), rev.trust_entry], `trust ${rev.trust_entry}`)
        if (cfgError.value) throw new Error(cfgError.value)
      }
      if (editing && same(entry, t.cur)) {
        setFormErr(NO_CHANGE)
        return
      }
      await stage(['connectors', name], entry, editing ? `configure connectors.${name}` : `add connectors.${name} (${t.label})`)
      if (cfgError.value) throw new Error(cfgError.value)
      onClose()
      onStaged(editing ? `configure connectors.${name}` : `add connectors.${name}`)
    } catch (err) {
      setFormErr(humanError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal wide onClose={onClose} label={editing ? `Configure ${name}` : `Add a ${t.label} connection`}>
      <ModalHead caps={editing ? `${t.label} connection` : 'A new connection'} title={editing ? `Configure ${name}` : `Add a ${t.label} connection`} onClose={onClose} />
      <form class="modal-body stack" id="gen-form" novalidate onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <div class="small dim"><p class="dl-sub conn-say">Secrets stay in your vault; the config only points to them.</p></div>
        <Field k="name" label="Name" req help={editing ? '' : 'How you refer to this connection, e.g. gh-work'} err={errs.name}>
          <input class="input mono" id="f-name" value={name} readOnly={editing} placeholder={base} onInput={(e) => setName((e.target as HTMLInputElement).value)} />
        </Field>
        {fields.map((f) => (
          <DeclField key={f.key} f={f} value={vals[f.key]} err={errs[f.key]} vaults={vaults} onChange={(v) => setVals({ ...vals, [f.key]: v })} />
        ))}
        {!t.hasSchema ? (
          <Field k="settings" label="Settings" help="conductor reads this service’s fields from its plugin once it’s on this machine; add the ones you know now, or come back after applying.">
            <KVEdit k="settings" value={extra} onChange={setExtra} />
          </Field>
        ) : null}
        {needsVault && vaults.vaults.length > 1 ? (
          <Field k="vault" label="Store typed secrets in" req>
            <select class="select mono" id="f-vault" value={vault || vaults.vaults[0]} onChange={(e) => setVault((e.target as HTMLSelectElement).value)}>
              {vaults.vaults.map((v) => <option value={v} key={v}>{v}</option>)}
            </select>
          </Field>
        ) : null}
        <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">{formErr}</div>
        {rev && !rev.builtin && !rev.installed ? (
          <div class="callout small" data-testid="install-note">
            The <b>{rev.plugin}</b> plugin isn’t on this machine yet: once you apply, conductor fetches it from <code>{rev.source || rev.use}</code>
            {rev.official ? ' (official registry, trusted)' : rev.trusted ? ' (a source you trust)' : ''} and runs it jailed to what it declares.
            {!rev.trusted ? <> conductor doesn’t trust that source yet, so this also adds it to <code>plugin_trust</code>; applying asks for your passkey.</> : null}
          </div>
        ) : null}
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" class="btn primary" id="gen-submit" disabled={busy} onClick={() => void submit()}>
          <Icon name="check" />Review change
        </button>
      </div>
    </Modal>
  )
}

// trustAllow is the live plugin_trust.allow list, read from the config tree.
import { config as cfgSignal } from '../configStore'
function trustAllow(): string[] {
  const tree = cfgSignal.value?.tree as Record<string, unknown> | undefined
  const pt = tree?.plugin_trust as { allow?: string[] } | undefined
  return Array.isArray(pt?.allow) ? pt!.allow : []
}

// ---------- agent runtimes ----------

export function RuntimeForm({ name, use0, review, cur, kinds, hosts, taken, onClose, onStaged }: {
  name?: string
  use0: string
  review?: AddReview | null
  cur?: Record<string, unknown> | null
  kinds: string[]
  hosts: string[]
  taken: string[]
  onClose: () => void
  onStaged: (s: string) => void
}): JSX.Element {
  const editing = !!name
  const iso = (cur?.isolation as { mode?: string } | undefined)?.mode ?? ''
  const [v, setV] = useState({ name: name ?? '', use: (cur?.use as string) ?? use0, tool: (cur?.tool as string) ?? '', default: cur?.default === true, host: (cur?.host as string) ?? '', isolation: iso })
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const set = (k: string, x: string | boolean) => setV({ ...v, [k]: x })
  const useOpts = [...new Set([...kinds, v.use].filter(Boolean))]
  async function submit() {
    const e: Record<string, string> = {}
    if (!editing && !NAME_RE.test(v.name)) e.name = 'lowercase, digits, dashes'
    else if (!editing && taken.includes(v.name)) e.name = 'Exists already.'
    if (!v.use) e.use = 'Required.'
    setErrs(e)
    if (Object.keys(e).length) return
    const n: Record<string, unknown> = { ...(cur ?? {}), use: v.use }
    for (const k of ['tool', 'host'] as const) {
      if (v[k]) n[k] = v[k]
      else delete n[k]
    }
    if (v.default) n.default = true
    else delete n.default
    if (v.isolation) n.isolation = { ...((cur?.isolation as object) ?? {}), mode: v.isolation }
    else delete n.isolation
    const nm = name ?? v.name
    if (editing && same(n, cur)) return setFormErr(NO_CHANGE)
    if (review && !review.trusted && review.trust_entry) {
      await stage(['plugin_trust', 'allow'], [...trustAllow(), review.trust_entry], `trust ${review.trust_entry}`)
      if (cfgError.value) return setFormErr(cfgError.value)
    }
    await stage(['runtimes', nm], n, editing ? `edit runtimes.${nm}` : `add runtimes.${nm}`)
    if (cfgError.value) return setFormErr(cfgError.value)
    onClose()
    onStaged(editing ? `edit runtimes.${nm}` : `add runtimes.${nm}`)
  }
  return (
    <Modal wide onClose={onClose} label={editing ? `Edit runtime ${name}` : 'Add a runtime'}>
      <ModalHead caps="Plugins · agent runtimes" title={editing ? `Edit runtime ${name}` : 'Add a runtime'} onClose={onClose} />
      <form class="modal-body stack" id="gen-form" novalidate onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <Field k="name" req err={errs.name}>
          <input class="input mono" id="f-name" value={v.name || name} readOnly={editing} onInput={(e) => set('name', (e.target as HTMLInputElement).value)} />
        </Field>
        <Field k="use" req err={errs.use}>
          <select class="select mono" id="f-use" value={v.use} onChange={(e) => set('use', (e.target as HTMLSelectElement).value)}>
            {useOpts.map((o) => <option value={o} key={o}>{o}</option>)}
          </select>
        </Field>
        {v.use === 'cli' ? (
          <Field k="tool" help="The agent command-line tool it runs, e.g. claude-code.">
            <input class="input mono" id="f-tool" value={v.tool} onInput={(e) => set('tool', (e.target as HTMLInputElement).value)} />
          </Field>
        ) : null}
        <Field k="host" label="Machine" help="Run its agents on another machine instead of this one.">
          <select class="select mono" id="f-host" value={v.host} onChange={(e) => set('host', (e.target as HTMLSelectElement).value)}>
            <option value="">— this machine —</option>
            {hosts.map((h) => <option value={h} key={h}>{h}</option>)}
          </select>
        </Field>
        <Field k="isolation" label="Isolation">
          <select class="select mono" id="f-isolation" value={v.isolation} onChange={(e) => set('isolation', (e.target as HTMLSelectElement).value)}>
            <option value="">— conductor’s default —</option>
            {['namespace', 'user', 'container', 'none'].map((o) => <option value={o} key={o}>{o}</option>)}
          </select>
        </Field>
        <Field k="default" label="Default">
          <label class="row small" style="min-height:30px"><input type="checkbox" class="cbx" id="f-default" checked={v.default} onChange={(e) => set('default', (e.target as HTMLInputElement).checked)} /> the default for new bots</label>
        </Field>
        <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">{formErr}</div>
        <div class="callout small"><code>isolation: none</code> on a runtime runs every agent it launches as you, unsandboxed. That is a widening; applying asks for your passkey.</div>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" class="btn primary" id="gen-submit" onClick={() => void submit()}><Icon name="check" />Review change</button>
      </div>
    </Modal>
  )
}

// ---------- machines ----------

export function HostForm({ name, cur, taken, onClose, onStaged }: { name?: string; cur?: Record<string, unknown> | null; taken: string[]; onClose: () => void; onStaged: (s: string) => void }): JSX.Element {
  const editing = !!name
  const [v, setV] = useState({ name: name ?? '', addr: (cur?.host as string) ?? '', user: (cur?.user as string) ?? '' })
  const [env, setEnv] = useState<[string, string][]>(mapToKV(cur?.env))
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  async function submit() {
    const e: Record<string, string> = {}
    if (!editing && !NAME_RE.test(v.name)) e.name = 'lowercase, digits, dashes'
    else if (!editing && taken.includes(v.name)) e.name = 'Exists already.'
    if (!v.addr.trim()) e.addr = 'Required.'
    if (!v.user.trim()) e.user = 'Required.'
    setErrs(e)
    if (Object.keys(e).length) return
    const n: Record<string, unknown> = { ...(cur ?? {}), host: v.addr.trim(), user: v.user.trim() }
    const m = kvToMap(env)
    if (Object.keys(m).length) n.env = m
    else delete n.env
    const nm = name ?? v.name
    if (editing && same(n, cur)) return setFormErr(NO_CHANGE)
    await stage(['hosts', nm], n, editing ? `edit hosts.${nm}` : `add hosts.${nm}`)
    if (cfgError.value) return setFormErr(cfgError.value)
    onClose()
    onStaged(editing ? `edit hosts.${nm}` : `add hosts.${nm}`)
  }
  return (
    <Modal onClose={onClose} label={editing ? `Edit host ${name}` : 'Add a host'}>
      <ModalHead caps="Plugins · machines" title={editing ? `Edit host ${name}` : 'Add a host'} onClose={onClose} />
      <form class="modal-body stack" id="gen-form" novalidate onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <Field k="name" req err={errs.name}>
          <input class="input mono" id="f-name" value={v.name || name} readOnly={editing} onInput={(e) => setV({ ...v, name: (e.target as HTMLInputElement).value })} />
        </Field>
        <Field k="addr" label="Address" req err={errs.addr}>
          <input class="input mono" id="f-addr" value={v.addr} placeholder="build-box.example.net" onInput={(e) => setV({ ...v, addr: (e.target as HTMLInputElement).value })} />
        </Field>
        <Field k="user" req err={errs.user}>
          <input class="input mono" id="f-user" value={v.user} placeholder="conductor" onInput={(e) => setV({ ...v, user: (e.target as HTMLInputElement).value })} />
        </Field>
        <Field k="env" help="Environment for commands run there.">
          <KVEdit k="env" value={env} onChange={setEnv} />
        </Field>
        <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">{formErr}</div>
        <div class="callout small">conductor signs in over SSH with this machine’s own keys and known hosts.</div>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" class="btn primary" id="gen-submit" onClick={() => void submit()}><Icon name="check" />Review change</button>
      </div>
    </Modal>
  )
}

// ---------- the version a connection or runtime takes ----------

export function RangeForm({ coll, name, plugin, ver, onClose, onStaged }: { coll: string; name: string; plugin: string; ver: string; onClose: () => void; onStaged: (s: string) => void }): JSX.Element {
  const [ma, mi] = ver.replace(/^v/, '').split('.').map((x) => parseInt(x, 10) || 0)
  const opts: [string, string][] = [[`^${ma}.${mi}`, `Up to the next major version (^${ma}.${mi}): new features and fixes`], [`~${ma}.${mi}`, `Fixes only (~${ma}.${mi}): ${ma}.${mi}.x`]]
  const [r, setR] = useState(opts[0][0])
  async function submit() {
    await stage([coll, name, 'use'], `${plugin}@${r}`, `${coll}.${name}: take releases within ${r}`)
    if (!cfgError.value) {
      onClose()
      onStaged(`${coll}.${name} within ${r}`)
    }
  }
  return (
    <Modal onClose={onClose} label={`Keep ${name} within a range`}>
      <ModalHead caps={`${coll}.${name}.use`} title={`Keep ${name} within a range`} onClose={onClose} />
      <form class="modal-body stack" id="gen-form" novalidate onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <Field k="range" label="Take new releases" req>
          <select class="select mono" id="f-range" value={r} onChange={(e) => setR((e.target as HTMLSelectElement).value)}>
            {opts.map(([a, b]) => <option value={a} key={a}>{b}</option>)}
          </select>
        </Field>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" class="btn primary" id="gen-submit" onClick={() => void submit()}>Stage the change</button>
      </div>
    </Modal>
  )
}

// ---------- the picker ----------

const ORIGW: Record<string, string> = { builtin: 'Built in', official: 'Official', custom: 'Your source', local: 'On this machine' }
const PAGE = 20

export function Picker({ kind, onPick, onSource, onClose }: { kind: Kind; onPick: (it: CatalogItem) => void; onSource: () => void; onClose: () => void }): JSX.Element {
  const [items, setItems] = useState<CatalogItem[] | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [origin, setOrigin] = useState<string[]>([])
  const [page, setPage] = useState(0)
  useEffect(() => {
    api.pluginCatalog(kind).then((r) => setItems(r.items ?? [])).catch((e) => setErr(humanError(e)))
  }, [kind])
  const what = kind === 'connector' ? 'connection' : kind === 'engine' ? 'code language' : 'runtime'
  const all = items ?? []
  // what you use first, then installed, then A to Z (catalog.js rank)
  const rank = (it: CatalogItem) => (it.configured ? 0 : it.installed && it.origin !== 'builtin' ? 1 : 2)
  const shown = useMemo(
    () => all.filter((it) => (!origin.length || origin.includes(it.origin)) && (!q || `${it.name} ${it.desc ?? ''}`.toLowerCase().includes(q.toLowerCase()))).sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name)),
    [all, q, origin],
  )
  const pages = Math.max(1, Math.ceil(shown.length / PAGE))
  const pg = Math.min(page, pages - 1)
  const xs = shown.slice(pg * PAGE, pg * PAGE + PAGE)
  const srcBtn = (
    <button type="button" class="btn sm ghost pk-src" data-act="src-add" onClick={onSource}>
      <Icon name="link" />Add from a git URL or custom source…
    </button>
  )
  return (
    <Modal wide cls="pk-modal" onClose={onClose} label={`Add a ${what}`}>
      <ModalHead caps="Plugins" title={`Add a ${what}`} onClose={onClose} />
      <div class="modal-body pk">
        <div class="pk-top">
          <FilterBar
            q={q}
            onQueryChange={(v) => { setQ(v); setPage(0) }}
            placeholder={{ connector: 'Find a service by name or what it does', engine: 'Find a code language', runtime: 'Find an agent runtime' }[kind]}
            ariaLabel="Find and filter the catalog"
            filterByHint
            menus={[{
              key: 'origin', label: 'Origin', multi: true, selected: origin,
              options: Object.keys(ORIGW).map((v) => ({ v, label: ORIGW[v], n: all.filter((it) => it.origin === v).length })).filter((o) => o.n),
              onToggle: (v) => { setOrigin(origin.includes(v) ? origin.filter((x) => x !== v) : [...origin, v]); setPage(0) },
              onClear: () => setOrigin([]),
            }]}
            end={srcBtn}
          />
        </div>
        <div id="pk-body">
          {err ? <p class="dl-sub">Couldn’t read what can be added: {err}</p> : items === null ? <p class="dl-sub">Loading…</p> : shown.length ? (
            <>
              <ul class="dl-list pk-list">
                {xs.map((it) => <PickRow key={it.name} it={it} kind={kind} onPick={() => onPick(it)} />)}
              </ul>
              {pages > 1 ? (
                <div class="pk-pages">
                  <span class="dl-sub">{pg * PAGE + 1}–{pg * PAGE + xs.length} of {shown.length}</span>
                  <button class="btn sm" type="button" disabled={!pg} title={pg ? undefined : 'The first page'} onClick={() => setPage(pg - 1)}><Icon name="chev" class="flip" />Previous</button>
                  <button class="btn sm" type="button" disabled={pg >= pages - 1} title={pg < pages - 1 ? undefined : 'The last page'} onClick={() => setPage(pg + 1)}>Next<Icon name="chev" /></button>
                </div>
              ) : null}
            </>
          ) : (
            <div class="pk-none">
              <p>Nothing in the catalog matches{q ? ` “${q}”` : ''}.</p>
              <button class="btn secondary" type="button" data-act="src-add" onClick={onSource}><Icon name="link" />Add it from a git URL or custom source…</button>
            </div>
          )}
        </div>
      </div>
    </Modal>
  )
}

function PickRow({ it, kind, onPick }: { it: CatalogItem; kind: Kind; onPick: () => void }): JSX.Element {
  const notInstalled = it.origin !== 'builtin' && !it.installed
  const n = it.configured
  const facts: (Fact | null)[] = [
    pluginFact.origin(it.origin, it.source),
    it.origin !== 'builtin' ? pluginFact.latest(it.installed) : null,
    n && kind !== 'engine' ? { icon: 'obj-connector', value: `${n} ${kind === 'connector' ? 'connection' : 'runtime'}${n > 1 ? 's' : ''} already`, label: 'In use' } : null,
    n && kind === 'engine' ? { icon: 'workflows', value: `${n} step${n > 1 ? 's' : ''} use it`, label: 'In use' } : null,
  ]
  const label = kind === 'connector' ? (notInstalled ? 'Install & configure' : 'Choose') : kind === 'runtime' ? (notInstalled ? 'Install & configure' : 'Add one') : 'Install & configure'
  return (
    <li class="pk-row" data-cat={`${kind}:${it.name}`}>
      <span class="sc-ic sm"><Icon name={kind === 'connector' ? 'plug' : kind === 'runtime' ? (it.name === 'cli' ? 'terminal' : 'server') : it.name === 'cli' ? 'terminal' : 'code'} /></span>
      <span class="dl-nt">
        <span class="pk-l1"><b>{it.name}</b>{it.desc ? <span class="dl-why">{it.desc}</span> : null}</span>
        <FactsLine facts={facts} />
      </span>
      <span class="dl-na">
        {kind === 'engine' && !notInstalled ? (
          <span class="dl-sub pk-inst">Installed</span>
        ) : (
          <button class={'btn sm' + (notInstalled ? ' secondary' : '')} type="button" data-act="cat-pick" data-kind={kind} data-name={it.name} onClick={onPick}>{label}</button>
        )}
      </span>
    </li>
  )
}

// ---------- a plugin from a source, and a code language ----------

// SourceForm asks for a `use:` reference and shows plugin add's review of it
// (where it comes from, whether conductor trusts it, what it may do).
export function SourceForm({ kind, onReviewed, onClose }: { kind: Kind; onReviewed: (r: AddReview) => void; onClose: () => void }): JSX.Element {
  const [use, setUse] = useState('')
  const [err, setErr] = useState('')
  async function check() {
    if (!use.trim()) return setErr('Required.')
    try {
      onReviewed(await api.pluginReview(kind, use.trim()))
    } catch (e) {
      setErr(humanError(e))
    }
  }
  return (
    <Modal onClose={onClose} label="Add from a source">
      <ModalHead caps="Plugins" title="Add from a git URL or custom source" onClose={onClose} />
      <form class="modal-body stack" id="gen-form" novalidate onSubmit={(e) => { e.preventDefault(); void check() }}>
        <Field k="use" label="Where it comes from" req err={err} help={<>A name from the official registry, a repository (<code>host/owner/repo</code>), or a path on this machine; add <code>@version</code> to hold one release.</>}>
          <input class="input mono" id="f-use" value={use} onInput={(e) => setUse((e.target as HTMLInputElement).value)} />
        </Field>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" class="btn primary" id="gen-submit" onClick={() => void check()}>Continue</button>
      </div>
    </Modal>
  )
}

// LanguageSheet is what adding a code language means: an engine has no
// block of its own; a code step names it. It shows plugin add's review and
// the step that uses it, and goes to the workflows.
export function LanguageSheet({ r, onClose }: { r: AddReview; onClose: () => void }): JSX.Element {
  return (
    <Modal onClose={onClose} label={`Use ${r.plugin}`}>
      <ModalHead caps="Plugins · code languages" title={`Use ${r.plugin} in a code step`} onClose={onClose} />
      <div class="modal-body stack">
        <FactsLine facts={[pluginFact.origin(r.origin, r.source), pluginFact.version(r.installed)]} />
        <p class="small" style="margin:0">
          A code language is added by naming it in a code step’s <code>use:</code>; there is nothing else to set up.
          {r.builtin ? ' It ships with conductor.' : r.installed ? ' It is already on this machine.' : ` conductor fetches it${r.source ? ` from ${r.source}` : ''} when you apply a step that uses it.`}
        </p>
        <pre class="code mono small">{r.snippet}</pre>
        {r.permissions?.length ? <ul class="dl-list perm-list">{r.permissions.map((l) => <li class="small mono" key={l}>{l}</li>)}</ul> : null}
        {!r.trusted ? <div class="callout warn small">conductor doesn’t trust <code>{r.trust_entry}</code> yet: add it to <code>plugin_trust</code> in the config before a step can use it.</div> : null}
      </div>
      <div class="modal-foot">
        <button type="button" class="btn ghost" onClick={onClose}>Close</button>
        <a class="btn primary" href="#/workflows" onClick={onClose}><Icon name="workflows" />Open the workflows</a>
      </div>
    </Modal>
  )
}
