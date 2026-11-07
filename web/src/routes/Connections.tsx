// Plugins (CONFORMANCE "Plugins": what conductor can use, four tabs drawn as
// one flat row — Connections, Agent runtimes, Code languages, Machines). Each
// row is a plugin instance: its glyph, name, what it's wired to (the facts
// line), and one menu. Prototype source: js/screens/system.js's
// `connectors/:tab?` route and css/daylight.css's `.dl-conn` row.
//
// Left out (no backend): per-connection version policy (pinned/range/current)
// and its "Update now"/"Hold"/"Choose a version…" actions — PluginInfo has no
// release list, only a flat pinned/floating + version. Bulk select, a version
// picker modal and the plugin-permissions sheet are the same gap. "Code
// languages" has no daemon concept at all (ConnectionsResponse has no
// per-language entries — a `use: lua`-style code-step plugin shows up as a
// plain plugin, not categorized); the tab says so rather than faking rows.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PluginsResponse, ConnectionsResponse, NamedUse, HostInfo } from '../api/types'
import { humanError } from '../api/errors'
import { ReviewPanel } from '../components/ReviewPanel'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { TabsBar } from '../components/TabsBar'
import { FactsLine } from '../components/Facts'
import { busy, cfgError, loadConfig, stage, staged } from '../configStore'
import { seg } from '../router'

type Tab = 'connectors' | 'runtimes' | 'languages' | 'machines'
const TABS: { id: Tab; label: string; icon: string }[] = [
  { id: 'connectors', label: 'Connections', icon: 'connectors' },
  { id: 'runtimes', label: 'Agent runtimes', icon: 'zap' },
  { id: 'languages', label: 'Code languages', icon: 'code' },
  { id: 'machines', label: 'Machines', icon: 'terminal' },
]

export function Connections(): JSX.Element {
  const [plugins, setPlugins] = useState<PluginsResponse | null>(null)
  const [conns, setConns] = useState<ConnectionsResponse | null>(null)
  const [err, setErr] = useState('')
  const [addHost, setAddHost] = useState(false)
  useEffect(() => {
    api.plugins().then(setPlugins).catch((e) => setErr(humanError(e)))
    api.connections().then(setConns).catch((e) => setErr(humanError(e)))
    void loadConfig()
  }, [])

  const tab = ((): Tab => {
    const s = seg(1)
    return s === 'runtimes' || s === 'languages' || s === 'machines' ? s : 'connectors'
  })()

  const pending = err ? 'Nothing to show until conductor answers.' : 'Loading…'
  const plugByUse = new Map((plugins?.plugins ?? []).map((p) => [p.name, p]))
  const usedBy = (name: string) => plugByUse.get(name.split('@')[0])

  const connFacts = (c: NamedUse) => {
    const base = c.use.split('@')[0]
    const p = usedBy(base)
    return [
      { icon: 'extensions', value: p?.builtin ? 'Built in' : 'Plugin' },
      p && !p.builtin ? { icon: p.pinned ? 'lock' : 'updates', value: p.pinned ? `Pinned ${p.version}` : 'Floating' } : null,
    ]
  }

  // renderList/machines take the loaded flag SEPARATELY from the rows array:
  // once `conns` has loaded, a null/missing field means "nothing configured"
  // (the empty message), not "still loading" — conflating the two left
  // Machines stuck on "Loading…" forever on a box with zero hosts (its field
  // is JSON null, never `[]`, when nothing is configured).
  const loaded = conns !== null
  const renderList = (rows: NamedUse[] | null | undefined, empty: string) =>
    !loaded ? (
      <p class="muted">{pending}</p>
    ) : !rows?.length ? (
      <p class="dl-sub">{empty}</p>
    ) : (
      <ul class="dl-list dl-conns">
        {rows.map((r) => (
          <li class="dl-conn" data-row={r.name} key={r.name}>
            <span class="dl-ng">
              <Icon name="connectors" />
            </span>
            <span class="dl-nt">
              <b>{r.name}</b>
              <span class="dl-why mono">
                {r.use}
                {r.type ? <span class="tgt"> · {r.type}</span> : null}
              </span>
              <FactsLine facts={connFacts(r)} />
            </span>
            <span class="dl-na" />
          </li>
        ))}
      </ul>
    )

  const machines = (hosts: HostInfo[] | null | undefined) =>
    !loaded ? (
      <p class="muted">{pending}</p>
    ) : !hosts?.length ? (
      <p class="dl-sub">No machines yet. A machine is another box conductor can run steps on, over SSH with your usual keys.</p>
    ) : (
      <ul class="dl-list dl-conns">
        {hosts.map((h) => (
          <li class="dl-conn" data-row={h.name} key={h.name}>
            <span class="dl-ng">
              <Icon name="terminal" />
            </span>
            <span class="dl-nt">
              <b>{h.name}</b>
              <span class="dl-why mono">{h.host}{h.user ? ` · ${h.user}` : ''}</span>
            </span>
            <span class="dl-na" />
          </li>
        ))}
      </ul>
    )

  const nothingAtAll = plugins !== null && conns !== null && !plugins.plugins?.length && !conns.connectors?.length && !conns.runtimes?.length && !conns.hosts?.length

  return (
    <div class="col m-page">
      <div class="dl-phead">
        <div>
          <h1>Plugins</h1>
          <p class="lede-2">
            What conductor is wired to. <b>Connections</b> are how it hears from your services (a code host, a chat, a
            schedule), <b>agent runtimes</b> are where agents do their work, and <b>machines</b> are other boxes it can
            run things on. Each one is powered by a plugin, named with <span class="mono">use:</span> in the config.
          </p>
        </div>
      </div>

      {err ? <div class="banner err" role="alert">Couldn't load plugins: {err}</div> : null}
      <ReviewPanel stagedCount={staged.value.length} />

      {nothingAtAll ? (
        <EmptyState testid="connections-empty" title="Nothing connected yet">
          <p>Add a connector, a runtime or a host under <code>connectors:</code>, <code>runtimes:</code> or <code>hosts:</code> in the config; each names its plugin with <code>use:</code>.</p>
        </EmptyState>
      ) : (
        <>
          <TabsBar items={TABS.map((t) => ({ id: t.id, label: t.label, href: '#/connections/' + (t.id === 'connectors' ? '' : t.id) }))} active={tab} ariaLabel="Plugins" />

          <section class="dl-card" aria-label={TABS.find((t) => t.id === tab)?.label}>
            {tab === 'connectors'
              ? renderList(conns?.connectors, 'No connections. A connection is how conductor hears from a service (a repo host, a chat, a schedule); add one under connectors: in the config.')
              : null}
            {tab === 'runtimes'
              ? renderList(conns?.runtimes, 'No agent runtimes yet. A runtime is where agent steps and bots do their work; add one under runtimes: when you need it.')
              : null}
            {tab === 'machines' ? machines(conns?.hosts) : null}
            {tab === 'languages' ? (
              <p class="dl-sub" data-testid="languages-gap" style="padding:16px 22px">
                Code languages aren't broken out from the plugin list yet — the daemon doesn't categorize a plugin as a
                language runtime versus any other kind. A code-step engine (for example <span class="mono">use: lua</span>)
                shows up as a plain plugin on the Connections tab instead.
              </p>
            ) : null}
          </section>

          {tab === 'machines' ? (
            <section class="dl-card" aria-label="Add a machine">
              <div class="panel-head" style="padding:12px 22px 0">
                {!addHost ? (
                  <button class="btn primary sm" type="button" data-testid="add-host" onClick={() => setAddHost(true)}>
                    <Icon name="plus" />Add a machine
                  </button>
                ) : null}
              </div>
              {addHost ? <AddHost onDone={() => setAddHost(false)} /> : null}
            </section>
          ) : null}
        </>
      )}
    </div>
  )
}

// AddHost stages a new hosts: entry (its address, and the user to sign in as);
// it lands through the same review and apply as every other edit.
function AddHost({ onDone }: { onDone: () => void }): JSX.Element {
  const [name, setName] = useState('')
  const [addr, setAddr] = useState('')
  const [user, setUser] = useState('')
  const [msg, setMsg] = useState('')
  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-')
  async function add() {
    if (!id || !addr.trim()) {
      setMsg('give the machine a name and an address')
      return
    }
    await stage(['hosts', id, 'host'], addr.trim(), `add host ${id}`)
    if (!cfgError.value && user.trim()) await stage(['hosts', id, 'user'], user.trim(), `${id}: user`)
    if (cfgError.value) {
      setMsg(cfgError.value)
      return
    }
    onDone()
  }
  return (
    <div class="add-host" data-testid="add-host-form">
      <label class="form-field">
        <span>Name</span>
        <input class="input" value={name} placeholder="build-box" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
      </label>
      <label class="form-field">
        <span>Address</span>
        <input class="input" value={addr} placeholder="build01.example.com" onInput={(e) => setAddr((e.target as HTMLInputElement).value)} />
      </label>
      <label class="form-field">
        <span>Sign in as (optional)</span>
        <input class="input" value={user} placeholder="your usual SSH user" onInput={(e) => setUser((e.target as HTMLInputElement).value)} />
      </label>
      {msg ? <div class="banner err" role="alert">{msg}</div> : null}
      <p class="dl-sub">conductor connects with this machine's SSH keys and known hosts. The new host is reviewed and applied above.</p>
      <div class="row-actions">
        <button class="btn ghost" type="button" onClick={onDone}>Cancel</button>
        <button class="btn primary" type="button" data-testid="stage-host" disabled={busy.value} onClick={() => void add()}>Add machine</button>
      </div>
    </div>
  )
}
