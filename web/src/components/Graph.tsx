// The workflow graph: one node per step, laid out top-to-bottom with an edge to
// each node it needs (design §4.4). Clicking a node reveals its line in the YAML
// pane (node→YAML) via onPick. A read-only SVG; no layout library.
import type { JSX } from 'preact'
import type { GraphNode } from '../api/types'

const NODE_W = 220
const NODE_H = 52
const GAP_Y = 34
const PAD = 16

// kindGlyph gives each node kind a one-letter badge (agent → A, verb → V, …).
function kindBadge(kind: string): string {
  const m: Record<string, string> = {
    agent: 'A',
    command: 'C',
    code: '{}',
    verb: 'V',
    decide: '?',
    handoff: 'H',
    call: '↳',
    team: 'T',
    parallel: '⫼',
  }
  return m[kind] || '•'
}

export function Graph({
  nodes,
  selected,
  onPick,
}: {
  nodes: GraphNode[]
  selected?: string
  onPick?: (n: GraphNode) => void
}): JSX.Element {
  if (!nodes.length) return <p class="muted">This workflow has no steps.</p>
  const index = new Map(nodes.map((n, i) => [n.id, i]))
  const y = (i: number) => PAD + i * (NODE_H + GAP_Y)
  const cx = PAD + NODE_W / 2
  const height = PAD * 2 + nodes.length * NODE_H + (nodes.length - 1) * GAP_Y
  const width = NODE_W + PAD * 2

  return (
    <div class="graph-wrap">
      <svg class="graph" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${nodes.length} steps`}>
        {/* edges */}
        {nodes.map((n, i) =>
          (n.needs || []).map((dep) => {
            const di = index.get(dep)
            if (di == null) return null
            const y1 = y(di) + NODE_H
            const y2 = y(i)
            return <path class="graph-edge" d={`M ${cx} ${y1} C ${cx} ${y1 + GAP_Y / 2}, ${cx} ${y2 - GAP_Y / 2}, ${cx} ${y2}`} />
          }),
        )}
        {/* nodes */}
        {nodes.map((n, i) => {
          const on = n.id === selected
          return (
            <g
              class={'graph-node' + (on ? ' on' : '') + (onPick ? ' pick' : '')}
              transform={`translate(${PAD} ${y(i)})`}
              tabIndex={onPick ? 0 : undefined}
              role={onPick ? 'button' : undefined}
              onClick={() => onPick?.(n)}
              onKeyDown={(e: KeyboardEvent) => {
                if (onPick && (e.key === 'Enter' || e.key === ' ')) {
                  e.preventDefault()
                  onPick(n)
                }
              }}
            >
              <rect class="graph-box" width={NODE_W} height={NODE_H} rx="10" />
              <text class="graph-badge" x="18" y={NODE_H / 2 + 5}>{kindBadge(n.kind)}</text>
              <text class="graph-id" x="40" y={NODE_H / 2 - 3}>{n.id}</text>
              <text class="graph-kind" x="40" y={NODE_H / 2 + 14}>{n.label || n.kind}</text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}
