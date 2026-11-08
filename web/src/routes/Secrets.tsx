// Secrets (prototype js/screens/secrets.js + env.js): two tabs, one system.
//
// Secrets, vault first (an approved departure from the prototype's single
// list): each vault is a card with its name, its type in words, whether the
// daemon can reach it, a ⋯ menu (Edit… / Remove…, staged like any config
// change) and "Add a secret"; inside it, its entries as the prototype's
// secret rows (key and reference, used by, where the value lives, Copy, ⋯
// Replace value… / Remove…). References to a vault that isn't configured go
// in a last "Not in any vault" card. A value is write-only: it goes to the
// daemon once (POST /api/v1/secrets/write, with a step-up) and never comes
// back; this page only ever sees names and references.
//
// Settings: the prototype's read-only rows with a ⋯ menu (Change the
// value… / Copy the reference / Delete…, all staged), the environment
// variables by machine with their scope in words, and the folded "What a
// step would get".
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import { authState } from '../api/auth'
import type { EnvVarInfo, SecretInfo, SecretsResponse, SystemResponse, VaultInfo } from '../api/types'
import { humanError } from '../api/errors'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { EmptyState } from '../components/EmptyState'
import { Menu, type MenuItem } from '../components/Menu'
import { TabsBar } from '../components/TabsBar'
import { StatusMark } from '../components/StatusMark'
import { ConfirmModal, FormModal, type FormField } from '../components/FormModal'
import { config, cfgError, loadConfig, stage, stageDelete, staged } from '../configStore'
import { seg } from '../router'

type Tab = 'secrets' | 'settings'

// ---------- words ----------

const VIC: Record<string, string> = { conductor: 'lock', onepassword: 'key', pass: 'key', hashicorp: 'server', file: 'file' }
const VNAME: Record<string, string> = {
  conductor: 'conductor vault',
  onepassword: '1Password',
  pass: 'pass',
  hashicorp: 'HashiCorp Vault',
  file: 'mounted files',
}
const vname = (t: string) => VNAME[t] || t || 'vault'

// The vault types the daemon opens (internal/connector buildVaultBackend),
// with the connection keys each reads.
interface VTField {
  k: string
  label?: string
  ph?: string
  help?: string
  req?: boolean
  def?: string
}
const VT: Record<string, VTField[]> = {
  conductor: [
    { k: 'path', ph: '~/.config/conductor/vault.json (default)' },
    {
      k: 'unlock.key',
      label: 'Unlock · key',
      ph: 'creds:house',
      help: 'creds:, keyring:, env:VAR or file:/path. Empty: the default chain ($CONDUCTOR_VAULT_KEY, systemd creds, keyring, vault.key).',
    },
  ],
  onepassword: [
    { k: 'account' },
    { k: 'service_account', label: 'Service account', ph: 'env:OP_SA', help: 'A bootstrap ref → OP_SERVICE_ACCOUNT_TOKEN.' },
  ],
  pass: [{ k: 'prefix', def: 'conductor' }],
  file: [{ k: 'dir', req: true, ph: '/run/secrets', help: 'A mounted secrets directory (systemd LoadCredential, docker, k8s).' }],
  hashicorp: [
    { k: 'addr', req: true, ph: 'https://vault.internal:8200' },
    { k: 'mount', def: 'secret' },
    { k: 'namespace' },
    { k: 'unlock.token', label: 'Unlock · token', req: true, ph: 'env:VAULT_TOKEN', help: 'Where the token that opens it lives.' },
  ],
}
const vtKey = (t: string, k: string) => `${t}__${k.replace('.', '_')}`

const refOf = (vault: string, key: string) => `{{ vault "${vault}" "${key}" }}`

/** A config path in words: what uses a secret or a setting. */
function useWords(u: string): string {
  let m: RegExpMatchArray | null
  if ((m = u.match(/^connectors\.([\w-]+)\.(.+)$/))) return `${m[1]} connection · ${m[2].replace(/[._]/g, ' ')}`
  if ((m = u.match(/^packs\.([\w-]+)\.secrets\.(.+)$/))) return `${m[1]} pack · ${m[2].replace(/_/g, ' ')}`
  if ((m = u.match(/^browser\.credentials\.([\w-]+)\.(.+)$/))) return `${m[1]} login · ${m[2].replace(/_/g, ' ')}`
  if ((m = u.match(/^runtimes\.([\w-]+)\.(.+)$/))) return `${m[1]} runtime · ${m[2].replace(/[._]/g, ' ')}`
  if ((m = u.match(/^hosts\.([\w-]+)\.env\.(.+)$/))) return `${m[2]} on the machine ${m[1]}`
  if ((m = u.match(/^settings\.(.+)$/))) return `the setting ${m[1]}`
  return u
}

/** How a vault is unlocked, in words, from its config entry. */
function unlockWords(type: string, cfg: Record<string, unknown> | undefined): string {
  const u = cfg?.unlock as unknown
  const ref = typeof u === 'string' ? u : ((u as { key?: string; token?: string } | undefined)?.key ?? (u as { token?: string } | undefined)?.token ?? '')
  if (!ref) return type === 'conductor' ? 'the default unlock chain' : ''
  const m = String(ref).match(/^(\w+):(.*)$/)
  if (!m) return 'a key written in the config'
  const [, kind, rest] = m
  if (kind === 'creds') return `the systemd credential “${rest || 'conductor-vault-key'}”`
  if (kind === 'keyring') return `the OS keyring${rest ? ` (${rest})` : ''}`
  if (kind === 'env') return `the ${rest} environment variable`
  if (kind === 'file') return `the file ${rest}`
  return ref
}

const SECRETISH = /TOKEN|SECRET|PASSWORD|PASSWD|KEY|CREDENTIAL/
const scopeWords = (s: string) => s.replace(/^runtime:/, 'agents on the runtime ').replace(/^host:/, 'commands on the machine ')
const scopeLabel = (s: string) => s.replace(':', ' ')
const isRef = (v: string) => /^\{\{|\$\{|^env:/.test(v)

// ---------- data shaping ----------

function vaultCfg(name: string): Record<string, unknown> | undefined {
  const t = config.value?.tree as Record<string, unknown> | undefined
  return ((t?.vaults as Record<string, unknown> | undefined)?.[name] as Record<string, unknown>) ?? undefined
}

function getPath(o: Record<string, unknown> | undefined, k: string): string {
  let cur: unknown = o
  for (const p of k.split('.')) cur = (cur as Record<string, unknown> | undefined)?.[p]
  return cur == null ? '' : String(cur)
}

function hostsOf(): string[] {
  const t = config.value?.tree as Record<string, unknown> | undefined
  return Object.keys((t?.hosts as Record<string, unknown>) ?? {}).sort()
}

function envValue(e: EnvVarInfo): string {
  const t = config.value?.tree as Record<string, unknown> | undefined
  const [kind, name] = e.scope.split(':')
  if (kind !== 'host') return e.ref ?? ''
  const env = ((t?.hosts as Record<string, Record<string, unknown>> | undefined)?.[name]?.env ?? {}) as Record<string, unknown>
  const v = env[e.name]
  return v == null ? e.ref ?? '' : String(v)
}

const isStaged = (path: string[]) => staged.value.some((c) => path.every((p, i) => c.path[i] === p))

// ---------- the screen ----------

type Modal =
  | { kind: 'vault'; edit?: string; name?: string }
  | { kind: 'vault-del'; name: string }
  | { kind: 'secret'; vault?: string; replace?: SecretInfo }
  | { kind: 'secret-del'; s: SecretInfo }
  | { kind: 'secret-ro'; v: VaultInfo }
  | { kind: 'setting'; name?: string }
  | { kind: 'setting-del'; name: string }
  | { kind: 'env'; scope?: string; name?: string }
  | { kind: 'env-del'; e: EnvVarInfo }
  | { kind: 'env-move'; e: EnvVarInfo }
  | { kind: 'env-import' }

export function Secrets(): JSX.Element {
  const [data, setData] = useState<SecretsResponse | null>(null)
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [modal, setModal] = useState<Modal | null>(null)

  const load = () => {
    api.secrets().then(setData).catch((e) => setErr(humanError(e)))
    api.system().then(setSys).catch(() => undefined)
    void loadConfig()
  }
  useEffect(load, [])

  const tab: Tab = seg(1) === 'settings' ? 'settings' : 'secrets'
  const vaults = data?.vaults ?? []
  const secrets = data?.secrets ?? []
  const settingsN = Object.keys(sys?.settings ?? {}).length + (sys?.env_vars?.length ?? 0)
  const done = (m: string) => {
    setMsg(m)
    setErr('')
    load()
  }
  // A staged config change goes to the review, like every change.
  const toReview = () => {
    location.hash = '#/system/config/apply'
  }

  return (
    <>
      <div class="dl-phead">
        <div>
          <h1>Secrets</h1>
        </div>
      </div>
      <TabsBar
        items={[
          { id: 'secrets', label: 'Secrets', href: '#/secrets', count: data ? secrets.length : null },
          { id: 'settings', label: 'Settings', href: '#/secrets/settings', count: sys ? settingsN : null },
        ]}
        active={tab}
        ariaLabel="Secrets"
      />
      {tab === 'secrets' ? (
        <p class="sec-explain">
          <Icon name="lock" />
          <span>
            <b>A secret</b> is write-only: stored in a vault and never shown again, not here and not to an agent. A field uses it as{' '}
            <code>{'{{ vault "house" "gh-app-key" }}'}</code>.
          </span>
        </p>
      ) : (
        <p class="sec-explain">
          <Icon name="settings" />
          <span>
            <b>A setting</b> is a plain value your config reads, visible to anyone who can read the config:{' '}
            <code>{'${settings.org}'}</code>, or an environment variable as <code>{'${env.NAME}'}</code>.
          </span>
        </p>
      )}
      {err || cfgError.value ? (
        <div class="callout danger small" role="alert" data-testid="secrets-error" style="margin-bottom:14px">
          <Icon name="warn" />
          <span class="grow">{err || cfgError.value}</span>
        </div>
      ) : null}
      {msg ? (
        <div class="callout ok small" role="status" data-testid="secrets-msg" style="margin-bottom:14px">
          <Icon name="check" />
          <span class="grow">{msg}</span>
          <button class="btn sm ghost" type="button" onClick={() => setMsg('')}>
            Dismiss
          </button>
        </div>
      ) : null}

      {data === null ? (
        <p class="dl-sub">Loading…</p>
      ) : tab === 'secrets' ? (
        <SecretsTab data={data} open={setModal} />
      ) : (
        <SettingsTab sys={sys} open={setModal} />
      )}

      {modal ? (
        <Modals
          m={modal}
          vaults={vaults}
          secrets={secrets}
          sys={sys}
          open={setModal}
          close={() => setModal(null)}
          done={done}
          fail={(e) => {
            setMsg('')
            setErr(e)
          }}
          toReview={toReview}
        />
      ) : null}
    </>
  )
}

// ---------- the Secrets tab ----------

function SecretsTab({ data, open }: { data: SecretsResponse; open: (m: Modal) => void }): JSX.Element {
  const vaults = data.vaults ?? []
  const secrets = data.secrets ?? []
  const [q, setQ] = useState('')
  const [fVault, setFVault] = useState<string[]>([])
  const [fKept, setFKept] = useState<string[]>([])
  const [fUsed, setFUsed] = useState<string[]>([])
  const known = new Set(vaults.map((v) => v.name))
  const stagedNew = staged.value
    .filter((c) => c.path[0] === 'vaults' && c.path.length >= 2 && !c.delete && !known.has(c.path[1]))
    .map((c) => c.path[1])
    .filter((n, i, a) => a.indexOf(n) === i)

  const toggle = (set: (f: (x: string[]) => string[]) => void) => (v: string) => set((x) => (x.includes(v) ? x.filter((y) => y !== v) : [...x, v]))
  const match = (s: SecretInfo) =>
    (!fVault.length || fVault.includes(s.vault)) &&
    (!fKept.length || fKept.some((k) => (k === 'here' ? !!s.stored : !s.stored))) &&
    (!fUsed.length || fUsed.some((k) => (k === 'used' ? !!s.used_by?.length : !s.used_by?.length))) &&
    (!q || `${s.key} ${s.ref} ${(s.used_by ?? []).join(' ')} ${(s.used_by ?? []).map(useWords).join(' ')}`.toLowerCase().includes(q.toLowerCase()))
  const filtering = !!(q || fVault.length || fKept.length || fUsed.length)
  const menus: FilterMenu[] = [
    {
      key: 'vault',
      label: 'Vault',
      multi: true,
      selected: fVault,
      options: vaults.map((v) => ({ v: v.name, label: `${v.name} · ${vname(v.type)}`, n: secrets.filter((s) => s.vault === v.name).length })),
      onToggle: toggle(setFVault),
      onClear: () => setFVault([]),
    },
    {
      key: 'kept',
      label: 'Stored',
      multi: true,
      selected: fKept,
      options: [
        { v: 'here', label: 'Stored in the vault', n: secrets.filter((s) => s.stored).length },
        { v: 'external', label: 'Only referenced', n: secrets.filter((s) => !s.stored).length },
      ],
      onToggle: toggle(setFKept),
      onClear: () => setFKept([]),
    },
    {
      key: 'used',
      label: 'Used',
      multi: true,
      selected: fUsed,
      options: [
        { v: 'used', label: 'Used', n: secrets.filter((s) => s.used_by?.length).length },
        { v: 'unused', label: 'Unused', n: secrets.filter((s) => !s.used_by?.length).length },
      ],
      onToggle: toggle(setFUsed),
      onClear: () => setFUsed([]),
    },
  ]
  const bad = vaults.filter((v) => v.status === 'fail')
  const dangling = secrets.filter((s) => s.vault && !known.has(s.vault)).filter(match)
  const shownVaults = vaults.filter((v) => !fVault.length || fVault.includes(v.name))
  const noVaults = !vaults.length && !stagedNew.length
  const countWords = !vaults.length
    ? 'none yet'
    : `${vaults.length} vault${vaults.length === 1 ? '' : 's'}${bad.length ? ` · ${bad.length === 1 ? 'one can’t' : `${bad.length} can’t`} be reached` : vaults.every((v) => v.status === 'ok') ? ', all reachable' : ''}`

  return (
    <>
      {bad.length ? (
        <div class="callout danger small" style="margin-bottom:14px" data-testid="vault-fail-banner">
          <StatusMark s="fail" />
          <span class="grow">
            <b>{bad[0].name} can’t be reached:</b> {bad[0].reason || 'it didn’t open'}.{' '}
            {secrets.filter((s) => s.vault === bad[0].name && s.used_by?.length).length
              ? 'Anything that uses it fails until it opens.'
              : `Nothing uses ${bad[0].name} yet, so no connection is affected.`}
          </span>
          <a class="btn sm secondary" href={`#/secrets`} onClick={(e) => { e.preventDefault(); document.getElementById('vault-' + bad[0].name)?.scrollIntoView({ block: 'start' }) }}>
            See the vault
          </a>
        </div>
      ) : null}
      {noVaults && !dangling.length ? null : (
        <FilterBar q={q} onQueryChange={setQ} placeholder="Find a secret by name, vault or what uses it" ariaLabel="Find and filter secrets" menus={menus} filterByHint />
      )}

      <section class="vaults" id="vaults">
        <div class="section-title sec-title">
          <h2>Your vaults</h2>
          {/* With no vault yet the hero below carries the one Add a vault, so the header has no count and no button of its own. */}
          {noVaults ? null : (
            <>
              <span class="dl-sub" data-testid="vaults-count">{countWords}</span>
              <button class="btn sm secondary vaults-add" type="button" data-testid="add-vault" onClick={() => open({ kind: 'vault' })}>
                <Icon name="plus" />
                Add a vault
              </button>
            </>
          )}
        </div>

        {noVaults ? (
          <section class="dl-card">
            <EmptyState
              hero
              icon="lock"
              testid="vaults-empty"
              title="No vaults yet"
              action={{ label: 'Add a vault', onClick: () => open({ kind: 'vault' }), testid: 'add-vault-empty' }}
            >
              <p>Secrets live in a vault, so add one first: conductor can keep its own, or read 1Password, pass, HashiCorp Vault or a mounted folder.</p>
            </EmptyState>
          </section>
        ) : null}

        {shownVaults.map((v) => (
          <VaultCard key={v.name} v={v} rows={secrets.filter((s) => s.vault === v.name).filter(match)} filtering={filtering} open={open} />
        ))}
        {stagedNew.map((n) => (
          <section class="dl-card vault-card" id={`vault-${n}`} key={n}>
            <header class="dl-ch">
              <h2>{n}</h2>
              <StatusMark s="staged" word="Added, not applied" />
              <span class="dl-sp"></span>
              <a class="btn sm secondary" href="#/system/config/apply">
                Review the change
              </a>
            </header>
            <p class="dl-sub" style="margin:0">It opens once the change is applied; then you can store secrets in it.</p>
          </section>
        ))}

        {dangling.length ? (
          <section class="dl-card vault-card bad" data-testid="not-in-vault">
            <header class="dl-ch">
              <h2>Not in any vault</h2>
              <StatusMark s="wait" word={`${dangling.length} reference${dangling.length === 1 ? '' : 's'}`} />
            </header>
            <p class="dl-sub" style="margin:0 0 8px">
              The config reads {dangling.length === 1 ? 'this secret' : 'these secrets'} from a vault that isn’t set up, so{' '}
              {dangling.length === 1 ? 'it' : 'they'} can’t be read. Add a vault with that name, or change the config to use a vault you have.
            </p>
            <ul class="dl-list sec-list">
              {dangling.map((s) => (
                <li class={'sec-row' + (s.used_by?.length ? '' : ' unused')} key={s.ref} data-secret={s.ref}>
                  <div class="sec-line">
                    <div class="sec-main">
                      <span class="sec-name">
                        <b>{s.key}</b>
                        <span class="sec-ref mono">{refOf(s.vault, s.key)}</span>
                      </span>
                      <span class="sec-uses">
                        {s.used_by?.length ? (
                          <>
                            <span class="dl-sub">Used by</span> {s.used_by.map(useWords).join(', ')}
                          </>
                        ) : (
                          <span class="tag">Unused</span>
                        )}
                      </span>
                      <span class="sec-when">
                        <StatusMark s="wait" word={`no vault named ${s.vault}`} />
                      </span>
                    </div>
                    <button class="btn sm secondary" type="button" data-testid={`add-missing-vault-${s.vault}`} onClick={() => open({ kind: 'vault', name: s.vault })}>
                      <Icon name="plus" />
                      Add vault {s.vault}
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          </section>
        ) : null}
        <div class="panel-foot small muted sec-foot">
          <Icon name="eyeoff" /> No screen, API route or log line returns a secret’s value; values are redacted from logs and the audit trail.
        </div>
      </section>
    </>
  )
}

function VaultCard({ v, rows, filtering, open }: { v: VaultInfo; rows: SecretInfo[]; filtering: boolean; open: (m: Modal) => void }): JSX.Element {
  const [menu, setMenu] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const how = unlockWords(v.type, vaultCfg(v.name))
  const pending = isStaged(['vaults', v.name])
  const canStore = v.writable && v.status === 'ok'
  const storeOff = !v.writable
    ? `Its values are set in ${vname(v.type)} itself`
    : v.status === 'fail'
      ? 'The vault can’t be reached'
      : v.status !== 'ok'
        ? 'The vault isn’t open yet: apply the change that adds it'
        : ''
  const items: MenuItem[] = [
    { label: 'Edit…', icon: 'edit', onClick: () => open({ kind: 'vault', edit: v.name }) },
    '-',
    { label: 'Remove…', icon: 'trash', onClick: () => open({ kind: 'vault-del', name: v.name }) },
  ]
  return (
    <section class={'dl-card vault-card' + (v.status === 'fail' ? ' bad' : '')} id={`vault-${v.name}`} data-testid={`vault-${v.name}`}>
      <header class="dl-ch">
        <Icon name={VIC[v.type] || 'secrets'} />
        <h2>{v.name}</h2>
        <span class="dl-sub">{vname(v.type)}</span>
        {v.status === 'fail' ? (
          <StatusMark s="fail" word="Can’t be reached" />
        ) : v.status === 'ok' ? (
          <StatusMark s="ok" word="Reachable" />
        ) : (
          <StatusMark s="wait" word="Not open yet" />
        )}
        {pending ? <StatusMark s="staged" word="Changed, not applied" /> : null}
        <span class="dl-sp"></span>
        <button
          class="btn sm secondary"
          type="button"
          data-testid={`add-secret-${v.name}`}
          title={storeOff || undefined}
          onClick={() => open(canStore ? { kind: 'secret', vault: v.name } : { kind: 'secret-ro', v })}
        >
          <Icon name="plus" />
          Add a secret
        </button>
        <button ref={btn} class="icon-btn" type="button" aria-label={`More for vault ${v.name}`} data-testid={`vault-more-${v.name}`} onClick={() => setMenu((o) => !o)}>
          <Icon name="more" />
        </button>
        {menu ? <Menu anchor={btn.current} items={items} onClose={() => setMenu(false)} label={`More for vault ${v.name}`} /> : null}
      </header>
      <p class="dl-sub vault-d" style="margin:0 0 6px">
        {v.status === 'fail'
          ? `${v.reason || 'It didn’t open'}.`
          : v.status !== 'ok'
            ? 'It opens when conductor next reloads its config.'
            : `${v.writable ? 'conductor can store values here' : 'Values are set in the vault itself'}${how ? `; unlocked with ${how}` : ''}.`}
        {storeOff && v.status === 'ok' ? ' Add a secret there, then use it here by its reference.' : ''}
      </p>
      <ul class="dl-list sec-list">
        {rows.length ? (
          rows.map((s) => <SecretRow key={s.ref} s={s} v={v} open={open} />)
        ) : (
          <li class="dl-empty-row dl-sub">
            {filtering
              ? 'No secret here matches.'
              : v.listed || canStore
                ? `Nothing is stored in ${v.name} yet.`
                : `Nothing in the config reads from ${v.name} yet.`}
          </li>
        )}
      </ul>
    </section>
  )
}

function CopyBtn({ value, what }: { value: string; what: string }): JSX.Element {
  const [copied, setCopied] = useState(false)
  return (
    <button
      class="btn sm ghost sec-copy"
      type="button"
      aria-label={`Copy the reference to ${what}`}
      onClick={() => {
        void navigator.clipboard?.writeText(value).catch(() => undefined)
        setCopied(true)
        setTimeout(() => setCopied(false), 1600)
      }}
    >
      <Icon name={copied ? 'check' : 'copy'} />
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}

function SecretRow({ s, v, open }: { s: SecretInfo; v: VaultInfo; open: (m: Modal) => void }): JSX.Element {
  const [menu, setMenu] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const ref = refOf(s.vault, s.key)
  const uses = s.used_by ?? []
  const writable = v.writable && v.status === 'ok'
  const items: MenuItem[] = [
    {
      label: 'Replace value…',
      icon: 'edit',
      disabled: !v.writable ? `Its value is set in ${vname(v.type)}` : v.status !== 'ok' ? 'The vault can’t be reached' : false,
      onClick: () => open({ kind: 'secret', vault: s.vault, replace: s }),
    },
    { label: 'Copy the reference', icon: 'copy', onClick: () => void navigator.clipboard?.writeText(ref).catch(() => undefined) },
    '-',
    {
      label: 'Remove…',
      icon: 'trash',
      disabled: uses.length
        ? `Still used by ${uses.map(useWords).join(', ')}: point those elsewhere first`
        : !v.writable
          ? `It lives in ${vname(v.type)}: remove it there`
          : !s.stored
            ? 'Nothing is stored under this key'
            : v.status !== 'ok'
              ? 'The vault can’t be reached'
              : false,
      onClick: () => open({ kind: 'secret-del', s }),
    },
  ]
  const where = s.stored ? (
    <span class="dl-sub">
      <Icon name={VIC[v.type] || 'secrets'} />
      stored in {vname(v.type)}
    </span>
  ) : v.listed || writable ? (
    <StatusMark s="wait" word="Not stored yet" />
  ) : (
    <span class="dl-sub">
      <Icon name={VIC[v.type] || 'secrets'} />
      kept in {vname(v.type)}
    </span>
  )
  return (
    <li class={'sec-row' + (uses.length ? '' : ' unused')} data-secret={s.ref}>
      <div class="sec-line">
        <div class="sec-main">
          <span class="sec-name">
            <b>{s.key}</b>
            <span class="sec-ref mono">{ref}</span>
          </span>
          <span class="sec-uses">
            {uses.length ? (
              <>
                <span class="dl-sub">Used by</span> {uses.map(useWords).join(', ')}
              </>
            ) : (
              <span class="tag">Unused</span>
            )}
          </span>
          <span class="sec-when">{where}</span>
        </div>
        <CopyBtn value={ref} what={s.key} />
        <button ref={btn} class="icon-btn" type="button" aria-label={`More for ${s.key}`} data-testid={`secret-more-${s.ref}`} onClick={() => setMenu((o) => !o)}>
          <Icon name="more" />
        </button>
        {menu ? <Menu anchor={btn.current} items={items} onClose={() => setMenu(false)} label={`More for ${s.key}`} /> : null}
      </div>
    </li>
  )
}

// ---------- the Settings tab ----------

function SettingsTab({ sys, open }: { sys: SystemResponse | null; open: (m: Modal) => void }): JSX.Element {
  const [q, setQ] = useState('')
  const [fKind, setFKind] = useState<string[]>([])
  const [fScope, setFScope] = useState<string[]>([])
  const [fUsed, setFUsed] = useState<string[]>([])
  const settings = Object.entries(sys?.settings ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const usedBy = sys?.settings_used_by ?? {}
  const env = sys?.env_vars ?? []
  const toggle = (set: (f: (x: string[]) => string[]) => void) => (v: string) => set((x) => (x.includes(v) ? x.filter((y) => y !== v) : [...x, v]))
  const keep = (kind: string, name: string, text: string, used: boolean, scope = '') =>
    (!fKind.length || fKind.includes(kind)) &&
    (!fUsed.length || fUsed.includes(used ? 'used' : 'unused')) &&
    (!fScope.length || kind !== 'env' || fScope.includes(scope.split(':')[0])) &&
    (!q || `${name} ${text}`.toLowerCase().includes(q.toLowerCase()))
  const menus: FilterMenu[] = [
    {
      key: 'kind',
      label: 'Kind',
      multi: true,
      selected: fKind,
      options: [
        { v: 'setting', label: 'Settings', n: settings.length },
        { v: 'env', label: 'Environment variables', n: env.length },
      ],
      onToggle: toggle(setFKind),
      onClear: () => setFKind([]),
    },
    {
      key: 'scope',
      label: 'For',
      multi: true,
      selected: fScope,
      options: [
        ['runtime', 'A runtime’s agents'],
        ['host', 'A machine’s commands'],
      ]
        .map(([v, label]) => ({ v, label, n: env.filter((e) => e.scope.split(':')[0] === v).length }))
        .filter((o) => o.n),
      onToggle: toggle(setFScope),
      onClear: () => setFScope([]),
    },
    {
      key: 'used',
      label: 'Used',
      multi: true,
      selected: fUsed,
      options: [
        { v: 'used', label: 'Used in config' },
        { v: 'unused', label: 'Not used yet' },
      ],
      onToggle: toggle(setFUsed),
      onClear: () => setFUsed([]),
    },
  ]
  const pendN = staged.value.filter((c) => c.path[0] === 'settings' || (c.path[0] === 'hosts' && c.path[2] === 'env')).length
  const sRows = settings.filter(([k, v]) => keep('setting', k, v, !!usedBy[k]?.length))
  const eRows = env.filter((e) => keep('env', e.name, envValue(e), !!e.used_by?.length, e.scope))
  const plain = env.filter((e) => e.literal && SECRETISH.test(e.name))
  const showS = !fKind.length || fKind.includes('setting')
  const showE = !fKind.length || fKind.includes('env')

  return (
    <>
      <FilterBar q={q} onQueryChange={setQ} placeholder="Find a setting or a variable by name or value" ariaLabel="Find and filter settings and environment variables" menus={menus} filterByHint />
      {pendN ? (
        <div class="set-review" role="status">
          <span>
            <b>
              {pendN} change{pendN > 1 ? 's' : ''}
            </b>
            , not applied yet. Each is checked on a copy of the config before it applies.
          </span>
          <a class="btn sm primary" href="#/system/config/apply">
            Review {pendN}
          </a>
        </div>
      ) : null}
      {showS ? (
        <section id="settings">
          <div class="section-title">
            <h2>Settings</h2>
            <span class="caps">read in config as <code>{'${settings.NAME}'}</code></span>
            <button class="btn sm secondary vaults-add" type="button" data-testid="add-setting" onClick={() => open({ kind: 'setting' })}>
              <Icon name="plus" />
              Add a setting
            </button>
          </div>
          <div class="panel">
            <ul class="dl-list sec-list">
              {sRows.length ? (
                sRows.map(([k, v]) => <SettingRow key={k} name={k} value={v} used={usedBy[k]?.length ?? 0} open={open} />)
              ) : (
                <li class="dl-empty-row dl-sub" data-testid="settings-empty">{settings.length ? 'No setting matches.' : 'No settings yet: add one to share a value across your config.'}</li>
              )}
            </ul>
          </div>
        </section>
      ) : null}
      {showE ? (
        <section id="env">
          <div class="section-title">
            <h2>Environment variables</h2>
            <span class="caps">read in config as <code>{'${env.NAME}'}</code></span>
            <button class="btn sm secondary vaults-add" type="button" data-testid="env-import" onClick={() => open({ kind: 'env-import' })}>
              <Icon name="upload" />
              Import
            </button>
            <button class="btn sm secondary" type="button" data-testid="env-add" onClick={() => open({ kind: 'env' })}>
              <Icon name="plus" />
              Add a variable
            </button>
          </div>
          <p class="dl-sub" style="margin:0 0 10px;max-width:90ch">
            Plain values set for a machine’s commands. A sandboxed agent never sees conductor’s own environment.
          </p>
          {plain.length ? (
            <div class="callout warn small" style="margin-bottom:12px">
              <Icon name="warn" />{' '}
              <span class="grow">
                <b>{plain.map((e) => e.name).join(', ')}</b> look{plain.length > 1 ? '' : 's'} secret but {plain.length > 1 ? 'are' : 'is'} plain text. Move{' '}
                {plain.length > 1 ? 'them' : 'it'} into the vault.
              </span>
              {plain.map((e) => (
                <button class="btn sm" type="button" key={e.scope + e.name} onClick={() => open({ kind: 'env-move', e })}>
                  <Icon name="lock" />
                  Move {e.name}
                </button>
              ))}
            </div>
          ) : null}
          <div class="panel">
            <ul class="dl-list sec-list">
              {eRows.length ? (
                eRows.map((e) => <EnvRow key={e.scope + ':' + e.name} e={e} open={open} />)
              ) : (
                <li class="dl-empty-row dl-sub">{env.length ? 'No variable matches.' : 'No environment variables yet: add one for a machine’s commands.'}</li>
              )}
            </ul>
          </div>
          <EnvPreview env={env} />
        </section>
      ) : null}
    </>
  )
}

function SettingRow({ name, value, used, open }: { name: string; value: string; used: number; open: (m: Modal) => void }): JSX.Element {
  const [menu, setMenu] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const ref = '${settings.' + name + '}'
  const st = staged.value.find((c) => c.path[0] === 'settings' && c.path[1] === name)
  const items: MenuItem[] = [
    { label: 'Change the value…', icon: 'edit', onClick: () => open({ kind: 'setting', name }) },
    { label: 'Copy the reference', icon: 'copy', onClick: () => void navigator.clipboard?.writeText(ref).catch(() => undefined) },
    '-',
    { label: 'Delete…', icon: 'trash', onClick: () => open({ kind: 'setting-del', name }) },
  ]
  const label = name.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())
  return (
    <li class={'sec-row' + (used ? '' : ' unused')} data-secret={'settings.' + name}>
      <div class="sec-line">
        <div class="sec-main">
          <span class="sec-name">
            <b>{label}</b>
            <span class="sec-ref mono">{ref}</span>
          </span>
          <span class="sec-uses">
            {used ? (
              <>
                <span class="dl-sub">Used by</span> {used} place{used === 1 ? '' : 's'} in config
              </>
            ) : (
              <span class="tag">Unused</span>
            )}
          </span>
          <span class="sec-when">
            {st?.delete ? (
              <StatusMark s="staged" word="Removal not applied" />
            ) : (
              <>
                <span class="mono sec-val">{st ? String(st.value) : value}</span>
                {st ? <StatusMark s="staged" word="Changed, not applied" /> : null}
                {/\$\{settings\./.test(value) ? <span class="dl-sub">· built from another setting</span> : null}
                {/\$\{env\./.test(value) ? <span class="dl-sub">· from an environment variable</span> : null}
              </>
            )}
          </span>
        </div>
        <CopyBtn value={ref} what={name} />
        <button ref={btn} class="icon-btn" type="button" aria-label={`More for ${label}`} data-testid={`setting-more-${name}`} onClick={() => setMenu((o) => !o)}>
          <Icon name="more" />
        </button>
        {menu ? <Menu anchor={btn.current} items={items} onClose={() => setMenu(false)} label={`More for ${label}`} /> : null}
      </div>
    </li>
  )
}

function EnvRow({ e, open }: { e: EnvVarInfo; open: (m: Modal) => void }): JSX.Element {
  const [menu, setMenu] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const ref = '${env.' + e.name + '}'
  const v = envValue(e)
  const looks = !!e.literal && SECRETISH.test(e.name)
  const [kind, host] = e.scope.split(':')
  const st = isStaged(kind === 'host' ? ['hosts', host, 'env', e.name] : ['runtimes', host, 'env', e.name])
  const uses = e.used_by ?? []
  const items: MenuItem[] = [
    { label: 'Change the value…', icon: 'edit', onClick: () => open({ kind: 'env', scope: e.scope, name: e.name }) },
    { label: 'Copy the reference', icon: 'copy', onClick: () => void navigator.clipboard?.writeText(ref).catch(() => undefined) },
    ...(looks ? [{ label: 'Move into the vault…', icon: 'lock', onClick: () => open({ kind: 'env-move', e }) }] : []),
    '-',
    { label: 'Delete…', icon: 'trash', onClick: () => open({ kind: 'env-del', e }) },
  ]
  return (
    <li class={'sec-row' + (uses.length ? '' : ' unused')} data-secret={'env.' + e.scope + ':' + e.name}>
      <div class="sec-line">
        <div class="sec-main">
          <span class="sec-name">
            <b>{e.name}</b>
            <span class="sec-ref mono">{ref}</span>
          </span>
          <span class="sec-uses">
            {uses.length ? (
              <>
                <span class="dl-sub">Used by</span> {uses.length} place{uses.length === 1 ? '' : 's'} in config
              </>
            ) : (
              <span class="tag">Unused</span>
            )}
          </span>
          <span class="sec-when">
            <span class="mono sec-val">{looks ? <span class="masked-v">{'•'.repeat(Math.min(v.length || 10, 12))}</span> : v}</span>{' '}
            <span class="dl-sub">· for {scopeWords(e.scope)}</span>
            {looks ? <StatusMark s="wait" word="Plain text" /> : null}
            {st ? <StatusMark s="staged" word="Changed, not applied" /> : null}
          </span>
        </div>
        <CopyBtn value={ref} what={e.name} />
        <button ref={btn} class="icon-btn" type="button" aria-label={`More for ${e.name}`} data-testid={`env-more-${e.name}`} onClick={() => setMenu((o) => !o)}>
          <Icon name="more" />
        </button>
        {menu ? <Menu anchor={btn.current} items={items} onClose={() => setMenu(false)} label={`More for ${e.name}`} /> : null}
      </div>
    </li>
  )
}

/** The prototype's folded "What a step would get": pick a runtime, a machine
 *  and a workflow, and see each variable that reaches the step and where it
 *  comes from. Names and sources only, never a value. */
function EnvPreview({ env }: { env: EnvVarInfo[] }): JSX.Element {
  const tree = (config.value?.tree ?? {}) as Record<string, unknown>
  const runtimes = Object.keys((tree.runtimes as Record<string, unknown>) ?? {}).sort()
  const hosts = hostsOf()
  const wfs = [
    ...Object.keys((tree.workflows as Record<string, unknown>) ?? {}),
    ...(((tree.triggers as { name?: string }[]) ?? []).map((t) => t?.name ?? '').filter(Boolean)),
  ].filter((n, i, a) => a.indexOf(n) === i)
  const [ctx, setCtx] = useState({ runtime: runtimes[0] ?? '', host: '', workflow: wfs[0] ?? '', sandboxed: true })
  const by = new Map<string, { name: string; scope: string; shadowed: string[]; ref: string | null }>()
  for (const e of env) {
    const [k, n] = e.scope.split(':')
    if (!((k === 'runtime' && n === ctx.runtime) || (k === 'host' && n === ctx.host))) continue
    const prev = by.get(e.name)
    by.set(e.name, { name: e.name, scope: e.scope, shadowed: prev ? [...prev.shadowed, prev.scope] : [], ref: e.ref ?? null })
  }
  const rows = [...by.values()].sort((a, b) => a.name.localeCompare(b.name))
  return (
    <details class="dl-card dl-tech-d" id="env-preview">
      <summary>
        <Icon name="chev" />
        What a step would get
      </summary>
      <div class="row wrap" style="gap:10px;margin:10px 0">
        <label class="small">
          Runtime{' '}
          <select class="select sm" value={ctx.runtime} onChange={(e) => setCtx({ ...ctx, runtime: (e.target as HTMLSelectElement).value })}>
            {['', ...runtimes].map((n) => (
              <option value={n}>{n || 'none'}</option>
            ))}
          </select>
        </label>
        <label class="small">
          Machine{' '}
          <select class="select sm" value={ctx.host} data-testid="env-ctx-host" onChange={(e) => setCtx({ ...ctx, host: (e.target as HTMLSelectElement).value })}>
            {['', ...hosts].map((n) => (
              <option value={n}>{n || 'this machine'}</option>
            ))}
          </select>
        </label>
        <label class="small">
          Workflow{' '}
          <select class="select sm" value={ctx.workflow} onChange={(e) => setCtx({ ...ctx, workflow: (e.target as HTMLSelectElement).value })}>
            {['', ...wfs].map((n) => (
              <option value={n}>{n || 'none'}</option>
            ))}
          </select>
        </label>
        <label class="row small">
          <button
            type="button"
            role="switch"
            class={'switch' + (ctx.sandboxed ? ' on' : '')}
            aria-checked={ctx.sandboxed}
            aria-label="Agent step in the sandbox"
            onClick={() => setCtx({ ...ctx, sandboxed: !ctx.sandboxed })}
          ></button>
          <span>an agent step in the sandbox</span>
        </label>
      </div>
      <table class="tbl compact responsive env-eff">
        <thead>
          <tr>
            <th>name</th>
            <th>comes from</th>
            <th>overrides</th>
            <th>{ctx.sandboxed ? 'reaches the agent' : 'reaches the step'}</th>
          </tr>
        </thead>
        <tbody>
          {rows.length ? (
            rows.map((r) => (
              <tr>
                <td data-l="name" class="mono">
                  {r.name}
                </td>
                <td data-l="comes from" class="mono">
                  {scopeLabel(r.scope)}
                  {r.ref ? <span class="muted tiny"> ({r.ref})</span> : null}
                </td>
                <td data-l="overrides" class="mono muted">
                  {r.shadowed.map(scopeLabel).join(', ') || '—'}
                </td>
                <td data-l="reaches">yes</td>
              </tr>
            ))
          ) : (
            <tr class="empty-row">
              <td colSpan={4}>No env vars reach this context.</td>
            </tr>
          )}
        </tbody>
      </table>
      <div class="muted tiny" style="padding:6px 2px">
        Names and where they come from only; values are never shown here.
      </div>
    </details>
  )
}

// ---------- forms ----------

function Modals({
  m,
  vaults,
  secrets,
  sys,
  open,
  close,
  done,
  fail,
  toReview,
}: {
  m: Modal
  vaults: VaultInfo[]
  secrets: SecretInfo[]
  sys: SystemResponse | null
  open: (m: Modal) => void
  close: () => void
  done: (msg: string) => void
  fail: (msg: string) => void
  toReview: () => void
}): JSX.Element | null {
  const passkey = !!authState.value.stepup_methods?.includes('passkey')
  const writable = vaults.filter((v) => v.writable && v.status === 'ok')
  const stageOr = async (fn: () => Promise<void>): Promise<string | void> => {
    await fn()
    if (cfgError.value) return cfgError.value
  }

  switch (m.kind) {
    case 'vault': {
      const edit = m.edit
      const cur = edit ? vaultCfg(edit) : undefined
      const curType = (cur?.type as string) || vaults.find((v) => v.name === edit)?.type || 'conductor'
      const values: Record<string, string> = { name: edit ?? m.name ?? '', type: curType }
      if (cur) for (const f of VT[curType] ?? []) values[vtKey(curType, f.k)] = getPath(cur, f.k)
      const fields: FormField[] = [
        {
          k: 'name',
          req: true,
          readonly: !!edit,
          pattern: /^[a-z][a-z0-9-]*$/,
          patternMsg: 'Lowercase letters, digits and dashes.',
          validate: (x) => (!edit && vaults.some((y) => y.name === x) ? 'A vault by that name exists.' : ''),
        },
        {
          k: 'type',
          type: 'seg',
          req: true,
          def: curType,
          options: Object.keys(VT).map((t) => [t, t, edit && t !== curType ? 'Changing a vault’s type is a new vault' : undefined] as [string, string, string?]),
        },
        ...Object.entries(VT).flatMap(([t, fs]) =>
          fs.map((f) => ({
            k: vtKey(t, f.k),
            label: f.label ?? f.k.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase()),
            req: f.req,
            ph: f.ph,
            help: f.help,
            def: f.def,
            show: (vals: Record<string, string>) => (vals.type || curType) === t,
          })),
        ),
      ]
      return (
        <FormModal
          caps={edit ? `vaults.${edit}` : 'vaults:'}
          title={edit ? `Edit vault ${edit}` : 'Add a vault'}
          fields={fields}
          values={values}
          note="A vault is config: this stages a change, validated on a copy (the unlock ref must resolve) before it applies."
          submit="Review change"
          submitIcon="check"
          testid="vault-form"
          onClose={close}
          onSubmit={async (x) => {
            const type = x.type || curType
            const n: Record<string, unknown> = { type }
            for (const f of VT[type] ?? []) {
              const val = x[vtKey(type, f.k)]
              if (val == null || val === '') continue
              const parts = f.k.split('.')
              let o = n
              for (const p of parts.slice(0, -1)) o = (o[p] as Record<string, unknown>) ?? (o[p] = {})
              o[parts[parts.length - 1]] = val
            }
            const name = edit ?? x.name
            const e = await stageOr(() => stage(['vaults', name], n, `${edit ? 'edit' : 'add'} vault ${name}`))
            if (e) return e
            toReview()
          }}
        />
      )
    }
    case 'vault-del': {
      const users = secrets.filter((s) => s.vault === m.name && s.used_by?.length)
      return (
        <ConfirmModal
          title={`Remove vault ${m.name}?`}
          body={
            <>
              Stages removing it from <code>vaults:</code>. The values in it stay where they are.{' '}
              {users.length
                ? `The config still reads ${users.length} secret${users.length === 1 ? '' : 's'} from it, so the check refuses it until those point elsewhere.`
                : 'Nothing in the config reads from it.'}
            </>
          }
          ok="Stage removal"
          danger
          onClose={close}
          onOk={async () => {
            await stageDelete(['vaults', m.name], `remove vault ${m.name}`)
            if (!cfgError.value) toReview()
          }}
        />
      )
    }
    case 'secret': {
      const sec = m.replace
      const opts = writable.map((v) => [v.name, `${v.name} (${vname(v.type)})`] as [string, string])
      const pre = m.vault && writable.some((v) => v.name === m.vault) ? m.vault : writable[0]?.name ?? ''
      const fields: FormField[] = [
        {
          k: 'vault',
          type: 'enum',
          req: true,
          label: 'Store in',
          readonly: !!sec,
          options: opts,
          def: pre,
          create: sec ? undefined : { label: 'New vault', onCreate: () => open({ kind: 'vault' }) },
        },
        {
          k: 'key',
          req: true,
          readonly: !!sec,
          label: 'Key',
          ph: 'e.g. pagerduty-ui',
          pattern: /^[\w./-]+$/,
          patternMsg: 'letters, digits, . _ / -',
          validate: (x, vals) => (!sec && secrets.some((s) => s.stored && s.ref === `${vals.vault}/${x}`) ? 'That entry exists: replace it instead.' : ''),
        },
        {
          k: 'value',
          type: 'text',
          req: true,
          label: 'Value · write-only',
          ph: 'paste the value',
          help: 'Sent once, written straight to the vault. There is no API that returns it: not to this page, not to an agent.',
        },
      ]
      return (
        <FormModal
          caps={sec ? 'write-only · replace the value' : 'new vault entry'}
          title={sec ? `Replace ${sec.ref}` : 'Add a secret'}
          wide
          intro={
            sec
              ? ''
              : 'A secret is a vault entry. Store a value in a writable vault here; entries in 1Password and other external vaults are referenced where they’re used.'
          }
          fields={fields}
          values={sec ? { vault: sec.vault, key: sec.key } : { vault: pre }}
          note={
            sec && sec.used_by?.length ? (
              <>
                <b>Used by</b> {sec.used_by.map((u, i) => <>{i ? ', ' : ''}<code>{u}</code></>)}. Those pick up the new value on the next reload.
              </>
            ) : undefined
          }
          submit={(sec ? 'Replace' : 'Store') + (passkey ? ' with passkey' : '')}
          submitIcon={passkey ? 'passkey' : 'lock'}
          testid="secret-form"
          onClose={close}
          onSubmit={async (v, clear) => {
            const vault = sec?.vault ?? v.vault
            const key = sec?.key ?? v.key
            const value = v.value
            clear('value') // the value leaves the page now
            try {
              await api.writeSecret(vault, key, value)
            } catch (e) {
              return humanError(e)
            }
            done(`${sec ? 'Replaced' : 'Stored'} ${vault}/${key}. Audited without its value.`)
          }}
        />
      )
    }
    case 'secret-ro': {
      const v = m.v
      const dir = getPath(vaultCfg(v.name), 'dir')
      const body =
        v.status === 'fail'
          ? `${v.name} can’t be reached right now (${v.reason || 'it didn’t open'}), so nothing can be stored in it. Fix how it unlocks, then try again.`
          : v.status !== 'ok'
            ? `${v.name} opens once the change that adds it is applied; then you can store secrets in it.`
            : v.type === 'file'
              ? `${v.name} reads files from ${dir || 'a mounted folder'}: a secret there is a file named by its key, put there on the machine (systemd, docker or k8s). It shows here once it’s there; conductor never writes it.`
              : `${v.name} is ${vname(v.type)}: its values are set in ${vname(v.type)} itself, and conductor only reads them. Add the entry there, then use it here by its reference.`
      return (
        <ConfirmModal
          title={`Add a secret to ${v.name}`}
          body={body}
          ok={writable.length ? 'Store in another vault' : 'OK'}
          cancel="Close"
          onClose={close}
          onOk={() => {
            if (writable.length) setTimeout(() => open({ kind: 'secret', vault: writable[0].name }), 0)
          }}
        />
      )
    }
    case 'secret-del':
      return (
        <ConfirmModal
          title={`Remove ${m.s.ref}?`}
          body={<>Deletes the entry from the vault {m.s.vault}. Nothing in the config reads it. This can’t be undone.</>}
          ok={passkey ? 'Remove with passkey' : 'Remove'}
          danger
          onClose={close}
          onOk={async () => {
            try {
              await api.deleteSecret(m.s.vault, m.s.key)
              done(`Removed ${m.s.ref} from its vault.`)
            } catch (e) {
              fail(humanError(e))
            }
          }}
        />
      )
    case 'setting': {
      const cur = m.name ? sys?.settings?.[m.name] ?? '' : ''
      return (
        <FormModal
          caps={m.name ? '${settings.' + m.name + '}' : 'Settings'}
          title={m.name ? `Change ${m.name}` : 'Add a setting'}
          fields={[
            ...(m.name
              ? []
              : [
                  {
                    k: 'name',
                    req: true,
                    pattern: /^[a-z_][a-z0-9_]*$/,
                    patternMsg: 'lowercase snake_case',
                    validate: (x: string) => (sys?.settings && x in sys.settings ? 'Exists already.' : ''),
                  } as FormField,
                ]),
            { k: 'value', req: true, help: 'May build on another: ${settings.org}/deploys, or read ${env.NAME}.' },
          ]}
          values={{ value: cur }}
          submit="Stage"
          testid="setting-form"
          onClose={close}
          onSubmit={async (v) => {
            const name = m.name ?? v.name
            const e = await stageOr(() => stage(['settings', name], v.value, `${m.name ? 'change' : 'add'} setting ${name}`))
            if (e) return e
            done(`Staged: ${m.name ? 'change' : 'add'} the setting ${name}. Not applied yet.`)
          }}
        />
      )
    }
    case 'setting-del':
      return (
        <ConfirmModal
          title={`Delete the setting ${m.name}?`}
          body={
            <>
              Anything reading <code>{'${settings.' + m.name + '}'}</code> would get nothing. This stages the change; it’s checked before it applies.
            </>
          }
          ok="Stage removal"
          danger
          onClose={close}
          onOk={async () => {
            await stageDelete(['settings', m.name], `remove setting ${m.name}`)
            done(cfgError.value ? '' : `Staged: remove the setting ${m.name}. Not applied yet.`)
          }}
        />
      )
    case 'env': {
      const hosts = hostsOf()
      const editing = !!m.name
      const e0 = editing ? sys?.env_vars?.find((x) => x.scope === m.scope && x.name === m.name) : undefined
      return (
        <FormModal
          caps={editing ? `hosts.${m.scope?.split(':')[1]}.env.${m.name}` : 'env'}
          title={editing ? `Edit ${m.name}` : 'Add an env var'}
          intro={
            hosts.length
              ? 'A value that looks secret belongs in a vault: write it as {{ vault "<vault>" "<key>" }}.'
              : 'Variables are set per machine, and there are no machines in the config yet. Add one under hosts: in the config first.'
          }
          fields={[
            {
              k: 'scope',
              type: 'enum',
              req: true,
              readonly: editing,
              label: 'For',
              options: hosts.map((h) => ['host:' + h, `commands on the machine ${h}`] as [string, string]),
            },
            {
              k: 'name',
              req: true,
              readonly: editing,
              ph: 'APP_USER',
              pattern: /^[A-Z_][A-Z0-9_]*$/,
              patternMsg: 'UPPER_SNAKE_CASE',
              validate: (v, all) => (!editing && sys?.env_vars?.some((x) => x.name === v && x.scope === all.scope) ? 'Already set there' : ''),
            },
            { k: 'value', req: true },
          ]}
          values={editing ? { scope: m.scope ?? '', name: m.name ?? '', value: e0 ? envValue(e0) : '' } : {}}
          submit="Review change"
          submitIcon="check"
          testid="env-form"
          onClose={close}
          onSubmit={async (v) => {
            const scope = m.scope ?? v.scope
            const name = m.name ?? v.name
            if (!scope) return 'Pick the machine it’s for.'
            const e = await stageOr(() => stage(['hosts', scope.split(':')[1], 'env', name], v.value, `${editing ? 'change' : 'add'} ${name}`))
            if (e) return e
            done(`Staged: ${name} for ${scopeWords(scope)}. Not applied yet.`)
          }}
        />
      )
    }
    case 'env-del': {
      const host = m.e.scope.split(':')[1]
      return (
        <ConfirmModal
          title={`Remove ${m.e.name}?`}
          body={
            <>
              Stages removing it from {scopeLabel(m.e.scope)}. Anything reading <code>{'${env.' + m.e.name + '}'}</code> would get nothing.
            </>
          }
          ok="Stage removal"
          cancel="Keep it"
          danger
          onClose={close}
          onOk={async () => {
            await stageDelete(['hosts', host, 'env', m.e.name], `remove ${m.e.name}`)
            done(cfgError.value ? '' : `Staged: remove ${m.e.name}. Not applied yet.`)
          }}
        />
      )
    }
    case 'env-move': {
      const key = 'env-' + m.e.name.toLowerCase().replace(/_/g, '-')
      const target = writable[0]
      if (!target)
        return (
          <ConfirmModal
            title={`Move ${m.e.name} into a vault?`}
            body="There is no vault conductor can store values in yet. Add a conductor vault on the Secrets tab first, then move it."
            ok="Add a vault"
            onClose={close}
            onOk={() => {
              location.hash = '#/secrets'
              setTimeout(() => open({ kind: 'vault' }), 0)
            }}
          />
        )
      const host = m.e.scope.split(':')[1]
      return (
        <ConfirmModal
          title={`Move ${m.e.name} into the vault?`}
          body={
            <>
              Stores the current value as <code>{target.name}/{key}</code> (write-only{passkey ? ', with your passkey' : ''}), and stages {m.e.name} as{' '}
              <code>{refOf(target.name, key)}</code>, so the plain-text line leaves {scopeLabel(m.e.scope)}.
            </>
          }
          ok="Continue"
          onClose={close}
          onOk={async () => {
            try {
              await api.writeSecret(target.name, key, envValue(m.e))
            } catch (e) {
              fail(humanError(e))
              return
            }
            await stage(['hosts', host, 'env', m.e.name], refOf(target.name, key), `move ${m.e.name} into the vault`)
            done(cfgError.value ? '' : `Stored ${target.name}/${key}; ${m.e.name} now reads it. The change is staged, not applied yet.`)
          }}
        />
      )
    }
    case 'env-import': {
      const hosts = hostsOf()
      return (
        <FormModal
          caps="env import"
          title="Import env vars"
          intro={`Paste KEY=value lines (a .env file). They go to the machine you pick. Values that look secret are stored in ${writable.length ? 'a vault' : 'a vault (add one first)'} and referenced, never written in plain text.`}
          fields={[
            { k: 'scope', type: 'enum', req: true, label: 'For', options: hosts.map((h) => ['host:' + h, `commands on the machine ${h}`] as [string, string]) },
            ...(writable.length
              ? [{ k: 'vault', type: 'enum', req: true, label: 'Secret-looking values go to', options: writable.map((v) => [v.name, `${v.name} (${vname(v.type)})`] as [string, string]) } as FormField]
              : []),
            {
              k: 'text',
              type: 'text',
              req: true,
              rows: 6,
              mono: true,
              label: 'Lines',
              ph: 'APP_USER=svc-app\nAPI_TOKEN=…',
              validate: (t) => {
                const bad = String(t || '')
                  .split('\n')
                  .filter((l) => l.trim() && !l.trim().startsWith('#') && !/^[A-Z_][A-Z0-9_]*=/.test(l.trim()))
                return bad.length ? `Not KEY=value: ${bad[0].slice(0, 40)}` : ''
              },
            },
          ]}
          submit="Review change"
          submitIcon="check"
          testid="env-import-form"
          onClose={close}
          onSubmit={async (v, clear) => {
            if (!v.scope) return 'Pick the machine they’re for: add one under hosts: in the config first.'
            const host = v.scope.split(':')[1]
            const lines = v.text
              .split('\n')
              .map((l) => l.trim())
              .filter((l) => l && !l.startsWith('#'))
              .map((l) => {
                const i = l.indexOf('=')
                const name = l.slice(0, i)
                const val = l.slice(i + 1).replace(/^["']|["']$/g, '')
                return { name, val, secret: SECRETISH.test(name) && !isRef(val), key: 'env-' + name.toLowerCase().replace(/_/g, '-') }
              })
            clear('text')
            if (lines.some((x) => x.secret) && !v.vault) return 'Some values look secret, and there is no vault to store them in. Add a vault first.'
            for (const x of lines) {
              if (x.secret) {
                try {
                  await api.writeSecret(v.vault, x.key, x.val)
                } catch (e) {
                  return humanError(e)
                }
              }
              await stage(['hosts', host, 'env', x.name], x.secret ? refOf(v.vault, x.key) : x.val, `import ${x.name}`)
              if (cfgError.value) return cfgError.value
            }
            const n = lines.filter((x) => x.secret).length
            done(`Staged ${lines.length} variable${lines.length === 1 ? '' : 's'}${n ? `; ${n} secret value${n === 1 ? '' : 's'} went to the vault` : ''}. Not applied yet.`)
          }}
        />
      )
    }
  }
}
