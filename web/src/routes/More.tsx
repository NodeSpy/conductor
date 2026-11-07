// More (phone only): the rail is hidden below 720px, so Settings and the
// machine card move here, reached from the tab bar's fifth item. No approved
// mock covers this page name directly — it mirrors the rail's own Settings
// group so every route stays reachable on a phone.
import type { JSX } from 'preact'
import { status } from '../state'
import { Icon } from '../components/Icon'

const ITEMS: { key: string; label: string; icon: string }[] = [
  { key: 'connections', label: 'Plugins', icon: 'connectors' },
  { key: 'packs', label: 'Packs', icon: 'extensions' },
  { key: 'secrets', label: 'Secrets', icon: 'secrets' },
  { key: 'system', label: 'System', icon: 'settings' },
  { key: 'config', label: 'Config', icon: 'settings' },
  { key: 'audit', label: 'Logs & audit', icon: 'live' },
  { key: 'security', label: 'Access', icon: 'secrets' },
]

export function More(): JSX.Element {
  const st = status.value
  return (
    <>
      <div class="dl-phead">
        <div>
          <h1>More</h1>
        </div>
      </div>
      <section class="dl-card">
        <nav class="nav">
          {ITEMS.map((it) => (
            <a href={'#/' + it.key}>
              <Icon name={it.icon} />
              <span class="lbl">{it.label}</span>
            </a>
          ))}
        </nav>
      </section>
      {st ? (
        <p class="dl-foot-note mono">
          conductor {st.version} · {st.paused ? 'paused' : 'running'}
        </p>
      ) : null}
    </>
  )
}
