// Plugins & Connections (design §5.4). A flat list of the plugins referenced by
// `use:` across connectors and runtimes, each with its version policy (pinned to
// a version, or floating) and whether it is a conductor built-in; then the
// connectors, runtimes and hosts as flat lists.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PluginsResponse, ConnectionsResponse } from '../api/types'
import { humanError } from '../api/errors'
import { ReviewPanel } from '../components/ReviewPanel'
import { busy, cfgError, loadConfig, stage, staged } from '../configStore'

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

  const pending = err ? 'Nothing to show until conductor answers.' : 'Loading…'
  return (
    <div class="col">
      <h1 class="screen-title serif">Plugins & Connections</h1>
      <p class="muted">
        What conductor is wired to. <b>Connectors</b> are how it hears from your services (a code host, a chat, a schedule),{' '}
        <b>runtimes</b> are where agents do their work, and <b>hosts</b> are other machines it can run things on. Each one is
        powered by a <b>plugin</b>, listed first with the version it is held to.
      </p>
      <p class="muted small">In the config file, each connector and runtime names its plugin with <span class="mono">use:</span>.</p>

      <ReviewPanel stagedCount={staged.value.length} />
      {err ? <div class="banner err" role="alert">Couldn't load plugins and connections: {err}</div> : null}

      <section class="panel" aria-label="Plugins">
        <div class="panel-head"><span class="panel-title">Plugins</span></div>
        {plugins?.plugins?.length ? (
          <div class="tbl-wrap">
          <table class="tbl">
            <thead><tr><th>Plugin</th><th>Version policy</th><th>Kind</th><th>Used by</th></tr></thead>
            <tbody>
              {plugins.plugins.map((p) => (
                <tr data-plugin={p.name}>
                  <td class="mono">{p.name}</td>
                  <td>{p.pinned ? <span class="tag mono">pinned {p.version}</span> : <span class="tag warn">floating</span>}</td>
                  <td>{p.builtin ? <span class="tag">built-in</span> : <span class="tag">external</span>}</td>
                  <td>{p.used_by?.map((u) => <span class="tag mono">{u}</span>)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        ) : (
          <p class="muted">{plugins ? 'No plugins yet. A connector or runtime names its plugin with use:.' : pending}</p>
        )}
      </section>

      <section class="panel" aria-label="Connectors">
        <div class="panel-head"><span class="panel-title">Connectors</span></div>
        <FlatList rows={conns ? conns.connectors ?? [] : undefined} pending={pending} empty="No connectors. A connector is how conductor hears from a service (a repo host, a chat, a schedule); add one under connectors: in the config." />
      </section>

      <section class="panel" aria-label="Runtimes">
        <div class="panel-head"><span class="panel-title">Runtimes</span></div>
        <FlatList rows={conns ? conns.runtimes ?? [] : undefined} pending={pending} empty="No runtimes yet. A runtime is where agent steps and bots do their work (an agent CLI on this machine, or a remote one); add one under runtimes: when you need it." />
      </section>

      <section class="panel" aria-label="Hosts">
        <div class="panel-head">
          <span class="panel-title">Hosts</span>
          {conns && !addHost ? (
            <div class="row-actions">
              <button class="btn primary sm" type="button" data-testid="add-host" onClick={() => setAddHost(true)}>Add a host</button>
            </div>
          ) : null}
        </div>
        {addHost ? <AddHost onDone={() => setAddHost(false)} /> : null}
        {conns?.hosts?.length ? (
          <table class="tbl">
            <thead><tr><th>Name</th><th>Host</th><th>User</th></tr></thead>
            <tbody>
              {conns.hosts.map((h) => (
                <tr><td class="mono">{h.name}</td><td class="mono">{h.host}</td><td>{h.user || '—'}</td></tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p class="muted">{conns ? 'No hosts yet. A host is another machine conductor can run steps on, over SSH with your usual keys. Add one when a workflow needs to work somewhere else.' : pending}</p>
        )}
      </section>
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
      setMsg('give the host a name and an address')
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
        <input class="field" value={name} placeholder="build-box" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
      </label>
      <label class="form-field">
        <span>Address</span>
        <input class="field" value={addr} placeholder="build01.example.com" onInput={(e) => setAddr((e.target as HTMLInputElement).value)} />
      </label>
      <label class="form-field">
        <span>Sign in as (optional)</span>
        <input class="field" value={user} placeholder="your usual SSH user" onInput={(e) => setUser((e.target as HTMLInputElement).value)} />
      </label>
      {msg ? <div class="banner err" role="alert">{msg}</div> : null}
      <p class="muted small">conductor connects with this machine's SSH keys and known hosts. The new host is reviewed and applied above.</p>
      <div class="row-actions">
        <button class="btn" type="button" onClick={onDone}>Cancel</button>
        <button class="btn primary" type="button" data-testid="stage-host" disabled={busy.value} onClick={() => void add()}>Add host</button>
      </div>
    </div>
  )
}

function FlatList({ rows, empty, pending }: { rows?: { name: string; use: string }[] | null; empty: string; pending: string }): JSX.Element {
  if (!rows) return <p class="muted">{pending}</p>
  if (!rows.length) return <p class="muted">{empty}</p>
  return (
    <table class="tbl">
      <thead><tr><th>Name</th><th>Uses</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr><td class="mono">{r.name}</td><td class="mono">{r.use || '—'}</td></tr>
        ))}
      </tbody>
    </table>
  )
}
