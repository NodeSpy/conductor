// Secrets & vaults (design §5.4). Read-only inventory: the named vaults and the
// top-level secret refs, with where each is used. Secret VALUES are never read
// or shown — the backend returns names and <vault>/<key> references only.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { SecretsResponse } from '../api/types'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

export function Secrets(): JSX.Element {
  const [data, setData] = useState<SecretsResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.secrets().then(setData).catch((e) => setErr(humanError(e)))
  }, [])

  const nothing = data !== null && !data.vaults?.length && !data.secrets?.length
  return (
    <div class="col">
      <h1 class="screen-title serif">Secrets</h1>
      {nothing ? (
        <EmptyState testid="secrets-empty" art={<KeyGlyph />} title="No secrets yet" action={{ label: 'Open the config', href: '#/config', testid: 'secrets-empty-config' }}>
          <p>Secrets are the tokens and passwords your workflows and bots use without ever seeing them. They live in a vault and are referred to by name; their values never show on this dashboard.</p>
          <p>Add a vault under <code>vaults:</code> in the config (the built-in <code>conductor</code> type needs nothing else), then store values with <code>conductor vault &lt;name&gt; add &lt;key&gt;</code>.</p>
        </EmptyState>
      ) : (
        <SecretsLists data={data} err={err} />
      )}
    </div>
  )
}

function SecretsLists({ data, err }: { data: SecretsResponse | null; err: string }): JSX.Element {
  return (
    <>
      <p class="muted">Vaults and secret references. Values are never shown — only names and where they are used.</p>
      {err ? <div class="banner err" role="alert">Couldn't load secrets: {err}</div> : null}
      {data === null ? (
        err ? null : <p class="muted">Loading…</p>
      ) : (
        <>
      <section class="panel" aria-label="Vaults">
        <div class="panel-head"><span class="panel-title">Vaults</span></div>
        {data?.vaults?.length ? (
          <table class="tbl">
            <thead><tr><th>Name</th><th>Type</th></tr></thead>
            <tbody>
              {data.vaults.map((v) => (
                <tr><td class="mono">{v.name}</td><td>{v.type || '—'}</td></tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p class="muted">No vaults. Add one under <span class="mono">vaults:</span> in the config.</p>
        )}
      </section>

      <section class="panel" aria-label="Secret references">
        <div class="panel-head"><span class="panel-title">Secret references</span></div>
        {data?.secrets?.length ? (
          <table class="tbl">
            <thead><tr><th>Name</th><th>Reference</th><th>Used by</th></tr></thead>
            <tbody>
              {data.secrets.map((s) => (
                <tr>
                  <td class="mono">{s.name}</td>
                  <td class="mono">{s.ref}</td>
                  <td>{s.used_by?.length ? s.used_by.map((u) => <span class="tag mono">{u}</span>) : <span class="muted">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p class="muted">Nothing refers to a secret yet. A step or connector that needs one names it as <span class="mono">&lt;vault&gt;/&lt;key&gt;</span>.</p>
        )}
      </section>
        </>
      )}
    </>
  )
}

// KeyGlyph is a key: the empty state's picture.
function KeyGlyph(): JSX.Element {
  return (
    <svg width="64" height="40" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="16" cy="20" r="10" />
      <circle cx="16" cy="20" r="3" opacity="0.55" />
      <path d="M26 20h32M50 20v8M42 20v6" />
    </svg>
  )
}
