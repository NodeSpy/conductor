// Secrets (CONFORMANCE "Secrets": two tabs, one system — Secrets and Settings
// — sharing one row shape: a readable name with its exact reference and Copy,
// used by, and a value or where it lives; the same two-sentence explanation;
// the filter bar; vaults always open at the foot of Secrets with their own Add
// a vault). Secret VALUES are never read or shown — the backend returns names
// and <vault>/<key> references only; Settings values are plain config scalars
// and are editable here, same as System → Config.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { SecretsResponse, SystemResponse } from '../api/types'
import { humanError } from '../api/errors'
import { FilterBar } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { TabsBar } from '../components/TabsBar'
import { ReviewPanel } from '../components/ReviewPanel'
import { busy, cfgError, loadConfig, stage, staged } from '../configStore'
import { seg } from '../router'

type Tab = 'secrets' | 'settings'

export function Secrets(): JSX.Element {
  const [data, setData] = useState<SecretsResponse | null>(null)
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [addVault, setAddVault] = useState(false)
  useEffect(() => {
    api.secrets().then(setData).catch((e) => setErr(humanError(e)))
    api.system().then(setSys).catch(() => undefined)
    void loadConfig()
  }, [])

  const tab: Tab = seg(1) === 'settings' ? 'settings' : 'secrets'

  return (
    <div class="col m-page">
      <div class="dl-phead">
        <div>
          <h1>Secrets</h1>
        </div>
      </div>

      {err ? <div class="banner err" role="alert">Couldn't load secrets: {err}</div> : null}
      <ReviewPanel stagedCount={staged.value.length} />

      {data === null ? (
        <p class="dl-sub">Loading…</p>
      ) : (
        <>
          <TabsBar
            items={[
              { id: 'secrets', label: 'Secrets', href: '#/secrets', count: data.secrets?.length || null },
              { id: 'settings', label: 'Settings', href: '#/secrets/settings', count: Object.keys(sys?.settings ?? {}).length || null },
            ]}
            active={tab}
            ariaLabel="Secrets"
          />

          {tab === 'secrets' ? (
            <p class="sec-explain">
              <Icon name="lock" />
              <span>
                <b>A secret</b> is write-only: stored in a vault and never shown again, not here and not to an agent. A
                field uses it as <code class="mono">{'{{ vault "house" "gh-app-key" }}'}</code>.
              </span>
            </p>
          ) : (
            <p class="sec-explain">
              <Icon name="settings" />
              <span>
                <b>A setting</b> is a plain value your config reads, visible to anyone who can read the config:{' '}
                <code class="mono">{'${settings.org}'}</code>, or an environment variable as{' '}
                <code class="mono">{'${env.NAME}'}</code>.
              </span>
            </p>
          )}

          {tab === 'secrets' ? (
            <>
              {data.secrets?.length ? (
                <FilterBar q={q} onQueryChange={setQ} placeholder="Find a secret" ariaLabel="Find a secret" menus={[]} />
              ) : null}
              <div class="section-title sec-title">
                <h2>In your vaults</h2>
                <button class="btn sm secondary vaults-add" type="button" disabled title="Writing a new secret value isn't wired to a daemon endpoint yet: use conductor vault on the box">
                  <Icon name="plus" />Add a secret
                </button>
              </div>
              <div class="panel">
                <Rows
                  rows={(data.secrets ?? []).filter((s) => !q || `${s.name} ${s.ref}`.toLowerCase().includes(q.toLowerCase())).map((s) => ({
                    name: s.name,
                    ref: s.ref,
                    usedBy: s.used_by ?? [],
                  }))}
                  empty="Nothing refers to a secret yet. A step or connector that needs one names it as <vault>/<key>."
                  secretMenu
                />
                <div class="panel-foot small muted">
                  <Icon name="eyeoff" /> No screen, API route or log line returns a secret's value; values are redacted from logs and the audit trail.
                </div>
              </div>

              <section class="vaults" id="vaults">
                <div class="section-title">
                  <h2>Where they're stored</h2>
                  <span class="caps">{data.vaults?.length ?? 0} vault{(data.vaults?.length ?? 0) === 1 ? '' : 's'}</span>
                  {!addVault ? (
                    <button class="btn sm secondary vaults-add" type="button" data-testid="add-vault" onClick={() => setAddVault(true)}>
                      <Icon name="plus" />Add a vault
                    </button>
                  ) : null}
                </div>
                {addVault ? <AddVault onDone={() => setAddVault(false)} /> : null}
                {data.vaults?.length ? (
                  <div class="grid g4">
                    {data.vaults.map((v) => (
                      <div class="panel stat vault-card" id={`vault-${v.name}`} key={v.name}>
                        <span class="k"><Icon name="secrets" /> {v.name} <span class="dl-sub" style="text-transform:none;letter-spacing:0;margin-left:auto">{v.type || 'conductor'}</span></span>
                        <span class="d">conductor can store values here.</span>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p class="dl-sub" data-testid="vaults-empty">No vaults. Add one above, or under <span class="mono">vaults:</span> in the config.</p>
                )}
              </section>
            </>
          ) : (
            <>
              <section class="dl-card" aria-label="Settings">
                {Object.keys(sys?.settings ?? {}).length ? (
                  <Rows
                    rows={Object.entries(sys?.settings ?? {}).map(([k, v]) => ({
                      name: k,
                      value: v,
                      path: ['settings', k],
                      usedBy: sys?.settings_used_by?.[k] ?? [],
                    }))}
                    empty=""
                  />
                ) : (
                  <p class="dl-sub" data-testid="settings-empty" style="padding:16px 22px">
                    No settings yet. Settings are named values your workflows and packs share, written as{' '}
                    <span class="mono">{'${settings.<name>}'}</span>. Add them under <span class="mono">settings:</span> in
                    the config and they become editable here.
                  </p>
                )}
              </section>

              <section class="dl-card" aria-label="Environment variables">
                <div class="panel-head" style="padding:14px 22px 0"><h2 style="margin:0;font-size:15px">Environment variables</h2></div>
                <p class="dl-sub" style="padding:0 22px 10px">Set on a runtime's agents or a machine's commands. Read-only here; edit them under <a class="link" href="#/system/config">Config</a>. Values are never shown — only whether one is a reference (where it lives) or a plain value.</p>
                {sys?.env_vars?.length ? (
                  <ul class="dl-list dl-conns">
                    {sys.env_vars.map((e) => (
                      <li class="dl-conn" key={e.scope + '.' + e.name}>
                        <span class="dl-ng"><Icon name="terminal" /></span>
                        <span class="dl-nt">
                          <b class="mono">{e.name}</b>
                          <span class="dl-why mono">{e.ref ? e.ref : 'plain value (not shown)'} <span class="tag">{e.scope}</span></span>
                        </span>
                        <span class="dl-na" />
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p class="dl-sub" style="padding:0 22px 18px">No env vars on a runtime or a host yet.</p>
                )}
              </section>
            </>
          )}
        </>
      )}
    </div>
  )
}

// Rows renders the shared Secrets/Settings row: a readable name with its exact
// reference (with Copy) or an editable value, and who uses it. A secret row
// (secretMenu) also carries the prototype's ⋯ menu (Change the value / Copy
// the reference / Delete); changing or deleting a secret value has no backend
// endpoint yet (GET /api/v1/secrets is read-only — see the report), so those
// two stay disabled with the real reason, same convention as every other
// disabled control in the dashboard (UI.off).
function Rows({
  rows,
  empty,
  secretMenu,
}: {
  rows: { name: string; ref?: string; value?: string; usedBy?: string[]; path?: string[] }[]
  empty: string
  secretMenu?: boolean
}): JSX.Element {
  if (!rows.length) return <p class="dl-sub" style="padding:8px 22px 18px">{empty}</p>
  return (
    <ul class="dl-list dl-conns">
      {rows.map((r) => (
        <li class="dl-conn" key={r.name}>
          <span class="dl-ng"><Icon name="key" /></span>
          <span class="dl-nt">
            <b>{r.name}</b>
            {r.path ? <input class="input mono" value={r.value ?? ''} data-path={r.path.join('.')} onChange={(e) => void stage(r.path!, (e.target as HTMLInputElement).value)} /> : null}
            {/* One .dl-why per row: a global list rule hides any sibling after
                the first (daylight.css ".dl-nt > :is(.dl-why,.reason) ~
                :is(.dl-why,.reason)"), so the reference and its "used by"
                tags share this single span rather than two. */}
            {r.ref || r.usedBy?.length ? (
              <span class="dl-why mono">
                {r.ref ? (
                  <>
                    {r.ref}{' '}
                    <button class="icon-btn sm" type="button" title="Copy the reference" aria-label={`Copy ${r.ref}`} onClick={() => void navigator.clipboard?.writeText(r.ref!)}>
                      <Icon name="copy" />
                    </button>
                  </>
                ) : null}
                {r.usedBy?.length ? r.usedBy.map((u) => <span class="tag mono" key={u}>{u}</span>) : null}
              </span>
            ) : null}
          </span>
          <span class="dl-na">{secretMenu && r.ref ? <SecretMenu name={r.name} reference={r.ref} /> : null}</span>
        </li>
      ))}
    </ul>
  )
}

function SecretMenu({ name, reference }: { name: string; reference: string }): JSX.Element {
  const [open, setOpen] = useState(false)
  const btnRef = useRef<HTMLButtonElement>(null)
  const noBackend = 'Not wired to a daemon endpoint yet: use conductor vault on the box'
  const items: MenuItem[] = [
    { label: 'Change the value', icon: 'edit', disabled: noBackend },
    { label: 'Copy the reference', icon: 'copy', onClick: () => void navigator.clipboard?.writeText(reference) },
    { label: 'Delete', icon: 'trash', disabled: noBackend },
  ]
  return (
    <>
      <button ref={btnRef} type="button" class="icon-btn" aria-label={`More for ${name}`} onClick={() => setOpen((o) => !o)}>
        <Icon name="more" />
      </button>
      {open ? <Menu anchor={btnRef.current} items={items} onClose={() => setOpen(false)} label={`More for ${name}`} /> : null}
    </>
  )
}

function AddVault({ onDone }: { onDone: () => void }): JSX.Element {
  const [name, setName] = useState('')
  const [type, setType] = useState('conductor')
  const [msg, setMsg] = useState('')
  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-')
  async function add() {
    if (!id) {
      setMsg('give the vault a name')
      return
    }
    await stage(['vaults', id, 'type'], type.trim() || 'conductor', `add vault ${id}`)
    if (cfgError.value) {
      setMsg(cfgError.value)
      return
    }
    onDone()
  }
  return (
    <div class="add-host" data-testid="add-vault-form">
      <label class="form-field">
        <span>Name</span>
        <input class="input" value={name} placeholder="house" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
      </label>
      <label class="form-field">
        <span>Type</span>
        <input class="input mono" value={type} onInput={(e) => setType((e.target as HTMLInputElement).value)} />
      </label>
      {msg ? <div class="banner err" role="alert">{msg}</div> : null}
      <div class="row-actions">
        <button class="btn ghost" type="button" onClick={onDone}>Cancel</button>
        <button class="btn primary" type="button" disabled={busy.value} data-testid="stage-vault" onClick={() => void add()}>Add vault</button>
      </div>
    </div>
  )
}
