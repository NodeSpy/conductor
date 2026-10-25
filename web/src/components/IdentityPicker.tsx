// The bot identity picker (design §4.4.2): shape → eyes → colour, each option
// drawn with your other two choices, with a live preview at its sizes. Used in
// New bot and in a bot's Settings; the brand kit's names are conductor's own
// (the ones its loader accepts), so a picked face both renders and validates.
import type { JSX } from 'preact'
import { SHAPES, EYES, COLORS, ICONS, type AvatarSpec } from '../lib/avatar'
import { AvatarPreview } from './Avatar'

export interface IdentityChoice {
  mode: 'face' | 'icon'
  shape: string
  eyes: string
  icon: string
  color: string
}

export function emptyChoice(): IdentityChoice {
  return { mode: 'face', shape: 'circle', eyes: 'round', icon: 'radar', color: 'teal' }
}

// toAvatarSpec is the config value a choice stages (bots.<b>.avatar).
export function toAvatarSpec(c: IdentityChoice): AvatarSpec {
  return c.mode === 'icon' ? { icon: c.icon, color: c.color } : { shape: c.shape, eyes: c.eyes, color: c.color }
}

export function IdentityPicker({
  value,
  onChange,
}: {
  value: IdentityChoice
  onChange: (c: IdentityChoice) => void
}): JSX.Element {
  const set = (patch: Partial<IdentityChoice>) => onChange({ ...value, ...patch })
  const spec = toAvatarSpec(value)

  return (
    <div class="identity-picker" data-testid="identity-picker">
      <div class="idp-preview">
        <AvatarPreview spec={spec} size={72} />
        <div class="idp-preview-row">
          <AvatarPreview spec={spec} size={40} />
          <AvatarPreview spec={spec} size={28} />
          <AvatarPreview spec={spec} size={20} />
        </div>
      </div>

      <div class="idp-tabs" role="tablist">
        <button class={'idp-tab' + (value.mode === 'face' ? ' on' : '')} type="button" role="tab" aria-selected={value.mode === 'face'} onClick={() => set({ mode: 'face' })}>
          A face
        </button>
        <button class={'idp-tab' + (value.mode === 'icon' ? ' on' : '')} type="button" role="tab" aria-selected={value.mode === 'icon'} onClick={() => set({ mode: 'icon' })}>
          An icon
        </button>
      </div>

      {value.mode === 'face' ? (
        <>
          <Field label="Shape">
            <div class="idp-grid" data-testid="idp-shapes">
              {SHAPES.map((s) => (
                <button class={'idp-opt' + (value.shape === s ? ' on' : '')} type="button" title={s} aria-pressed={value.shape === s} onClick={() => set({ shape: s })}>
                  <AvatarPreview spec={{ shape: s, eyes: value.eyes, color: value.color }} size={34} />
                </button>
              ))}
            </div>
          </Field>
          <Field label="Eyes">
            <div class="idp-grid" data-testid="idp-eyes">
              {EYES.map((e) => (
                <button class={'idp-opt' + (value.eyes === e ? ' on' : '')} type="button" title={e} aria-pressed={value.eyes === e} onClick={() => set({ eyes: e })}>
                  <AvatarPreview spec={{ shape: value.shape, eyes: e, color: value.color }} size={34} />
                </button>
              ))}
            </div>
          </Field>
        </>
      ) : (
        <Field label="Icon">
          <div class="idp-grid" data-testid="idp-icons">
            {ICONS.map((i) => (
              <button class={'idp-opt' + (value.icon === i ? ' on' : '')} type="button" title={i} aria-pressed={value.icon === i} onClick={() => set({ icon: i })}>
                <AvatarPreview spec={{ icon: i, color: value.color }} size={34} />
              </button>
            ))}
          </div>
        </Field>
      )}

      <Field label="Colour">
        <div class="idp-colors" data-testid="idp-colors">
          {COLORS.map((c) => (
            <button
              class={'idp-color' + (value.color === c ? ' on' : '')}
              type="button"
              title={c}
              data-color={c}
              aria-pressed={value.color === c}
              onClick={() => set({ color: c })}
            >
              <AvatarPreview spec={value.mode === 'icon' ? { icon: value.icon, color: c } : { shape: value.shape, eyes: value.eyes, color: c }} size={28} />
              <span class="idp-color-name">{c}</span>
            </button>
          ))}
        </div>
      </Field>
    </div>
  )
}

function Field({ label, children }: { label: string; children: JSX.Element | JSX.Element[] }): JSX.Element {
  return (
    <div class="idp-field">
      <span class="idp-label">{label}</span>
      {children}
    </div>
  )
}
