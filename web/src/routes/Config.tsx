// The Config screen (design §5.3): edit the live config file one value at a time.
// Each scalar is a field that stages a comment-preserving splice edit; staged
// edits collect in a review (diff + effects + widening), and Apply writes the
// file with a backup and reloads the daemon. Edits never touch comments or
// sections this binary does not know — the edit is spliced, not re-serialized.
import type { JSX } from 'preact'
import { useEffect } from 'preact/hooks'
import { config, staged, cfgError, loadConfig, stage, unstage } from '../configStore'
import type { ConfigLeaf } from '../api/types'
import { ReviewPanel } from '../components/ReviewPanel'

// Sections whose scalars are safe to edit inline. Everything else is read-only
// in this screen (bots, teams, workflows have their own screens / are edited as
// text) — but all sections are shown so the config is legible.
const EDITABLE = new Set(['settings', 'dashboard', 'update', 'sandbox', 'store', 'usage', 'callable', 'memory'])

function groupBySection(leaves: ConfigLeaf[]): Map<string, ConfigLeaf[]> {
  const m = new Map<string, ConfigLeaf[]>()
  for (const l of leaves) {
    const arr = m.get(l.section) ?? []
    arr.push(l)
    m.set(l.section, arr)
  }
  return m
}

export function Config(): JSX.Element {
  useEffect(() => {
    void loadConfig()
  }, [])

  const c = config.value
  const stagedPaths = new Set(staged.value.map((s) => s.path.join('.')))

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Config</h1>
          <p class="lede-2">
            Editing <span class="mono">{c?.path ?? '…'}</span>
            {c?.sha ? <span class="mono"> · {c.sha}</span> : null}. Changes are staged, reviewed, then applied with a backup and a reload.
          </p>
        </div>
      </div>

      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}

      {staged.value.length ? (
        <section class="dl-card" aria-label="Staged changes">
          <div class="panel-head" style="padding:14px 22px 0"><h2 style="margin:0;font-size:15px">Staged ({staged.value.length})</h2></div>
          <ul class="staged-list">
            {staged.value.map((s) => (
              <li key={s.id}>
                <span class="mono id">{s.path.join('.')}</span>
                {s.delete ? (
                  <span class="staged-val">(remove)</span>
                ) : isStructured(s.value) ? (
                  <pre class="staged-val staged-tree mono" data-testid="staged-tree">{JSON.stringify(s.value, null, 2)}</pre>
                ) : (
                  <span class="staged-val">{fmt(s.value)}</span>
                )}
                <button class="btn ghost sm" type="button" onClick={() => void unstage(s.id)}>
                  Discard
                </button>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <ReviewPanel stagedCount={staged.value.length} />

      {c
        ? [...groupBySection(c.leaves ?? [])].map(([section, leaves]) => (
            <section class="dl-card" aria-label={section} key={section}>
              <div class="cfg-head" style="padding:14px 22px 0">
                <h2 style="margin:0;font-size:15px">{sectionTitle(section)}</h2>
                <span class="cfg-head-sub">
                  <span class="mono">{section}</span>
                  {!EDITABLE.has(section) ? <span class="tag">read-only here</span> : null}
                </span>
              </div>
              <div class="fields">
                {leaves.map((l) => (
                  <Field key={l.path.join('.')} leaf={l} editable={EDITABLE.has(section)} staged={stagedPaths.has(l.path.join('.'))} />
                ))}
              </div>
            </section>
          ))
        : null}
    </div>
  )
}

function Field({ leaf, editable, staged: isStaged }: { leaf: ConfigLeaf; editable: boolean; staged: boolean }): JSX.Element {
  const path = leaf.path.join('.')
  const sub = leaf.path.slice(1).join('.') || path
  const value = fmt(leaf.value)
  // A long value (a path, a URL) gets a wrapping box across the whole row, so
  // all of it can be read; a short one stays a one-line input.
  const long = leaf.kind === 'string' && value.length > 28

  const commit = (raw: string | boolean) => {
    let value: unknown = raw
    if (leaf.kind === 'int') value = Number(raw)
    else if (leaf.kind === 'float') value = Number(raw)
    else if (leaf.kind === 'bool') value = raw === true || raw === 'true'
    void stage(leaf.path, value)
  }

  return (
    <label class={'field' + (isStaged ? ' is-staged' : '') + (long ? ' wide' : '')} data-path={path}>
      <span class="cfg-label">
        <span class="cfg-name">{leafLabel(leaf.path)}</span>
        {sub !== leaf.path[leaf.path.length - 1] ? <span class="field-path mono">{sub}</span> : null}
      </span>
      {!editable ? (
        <span class="field-ro mono">{value}</span>
      ) : leaf.kind === 'bool' ? (
        <input
          type="checkbox"
          checked={leaf.value === true}
          data-path={path}
          onChange={(e) => commit((e.target as HTMLInputElement).checked)}
        />
      ) : long ? (
        <textarea
          class="input field-long mono"
          rows={1}
          ref={fit}
          value={value}
          data-path={path}
          onInput={(e) => fit(e.target as HTMLTextAreaElement)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              ;(e.target as HTMLTextAreaElement).blur()
            }
          }}
          onChange={(e) => commit((e.target as HTMLTextAreaElement).value)}
        />
      ) : (
        <input
          type={leaf.kind === 'int' || leaf.kind === 'float' ? 'number' : 'text'}
          class="input mono"
          value={fmt(leaf.value)}
          data-path={path}
          onChange={(e) => commit((e.target as HTMLInputElement).value)}
        />
      )}
      {isStaged ? <span class="field-badge">staged</span> : null}
    </label>
  )
}

// Plain names for the core config's sections; anything else is its key,
// humanized. The dotted key is shown beside it for anyone who edits the file.
const SECTION_TITLES: Record<string, string> = {
  settings: 'Settings',
  dashboard: 'Dashboard',
  store: 'Where conductor keeps its state',
  update: 'Updates',
  sandbox: 'Sandbox',
  usage: 'Usage limits',
  callable: 'Workflows other tools can call',
  memory: 'Memory',
  connectors: 'Connections',
  runtimes: 'Runtimes',
  hosts: 'Hosts',
  vaults: 'Vaults',
  secrets: 'Secrets',
  packs: 'Packs',
  bots: 'Bots',
  teams: 'Teams',
  operators: 'Operators',
  triggers: 'Workflows',
  browser: 'Browser',
  policy: 'Policy',
}

function sectionTitle(s: string): string {
  return SECTION_TITLES[s] ?? humanKey(s)
}

// Plain names for keys whose humanized form would still read as jargon.
const KEY_LABELS: Record<string, string> = {
  state_file: 'State file',
  allow_open: 'Open without sign-in',
  listen: 'Listens on',
  use: 'Plugin',
}

// leafLabel names a value by its key; a list item ("listen.0") by its list,
// numbered from the second on.
function leafLabel(path: string[]): string {
  const last = path[path.length - 1]
  // "enabled" says what it turns on: "Dashboard on".
  if (last === 'enabled' && path.length > 1) return humanKey(path[path.length - 2]) + ' on'
  if (/^\d+$/.test(last) && path.length > 1) {
    const n = Number(last)
    return humanKey(path[path.length - 2]) + (n > 0 ? ` (${n + 1})` : '')
  }
  return humanKey(last)
}

// fit grows a long value's box to show all of it, at any width.
function fit(el: HTMLTextAreaElement | null): void {
  if (!el) return
  el.style.height = 'auto'
  el.style.height = el.scrollHeight + 2 + 'px'
}

// humanKey turns a config key into a label: "state_file" → "State file".
function humanKey(k: string): string {
  if (KEY_LABELS[k]) return KEY_LABELS[k]
  const words = k.replace(/[_-]+/g, ' ').trim()
  return words ? words[0].toUpperCase() + words.slice(1) : k
}

// A staged map or list (a whole section staged at once) is shown in full as
// indented JSON, never as "[object Object]".
function isStructured(v: unknown): boolean {
  return typeof v === 'object' && v !== null
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return ''
  return String(v)
}
