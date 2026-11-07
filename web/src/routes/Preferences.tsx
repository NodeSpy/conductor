// Preferences (design/dashboard-prototype js/screens/config.js
// UI.displayPrefsHTML / UI.route('preferences')): how the dashboard looks, for
// this browser only — it changes nothing in conductor and needs no review.
// Ported scope for this phase is theme, density and motion (CONFORMANCE);
// usage display, advanced and planned-feature toggles stay in System -> Config,
// which already owns that state.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { themeSignal, setTheme } from '../lib/theme'

type Density = 'comfortable' | 'compact'
type Motion = 'standard' | 'reduced'

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

function currentMotion(): Motion {
  const v = document.documentElement.getAttribute('data-motion')
  return v === 'standard' ? 'standard' : v === 'reduced' ? 'reduced' : matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduced' : 'standard'
}

function setMotion(next: Motion, explicit: boolean) {
  const root = document.documentElement
  if (!explicit) {
    root.removeAttribute('data-motion')
    localStorage.removeItem('motion')
  } else {
    root.setAttribute('data-motion', next)
    localStorage.setItem('motion', next)
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
  const [motionExplicit, setMotionExplicit] = useState(() => localStorage.getItem('motion') === 'reduced' || localStorage.getItem('motion') === 'standard')
  const [motion, setMotionState] = useState<Motion>(currentMotion)

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Preferences</h1>
          <p class="lede-2">For this browser only: they change nothing in conductor and need no review.</p>
        </div>
      </div>
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
            <label>Motion</label>
            <Seg
              items={[
                ['standard', 'Standard'],
                ['reduced', 'Reduced'],
              ]}
              value={motion}
              onChange={(v) => {
                setMotion(v, true)
                setMotionExplicit(true)
                setMotionState(v)
              }}
            />
            <div class="hint">{motionExplicit ? 'Set for this browser, overriding your device.' : "Follows your device's reduce-motion setting."}</div>
          </div>
        </div>
      </section>
    </div>
  )
}
