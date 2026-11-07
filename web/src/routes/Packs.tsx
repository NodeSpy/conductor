// Packs (design §5.4). The installed pack instances, each with its source and
// pinned version. Hold/unhold and remove are config edits: they stage through
// the same pipeline (packs.<name>.hold / delete packs.<name>) and apply with a
// backup and reload via the shared review panel.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PacksResponse } from '../api/types'
import { staged, loadConfig, stage, stageDelete } from '../configStore'
import { ReviewPanel } from '../components/ReviewPanel'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

export function Packs(): JSX.Element {
  const [data, setData] = useState<PacksResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.packs().then(setData).catch((e) => setErr(humanError(e)))
    void loadConfig()
  }, [])

  const stagedPaths = new Set(staged.value.map((s) => s.path.join('.')))

  return (
    <div class="col">
      <h1 class="screen-title serif">Packs</h1>
      {data !== null && !data.packs?.length ? (
        <>
          <ReviewPanel stagedCount={staged.value.length} />
          <EmptyState testid="packs-empty" art={<BoxGlyph />} title="No packs installed">
            <p>A pack is a ready-made set of workflows (reviewing pull requests, fixing CI) that you connect to your own accounts and secrets.</p>
            <p>Find one with <code>conductor pack search</code>, then add it with <code>conductor pack add &lt;source&gt;</code>. It shows up here to hold or remove.</p>
          </EmptyState>
        </>
      ) : (
        <>
      <p class="muted">Installed packs. Hold pins a pack against updates; remove deletes it — both are staged as config edits and applied with a backup.</p>
      {err ? <div class="banner err" role="alert">Couldn't load packs: {err}</div> : null}

      <ReviewPanel stagedCount={staged.value.length} />

      <section class="panel" aria-label="Installed packs">
        {data?.packs?.length ? (
          <table class="tbl">
            <thead><tr><th>Name</th><th>Source</th><th>Version</th><th>Status</th><th></th></tr></thead>
            <tbody>
              {data.packs.map((p) => {
                const pendingHold = stagedPaths.has(`packs.${p.name}.hold`)
                return (
                  <tr data-pack={p.name} data-pending-hold={pendingHold ? '1' : undefined}>
                    <td class="mono">{p.name}</td>
                    <td class="mono">{p.use}</td>
                    <td class="mono">{p.version || 'floating'}</td>
                    <td>{p.hold ? <span class="tag warn">held</span> : <span class="tag">tracking</span>}</td>
                    <td class="row-actions">
                      <button class="btn ghost sm" type="button" onClick={() => void stage(['packs', p.name, 'hold'], !p.hold, p.hold ? 'resume updates' : 'hold updates')}>
                        {p.hold ? 'Resume updates' : 'Hold'}
                      </button>
                      <button class="btn ghost sm danger" type="button" onClick={() => void stageDelete(['packs', p.name], 'remove pack')}>
                        Remove
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        ) : (
          <p class="muted">{err ? 'Nothing to show until conductor answers.' : 'Loading…'}</p>
        )}
      </section>
        </>
      )}
    </div>
  )
}

// BoxGlyph is a parcel: the empty state's picture.
function BoxGlyph(): JSX.Element {
  return (
    <svg width="56" height="52" viewBox="0 0 56 52" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round">
      <path d="M28 4l22 10v24L28 48 6 38V14z" />
      <path d="M6 14l22 10 22-10M28 24v24" opacity="0.6" />
      <path d="M17 9l22 10" opacity="0.45" />
    </svg>
  )
}
