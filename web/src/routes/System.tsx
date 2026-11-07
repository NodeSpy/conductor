// System (CONFORMANCE shell note: the Settings group is Plugins, Packs,
// Secrets and System — Config, Safety, Updates, Logs & audit and Access under
// one item with its own sub-navigation; old #/config, #/safety, #/updates,
// #/audit and #/security links open the matching tab). Prototype source:
// js/ui.js UI.systemTabs and js/screens/system.js's connectors/updates/logs
// routes plus js/screens/access.js's security route.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api } from '../api/client'
import type { SystemResponse } from '../api/types'
import { TabsBar } from '../components/TabsBar'
import { stage } from '../configStore'
import { seg } from '../router'
import { Config } from './Config'
import { Audit } from './Audit'
import { Access } from './Access'
import '../styles/auth.css'

type Tab = 'config' | 'safety' | 'updates' | 'logs' | 'access'

const TABS: { id: Tab; label: string; href: string }[] = [
  { id: 'config', label: 'Config', href: '#/system/config' },
  { id: 'safety', label: 'Safety', href: '#/system/safety' },
  { id: 'updates', label: 'Updates', href: '#/system/updates' },
  { id: 'logs', label: 'Logs & audit', href: '#/system/logs' },
  { id: 'access', label: 'Access', href: '#/system/access' },
]

export function System(): JSX.Element {
  const s = seg(1)
  const tab: Tab = (['config', 'safety', 'updates', 'logs', 'access'] as Tab[]).includes(s as Tab) ? (s as Tab) : 'config'

  return (
    <div class="col m-page">
      <TabsBar items={TABS} active={tab} ariaLabel="System" />
      {tab === 'config' ? <Config /> : null}
      {tab === 'safety' ? <Safety /> : null}
      {tab === 'updates' ? <Updates /> : null}
      {tab === 'logs' ? <Audit /> : null}
      {tab === 'access' ? <Access /> : null}
    </div>
  )
}

function Safety(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.system().then(setSys).catch((e) => setErr(String(e)))
  }, [])
  const s = sys?.safety
  return (
    <div class="col">
      <div class="dl-phead"><div><h1>Safety</h1><p class="lede-2">The live least-privilege posture: the sandbox, egress and dashboard listeners, with a warning for anything loosened.</p></div></div>
      {err ? <div class="banner err" role="alert">Couldn't load Safety: {err}</div> : null}
      {!sys ? (
        <p class="muted">Loading…</p>
      ) : (
        <section class="dl-card" aria-label="Safety">
          {s?.warnings?.length ? (
            <div class="banner attn" role="alert" style="margin:18px 22px 0">
              <ul class="widen-list">
                {s.warnings.map((w) => <li key={w}>{w}</li>)}
              </ul>
            </div>
          ) : (
            <div class="banner ok" style="margin:18px 22px 0">No loosened guardrails — the posture is at defaults.</div>
          )}
          <div class="acc-tech" style="padding:0 22px 18px">
            <dl>
              <dt>Agent sandbox</dt><dd class="mono">{s?.sandbox_agents || 'default (on)'}</dd>
              <dt>Egress</dt><dd class="mono">{s?.egress?.length ? s.egress.join(', ') : 'default-deny'}</dd>
              <dt>Dashboard listeners</dt><dd class="mono">{s?.dashboard_listen?.length ? s.dashboard_listen.join(', ') : 'localhost'}</dd>
            </dl>
          </div>
        </section>
      )}
    </div>
  )
}

function Updates(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.system().then(setSys).catch((e) => setErr(String(e)))
  }, [])
  const u = sys?.update
  return (
    <div class="col">
      <div class="dl-phead"><div><h1>Updates</h1><p class="lede-2">How conductor keeps itself current.</p></div></div>
      {err ? <div class="banner err" role="alert">Couldn't load Updates: {err}</div> : null}
      <section class="dl-card" aria-label="Updates">
        <div class="fields" style="padding:18px 22px">
          <label class="field">
            <span>Update automatically</span>
            <input type="checkbox" checked={u?.auto ?? false} onChange={(e) => void stage(['update', 'auto'], (e.target as HTMLInputElement).checked)} />
          </label>
          <label class="field">
            <span>Interval</span>
            <input class="input mono" value={u?.interval ?? ''} onChange={(e) => void stage(['update', 'interval'], (e.target as HTMLInputElement).value)} />
          </label>
          <label class="field">
            <span>Apply command</span>
            <input class="input mono" value={u?.apply ?? ''} onChange={(e) => void stage(['update', 'apply'], (e.target as HTMLInputElement).value)} />
          </label>
        </div>
      </section>
    </div>
  )
}
