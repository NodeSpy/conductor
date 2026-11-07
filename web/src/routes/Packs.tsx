// Packs (CONFORMANCE "Packs": packs only — bundles of workflows — with
// Installed and Browse tabs; compact rows; Add a pack opens the catalog;
// guided overrides). Hold/unhold and remove are config edits: they stage
// through the same pipeline (packs.<name>.hold / delete packs.<name>) and
// apply with a backup and reload via the shared review panel.
//
// Pack overrides (CONFORMANCE "Pack overrides"): roles → compatible
// connections, triggers with consent, steps' runtime/model — all staged
// through the same pipeline, from GET /api/v1/packs/{name}/manifest (the
// pack's vendored requires: matched against this instance's live bindings).
// See PackOverrides below.
//
// Left out (no backend): Browse — the prototype's registry search is its own
// fake data (`REG` in system.js); the daemon has no catalog/search API, so
// there is nothing to list or page through here. The tab says so and points
// at the CLI, which is the only way a pack gets installed today. A step's
// full builder (phase 5) is not wired into the overrides view — only runtime
// and model are guided fields here; anything else is a YAML-second fallback
// (shown as the staged JSON, since no raw-YAML editor exists anywhere in this
// port — see the Config screen's own note).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { PacksResponse, PackManifestResponse, PackRoleInfo } from '../api/types'
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

  const s1 = seg(1)
  if (s1 && s1 !== 'browse') {
    return <PackOverrides name={s1} />
  }
  const tab: Tab = s1 === 'browse' ? 'browse' : 'installed'
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
                          <a class="btn ghost sm" href={`#/packs/${encodeURIComponent(p.name)}`}>Customize</a>
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

// PackOverrides: the guided edit for one pack instance (CONFORMANCE "Pack
// overrides"). Every field here stages a real config edit at
// packs.<name>.<field> through the shared pipeline; nothing applies until
// Review → Apply.
function PackOverrides({ name }: { name: string }): JSX.Element {
  const [man, setMan] = useState<PackManifestResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    void loadConfig()
    api.packManifest(name).then(setMan).catch((e) => setErr(humanError(e)))
  }, [name])

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>{name}</h1>
          <p class="lede-2">Overrides for this pack instance. Each stages an edit at <span class="mono">packs.{name}.…</span>; nothing changes until you review and apply it.</p>
        </div>
        <div class="dl-actions tight"><a class="btn secondary" href="#/packs">Back to packs</a></div>
      </div>

      {err ? <div class="banner err" role="alert">Couldn't load this pack's manifest: {err}</div> : null}
      <ReviewPanel stagedCount={staged.value.length} />

      {!man ? (
        <p class="muted">{err ? '' : 'Loading…'}</p>
      ) : (
        <>
          {renderRoles('Connections', 'connectors', man.connectors, name)}
          {renderRoles('Stores', 'stores', man.stores, name)}
          {renderRoles('Secrets', 'secrets', man.secrets, name)}
          {renderRoles('Handoffs', 'handoffs', man.handoffs, name)}

          <section class="dl-card" aria-label="Triggers">
            <header class="dl-ch"><h2>Triggers</h2><span class="dl-sub">shipped disarmed; arming is the consent — a repo scope is required</span></header>
            {man.triggers?.length ? (
              <ul class="dl-list">
                {man.triggers.map((t) => (
                  <li class="po-row" key={t.name}>
                    <span class="dl-nt"><b>{t.name}</b><span class="dl-why mono">{t.on}</span></span>
                    <span class="cfg-who">{t.armed ? <span class="tag" data-s="ok">armed · {t.repos?.join(', ') || 'no repos'}</span> : <span class="tag">disarmed</span>}</span>
                    <TriggerArmControl name={name} trigger={t.name} armed={t.armed} repos={t.repos ?? []} />
                  </li>
                ))}
              </ul>
            ) : (
              <p class="dl-sub" style="padding:8px 22px 18px">This pack ships no triggers.</p>
            )}
          </section>

          <section class="dl-card" aria-label="Steps">
            <header class="dl-ch"><h2>Steps</h2><span class="dl-sub">change a step's runtime or model; anything else needs the config directly</span></header>
            {man.steps?.length ? (
              <ul class="dl-list">
                {man.steps.map((st) => <StepOverrideRow key={st.ref} name={name} step={st} />)}
              </ul>
            ) : (
              <p class="dl-sub" style="padding:8px 22px 18px">This pack's workflows define no named steps to override.</p>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function renderRoles(title: string, kind: string, roles: PackRoleInfo[] | undefined, pack: string): JSX.Element | null {
  if (!roles?.length) return null
  return (
    <section class="dl-card" aria-label={title}>
      <header class="dl-ch"><h2>{title}</h2><span class="dl-sub">what this pack needs, bound to one of yours</span></header>
      <ul class="dl-list">
        {roles.map((r) => (
          <li class="po-row" key={r.name}>
            <span class="dl-nt"><b>{r.name}</b><span class="dl-why">role · {r.kind}</span></span>
            {r.candidates?.length ? (
              <select
                class="select"
                value={r.bound ?? ''}
                onChange={(e) => {
                  const v = (e.target as HTMLSelectElement).value
                  void (v ? stage(['packs', pack, kind, r.name], v, `bind ${r.name}`) : stageDelete(['packs', pack, kind, r.name], `unbind ${r.name}`))
                }}
              >
                <option value="">— not bound —</option>
                {r.candidates.map((c) => <option value={c}>{c}</option>)}
              </select>
            ) : (
              <input
                class="input mono"
                placeholder={`a ${r.kind} name`}
                value={r.bound ?? ''}
                onChange={(e) => {
                  const v = (e.target as HTMLInputElement).value
                  void (v ? stage(['packs', pack, kind, r.name], v, `bind ${r.name}`) : stageDelete(['packs', pack, kind, r.name], `unbind ${r.name}`))
                }}
              />
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

function TriggerArmControl({ name, trigger, armed, repos }: { name: string; trigger: string; armed: boolean; repos: string[] }): JSX.Element {
  const [repoText, setRepoText] = useState(repos.join(', '))
  if (armed) {
    return (
      <button class="btn sm ghost" type="button" onClick={() => void stage(['packs', name, 'triggers', trigger, 'enabled'], false, `disarm ${trigger}`)}>
        Disarm
      </button>
    )
  }
  return (
    <span class="row-actions">
      <input class="input mono sm" placeholder="org/repo, …" value={repoText} onInput={(e) => setRepoText((e.target as HTMLInputElement).value)} />
      <button
        class="btn sm primary"
        type="button"
        disabled={!repoText.trim()}
        onClick={() => {
          const list = repoText.split(',').map((s) => s.trim()).filter(Boolean)
          void stage(['packs', name, 'triggers', trigger, 'repos'], list, `scope ${trigger}`)
          void stage(['packs', name, 'triggers', trigger, 'enabled'], true, `arm ${trigger}`)
        }}
      >
        Arm with consent
      </button>
    </span>
  )
}

function StepOverrideRow({ name, step }: { name: string; step: { ref: string; runtime?: string; model?: string; overridden?: boolean } }): JSX.Element {
  const [runtime, setRuntime] = useState(step.runtime ?? '')
  const [model, setModel] = useState(step.model ?? '')
  return (
    <li class="po-row">
      <span class="dl-nt"><b class="mono">{step.ref}</b>{step.overridden ? <span class="dl-why">your override</span> : <span class="dl-why">as the pack wrote it</span>}</span>
      <input class="input mono sm" placeholder="runtime" value={runtime} onInput={(e) => setRuntime((e.target as HTMLInputElement).value)} />
      <input class="input mono sm" placeholder="model" value={model} onInput={(e) => setModel((e.target as HTMLInputElement).value)} />
      <button
        class="btn sm ghost"
        type="button"
        onClick={() => {
          const v: Record<string, string> = {}
          if (runtime.trim()) v.runtime = runtime.trim()
          if (model.trim()) v.model = model.trim()
          void (Object.keys(v).length
            ? stage(['packs', name, 'steps', step.ref], v, `override ${step.ref}`)
            : stageDelete(['packs', name, 'steps', step.ref], `clear override on ${step.ref}`))
        }}
      >
        Stage
      </button>
    </li>
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
