// A plain, highlighted code editor (prototype js/ui.js UI.editor, css/interact
// `.ced`): a transparent textarea over a syntax-highlighted <pre>, line
// numbers, and optional error/highlight line marking. NOT CodeMirror — the
// prototype's own builder config-file pane isn't either (CodeMirror 6 there is
// only for the markdown body editor), so this is a faithful, much smaller
// port: line-based regex highlighting is "good enough" (the prototype's words)
// for a YAML pane that is reviewed through the config diff before it ever
// applies.
//
// The highlighted <pre> is built from real Preact nodes, never raw HTML
// (the dashboard's CSP sets require-trusted-types-for 'script': see Icon.tsx).
import type { JSX } from 'preact'
import { useRef } from 'preact/hooks'

interface Seg {
  text: string
  cls?: string
}

// yamlSegs splits one line into highlighted segments: a trailing comment,
// then key: value (the key and a quoted/boolean/numeric/anchor value each
// get their own class; everything else is plain).
function yamlSegs(raw: string): Seg[] {
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
  const valSegs = (v: string): Seg[] => {
    const t = v.trim()
    if (!t) return [{ text: v }]
    const lead = v.slice(0, v.indexOf(t))
    const tail = v.slice(v.indexOf(t) + t.length)
    let cls: string | undefined
    if (/^["'].*["']$/.test(t)) cls = 'y-s'
    else if (/^(true|false|on|off|null|yes|no)$/.test(t)) cls = 'y-b'
    else if (/^-?\d[\d._]*[a-z%]*$/i.test(t)) cls = 'y-n'
    else if (/^[&*][\w-]+/.test(t)) cls = 'y-a'
    const segs: Seg[] = []
    if (lead) segs.push({ text: lead })
    segs.push({ text: t, cls })
    if (tail) segs.push({ text: tail })
    return segs
  }
  const out: Seg[] = []
  const m = body.match(/^(\s*(?:-\s+)?)((?!-\s)[^\s:#'"{}[\]][^:#{}]*?|"[^"]*")(:)(\s+|$)(.*)$/)
  if (m) {
    if (m[1]) out.push({ text: m[1] })
    out.push({ text: m[2], cls: 'y-k' }, { text: ':', cls: 'y-p' })
    if (m[4]) out.push({ text: m[4] })
    out.push(...valSegs(m[5]))
  } else {
    const d = body.match(/^(\s*-\s+)(.*)$/)
    if (d) {
      out.push({ text: d[1], cls: 'y-p' }, ...valSegs(d[2]))
    } else {
      out.push(...valSegs(body))
    }
  }
  if (comment) out.push({ text: comment, cls: 'y-c' })
  return out
}

function Line({ raw, n, hl, err }: { raw: string; n: number; hl: boolean; err: boolean }): JSX.Element {
  const segs = yamlSegs(raw)
  return (
    <span class={'ln' + (hl ? ' hl' : '') + (err ? ' err' : '')} data-ln={n}>
      {segs.length ? segs.map((s) => (s.cls ? <span class={s.cls}>{s.text}</span> : <>{s.text}</>)) : ' '}
      {'\n'}
    </span>
  )
}

// CodeEditor is a controlled YAML text editor: onChange fires on every
// keystroke (the caller debounces if it stages on every change).
export function CodeEditor({
  id,
  text,
  onChange,
  readOnly,
  minRows = 10,
  label = 'YAML',
  errLines = [],
  hlLine,
}: {
  id: string
  text: string
  onChange?: (text: string) => void
  readOnly?: boolean
  minRows?: number
  label?: string
  errLines?: number[]
  hlLine?: number
}): JSX.Element {
  const preRef = useRef<HTMLPreElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const lines = text.split('\n')
  return (
    <div class="ced" data-lang="yaml" id={id} data-readonly={readOnly ? '1' : undefined}>
      <pre class="code ced-hl" aria-hidden="true" ref={preRef}>
        {lines.map((l, i) => (
          <Line raw={l} n={i + 1} hl={hlLine === i + 1} err={errLines.includes(i + 1)} />
        ))}
      </pre>
      <textarea
        ref={taRef}
        class="ced-ta"
        spellcheck={false}
        autocapitalize="off"
        autocomplete="off"
        aria-label={label}
        readOnly={readOnly}
        style={{ minHeight: `${minRows * 19.5 + 16}px` }}
        value={text}
        onInput={(e) => onChange?.(e.currentTarget.value)}
        onScroll={() => {
          if (preRef.current && taRef.current) {
            preRef.current.scrollTop = taRef.current.scrollTop
            preRef.current.scrollLeft = taRef.current.scrollLeft
          }
        }}
        onKeyDown={(e) => {
          if (e.key === 'Tab' && !e.shiftKey && !readOnly) {
            e.preventDefault()
            const ta = e.currentTarget
            const a = ta.selectionStart
            const b = ta.selectionEnd
            ta.setRangeText('  ', a, b, 'end')
            onChange?.(ta.value)
          }
        }}
      />
    </div>
  )
}
