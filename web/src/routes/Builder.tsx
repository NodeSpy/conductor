// The workflow builder (design §4.4): a canvas that shows the steps as a graph,
// beside an editable YAML pane they stay in sync with. You compose a workflow
// draft here; it is the config-edit pipeline (Config screen) that applies it to
// the live config, so this page never writes on its own — it builds the YAML and
// hands it off. The canvas is derived live from the YAML (a light structural
// parse — the daemon is the real validator, via the dry-run Test).
import type { JSX } from 'preact'
import { useMemo, useRef, useState } from 'preact/hooks'
import { Graph } from '../components/Graph'
import type { GraphNode } from '../api/types'

const TEMPLATE = `name: my-workflow
on: manual
steps:
  - id: review
    agent: reviewer
    prompt: Review the change and summarise risks.
  - id: comment
    uses: github.comment
    options:
      body: "{{ .review.summary }}"
`

// A step template per kind, appended when you click a palette chip.
const PALETTE: { kind: string; label: string; snippet: (id: string) => string }[] = [
  { kind: 'agent', label: 'Agent', snippet: (id) => `  - id: ${id}\n    agent: worker\n    prompt: Do the work.\n` },
  { kind: 'verb', label: 'Verb', snippet: (id) => `  - id: ${id}\n    uses: github.comment\n    options:\n      body: "..."\n` },
  { kind: 'code', label: 'Code', snippet: (id) => `  - id: ${id}\n    run: js\n    code: "return { ok: true }"\n` },
  { kind: 'decide', label: 'Decide', snippet: (id) => `  - id: ${id}\n    decide:\n      prompt: Is this a test failure?\n` },
  { kind: 'handoff', label: 'Hand-off', snippet: (id) => `  - id: ${id}\n    agent: author\n    handoff: slack\n    prompt: Draft a reply for review.\n` },
]

// parseSteps does a light, indentation-aware scan of a workflow YAML's `steps:`
// list — enough to render the canvas. It is not a YAML parser (the daemon
// validates on Test); it recognises list items and the one form key each
// carries, matching config.Step.Form's precedence.
function parseSteps(yaml: string): GraphNode[] {
  const lines = yaml.split('\n')
  let inSteps = false
  let stepsIndent = -1
  const items: { startLine: number; lines: string[] }[] = []
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i]
    const indent = raw.search(/\S/)
    if (indent < 0) continue
    if (!inSteps) {
      if (/^\s*steps\s*:/.test(raw)) {
        inSteps = true
        stepsIndent = indent
      }
      continue
    }
    // Left the steps block (a sibling/parent key at <= steps indent).
    if (indent <= stepsIndent && !/^\s*-/.test(raw)) break
    const itemIndent = raw.indexOf('-')
    if (/^\s*-/.test(raw) && itemIndent > stepsIndent) {
      items.push({ startLine: i + 1, lines: [raw] })
    } else if (items.length) {
      items[items.length - 1].lines.push(raw)
    }
  }

  const nodes: GraphNode[] = []
  let prev = ''
  items.forEach((it, idx) => {
    const text = it.lines.join('\n')
    const idM = text.match(/\bid\s*:\s*([\w.-]+)/)
    const id = idM ? idM[1] : `step${idx + 1}`
    const kind = kindOf(text)
    const label = labelOf(text, kind)
    nodes.push({ id, kind, label, needs: prev ? [prev] : [], line: it.startLine })
    prev = id
  })
  return nodes
}

function kindOf(text: string): string {
  if (/\bhandoff\s*:/.test(text)) return 'handoff'
  if (/\buses\s*:/.test(text)) return 'verb'
  if (/\bdecide\s*:/.test(text)) return 'decide'
  if (/\b(call|workflow)\s*:/.test(text)) return 'call'
  if (/\b(use|run)\s*:/.test(text)) return 'code'
  if (/\bagent\s*:/.test(text) || /\bprompt\s*:/.test(text)) return 'agent'
  if (/\bcommand\s*:/.test(text)) return 'command'
  if (/\bparallel\s*:/.test(text)) return 'parallel'
  return 'step'
}

function labelOf(text: string, kind: string): string {
  const keys: Record<string, RegExp> = {
    agent: /\bagent\s*:\s*([\w.-]+)/,
    verb: /\buses\s*:\s*([\w.-]+)/,
    handoff: /\bhandoff\s*:\s*([\w.-]+)/,
    call: /\b(?:call|workflow)\s*:\s*([\w.-]+)/,
    code: /\b(?:use|run)\s*:\s*([\w.-]+)/,
  }
  const re = keys[kind]
  const m = re && text.match(re)
  return m ? m[1] : ''
}

export function Builder(): JSX.Element {
  const [yaml, setYaml] = useState(TEMPLATE)
  const [sel, setSel] = useState('')
  const taRef = useRef<HTMLTextAreaElement>(null)
  const nodes = useMemo(() => parseSteps(yaml), [yaml])

  const addStep = (p: (typeof PALETTE)[number]) => {
    const id = `${p.kind}-${nodes.length + 1}`
    const next = yaml.replace(/\n*$/, '\n') + p.snippet(id)
    setYaml(next)
    setSel(id)
  }

  const pick = (n: GraphNode) => {
    setSel(n.id)
    const ta = taRef.current
    const line = n.line ?? 0
    if (ta && line > 0) {
      const pos = yaml.split('\n').slice(0, line - 1).join('\n').length + 1
      ta.focus()
      ta.setSelectionRange(pos, pos)
    }
  }

  return (
    <div class="col" data-builder>
      <nav class="crumbs" aria-label="Breadcrumb">
        <a href="#/workflows">Workflows</a>
        <span class="crumbs-sep">/</span>
        <span>New workflow</span>
      </nav>
      <h1 class="screen-title serif">Build a workflow</h1>
      <p class="muted">
        Compose the steps on the canvas and in the YAML — they stay in sync. When it reads right, copy the YAML into your
        config through the Config screen's review-and-apply, then Test it from its workflow page.
      </p>

      <div class="palette">
        <span class="palette-label">Add step:</span>
        {PALETTE.map((p) => (
          <button type="button" class="chip" onClick={() => addStep(p)}>+ {p.label}</button>
        ))}
      </div>

      <div class="builder-cols">
        <section class="panel builder-canvas" aria-label="Canvas">
          <div class="panel-head"><span class="panel-title serif">Canvas</span><span class="panel-sub">{nodes.length} steps</span></div>
          <Graph nodes={nodes} selected={sel} onPick={pick} />
        </section>
        <section class="panel builder-yaml" aria-label="YAML">
          <div class="panel-head"><span class="panel-title serif">YAML</span></div>
          <div class="editor">
            <pre class="editor-gutter" aria-hidden="true">
              {yaml.split('\n').map((_, i) => <span>{i + 1 + '\n'}</span>)}
            </pre>
            <textarea
              ref={taRef}
              class="editor-area code"
              spellcheck={false}
              value={yaml}
              onInput={(e) => setYaml((e.target as HTMLTextAreaElement).value)}
              aria-label="Workflow YAML"
            />
          </div>
        </section>
      </div>
    </div>
  )
}
