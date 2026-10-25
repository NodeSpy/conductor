// Secrets & vaults (design §5.4). Read-only inventory: the named vaults and the
// top-level secret refs, with where each is used. Secret VALUES are never read
// or shown — the backend returns names and <vault>/<key> references only.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { SecretsResponse } from '../api/types'

export function Secrets(): JSX.Element {
  const [data, setData] = useState<SecretsResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.secrets().then(setData).catch((e) => setErr(String(e)))
  }, [])

  return (
    <div class="col">
      <h1 class="screen-title serif">Secrets</h1>
      <p class="muted">Vaults and secret references. Values are never shown — only names and where they are used.</p>
      {err ? <div class="banner err">{err}</div> : null}

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
          <p class="muted">No vaults configured.</p>
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
          <p class="muted">No secret references.</p>
        )}
      </section>
    </div>
  )
}
