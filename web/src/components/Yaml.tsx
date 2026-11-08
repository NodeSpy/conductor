// Yaml renders a step's recorded Inputs/Outputs YAML the way the prototype's
// UI.yaml/UI.yamlLine do (design js/ui.js): one <span class="ln"> per line (the
// shared .code CSS numbers them with a counter), light line-based syntax
// colouring (keys, strings, numbers, booleans, anchors, comments — classes
// y-k/y-s/y-n/y-b/y-a/y-c/y-p, all from tokens.css), never a full parse. Good
// enough for a checkpointed, already-valid YAML string; not a real YAML
// highlighter.
import type { JSX } from 'preact'

const KV = /^(\s*(?:-\s+)?)((?!-\s)[^\s:#'"{}[\]][^:#{}]*?|"[^"]*")(:)(\s+|$)(.*)$/
const DASH = /^(\s*-\s+)(.*)$/

function valueNode(v: string): JSX.Element {
  const t = v.trim()
  if (!t) return <>{v}</>
  const start = v.indexOf(t)
  const lead = v.slice(0, start)
  const tail = v.slice(start + t.length)
  let cls = ''
  if (/^["'].*["']$/.test(t)) cls = 'y-s'
  else if (/^(true|false|on|off|null|yes|no)$/.test(t)) cls = 'y-b'
  else if (/^-?\d[\d._]*[a-z%]*$/i.test(t)) cls = 'y-n'
  else if (/^[&*][\w-]+/.test(t)) cls = 'y-a'
  return (
    <>
      {lead}
      {cls ? <span class={cls}>{t}</span> : t}
      {tail}
    </>
  )
}

function lineNode(raw: string): JSX.Element {
  // Split off a trailing comment (a # at line start or after whitespace,
  // outside quotes) before matching key/value shape.
  let body = raw
  let comment = ''
  let q: string | null = null
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]
    if (q) {
      if (c === q) q = null
      continue
    }
    if (c === '"' || c === "'") {
      q = c
      continue
    }
    if (c === '#' && (i === 0 || /\s/.test(raw[i - 1]))) {
      body = raw.slice(0, i)
      comment = raw.slice(i)
      break
    }
  }
  const m = body.match(KV)
  let content: JSX.Element
  if (m) {
    content = (
      <>
        {m[1]}
        <span class="y-k">{m[2]}</span>
        <span class="y-p">:</span>
        {m[4]}
        {valueNode(m[5])}
      </>
    )
  } else {
    const d = body.match(DASH)
    content = d ? (
      <>
        <span class="y-p">{d[1]}</span>
        {valueNode(d[2])}
      </>
    ) : (
      valueNode(body)
    )
  }
  return (
    <>
      {content}
      {comment ? <span class="y-c">{comment}</span> : null}
    </>
  )
}

export function Yaml({ text }: { text: string }): JSX.Element {
  const lines = text.split('\n')
  return (
    <div class="code">
      {lines.map((l, i) => (
        <span class="ln" key={i}>
          {l ? lineNode(l) : ' '}
        </span>
      ))}
    </div>
  )
}
