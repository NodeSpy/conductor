// Packs (CONFORMANCE "Packs": packs only — bundles of workflows — with
// Installed and Browse tabs; compact rows; Add a pack opens the catalog;
// guided overrides). Hold/unhold and remove are config edits: they stage
// through the same pipeline (packs.<name>.hold / delete packs.<name>) and
// apply with a backup and reload via the shared review panel.
//
// Left out (no backend): Browse — the prototype's registry search is its own
// fake data (`REG` in system.js); the daemon has no catalog/search API, so
// there is nothing to list or page through here. The tab says so and points
// at the CLI, which is the only way a pack gets installed today. Pack
// overrides (roles → connections, triggers, steps via the builder) have no
// daemon surface either; Customize still only links each pack back to
// Workflows, where its own triggers already live.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PacksResponse } from '../api/types'
import { staged, loadConfig, stage, stageDelete } from '../configStore'
import { ReviewPanel } from '../components/ReviewPanel'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { TabsBar } from '../components/TabsBar'
import { seg } from '../router'

type Tab = 'installed' | 'browse'

export function Packs(): JSX.Element {
  const [data, setData] = useState<PacksResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.packs().then(setData).catch((e) => setErr(humanError(e)))
    void loadConfig()
  }, [])

  const tab: Tab = seg(1) === 'browse' ? 'browse' : 'installed'
  const stagedPaths = new Set(staged.value.map((s) => s.path.join('.')))

  return (
    <div class="col m-page">
      <div class="dl-phead">
        <div>
          <h1>Packs</h1>
          <p class="lede-2">A pack is a bundle of workflows someone else wrote, installed and updated as one unit.</p>
        </div>
      </div>

      {data !== null && !data.packs?.length && tab === 'installed' ? (
        <>
          <ReviewPanel stagedCount={staged.value.length} />
          <TabsBar items={[{ id: 'installed', label: 'Installed', href: '#/packs' }, { id: 'browse', label: 'Browse', href: '#/packs/browse' }]} active={tab} ariaLabel="Packs" />
          <EmptyState testid="packs-empty" art={<BoxGlyph />} title="No packs installed">
            <p>Packs are added on the conductor machine, not from the dashboard: adding one fetches it and asks you to review what it can do. Find one with <code>conductor pack search</code>, then add it with <code>conductor pack add &lt;source&gt;</code>.</p>
            <p>Once it's installed it shows up here, where you can hold it at its version or remove it.</p>
          </EmptyState>
        </>
      ) : (
        <>
          {err ? <div class="banner err" role="alert">Couldn't load packs: {err}</div> : null}
          <ReviewPanel stagedCount={staged.value.length} />
          <TabsBar
            items={[
              { id: 'installed', label: 'Installed', href: '#/packs', count: data?.packs?.length || null },
              { id: 'browse', label: 'Browse', href: '#/packs/browse' },
            ]}
            active={tab}
            ariaLabel="Packs"
          />

          {tab === 'installed' ? (
            <section class="dl-card" aria-label="Installed packs">
              {data?.packs?.length ? (
                <ul class="dl-list dl-conns pack-list">
                  {data.packs.map((p) => {
                    const pendingHold = stagedPaths.has(`packs.${p.name}.hold`)
                    return (
                      <li class="dl-conn" data-pack={p.name} data-pending-hold={pendingHold ? '1' : undefined} key={p.name}>
                        <span class="dl-ng"><Icon name="extensions" /></span>
                        <span class="dl-nt">
                          <b>{p.name}</b>
                          <span class="dl-why mono">{p.use}{p.version ? ` @ ${p.version}` : ' · floating'}</span>
                          <span class="dl-why">{p.hold ? <span class="tag" data-s="idle">held</span> : <span class="tag">tracking updates</span>}</span>
                        </span>
                        <span class="dl-na">
                          <button class="btn ghost sm" type="button" onClick={() => void stage(['packs', p.name, 'hold'], !p.hold, p.hold ? 'resume updates' : 'hold updates')}>
                            {p.hold ? 'Resume updates' : 'Hold'}
                          </button>
                          <button class="btn ghost sm" type="button" onClick={() => void stageDelete(['packs', p.name], 'remove pack')}>
                            Remove
                          </button>
                        </span>
                      </li>
                    )
                  })}
                </ul>
              ) : (
                <p class="dl-sub" style="padding:16px 22px">{err ? 'Nothing to show until conductor answers.' : 'Loading…'}</p>
              )}
            </section>
          ) : (
            <section class="dl-card" aria-label="Browse" data-testid="packs-browse-gap">
              <p class="dl-sub" style="padding:16px 22px">
                conductor has no pack catalog or search API yet, so there's nothing to browse here. Find a pack with{' '}
                <span class="mono">conductor pack search</span> on the box, then add it with <span class="mono">conductor pack add &lt;source&gt;</span>; it shows up on Installed once applied.
              </p>
            </section>
          )}
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
