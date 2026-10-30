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
      <h1 class="screen-title serif">Config</h1>
      <p class="muted">
        Editing <span class="mono">{c?.path ?? '…'}</span>
        {c?.sha ? <span class="mono"> · {c.sha}</span> : null}. Changes are staged, reviewed, then applied with a backup and a reload.
      </p>

      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}

      {staged.value.length ? (
        <section class="panel staged-panel" aria-label="Staged changes">
          <div class="panel-head">
            <span class="panel-title">Staged ({staged.value.length})</span>
          </div>
          <ul class="staged-list">
            {staged.value.map((s) => (
              <li>
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
            <section class="panel" aria-label={section}>
              <div class="panel-head">
                <span class="panel-title mono">{section}</span>
                {!EDITABLE.has(section) ? <span class="tag">read-only here</span> : null}
              </div>
              <div class="fields">
                {leaves.map((l) => (
                  <Field leaf={l} editable={EDITABLE.has(section)} staged={stagedPaths.has(l.path.join('.'))} />
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
  const label = leaf.path.slice(1).join('.') || leaf.path.join('.')

  const commit = (raw: string | boolean) => {
    let value: unknown = raw
    if (leaf.kind === 'int') value = Number(raw)
    else if (leaf.kind === 'float') value = Number(raw)
    else if (leaf.kind === 'bool') value = raw === true || raw === 'true'
    void stage(leaf.path, value)
  }

  return (
    <label class={'field' + (isStaged ? ' is-staged' : '')} data-path={path}>
      <span class="field-label mono">{label}</span>
      {!editable ? (
        <span class="field-ro mono">{fmt(leaf.value)}</span>
      ) : leaf.kind === 'bool' ? (
        <input
          type="checkbox"
          class="field-check"
          checked={leaf.value === true}
          data-path={path}
          onChange={(e) => commit((e.target as HTMLInputElement).checked)}
        />
      ) : (
        <input
          type={leaf.kind === 'int' || leaf.kind === 'float' ? 'number' : 'text'}
          class="field-input mono"
          value={fmt(leaf.value)}
          data-path={path}
          onChange={(e) => commit((e.target as HTMLInputElement).value)}
        />
      )}
      {isStaged ? <span class="field-badge">staged</span> : null}
    </label>
  )
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
