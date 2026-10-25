// Plugins & Connections (design §5.4). A flat list of the plugins referenced by
// `use:` across connectors and runtimes, each with its version policy (pinned to
// a version, or floating) and whether it is a conductor built-in; then the
// connectors, runtimes and hosts as flat lists.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PluginsResponse, ConnectionsResponse } from '../api/types'

export function Connections(): JSX.Element {
  const [plugins, setPlugins] = useState<PluginsResponse | null>(null)
  const [conns, setConns] = useState<ConnectionsResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.plugins().then(setPlugins).catch((e) => setErr(String(e)))
    api.connections().then(setConns).catch((e) => setErr(String(e)))
  }, [])

  return (
    <div class="col">
      <h1 class="screen-title serif">Plugins & Connections</h1>
      <p class="muted">Plugins come from the <span class="mono">use:</span> references on connectors and runtimes. The version policy is per plugin: pinned to a version, or floating.</p>
      {err ? <div class="banner err">{err}</div> : null}

      <section class="panel" aria-label="Plugins">
        <div class="panel-head"><span class="panel-title">Plugins</span></div>
        {plugins?.plugins?.length ? (
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
        ) : (
          <p class="muted">No plugins referenced.</p>
        )}
      </section>

      <section class="panel" aria-label="Connectors">
        <div class="panel-head"><span class="panel-title">Connectors</span></div>
        <FlatList rows={conns?.connectors ?? []} empty="No connectors." />
      </section>

      <section class="panel" aria-label="Runtimes">
        <div class="panel-head"><span class="panel-title">Runtimes</span></div>
        <FlatList rows={conns?.runtimes ?? []} empty="No runtimes." />
      </section>

      <section class="panel" aria-label="Hosts">
        <div class="panel-head"><span class="panel-title">Hosts</span></div>
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
          <p class="muted">No hosts.</p>
        )}
      </section>
    </div>
  )
}

function FlatList({ rows, empty }: { rows: { name: string; use: string }[]; empty: string }): JSX.Element {
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
