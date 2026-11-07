// Preferences (design/dashboard-prototype js/screens/config.js
// UI.displayPrefsHTML / js/screens/runs.js UI.notifyPrefsHTML /
// UI.route('preferences')): how the dashboard looks and how it tells you
// things, for this browser only — a localStorage preference, not a config
// change, so it needs no review. "Show advanced" and "Show planned features"
// are stored here the same way the prototype stores them (D.prefs.advanced /
// D.prefs.planned) but nothing else in this app reads them yet — no form
// anywhere has an "advanced" section to show or hide, so the toggle has
// nothing to flip on. Kept (not dropped) because the prototype's own switch
// does the same today when nothing on a given screen uses it; the switch
// itself works and its state persists.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { themeSignal, setTheme } from '../lib/theme'

type Density = 'comfortable' | 'compact'
type UsageDisplay = 'off' | 'subscription' | 'api'

function boolPref(key: string, def = false): boolean {
  const v = localStorage.getItem(key)
  return v === null ? def : v === '1'
}
function setBoolPref(key: string, v: boolean): void {
  localStorage.setItem(key, v ? '1' : '0')
}

function Switch({ on, label, onChange }: { on: boolean; label: JSX.Element | string; onChange: (v: boolean) => void }): JSX.Element {
  return (
    <label class="row">
      <button type="button" role="switch" class={'switch' + (on ? ' on' : '')} aria-checked={on} aria-label={typeof label === 'string' ? label : undefined} onClick={() => onChange(!on)} />
      <span>{label}</span>
    </label>
  )
}

function currentDensity(): Density {
  return document.documentElement.getAttribute('data-density') === 'compact' ? 'compact' : 'comfortable'
}

function setDensity(next: Density) {
  const root = document.documentElement
  if (next === 'compact') {
    root.setAttribute('data-density', 'compact')
    localStorage.setItem('density', 'compact')
  } else {
    root.removeAttribute('data-density')
    localStorage.removeItem('density')
  }
}

function Seg<T extends string>({ items, value, onChange }: { items: [T, string][]; value: T; onChange: (v: T) => void }): JSX.Element {
  return (
    <div class="seg" role="group">
      {items.map(([v, l]) => (
        <button key={v} type="button" class={v === value ? 'on' : ''} aria-pressed={v === value} onClick={() => onChange(v)}>
          {l}
        </button>
      ))}
    </div>
  )
}

export function Preferences(): JSX.Element {
  const theme = themeSignal.value
  const [density, setDensityState] = useState<Density>(currentDensity)
  const [usage, setUsage] = useState<UsageDisplay>(() => (localStorage.getItem('usage-mode') as UsageDisplay) || 'off')
  const [advanced, setAdvanced] = useState(() => boolPref('pref-advanced'))
  const [planned, setPlanned] = useState(() => boolPref('pref-planned'))
  const [notifyApprovals, setNotifyApprovals] = useState(() => boolPref('pref-notify-approvals', true))
  const [notifyPush, setNotifyPush] = useState(() => boolPref('pref-notify-push', true))
  const [notifyFailed, setNotifyFailed] = useState(() => boolPref('pref-notify-failed', true))

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Preferences</h1>
          <p class="lede-2">For this browser only: they change nothing in conductor and need no review.</p>
        </div>
      </div>
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="panel" id="display">
            <div class="panel-head">
              <h3>How it looks</h3>
            </div>
            <div class="panel-body stack tight small">
              <div class="field">
                <label>Theme</label>
                <Seg
                  items={[
                    ['auto', 'Match my device'],
                    ['light', 'Light'],
                    ['dark', 'Dark'],
                  ]}
                  value={theme}
                  onChange={(v) => setTheme(v)}
                />
              </div>
              <div class="field">
                <label>Density</label>
                <Seg
                  items={[
                    ['comfortable', 'Comfortable'],
                    ['compact', 'Compact'],
                  ]}
                  value={density}
                  onChange={(v) => {
                    setDensity(v)
                    setDensityState(v)
                  }}
                />
                <div class="hint">Compact shows one-line rows in tables, lists and the bot tree. Always comfortable on a phone.</div>
              </div>
              <div class="field">
                <label>Usage display</label>
                <Seg
                  items={[
                    ['off', 'Off'],
                    ['subscription', 'Subscription'],
                    ['api', 'API ($)'],
                  ]}
                  value={usage}
                  onChange={(v) => {
                    setUsage(v)
                    localStorage.setItem('usage-mode', v)
                  }}
                />
                <div class="hint">Off by default. Subscription shows runs per provider; API adds what they cost. Providers are never added together.</div>
              </div>
              <Switch
                on={advanced}
                onChange={(v) => {
                  setAdvanced(v)
                  setBoolPref('pref-advanced', v)
                }}
                label={<><b>Show advanced</b> <span class="muted">puts "Edit as YAML" and technical details within reach on every form; off by default (they stay one click away)</span></>}
              />
              <Switch
                on={planned}
                onChange={(v) => {
                  setPlanned(v)
                  setBoolPref('pref-planned', v)
                }}
                label={<><b>Show planned features</b> <span class="muted">previews of multi-user sign-in, designed but not built yet; off by default</span></>}
              />
            </div>
          </section>
        </div>
        <div class="dl-col-side">
          <section class="dl-card" id="notify">
            <header class="dl-ch"><h2>How you're told</h2></header>
            <div class="dl-prefs">
              <Switch on={notifyApprovals} label="When a bot asks for approval" onChange={(v) => { setNotifyApprovals(v); setBoolPref('pref-notify-approvals', v) }} />
              <Switch on={notifyPush} label="When a browser sign-in needs me" onChange={(v) => { setNotifyPush(v); setBoolPref('pref-notify-push', v) }} />
              <Switch on={notifyFailed} label="When any run fails" onChange={(v) => { setNotifyFailed(v); setBoolPref('pref-notify-failed', v) }} />
            </div>
            <p class="dl-sub">Alerts outside the dashboard (Slack, PagerDuty) are set up as workflows on conductor's own events.</p>
          </section>
        </div>
      </div>
    </div>
  )
}
