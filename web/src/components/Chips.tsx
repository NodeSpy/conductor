// Prose that may carry a template expression ({{.step.output}} == 0) or a
// config key: those parts render as inline <code> chips, the rest stays text
// (owner rule: inline code in prose is a <code> chip).
import type { JSX } from 'preact'

// a {{…}} expression with any comparisons chained after it
const EXPR = String.raw`\{\{[^}]*\}\}(?:\s*(?:==|!=|>=|<=|>|<|&&|\|\|)\s*(?:"[^"]*"|'[^']*'|\{\{[^}]*\}\}|[\w-]+(?:\.[\w-]+)*))*`

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** Chips every {{…}} expression in text, plus each of `keys` (config paths) where it appears whole. */
export function Chipped({ text, keys = [] }: { text: string; keys?: string[] }): JSX.Element {
  const ks = keys.filter(Boolean).sort((a, b) => b.length - a.length).map(esc)
  const re = new RegExp([EXPR, ...ks.map((k) => String.raw`(?<![\w.])${k}(?![\w-]|\.\w)`)].join('|'), 'g')
  const out: (string | JSX.Element)[] = []
  let at = 0
  for (const m of text.matchAll(re)) {
    if (m.index! > at) out.push(text.slice(at, m.index))
    out.push(<code>{m[0]}</code>)
    at = m.index! + m[0].length
  }
  if (at < text.length) out.push(text.slice(at))
  return <>{out}</>
}
