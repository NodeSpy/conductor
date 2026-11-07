// The Config screen (design §5.3): edit the live config file one value at a time.
// Each scalar is a field that stages a comment-preserving splice edit; staged
// edits collect in a review (diff + effects + widening), and Apply writes the
// file with a backup and reloads the daemon. Edits never touch comments or
// sections this binary does not know — the edit is spliced, not re-serialized.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { config, staged, cfgError, loadConfig, stage, unstage, history, loadHistory, restoreToReview, restoreId } from '../configStore'
import { api } from '../api/client'
import type { ConfigLeaf, ConfigResponse, HistoryItem, DiffLineDTO } from '../api/types'
import { ReviewPanel } from '../components/ReviewPanel'
import { Icon } from '../components/Icon'
import { ago } from '../lib/format'
import { seg } from '../router'

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
    void loadHistory()
  }, [])

  const c = config.value
  const stagedPaths = new Set(staged.value.map((s) => s.path.join('.')))

  if (seg(2) === 'raw') return <RawConfig c={c} />

  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Config</h1>
          <p class="dl-lead">Edits to a section, a bot or a workflow wait here; nothing applies until you review it.</p>
        </div>
        <div class="dl-actions tight">
          <a class="btn secondary" href="#/system/config/raw"><Icon name="code" />Edit the file</a>
          {staged.value.length ? (
            <a class="btn primary" href="#/system/config">
              Review {staged.value.length} change{staged.value.length > 1 ? 's' : ''}
            </a>
          ) : null}
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

      <HistoryCard />
      <FilesCard path={c?.path} sha={c?.sha} />

      <div class="dl-cols">
        <div class="dl-col-main">
          <details class="panel cfg-sections" data-testid="cfg-edit-sections">
            <summary class="panel-head"><h3>Edit the config by section</h3><span class="muted small">each section in its editor, or in the YAML</span></summary>
            <div class="panel-body">
              {!c ? (
                <p class="dl-sub">Loading…</p>
              ) : (
                [...groupBySection(c.leaves ?? [])].map(([section, leaves]) => (
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
              )}
            </div>
          </details>
        </div>
        <div class="dl-col-side">
          <SafeCard />
        </div>
      </div>
    </div>
  )
}

// RawConfig ("Edit the file" from the Config head): the live config's raw
// text, read-only for this phase — ConfigResponse carries the full text, but
// writing it back as a single edit (rather than the per-path splice the
// section editors use) isn't wired yet (design gap, see the report).
function RawConfig({ c }: { c: ConfigResponse | null }): JSX.Element {
  return (
    <div class="col">
      <div class="dl-phead">
        <div>
          <h1>Config · the file</h1>
          <p class="dl-lead">
            <span class="mono">{c?.path ?? '…'}</span>
            {c?.sha ? <span class="mono"> · {c.sha}</span> : null}
          </p>
        </div>
        <div class="dl-actions tight">
          <a class="btn secondary" href="#/system/config">Back to Config</a>
        </div>
      </div>
      <p class="dl-sub">Read-only here for now: edit a value from a section below, or on the box. Saving this view back as one edit isn't wired yet (design gap).</p>
      <section class="dl-card">
        {c ? <pre class="diff hist-diff mono" style="padding:14px 22px">{c.text}</pre> : <p class="dl-sub" style="padding:14px 22px">Loading…</p>}
      </section>
    </div>
  )
}

// HistoryCard: "applied changes full width, readable at a glance" (design
// CONFORMANCE "System · Config"). Each row names who applied a change and
// when, with the paths it touched; Diff expands the exact lines, and every
// entry but the newest offers a one-click restore (the pipeline's own
// backup → validate → diff → confirm → apply gate, same as a staged edit).
function HistoryCard(): JSX.Element {
  const items = history.value
  return (
    <section class="dl-card cfg-hist" aria-label="Applied changes">
      <header class="dl-ch">
        <h2>Applied changes</h2>
        <span class="dl-sub">newest first · each keeps a backup you can go back to</span>
      </header>
      {items.length ? (
        <>
          <ul class="dl-list">
            {items.map((h, i) => <HistoryRow key={h.id} h={h} canRestore={i > 0} />)}
          </ul>
          <p class="dl-sub cfg-note">Restore goes back to before a change, undoing everything since. It's checked on a copy and backed up before it applies.</p>
        </>
      ) : (
        <p class="dl-sub" style="padding:0 22px 18px">No changes applied yet.</p>
      )}
    </section>
  )
}

// FilesCard: the prototype's "Files" details (what conductor loaded at boot,
// each with its owner and sha). Left at its honest unavailable state — the
// System read model only carries the one live config file's sha
// (ConfigResponse), not the full load list (imports, vendored packs, the
// lockfile) — rather than dropping the section (design gap, see the report).
function FilesCard({ path, sha }: { path?: string; sha?: string }): JSX.Element {
  return (
    <details class="panel dl-files-d">
      <summary class="panel-head"><h3>Files</h3><span class="muted small">what conductor loaded at boot</span></summary>
      <div class="panel-body">
        {path ? (
          <table class="tbl compact responsive">
            <thead><tr><th>file</th><th>sha</th></tr></thead>
            <tbody>
              <tr>
                <td class="mono"><a class="link" href="#/system/config/raw">{path}</a></td>
                <td class="mono">{sha}</td>
              </tr>
            </tbody>
          </table>
        ) : (
          <p class="dl-sub">Loading…</p>
        )}
        <p class="dl-sub" style="margin-top:8px">
          Imports, vendored packs and the lockfile aren't in this list yet (design gap): only the one live config file's sha is read here.
        </p>
      </div>
    </details>
  )
}

// SafeCard: the prototype's static "How a change is kept safe" panel — plain
// facts about the edit pipeline itself, not live data, so it needs no backend.
function SafeCard(): JSX.Element {
  return (
    <details class="panel dl-files-d">
      <summary class="panel-head"><h3>How a change is kept safe</h3></summary>
      <div class="panel-body small stack tight">
        <div class="row guard"><span class="grow">Edits are node-level: comments, anchors and unknown keys are kept.</span></div>
        <div class="row guard"><span class="grow">Validated on a copy, with copied plugin state; conductor validate is not read-only.</span></div>
        <div class="row guard"><span class="grow">A config this binary can't load is never written.</span></div>
        <div class="row guard"><span class="grow">Boot guard: a config that fails at boot is restored automatically.</span></div>
      </div>
    </details>
  )
}

function HistoryRow({ h, canRestore }: { h: HistoryItem; canRestore: boolean }): JSX.Element {
  const [open, setOpen] = useState(false)
  const who = h.operator ? (h.event === 'restore' ? `${h.operator}, restoring ${h.restored_from ?? ''}` : h.operator) : 'Someone (outside the dashboard, or before this audit log)'
  const what = h.changed && h.changed.length
    ? h.changed.length === 1 ? h.changed[0] : `${h.changed.length} paths, incl. ${h.changed[0]}`
    : h.added || h.removed ? `${h.added} added, ${h.removed} removed` : 'no textual change'
  return (
    <li class="cfg-h">
      <div class="cfg-h-row">
        <span class="dl-ng"><Icon name="clock" /></span>
        <span class="dl-nt">
          <b class="mono">{what}</b>
          {open ? <span class="dl-tech mono">{h.sha}</span> : null}
        </span>
        <span class="cfg-who">
          <span>{who}</span>
          <span class="dl-sub">{ago(h.ts)} · {new Date(h.ts).toLocaleString()}</span>
        </span>
        <span class="dl-na">
          <button class="btn sm ghost" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? 'Hide the diff' : 'Diff'}
          </button>
          {canRestore ? (
            <button class="btn sm ghost" type="button" onClick={() => void restoreToReview(h.id)} disabled={!!restoreId.value}>
              Restore to before this…
            </button>
          ) : (
            <span class="dl-sub">in use now</span>
          )}
        </span>
      </div>
      {open ? <HistoryDiff id={h.id} /> : null}
    </li>
  )
}

function HistoryDiff({ id }: { id: string }): JSX.Element {
  const [lines, setLines] = useState<DiffLineDTO[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let live = true
    api.historyDiff(id).then((r) => { if (live) setLines(r.lines ?? []) }).catch((e) => { if (live) setErr(String(e)) })
    return () => { live = false }
  }, [id])
  if (err) return <p class="dl-sub hist-diff">Couldn't load the diff: {err}</p>
  if (!lines) return <p class="dl-sub hist-diff">Loading…</p>
  if (!lines.length) return <p class="dl-sub hist-diff">This change was made outside the dashboard; its diff is in the backup <span class="mono">{id}</span>.</p>
  return (
    <pre class="diff hist-diff" aria-label="Unified diff">
      {lines.map((l) => (
        <div class={'dl ' + l.op}>
          <span class="gutter">{l.op === 'add' ? '+' : l.op === 'del' ? '-' : ' '}</span>
          <span class="dtext">{l.text}</span>
        </div>
      ))}
    </pre>
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
