// A bot's face, drawn from conductor's own avatar vocabulary (design §4.4.2).
//
// conductor's loader (internal/config/botidentity.go) is the source of truth for
// which names validate, so this renderer is keyed to THOSE names — not the brand
// mascot kit, whose names the loader would refuse. A face is a solid silhouette
// in the bot's colour with a pair of eyes (or a visor) in the theme's ink; an
// icon is a glyph on a colour tile; otherwise the initials stand in.
//
// The dashboard's CSP sets `require-trusted-types-for 'script'`, which forbids
// innerHTML — so every mark is built as a real Preact VNode (h(...)), never an
// HTML string. One body + one eye set + one colour, mix and match.
import { h } from 'preact'
import type { JSX } from 'preact'

type El = JSX.Element

export const SHAPES = ['circle', 'squircle', 'hexagon', 'shield', 'blob', 'diamond', 'drop', 'star'] as const
export const EYES = ['round', 'wide', 'sleepy', 'wink', 'visor', 'dots', 'happy', 'focused'] as const
export const COLORS = ['indigo', 'violet', 'plum', 'magenta', 'teal', 'ocean', 'slate', 'sand', 'graphite'] as const
export const ICONS = [
  'radar', 'shield', 'wrench', 'book', 'bolt', 'eye', 'gear', 'pulse',
  'cloud', 'database', 'terminal', 'chart', 'bell', 'key', 'globe',
] as const

export type Shape = (typeof SHAPES)[number]
export type Eyes = (typeof EYES)[number]
export type Color = (typeof COLORS)[number]
export type Icon = (typeof ICONS)[number]

export interface AvatarSpec {
  shape?: string
  eyes?: string
  icon?: string
  color?: string
  image?: string
}

// Identity colours as a mid-tone hex that carries on both themes (the fill is a
// solid brand colour, not a token, so a face reads the same light or dark).
const HEX: Record<string, string> = {
  indigo: '#5b6ee0',
  violet: '#957ee5',
  plum: '#9b5fb0',
  magenta: '#d861aa',
  teal: '#09a596',
  ocean: '#2d8fb8',
  slate: '#677b94',
  sand: '#a98f5e',
  graphite: '#5b6472',
}

export function colorHex(c?: string): string {
  return (c && HEX[c]) || HEX.slate
}

const INK = 'currentColor'
// Small VNode helpers for SVG primitives.
const path = (d: string, extra: JSX.SVGAttributes = {}): El => h('path', { d, ...extra })
const circle = (cx: number, cy: number, r: number, extra: JSX.SVGAttributes = {}): El => h('circle', { cx, cy, r, ...extra })
const dot = (cx: number, cy: number, r: number): El => circle(cx, cy, r, { fill: INK })
const strokeAttrs = (w = 2.2): JSX.SVGAttributes => ({ fill: 'none', stroke: INK, 'stroke-width': w, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' })

// --- bodies, on a 48 grid ---
function bodyNode(shape: string, fill: string): El {
  switch (shape) {
    case 'squircle':
      return h('rect', { x: 6, y: 6, width: 36, height: 36, rx: 13, fill })
    case 'hexagon':
      return path('M24 5 41.5 15v18L24 43 6.5 33V15Z', { fill })
    case 'shield':
      return path('M10 9.5H38V24c0 9-6.5 14.5-14 17.5C16.5 38.5 10 33 10 24Z', { fill })
    case 'blob':
      return path('M9 21C8 11 17 6.5 25 9.5 29.5 11 32 10 36 10.5c7 1.5 9 11.5 6 19.5-3.5 10-15 13-24 10.5C11 38.5 9.6 30 9 21Z', { fill })
    case 'diamond':
      return path('M24 5 43 24 24 43 5 24Z', { fill })
    case 'drop':
      return path('M24 5c8 9 14 15 14 22a14 14 0 0 1-28 0c0-7 6-13 14-22Z', { fill })
    case 'star':
      return path('M24 5 29 18l14 1-10.5 9 3.5 14L24 38 12 42l3.5-14L5 19l14-1Z', { fill })
    case 'circle':
    default:
      return circle(24, 24, 19, { fill })
  }
}

// --- eye sets, in the theme ink ---
function eyeNodes(eyes: string, cy = 24): El[] {
  switch (eyes) {
    case 'wide':
      return [dot(14, cy, 2.8), dot(34, cy, 2.8)]
    case 'sleepy':
      return [
        path(`M14.5 ${cy - 1}a3.3 3.3 0 0 0 6.6 0`, strokeAttrs(2.2)),
        path(`M27 ${cy - 1}a3.3 3.3 0 0 0 6.6 0`, strokeAttrs(2.2)),
      ]
    case 'wink':
      return [dot(18, cy, 3), path(`M28.5 ${cy}h6`, strokeAttrs(2.4))]
    case 'visor':
      return [h('rect', { x: 13, y: cy - 3, width: 22, height: 6, rx: 3, fill: INK })]
    case 'dots':
      return [dot(18, cy, 2.3), dot(30, cy, 2.3)]
    case 'happy':
      return [
        path(`M14 ${cy + 1}q4-5.5 8 0`, strokeAttrs(2.4)),
        path(`M26 ${cy + 1}q4-5.5 8 0`, strokeAttrs(2.4)),
      ]
    case 'focused':
      return [
        circle(18, cy, 3.6, strokeAttrs(1.9)), dot(18, cy, 1.7),
        circle(30, cy, 3.6, strokeAttrs(1.9)), dot(30, cy, 1.7),
      ]
    case 'round':
    default:
      return [dot(18, cy, 3.1), dot(30, cy, 3.1)]
  }
}

// --- icon glyphs ---
function iconNodes(icon: string): El[] {
  const s = strokeAttrs(2.2)
  switch (icon) {
    case 'radar':
      return [circle(24, 24, 12, s), path('M24 24 32 16', s), circle(24, 24, 2, { fill: INK })]
    case 'shield':
      return [path('M24 12l10 4v8c0 7-5 10-10 12-5-2-10-5-10-12v-8Z', s)]
    case 'wrench':
      return [path('M30 14a6 6 0 0 0-8 7l-8 8 3 3 8-8a6 6 0 0 0 7-8l-4 4-2-2Z', s)]
    case 'book':
      return [path('M14 14h9v20h-9Z', s), path('M25 14h9v20h-9Z', s)]
    case 'bolt':
      return [path('M26 12 16 26h7l-1 10 10-14h-7Z', s)]
    case 'eye':
      return [path('M12 24s5-8 12-8 12 8 12 8-5 8-12 8-12-8-12-8Z', s), circle(24, 24, 3, { fill: INK })]
    case 'gear':
      return [circle(24, 24, 5, s), path('M24 13v4M24 31v4M13 24h4M31 24h4M16 16l3 3M29 29l3 3M32 16l-3 3M19 29l-3 3', s)]
    case 'pulse':
      return [path('M12 24h6l3-8 5 16 3-8h7', s)]
    case 'cloud':
      return [path('M16 30a6 6 0 0 1 1-12 8 8 0 0 1 15 2 5 5 0 0 1-1 10Z', s)]
    case 'database':
      return [h('ellipse', { cx: 24, cy: 15, rx: 10, ry: 4, ...s }), path('M14 15v18c0 2 5 4 10 4s10-2 10-4V15', s)]
    case 'terminal':
      return [path('M14 16h20v16H14Z', s), path('M18 22l4 3-4 3M26 28h5', s)]
    case 'chart':
      return [path('M14 34V14', s), path('M14 34h20', s), path('M20 30v-8M26 30v-12M32 30v-6', s)]
    case 'bell':
      return [path('M18 30a6 6 0 0 1 12 0ZM24 14v2M20 30v2a4 4 0 0 0 8 0v-2', s)]
    case 'key':
      return [circle(18, 22, 5, s), path('M22 25l10 10M30 33l2-2M27 30l2-2', s)]
    case 'globe':
      return [circle(24, 24, 12, s), path('M12 24h24M24 12c5 5 5 19 0 24M24 12c-5 5-5 19 0 24', s)]
    default:
      return [circle(24, 24, 4, { fill: INK })]
  }
}

// avatarVNode renders a bot's face (or an icon tile) as a Preact <svg> VNode.
// The eyes/glyph use currentColor; the caller sets the ink via `color` on a
// wrapper (so it follows the theme).
export function avatarVNode(spec: AvatarSpec | undefined, size: number): El {
  const fill = colorHex(spec?.color)
  const common = { xmlns: 'http://www.w3.org/2000/svg', viewBox: '0 0 48 48', width: size, height: size }
  if (spec?.icon) {
    return h('svg', common, [h('rect', { x: 3, y: 3, width: 42, height: 42, rx: 11, fill }), ...iconNodes(spec.icon)])
  }
  const shape = spec?.shape || 'circle'
  const eyes = spec?.eyes || 'round'
  const cy = shape === 'drop' || shape === 'star' ? 26 : 24
  return h('svg', common, [bodyNode(shape, fill), ...eyeNodes(eyes, cy)])
}
