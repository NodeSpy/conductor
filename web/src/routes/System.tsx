// System (design §5.4): the box's own settings and posture, in tabs.
//   Settings — the free-form settings: map, editable (staged config edits).
//   Safety   — the live least-privilege posture: sandbox, egress, dashboard
//              listeners, with warnings for anything loosened.
//   Updates  — the update: block (auto/interval/apply), editable.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { SystemResponse } from '../api/types'
import { staged, loadConfig, stage } from '../configStore'
import { ReviewPanel } from '../components/ReviewPanel'

type Tab = 'settings' | 'safety' | 'updates'

export function System(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [tab, setTab] = useState<Tab>('settings')
  const [err, setErr] = useState('')
  useEffect(() => {
    api.system().then(setSys).catch((e) => setErr(String(e)))
    void loadConfig()
  }, [])

  return (
    <div class="col">
      <h1 class="screen-title serif">System</h1>
      <p class="muted">
        conductor <span class="mono">{sys?.version ?? '…'}</span>
        {sys?.config_sha ? <span class="mono"> · config {sys.config_sha}</span> : null}
      </p>
      {err ? <div class="banner err">{err}</div> : null}

      <div class="tabs" role="tablist">
        <button class={'tab' + (tab === 'settings' ? ' on' : '')} role="tab" aria-selected={tab === 'settings'} onClick={() => setTab('settings')}>Settings</button>
        <button class={'tab' + (tab === 'safety' ? ' on' : '')} role="tab" aria-selected={tab === 'safety'} onClick={() => setTab('safety')}>Safety</button>
        <button class={'tab' + (tab === 'updates' ? ' on' : '')} role="tab" aria-selected={tab === 'updates'} onClick={() => setTab('updates')}>Updates</button>
      </div>

      <ReviewPanel stagedCount={staged.value.length} />

      {tab === 'settings' ? <Settings sys={sys} /> : null}
      {tab === 'safety' ? <Safety sys={sys} /> : null}
      {tab === 'updates' ? <Updates sys={sys} /> : null}
    </div>
  )
}

function Settings({ sys }: { sys: SystemResponse | null }): JSX.Element {
  const entries = Object.entries(sys?.settings ?? {})
  return (
    <section class="panel" aria-label="Settings">
      {entries.length ? (
        <div class="fields">
          {entries.map(([k, v]) => (
            <label class="field" data-path={`settings.${k}`}>
              <span class="field-label mono">{k}</span>
              <input class="field-input mono" value={v} data-path={`settings.${k}`} onChange={(e) => void stage(['settings', k], (e.target as HTMLInputElement).value)} />
            </label>
          ))}
        </div>
      ) : (
        <p class="muted">No settings defined.</p>
      )}
    </section>
  )
}

function Safety({ sys }: { sys: SystemResponse | null }): JSX.Element {
  const s = sys?.safety
  return (
    <section class="panel" aria-label="Safety">
      {s?.warnings?.length ? (
        <div class="banner attn" role="alert">
          <ul class="widen-list">
            {s.warnings.map((w) => <li>{w}</li>)}
          </ul>
        </div>
      ) : (
        <div class="banner ok">No loosened guardrails — the posture is at defaults.</div>
      )}
      <dl class="facts">
        <div><dt>Agent sandbox</dt><dd class="mono">{s?.sandbox_agents || 'default (on)'}</dd></div>
        <div><dt>Egress</dt><dd class="mono">{s?.egress?.length ? s.egress.join(', ') : 'default-deny'}</dd></div>
        <div><dt>Dashboard listeners</dt><dd class="mono">{s?.dashboard_listen?.length ? s.dashboard_listen.join(', ') : 'localhost'}</dd></div>
      </dl>
    </section>
  )
}

function Updates({ sys }: { sys: SystemResponse | null }): JSX.Element {
  const u = sys?.update
  return (
    <section class="panel" aria-label="Updates">
      <div class="fields">
        <label class="field" data-path="update.auto">
          <span class="field-label mono">auto</span>
          <input type="checkbox" class="field-check" checked={u?.auto ?? false} data-path="update.auto" onChange={(e) => void stage(['update', 'auto'], (e.target as HTMLInputElement).checked)} />
        </label>
        <label class="field" data-path="update.interval">
          <span class="field-label mono">interval</span>
          <input class="field-input mono" value={u?.interval ?? ''} data-path="update.interval" onChange={(e) => void stage(['update', 'interval'], (e.target as HTMLInputElement).value)} />
        </label>
        <label class="field" data-path="update.apply">
          <span class="field-label mono">apply</span>
          <input class="field-input mono" value={u?.apply ?? ''} data-path="update.apply" onChange={(e) => void stage(['update', 'apply'], (e.target as HTMLInputElement).value)} />
        </label>
      </div>
    </section>
  )
}
