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
import { Sigil } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { TabsBar } from '../components/TabsBar'
import { stage, staged, unstage, cfgError } from '../configStore'
import { wfApi } from '../api/wfbuilder'
import { useConfirm, agoText } from '../components/SysBits'
import { off } from '../components/BrowserFields'
import { confirmLabel } from '../api/auth'
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
    // only a tab that needs a look is hot (UI.fitTabs: More carries a dot
    // only when a tab it holds is)
    hot: t.id === 'safety' && !!paused,
  }))
  return <TabsBar items={items} active={active} ariaLabel="System" className="dl-tabs dl-subnav" />
}

export function System(): JSX.Element {
  const s = seg(1)
  const tab: Tab = (['config', 'safety', 'updates', 'logs', 'access'] as Tab[]).includes(s as Tab) ? (s as Tab) : 'config'

  // Plain div, not .col: .col's flex `gap` would add space between the tab
  // bar and the page body that the design doesn't have (its tabs and
  // dl-phead are plain block siblings) — that extra gap was sitting the page
  // title lower than the design's on every System tab.
  return (
    <div class="m-page">
      {tab === 'config' && (seg(2) === 'raw' || seg(2) === 'apply') ? null : <SystemTabs active={tab} />}
      {tab === 'config' ? <Config /> : null}
      {tab === 'safety' ? <Safety /> : null}
      {tab === 'updates' ? <Updates /> : null}
      {tab === 'logs' ? <Audit /> : null}
      {tab === 'access' ? <Access /> : null}
    </div>
  )
}

// Safety (prototype js/screens/bots.js UI.route('safety') + UI.sandboxHTML in
// system.js): the emergency pause (top when paused, bottom when not), the
// sandbox card, the sites agents may reach, what runs outside the sandbox,
// containers, details, and what each bot may do. Sandbox and site edits stage
// through the config pipeline and show as staged here until applied.
// Pause/resume calls POST /api/v1/system/pause (the same control file as
// `conductor pause`/`resume`). "Running outside the sandbox" reads
// outsideSandboxItems (internal/dashboard/sections.go). No container runtime
// is wired into conductor, so Containers says none are running (true today).
function Safety(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmNode, ask] = useConfirm()
  const reload = () => api.system().then(setSys).catch((e) => setErr(String(e)))
  useEffect(() => void reload(), [])
  useEffect(() => {
    try {
      localStorage.setItem('seen-safety', '1')
    } catch {
      /* storage unavailable (private mode) */
    }
  }, [])
  const s = sys?.safety
  const paused = !!status.value?.paused
  const stAgents = staged.value.find((c) => c.path.join('.') === 'sandbox.agents')
  const stEgress = staged.value.find((c) => c.path.join('.') === 'sandbox.egress')
  const sbStaged = staged.value.some((c) => c.path[0] === 'sandbox')
  const agents = stAgents ? (stAgents.delete ? '' : String(stAgents.value ?? '')) : s?.sandbox_agents ?? ''
  const on = agents !== 'off'
  const egress: string[] = stEgress ? (stEgress.delete ? [] : ((stEgress.value as string[]) ?? [])) : s?.egress ?? []

  async function togglePause() {
    const ok = await ask(
      !paused
        ? {
            title: 'Pause all automation?',
            body: (
              <>
                Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish.{' '}
                <span class="dl-sub">Events that arrive while paused may not be kept. Resume from System → Safety or from Needs you.</span>
              </>
            ),
            ok: 'Pause all automation',
            danger: true,
          }
        : { title: 'Resume automation?', body: 'conductor starts dispatching again; new events start their runs.', ok: 'Resume' },
    )
    if (!ok) return
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
      const ok = await ask({
        title: 'Turn the sandbox off?',
        body: 'Agents would run as you, with your files and your network: nothing confines what they touch. It’s staged like any change, and applying it asks you to confirm it’s you.',
        ok: 'Stage turning it off',
        danger: true,
      })
      if (!ok) return
      await stage(['sandbox', 'agents'], 'off', 'Turn the agent sandbox off')
    } else if (stAgents) {
      await unstage(stAgents.id)
    } else {
      await stage(['sandbox', 'agents'], 'on', 'Turn the agent sandbox back on')
    }
  }

  const [newSite, setNewSite] = useState('')
  const [shake, setShake] = useState(false)
  async function addSite() {
    const v = newSite.trim()
    if (!v || !s) {
      setShake(true)
      setTimeout(() => setShake(false), 400)
      ;(document.getElementById('sb-eg-new') as HTMLInputElement | null)?.focus()
      return
    }
    if (!egress.includes(v)) await stage(['sandbox', 'egress'], [...egress, v], `Let agents reach ${v}`)
    setNewSite('')
  }
  async function removeSite(site: string) {
    if (!s) return
    await stage(['sandbox', 'egress'], egress.filter((e) => e !== site), `Stop agents reaching ${site}`)
  }

  const stop = <PauseCard paused={paused} busy={busy} onToggle={() => void togglePause()} />

  return (
    <>
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
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}

      {paused ? stop : null}

      <div class="dl-cols" style="margin-top:0">
        <div class="dl-col-main">
          <section class="dl-card sb-card" aria-label="The sandbox">
            <header class="dl-ch"><h2>The sandbox</h2>{sbStaged ? <StatusMark s="staged" word="Not applied" /> : null}</header>
            <label class="row">
              <button type="button" role="switch" class={'switch' + (on ? ' on' : '')} aria-checked={on} aria-label="Run agents in the sandbox" disabled={!sys} onClick={() => void toggleSandbox()} />
              <span><b>Run agents in the sandbox</b> <span class="dl-sub">{on ? 'on' : 'off'}</span></span>
            </label>
            <p class="sb-say">
              {on
                ? 'Agents can’t read your keys or other bots’ files, can reach only the sites below, and can’t push code themselves: conductor pushes for them, signed as you.'
                : <><b>Off: agents run as you</b>, with your files, your keys and your network. Turning it back on is safe at any time.</>}
            </p>
            <div class="field">
              <label>Kind of sandbox</label>
              <div class="sb-kinds">
                <label class="sb-kind">
                  <input type="radio" name="sb-kind" value="namespace" checked readOnly />
                  <span><b>Built-in sandbox</b> <span class="dl-sub">recommended</span>
                    <span class="dl-why">Light and fast; each bot gets a fresh, private space. <span class="dl-sub">(namespaces on Linux, Seatbelt on macOS)</span></span>
                  </span>
                </label>
                <label class="sb-kind is-off" title="This conductor doesn't run agents in containers">
                  <input type="radio" name="sb-kind" value="container" disabled />
                  <span><b>Container</b>
                    <span class="dl-why">Each bot gets its own small computer that keeps the tools it installs. <span class="dl-sub">(a Docker or Podman container; this conductor doesn’t offer it)</span></span>
                  </span>
                </label>
              </div>
            </div>
            {sbStaged ? <a class="btn sm primary" href="#/system/config/apply">Review the change</a> : null}
          </section>

          <section class="dl-card" aria-label="Sites agents may reach">
            <header class="dl-ch"><h2>Sites agents may reach</h2></header>
            <ul class="dl-list sb-sites">
              {egress.length ? (
                egress.map((e) => (
                  <li class="acc-row" key={e}>
                    <span class="mono">{e}</span>
                    <span class="dl-na">
                      <button class="btn sm ghost" type="button" aria-label={`Remove ${e}`} onClick={() => void removeSite(e)}>Remove</button>
                    </span>
                  </li>
                ))
              ) : (
                <li class="dl-sub">None: agents can’t reach the network.</li>
              )}
            </ul>
            <div class="sb-add">
              <input
                class={'input mono' + (shake ? ' shake' : '')}
                id="sb-eg-new"
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
            <p class="dl-sub">Allowing a new site gives agents more reach, so applying it {confirmLabel('x').endsWith('passkey') ? 'asks for your passkey' : 'asks you to confirm it’s you'}.</p>
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
                    <span class="dl-na"><a class="btn sm secondary" href={/^The runtime /.test(o.what) ? '#/connections' : '#/workflows'}>Change it</a></span>
                  </li>
                ))}
              </ul>
            ) : (
              <p class="dl-sub">Nothing: every agent runs in the sandbox. A pack can never turn it off.</p>
            )}
          </section>
          <section class="dl-card" aria-label="Containers">
            <header class="dl-ch"><h2>Containers</h2></header>
            <p class="dl-sub">No containers are running.</p>
          </section>
          <details class="dl-card dl-tech-d acc-tech">
            <summary><Icon name="chev" />Details</summary>
            <dl class="dl-facts">
              <dt>Agent sandbox</dt><dd class="mono">{s?.sandbox_agents || 'on (the default)'}</dd>
              <dt>Pushing code</dt><dd>conductor pushes for agents, signed as you</dd>
              <dt>The dashboard</dt><dd>its own addresses: <span class="mono">{s?.dashboard_listen?.length ? s.dashboard_listen.join(', ') : 'localhost'}</span></dd>
              <dt>Backend</dt><dd class="mono">built-in · 0 containers</dd>
            </dl>
          </details>
        </div>
      </div>

      {s?.warnings?.length ? (
        <div class="banner attn" role="alert" style="margin-top:20px">
          <ul class="widen-list">
            {s.warnings.map((w) => <li key={w}>{w}</li>)}
          </ul>
        </div>
      ) : null}

      <WhatEachBotMayDo />

      {paused ? null : <div style="margin-top:28px">{stop}</div>}
      {confirmNode}
    </>
  )
}

function PauseCard({ paused, busy, onToggle }: { paused: boolean; busy: boolean; onToggle: () => void }): JSX.Element {
  return (
    <section class={'dl-card safety-stop' + (paused ? ' need' : '')} id="pause" aria-label={paused ? 'All automation is paused' : 'Pause all automation'}>
      <header class="dl-ch"><h2>{paused ? 'All automation is paused' : 'Pause all automation'}</h2></header>
      <p>
        {paused
          ? 'Nothing new starts: workflows don’t run and bots don’t pick up new work. Runs that were already going have finished or are finishing.'
          : 'An emergency stop. Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish; nothing is lost from your config.'}
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

// WhatEachBotMayDo (the prototype's second Safety heading): every bot, in
// words, from the capabilities the bot read model computes (does/asks). Each
// verb reads as its plugin's own description (the daemon's workflow catalog:
// the connection's declared verbs), the raw verb kept as its tooltip.
function WhatEachBotMayDo(): JSX.Element {
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  const [labels, setLabels] = useState<Record<string, string>>({})
  useEffect(() => {
    botsApi.bots().then((r) => setBots(r.bots ?? [])).catch(() => setBots([]))
    wfApi
      .catalog()
      .then((r) => {
        const m: Record<string, string> = {}
        for (const c of r.connections ?? []) for (const v of c.verbs ?? []) if (v.desc) m[`${c.name}.${v.name}`] = v.desc
        setLabels(m)
      })
      .catch(() => {})
  }, [])
  const own = (bots ?? []).reduce((a, b) => a + (b.capabilities?.does?.length || 0), 0)
  return (
    <>
      <div class="dl-phead" style="margin-top:28px">
        <div>
          <h2>What each bot may do</h2>
          <p class="dl-sub" style="margin-top:4px">
            {!bots ? 'Loading…' : !bots.length ? 'No bots yet: each one you add shows here with what it may do.' : own ? `${own} action${own > 1 ? 's' : ''} beyond reading run${own > 1 ? '' : 's'} without asking.` : 'Nothing beyond reading runs without asking.'}
          </p>
        </div>
      </div>
      {bots?.length ? (
        <div class="stack" style="margin-top:14px">
          {bots.map((b) => <BotMayDoPanel key={b.bot} b={b} labels={labels} />)}
        </div>
      ) : null}
    </>
  )
}

function BotMayDoPanel({ b, labels }: { b: BotSummary; labels: Record<string, string> }): JSX.Element {
  const does = b.capabilities?.does || []
  const asks = b.capabilities?.asks || []
  const plain = (v: string) => {
    const t = labels[v] || v
    return t.replace(/^./, (c) => c.toUpperCase())
  }
  const grp = (label: string, s: string, items: string[], empty: string) => (
    <div class="cang" data-s={s}>
      <h3><StatusMark s={s} mkOnly />{label}</h3>
      <ul>{items.length ? items.map((v) => <li key={v} title={v}>{plain(v)}</li>) : <li class="dl-sub">{empty}</li>}</ul>
    </div>
  )
  return (
    <div class="panel">
      <div class="panel-head">
        <Sigil id={b.identity} size="sm" />
        <h3><a class="link-plain" href={`#/bots/${b.bot}/access`}>{displayName(b.identity, b.bot)}</a></h3>
        <span class="muted small">{b.team ? `team ${b.team} · on its own: only things that can be undone` : 'solo'}</span>
        <div class="tools"><a class="btn sm ghost" href={`#/bots/${b.bot}/access`}><Icon name="edit" />Change</a></div>
      </div>
      <div class="panel-body">
        <div class="dl-can compact">
          {b.shadow ? <p class="dl-sub">In practice mode: even what it does on its own is only recorded, never done.</p> : null}
          <div class="dl-can-grid">
            {grp('Does on its own', 'ok', does, 'Nothing: it asks first')}
            {grp('Asks you first', 'ember', asks, 'Nothing needs your approval')}
            {grp('Never', 'fail', [], 'Nothing is refused outright')}
          </div>
        </div>
      </div>
    </div>
  )
}

// Updates (prototype js/screens/system.js UI.route('updates')): Check now
// runs the daemon's real self-updater check (git ls-remote for a newer
// published tag, then download + verify + preflight of it); Install now and
// Go back to vX… call the same doUpdate the CLI uses: fetch, verify,
// preflight, atomic swap, restart, previous binary kept. The policy switches
// stage update.* through the config pipeline. Releases are tags only: the
// daemon has no release dates or notes to show.
function Updates(): JSX.Element {
  const [sys, setSys] = useState<SystemResponse | null>(null)
  const [err, setErr] = useState('')
  const [checking, setChecking] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [confirmNode, ask] = useConfirm()
  const reload = () => api.system().then(setSys).catch((e) => setErr(String(e)))
  useEffect(() => void reload(), [])
  const live = sys?.update
  const st = (k: string) => staged.value.find((c) => c.path.join('.') === 'update.' + k)
  const val = <T,>(k: string, cur: T): T => {
    const c = st(k)
    return c ? (c.delete ? cur : (c.value as T)) : cur
  }
  const u = live
    ? { ...live, auto: val('auto', live.auto), deps: val('deps', live.deps), reload_in_place: val('reload', live.reload_in_place), interval: val('interval', live.interval ?? '') }
    : undefined
  const upStaged = staged.value.some((c) => c.path[0] === 'update')

  async function checkNow() {
    setChecking(true)
    setErr('')
    try {
      setSys(await api.checkUpdate())
    } catch (e) {
      setErr(String(e))
    } finally {
      setChecking(false)
    }
  }

  async function install(version: string, title: string, body: string, ok: string, danger?: boolean) {
    if (!(await ask({ title, body, ok, danger }))) return
    setInstalling(true)
    setErr('')
    try {
      await api.installUpdate(version)
      await reload()
    } catch (e) {
      setErr(String(e))
    } finally {
      setInstalling(false)
    }
  }

  const check = (ok: boolean, t: string) => (
    <li><StatusMark s={ok ? 'ok' : 'wait'} mkOnly /><span>{t}</span></li>
  )

  return (
    <>
      <div class="dl-phead">
        <div><h1>Updates</h1></div>
        <div class="dl-actions tight">
          <button class="btn secondary" type="button" disabled={checking} onClick={checkNow}><Icon name="updates" />{checking ? 'Checking…' : 'Check now'}</button>
        </div>
      </div>
      {err ? <div class="banner err" role="alert">Couldn't reach the updater: {err}</div> : null}
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      {!sys || !u ? (
        <p class="muted" style="margin-top:20px">Loading…</p>
      ) : (
        <div class="dl-cols">
          <div class="dl-col-main">
            {u.available ? (
              <section class="dl-card" aria-label="Found release">
                <header class="dl-ch">
                  <h2>{u.available}</h2>
                  {u.last_check ? <span class="dl-sub">found {agoText(u.last_check)}</span> : null}
                </header>
                <ul class="upd-checks">
                  {check(!!u.checks?.downloaded && !!u.checks?.signature_ok, 'Downloaded, and its signature checks out')}
                  {check(!!u.checks?.config_loads, 'It can load your config (checked on a copy; your live config isn’t touched)')}
                  {check(!!u.checks?.config_loads, 'The plugins it needs are installed or can be fetched')}
                  {check(!!u.auto, u.auto ? `It installs itself within ${u.interval || 'the configured interval'}` : 'Automatic installs are off: install it yourself')}
                </ul>
                {u.check_error ? <p class="banner err" role="alert">{u.check_error}</p> : null}
                <div class="dl-actions">
                  <button class="btn primary" type="button" disabled={installing || !!u.check_error} onClick={() => install(u.available!, `Install ${u.available} now?`, `conductor checks it again, keeps ${u.version} so you can go back, then switches over and restarts. Anything running finishes first.`, 'Install now')}>
                    <Icon name="download" />{installing ? 'Installing…' : 'Install now'}
                  </button>
                  <span class="dl-sub">The same as the automatic install: the current version is kept, so you can go back.</span>
                </div>
                <details class="dl-tech-d">
                  <summary><Icon name="chev" />Technical details</summary>
                  <dl class="dl-facts">
                    <dt>Check</dt><dd class="mono">{u.available} validate (on a copy of the config and plugin state)</dd>
                    <dt>Kept</dt><dd><code>conductor.prev</code>: the previous binary, for rolling back</dd>
                    <dt>Install</dt><dd>an atomic swap of the binary, then a restart; unchanged plugins are reloaded in place when that’s on</dd>
                  </dl>
                </details>
              </section>
            ) : (
              <section class="dl-card" aria-label="Running version">
                <p>
                  {u.checked
                    ? <>You’re on {u.version}, the newest release. Last checked {agoText(u.last_check)}.</>
                    : <>You’re on {u.version}. It hasn’t looked for a newer release since it started: Check now looks.</>}
                </p>
                {u.check_error ? <p class="dl-sub" role="alert">The last check didn’t finish: {u.check_error}</p> : null}
              </section>
            )}
            <section class="dl-card" aria-label="Releases">
              <header class="dl-ch"><h2>Releases</h2></header>
              <ul class="dl-list">
                {(u.releases ?? []).length ? (
                  u.releases!.map((r) => (
                    <li key={r.version} class="upd-rel">
                      <span class="mono">{r.version}</span>
                      {r.current || r.ready ? <div class="dl-nt"><span class="dl-sub">{r.current ? 'running now' : 'found, checked and ready'}</span></div> : <div class="dl-nt" />}
                      <span class="dl-na">
                        {r.current ? (
                          <span class="dl-sub">in use</span>
                        ) : r.ready ? (
                          <span class="dl-sub">ready</span>
                        ) : r.older ? (
                          <button
                            class="btn sm secondary"
                            type="button"
                            disabled={installing}
                            onClick={() => install(r.version, `Go back to ${r.version}?`, `conductor checks that ${r.version} loads your config, then switches back and restarts. ${u.version} is kept, so you can return to it.`, `Go back to ${r.version}`, true)}
                          >
                            <Icon name="undo" />Go back to {r.version}…
                          </button>
                        ) : null}
                      </span>
                    </li>
                  ))
                ) : (
                  <li class="dl-sub">{u.checked ? 'No releases are published for this platform.' : 'Check now lists the published releases.'}</li>
                )}
              </ul>
            </section>
          </div>
          <div class="dl-col-side">
            <section class="dl-card" aria-label="Update policy">
              <header class="dl-ch"><h2>How updates install</h2>{upStaged ? <StatusMark s="staged" word="Not applied" /> : null}</header>
              <div class="dl-prefs">
                <label class="row">
                  <button type="button" role="switch" class={'switch' + (u.auto ? ' on' : '')} aria-checked={!!u.auto} aria-label="Install updates automatically" onClick={() => void stage(['update', 'auto'], !u.auto)} />
                  <span><b>Install updates automatically</b> <span class="dl-sub">{u.auto ? 'on, the default' : 'off'}</span></span>
                </label>
                {u.auto ? (
                  <div class="upd-sub">
                    <label class="row">
                      <button type="button" role="switch" class={'switch' + (u.deps ? ' on' : '')} aria-checked={!!u.deps} aria-label="Keep packs and plugins up to date" onClick={() => void stage(['update', 'deps'], !u.deps)} />
                      <span>Keep packs and plugins up to date too</span>
                    </label>
                    <label class="row">
                      <button type="button" role="switch" class={'switch' + (u.reload_in_place ? ' on' : '')} aria-checked={!!u.reload_in_place} aria-label="Restart only the plugins that changed" onClick={() => void stage(['update', 'reload'], !u.reload_in_place)} />
                      <span>Restart only the plugins that changed</span>
                    </label>
                  </div>
                ) : (
                  <p class="dl-sub">You install each release yourself, from here. To react to a new release (ask someone first, post it), set that up on a workflow.</p>
                )}
                <div class="field">
                  <label for="upd-int">Look for updates every</label>
                  <input class="input mono" id="upd-int" style="width:90px" value={u.interval ?? ''} placeholder="10m" onChange={(e) => void stage(['update', 'interval'], (e.target as HTMLInputElement).value.trim())} />
                </div>
              </div>
              <a class="btn sm primary" href="#/system/config/apply" {...off(upStaged ? '' : 'Change a setting first')} onClick={(e) => { if (!upStaged) e.preventDefault() }}>Review the change</a>
            </section>
            <p class="dl-sub">A release that can’t load this machine’s config is never installed, and the previous version is always kept.</p>
          </div>
        </div>
      )}
      {confirmNode}
    </>
  )
}
