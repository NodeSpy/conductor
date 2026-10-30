// The value-source field (design §3.2 "The value-source field"): one field for
// every configurable value, with a quiet source menu inside it rather than a
// segmented control in front. A sensitive field is write-only — a typed value is
// stored in conductor's vault and the field holds a reference; it only ever shows
// the reference, never the value. Browser credentials and card/profile details
// offer only Typed and Vault (design §4.5.1).
//
// It is a controlled component: `value` is { source, value?, ref? } and onChange
// emits the next one. The parent turns it into a stored value (typed) or a
// reference (vault/env/setting) when it saves.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'

export type VSource = 'typed' | 'vault' | 'env' | 'setting'

export interface VSValue {
  source: VSource
  value?: string // a typed value (write-only for sensitive fields)
  ref?: string // a reference ({{ vault … }}, ${env.X}, ${settings.x})
  set?: boolean // true when a value is already stored (shows "set")
}

const NAMES: Record<VSource, string> = {
  typed: 'Typed value',
  vault: 'Vault item',
  env: 'Environment variable',
  setting: 'Setting',
}

// sourcesFor returns which sources a field offers. Browser credentials and
// card/profile details (credential: true) offer only Typed and Vault; a plain
// sensitive field adds Env; only a non-sensitive field offers a Setting.
function sourcesFor(sensitive: boolean, credential: boolean): VSource[] {
  if (credential) return ['typed', 'vault']
  if (sensitive) return ['typed', 'vault', 'env']
  return ['vault', 'env', 'typed', 'setting']
}

export function ValueSource({
  value,
  onChange,
  sensitive = false,
  credential = false,
  label,
  placeholder,
  testid,
}: {
  value: VSValue
  onChange: (v: VSValue) => void
  sensitive?: boolean
  credential?: boolean
  label?: string
  placeholder?: string
  testid?: string
}): JSX.Element {
  const [menu, setMenu] = useState(false)
  const sources = sourcesFor(sensitive, credential)
  const src = value.source

  function pick(s: VSource) {
    setMenu(false)
    onChange({ ...value, source: s })
  }

  return (
    <div class="vs" data-vs={testid} data-src={src}>
      {label ? <span class="vs-label">{label}</span> : null}
      <div class="vs-row">
        <details class="vs-menu" open={menu}>
          <summary
            class="vs-src"
            onClick={(e) => {
              e.preventDefault()
              setMenu(!menu)
            }}
          >
            {NAMES[src]} <span class="chev">▾</span>
          </summary>
          <ul class="vs-src-list" role="menu">
            {sources
              .filter((s) => !isOther(s, sensitive))
              .map((s) => (
                <li>
                  <button type="button" role="menuitem" onClick={() => pick(s)}>
                    {NAMES[s]}
                  </button>
                </li>
              ))}
            {sources.some((s) => isOther(s, sensitive)) ? <li class="vs-more">Other sources</li> : null}
            {sources
              .filter((s) => isOther(s, sensitive))
              .map((s) => (
                <li>
                  <button type="button" role="menuitem" onClick={() => pick(s)}>
                    {NAMES[s]}
                  </button>
                </li>
              ))}
          </ul>
        </details>
        <div class="vs-body">
          {src === 'typed' ? (
            <input
              class="vs-input"
              type={sensitive ? 'password' : 'text'}
              value={sensitive ? '' : value.value || ''}
              placeholder={
                sensitive
                  ? value.set
                    ? 'set · type to replace'
                    : placeholder || 'stored in the vault when you save; the field keeps the reference'
                  : placeholder || 'a value'
              }
              data-testid={testid ? testid + '-input' : undefined}
              onInput={(e) => onChange({ ...value, value: (e.target as HTMLInputElement).value })}
            />
          ) : (
            <input
              class="vs-input mono"
              type="text"
              value={value.ref || ''}
              placeholder={refPlaceholder(src)}
              data-testid={testid ? testid + '-ref' : undefined}
              onInput={(e) => onChange({ ...value, ref: (e.target as HTMLInputElement).value })}
            />
          )}
        </div>
      </div>
      {src === 'vault' ? (
        <p class="vs-check muted">The one reference syntax: {'{{ vault "<vault>" "<key>" }}'} — a value is never shown here.</p>
      ) : null}
      {src === 'setting' && sensitive ? (
        <p class="vs-msg warn">Blocked: a setting is plain config, in every diff and backup. A sensitive value has to come from a vault or an env var.</p>
      ) : null}
    </div>
  )
}

function isOther(s: VSource, sensitive: boolean): boolean {
  if (sensitive) return s === 'setting'
  return s === 'typed' || s === 'setting'
}

function refPlaceholder(s: VSource): string {
  switch (s) {
    case 'vault':
      return '{{ vault "op" "Me/Home/email" }}'
    case 'env':
      return '${env.HOME_PHONE}'
    case 'setting':
      return '${settings.name}'
    default:
      return ''
  }
}

// vsToWire turns a value-source field into the wire value: a typed value goes in
// `values`, any reference in `refs`.
export function vsToWire(v: VSValue): { value?: string; ref?: string } {
  if (v.source === 'typed') return v.value ? { value: v.value } : {}
  return v.ref ? { ref: v.ref } : {}
}

export function emptyVS(): VSValue {
  return { source: 'typed' }
}
