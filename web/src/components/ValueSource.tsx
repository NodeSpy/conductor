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
import { Icon } from './Icon'

export type VSource = 'typed' | 'vault' | 'env' | 'setting'

export interface VSValue {
  source: VSource
  value?: string // a typed value (write-only for sensitive fields)
  ref?: string // a reference ({{ vault … }}, ${env.X}, ${settings.x})
  set?: boolean // true when a value is already stored (shows "set")
}

// SRCW is the prototype's wording (js/valuesource.js SRCW): a typed value in a
// sensitive field is a Secret (stored encrypted, never shown again); in a plain
// field it is a Typed value, written into the config as it is.
function SRCW(s: VSource, sensitive: boolean): [string, string, string] {
  switch (s) {
    case 'typed':
      return sensitive
        ? ['Secret', 'Type it once; conductor stores it encrypted in its vault. It never comes back to the browser.', 'key']
        : ['Typed value', 'Written into the config as it is. Not for anything secret.', 'edit']
    case 'vault':
      return ['Vault item', 'A conductor vault entry, or an item in 1Password or Bitwarden, read when needed.', 'lock']
    case 'env':
      return ['Environment variable', 'Read from conductor’s environment when the value is used.', 'terminal']
    default:
      return ['Setting', 'A shared value from Config → Settings. Plain config, so never for a secret.', 'config']
  }
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
  suggest,
}: {
  value: VSValue
  onChange: (v: VSValue) => void
  sensitive?: boolean
  credential?: boolean
  label?: string
  placeholder?: string
  testid?: string
  // suggest offers known references (a vault's entries) to pick from while
  // typing a reference.
  suggest?: string[]
}): JSX.Element {
  const [menu, setMenu] = useState(false)
  const sources = sourcesFor(sensitive, credential)
  const src = value.source

  function pick(s: VSource) {
    setMenu(false)
    onChange({ ...value, source: s })
  }

  const main = sources.filter((s) => !isOther(s, sensitive))
  const other = sources.filter((s) => isOther(s, sensitive))
  const opt = (s: VSource): JSX.Element => (
    <button type="button" role="menuitemradio" class={'vs-opt' + (s === src ? ' on' : '')} aria-checked={s === src} onClick={() => pick(s)}>
      <Icon name={SRCW(s, sensitive)[2]} />
      <b>{SRCW(s, sensitive)[0]}</b>
      <span>{SRCW(s, sensitive)[1]}</span>
    </button>
  )

  return (
    <div class="field" data-vs={testid}>
      {label ? <span class="flabel">{label}</span> : null}
      <div class="vs" data-src={src}>
        <div class="vs-row">
          <details class="vs-menu" open={menu}>
            <summary
              class="vs-srcbtn"
              onClick={(e) => {
                e.preventDefault()
                setMenu(!menu)
              }}
            >
              <Icon name={SRCW(src, sensitive)[2]} />
              <span>{SRCW(src, sensitive)[0]}</span>
              <Icon name="chevd" class="chev" />
            </summary>
            {menu ? (
              <div class="vs-pop" role="menu">
                {main.map(opt)}
                {other.length ? <div class="vs-more">Other sources</div> : null}
                {other.map(opt)}
              </div>
            ) : null}
          </details>
          <div class="vs-body">
            {src === 'typed' ? (
              <input
                class="input"
                type={sensitive ? 'password' : 'text'}
                value={sensitive ? '' : value.value || ''}
                placeholder={
                  sensitive
                    ? value.set
                      ? 'set · type to replace'
                      : placeholder || 'stored write-only; the field keeps the reference'
                    : placeholder || 'a value'
                }
                data-testid={testid ? testid + '-input' : undefined}
                onInput={(e) => onChange({ ...value, value: (e.target as HTMLInputElement).value })}
              />
            ) : (
              <input
                class="input mono"
                type="text"
                value={value.ref || ''}
                placeholder={refPlaceholder(src)}
                data-testid={testid ? testid + '-ref' : undefined}
                list={src === 'vault' && suggest?.length && testid ? testid + '-refs' : undefined}
                onInput={(e) => onChange({ ...value, ref: (e.target as HTMLInputElement).value })}
              />
            )}
          </div>
        </div>
        {src === 'vault' && suggest?.length && testid ? (
          <datalist id={testid + '-refs'}>
            {suggest.map((r) => <option value={r} key={r} />)}
          </datalist>
        ) : null}
        {src === 'vault' ? (
          <p class="vs-check dl-sub">The one reference syntax: <code>{'{{ vault "<vault>" "<key>" }}'}</code> — a value is never shown here.</p>
        ) : null}
        {src === 'setting' && sensitive ? (
          <p class="vs-check callout warn small">Blocked: a setting is plain config, in every diff and backup. A sensitive value has to come from a vault or an env var.</p>
        ) : null}
      </div>
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
