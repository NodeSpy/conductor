// A bot's or a team's face, wherever it appears (rows, headings, chat, the
// conversation list): the mascot in its colour, an icon tile, an uploaded
// picture, or the initials when it has none (design §4.4.2). One component so a
// face looks the same everywhere and a staged change shows at every size at once.
import type { JSX } from 'preact'
import { avatarVNode, colorHex, type AvatarSpec } from '../lib/avatar'

export interface Identity {
  name?: string
  initials?: string
  display_name?: string
  color?: string
  avatar?: AvatarSpec
  picture?: string // "/v1/bots/<b>/avatar" — served under /api
}

// AvatarPreview renders a spec directly (the identity picker's live preview),
// independent of any saved identity.
export function AvatarPreview({ spec, color, size = 44 }: { spec: AvatarSpec; color?: string; size?: number }): JSX.Element {
  const s = color ? { ...spec, color } : spec
  return (
    <span class="avatar" style={{ width: size, height: size, color: 'var(--color-text-primary)' }}>
      {avatarVNode(s, size)}
    </span>
  )
}

export function Avatar({ id, size = 40, kind = 'bot' }: { id?: Identity; size?: number; kind?: 'bot' | 'team' }): JSX.Element {
  const label = id?.display_name || id?.name || '?'
  const spec = id?.avatar
  const picture = id?.picture

  if (picture || spec?.image) {
    const src = '/api' + (picture || '')
    return <img class="avatar img" src={src} alt={label} width={size} height={size} />
  }
  if (spec && (spec.shape || spec.icon)) {
    const withColor: AvatarSpec = spec.color ? spec : { ...spec, color: id?.color }
    return (
      <span class="avatar" title={label} style={{ width: size, height: size, color: 'var(--color-text-primary)' }}>
        {avatarVNode(withColor, size)}
      </span>
    )
  }
  // No face: initials on a quiet tile, tinted by the identity colour when set.
  const bg = id?.color ? colorHex(id.color) : 'var(--color-bg-raised)'
  const fg = id?.color ? '#fff' : 'var(--color-text-secondary)'
  return (
    <span
      class={'avatar initials' + (kind === 'team' ? ' team' : '')}
      title={label}
      style={{ width: size, height: size, background: bg, color: fg, fontSize: Math.round(size * 0.4) }}
    >
      {id?.initials || label.slice(0, 1).toUpperCase()}
    </span>
  )
}

// StackedTeam shows a team's own picture, or its members' faces stacked (the
// conversation list, design §4.4.2).
export function StackedTeam({ team, members, size = 40 }: { team?: Identity; members?: Identity[]; size?: number }): JSX.Element {
  if (team && (team.picture || team.avatar)) return <Avatar id={team} size={size} kind="team" />
  const faces = (members || []).slice(0, 3)
  if (faces.length === 0) return <Avatar id={team} size={size} kind="team" />
  return (
    <span class="avatar-stack" style={{ width: size + (faces.length - 1) * (size * 0.4) }}>
      {faces.map((m, i) => (
        <span class="stack-face" style={{ left: i * (size * 0.4), zIndex: faces.length - i }}>
          <Avatar id={m} size={Math.round(size * 0.78)} />
        </span>
      ))}
    </span>
  )
}
