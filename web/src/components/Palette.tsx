// ⌘K: one command line for everything (prototype js/palette.js, ported line
// for line). It indexes actions that do what they say, pending approvals,
// every page, and the live things by name — bots, teams, workflows, runs,
// connections, vaults, secret references, settings, config paths and
// glossary terms. The live lists load when the palette opens, in parallel;
// one that fails just leaves its kind out. Results are deduped, and a query
// that finds nothing says so and suggests what to try.
//
// The top bar's `.search` affordance is what opens it (click, Enter/Space,
// ⌘K / Ctrl K); the single-key shortcuts and the shortcut sheet live in
// Shortcuts.tsx.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { createPortal } from 'preact/compat'
import { go } from '../router'
import { Icon } from './Icon'
import { api } from '../api/client'
import { botsApi, type BotSummary, type TeamSummary } from '../api/bots'
import type { ConnectorRow, SecretInfo, VaultInfo, WorkflowSummary } from '../api/types'
import { humanError } from '../api/errors'
import { status, runs, proposalsPending, statusMark, load } from '../state'
import { config, staged, loadConfig } from '../configStore'
import { themeSignal, setTheme } from '../lib/theme'
import { paletteOpen, shortcutsOpen } from '../lib/overlays'
import { TERMS } from '../routes/Glossary'

export interface PaletteItem {
  key: string
  label: string
  icon: string
}

type Kind = 'action' | 'approval' | 'page' | 'bot' | 'team' | 'workflow' | 'run' | 'connector' | 'vault' | 'secret' | 'setting' | 'config' | 'term'

interface Entry {
  kind: Kind
  label: string
  run: () => void
  key: string
  icon?: string
  hint?: string
  words?: string
}

// The live lists the index reads, filled in as each request comes back.
interface Data {
  bots?: BotSummary[]
  teams?: TeamSummary[]
  workflows?: WorkflowSummary[]
  connectors?: ConnectorRow[]
  vaults?: VaultInfo[]
  secrets?: SecretInfo[]
  settings?: Record<string, string>
  update?: string
}

const to = (h: string) => () => go(h)

/** Config paths from the YAML, up to three levels: triggers[ci-fixer].policy.budget, bots.platform-sre.autonomy … */
function configPaths(text: string): [string, number][] {
  const out: [string, number][] = []
  const stack: { ind: number; key: string; list?: boolean }[] = []
  text.split('\n').forEach((line, i) => {
    const m = line.match(/^(\s*)(- )?(?:name:\s*([\w:.-]+)|([\w.@/-]+):)/)
    if (!m) return
    const ind = m[1].length + (m[2] ? 2 : 0)
    while (stack.length && stack[stack.length - 1].ind >= ind) stack.pop()
    if (m[3] && m[2]) {
      const parent = stack[stack.length - 1]
      if (parent) {
        stack.push({ ind: ind - 1, key: `${parent.key}[${m[3]}]`, list: true })
        stack[stack.length - 2] = parent
      }
      return
    }
    const key = m[4]
    if (!key) return
    const parent = stack[stack.length - 1]
    const full = parent ? `${parent.key}.${key}` : key
    if (stack.length < 4) out.push([full, i + 1])
    stack.push({ ind, key: full })
  })
  return out
}

// isDark mirrors the account menu's reading of the theme (Shell.tsx): "auto"
// follows the system, so the action offers the other one of what shows now.
function isDark(): boolean {
  const t = themeSignal.value
  return t === 'dark' || (t === 'auto' && matchMedia('(prefers-color-scheme: dark)').matches)
}

// togglePause is System → Safety's own pause control (Safety's togglePause):
// the same confirm before pausing, the same POST /api/v1/system/pause, then
// a status reload so the host card and Safety read it at once.
async function togglePause(): Promise<void> {
  const paused = !!status.value?.paused
  if (!paused && !confirm('Pause all automation? Nothing new starts until you resume: no workflow runs and no bot picks up new work. Runs already going finish; nothing is lost from your config.')) return
  try {
    await api.setPaused(!paused)
    await load()
  } catch (e) {
    alert(humanError(e))
  }
}

// openTerm goes to the Glossary and brings the term's row into view (the
// prototype's #/glossary#g-<term>; a second # can't ride on a hash route).
function openTerm(k: string): void {
  go('glossary')
  setTimeout(() => document.getElementById('g-' + k)?.scrollIntoView({ block: 'center' }), 60)
}

function runTarget(r: { repo?: string; number?: number }): string {
  return r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : ''
}

// EXTRA_PAGES: screens the rail doesn't list on its own (the prototype's
// Glossary/Raw config/Runtimes/Hosts/Audit log list, at this app's routes).
const EXTRA_PAGES: [string, string, string][] = [
  ['Glossary', 'glossary', 'info'],
  ['Preferences', 'preferences', 'settings'],
  ['Config', 'system/config', 'config'],
  ['Raw config', 'system/config/raw', 'code'],
  ['Safety', 'system/safety', 'shield'],
  ['Updates', 'system/updates', 'updates'],
  ['Logs', 'system/logs', 'logs'],
  ['Audit log', 'system/logs/audit', 'clock'],
  ['Access', 'system/access', 'key'],
  ['Runtimes', 'connections/runtimes', 'server'],
  ['Code languages', 'connections/languages', 'code'],
  ['Machines', 'connections/machines', 'obj-host'],
]

function index(pages: PaletteItem[], d: Data): Entry[] {
  const I: Entry[] = []
  const add = (kind: Kind, label: string, run: () => void, o: Partial<Entry> = {}) => I.push({ kind, label, run, key: kind + '|' + label, ...o })
  // actions first: they are what people type
  add('action', 'New bot', to('bots?new=1'), { icon: 'plus', words: 'create add bot teammate watcher' })
  add('action', 'New workflow', to('workflows?new=1'), { icon: 'plus', words: 'create trigger recipe' })
  add('action', 'Add a login', to('browser/add'), { icon: 'passkey', words: 'credential password site browser' })
  add('action', 'Connect a service (Slack, GitHub…)', to('connections?add=1'), { icon: 'plus', words: 'connector connection plugin slack github pagerduty add integrate' })
  add('action', status.value?.paused ? 'Resume automation' : 'Pause all automation', () => void togglePause(), { icon: 'pause', words: 'stop halt dispatch emergency kill' })
  const pending = staged.value.length
  add('action', 'Review and apply staged changes', to('system/config'), { icon: 'check', words: 'apply diff staged', hint: pending ? `${pending} staged` : 'nothing staged' })
  add('action', 'Switch to the ' + (isDark() ? 'light' : 'dark') + ' theme', () => setTheme(isDark() ? 'light' : 'dark'), { icon: 'sun', words: 'theme dark light mode' })
  add('action', 'What can my bots do? (System → Safety)', to('system/safety'), { icon: 'shield', words: 'safety permissions tiers sandbox' })
  add('action', 'Keyboard shortcuts', () => (shortcutsOpen.value = true), { icon: 'keyboard', words: 'keys help ?' })
  add('action', 'Update conductor', to('system/updates'), { icon: 'updates', words: 'upgrade version', hint: d.update || '' })
  // what needs you
  proposalsPending.value.forEach((p) =>
    add('approval', `Approve or deny ${p.proposal}: ${p.summary || p.action || p.uses}`, to(`bots/${p.bot}/proposals/${p.proposal}`), {
      icon: 'attention',
      words: `approve deny ${p.bot} ${p.uses} ${p.terraform || /^tf|terraform/.test(p.uses || '') ? 'terraform plan apply' : ''} ${JSON.stringify(p.options || {}).slice(0, 200)}`,
    }),
  )
  // pages
  pages.forEach((p) => add('page', p.label, to(p.key), { icon: p.icon }))
  EXTRA_PAGES.forEach(([l, h, ic]) => add('page', l, to(h), { icon: ic }))
  // things
  ;(d.bots || []).forEach((b) => add('bot', b.bot, to('bots/' + b.bot), { icon: 'members', hint: b.team ? 'team ' + b.team : '', words: b.description }))
  ;(d.teams || []).forEach((t) => add('team', t.team, to('teams/' + t.team), { icon: 'members', words: t.description }))
  ;(d.workflows || []).forEach((w) => add('workflow', w.name, to('workflows/' + encodeURIComponent(w.name)), { icon: 'workflows', hint: w.source ? w.source : 'your config', words: `${w.on || ''} ${w.when || ''} ${w.does || ''}`.slice(0, 200) }))
  runs.value.forEach((r) => {
    const t = runTarget(r)
    add('run', `${r.id} · ${r.workflow}${t ? ' · ' + t : ''}`, to(`runs/${r.id}`), { icon: 'clock', hint: statusMark(r.status).word, words: `${r.error || ''} ${r.kind}` })
  })
  ;(d.connectors || []).forEach((c) => add('connector', `${c.name} (${c.use})`, to('connections'), { icon: 'link' }))
  ;(d.vaults || []).forEach((x) => add('vault', x.name, to('secrets'), { icon: 'lock', hint: x.type || '' }))
  ;(d.secrets || []).forEach((x) => add('secret', `${x.vault}/${x.key}`, to('secrets'), { icon: 'lock', words: x.name }))
  Object.keys(d.settings || {}).forEach((k) => add('setting', '${settings.' + k + '}', to('secrets/settings'), { icon: 'code' }))
  configPaths(config.value?.text || '').forEach(([p, line]) => add('config', p, to(`system/config/raw?line=${line}`), { icon: 'code', hint: `config.yaml line ${line}` }))
  TERMS.forEach(([k, t, desc]) => add('term', `${t}: what it means`, () => openTerm(k), { icon: 'info', words: desc }))
  const seen = new Set<string>()
  return I.filter((x) => (seen.has(x.key) ? false : (seen.add(x.key), true)))
}

const RANK: Record<Kind, number> = { action: 0, approval: 1, page: 2, bot: 3, team: 3, workflow: 3, run: 4, connector: 5, vault: 6, secret: 6, setting: 6, config: 7, term: 8 }

function search(all: Entry[], q: string): Entry[] {
  q = q.trim().toLowerCase()
  if (!q) return all.filter((x) => ['action', 'approval', 'page'].includes(x.kind)).slice(0, 14)
  const toks = q.split(/\s+/)
  return all
    .map((x): [number, Entry] | null => {
      const hay = `${x.label} ${x.kind} ${x.hint || ''} ${x.words || ''}`.toLowerCase()
      const lab = x.label.toLowerCase()
      if (!toks.every((t) => hay.includes(t))) return null
      let s = RANK[x.kind] * 10
      if (lab.startsWith(q)) s -= 30
      else if (lab.includes(q)) s -= 15
      return [s, x]
    })
    .filter((x): x is [number, Entry] => !!x)
    .sort((a, b) => a[0] - b[0])
    .map(([, x]) => x)
    .slice(0, 30)
}

// loadData fetches every live list the index reads, in parallel; each one
// lands on its own, and a failure leaves that kind out.
function loadData(set: (f: (d: Data) => Data) => void): void {
  const merge = (p: Promise<Partial<Data>>) => p.then((part) => set((d) => ({ ...d, ...part }))).catch(() => {})
  merge(botsApi.bots().then((r) => ({ bots: r.bots || [], teams: r.teams || [] })))
  merge(api.workflows().then((r) => ({ workflows: r.workflows || [] })))
  merge(api.connections().then((r) => ({ connectors: r.connectors || [] })))
  merge(api.secrets().then((r) => ({ vaults: r.vaults || [], secrets: r.secrets || [] })))
  merge(api.system().then((r) => ({ settings: r.settings || {}, update: r.update?.available || '' })))
  if (!config.value) void loadConfig()
}

export function Palette({ items }: { items: PaletteItem[] }): JSX.Element {
  const open = paletteOpen.value
  const openPalette = () => {
    if (!paletteOpen.value) paletteOpen.value = true
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        openPalette()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <>
      <div class="search" role="button" tabIndex={0} aria-label="Search runs, bots and settings" onClick={openPalette} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), openPalette())}>
        <Icon name="search" />
        <span class="lbl">Search runs, bots and settings</span>
        <span class="kbd">⌘K</span>
      </div>
      {open ? <PaletteDialog items={items} /> : null}
    </>
  )
}

function PaletteDialog({ items }: { items: PaletteItem[] }): JSX.Element {
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const [data, setData] = useState<Data>({})
  const inputRef = useRef<HTMLInputElement>(null)
  const opener = useRef<Element | null>(document.activeElement)

  const close = () => {
    paletteOpen.value = false
    // closing hands focus back (ui.js)
    const o = opener.current as HTMLElement | null
    if (o && o.isConnected && typeof o.focus === 'function') o.focus()
  }

  useEffect(() => {
    loadData(setData)
    const onEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        close()
      }
    }
    document.addEventListener('keydown', onEsc, true)
    const t = setTimeout(() => inputRef.current?.focus(), 10)
    return () => {
      document.removeEventListener('keydown', onEsc, true)
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Reading the signals here keeps the index current while the palette is
  // open (a run that lands, the theme, the paused state, staged changes).
  void status.value
  void runs.value
  void proposalsPending.value
  void staged.value
  void config.value
  void themeSignal.value
  const all = index(items, data)
  const list = search(all, q)
  const cur = Math.min(sel, Math.max(0, list.length - 1))

  const run = (x: Entry) => {
    close()
    x.run()
  }
  const setQuery = (v: string) => {
    setQ(v)
    setSel(0)
  }

  // on <body>, as the prototype mounts it (inside the top strip it would
  // inherit the strip's smaller type)
  return createPortal(
    <div>
      <div class="scrim" id="pal-scrim" style={{ placeItems: 'start center' }} onClick={close} />
      <div class="palette" role="dialog" aria-label="Command line">
        <input
          ref={inputRef}
          placeholder="Type a command, a name, a run id, a config key…"
          aria-label="Search or run a command"
          autocomplete="off"
          spellcheck={false}
          value={q}
          onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              setSel(Math.min(cur + 1, list.length - 1))
              e.preventDefault()
            }
            if (e.key === 'ArrowUp') {
              setSel(Math.max(cur - 1, 0))
              e.preventDefault()
            }
            if (e.key === 'Enter' && list[cur]) {
              e.preventDefault()
              run(list[cur])
            }
            if (e.key === 'Escape') {
              e.preventDefault()
              close()
            }
          }}
        />
        <div class="res" role="listbox">
          {list.length ? (
            list.map((x, i) => (
              <a
                href="#"
                role="option"
                data-i={i}
                key={x.key}
                class={i === cur ? 'on' : ''}
                aria-selected={i === cur}
                onClick={(e) => {
                  e.preventDefault()
                  run(x)
                }}
              >
                <Icon name={x.icon || 'dot'} />
                <span class="grow ellipsis">{x.label}</span>
                {x.hint ? <span class="muted tiny ellipsis" style={{ maxWidth: '40%' }}>{x.hint}</span> : null}
                <span class="pal-kind">{x.kind}</span>
              </a>
            ))
          ) : (
            <div class="empty small pal-empty">
              <b>No results for “{q.trim()}”.</b> Try a bot or workflow name, a run id like r-7ef0, a config key like budget, or a command:{' '}
              {(['new bot', 'approve', 'add a login'] as const).map((s, i) => (
                <>
                  <button
                    class="link"
                    type="button"
                    data-q={s}
                    onClick={() => {
                      setQuery(s)
                      inputRef.current?.focus()
                    }}
                  >
                    {s}
                  </button>
                  {i < 2 ? ', ' : '.'}
                </>
              ))}
            </div>
          )}
        </div>
        <div class="pal-foot muted tiny">
          <span><span class="kbd">↑↓</span> move</span>
          <span><span class="kbd">↵</span> open or run</span>
          <span><span class="kbd">esc</span> close</span>
          <span><span class="kbd">?</span> all shortcuts</span>
        </div>
      </div>
    </div>,
    document.body,
  )
}
