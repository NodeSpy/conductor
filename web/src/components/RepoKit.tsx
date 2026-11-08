// The pieces the repo browser (RepoView) is built from, ported from the
// prototype's js/screens/repo.js and js/ui.js: a document's front matter
// (parse, emit in its kind's field order, the kind's schema check worded as
// conductor words it), a line diff drawn as UI.diffView draws it, a safe
// markdown preview (UI.mdRender — real nodes, never HTML strings: the CSP
// forbids innerHTML) and the markdown editor box (UI.mdEditor: Write /
// Preview / side by side, the formatting toolbar, [[ links). The prototype's
// editor surface is CodeMirror; here it is a plain textarea in the same box.
import type { ComponentChildren, JSX } from 'preact'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { parse as yamlParse, stringify as yamlStringify } from 'yaml'
import type { RepoKind } from '../api/repo'
import { Icon } from './Icon'

// ---------- front matter ----------

export type FM = Record<string, unknown>

export interface Doc {
  fm: FM
  body: string
  has: boolean
  err?: string
}

// parseDoc splits a document as conductor does (SplitFrontmatter).
export function parseDoc(text: string): Doc {
  const t = (text || '').replace(/^﻿/, '')
  if (!t.startsWith('---\n')) return { fm: {}, body: text || '', has: false }
  const end = t.indexOf('\n---', 3)
  if (end < 0)
    return {
      fm: {},
      body: text,
      has: false,
      err: 'front matter is not closed with ---',
    }
  const raw = t.slice(4, end + 1)
  let rest = t.slice(end + 4)
  const nl = rest.indexOf('\n')
  rest = nl >= 0 ? rest.slice(nl + 1) : ''
  try {
    const v = yamlParse(raw)
    return {
      fm: v && typeof v === 'object' && !Array.isArray(v) ? (v as FM) : {},
      body: rest,
      has: true,
    }
  } catch (e) {
    return {
      fm: {},
      body: rest,
      has: true,
      err: 'front matter: ' + (e instanceof Error ? e.message.split('\n')[0] : 'not YAML'),
    }
  }
}

const emptyV = (v: unknown) => v == null || v === '' || (Array.isArray(v) && !v.length)

// emitDoc renders a document: its kind's fields in order (empty ones left
// out), then the body — conductor's JoinFrontmatter. Keys the kind doesn't
// name are kept as they were, so an edit never drops what it didn't show.
export function emitDoc(kind: RepoKind, fm: FM, body: string): string {
  const o: FM = {}
  for (const f of kind.fields) if (!emptyV(fm[f.name])) o[f.name] = fm[f.name]
  for (const [k, v] of Object.entries(fm)) if (!(k in o) && !kind.fields.some((f) => f.name === k) && !emptyV(v)) o[k] = v
  const y = Object.keys(o).length ? yamlStringify(o, { lineWidth: 0 }) : ''
  const b = (body || '').replace(/^\n+/, '')
  return '---\n' + y + '---\n' + (b.trim() ? b.replace(/\s*$/, '\n') : '')
}

// validate is the kind's schema check (internal/bot kinds.go Validate), in
// the prototype's words: "<field>: missing required field (type)", …
export function validate(kind: RepoKind, fm: FM): string[] {
  const e: string[] = []
  for (const f of kind.fields) {
    const v = fm[f.name]
    if (emptyV(v)) {
      if (f.required)
        e.push(
          `${f.name}: missing required field (${f.type === 'steps' ? 'at least one step' : f.name === 'evidence' ? 'at least one piece of evidence' : f.type})`,
        )
      continue
    }
    if (f.type === 'string' && (typeof v !== 'string' || /\n/.test(v))) e.push(`${f.name}: want one line`)
    if (f.type === 'text' && typeof v !== 'string') e.push(`${f.name}: want text`)
    if (f.type === 'enum' && !(f.values || []).includes(String(v))) e.push(`${f.name}: ${String(v)} is not one of ${(f.values || []).join(', ')}`)
    if (f.type === 'list' || f.type === 'steps') {
      if (!Array.isArray(v)) e.push(`${f.name}: want a list`)
      else
        v.forEach((x, i) => {
          if (typeof x !== 'string' || !x.trim()) e.push(`${f.name}: item ${i + 1}: want a non-empty string`)
        })
    }
    if (f.type === 'number' && typeof v !== 'number') e.push(`${f.name}: want a number`)
    if (f.type === 'bool' && typeof v !== 'boolean') e.push(`${f.name}: want true or false`)
  }
  for (const k of Object.keys(fm)) if (!kind.fields.some((f) => f.name === k)) e.push(`${k}: not a field of this kind`)
  return e
}

// slug is conductor's Slug: a title as a path-safe name.
export function slug(t: string): string {
  let s = String(t || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  if (s.length > 48) s = s.slice(0, 48).replace(/^-+|-+$/g, '')
  return s || 'note'
}

// ---------- diff ----------

interface DRow {
  t: ' ' | '+' | '-'
  s: string
  a?: number
  b?: number
}

// lineDiff is a line-level LCS diff (files here are small).
export function lineDiff(A: string, B: string): DRow[] {
  const a = A.split('\n')
  const b = B.split('\n')
  if (a[a.length - 1] === '') a.pop()
  if (b[b.length - 1] === '') b.pop()
  const n = a.length
  const m = b.length
  const L: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = a[i] === b[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1])
  const out: DRow[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ t: ' ', s: a[i], a: i + 1, b: j + 1 })
      i++
      j++
    } else if (L[i + 1][j] >= L[i][j + 1]) {
      out.push({ t: '-', s: a[i], a: i + 1 })
      i++
    } else {
      out.push({ t: '+', s: b[j], b: j + 1 })
      j++
    }
  }
  while (i < n) out.push({ t: '-', s: a[i], a: ++i })
  while (j < m) out.push({ t: '+', s: b[j], b: ++j })
  return out
}

function hunks(d: DRow[], ctx: number): DRow[][] {
  const keep = d.map(() => false)
  d.forEach((r, i) => {
    if (r.t === ' ') return
    for (let k = Math.max(0, i - ctx); k <= Math.min(d.length - 1, i + ctx); k++) keep[k] = true
  })
  const out: DRow[][] = []
  let cur: DRow[] | null = null
  d.forEach((r, i) => {
    if (keep[i]) {
      if (!cur) {
        cur = []
        out.push(cur)
      }
      cur.push(r)
    } else cur = null
  })
  return out
}

// DiffView is UI.diffView(A, B, 'unified', {file}): hunks of three lines'
// context, each headed by its file and line.
export function DiffView({ before, after, file }: { before: string; after: string; file: string }): JSX.Element {
  const d = lineDiff(before, after)
  if (!d.some((r) => r.t !== ' ')) {
    return (
      <div class="empty small">
        <span class="serif">No difference</span>The two versions are the same.
      </div>
    )
  }
  return (
    <div class="diff unified">
      {hunks(d, 3).map((h) => {
        const r = h.find((x) => x.t !== ' ') || h[0]
        return (
          <>
            <div class="h">
              {file} · line {r.t === '-' ? r.a : r.b}
            </div>
            {h.map((x) => (
              <div class={'dl' + (x.t === '+' ? ' add' : x.t === '-' ? ' del' : '')}>
                <span>{x.a || ''}</span>
                <span>{x.b || ''}</span>
                <span>{x.t}</span>
                <span>{x.s || ' '}</span>
              </div>
            ))}
          </>
        )
      })}
    </div>
  )
}

// ---------- markdown preview (UI.mdRender) ----------

function inline(t: string): ComponentChildren[] {
  const out: ComponentChildren[] = []
  const re = /(\*\*([^*]+)\*\*)|(`([^`]+)`)|(\[\[([^\]]+)\]\])|(\[([^\]]+)\]\((https?:\/\/[^)\s]+)\))|((?:^|(?<=[^\w*]))[_*]([^_*\n]+)[_*](?!\w))/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(t))) {
    if (m.index > last) out.push(t.slice(last, m.index))
    if (m[1]) out.push(<b>{m[2]}</b>)
    else if (m[3]) out.push(<code>{m[4]}</code>)
    else if (m[5]) out.push(<span class="wikilink">{m[6]}</span>)
    else if (m[7])
      out.push(
        <a href={m[9]} target="_blank" rel="noopener noreferrer">
          {m[8]}
        </a>,
      )
    else if (m[10]) out.push(<i>{m[11]}</i>)
    last = re.lastIndex
  }
  if (last < t.length) out.push(t.slice(last))
  return out
}

export function Markdown({ src }: { src: string }): JSX.Element {
  const out: JSX.Element[] = []
  let list: { t: 'ul' | 'ol'; items: string[] } | null = null
  let code: string[] | null = null
  let para: string[] = []
  const flushP = () => {
    if (para.length) out.push(<p>{inline(para.join(' '))}</p>)
    para = []
  }
  const flushL = () => {
    if (list) {
      const items = list.items.map((x) => <li>{inline(x)}</li>)
      out.push(list.t === 'ol' ? <ol>{items}</ol> : <ul>{items}</ul>)
    }
    list = null
  }
  for (const l of String(src || '').split('\n')) {
    if (code) {
      if (/^```/.test(l)) {
        out.push(
          <pre>
            <code>{code.join('\n')}</code>
          </pre>,
        )
        code = null
      } else code.push(l)
      continue
    }
    if (/^```/.test(l)) {
      flushP()
      flushL()
      code = []
      continue
    }
    const h = l.match(/^(#{1,6})\s+(.*)$/)
    if (h) {
      flushP()
      flushL()
      const lv = Math.min(4, h[1].length + 1)
      out.push(lv === 2 ? <h2>{inline(h[2])}</h2> : lv === 3 ? <h3>{inline(h[2])}</h3> : <h4>{inline(h[2])}</h4>)
      continue
    }
    const li = l.match(/^\s*([-*+]|\d+\.)\s+(.*)$/)
    if (li) {
      flushP()
      const t = /\d/.test(li[1]) ? 'ol' : 'ul'
      const cur = list as { t: 'ul' | 'ol'; items: string[] } | null
      if (!cur || cur.t !== t) {
        flushL()
        list = { t, items: [] }
      }
      ;(list as unknown as { items: string[] }).items.push(li[2])
      continue
    }
    if (!l.trim()) {
      flushP()
      flushL()
      continue
    }
    const cur = list as { t: 'ul' | 'ol'; items: string[] } | null
    if (cur && /^\s{2,}/.test(l)) {
      cur.items[cur.items.length - 1] += ' ' + l.trim()
      continue
    }
    flushL()
    para.push(l.trim())
  }
  if (code)
    out.push(
      <pre>
        <code>{(code as string[]).join('\n')}</code>
      </pre>,
    )
  flushP()
  flushL()
  return <>{out}</>
}

// ---------- the markdown editor (UI.mdEditor) ----------

export function MdEditor({
  id,
  text,
  label,
  placeholder,
  links,
  onChange,
}: {
  id: string
  text: string
  label: string
  placeholder?: string
  // [[ links: the other entries of the repo
  links?: { v: string; label: string; kind: string }[]
  onChange: (t: string) => void
}): JSX.Element {
  const [mode, setMode] = useState<'write' | 'preview' | 'split'>('write')
  const [ac, setAc] = useState<{ q: string; at: number } | null>(null)
  const ta = useRef<HTMLTextAreaElement>(null)
  // the box grows with its text up to the editor's cap (--ed-max), then scrolls, as CodeMirror does
  useLayoutEffect(() => {
    const el = ta.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = el.scrollHeight + 2 + 'px'
  }, [text, mode])
  const mod = typeof navigator !== 'undefined' && /Mac|iP/.test(navigator.platform) ? '⌘' : 'Ctrl+'

  const fmt = (f: string) => {
    const el = ta.current
    if (!el) return
    const a = el.selectionStart
    const z = el.selectionEnd
    const sel = el.value.slice(a, z)
    let ins = ''
    let s0 = a
    let s1 = a
    if (f === 'bold' || f === 'italic' || (f === 'code' && !sel.includes('\n'))) {
      const w = f === 'bold' ? '**' : f === 'italic' ? '_' : '`'
      const t = sel || (f === 'code' ? 'code' : 'text')
      ins = w + t + w
      s0 = a + w.length
      s1 = s0 + t.length
    } else if (f === 'code') {
      ins = '```\n' + sel + '\n```'
      s0 = a + 4
      s1 = s0 + sel.length
    } else if (f === 'list') {
      ins = (sel || 'item')
        .split('\n')
        .map((l) => (/^\s*[-*] /.test(l) ? l : '- ' + l))
        .join('\n')
      s0 = a + (sel ? 0 : 2)
      s1 = a + ins.length
    } else if (f === 'link') {
      const t = sel || 'text'
      ins = `[${t}](https://)`
      s0 = a + t.length + 3
      s1 = s0 + 8
    }
    el.setRangeText(ins, a, z, 'end')
    el.setSelectionRange(s0, s1)
    el.focus()
    onChange(el.value)
  }
  const tool = (f: string, lbl: string, icn: string, keys?: string) => (
    <button type="button" class="md-tool" data-act="md-fmt" aria-label={lbl} data-tip={lbl + (keys ? ` (${keys})` : '')} onClick={() => fmt(f)}>
      <Icon name={icn} />
    </button>
  )
  const hits = ac && links ? links.filter((x) => (x.v + ' ' + x.label).toLowerCase().includes(ac.q.toLowerCase())).slice(0, 8) : []
  const pick = (v: string) => {
    const el = ta.current
    if (!el || !ac) return
    const before = el.value.slice(0, ac.at)
    const after = el.value.slice(el.selectionStart)
    const next = before + v + ']]' + after.replace(/^\]\]/, '')
    onChange(next)
    setAc(null)
    setTimeout(() => {
      const p = (before + v + ']]').length
      el.setSelectionRange(p, p)
      el.focus()
    }, 0)
  }
  const onInput = (v: string, el: HTMLTextAreaElement) => {
    onChange(v)
    const upto = v.slice(0, el.selectionStart)
    const m = upto.match(/\[\[([^\]\n]*)$/)
    setAc(m && links ? { q: m[1], at: upto.length - m[1].length } : null)
  }
  return (
    <div class={`md-ed mode-${mode}`} data-md={id}>
      <div class="md-head">
        <div class="md-tabs" role="tablist" aria-label={label}>
          <button type="button" role="tab" class="md-tab" data-act="md-mode" data-v="write" aria-selected={mode !== 'preview'} onClick={() => setMode('write')}>
            Write
          </button>
          <button
            type="button"
            role="tab"
            class="md-tab"
            data-act="md-mode"
            data-v="preview"
            aria-selected={mode === 'preview'}
            onClick={() => setMode('preview')}
          >
            Preview
          </button>
        </div>
        <div class="md-tools" role="toolbar" aria-label="Formatting">
          {tool('bold', 'Bold', 'bold', mod + 'B')}
          {tool('italic', 'Italic', 'italic', mod + 'I')}
          {tool('list', 'Bulleted list', 'list')}
          {tool('link', 'Link', 'link', mod + 'K')}
          {tool('code', 'Code', 'code', mod + 'E')}
          <span class="md-sep" aria-hidden="true"></span>
          <button
            type="button"
            class="md-tool"
            data-act="md-mode"
            data-v={mode === 'split' ? 'write' : 'split'}
            aria-pressed={mode === 'split'}
            aria-label="Side by side"
            data-tip="Side by side"
            onClick={() => setMode(mode === 'split' ? 'write' : 'split')}
          >
            <Icon name="split" />
          </button>
        </div>
      </div>
      <div class="md-body">
        <div class="md-cm" data-ph={placeholder || 'Write in markdown'} data-label={`${label} (markdown)`}>
          <textarea
            ref={ta}
            id={id}
            class="md-ta"
            spellcheck
            aria-label={`${label} (markdown)`}
            placeholder={placeholder || 'Write in markdown'}
            value={text}
            onInput={(e) => onInput(e.currentTarget.value, e.currentTarget)}
            onKeyDown={(e) => {
              const k = e.key.toLowerCase()
              if ((e.metaKey || e.ctrlKey) && ['b', 'i', 'k', 'e'].includes(k)) {
                e.preventDefault()
                fmt(k === 'b' ? 'bold' : k === 'i' ? 'italic' : k === 'k' ? 'link' : 'code')
              }
              if (e.key === 'Escape' && ac) setAc(null)
              if (e.key === 'Enter' && ac && hits.length) {
                e.preventDefault()
                pick(hits[0].v)
              }
            }}
          />
          {ac && hits.length ? (
            <div class="md-ac" role="listbox" aria-label="Link another entry">
              {hits.map((x, i) => (
                <button
                  type="button"
                  class="md-ac-i"
                  role="option"
                  aria-selected={i === 0}
                  onMouseDown={(e) => {
                    e.preventDefault()
                    pick(x.v)
                  }}
                >
                  <span>{x.label}</span>
                  <span class="md-ac-k">{x.kind}</span>
                  <span class="md-ac-v">{x.v}</span>
                </button>
              ))}
            </div>
          ) : null}
        </div>
        <div class="md-prev prose" id={`${id}-prev`} role="tabpanel" aria-label="Preview">
          {text.trim() ? <Markdown src={text} /> : <p class="md-none">Nothing to preview</p>}
        </div>
      </div>
      <div class="md-foot">
        <Icon name="edit" />
        <span>
          Write in markdown
          {links ? (
            <>
              {' · '}
              <code class="md-chip">[[</code> links another entry
            </>
          ) : null}
        </span>
      </div>
      <div class="md-err" role="alert"></div>
    </div>
  )
}
