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
import { Icon } from '../components/Icon'
import { stage } from '../configStore'
import { seg } from '../router'
import { status } from '../state'
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

// SystemTabs is the prototype's UI.systemTabs (js/ui.js): System's own
// sub-navigation, ported verbatim (class names, the "!" mark when Safety needs
// a look) rather than the generic .tabs component Secrets and Plugins use —
// the two read at different weights on purpose (CONFORMANCE).
function SystemTabs({ active }: { active: Tab }): JSX.Element {
  const paused = status.value?.paused
  return (
    <nav class="dl-tabs dl-subnav" aria-label="System">
      {TABS.map((t) => (
        <a key={t.id} href={t.href} class={t.id === active ? 'on' : ''} aria-current={t.id === active ? 'page' : undefined}>
          {t.label}
          {t.id === 'safety' && paused ? <span class="count hot">!</span> : null}
        </a>
      ))}
    </nav>
  )
}

export function System(): JSX.Element {
  const s = seg(1)
  const tab: Tab = (['config', 'safety', 'updates', 'logs', 'access'] as Tab[]).includes(s as Tab) ? (s as Tab) : 'config'

  return (
    <div class="col m-page">
      <SystemTabs active={tab} />
      {tab === 'config' ? <Config /> : null}
      {tab === 'safety' ? <Safety /> : null}
      {tab === 'updates' ? <Updates /> : null}
      {tab === 'logs' ? <Audit /> : null}
      {tab === 'access' ? <Access /> : null}
    </div>
  )
}

// Safety (prototype js/screens/bots.js UI.route('safety') + UI.sandboxHTML in
// system.js): the emergency pause (when paused), the sandbox card, the sites
// agents may reach, what runs outside the sandbox, containers, and technical
// details. Ported close to line-for-line; "kind of sandbox", "running outside
// the sandbox" and "containers" have no read-model in SystemResponse yet
// (design gap, see the report) so those stay in place showing their honest
// unavailable state rather than inventing data — "what each bot may do" (a
// second, per-bot section in the prototype) is the same gap, one level up,
// and is left for the Bots phase that already owns that page.
function Safety(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const reload = () => api.system().then(setSys).catch((e) => setErr(String(e)))
  useEffect(() => void reload(), [])
  const s = sys?.safety
  const paused = status.value?.paused
  const on = s ? s.sandbox_agents !== 'off' : true

  async function toggleSandbox() {
    if (!s) return
    if (on) {
      if (!confirm('Turn the sandbox off? Agents would run as you, with your files and your network: nothing confines what they touch. Applying it asks for your passkey.')) return
      await stage(['sandbox', 'agents'], 'off')
    } else {
      await stage(['sandbox', 'agents'], '')
    }
    void reload()
  }

  const [newSite, setNewSite] = useState('')
  async function addSite() {
    const v = newSite.trim()
    if (!v || !s) return
    await stage(['sandbox', 'egress'], [...(s.egress ?? []), v])
    setNewSite('')
    void reload()
  }
  async function removeSite(site: string) {
    if (!s) return
    await stage(['sandbox', 'egress'], (s.egress ?? []).filter((e) => e !== site))
    void reload()
  }

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Safety</h1>
          <p class="dl-lead">
            <b>Nothing risky happens without you.</b> Agents run sandboxed, and every bot does only what you allowed: some
            things on its own, some only after asking you, some never.
          </p>
        </div>
      </div>
      {err ? <div class="banner err" role="alert">Couldn't load Safety: {err}</div> : null}

      {paused ? (
        <section class="dl-card safety-stop need" id="pause" aria-label="All automation is paused">
          <header class="dl-ch"><h2>All automation is paused</h2></header>
          <p>Nothing new starts: workflows don't run and bots don't pick up new work. Runs that were already going have finished or are finishing.</p>
          <p class="dl-sub">Resuming starts dispatching again straight away.</p>
          <div class="dl-actions">
            <a class="btn primary" href="#/attention"><Icon name="hold" />Resume from Needs you</a>
          </div>
        </section>
      ) : null}

      <div class="dl-cols" style="margin-top:0">
        <div class="dl-col-main">
          <section class="dl-card sb-card" aria-label="The sandbox">
            <header class="dl-ch"><h2>The sandbox</h2></header>
            {!sys ? (
              <p class="dl-sub" style="padding:0 22px 18px">Loading…</p>
            ) : (
              <>
                <label class="row" style="padding:0 22px">
                  <button type="button" role="switch" class={'switch' + (on ? ' on' : '')} aria-checked={on} aria-label="Run agents in the sandbox" onClick={() => void toggleSandbox()} />
                  <span><b>Run agents in the sandbox</b> <span class="dl-sub">{on ? 'on' : 'off'}</span></span>
                </label>
                <p class="sb-say" style="padding:0 22px">
                  {on
                    ? "Agents can't read your keys or other bots' files, can reach only the sites below, and can't push code themselves: conductor pushes for them, signed as you."
                    : <><b>Off: agents run as you</b>, with your files, your keys and your network. Turning it back on is safe at any time.</>}
                </p>
                <div class="field" style="padding:0 22px 18px">
                  <label>Kind of sandbox</label>
                  <div class="sb-kinds">
                    <label class="sb-kind">
                      <input type="radio" name="sb-kind" checked disabled />
                      <span><b>Built-in sandbox</b> <span class="dl-sub">the only kind this version offers</span></span>
                    </label>
                  </div>
                  <p class="dl-sub">A container sandbox backend isn't wired to a daemon setting yet (design gap).</p>
                </div>
              </>
            )}
          </section>

          <section class="dl-card" aria-label="Sites agents may reach">
            <header class="dl-ch"><h2>Sites agents may reach</h2></header>
            <ul class="dl-list sb-sites">
              {(s?.egress ?? []).length ? (
                s!.egress.map((e) => (
                  <li class="acc-row" key={e}>
                    <span class="mono">{e}</span>
                    <span class="dl-na">
                      <button class="btn sm ghost" type="button" aria-label={`Remove ${e}`} onClick={() => void removeSite(e)}>Remove</button>
                    </span>
                  </li>
                ))
              ) : (
                <li class="dl-sub">None: agents can't reach the network.</li>
              )}
            </ul>
            <div class="sb-add" style="padding:0 22px 18px">
              <input
                class="input mono"
                placeholder="api.example.com:443"
                aria-label="A site agents may reach"
                value={newSite}
                onInput={(e) => setNewSite((e.target as HTMLInputElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    void addSite()
                  }
                }}
              />
              <button type="button" class="btn sm secondary" onClick={() => void addSite()}>
                <Icon name="plus" />Allow this site
              </button>
            </div>
            <p class="dl-sub" style="padding:0 22px 18px">Allowing a new site gives agents more reach, so applying it asks for your passkey.</p>
          </section>
        </div>

        <div class="dl-col-side">
          <section class="dl-card" aria-label="Running outside the sandbox">
            <header class="dl-ch"><h2>Running outside the sandbox</h2></header>
            <p class="dl-sub" style="padding:0 22px 18px">Not available yet: which runtimes or steps opt out isn't in the System read model (design gap).</p>
          </section>
          <section class="dl-card" aria-label="Containers">
            <header class="dl-ch"><h2>Containers</h2></header>
            <p class="dl-sub" style="padding:0 22px 18px">Not available yet: a live container list isn't in the System read model (design gap).</p>
          </section>
          <details class="dl-card dl-tech-d acc-tech">
            <summary>Details</summary>
            <dl class="dl-facts">
              <dt>Agent sandbox</dt><dd class="mono">{s?.sandbox_agents || 'default (on)'}</dd>
              <dt>Egress</dt><dd class="mono">{s?.egress?.length ? s.egress.join(', ') : 'default-deny'}</dd>
              <dt>Dashboard listeners</dt><dd class="mono">{s?.dashboard_listen?.length ? s.dashboard_listen.join(', ') : 'localhost'}</dd>
            </dl>
          </details>
        </div>
      </div>

      {s?.warnings?.length ? (
        <div class="banner attn" role="alert">
          <ul class="widen-list">
            {s.warnings.map((w) => <li key={w}>{w}</li>)}
          </ul>
        </div>
      ) : null}
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
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card" aria-label="Updates">
            <header class="dl-ch"><h2>Running now</h2></header>
            <p class="dl-sub" style="padding:0 22px 18px">
              {sys ? <>Version <span class="mono">{sys.version}</span> · config <span class="mono">{sys.config_sha}</span></> : 'Loading…'}
            </p>
          </section>
          <section class="dl-card" aria-label="Releases">
            <header class="dl-ch"><h2>Releases</h2></header>
            <p class="dl-sub" style="padding:0 22px 18px">Not available yet: a live release list isn't in the System read model (design gap).</p>
          </section>
        </div>
        <div class="dl-col-side">
          <section class="dl-card" aria-label="Update policy">
            <header class="dl-ch"><h2>How updates install</h2></header>
            <div class="dl-prefs" style="padding:0 22px 18px">
              <label class="row">
                <button type="button" role="switch" class={'switch' + (u?.auto ? ' on' : '')} aria-checked={!!u?.auto} aria-label="Install updates automatically" onClick={() => void stage(['update', 'auto'], !(u?.auto ?? false))} />
                <span><b>Install updates automatically</b> <span class="dl-sub">{u?.auto ? 'on' : 'off'}</span></span>
              </label>
              <div class="field">
                <label>Look for updates every</label>
                <input class="input mono" style="width:120px" value={u?.interval ?? ''} placeholder="24h" onChange={(e) => void stage(['update', 'interval'], (e.target as HTMLInputElement).value)} />
              </div>
              <div class="field">
                <label>Apply command</label>
                <input class="input mono" value={u?.apply ?? ''} onChange={(e) => void stage(['update', 'apply'], (e.target as HTMLInputElement).value)} />
              </div>
            </div>
          </section>
          <p class="dl-sub">
            Checking for a specific available release, installing it from here and rolling back to a previous one aren't wired to
            a daemon endpoint yet (design gap): this tab covers the policy conductor's own updater reads, not a live release list.
          </p>
        </div>
      </div>
    </div>
  )
}
