// Plugins & Connections (design §5.4). A flat list of the plugins referenced by
// `use:` across connectors and runtimes, each with its version policy (pinned to
// a version, or floating) and whether it is a conductor built-in; then the
// connectors, runtimes and hosts as flat lists. A plugin update that changes
// what the plugin may do waits for the operator's approval while the running
// build keeps working: it is listed first, running version → held version
// with what it adds and removes, and can be approved (the held build replaces
// the running one) or declined (the running build stays) here.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PluginsResponse, ConnectionsResponse, PluginHeld } from '../api/types'

export function Connections(): JSX.Element {
  const [plugins, setPlugins] = useState<PluginsResponse | null>(null)
  const [conns, setConns] = useState<ConnectionsResponse | null>(null)
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')
  const loadPlugins = () => api.plugins().then(setPlugins).catch((e) => setErr(String(e)))
  useEffect(() => {
    loadPlugins()
    api.connections().then(setConns).catch((e) => setErr(String(e)))
  }, [])
  const decide = (send: (h: PluginHeld) => Promise<{ message: string }>) => async (h: PluginHeld) => {
    setErr('')
    setNote('')
    try {
      const ack = await send(h)
      setNote(ack.message)
      loadPlugins()
    } catch (e) {
      setErr(String(e))
    }
  }
  const held = plugins?.plugins?.flatMap((p) => (p.held ? [p.held] : [])) ?? []

  return (
    <div class="col">
      <h1 class="screen-title serif">Plugins & Connections</h1>
      <p class="muted">Plugins come from the <span class="mono">use:</span> references on connectors and runtimes. The version policy is per plugin: pinned to a version, or floating.</p>
      {err ? <div class="banner err">{err}</div> : null}
      {note ? <div class="banner ok" role="status">{note}</div> : null}

      {held.length ? (
        <section class="panel" aria-label="Updates waiting for your approval" data-held-updates>
          <div class="panel-head"><span class="panel-title">Updates waiting for your approval</span></div>
          <p class="muted">These updates change what the plugin may do. Each running build keeps working exactly as it is until you approve its update.</p>
          {held.map((h) => (
            <Held held={h} onApprove={decide(api.approvePlugin)} onDecline={decide(api.declinePlugin)} />
          ))}
        </section>
      ) : null}

      <section class="panel" aria-label="Plugins">
        <div class="panel-head"><span class="panel-title">Plugins</span></div>
        {plugins?.plugins?.length ? (
          <table class="tbl">
            <thead><tr><th>Plugin</th><th>Version policy</th><th>Kind</th><th>Used by</th></tr></thead>
            <tbody>
              {plugins.plugins.map((p) => (
                <tr data-plugin={p.name}>
                  <td class="mono">{p.name}{p.held ? <> <span class="tag warn" data-held>update waiting for approval</span></> : null}</td>
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

// version is the release part of a tag ("connectors/github/v1.5.0" → "v1.5.0").
function version(tag: string): string {
  return tag.slice(tag.lastIndexOf('/') + 1)
}

function Held({ held, onApprove, onDecline }: { held: PluginHeld; onApprove: (h: PluginHeld) => void; onDecline: (h: PluginHeld) => void }): JSX.Element {
  return (
    <article class="held" data-held-plugin={held.name}>
      <div class="held-head">
        <span class="mono held-name">{held.name}</span>
        <span class="held-versions">
          <span class="tag mono" title={held.running}>running {version(held.running)}</span>
          <span aria-hidden="true">→</span>
          <span class="tag warn mono" title={held.version}>held {version(held.version)}</span>
        </span>
      </div>
      <div class="diff held-diff" aria-label={`What ${version(held.version)} changes`}>
        {held.adds?.map((a) => (
          <div class="dl add" data-adds><span class="gutter">+</span><span>{a}</span></div>
        ))}
        {held.removes?.map((r) => (
          <div class="dl del" data-removes><span class="gutter">−</span><span>{r}</span></div>
        ))}
      </div>
      <p class="muted held-legend">+ it may do this, and {version(held.running)} may not · − {version(held.running)} may, and it no longer asks to</p>
      <div class="row-actions held-actions">
        <button class="btn primary sm" type="button" data-testid="approve-plugin" onClick={() => onApprove(held)}>Approve {version(held.version)}</button>
        <button class="btn ghost sm" type="button" data-testid="decline-plugin" onClick={() => onDecline(held)}>Keep {version(held.running)}</button>
      </div>
    </article>
  )
}
