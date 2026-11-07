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
import { botsApi, displayName, type BotSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { TabsBar } from '../components/TabsBar'
import { stage } from '../configStore'
import { seg } from '../router'
import { status, load } from '../state'
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
// dl-tabs/dl-subnav styling, with the generic TabsBar's fit/overflow
// behaviour (UI.fitTabs applies to both .tabs and .dl-tabs rows) so a narrow
// viewport gets a "More" tab instead of clipping, and the active tab always
// stays visible — including the Access tab on iPhone.
function SystemTabs({ active }: { active: Tab }): JSX.Element {
  const paused = status.value?.paused
  const items = TABS.map((t) => ({
    id: t.id,
    label: t.label,
    href: t.href,
    count: t.id === 'safety' && paused ? '!' : null,
    hot: true,
  }))
  return <TabsBar items={items} active={active} ariaLabel="System" className="dl-tabs dl-subnav" />
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
// agents may reach, what runs outside the sandbox, containers, technical
// details, and what each bot may do. Ported close to line-for-line.
// "Running outside the sandbox" reads outsideSandboxItems
// (internal/dashboard/sections.go) — the same steps `conductor validate`
// warns about (trust: full / isolation: {mode: none}). Containers has no
// backend (no container runtime is wired into conductor yet) so it honestly
// shows "No containers are running." — the prototype's own empty copy for
// that card, true today for every box. Pause/resume calls POST
// /api/v1/system/pause (handleSetPaused, same control file as `conductor
// pause`/`resume`). "What each bot may do" reuses BotDetail's access summary
// (botCanSummary) for every bot the dashboard knows about.
function Safety(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const reload = () => api.system().then(setSys).catch((e) => setErr(String(e)))
  useEffect(() => void reload(), [])
  const s = sys?.safety
  const paused = status.value?.paused
  const on = s ? s.sandbox_agents !== 'off' : true

  async function togglePause() {
    if (!paused && !confirm("Pause all automation? Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish; nothing is lost from your config.")) return
    setBusy(true)
    try {
      await api.setPaused(!paused)
      await load()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

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

      {paused ? <PauseCard paused busy={busy} onToggle={() => void togglePause()} /> : null}

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
                      <input type="radio" name="sb-kind" checked readOnly />
                      <span><b>Built-in sandbox</b> <span class="dl-sub">recommended</span>
                        <span class="dl-why">Light and fast; each bot gets a fresh, private space. <span class="dl-sub">(namespaces on Linux, Seatbelt on macOS)</span></span>
                      </span>
                    </label>
                    <label class="sb-kind">
                      <input type="radio" name="sb-kind" disabled />
                      <span><b>Container</b>
                        <span class="dl-why">Each bot gets its own small computer that keeps the tools it installs. <span class="dl-sub">(a Docker or Podman container, not offered on this box yet)</span></span>
                      </span>
                    </label>
                  </div>
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
            <header class="dl-ch"><h2>Running outside the sandbox</h2><span class="dl-sub">{s?.outside?.length ?? 0}</span></header>
            {s?.outside?.length ? (
              <ul class="dl-list">
                {s.outside.map((o) => (
                  <li class="acc-row" key={o.what}>
                    <div class="dl-nt">
                      <b>{o.what}</b>
                      <span class="dl-why">{o.why} Set in {o.who}.</span>
                    </div>
                  </li>
                ))}
              </ul>
            ) : (
              <p class="dl-sub" style="padding:0 22px 18px">Nothing: every agent runs in the sandbox. A pack can never turn it off.</p>
            )}
          </section>
          <section class="dl-card" aria-label="Containers">
            <header class="dl-ch"><h2>Containers</h2></header>
            <p class="dl-sub" style="padding:0 22px 18px">No containers are running.</p>
          </section>
          <details class="dl-card dl-tech-d acc-tech">
            <summary><Icon name="chev" />Details</summary>
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

      <WhatEachBotMayDo />

      {paused ? null : <div style="margin-top:28px"><PauseCard paused={false} busy={busy} onToggle={() => void togglePause()} /></div>}
    </div>
  )
}

// PauseCard: the prototype's one "stop" template, shown twice (top when
// paused, bottom as a card when not) — same copy and action either way, just
// the header/button words and tone flip with the state (js/screens/bots.js
// UI.route('safety')).
function PauseCard({ paused, busy, onToggle }: { paused: boolean; busy: boolean; onToggle: () => void }): JSX.Element {
  return (
    <section class={'dl-card safety-stop' + (paused ? ' need' : '')} id="pause" aria-label={paused ? 'All automation is paused' : 'Pause all automation'}>
      <header class="dl-ch"><h2>{paused ? 'All automation is paused' : 'Pause all automation'}</h2></header>
      <p>
        {paused
          ? "Nothing new starts: workflows don't run and bots don't pick up new work. Runs that were already going have finished or are finishing."
          : "An emergency stop. Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish; nothing is lost from your config."}
      </p>
      <p class="dl-sub">
        {paused
          ? 'Resuming starts dispatching again straight away.'
          : 'Resume here (or from Needs you, where it waits while paused). Events that arrive while paused may not be kept.'}
      </p>
      <div class="dl-actions">
        <button type="button" class={'btn ' + (paused ? 'primary' : 'secondary danger-text')} disabled={busy} onClick={onToggle}>
          <Icon name={paused ? 'play' : 'pause'} />{paused ? 'Resume automation' : 'Pause all automation…'}
        </button>
      </div>
    </section>
  )
}

// WhatEachBotMayDo (prototype's second Safety heading, same page): every bot,
// in words — reusing the same capabilities.does/asks the Bots list and a
// bot's own "What it can do" tab already compute server-side
// (BotSummary.capabilities, internal/dashboard's bot read model). The
// prototype's "Never" column has no equivalent list in that read model
// either (BotDetail's own access tab leaves it empty for the same reason),
// so it stays an honest "Anything it isn't granted" rather than a fabricated
// list.
function WhatEachBotMayDo(): JSX.Element | null {
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  useEffect(() => {
    botsApi.bots().then((r) => setBots(r.bots)).catch(() => setBots([]))
  }, [])
  if (!bots || !bots.length) return null
  const own = bots.reduce((a, b) => a + (b.capabilities?.does?.length || 0), 0)
  return (
    <>
      <div class="dl-phead" style="margin-top:28px">
        <div>
          <h2>What each bot may do</h2>
          <p class="dl-sub" style="margin-top:4px">
            {own ? `${own} action${own > 1 ? 's' : ''} beyond reading runs without asking.` : 'Nothing beyond reading runs without asking.'}
          </p>
        </div>
      </div>
      <div class="stack" style="margin-top:14px">
        {bots.map((b) => <BotMayDoPanel key={b.bot} b={b} />)}
      </div>
    </>
  )
}

function BotMayDoPanel({ b }: { b: BotSummary }): JSX.Element {
  const does = b.capabilities?.does || []
  const asks = b.capabilities?.asks || []
  const grp = (label: string, s: string, items: string[], empty: string) => (
    <div class="cang" data-s={s}>
      <h3><StatusMark s={s} mkOnly label={label} />{label}</h3>
      <ul>{items.length ? items.map((v) => <li key={v} class="mono">{v}</li>) : <li class="dl-sub">{empty}</li>}</ul>
    </div>
  )
  return (
    <div class="panel">
      <div class="panel-head">
        <Avatar id={b.identity} size={28} />
        <h3><a class="link-plain" href={`#/bots/${b.bot}/access`}>{displayName(b.identity, b.bot)}</a></h3>
        <span class="muted small">{b.team ? `team ${b.team}` : 'solo'}</span>
        <div class="tools"><a class="btn sm ghost" href={`#/bots/${b.bot}/access`}><Icon name="edit" />Change</a></div>
      </div>
      <div class="panel-body">
        {b.shadow ? <p class="dl-sub">In practice mode: even what it does on its own is only recorded, never done.</p> : null}
        <div class="dl-can-grid">
          {grp('Does on its own', 'ok', does, b.shadow ? "Nothing — it's in practice mode, so it only suggests." : 'Nothing: it asks first')}
          {grp('Asks you first', 'ember', asks, 'Nothing needs your approval')}
          {grp('Never', 'fail', [], "Anything it isn't granted")}
        </div>
      </div>
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
            <p class="dl-sub" style="padding:0 22px 18px">This dashboard shows the version running now. To see what changed in other releases, or go back to one you've run before, use <span class="mono">conductor update</span> on the box.</p>
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
            This is the policy conductor's own updater follows on the box. Checking for a release, installing one from here, and
            rolling back all happen through <span class="mono">conductor update</span>, not this page.
          </p>
        </div>
      </div>
    </div>
  )
}
