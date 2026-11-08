// The workflow builder, ported from the prototype's js/screens/builder.js.
//
// A vertical list, top to bottom, the way conductor runs a workflow: the
// trigger card (`on:` + filters) first, then the ordered steps, with a
// `parallel:` step drawn as a fork that joins before the next step. A "+"
// between every step opens a searchable picker (built-in step kinds and every
// connection's verbs, from its plugin's own declaration). Each step expands in
// place into a form generated from its declared fields. Any field can insert a
// reference to the event or an earlier step's output ({{.triage.kind}}).
//
// YAML is canonical: every edit is an edit of the workflow's config node, the
// YAML pane shows it, and editing the YAML pane updates the list. Save stages
// the node through config staging; Save and apply then reviews the diff and
// applies it (the review panel below).
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useMemo, useReducer, useRef, useState } from 'preact/hooks'
import { createPortal } from 'preact/compat'
import { parseDocument, stringify as yamlStringify, LineCounter, isNode, Document } from 'yaml'
import { api } from '../api/client'
import type { WorkflowDetailResponse, TestResponse, WorkflowSummary } from '../api/types'
import { wfApi, type CatalogConnection, type WorkflowBuilderInfo, type WorkflowEvent } from '../api/wfbuilder'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { CodeEditor } from '../components/CodeEditor'
import { ReviewPanel } from '../components/ReviewPanel'
import { Menu, type MenuItem } from '../components/Menu'
import { FormModal } from '../components/FormModal'
import { FilterBar } from '../components/FilterBar'
import { EmptyState } from '../components/EmptyState'
import { statusMark } from '../state'
import { ago, humanDuration } from '../lib/format'
import { TIME_RANGES, rangeMinutes } from '../lib/filter'
import { go } from '../router'
import { humanError } from '../api/errors'
import { config, stage, staged, review, applyResult, loadConfig, doReview, cfgError } from '../configStore'
import * as WF from '../lib/wf'
import { isObj, type Obj, type WfCtx, type Entry, type Issue } from '../lib/wf'

/* ================= drafts (a new or unsaved workflow lives in this browser until Save) ================= */
const DRAFT = (n: string) => 'cd-wf-draft:' + n
export function readDraft(name: string): { text: string; kind: 'trigger' | 'workflow' } | null {
  try {
    const raw = localStorage.getItem(DRAFT(name))
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
export function writeDraft(name: string, text: string | null, kind: 'trigger' | 'workflow' = 'trigger'): void {
  try {
    if (text == null) localStorage.removeItem(DRAFT(name))
    else localStorage.setItem(DRAFT(name), JSON.stringify({ text, kind }))
  } catch {
    /* private mode: the draft lives in memory only */
  }
}
export function draftNames(): string[] {
  const out: string[] = []
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i) || ''
      if (k.startsWith('cd-wf-draft:')) out.push(k.slice('cd-wf-draft:'.length))
    }
  } catch {
    /* none */
  }
  return out
}

// nodeText is one node of the config file as it's written there: its key
// order and its comments kept (the decoded tree sorts keys).
function nodeText(src: string, path: (string | number)[]): string {
  try {
    const doc = parseDocument(src || '')
    const n = doc.getIn(path, true)
    if (!isNode(n)) return ''
    const d = new Document()
    d.contents = n as typeof d.contents
    return d.toString({ lineWidth: 0 })
  } catch {
    return ''
  }
}
const dump = (v: unknown) => (v == null ? '' : yamlStringify(v, { lineWidth: 0 }))
const clone = <T,>(v: T): T => (v == null ? v : JSON.parse(JSON.stringify(v)))
const locKey = (loc: (string | number)[]) => loc.join('.')
const locOf = (s: string) => s.split('.').map((x) => (/^\d+$/.test(x) ? +x : x))

// the trigger in words (the mock's When box): an event's own description, from its plugin
const LIFECYCLE: Record<string, string> = { manual: 'You start it', 'conductor.failed': 'A run fails', 'conductor.escalate': 'conductor needs a person', 'conductor.needs_input': 'A run asks a question' }
function whenWords(ctx: WfCtx, srcs: WF.Source[]): string {
  if (!srcs.length) return 'Nothing starts it yet'
  return srcs
    .map((x, i) => {
      const w = LIFECYCLE[x.on] || WF.eventOf(ctx, x.on)?.event.desc || x.on
      return i ? w.replace(/^A /, 'a ') : w.replace(/^./, (c) => c.toUpperCase())
    })
    .join(' or ')
}
function filterWords(f: unknown): string {
  if (!isObj(f)) return ''
  const out: string[] = []
  const list = (x: unknown) => ([] as unknown[]).concat(x).join(' or ')
  if (f.repo) out.push(`on ${list(f.repo)}`)
  if (f.not_label_any) out.push(`unless it’s labelled “${([] as unknown[]).concat(f.not_label_any).join('” or “')}”`)
  if (f.label_any) out.push(`only when labelled “${([] as unknown[]).concat(f.label_any).join('” or “')}”`)
  Object.keys(f)
    .filter((k) => !['repo', 'not_label_any', 'label_any'].includes(k))
    .forEach((k) => out.push(`with ${k.replace(/_/g, ' ')} ${list(f[k])}`))
  return out.join(', ')
}
const runParts = (rs: { status: string }[]) =>
  (
    [
      [rs.filter((r) => r.status === 'ok').length, 'went fine'],
      [rs.filter((r) => r.status === 'failed').length, 'failed'],
      [rs.filter((r) => r.status === 'running').length, 'running'],
      [rs.filter((r) => !['ok', 'failed', 'running'].includes(r.status)).length, 'other'],
    ] as [number, string][]
  )
    .filter(([n]) => n)
    .map(([n, t]) => `${n} ${t}`)
    .join(' · ')

/* ================= the builder's state ================= */
type Owner = 'config' | 'pack' | 'bot' | 'readonly' | 'missing'
interface B {
  name: string
  owner: Owner
  kind: 'trigger' | 'workflow'
  path: string[] | null // where Save stages it (null: a new one, computed at save)
  pack: string
  base: string // the text Save compares against: staged, else live
  live: string
  text: string
  hist: string[]
  hi: number
  lastPush: number
  value: Obj | null
  err: { line: number; msg: string } | null
  issues: Issue[]
  lines: Map<string, [number, number]> // a step's YAML line range, by location
  open: Set<string>
  sel: string | null
  side: 'yaml' | 'data'
  tab: 'recipe' | 'config' | 'runs' | 'settings'
  ev: string
  advOpen: Set<string>
  pasted: WorkflowEvent[]
  rtab: Record<string, string>
}

interface Loaded {
  name: string
  wf: WorkflowDetailResponse | null
  info: WorkflowBuilderInfo | null
  conns: CatalogConnection[]
  engines: string[]
  list: WorkflowSummary[]
}

type Modal =
  | { k: 'picker'; title: string; caps: string; ph?: string; items: PickItem[]; onPick: (x: PickItem) => void }
  | { k: 'ref'; target: string; loc: string; code: boolean }
  | { k: 'yamlsub'; loc: string }
  | { k: 'paste' }
  | null

interface PickItem {
  key: string
  group: string
  icon: string
  label: string
  sub: string
  dis?: string
  mono?: boolean
  risk?: 'read' | 'change' | 'high'
}

// a reason a control can't be used: aria-disabled with the reason as its tip (UI.off)
const off = (reason: string | false | null | undefined) => (reason ? { 'aria-disabled': 'true' as const, 'data-reason': reason, 'data-tip': reason } : {})

export function WorkflowDetail({ name }: { name: string }): JSX.Element {
  const [L, setL] = useState<Loaded | null>(null)
  const [err, setErr] = useState('')
  const [, force] = useReducer((n: number) => n + 1, 0)
  const bRef = useRef<B | null>(null)
  const [modal, setModal] = useState<Modal>(null)
  const [menu, setMenu] = useState<{ anchor: HTMLElement; items: MenuItem[] } | null>(null)
  const [toast, setToast] = useState<{ title: string; sub?: string; s: string } | null>(null)
  const [test, setTest] = useState<TestResponse | null>(null)
  const [testing, setTesting] = useState(false)
  const [since, setSince] = useState('7d')
  const [ac, setAc] = useState<{ target: string; loc: string; paths: { path: string; value: unknown }[]; sel: number; x: number; y: number; w: number } | null>(null)
  const dragId = useRef<string | null>(null)
  const reviewRef = useRef<HTMLDivElement>(null)

  const say = (title: string, sub = '', s = 'run') => {
    setToast({ title, sub, s })
    setTimeout(() => setToast((t) => (t && t.title === title ? null : t)), 3200)
  }

  const loadAll = async () => {
    setErr('')
    const [wf, info, cat, conn, list] = await Promise.all([
      api.workflow(name).catch(() => null),
      wfApi.builder(name).catch(() => null),
      wfApi.catalog().catch(() => ({ connections: [] })),
      api.connections().catch(() => null),
      api.workflows().catch(() => ({ workflows: [] as WorkflowSummary[] })),
      loadConfig(),
    ])
    const engines = [...new Set(['cli', ...((conn?.languages || []).map((l) => l.name))])]
    setL({ name, wf, info, conns: cat.connections || [], engines, list: list.workflows || [] })
  }
  useEffect(() => {
    bRef.current = null
    setL(null)
    setTest(null)
    loadAll().catch((e) => setErr(humanError(e)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  /* applied: the live config changed under us — reload, keeping nothing staged */
  const appliedAtMount = useRef(applyResult.value)
  useEffect(() => {
    if (applyResult.value && applyResult.value !== appliedAtMount.current) {
      appliedAtMount.current = applyResult.value
      writeDraft(name, null)
      const t = setTimeout(() => {
        bRef.current = null
        setL(null)
        void loadAll()
      }, 1500)
      return () => clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applyResult.value])

  const keyRef = useRef<((e: KeyboardEvent) => void) | null>(null)
  useEffect(() => {
    const k = (e: KeyboardEvent) => keyRef.current?.(e)
    document.addEventListener('keydown', k)
    return () => document.removeEventListener('keydown', k)
  }, [])

  const tree = (config.value?.tree || {}) as Obj
  const ctx: WfCtx = useMemo(() => {
    const rts = isObj(tree.runtimes) ? tree.runtimes : {}
    const wfs = isObj(tree.workflows) ? tree.workflows : {}
    const own = Object.entries(wfs).map(([n, d]) => ({ name: n, inputs: (isObj(d) && isObj(d.inputs) ? d.inputs : {}) as WfCtx['workflows'][0]['inputs'] }))
    const packs = (L?.list || []).filter((w) => w.kind === 'workflow' && !own.some((o) => o.name === w.name)).map((w) => ({ name: w.name, inputs: {} }))
    return {
      conns: L?.conns || [],
      runtimes: Object.keys(rts),
      defaultRuntime: Object.entries(rts).find(([, r]) => isObj(r) && r.default)?.[0],
      workflows: [...own, ...packs],
      engines: L?.engines || ['cli'],
      bots: Object.keys(isObj(tree.bots) ? tree.bots : {}),
      teams: Object.keys(isObj(tree.teams) ? tree.teams : {}),
      checks: Object.keys(isObj(tree.checks) ? tree.checks : {}),
    }
  }, [L, config.value])

  /* ---------- the staged and live versions of this workflow's config node ---------- */
  const nodeAt = (path: string[] | null): unknown => (path ? WF.at(tree, path.map((p) => (/^\d+$/.test(p) ? +p : p))) : undefined)
  const stagedAt = (path: string[] | null): unknown => {
    if (!path) return undefined
    const c = [...staged.value].reverse().find((x) => x.path.join('.') === path.join('.'))
    return c ? (c.delete ? null : c.value) : undefined
  }

  /* ---------- load ---------- */
  if (L && L.name === name && !bRef.current) {
    const info = L.info
    const wf = L.wf
    const draft = readDraft(name)
    let owner: Owner = 'missing'
    let path: string[] | null = null
    let live = ''
    let kind: 'trigger' | 'workflow' = (info?.kind || wf?.kind === 'workflow' ? (info?.kind as 'trigger' | 'workflow') || 'workflow' : draft?.kind) || 'trigger'
    if (info?.path) {
      owner = 'config'
      path = info.path
      live = nodeText(config.value?.text || '', path.map((p) => (/^\d+$/.test(p) ? +p : p))) || dump(nodeAt(path))
    } else if (wf) {
      owner = wf.source?.startsWith('pack:') ? 'pack' : 'readonly'
      live = wf.yaml
    } else if (draft) {
      owner = 'config'
      kind = draft.kind
    }
    const st = stagedAt(path)
    const base = st !== undefined ? dump(st) : live
    const text = draft && owner === 'config' ? draft.text : base
    const b: B = {
      name,
      owner,
      kind,
      path,
      pack: wf?.source?.startsWith('pack:') ? wf.source.slice(5) : '',
      base,
      live,
      text,
      hist: [text],
      hi: 0,
      lastPush: 0,
      value: null,
      err: null,
      issues: [],
      lines: new Map(),
      open: new Set(),
      sel: null,
      side: 'yaml',
      tab: 'recipe',
      ev: info?.events?.[0]?.id || '',
      advOpen: new Set(),
      pasted: [],
      rtab: {},
    }
    bRef.current = b
    parse(b)
    const first = WF.walk(ctx, b.value || {})[0]
    if (b.value && !b.value.on && !b.value.inputs) b.open.add('$trigger')
    else if (first && first.step.id) b.open.add(first.id)
    b.sel = first ? first.id : null
  }

  function parse(b: B) {
    const lc = new LineCounter()
    try {
      const doc = parseDocument(b.text || '', { lineCounter: lc })
      if (doc.errors.length) {
        const e = doc.errors[0]
        b.err = { line: e.linePos?.[0]?.line || 1, msg: e.message.split('\n')[0] }
      } else {
        const v = doc.toJS()
        b.value = isObj(v) ? v : v == null ? {} : (v as Obj)
        b.err = null
        // each step's line range, for highlighting the selected step and pointing at issues
        b.lines = new Map()
        WF.walk(ctx, b.value).forEach((e) => {
          const n = doc.getIn(e.path, true)
          if (isNode(n) && n.range) b.lines.set(locKey(e.path), [lc.linePos(n.range[0]).line, lc.linePos(Math.max(n.range[0], n.range[1] - 1)).line])
        })
        ;['on', 'filter', 'name'].forEach((k) => {
          const n = doc.getIn([k], true)
          if (isNode(n) && n.range) b.lines.set(k, [lc.linePos(n.range[0]).line, lc.linePos(n.range[0]).line])
        })
      }
    } catch (e) {
      b.err = { line: 1, msg: e instanceof Error ? e.message : String(e) }
    }
    const evf = currentEventFields(b)
    b.issues = b.err ? [{ level: 'error', path: [], line: b.err.line, msg: b.err.msg }] : WF.validate(ctx, b.value, { reusable: b.kind === 'workflow', eventFields: evf || undefined })
    b.issues.forEach((i) => {
      if (i.line) return
      let p = i.path.slice()
      while (p.length) {
        const r = b.lines.get(locKey(p))
        if (r) {
          i.line = r[0]
          return
        }
        p = p.slice(0, -1)
      }
      i.line = 1
    })
  }
  function currentEventFields(b: B): Obj | null {
    const e = (L?.info?.events || []).concat(b.pasted).find((x) => x.id === b.ev)
    return e ? ((e.fields || {}) as Obj) : null
  }

  if (err) return <Missing name={name} />
  if (!L || L.name !== name || !bRef.current) return <div class="wfb-page"><p class="muted">Loading…</p></div>
  const b = bRef.current
  if (b.owner === 'missing') return <Missing name={name} />

  /* ================= operations ================= */
  const readOnly = (): string =>
    b.owner === 'bot' ? 'Synthesized from the bot definition: edit the bot instead' : b.owner === 'pack' ? "Pack-owned: the structure is the pack's. Change its settings on the pack's page, or call this workflow from your own trigger" : b.owner === 'readonly' ? "This trigger has no name, so it can't be edited here safely: give it a name: in the config file" : ''
  const errs = () => b.issues.filter((i) => i.level === 'error' && !i.todo)
  const status = (): 'draft' | 'saved' | 'live' => {
    if (b.owner !== 'config') return 'live'
    if (b.text !== b.base) return 'draft'
    if (b.base !== b.live) return 'saved'
    return 'live'
  }
  const saveDraft = () => {
    if (b.owner !== 'config') return
    writeDraft(b.name, b.text === b.base && b.live ? null : b.text, b.kind)
  }
  function commit(text: string, opts: { coalesce?: boolean } = {}) {
    if (text === b.text) return
    b.text = text
    const now = Date.now()
    if (opts.coalesce && now - b.lastPush < 1200 && b.hi > 0) b.hist[b.hi] = text
    else {
      b.hist = b.hist.slice(0, b.hi + 1)
      b.hist.push(text)
      b.hi = b.hist.length - 1
    }
    b.lastPush = now
    parse(b)
    saveDraft()
    force(0)
  }
  const CANON = ['name', 'description', 'on', 'enabled', 'repo', 'filter', 'group', 'policy', 'gate', 'shadow', 'callable', 'options', 'inputs', 'outputs', 'steps', 'hooks']
  function canonical(v: Obj, had: Set<string>): Obj {
    const keys = Object.keys(v)
    const fresh = keys.filter((k) => !had.has(k) && CANON.includes(k))
    if (!fresh.length) return v
    const out: Obj = {}
    const rank = (k: string) => CANON.indexOf(k)
    keys.filter((k) => !fresh.includes(k)).forEach((k) => {
      fresh.filter((f) => !(f in out) && rank(f) < rank(k) && rank(k) >= 0).sort((a, c) => rank(a) - rank(c)).forEach((f) => (out[f] = v[f]))
      out[k] = v[k]
    })
    fresh.forEach((f) => !(f in out) && (out[f] = v[f]))
    return out
  }
  function mutate(fn: (v: Obj) => boolean | void, opts: { coalesce?: boolean } = {}): boolean {
    if (b.err) {
      say('Fix the YAML first', `line ${b.err.line}: ${b.err.msg}`, 'fail')
      return false
    }
    const v = clone(b.value || {})
    const had = new Set(Object.keys(b.value || {}))
    if (fn(v) === false) return false
    commit(dump(canonical(v, had)), opts)
    return true
  }
  const allIds = (v: unknown) => WF.walk(ctx, v).map((e) => e.step.id).filter(Boolean) as string[]
  const uniqueId = (v: unknown, base0: string) => {
    const base = String(base0 || 'step').replace(/[^\w-]/g, '-').replace(/^[^a-z]+/i, '') || 'step'
    const ids = new Set(allIds(v))
    if (!ids.has(base)) return base
    let n = 2
    while (ids.has(`${base}-${n}`)) n++
    return `${base}-${n}`
  }
  const entryAt = (loc: string) => WF.walk(ctx, b.value || {}).find((e) => locKey(e.path) === loc)
  const entryById = (id: string) => WF.walk(ctx, b.value || {}).find((e) => e.id === id)
  const findLoc = (v: unknown, id: string) => {
    const e = WF.walk(ctx, v).find((x) => x.id === id)
    return e ? { list: WF.at(v, e.list) as unknown[], index: e.index, e } : null
  }
  const stepAt = (v: Obj, loc: string) => WF.at(v, locOf(loc)) as Obj

  function undo() {
    if (b.hi <= 0) return
    b.hi--
    b.text = b.hist[b.hi]
    parse(b)
    saveDraft()
    force(0)
    say('Undone', '', 'idle')
  }
  function redo() {
    if (b.hi >= b.hist.length - 1) return
    b.hi++
    b.text = b.hist[b.hi]
    parse(b)
    saveDraft()
    force(0)
  }
  function deleteStep(id: string) {
    let gone = false
    mutate((v) => {
      const f = findLoc(v, id)
      if (!f) return false
      f.list.splice(f.index, 1)
      gone = true
      WF.walk(ctx, v).filter((x) => x.kind === 'parallel').forEach((p) => (p.step.parallel = (p.step.parallel as unknown[][]).filter((br) => br.length)))
    })
    if (gone) {
      b.open.delete(id)
      say(`Deleted ${id}`, 'Ctrl+Z to undo', 'idle')
    }
  }
  function dupStep(id: string) {
    let nid = ''
    mutate((v) => {
      const f = findLoc(v, id)
      if (!f) return false
      const c = clone(f.list[f.index]) as Obj
      const re = (n: unknown) => {
        if (isObj(n) && n.id) n.id = uniqueId(v, n.id + '-copy')
        if (isObj(n) && Array.isArray(n.parallel)) (n.parallel as unknown[][]).forEach((br) => br.forEach(re))
      }
      re(c)
      nid = String(c.id || '')
      f.list.splice(f.index + 1, 0, c)
    })
    if (nid) {
      b.open.add(nid)
      b.sel = nid
      force(0)
    }
  }
  function moveStep(id: string, d: number, keepFocus = false) {
    const ok = mutate((v) => {
      const f = findLoc(v, id)
      if (!f) return false
      const j = f.index + d
      if (j < 0 || j >= f.list.length) return false
      const [x] = f.list.splice(f.index, 1)
      f.list.splice(j, 0, x)
    })
    if (ok && keepFocus) setTimeout(() => document.querySelector<HTMLElement>(`#card-${CSS.escape(id)} .sc-head`)?.focus())
  }
  function setField(loc: string, path: string, val: unknown, opts: { coalesce?: boolean } = {}) {
    if (b.owner !== 'config') return
    if (!loc) {
      mutate((v) => {
        WF.setPath(v, path, val)
        if (val === undefined) prune(v, path)
      }, opts)
      return
    }
    if (path === 'id') {
      const e = entryAt(loc)
      const old = e?.step.id as string | undefined
      if (old && val && old !== val && /^[a-z][\w-]*$/i.test(String(val))) {
        if (b.open.has(old)) {
          b.open.delete(old)
          b.open.add(String(val))
        }
        if (b.sel === old) b.sel = String(val)
      }
    }
    mutate((v) => {
      const s = stepAt(v, loc)
      if (!s) return false
      if (path === 'isolation.mode' && val === undefined) {
        delete s.isolation
        return
      }
      WF.setPath(s, path, val)
      if (val === undefined) prune(s, path)
    }, opts)
  }
  function prune(obj: Obj, path: string) {
    const parts = path.split('.')
    for (let i = parts.length - 1; i > 0; i--) {
      const par = WF.getPath(obj, parts.slice(0, i).join('.'))
      if (isObj(par) && !Object.keys(par).length) WF.setPath(obj, parts.slice(0, i).join('.'), undefined)
      else break
    }
  }
  const coerce = (type: string, raw: string): unknown => {
    if (type === 'number') return raw === '' ? undefined : /^-?\d+(\.\d+)?$/.test(raw) ? Number(raw) : raw
    if (type === 'bool') return raw ? true : undefined
    if (type === 'code') return raw === '' ? undefined : raw.endsWith('\n') ? raw : raw + '\n'
    return raw === '' ? undefined : raw
  }

  /* ---------- save and apply ---------- */
  async function save(): Promise<boolean> {
    if (b.owner !== 'config') return false
    if (status() !== 'draft') return true
    if (b.err) {
      say('Fix the YAML first', `line ${b.err.line}: ${b.err.msg}`, 'fail')
      return false
    }
    const v = b.value || {}
    let path = b.path
    let value: unknown = v
    let summary = `edit workflow ${b.name}`
    if (!path) {
      if (b.kind === 'workflow') {
        path = ['workflows', b.name]
        const nv = { ...v }
        delete nv.name
        value = nv
      } else {
        const cur = Array.isArray(tree.triggers) ? clone(tree.triggers) : []
        cur.push(v)
        value = cur
        path = ['triggers']
      }
      summary = `new workflow ${b.name}`
    } else if (b.kind === 'workflow') {
      const nv = { ...v }
      delete nv.name
      value = nv
    }
    await stage(path, value, summary)
    if (cfgError.value) {
      say('Not saved', cfgError.value, 'fail')
      return false
    }
    if (path.length === 2 || b.kind === 'workflow') {
      b.base = b.text
      if (!b.path) b.path = path[0] === 'triggers' ? null : path
    } else b.base = b.text
    writeDraft(b.name, b.live ? null : b.text, b.kind)
    force(0)
    say('Saved', 'staged · Save and apply shows the diff, then applies it', 'run')
    return true
  }
  async function deploy() {
    if (!(await save())) return
    await doReview()
    setTimeout(() => reviewRef.current?.scrollIntoView({ block: 'start', behavior: 'smooth' }), 60)
  }
  async function runTest() {
    setTesting(true)
    try {
      const r = await api.testWorkflow(name)
      setTest(r)
      say(r.ok ? `Tested ${name}` : `${name} won't run as it is`, r.message, r.ok ? 'ok' : 'fail')
    } catch (e) {
      setTest({ ok: false, message: humanError(e) })
    } finally {
      setTesting(false)
    }
  }

  /* keys: undo/redo, save, move/delete/duplicate the focused step */
  {
    keyRef.current = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      const inField = !!t.closest?.('input, select, textarea:not(.ced-ta)') || (t.classList?.contains('ced-ta') && !t.closest('#wf-yaml'))
      const mod = e.ctrlKey || e.metaKey
      if (mod && e.key.toLowerCase() === 'z' && !inField) {
        e.preventDefault()
        if (e.shiftKey) redo()
        else undo()
      } else if (mod && e.key.toLowerCase() === 'y' && !inField) {
        e.preventDefault()
        redo()
      } else if (mod && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void save()
      }
      const head = t.closest?.('.sc-head') as HTMLElement | null
      if (head && !inField) {
        const card = head.closest('[data-card]') as HTMLElement | null
        const id = card?.dataset.card || ''
        if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
          e.preventDefault()
          moveStep(id, e.key === 'ArrowUp' ? -1 : 1, true)
        } else if (!e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
          e.preventDefault()
          const hs = [...document.querySelectorAll<HTMLElement>('.wfb-list .sc-head')]
          hs[hs.indexOf(head) + (e.key === 'ArrowUp' ? -1 : 1)]?.focus()
        } else if ((e.key === 'Delete' || e.key === 'Backspace') && id && id !== '$trigger' && !readOnly()) {
          e.preventDefault()
          deleteStep(id)
        } else if (mod && e.key.toLowerCase() === 'd' && id && id !== '$trigger' && !readOnly()) {
          e.preventDefault()
          dupStep(id)
        }
      }
    }
  }

  /* ================= pickers ================= */
  function pickerItems(inBranch: boolean): PickItem[] {
    const askVerb = ctx.conns.flatMap((c) => c.verbs.filter((v) => v.ask).map((v) => `${c.name}.${v.name}`))[0]
    const items: PickItem[] = [
      { key: 'kind:agent', group: 'Built-in', icon: 'sparkle', label: 'Agent', sub: 'An agent with a prompt, a runtime and a model, sandboxed.' },
      { key: 'kind:code', group: 'Built-in', icon: 'code', label: 'Code', sub: 'Inline JavaScript, Python, shell… stdout becomes outputs.' },
      { key: 'kind:decide', group: 'Built-in', icon: 'obj-question', label: 'Decide', sub: 'Typed questions answered with probabilities.' },
      { key: 'kind:ask', group: 'Built-in', icon: 'hand', label: 'Hand-off / approval', sub: 'Ask a human and wait: approve, revise or discard.', dis: askVerb ? '' : 'No connection here can ask a person yet: add one in Plugins' },
      { key: 'kind:wait', group: 'Built-in', icon: 'clock', label: 'Wait', sub: 'sleep: a duration, or wait_for: a condition on a read verb.' },
      { key: 'kind:parallel', group: 'Built-in', icon: 'workflows', label: 'Parallel group', sub: 'Branches run at once and join before the next step.', dis: inBranch ? "A parallel group can't nest inside a branch" : '' },
      { key: 'kind:bot', group: 'Built-in', icon: 'members', label: 'Hand to a bot or team', sub: 'Deliver to a bot, or to a team whose rules (or lead) route it.', dis: ctx.bots.length || ctx.teams.length ? '' : 'No bots or teams yet' },
      { key: 'kind:call', group: 'Built-in', icon: 'link', label: 'Call a workflow', sub: 'Run a reusable workflow with inputs; its outputs come back.', dis: ctx.workflows.length ? '' : 'No reusable workflows yet: make one with New workflow → Reusable' },
      { key: 'kind:log', group: 'Built-in', icon: 'logs', label: 'Log', sub: 'A breadcrumb in the run log.' },
      { key: 'kind:set', group: 'Built-in', icon: 'edit', label: 'Set values', sub: 'Publish computed values as outputs.' },
      { key: 'kind:assert', group: 'Built-in', icon: 'check', label: 'Assert', sub: 'Fail the run unless a condition holds.' },
      { key: 'tpl:command', group: 'Built-in', icon: 'code', label: 'Run a command', sub: 'use: cli with command: [ … ], in the sandbox', risk: 'change' },
      { key: 'tpl:fail', group: 'Built-in', icon: 'x', label: 'Fail the run', sub: 'fail: stop here with a message (pair it with if:)' },
    ]
    ctx.conns.forEach((c) => c.verbs.forEach((v) => items.push({ key: `verb:${c.name}.${v.name}`, group: `${c.use} · ${c.name}`, icon: 'zap', label: v.desc || v.name, sub: `${c.name}.${v.name}`, mono: true })))
    // groups keep their order; inside a group, what only reads comes first and what's high impact last
    const R = { read: 0, change: 1, high: 2 }
    const gi = new Map<string, number>()
    items.forEach((x) => !gi.has(x.group) && gi.set(x.group, gi.size))
    return items
      .map((x, i) => [x, i] as [PickItem, number])
      .sort((a, b) => gi.get(a[0].group)! - gi.get(b[0].group)! || (a[0].risk ? R[a[0].risk] : 0) - (b[0].risk ? R[b[0].risk] : 0) || a[1] - b[1])
      .map(([x]) => x)
  }
  function makeStep(v: Obj, item: PickItem): Obj {
    const [t, k] = item.key.split(/:(.*)/s)
    if (t === 'kind') {
      const base: Record<string, string> = { agent: 'agent', code: 'code', decide: 'decide', ask: 'approval', wait: 'wait', parallel: 'parallel', call: 'call', log: 'log', set: 'vars', assert: 'check', bot: 'handoff' }
      const id = uniqueId(v, base[k])
      switch (k) {
        case 'agent':
          return { id, type: 'agent', prompt: '' }
        case 'code':
          return { id, use: ctx.engines.find((e) => e !== 'cli') || 'cli', ...(ctx.engines.some((e) => e !== 'cli') ? { code: 'return { ok: true };\n' } : { command: ['echo', 'hello'] }) }
        case 'decide':
          return { id, model: 'light', decide: { state: '', questions: { proceed: { type: 'noul', instructions: '' } } } }
        case 'ask': {
          const askVerb = ctx.conns.flatMap((c) => c.verbs.filter((x) => x.ask).map((x) => `${c.name}.${x.name}`))[0]
          return { id, uses: askVerb, options: {} }
        }
        case 'wait':
          return { sleep: '30s' }
        case 'parallel': {
          const a = uniqueId(v, id + '-a')
          return { parallel: [[{ id: a, log: 'branch a' }], [{ id: a === id + '-a' ? id + '-b' : uniqueId(v, id + '-b'), log: 'branch b' }]] }
        }
        case 'bot':
          return ctx.teams.length ? { id, bot: { team: ctx.teams[0], title: '{{.title}}' } } : { id, bot: { name: ctx.bots[0], title: '{{.title}}' } }
        case 'call':
          return { id, call: ctx.workflows[0]?.name || '', with: {} }
        case 'log':
          return { log: '' }
        case 'set':
          return { id, set: { key: 'value' } }
        case 'assert':
          return { assert: '' }
      }
    }
    if (t === 'tpl') return k === 'command' ? { id: uniqueId(v, 'run'), use: 'cli', command: ['make', 'test'] } : { id: uniqueId(v, 'stop'), fail: 'Explain why the run stops here' }
    const verb = k.split('.')[1]
    const node: Obj = { id: uniqueId(v, verb), uses: k }
    const d = WF.verbOf(ctx, k)
    const req = (d?.verb.options || []).filter((f) => f.required)
    if (req.length) node.options = Object.fromEntries(req.map((f) => [f.key, f.type === 'list' ? [] : f.enum?.length ? f.enum[0] : '']))
    return node
  }
  function openAdd(loc: (string | number)[], i: number) {
    setModal({
      k: 'picker',
      caps: 'add a step',
      title: 'What should happen?',
      items: pickerItems(loc.length > 1),
      onPick: (item) => {
        let newId = ''
        mutate((v) => {
          let list = WF.at(v, loc) as unknown[] | undefined
          if (!Array.isArray(list)) {
            if (loc.length === 1) v.steps = []
            list = WF.at(v, loc) as unknown[]
          }
          const node = makeStep(v, item)
          newId = String(node.id || '')
          list.splice(i, 0, node)
        })
        if (newId) {
          b.open.add(newId)
          b.sel = newId
          force(0)
          setTimeout(() => {
            const c = document.getElementById('card-' + newId)
            c?.scrollIntoView({ block: 'center' })
            c?.classList.add('just-added')
          }, 30)
        }
      },
    })
  }
  function openTriggerPicker(add: boolean) {
    const items: PickItem[] = [
      { key: 'on:manual', group: 'conductor', icon: 'play', label: 'Manual run', sub: 'manual · conductor run, or the Run button' },
      { key: 'on:conductor.failed', group: 'conductor', icon: 'warn', label: 'A run failed', sub: 'conductor.failed · lifecycle' },
    ]
    ctx.conns.forEach((c) => c.events.forEach((e) => items.push({ key: `on:${c.name}.${e.name}`, group: `${c.use} · ${c.name}`, icon: 'zap', label: e.desc || e.name, sub: `${c.name}.${e.name}`, mono: true })))
    setModal({
      k: 'picker',
      caps: 'trigger',
      title: 'What starts this workflow?',
      ph: 'Search events: check failed, review, incident, schedule…',
      items,
      onPick: (item) =>
        mutate((v) => {
          const on = item.key.slice(3)
          const cur = WF.sources(v).map((x) => (x.block ? { [x.on]: x.block } : x.on))
          if (add) {
            if (cur.includes(on)) return false
            cur.push(on)
          } else cur.splice(0, cur.length, on)
          v.on = cur.length === 1 ? cur[0] : cur
        }),
    })
  }
  function openHookPicker(at: string) {
    setModal({
      k: 'picker',
      caps: 'add a hook',
      title: `Which call on ${at}?`,
      items: pickerItems(false).filter((x) => x.key.startsWith('verb:')),
      onPick: (item) =>
        mutate((v) => {
          const n = makeStep(v, item)
          if (!n.uses) return false
          v.hooks = Array.isArray(v.hooks) ? v.hooks : []
          ;(v.hooks as unknown[]).push({ at, uses: n.uses, ...(n.options ? { options: n.options } : {}) })
        }),
    })
  }
  function stepMenu(anchor: HTMLElement, id: string) {
    const ro = readOnly()
    const f = findLoc(b.value, id)
    const first = f && f.index === 0
    const last = f && f.index === f.list.length - 1
    setMenu({
      anchor,
      items: [
        { label: 'Insert a step above', icon: 'plus', disabled: ro || false, onClick: () => f && openAdd(f.e.list, f.index) },
        { label: 'Insert a step below', icon: 'plus', disabled: ro || false, onClick: () => f && openAdd(f.e.list, f.index + 1) },
        '-',
        { label: 'Move up', icon: 'chev', disabled: ro || (first ? 'Already first' : false), onClick: () => moveStep(id, -1) },
        { label: 'Move down', icon: 'chev', disabled: ro || (last ? 'Already last' : false), onClick: () => moveStep(id, 1) },
        { label: 'Duplicate', icon: 'copy', disabled: ro || false, onClick: () => dupStep(id) },
        {
          label: 'Show in YAML',
          icon: 'code',
          onClick: () => {
            b.sel = id
            b.side = 'yaml'
            b.tab = 'config'
            force(0)
          },
        },
        '-',
        { label: 'Delete', icon: 'trash', disabled: ro || false, onClick: () => deleteStep(id) },
      ],
    })
  }

  /* ---------- references: the data picker and autocomplete ---------- */
  const scopeFor = (loc: string) => WF.scope(ctx, b.value || {}, loc ? entryAt(loc) || null : null, currentEventFields(b))
  function insertRef(target: string, path: string) {
    const el = document.getElementById(target) as HTMLInputElement | HTMLTextAreaElement | null
    if (!el) return
    const isCode = el.closest('.code-wrap') != null
    const txt = isCode ? `ctx.${path}` : `{{.${path}}}`
    el.focus()
    const a = el.selectionStart ?? el.value.length
    const z = el.selectionEnd ?? a
    const m = el.value.slice(0, a).match(/\{\{\s*\.?[\w.]*$/)
    if (m && !isCode) el.setRangeText(txt, a - m[0].length, z, 'end')
    else el.setRangeText(txt, a, z, 'end')
    el.dispatchEvent(new Event('input', { bubbles: true }))
    setAc(null)
  }
  function autocomplete(el: HTMLInputElement | HTMLTextAreaElement, loc: string) {
    const m = el.value.slice(0, el.selectionStart ?? 0).match(/\{\{\s*\.([\w.-]*)$/)
    if (!m) return setAc(null)
    const ps = WF.paths(scopeFor(loc)).filter((p) => p.path.toLowerCase().startsWith(m[1].toLowerCase())).slice(0, 8)
    if (!ps.length) return setAc(null)
    const r = el.getBoundingClientRect()
    setAc({ target: el.id, loc, paths: ps, sel: 0, x: Math.min(r.left, innerWidth - 368), y: r.bottom + 4, w: Math.min(r.width, 360) })
  }

  /* ================= rendering ================= */
  const KC = (k: string) => WF.KINDS[k] || WF.KINDS.unknown
  const issuesFor = (id: string) => b.issues.filter((i) => i.step === id)

  function bar(): JSX.Element {
    const s = status()
    const ro = readOnly()
    const e = errs()
    const applyBlock = b.err ? 'Fix the YAML first' : e.length ? `${e.length} error${e.length > 1 ? 's' : ''} to fix first` : b.owner !== 'config' ? ro : s === 'live' ? 'Nothing to apply: this is what’s live' : ''
    const v = b.value || {}
    const runsN = (L?.wf?.runs || []).length
    const testBlock = b.kind === 'workflow' ? 'A reusable workflow runs when another workflow calls it: test that one' : !L?.wf ? 'Save and apply it first: a test checks what’s live' : b.err ? 'Fix the YAML first' : ''
    return (
      <>
        <nav class="dl-crumbs" aria-label="Where you are">
          <a href="#/workflows">Workflows</a>
          <span class="sep" aria-hidden="true">›</span>
          <span class="cur" aria-current="page">{b.name || 'A new workflow'}</span>
        </nav>
        <div class="dl-phead">
          <div>
            <h1 class="wfb-title">{b.name}</h1>
            <p class="dl-lead">{String(v.description || '')}</p>
            <p class="wfb-owner">
              {b.owner === 'pack' ? <>From the pack <b>{b.pack}</b>: change its settings on the pack’s page.</> : b.owner === 'bot' ? 'Made by conductor for the bot (synthesized); read-only here.' : ''}{' '}
              {s === 'saved' ? <StatusMark s="staged" word="Saved, not applied" sm cls={'wf-state ' + s} /> : <span class={'dl-sub wf-state ' + s} id="wf-state">{s === 'draft' ? 'Unsaved changes' : 'Live'}</span>}
            </p>
          </div>
          <div class="dl-actions tight">
            <button class="btn ghost" type="button" {...off(b.hi > 0 ? '' : 'Nothing to undo')} onClick={() => b.hi > 0 && undo()}>
              <Icon name="undo" />Undo
            </button>
            <button class="btn secondary" type="button" {...off(testBlock || (testing ? 'Testing…' : ''))} onClick={() => !testBlock && !testing && void runTest()}>
              <Icon name="play" />Test
            </button>
            <button class="btn primary" type="button" data-testid="wf-deploy" {...off(applyBlock)} onClick={() => !applyBlock && void deploy()}>
              Save and apply
            </button>
          </div>
        </div>
        <nav class="dl-tabs" role="tablist">
          {(
            [
              ['recipe', 'Recipe'],
              ['config', 'Config file'],
              ['runs', 'Runs', runsN],
              ['settings', 'Settings'],
            ] as [B['tab'], string, number?][]
          ).map(([k, l, n]) => (
            <a
              href="#"
              role="tab"
              class={b.tab === k ? 'on' : ''}
              aria-selected={b.tab === k ? 'true' : undefined}
              onClick={(e) => {
                e.preventDefault()
                b.tab = k
                force(0)
              }}
            >
              {l}
              {n ? <> <span class="count">{n}</span></> : null}
            </a>
          ))}
        </nav>
      </>
    )
  }

  function addSlot(loc: (string | number)[], index: number, label?: string): JSX.Element {
    const ro = readOnly()
    return (
      <div class="spine-add">
        <button type="button" class="plus" {...off(ro)} aria-label={label || 'Add a step here'} onClick={() => !ro && openAdd(loc, index)}>
          <Icon name="plus" />
        </button>
      </div>
    )
  }

  function triggerCard(): JSX.Element {
    const v = b.value || {}
    const open = b.open.has('$trigger')
    const srcs = WF.sources(v)
    const iss = b.issues.filter((i) => i.trigger)
    const reusable = b.kind === 'workflow' || !!v.inputs
    const ro = readOnly()
    const inputs = isObj(v.inputs) ? v.inputs : {}
    const sum = reusable ? `called with ${Object.keys(inputs).join(', ') || 'no inputs'}` : ''
    const ev = (L?.info?.events || []).concat(b.pasted).find((x) => x.id === b.ev)
    const hard = iss.filter((i) => !i.todo).length
    return (
      <div class={'trig-card' + (open ? ' open' : '') + (iss.some((i) => !i.todo) ? ' has-err' : '')} id="card-$trigger">
        <div class="sc-head" role="button" tabIndex={0} aria-expanded={open} onClick={() => toggle('$trigger')} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), toggle('$trigger'))}>
          <span class="wl">
            <Icon name={reusable ? 'link' : 'zap'} />
            {reusable ? 'Called' : 'When'}
          </span>
          <div class="grow">
            <b>{reusable ? sum : whenWords(ctx, srcs)}</b> <span class="dl-sub">{filterWords(v.filter)}</span>
          </div>
          {hard ? <StatusMark s="fail" word={`${hard} error${hard === 1 ? '' : 's'}`} sm /> : iss.length ? <span class="dl-sub">{iss.length} to do</span> : ev ? <span class="dl-sub">test event: {ev.label.split(' · ')[0]}</span> : null}
          <span class="chev"><Icon name={open ? 'chevd' : 'chev'} /></span>
        </div>
        {open ? (
          <div class="sc-body">
            {iss.map((i) => (i.todo ? <div class="callout small todo"><Icon name="dot" /> {i.todo}</div> : <div class="callout danger small">{i.msg}</div>))}
            {!srcs.length && !reusable ? (
              <div class="empty small">
                <span class="serif">What starts this workflow?</span>An event from a connection, a schedule, or a manual run.
                <button class="btn primary" style="margin-top:8px" type="button" {...off(ro)} onClick={() => !ro && openTriggerPicker(false)}>
                  <Icon name="plus" />Choose a trigger
                </button>
              </div>
            ) : null}
            {reusable ? inputsEditor(v, ro) : srcs.length ? sourcesEditor(v, srcs, ro) : null}
          </div>
        ) : null}
      </div>
    )
  }

  function sourcesEditor(v: Obj, srcs: WF.Source[], ro: string): JSX.Element {
    const allowed = new Set<string>()
    srcs.forEach((s) => (WF.eventOf(ctx, s.on)?.event.filters || []).forEach((k) => allowed.add(k)))
    const keys = [...allowed].flatMap((k) => [k, 'not_' + k]).concat(['expr'])
    const filter = isObj(v.filter) ? v.filter : {}
    const freeKeys = keys.filter((k) => !(k in filter))
    return (
      <>
        <div class="caps">Sources</div>
        {srcs.map((s, i) => {
          const e = WF.eventOf(ctx, s.on)
          return (
            <div class="src-row">
              <span class="sc-ic sm"><Icon name={s.on === 'manual' ? 'play' : e ? 'zap' : 'live'} /></span>
              <div class="grow">
                <b class="mono small">{s.on}</b>
                <div class="muted tiny">{s.on === 'manual' ? 'started by you: conductor run, or Run now' : e ? e.event.desc || e.event.name : 'conductor lifecycle event'}</div>
              </div>
              <button class="btn sm ghost" type="button" {...off(ro)} aria-label={`Remove source ${s.on}`} onClick={() => !ro && removeSource(i)}>
                <Icon name="trash" />
              </button>
            </div>
          )
        })}
        <button class="btn sm" type="button" {...off(ro)} onClick={() => !ro && openTriggerPicker(true)}>
          <Icon name="plus" />Add a source
        </button>
        <div class="caps" style="margin-top:6px">
          Filters <span class="muted" style="text-transform:none;letter-spacing:0">· a map is AND; any key takes not_; expr: adds a condition</span>
        </div>
        {typeof v.filter === 'string' || Array.isArray(v.filter) ? <div class="callout info small">This filter is {typeof v.filter === 'string' ? 'an expression' : 'a list (OR of alternatives)'}: edit it in the YAML pane or below.</div> : null}
        {Object.entries(filter).map(([k, x]) => (
          <div class="flt-row">
            <span class="mono small">{k}</span>
            <input
              class="input mono"
              value={([] as unknown[]).concat(x).join(', ')}
              readOnly={!!ro}
              aria-label={`Filter ${k}`}
              onInput={(e) => {
                const vals = e.currentTarget.value.split(',').map((y) => y.trim()).filter(Boolean)
                mutate((nv) => {
                  ;(nv.filter as Obj)[k] = vals.length === 1 && !Array.isArray(x) ? vals[0] : vals
                }, { coalesce: true })
              }}
            />
            <button class="btn sm ghost" type="button" {...off(ro)} aria-label={`Remove filter ${k}`} onClick={() => !ro && mutate((nv) => {
              delete (nv.filter as Obj)[k]
              if (!Object.keys(nv.filter as Obj).length) delete nv.filter
            })}>
              <Icon name="trash" />
            </button>
          </div>
        ))}
        <FilterAdd keys={freeKeys} ro={ro} allowed={allowed.size > 0} onAdd={(k, val) => mutate((nv) => {
          nv.filter = isObj(nv.filter) ? nv.filter : {}
          if (/expr$/.test(k)) (nv.filter as Obj)[k] = val
          else {
            const vals = val.split(',').map((y) => y.trim()).filter(Boolean)
            ;(nv.filter as Obj)[k] = vals.length === 1 ? (vals[0] === 'true' ? true : vals[0] === 'false' ? false : vals[0]) : vals
          }
        })} />
        <details class="adv" open={b.advOpen.has('$trigger')} onToggle={(e) => advToggle('$trigger', (e.currentTarget as HTMLDetailsElement).open)}>
          <summary>More: name, grouping, budget</summary>
          <div class="stack tight" style="margin-top:8px">
            {fieldRow('', 'name', { type: 'string', help: 'A trigger reachable by conductor run needs a unique name.' }, v.name, ro || 'Rename it from the Workflows list (Duplicate…, then delete the old one)')}
            {fieldRow('', 'group.key', { type: 'string', help: 'Batch a burst of events into one run, e.g. {{.repo}}#{{.pr}}.' }, WF.getPath(v, 'group.key'), ro)}
            {fieldRow('', 'group.window', { type: 'string' }, WF.getPath(v, 'group.window'), ro)}
            {fieldRow('', 'policy.budget.window', { type: 'string', label: 'budget · window', ph: '24h (default)', ref: false }, WF.getPath(v, 'policy.budget.window'), ro)}
            {fieldRow('', 'policy.budget.max_tokens', { type: 'string', label: 'budget · max tokens in the window', ph: '500k or 2m', ref: false, help: 'A hard cap: over-cap runs shed and retry when the window frees.' }, WF.getPath(v, 'policy.budget.max_tokens'), ro)}
            {fieldRow('', 'policy.budget.max_cost_usd', { type: 'number', label: 'budget · max cost (USD) in the window', ref: false }, WF.getPath(v, 'policy.budget.max_cost_usd'), ro)}
            {fieldRow('', 'callable', { type: 'bool', on: 'callable over the invoke API (widening)' }, v.callable, ro)}
          </div>
        </details>
      </>
    )
  }
  function removeSource(i: number) {
    mutate((v) => {
      const s = WF.sources(v)
      s.splice(i, 1)
      const raw = s.map((x) => (x.block ? { [x.on]: x.block } : x.on))
      if (!raw.length) delete v.on
      else v.on = raw.length === 1 && typeof raw[0] === 'string' ? raw[0] : raw
    })
  }
  function inputsEditor(v: Obj, ro: string): JSX.Element {
    const ins = isObj(v.inputs) ? v.inputs : {}
    return (
      <>
        <div class="caps">Inputs</div>
        {Object.entries(ins).map(([k, d]) => {
          const dd = isObj(d) ? d : {}
          return (
            <div class="flt-row">
              <span class="mono small">{k}</span>
              <span class="muted tiny">
                {String(dd.type || 'string')}
                {dd.required ? ' · required' : ''}
                {dd.default != null ? ' · default ' + String(dd.default) : ''}
              </span>
              <button class="btn sm ghost" type="button" {...off(ro)} aria-label={`Remove input ${k}`} onClick={() => !ro && mutate((nv) => void delete (nv.inputs as Obj)[k])}>
                <Icon name="trash" />
              </button>
            </div>
          )
        })}
        <InputAdd ro={ro} onAdd={(n, type, req) => mutate((nv) => {
          nv.inputs = isObj(nv.inputs) ? nv.inputs : {}
          ;(nv.inputs as Obj)[n] = { type, ...(req ? { required: true } : {}) }
        })} />
      </>
    )
  }

  /* ---------- generated step forms ---------- */
  interface FD {
    type: string
    label?: string
    help?: ComponentChildren
    ph?: string
    def?: unknown
    req?: boolean
    ref?: boolean
    rows?: number
    on?: string
    options?: (string | [string, string])[]
    list?: string
  }
  const TEMPLATED = new Set(['text', 'string', 'number'])
  function fieldRow(loc: string, path: string, fd: FD, val: unknown, ro: string, extra?: ComponentChildren): JSX.Element {
    const id = 'wf-' + (loc || 'trig').replace(/\./g, '_') + '-' + path.replace(/[^\w]/g, '_')
    const label = fd.label || path.split('.').pop()!.replace(/_/g, ' ')
    const e = loc ? entryAt(loc) : null
    const isu = loc ? b.issues.filter((i) => e && i.step === e.id && i.field && (i.field === path || i.field.startsWith(path + '.'))) : b.issues.filter((i) => i.trigger && i.field === path)
    const ph = fd.def != null && fd.type !== 'enum' && fd.type !== 'bool' ? `default ${fd.def}` : fd.ph || ''
    const refBtn =
      fd.ref !== false && (TEMPLATED.has(fd.type) || fd.type === 'code') && !ro ? (
        <button type="button" class="ref-btn" onClick={() => setModal({ k: 'ref', target: id, loc, code: fd.type === 'code' })}>
          <Icon name="code" />
          <span>Insert from the event</span>
        </button>
      ) : null
    const v = val == null ? '' : val
    const str = typeof v === 'object' ? JSON.stringify(v) : String(v)
    const onText = (el: HTMLInputElement | HTMLTextAreaElement) => {
      autocomplete(el, loc)
      if (ro) return
      setField(loc, path, coerce(fd.type, el.value), { coalesce: path !== 'id' })
    }
    let input: JSX.Element
    switch (fd.type) {
      case 'text':
        input = <textarea class="textarea mono" rows={fd.rows || 3} id={id} readOnly={!!ro} aria-readonly={ro ? 'true' : undefined} placeholder={ph} value={str} onInput={(ev) => onText(ev.currentTarget)} onBlur={() => setTimeout(() => setAc(null), 150)} />
        break
      case 'code':
        input = (
          <div class="code-wrap">
            <textarea class="textarea mono" rows={7} id={id} readOnly={!!ro} value={str} spellcheck={false} onInput={(ev) => !ro && setField(loc, path, coerce('code', ev.currentTarget.value), { coalesce: true })} />
          </div>
        )
        break
      case 'bool':
        input = (
          <label class="row small" style="min-height:30px">
            <input type="checkbox" class="cbx" id={id} checked={!!v} disabled={!!ro} onChange={(ev) => setField(loc, path, ev.currentTarget.checked ? true : undefined)} /> {fd.on || 'on'}
          </label>
        )
        break
      case 'enum': {
        const opts = (fd.options || []).map((o) => (Array.isArray(o) ? o : [o, o])) as [string, string][]
        input = (
          <select class="select mono" id={id} disabled={!!ro} onChange={(ev) => setField(loc, path, ev.currentTarget.value === '' ? undefined : ev.currentTarget.value)}>
            {(fd.req ? [] : [['', fd.def != null ? `default (${fd.def})` : '— none —']]).concat(opts).map(([ov, ol]) => (
              <option value={ov} selected={String(ov) === str}>{ol}</option>
            ))}
            {str !== '' && !opts.some((o) => String(o[0]) === str) ? <option selected value={str}>{str}</option> : null}
          </select>
        )
        break
      }
      case 'list':
      case 'multi': {
        const items = Array.isArray(v) ? (v as unknown[]) : v === '' ? [] : [v]
        input = (
          <div class="listed" id={id}>
            {items.map((x) => (
              <span class="chip-x">
                <span class="mono">{String(x)}</span>
                {ro ? null : (
                  <button type="button" aria-label={`Remove ${x}`} onClick={() => setField(loc, path, items.filter((y) => String(y) !== String(x)))}>
                    <Icon name="x" />
                  </button>
                )}
              </span>
            ))}
            {ro ? null : fd.type === 'multi' ? (
              <select class="select mono list-add-sel" aria-label={`Add ${label}`} onChange={(ev) => ev.currentTarget.value && setField(loc, path, items.concat([ev.currentTarget.value]))}>
                <option value="">+ add…</option>
                {(fd.options || []).filter((o) => !items.includes(o)).map((o) => (
                  <option>{String(o)}</option>
                ))}
              </select>
            ) : (
              <input
                class="input mono list-add"
                placeholder="add, then Enter"
                aria-label={`Add ${label}`}
                onKeyDown={(ev) => {
                  if (ev.key !== 'Enter') return
                  ev.preventDefault()
                  const x = ev.currentTarget.value.trim()
                  if (!x) return
                  ev.currentTarget.value = ''
                  setField(loc, path, items.concat([x]))
                }}
              />
            )}
          </div>
        )
        break
      }
      case 'map': {
        const ents = Object.entries(isObj(v) ? v : {})
        const write = (box: HTMLElement) => {
          const o: Obj = {}
          box.querySelectorAll('.kvrow').forEach((r) => {
            const k = (r.querySelector('[data-mk=k]') as HTMLInputElement).value.trim()
            const val2 = (r.querySelector('[data-mk=v]') as HTMLInputElement).value
            if (k) o[k] = /^-?\d+(\.\d+)?$/.test(val2) ? Number(val2) : val2
          })
          setField(loc, path, Object.keys(o).length ? o : undefined, { coalesce: true })
        }
        input = (
          <div class="kvedit wf-map" id={id}>
            {ents.concat(ro ? [] : [['', '']]).map(([a, c], i) => (
              <div class="kvrow" key={i}>
                <input class="input mono" placeholder="key" value={a} data-mk="k" readOnly={!!ro} onChange={(ev) => write(ev.currentTarget.closest('.wf-map') as HTMLElement)} />
                <input class="input mono" placeholder="value" value={typeof c === 'object' ? JSON.stringify(c) : String(c)} data-mk="v" readOnly={!!ro} onChange={(ev) => write(ev.currentTarget.closest('.wf-map') as HTMLElement)} />
                <span></span>
              </div>
            ))}
          </div>
        )
        break
      }
      case 'seg': {
        const opts = (fd.options || []) as [string, string][]
        const cur = String(v || fd.def || '')
        input = (
          <div class="seg" role="group">
            {opts.map(([ov, ol]) => (
              <button type="button" {...off(ro)} aria-pressed={String(ov) === cur} class={String(ov) === cur ? 'on' : ''} onClick={() => !ro && setField(loc, path, ov === '' ? undefined : ov)}>
                {ol}
              </button>
            ))}
          </div>
        )
        break
      }
      default:
        input = <input class="input mono" id={id} value={str} readOnly={!!ro} aria-readonly={ro ? 'true' : undefined} placeholder={ph} list={fd.list} autocomplete="off" onInput={(ev) => onText(ev.currentTarget)} onBlur={() => setTimeout(() => setAc(null), 150)} onKeyDown={acKeys} />
    }
    const hard = isu.filter((i) => !i.todo)
    return (
      <div class={'field wf-field' + (hard.length ? ' bad' : isu.length ? ' todo' : '')} data-field={path}>
        <label for={id}>
          {label} {fd.req ? <span class="req">*</span> : null}
          {refBtn}
        </label>
        {input}
        {fd.help ? <div class="hint">{fd.help}</div> : null}
        {isu.map((i) => (i.todo ? <div class="ftodo"><Icon name="dot" />{i.todo}</div> : <div class="ferr">{i.msg}</div>))}
        {extra}
      </div>
    )
  }
  function acKeys(e: KeyboardEvent) {
    if (!ac || (e.target as HTMLElement).id !== ac.target) return
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setAc({ ...ac, sel: (ac.sel + (e.key === 'ArrowDown' ? 1 : -1) + ac.paths.length) % ac.paths.length })
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      insertRef(ac.target, ac.paths[ac.sel].path)
    } else if (e.key === 'Escape') {
      e.stopPropagation()
      setAc(null)
    }
  }

  function formFor(e: Entry, loc: string, ro: string): JSX.Element {
    const s = e.step
    const k = e.kind
    const rows: JSX.Element[] = []
    const seen = new Set(['id', 'type', 'uses', 'if', 'for_each', 'timeout', 'retry', 'continue_on_error', 'hooks', 'output_schema', 'decide', 'parallel', 'bot', 'call', 'workflow', 'with', 'use', 'command', 'run', 'code', 'sleep', 'wait_for', 'log', 'set', 'assert', 'fail', 'skill', 'isolation', '<<'])
    const F = (path: string, fd: FD) => {
      seen.add(path.split('.')[0])
      rows.push(fieldRow(loc, path, fd, WF.getPath(s, path), ro))
    }
    if (k === 'verb' || k === 'ask') {
      const d = WF.verbOf(ctx, s.uses)
      const [inst, verb] = String(s.uses).split('.')
      const c = ctx.conns.find((x) => x.name === inst)
      const accts = c ? ctx.conns.filter((x) => x.use === c.use) : []
      rows.push(
        <div class="acct-row">
          <div class="field">
            <label>Account <span class="opt">connection</span></label>
            <select class="select mono" disabled={!!ro} aria-label="Connected account" onChange={(ev) => mutate((v) => {
              const st = stepAt(v, loc)
              st.uses = ev.currentTarget.value + '.' + String(st.uses).split('.')[1]
            })}>
              {accts.length ? accts.map((a) => <option selected={a.name === inst} value={a.name}>{a.name} · {a.use}{a.unavailable ? ' (not installed)' : ''}</option>) : <option>{inst} · not connected</option>}
            </select>
            {accts.length > 1 ? <div class="hint">{accts.length} {c!.use} connections: pick which account acts.</div> : !c ? <div class="hint"><a class="link" href="#/connections">Add a {inst} connection</a> to run this.</div> : null}
          </div>
          <div class="field">
            <label>Action</label>
            <select class="select mono" disabled={!!ro} aria-label="Action" onChange={(ev) => mutate((v) => {
              const st = stepAt(v, loc)
              st.uses = String(st.uses).split('.')[0] + '.' + ev.currentTarget.value
              const nd = WF.verbOf(ctx, st.uses)
              if (isObj(st.options) && nd && !nd.verb.open) Object.keys(st.options).forEach((x) => !(nd.verb.options || []).find((f) => f.key === x) && delete (st.options as Obj)[x])
            })}>
              {c && c.verbs.length ? c.verbs.map((vd) => <option value={vd.name} selected={vd.name === verb}>{vd.name}{vd.desc ? ' · ' + vd.desc : ''}</option>) : <option>{verb}</option>}
            </select>
          </div>
        </div>,
      )
      ;(d?.verb.options || []).forEach((f) => F('options.' + f.key, { type: f.enum?.length ? 'enum' : f.type === 'bool' || f.type === 'boolean' ? 'bool' : f.type === 'list' || f.type === 'array' ? 'list' : f.type === 'map' || f.type === 'object' ? 'map' : f.type === 'int' || f.type === 'number' ? 'number' : 'string', options: f.enum, req: f.required, help: f.desc, label: f.key.replace(/_/g, ' ') }))
    } else if (k === 'agent') {
      rows.push(<div class="sc-sec">What it does</div>)
      F('prompt', { type: 'text', rows: 5, help: 'Optional: omit it and the agent gets the event. Insert data with “Insert from the event” or by typing {{.' })
      rows.push(<div class="sc-sec">Where it runs</div>)
      F('runtime', { type: 'enum', options: ctx.runtimes, def: ctx.defaultRuntime })
      F('model', { type: 'string', list: 'dl-models', help: 'A fleet (fixer, reviewer…), a model id, or a wildcard. Omit for a bare launch.' })
      const iso = isObj(s.isolation) ? s.isolation : null
      rows.push(<div class="sc-sec">Isolation <span>how the agent is fenced in</span></div>)
      F('isolation.mode', { type: 'seg', label: 'mode', options: [['', 'none set'], ['user', 'user'], ['namespace', 'namespace'], ['container', 'container']], help: "user: a low-privilege account · namespace: a mount/net namespace jail · container: an image. No block: the runtime's own isolation applies." })
      if (iso && iso.mode === 'user') F('isolation.user', { type: 'string', label: 'user', req: true, ph: 'conductor-agent' })
      if (iso && iso.mode === 'container') {
        F('isolation.container.image', { type: 'string', label: 'container image', req: true, ph: 'ghcr.io/acme/agents:latest' })
        F('isolation.container.engine', { type: 'enum', label: 'engine', options: ['docker', 'podman'] })
      }
      if (iso && iso.mode) {
        const eg = isObj(iso.network) && Array.isArray(iso.network.egress) && iso.network.egress.length
        F('isolation.network.egress', { type: 'list', label: 'egress allowlist', ph: 'api.github.com:443', help: "Only these hosts, through conductor's egress proxy. Adding a host widens." })
        F('isolation.network.deny', { type: 'bool', label: 'enforce it', on: eg ? (['namespace', 'container'].includes(String(iso.mode)) ? 'enforced: no network but the allowlist (deny: true)' : 'needs namespace or container to enforce') : 'no network at all (deny: true)' })
        F('isolation.fs', { type: 'list', label: 'extra paths (fs allowlist)', ph: '/srv/media' })
        F('isolation.limits.memory', { type: 'string', label: 'memory limit', ph: '2g' })
        F('isolation.limits.cpu', { type: 'string', label: 'cpu limit', ph: '200%' })
        F('isolation.limits.pids', { type: 'number', label: 'pids limit' })
        if (iso.mode === 'namespace') F('isolation.privileged', { type: 'bool', label: 'privileged', on: "see the daemon's own files (widening)" })
      }
      F('workspace', { type: 'enum', options: ['local', 'worktree'] })
      F('checkout', { type: 'enum', options: ['none', 'checkout-pr', 'branch-off'] })
      rows.push(grants(loc, s, ro))
      F('handoff', { type: 'enum', options: ctx.conns.filter((c) => c.verbs.some((x) => x.ask)).map((c) => c.name), help: 'Present a review loop on a connection that can ask people.' })
      F('skill.secrets_via', { type: 'enum', label: 'secrets via', options: [['none', 'none (the default)'], ['broker', 'broker: single-use grants, audited'], ['env', "env: the agent's environment"]], help: 'How this agent may obtain secrets. allow_secrets needs broker.' })
      F('skill.allow_secrets', { type: 'list', label: 'secrets (broker grants)', ph: 'vault/key', help: 'Exact <vault>/<key> entries the agent may request through the broker: single use, 60s, audited. Needs secrets via: broker.' })
      F('gate.run', { type: 'multi', label: 'quality gate', options: ctx.checks })
    } else if (k === 'code') {
      const eng = String(s.use || s.run || 'cli')
      rows.push(
        <div class="field">
          <label>Language</label>
          <select class="select mono" disabled={!!ro} aria-label="Language" onChange={(ev) => mutate((v) => {
            const st = stepAt(v, loc)
            const was = st.use || st.run
            delete st.run
            st.use = ev.currentTarget.value
            if (ev.currentTarget.value === 'cli') {
              delete st.code
              st.command = st.command || ['echo', 'hello']
            } else {
              delete st.command
              if (!st.code || was === 'cli') st.code = 'return { ok: true };\n'
            }
          })}>
            {[...new Set(ctx.engines.concat([eng]))].map((ek) => <option value={ek} selected={ek === eng}>{ek === 'cli' ? 'Command (argv)' : ek}</option>)}
          </select>
          <div class="hint">ctx carries the trigger’s fields and every earlier step’s outputs (ctx.logs.text). What you return or print becomes this step’s outputs.</div>
        </div>,
      )
      if (eng === 'cli' || s.command) F('command', { type: 'list', req: true, help: 'argv, one item per argument.' })
      else rows.push(fieldRow(loc, 'code', { type: 'code', req: true }, s.code, ro))
      F('env', { type: 'map', help: 'Pass a secret explicitly: {{ vault "house" "key" }}.' })
    } else if (k === 'decide') {
      F('model', { type: 'string', list: 'dl-models', help: 'The tier that answers.' })
      F('decide.state', { type: 'text', label: 'state', rows: 3, help: 'What the decision looks at.' })
      rows.push(questions(s, loc, ro))
    } else if (k === 'call') {
      const target = String(s.call || s.workflow || '')
      rows.push(
        <div class="field">
          <label>Workflow</label>
          <select class="select mono" disabled={!!ro} aria-label="Workflow to call" onChange={(ev) => mutate((v) => {
            const st = stepAt(v, loc)
            delete st.workflow
            st.call = ev.currentTarget.value
            st.with = {}
          })}>
            {ctx.workflows.map((w) => <option selected={w.name === target}>{w.name}</option>)}
            {target && !ctx.workflows.some((w) => w.name === target) ? <option selected>{target}</option> : null}
          </select>
        </div>,
      )
      const w = ctx.workflows.find((x) => x.name === target)
      Object.entries(w?.inputs || {}).forEach(([ik, d]) => F('with.' + ik, { type: d?.type === 'integer' ? 'number' : 'string', req: !!d?.required, def: d?.default, label: ik + ' (input)' }))
    } else if (k === 'wait') {
      const mode = s.wait_for ? 'wait_for' : 'sleep'
      rows.push(
        <div class="field">
          <label>Wait for</label>
          <div class="seg" role="group">
            {[['sleep', 'a duration'], ['wait_for', 'a condition']].map(([mv, ml]) => (
              <button type="button" {...off(ro)} aria-pressed={mv === mode} class={mv === mode ? 'on' : ''} onClick={() => !ro && mutate((v) => {
                const st = stepAt(v, loc)
                if (mv === 'sleep') {
                  delete st.wait_for
                  st.sleep = st.sleep || '30s'
                } else {
                  delete st.sleep
                  st.wait_for = { uses: '', until: '', every: '5s', timeout: '2m' }
                }
              })}>{ml}</button>
            ))}
          </div>
        </div>,
      )
      if (mode === 'sleep') F('sleep', { type: 'string', req: true, help: 'e.g. 30s, 5m.' })
      else {
        F('wait_for.uses', { type: 'enum', label: 'read verb', options: ctx.conns.flatMap((c) => c.verbs.map((x) => `${c.name}.${x.name}`)) })
        F('wait_for.until', { type: 'string', label: 'until', help: "Over the verb's outputs: status == 'completed'." })
        F('wait_for.every', { type: 'string', label: 'every' })
        F('wait_for.timeout', { type: 'string', label: 'timeout' })
      }
    } else if (k === 'bot') {
      const toTeam = isObj(s.bot) && !!s.bot.team
      rows.push(
        <div class="field">
          <label>Hand to</label>
          <div class="seg" role="group">
            {[['bot', 'a bot'], ['team', 'a team']].map(([mv, ml]) => (
              <button type="button" {...off(ro)} aria-pressed={(mv === 'team') === toTeam} class={(mv === 'team') === toTeam ? 'on' : ''} onClick={() => !ro && mutate((v) => {
                const st = stepAt(v, loc)
                const nb: Obj = { ...(isObj(st.bot) ? st.bot : {}) }
                delete nb.name
                delete nb.team
                if (mv === 'team') nb.team = ctx.teams[0]
                else nb.name = ctx.bots[0]
                st.bot = nb
              })}>{ml}</button>
            ))}
          </div>
          <div class="hint">{toTeam ? "The team's routing rules pick the bot; with no match its lead triages. The requester is this trigger's event." : 'Delivered to the bot directly, as conductor bot deliver.'}</div>
        </div>,
      )
      if (toTeam) F('bot.team', { type: 'enum', label: 'team', req: true, options: ctx.teams })
      else F('bot.name', { type: 'enum', label: 'bot', req: true, options: ctx.bots })
      F('bot.title', { type: 'string', label: 'title', help: 'Becomes the ticket’s title.' })
      F('bot.text', { type: 'text', label: 'text', rows: 3, help: 'The payload. It reaches the bot fenced as untrusted data, never as instructions.' })
      F('bot.requester', { type: 'string', label: 'requester', help: 'Who asked; leave empty for an alert with no human behind it.' })
      F('bot.urgent', { type: 'bool', label: 'urgent', on: "start a worker now, don't wait on the conversation" })
    } else if (k === 'log') F('log', { type: 'text', rows: 2 })
    else if (k === 'set') F('set', { type: 'map', label: 'values', help: 'Read later as {{.' + (s.id || 'this') + '.<key>}}.' })
    else if (k === 'assert') F('assert', { type: 'string', help: 'Same grammar as if:. The run fails unless it holds.' })
    else if (k === 'fail') F('fail', { type: 'text', rows: 2 })
    const adv: JSX.Element[] = []
    const A = (path: string, fd: FD) => adv.push(fieldRow(loc, path, fd, WF.getPath(s, path), ro))
    A('id', { type: 'string', ref: false, help: 'How later steps address its outputs. Renaming updates nothing else: fix references the validator flags.' })
    A('if', { type: 'string', help: 'Skip unless it holds: {{.triage.kind}} == test_failure' })
    A('for_each', { type: 'string', help: 'Run once per element: {{.item}} and {{.index}} in scope.' })
    A('timeout', { type: 'string' })
    A('retry.max', { type: 'number', label: 'retry · max', ref: false })
    A('continue_on_error', { type: 'bool', on: 'record the error and keep going' })
    if (k === 'agent' && isObj(s.output_schema)) {
      const props = Object.entries(isObj(s.output_schema.properties) ? s.output_schema.properties : {})
      rows.push(
        <div class="field">
          <label>output_schema <span class="opt">what the agent must answer with</span></label>
          <div class="small">
            {props.length ? <>returns {props.map(([n, d], i) => <>{i ? ', ' : ''}<span class="mono">{n}</span> <span class="muted">({String((isObj(d) && d.type) || 'any')})</span></>)}</> : <span class="mono">{JSON.stringify(s.output_schema).slice(0, 80)}</span>}
          </div>
          <div class="hint">Edit it with the keys below or in the YAML pane.</div>
        </div>,
      )
    }
    const extra = Object.keys(s).filter((x) => !seen.has(x))
    const xs = extra.concat(k === 'agent' && s.output_schema ? ['output_schema'] : [])
    return (
      <>
        {rows}
        {xs.length ? (
          <div class="yaml-only small">
            <span class="tag">+{xs.length} key{xs.length === 1 ? '' : 's'} only in YAML</span> <span class="mono muted">{xs.join(', ')}</span>
            {ro ? null : (
              <>
                {' '}
                <button type="button" class="link" onClick={() => setModal({ k: 'yamlsub', loc })}>Edit them here</button>
              </>
            )}
          </div>
        ) : null}
        <details class="adv" open={b.advOpen.has(loc)} onToggle={(ev) => advToggle(loc, (ev.currentTarget as HTMLDetailsElement).open)}>
          <summary>More options: id, if, for_each, timeout, retry</summary>
          <div class="adv-grid">{adv}</div>
        </details>
      </>
    )
  }
  function questions(s: Obj, loc: string, ro: string): JSX.Element {
    const q = (isObj(s.decide) && isObj(s.decide.questions) ? s.decide.questions : {}) as Record<string, Obj>
    const write = (fn: (q: Record<string, Obj>) => void) => mutate((v) => {
      const st = stepAt(v, loc)
      st.decide = isObj(st.decide) ? st.decide : {}
      const nq = clone(q)
      fn(nq)
      ;(st.decide as Obj).questions = nq
    }, { coalesce: true })
    return (
      <div class="field">
        <label>Questions</label>
        <div class="q-list">
          {Object.entries(q).map(([n, d]) => (
            <div class="q-row">
              <input class="input mono" value={n} aria-label="Question name" readOnly={!!ro} onChange={(ev) => {
                const nn = ev.currentTarget.value.trim()
                if (nn && nn !== n) write((nq) => {
                  nq[nn] = nq[n]
                  delete nq[n]
                })
              }} />
              <select class="select mono" aria-label="Answer type" disabled={!!ro} onChange={(ev) => write((nq) => {
                nq[n] = { ...nq[n], type: ev.currentTarget.value }
                if (ev.currentTarget.value === 'choice') nq[n].choices = nq[n].choices || []
                else delete nq[n].choices
              })}>
                {['noul', 'choice', 'score'].map((t) => <option selected={t === ((d && d.type) || 'noul')}>{t}</option>)}
              </select>
              <input class="input" value={String((d && d.instructions) || '')} placeholder="instructions" aria-label="Instructions" readOnly={!!ro} onInput={(ev) => write((nq) => void (nq[n] = { ...nq[n], instructions: ev.currentTarget.value }))} />
              {d && d.type === 'choice' ? <input class="input mono q-ch" value={(Array.isArray(d.choices) ? d.choices : []).join(', ')} placeholder="choice, choice" aria-label="Choices" readOnly={!!ro} onInput={(ev) => write((nq) => void (nq[n] = { ...nq[n], choices: ev.currentTarget.value.split(',').map((x) => x.trim()).filter(Boolean) }))} /> : null}
              <button type="button" class="icon-btn" {...off(ro)} aria-label="Remove question" onClick={() => !ro && write((nq) => void delete nq[n])}>
                <Icon name="trash" />
              </button>
            </div>
          ))}
        </div>
        <button type="button" class="btn sm" {...off(ro)} onClick={() => !ro && write((nq) => {
          let n = 'question'
          let i = 2
          while (nq[n]) n = 'question' + i++
          nq[n] = { type: 'noul', instructions: '' }
        })}>
          <Icon name="plus" />Add a question
        </button>
        <div class="hint">noul = yes/no with a probability · choice = one of a list · score = 0..1. Answers are outputs by question name.</div>
      </div>
    )
  }
  function grants(loc: string, s: Obj, ro: string): JSX.Element {
    const v = WF.getPath(s, 'skill.verbs')
    const isMap = isObj(v)
    const entries: [string, unknown][] = isMap ? Object.entries(v) : (Array.isArray(v) ? v : v ? [v] : []).map((x) => [String(x), null])
    const id = 'wf-' + loc.replace(/\./g, '_') + '-grants'
    const setG = (nv: unknown) => setField(loc, 'skill.verbs', nv)
    const all = ctx.conns.flatMap((c) => c.verbs.map((x) => `${c.name}.${x.name}`)).concat(ctx.conns.map((c) => c.name + '.*'))
    return (
      <div class="field wf-field" data-field="skill.verbs">
        <label>skill verbs (grant) <span class="opt">{isMap ? 'map form: scoped per verb' : 'list form'}</span></label>
        <div class="grants" id={id}>
          {entries.length ? (
            entries.map(([pat, scope]) => (
              <div class="grant">
                <div class="row">
                  <span class="mono small grow">{pat}</span>
                  {ro ? null : (
                    <button type="button" class="btn sm ghost" aria-label={`Remove grant ${pat}`} onClick={() => {
                      if (isMap) {
                        const n = { ...(v as Obj) }
                        delete n[pat]
                        setG(Object.keys(n).length ? n : undefined)
                      } else setG(entries.map(([p]) => p).filter((p) => p !== pat))
                    }}>
                      <Icon name="trash" />
                    </button>
                  )}
                </div>
                {isMap ? (
                  <div class="grant-scopes">
                    {Object.entries(isObj(scope) ? scope : {}).map(([sk, xs]) => <span class="tag removable"><span class="mono">{sk}: {([] as unknown[]).concat(xs).join(', ')}</span></span>)}
                    {!isObj(scope) || !Object.keys(scope).length ? <span class="muted tiny">no listed resources: limited to the dispatch’s own context (its repo, the channel its event came from)</span> : null}
                  </div>
                ) : null}
              </div>
            ))
          ) : (
            <div class="muted tiny">None: deny by default, the agent can call no verbs.</div>
          )}
        </div>
        {ro ? null : (
          <div class="row" style="gap:6px;margin-top:6px;flex-wrap:wrap">
            <select class="select mono sm" id={id + '-new'} aria-label="Add a verb grant">
              <option value="">+ grant a verb…</option>
              {all.filter((x) => !entries.some(([p]) => p === x)).map((x) => <option>{x}</option>)}
            </select>
            <button type="button" class="btn sm" onClick={() => {
              const sel = document.getElementById(id + '-new') as HTMLSelectElement
              if (!sel?.value) return
              if (isMap) setG({ ...(v as Obj), [sel.value]: {} })
              else setG(entries.map(([p]) => p).concat([sel.value]))
            }}>
              <Icon name="plus" />Grant
            </button>
            {isMap ? (
              <button type="button" class="btn sm ghost" {...off(Object.values(v as Obj).some((x) => isObj(x) && Object.keys(x).length) ? 'Some grants list resources: remove their scopes first' : '')} onClick={() => setG(Object.keys(v as Obj))}>
                Use the list form
              </button>
            ) : (
              <button type="button" class="btn sm ghost" onClick={() => setG(Object.fromEntries(entries.map(([p]) => [p, {}])))}>
                Scope per verb (map form)
              </button>
            )}
          </div>
        )}
        <div class="hint">Deny by default: the verbs this agent may call back into conductor. In the map form, a resource you list widens what a call may name; one you don’t list is limited to the dispatch’s own context.</div>
      </div>
    )
  }

  function badges(id: string): JSX.Element | null {
    const all = issuesFor(id)
    const iss = all.filter((i) => !i.todo)
    const td = all.filter((i) => i.todo)
    const er = iss.filter((i) => i.level === 'error').length
    const wr = iss.filter((i) => i.level === 'warn').length
    if (!er && !wr && td.length) return <span class="dl-sub" title={td.map((i) => i.todo).join(' · ')}>{td.length} to do</span>
    return er ? <StatusMark s="fail" word={`${er} error${er === 1 ? '' : 's'}`} tip={iss.find((i) => i.level === 'error')!.msg} sm /> : wr ? <StatusMark s="wait" word={`${wr} warning${wr === 1 ? '' : 's'}`} tip={iss[0].msg} sm /> : null
  }
  function toggle(id: string) {
    if (b.open.has(id)) b.open.delete(id)
    else b.open.add(id)
    if (id !== '$trigger') b.sel = id
    force(0)
  }
  function advToggle(k: string, open: boolean) {
    if (open) b.advOpen.add(k)
    else b.advOpen.delete(k)
  }
  const dnd = (id: string, ro: string) =>
    ro
      ? {}
      : {
          draggable: true,
          onDragStart: (e: DragEvent) => {
            if (!(e.target as HTMLElement).matches?.('[data-card]')) return
            dragId.current = id
            e.dataTransfer?.setData('text/plain', id)
            ;(e.currentTarget as HTMLElement).classList.add('dragging')
          },
          onDragEnd: () => {
            dragId.current = null
            document.querySelectorAll('.dragging, .drop-before, .drop-after').forEach((x) => x.classList.remove('dragging', 'drop-before', 'drop-after'))
          },
          onDragOver: (e: DragEvent) => {
            if (!dragId.current || dragId.current === id) return
            e.preventDefault()
            e.stopPropagation()
            const c = e.currentTarget as HTMLElement
            document.querySelectorAll('.drop-before, .drop-after').forEach((x) => x.classList.remove('drop-before', 'drop-after'))
            const r = c.getBoundingClientRect()
            c.classList.add(e.clientY < r.top + r.height / 2 ? 'drop-before' : 'drop-after')
          },
          onDrop: (e: DragEvent) => {
            const src = dragId.current
            if (!src) return
            e.preventDefault()
            e.stopPropagation()
            const after = (e.currentTarget as HTMLElement).classList.contains('drop-after')
            dragId.current = null
            mutate((v) => {
              const a = findLoc(v, src)
              const t = findLoc(v, id)
              if (!a || !t) return false
              const node = a.list[a.index] as Obj
              if (Array.isArray(node.parallel) && t.e.branch) {
                say("A parallel group can't go inside a branch", '', 'wait')
                return false
              }
              a.list.splice(a.index, 1)
              const t2 = findLoc(v, id)!
              t2.list.splice(t2.index + (after ? 1 : 0), 0, node)
              WF.walk(ctx, v).filter((x) => x.kind === 'parallel').forEach((p) => (p.step.parallel = (p.step.parallel as unknown[][]).filter((br) => br.length)))
            })
          },
        }

  function stepCard(e: Entry, loc: string, num: string | number): JSX.Element {
    if (e.kind === 'parallel') return parallelCard(e, loc, num)
    const s = e.step
    const kc = KC(e.kind)
    const id = e.id
    const open = b.open.has(id)
    const ro = readOnly()
    const iss = issuesFor(id)
    const er = iss.filter((i) => i.level === 'error' && !i.todo).length
    const list = WF.at(b.value, e.list) as unknown[]
    const first = e.index === 0
    const last = e.index === list.length - 1
    const general = iss.filter((i) => !i.field || !open)
    return (
      <div class={'step-card' + (open ? ' open' : '') + (b.sel === id ? ' sel' : '') + (er ? ' has-err' : '') + (b.owner === 'pack' ? ' pack-owned' : '')} id={'card-' + id} data-card={id} data-loc={loc} style={{ '--kc': kc.color } as JSX.CSSProperties} {...dnd(id, ro)}>
        <div class="sc-head" role="button" tabIndex={0} aria-expanded={open} aria-label={`Step ${num}: ${id}`} onClick={(ev) => !(ev.target as HTMLElement).closest('.sc-more') && toggle(id)} onKeyDown={(ev) => (ev.key === 'Enter' || ev.key === ' ') && ev.target === ev.currentTarget && (ev.preventDefault(), toggle(id))}>
          <span class="grip" aria-hidden={ro ? 'true' : undefined} data-tip={ro ? undefined : 'Drag to reorder · Alt+↑/↓'}><Icon name="more" /></span>
          <span class="sc-num">{num}</span>
          <div class="grow sc-text">
            <div class="sc-title">
              <b>{s.id ? String(s.id) : '(no id)'}</b>
              {s.if ? <span class="tag">if</span> : null}
              {s.for_each ? <span class="tag">for each</span> : null}
            </div>
            <div class="sc-sum" id={'sum-' + id}>{WF.summary(ctx, s)}</div>
          </div>
          <span class="sc-ty"><Icon name={kc.icon} />{kc.label}</span>
          <span class="sc-badges" id={'badge-' + id}>{badges(id)}</span>
          <button type="button" class="icon-btn sc-more" aria-label="Step actions" onClick={(ev) => stepMenu(ev.currentTarget, id)}>
            <Icon name="more" />
          </button>
          <span class="chev"><Icon name={open ? 'chevd' : 'chev'} /></span>
        </div>
        {open ? (
          <div class="sc-body">
            {general.filter((i) => !i.field).map((i) => (i.todo ? <div class="callout small todo">{i.todo}</div> : <div class={'callout small ' + (i.level === 'error' ? 'danger' : i.level === 'info' ? 'info' : 'warn')}>{i.msg}</div>))}
            <div class="sc-form">{formFor(e, loc, ro)}</div>
            <div class="sc-test">
              <button class="btn sm" type="button" {...off(!s.id ? 'Give this step an id to test it' : !L?.wf ? 'Save and apply it first: a test checks what’s live' : b.err ? 'Fix the YAML first' : '')} onClick={() => s.id && L?.wf && !b.err && void runTest()}>
                <Icon name="play" />Test{e.order ? ' up to here' : ''}
              </button>
              <span class="muted tiny">checks the live version resolves, end to end; nothing is dispatched</span>
            </div>
            <div class="sc-foot row">
              <button class="btn sm ghost" type="button" {...off(ro || (first ? 'Already first in its list' : ''))} onClick={() => !ro && !first && moveStep(id, -1)}><Icon name="chev" class="up" />Move up</button>
              <button class="btn sm ghost" type="button" {...off(ro || (last ? 'Already last in its list' : ''))} onClick={() => !ro && !last && moveStep(id, 1)}><Icon name="chev" class="down" />Move down</button>
              <button class="btn sm ghost" type="button" {...off(ro)} onClick={() => !ro && dupStep(id)}><Icon name="copy" />Duplicate</button>
              <button class="btn sm ghost danger-text" type="button" {...off(ro)} onClick={() => !ro && deleteStep(id)}><Icon name="trash" />Delete</button>
            </div>
          </div>
        ) : null}
      </div>
    )
  }
  function parallelCard(e: Entry, loc: string, num: string | number): JSX.Element {
    const s = e.step
    const ro = readOnly()
    const id = e.id
    const iss = issuesFor(id)
    const LT = 'ABCDEFGH'
    const br = s.parallel as unknown[][]
    const all = WF.walk(ctx, b.value)
    return (
      <div class={'par-card' + (b.sel === id ? ' sel' : '')} id={'card-' + id} data-card={id} data-loc={loc} {...dnd(id, ro)}>
        <div class="par-head">
          <span class="grip"><Icon name="more" /></span>
          <span class="sc-num">{num}</span>
          <span class="sc-ic par"><Icon name="workflows" /></span>
          <div class="grow"><b>Parallel</b> <span class="muted small">{br.length} branches · joined before the next step</span></div>
          {iss.map((i) => <StatusMark s={i.level === 'error' ? 'fail' : 'wait'} word="" cls={'tpill ' + (i.level === 'error' ? 'fail' : 'warn')} tip={i.msg} sm />)}
          <button type="button" class="icon-btn" aria-label="Parallel group actions" onClick={(ev) => stepMenu(ev.currentTarget, id)}><Icon name="more" /></button>
        </div>
        <div class="par-fork" aria-hidden="true"></div>
        <div class="par-lanes" style={{ '--n': br.length + (ro ? 0 : 1) } as JSX.CSSProperties}>
          {br.map((_, bi) => {
            const bloc = e.path.concat(['parallel', bi])
            const kids = all.filter((x) => x.branch && x.branch.parent.id === id && x.branch.b === bi)
            return (
              <div class="lane" data-lane={locKey(bloc)}>
                <div class="lane-head">
                  <span class="caps">branch {LT[bi] || bi + 1}</span>
                  <button type="button" class="icon-btn sm" {...off(ro || (br.length <= 2 ? 'A parallel group keeps at least two branches; delete the group instead' : ''))} aria-label={`Remove branch ${LT[bi]}`} onClick={() => !ro && br.length > 2 && mutate((v) => void (WF.at(v, e.path) as Obj as { parallel: unknown[] }).parallel.splice(bi, 1))}>
                    <Icon name="trash" />
                  </button>
                </div>
                {addSlot(bloc, 0, `Add a step at the top of branch ${LT[bi]}`)}
                {kids.map((x, j) => (
                  <>
                    {stepCard(x, locKey(x.path), `${num}${LT[bi].toLowerCase()}${j + 1}`)}
                    {addSlot(bloc, j + 1, `Add a step in branch ${LT[bi]} after ${x.id}`)}
                  </>
                ))}
              </div>
            )
          })}
          {ro ? null : (
            <div class="lane add-lane">
              <button type="button" class="btn sm" onClick={() => {
                let nid = ''
                mutate((v) => {
                  const g = WF.at(v, e.path) as { parallel: unknown[][] }
                  nid = uniqueId(v, 'branch-' + 'abcdefgh'[g.parallel.length])
                  g.parallel.push([{ id: nid, log: 'new branch' }])
                })
                if (nid) {
                  b.open.add(nid)
                  force(0)
                }
              }}>
                <Icon name="plus" />Add a branch
              </button>
            </div>
          )}
        </div>
        <div class="par-join" aria-hidden="true"><span class="caps">join</span></div>
      </div>
    )
  }
  function hooksLane(v: Obj, ro: string): JSX.Element {
    const hs = (Array.isArray(v.hooks) ? v.hooks : []) as Obj[]
    return (
      <div class="hooks-lane" id="hooks-lane">
        <div class="row" style="gap:8px">
          <span class="caps">Hooks</span>
          <span class="muted tiny">verb calls when the workflow starts, finishes, fails or is stopped</span>
        </div>
        {hs.length ? (
          hs.map((h, i) => (
            <div class="hook-row">
              <span class="tag" data-s={h.at === 'fail' ? 'fail' : h.at === 'done' ? 'ok' : 'idle'}>on {String(h.at || '?')}</span>
              <span class="mono small">{String(h.uses || '')}</span>
              {h.if ? <span class="tag" data-tip={String(h.if)}>if</span> : null}
              <span class="muted tiny grow ellipsis">{Object.entries(isObj(h.options) ? h.options : {}).map(([k2, x]) => `${k2}: ${typeof x === 'object' ? JSON.stringify(x) : x}`).join(' · ')}</span>
              {ro ? null : (
                <button type="button" class="icon-btn" aria-label={`Remove the ${h.at} hook`} onClick={() => mutate((nv) => {
                  ;(nv.hooks as unknown[]).splice(i, 1)
                  if (!(nv.hooks as unknown[]).length) delete nv.hooks
                })}>
                  <Icon name="trash" />
                </button>
              )}
            </div>
          ))
        ) : (
          <div class="muted tiny">none</div>
        )}
        {ro ? null : (
          <div class="row wrap hook-adds" style="gap:6px">
            {['start', 'done', 'fail', 'stop'].map((at) => (
              <button type="button" class="btn sm ghost" {...off(ctx.conns.some((c) => c.verbs.length) ? '' : 'No connection declares a verb to call yet')} onClick={() => ctx.conns.some((c) => c.verbs.length) && openHookPicker(at)}>
                <Icon name="plus" />on {at}
              </button>
            ))}
          </div>
        )}
      </div>
    )
  }
  function listInner(): JSX.Element {
    const v = b.value || {}
    if (b.err && !b.value) return <div class="callout danger">{b.err.msg}</div>
    const steps = Array.isArray(v.steps) ? v.steps : []
    const top = WF.walk(ctx, v).filter((e) => !e.branch)
    const ro = readOnly()
    return (
      <>
        {b.err ? <div class="callout danger small sticky-err"><Icon name="warn" /> YAML error at line {b.err.line}: {b.err.msg}. The list shows the last valid version.</div> : null}
        {ro ? (
          <div class={'callout small ' + (b.owner === 'pack' ? 'info' : 'warn')}>
            {b.owner === 'pack' ? (
              <>
                <b>Pack-owned.</b> Its model, runtime and other settings are changed as overrides on <a class="link" href={'#/packs/' + encodeURIComponent(b.pack)}>the pack’s page</a>; the pack’s file is never touched and an update keeps them. To add steps, call <span class="mono">{b.pack}/{String(v.name || b.name)}</span> from your own trigger.
              </>
            ) : (
              <>
                <b>Read-only here.</b> {ro}.
              </>
            )}
          </div>
        ) : null}
        <div class="staff">
          {triggerCard()}
          {addSlot(['steps'], 0, 'Add a step after the trigger')}
          {top.map((e, i) => (
            <>
              {stepCard(e, locKey(e.path), i + 1)}
              {addSlot(['steps'], e.index + 1, `Add a step after ${e.id}`)}
            </>
          ))}
          {hooksLane(v, ro)}
          {!steps.length ? <div class="staff-end muted small">{ro ? '' : 'Add the first step with +'}</div> : <div class="staff-end caps">end</div>}
        </div>
        <datalist id="dl-models">{[...new Set(collectModels(tree))].map((m) => <option value={m} />)}</datalist>
      </>
    )
  }

  /* ---------- side: YAML + data ---------- */
  function hlLines(): number[] {
    const e = b.sel ? entryById(b.sel) : null
    const r = e ? b.lines.get(locKey(e.path)) : null
    if (!r) return []
    const out: number[] = []
    for (let n = r[0]; n <= r[1]; n++) out.push(n)
    return out
  }
  function sideHTML(): JSX.Element {
    let body: JSX.Element
    if (b.side === 'data') {
      const e = b.sel ? entryById(b.sel) : null
      const groups = WF.scope(ctx, b.value || {}, e || null, currentEventFields(b))
      body = (
        <div class="data-pane">
          <div class="small muted" style="margin-bottom:8px">
            Data <b>{e ? e.id : 'the first step'}</b> can read. {b.ev ? 'The event shows the fields the chosen run carried.' : 'Pick a test event under Try it to see real values.'}
          </div>
          {groups.map((g) => (
            <div class="dgroup">
              <div class="dg-head">
                <Icon name={g.icon} />
                <b class="mono">{g.label}</b>
                <span class="mono tiny muted ellipsis">{g.sub}</span>
                {g.tested ? <StatusMark s="ok" word="from a real run" cls="tpill ok" sm /> : g.unknown ? <StatusMark s="idle" word="known after a run" cls="tpill idle" sm /> : <StatusMark s="idle" word="declared" cls="tpill idle" sm />}
              </div>
              <Tree o={g.data} base={g.key} depth={0} />
            </div>
          ))}
        </div>
      )
    } else {
      const ro = b.owner !== 'config'
      body = (
        <div class="yaml-wrap">
          <CodeEditor
            id="wf-yaml"
            text={b.text}
            readOnly={ro}
            minRows={24}
            label="Workflow YAML"
            errLines={b.issues.filter((i) => i.level === 'error').map((i) => i.line || 1)}
            hlLines={hlLines()}
            onChange={(t) => commit(t, { coalesce: true })}
          />
        </div>
      )
    }
    const where = b.owner === 'config' ? `config.yaml · ${b.kind === 'workflow' ? `workflows.${b.name}` : `triggers[name=${b.name}]`}` : b.owner === 'pack' ? 'the pack ' + b.pack : 'read-only'
    return (
      <>
        <div class="side-head">
          <div class="seg" role="group">
            {(
              [
                ['yaml', 'code', 'YAML'],
                ['data', 'grid', 'Data'],
              ] as const
            ).map(([k, ic, l]) => (
              <button type="button" aria-pressed={b.side === k} class={b.side === k ? 'on' : ''} onClick={() => ((b.side = k), force(0))}>
                <Icon name={ic} />
                {l}
              </button>
            ))}
          </div>
          <span class="muted tiny" style="margin-left:auto"><Icon name="code" /> YAML is canonical · {where}</span>
        </div>
        <div class="side-body">{body}</div>
        <div class="side-foot" id="wf-issues">{issuesHTML()}</div>
      </>
    )
  }
  function issuesHTML(): JSX.Element {
    const td = b.issues.filter((i) => i.todo)
    const e = b.issues.filter((i) => i.level === 'error' && !i.todo)
    const w = b.issues.filter((i) => i.level === 'warn')
    if (!b.issues.length)
      return (
        <>
          <StatusMark s="success" word="valid" cls="val-pill" sm />
          <span class="mono tiny muted">checked on a copy · {status() === 'draft' ? 'unsaved edits' : 'no unsaved edits'}</span>
        </>
      )
    return (
      <div class="issues">
        <div>
          {td.length ? <span class="val-pill todo">{td.length} to do</span> : null}
          {e.length ? <StatusMark s="fail" word={`${e.length} error${e.length > 1 ? 's' : ''}`} cls="val-pill" sm /> : null}
          {w.length ? <StatusMark s="wait" word={`${w.length} warning${w.length > 1 ? 's' : ''}`} cls="val-pill" sm /> : null}
        </div>
        {b.issues.slice(0, 6).map((i) => (
          <button type="button" class={'issue ' + (i.todo ? 'todo' : i.level)} onClick={() => {
            if (i.step) {
              b.sel = i.step
              b.open.add(i.step)
            } else if (i.trigger) b.open.add('$trigger')
            b.side = 'yaml'
            force(0)
            setTimeout(() => (i.step ? document.getElementById('card-' + i.step) : null)?.scrollIntoView({ block: 'center' }), 30)
          }}>
            {i.todo ? (
              <>
                <Icon name="dot" />
                {i.todo}
              </>
            ) : (
              <>
                <span class="mono tiny">L{i.line}</span>
                {i.msg}
              </>
            )}
          </button>
        ))}
      </div>
    )
  }

  /* the side of the recipe: Try it, At a glance, and the config file */
  function tryCard(): JSX.Element {
    const evs = (L?.info?.events || []).concat(b.pasted)
    const srcs = WF.sources(b.value || {})
    return (
      <section class="dl-card" id="wfb-try">
        <header class="dl-ch"><h2>Try it</h2></header>
        <p class="dl-sub" style="margin:0 0 12px">Check every step against a recent event. Nothing is dispatched: a test only checks that it resolves.</p>
        <label class="dl-search dl-select">
          <Icon name="pr" />
          <select aria-label="Test event" onChange={(ev) => {
            const val = ev.currentTarget.value
            if (val === '__paste__') {
              ev.currentTarget.value = b.ev
              setModal({ k: 'paste' })
              return
            }
            b.ev = val
            parse(b)
            force(0)
          }}>
            <option value="">Choose an event…</option>
            {evs.map((x) => (
              <option value={x.id} selected={x.id === b.ev}>
                {x.label}
                {x.at ? ` · ${ago(x.at)}` : ''}
              </option>
            ))}
            {srcs.filter((so) => so.on !== 'manual' && !evs.some((x) => x.on === so.on)).map((so) => <option disabled>no {so.on} events in the last 14 days</option>)}
            <option value="__paste__">Paste a payload…</option>
          </select>
        </label>
        <button class="btn secondary wide" type="button" {...off(!b.ev ? 'Choose an event first' : !L?.wf ? 'Save and apply it first: a test checks what’s live' : testing ? 'Testing…' : '')} onClick={() => b.ev && L?.wf && !testing && void runTest()}>
          <Icon name="play" />Run a test
        </button>
        {test ? (
          <div class={'callout small ' + (test.ok ? 'info' : 'warn')} style="margin-top:10px" role="status">
            <StatusMark s={test.ok ? 'ok' : 'wait'} mkOnly /> {test.message}
          </div>
        ) : null}
      </section>
    )
  }
  function trySide(): JSX.Element {
    const v = b.value || {}
    const runs = L?.wf?.runs || []
    const budget = WF.getPath(v, 'policy.budget.max_cost_usd')
    const facts: [string, string][] = [
      ...(budget != null ? [['Budget', `Up to $${Number(budget).toFixed(2)} per run`] as [string, string]] : []),
      ['Recent runs', runs.length ? `${runs.length}: ${runParts(runs)}` : 'none yet'],
      ['Owner', b.owner === 'pack' ? `the pack ${b.pack}` : b.owner === 'bot' ? 'conductor (for the bot)' : 'your config'],
    ]
    return (
      <>
        {tryCard()}
        <section class="dl-card">
          <header class="dl-ch"><h2>At a glance</h2></header>
          <dl class="dl-facts">
            {facts.map(([k, x]) => (
              <>
                <dt>{k}</dt>
                <dd>{x}</dd>
              </>
            ))}
          </dl>
        </section>
        <section class="dl-card wfb-side-card">
          <header class="dl-ch">
            <h2>Config file</h2>
            <span class="dl-sp"></span>
            <a class="dl-link" href="#" onClick={(e) => (e.preventDefault(), (b.tab = 'config'), force(0))}>Open full size</a>
          </header>
          <aside class="wfb-side" id="wfb-side">{sideHTML()}</aside>
        </section>
      </>
    )
  }
  function tabBody(): JSX.Element {
    if (b.tab === 'config')
      return (
        <section class="dl-card wfb-config">
          <aside class="wfb-side" id="wfb-side">{sideHTML()}</aside>
        </section>
      )
    if (b.tab === 'runs') {
      const mins = rangeMinutes(since)
      const cutoff = mins ? Date.now() - mins * 60000 : 0
      const rs = (L?.wf?.runs || []).filter((r) => !cutoff || new Date(r.started).getTime() >= cutoff).slice(0, 12)
      const rangeLabel = TIME_RANGES.find(([k]) => k === since)?.[1] || 'Last 7 days'
      return (
        <>
          <FilterBar q="" onQueryChange={() => {}} noSearch showTime sinceDef="7d" since={since} onSinceChange={setSince} ariaLabel="Time range" />
          <section class="dl-card">
            {rs.length ? (
              <ul class="dl-list">
                {rs.map((r) => {
                  const m = statusMark(r.status)
                  return (
                    <li>
                      <a class="dl-row-a" href={'#/runs/' + encodeURIComponent(r.id)}>
                        <span class="dl-ng"><StatusMark s={m.s} lg mkOnly label={m.word} /></span>
                        <span class="dl-nt">
                          <b>
                            {runTitle(r)} <span class="tgt">{r.repo ? `${r.repo}${r.number ? '#' + r.number : ''}` : ''}</span>
                          </b>
                          <span class="dl-why">{m.word} · <span class="mono" title={r.id}>{shortRunId(r.id)}</span></span>
                        </span>
                        <span class="dl-age">{ago(r.started)}</span>
                      </a>
                    </li>
                  )
                })}
              </ul>
            ) : (
              <p class="dl-sub">No runs {since === 'all' ? 'yet' : `in the ${rangeLabel.toLowerCase()}`}.</p>
            )}
            <p class="dl-foot-note">
              <a class="dl-link" href={`#/activity?wf=${encodeURIComponent(b.name)}&since=${since}`}>Every run of {b.name} in Activity →</a>
            </p>
          </section>
        </>
      )
    }
    if (b.tab === 'settings')
      return (
        <section class="dl-card">
          <header class="dl-ch"><h2>Settings</h2></header>
          <div id="wfb-list" class="wfb-settings">{triggerCard()}</div>
          <p class="dl-sub">
            Budgets, isolation and the rest of the workflow’s keys are in the{' '}
            <a class="dl-link" href="#" onClick={(e) => (e.preventDefault(), (b.tab = 'config'), force(0))}>config file</a>.
          </p>
        </section>
      )
    let list: JSX.Element
    try {
      list = listInner()
    } catch (e) {
      list = <div class="callout danger small sticky-err"><Icon name="warn" /> The list can’t show this YAML ({e instanceof Error ? e.message : String(e)}). The YAML pane is canonical.</div>
    }
    return (
      <div class="dl-cols">
        <div class="dl-col-main">
          <section class="dl-card dl-recipe">
            <div class="wfb-list" id="wfb-list">{list}</div>
          </section>
        </div>
        <div class="dl-col-side">{trySide()}</div>
      </div>
    )
  }

  const stagedHere = staged.value.length > 0
  return (
    <div class="wfb-page" data-wf-detail={name}>
      <div class="wfb pane-steps" id="wfb">
        <div class="wfb-bar" id="wfb-bar">{bar()}</div>
        <div class="wfb-tab" id="wfb-tab">{tabBody()}</div>
      </div>
      <div ref={reviewRef}>{stagedHere || review.value || applyResult.value ? <ReviewPanel stagedCount={staged.value.length} /> : null}</div>

      {createPortal(
        <>
      {menu ? <Menu anchor={menu.anchor} items={menu.items} onClose={() => setMenu(null)} /> : null}
      {ac ? (
        <div class="ac" role="listbox" style={{ left: ac.x + 'px', top: ac.y + 'px', minWidth: ac.w + 'px' }}>
          {ac.paths.map((p, i) => (
            <button type="button" role="option" class={i === ac.sel ? 'on' : ''} onMouseDown={(e) => (e.preventDefault(), insertRef(ac.target, p.path))}>
              <span class="mono">{p.path}</span>
              <span class="muted mono tiny ellipsis">{typeof p.value === 'object' ? JSON.stringify(p.value).slice(0, 40) : String(p.value).slice(0, 40)}</span>
            </button>
          ))}
        </div>
      ) : null}
      {modal?.k === 'picker' ? <Picker {...modal} onClose={() => setModal(null)} /> : null}
      {modal?.k === 'ref' ? (
        <RefPicker
          groups={scopeFor(modal.loc)}
          code={modal.code}
          onClose={() => setModal(null)}
          onPick={(p) => {
            setModal(null)
            setTimeout(() => insertRef(modal.target, p), 20)
          }}
        />
      ) : null}
      {modal?.k === 'yamlsub' ? (() => {
        const e = entryAt(modal.loc)
        const s = e?.step || {}
        const known = new Set(['id', 'type', 'uses', 'if', 'for_each', 'timeout', 'retry', 'continue_on_error', 'prompt', 'runtime', 'model', 'isolation', 'skill', 'options', 'hooks', 'decide', 'parallel', 'bot', 'call', 'workflow', 'with', 'use', 'command', 'run', 'code', 'sleep', 'wait_for', 'log', 'set', 'assert', 'fail', 'workspace', 'checkout', 'handoff', 'gate', 'env'])
        const sub = Object.fromEntries(Object.entries(s).filter(([k2]) => !known.has(k2) || k2 === 'output_schema'))
        return (
          <FormModal
            caps={`${s.id || 'step'} · keys only in YAML`}
            title={`More keys on ${s.id || 'this step'}`}
            intro="Keys the form doesn't model yet, kept exactly as written. The validator checks them like any other."
            values={{ y: dump(sub) }}
            fields={[{ k: 'y', type: 'text', rows: 10, mono: true, label: 'YAML', validate: (t) => {
              const d = parseDocument(t || '')
              if (d.errors.length) return 'Not valid YAML: ' + d.errors[0].message.split('\n')[0]
              const x = d.toJS()
              return x == null || isObj(x) ? '' : 'A map of keys'
            } }]}
            submit="Apply to the step"
            submitIcon="check"
            onClose={() => setModal(null)}
            onSubmit={(vals) => {
              const nv = (parseDocument(vals.y || '').toJS() || {}) as Obj
              mutate((root) => {
                const st = stepAt(root, modal.loc)
                Object.keys(sub).forEach((k2) => delete st[k2])
                Object.assign(st, nv)
              })
            }}
          />
        )
      })() : null}
      {modal?.k === 'paste' ? (
        <FormModal
          caps="test event"
          title={`Paste a ${WF.sources(b.value || {})[0]?.on || 'manual'} payload`}
          intro="The data pane and the reference picker show its fields. It stays in this browser; nothing is sent."
          values={{ json: '{\n  \n}' }}
          fields={[{ k: 'json', type: 'text', rows: 12, req: true, mono: true, label: 'Payload (JSON)', validate: (t) => {
            try {
              const x = JSON.parse(t)
              return x && typeof x === 'object' ? '' : 'An object'
            } catch (e) {
              return 'Not valid JSON: ' + (e instanceof Error ? e.message : String(e))
            }
          } }]}
          submit="Use this payload"
          submitIcon="play"
          onClose={() => setModal(null)}
          onSubmit={(vals) => {
            const on = WF.sources(b.value || {})[0]?.on || 'manual'
            const id = 'paste-' + Date.now().toString(36)
            b.pasted.push({ id, on, at: '', label: `pasted ${on} payload`, fields: JSON.parse(vals.json) })
            b.ev = id
            parse(b)
            force(0)
            say('Test event set', 'the data pane now shows the pasted fields', 'ok')
          }}
        />
      ) : null}
      {toast ? (
        <div class="toasts" aria-live="polite">
          <div class="toast" data-s={toast.s} role="status">
            <b>{toast.title}</b>
            {toast.sub ? <span class="muted small">{toast.sub}</span> : null}
          </div>
        </div>
      ) : null}
        </>,
        document.body,
      )}
    </div>
  )
}

// A run in a workflow's own list says what happened, not the workflow's name
// again (the list is already this workflow's): the run summary carries no
// title of its own, so it's built from its outcome.
function runTitle(r: { status: string; steps: number; duration_ms: number; failed_step?: string; error?: string }): string {
  const n = `${r.steps} step${r.steps === 1 ? '' : 's'}`
  if (r.status === 'failed') return r.failed_step ? `Stopped at “${r.failed_step}”` : 'Stopped before it finished'
  if (r.status === 'running') return `Running, ${n} so far`
  if (r.status === 'ok') return `Ran ${n} in ${r.duration_ms < 1000 ? 'under 1 s' : humanDuration(r.duration_ms)}`
  if (r.status === 'retried') return 'Retried'
  return n
}
// The short form of a run id the prototype shows (r-7f3a): the id's own
// random tail, enough to tell the runs in one list apart. The full id is the
// row's link and its tooltip.
function shortRunId(id: string): string {
  const tail = id.split('-').pop() || id
  return 'r-' + tail.slice(0, 6)
}

function collectModels(tree: Obj): string[] {
  const out: string[] = []
  const models = tree.models
  if (isObj(models)) Object.keys(models).forEach((k) => out.push(k))
  return out
}

function Tree({ o, base, depth }: { o: unknown; base: string; depth: number }): JSX.Element {
  if (!isObj(o) && !Array.isArray(o))
    return (
      <div class="dtree">
        <div class="dleaf"><span class="dv mono">{JSON.stringify(o)}</span></div>
      </div>
    )
  const ent = Array.isArray(o) ? o.map((v, i) => [String(i), v] as [string, unknown]) : Object.entries(o)
  if (!ent.length) return <div class="muted tiny" style="padding:4px 0">{Array.isArray(o) ? '[ ]' : '{ } · no outputs'}</div>
  return (
    <div class="dtree">
      {ent.map(([k, v]) => {
        const p = base ? `${base}.${k}` : k
        const kids = (isObj(v) || Array.isArray(v)) && depth < 3
        const prev = typeof v === 'string' ? `"${v.length > 70 ? v.slice(0, 69) + '…' : v}"` : isObj(v) ? `{${Object.keys(v).length}}` : Array.isArray(v) ? `[${v.length}]` : String(v)
        const leaf = (
          <div class="dleaf">
            <span class="dk mono">{k}</span>
            <span class="dv mono">{prev}</span>
          </div>
        )
        return kids ? (
          <div class="dnode">
            {leaf}
            <div class="dkids"><Tree o={v} base={p} depth={depth + 1} /></div>
          </div>
        ) : (
          leaf
        )
      })}
    </div>
  )
}

function ModalShell({ caps, title, wide, onClose, children }: { caps: string; title: string; wide?: boolean; onClose: () => void; children: ComponentChildren }): JSX.Element {
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('keydown', k)
    return () => document.removeEventListener('keydown', k)
  }, [])
  return (
    <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div class={'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true" tabIndex={-1}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            <div class="caps">{caps}</div>
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        {children}
      </div>
    </div>
  )
}

const RECENT = 'cd-wf-recent-steps'
const RISK_TAG = { read: 'reads only', change: 'changes things', high: 'high impact' }
function Picker({ caps, title, ph, items, onPick, onClose }: { caps: string; title: string; ph?: string; items: PickItem[]; onPick: (x: PickItem) => void; onClose: () => void }): JSX.Element {
  const [q, setQ] = useState('')
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    setTimeout(() => ref.current?.focus(), 30)
  }, [])
  let recentKeys: string[] = []
  try {
    recentKeys = JSON.parse(localStorage.getItem(RECENT) || '[]')
  } catch {
    /* none */
  }
  const recent = recentKeys.map((k) => items.find((x) => x.key === k)).filter(Boolean).slice(0, 5) as PickItem[]
  const qq = q.toLowerCase().trim()
  const match = items.filter((x) => !qq || `${x.label} ${x.sub} ${x.group} ${x.key}`.toLowerCase().includes(qq))
  const groups: Record<string, PickItem[]> = {}
  match.forEach((x) => (groups[x.group] = groups[x.group] || []).push(x))
  const pick = (x: PickItem) => {
    if (x.dis) return
    try {
      localStorage.setItem(RECENT, JSON.stringify([x.key].concat(recentKeys.filter((k) => k !== x.key)).slice(0, 8)))
    } catch {
      /* fine */
    }
    onClose()
    onPick(x)
  }
  const sect = (t: string, list: PickItem[]) => (
    <div class="pk-group">
      <div class="caps">{t}</div>
      <div class="pk-grid">
        {list.map((x) => (
          <button type="button" class="pk-item" role="option" {...off(x.dis)} onClick={() => pick(x)}>
            <span class="sc-ic sm"><Icon name={x.icon} /></span>
            <span class="grow">
              <b>{x.label}</b>
              {x.risk ? <span class={'pk-risk' + (x.risk === 'high' ? ' high' : '')}>{RISK_TAG[x.risk]}</span> : null}
              <span class={'pk-sub' + (x.mono ? ' mono' : '')}>{x.dis || x.sub}</span>
            </span>
          </button>
        ))}
      </div>
    </div>
  )
  return (
    <ModalShell caps={caps} title={title} wide onClose={onClose}>
      <div class="modal-body picker">
        <div class="row" style="gap:8px;margin-bottom:12px">
          <div class="input-icon grow" style="margin:0">
            <Icon name="search" />
            <input ref={ref} class="input" placeholder={ph || 'Search: comment, slack, agent, code, approval…'} autocomplete="off" aria-label="Search steps" value={q} onInput={(e) => setQ(e.currentTarget.value)} onKeyDown={(e) => {
              if (e.key === 'Enter') {
                const f = match.find((x) => !x.dis)
                if (f) pick(f)
              }
            }} />
          </div>
          <a class="btn sm" href="#/connections"><Icon name="plus" />New connector</a>
        </div>
        <div class="pk-list" role="listbox">
          {!qq && recent.length ? sect('Recently used', recent) : null}
          {Object.keys(groups).length ? Object.entries(groups).map(([g, l]) => sect(g, l)) : <div class="empty small">Nothing matches “{q}”.</div>}
        </div>
      </div>
    </ModalShell>
  )
}

function RefPicker({ groups, code, onPick, onClose }: { groups: WF.ScopeGroup[]; code: boolean; onPick: (p: string) => void; onClose: () => void }): JSX.Element {
  const [q, setQ] = useState('')
  const leaf = (o: unknown, base: string, depth: number): JSX.Element => {
    if (!isObj(o) && !Array.isArray(o)) return <></>
    const ent = Array.isArray(o) ? o.map((v, i) => [String(i), v] as [string, unknown]) : Object.entries(o)
    if (!ent.length) return <div class="muted tiny" style="padding:4px 0">{'{ } · no outputs'}</div>
    return (
      <div class="dtree">
        {ent.map(([k, v]) => {
          const p = base ? `${base}.${k}` : k
          const kids = (isObj(v) || Array.isArray(v)) && depth < 3
          const prev = typeof v === 'string' ? `"${v.length > 70 ? v.slice(0, 69) + '…' : v}"` : isObj(v) ? `{${Object.keys(v).length}}` : Array.isArray(v) ? `[${v.length}]` : String(v)
          const btn = (
            <button type="button" class={'dleaf ins' + (q && !p.toLowerCase().includes(q.toLowerCase()) ? ' hidden' : '')} title={`Insert {{.${p}}}`} onClick={() => onPick(p)}>
              <span class="dk mono">{k}</span>
              <span class="dv mono">{prev}</span>
              <span class="dins"><Icon name="plus" /></span>
            </button>
          )
          return kids ? (
            <div class="dnode">
              {btn}
              <div class="dkids">{leaf(v, p, depth + 1)}</div>
            </div>
          ) : (
            btn
          )
        })}
      </div>
    )
  }
  return (
    <ModalShell caps={`insert a reference · ${code ? 'ctx.<path>' : '{{.path}}'}`} title="Data from earlier" wide onClose={onClose}>
      <div class="modal-body">
        <div class="input-icon" style="margin-bottom:10px">
          <Icon name="search" />
          <input class="input" placeholder="filter: repo, logs.text…" aria-label="Filter data" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
        </div>
        <div id="rf-tree">
          {groups.length ? (
            groups.map((g) => (
              <div class="dgroup">
                <div class="dg-head">
                  <Icon name={g.icon} />
                  <b class="mono">{g.label}</b>
                  <span class="mono tiny muted ellipsis">{g.sub}</span>
                  {g.tested ? <StatusMark s="ok" word="actual" cls="tpill ok" sm /> : g.unknown ? <StatusMark s="idle" word="known after a run" cls="tpill idle" sm /> : <StatusMark s="idle" word="declared" cls="tpill idle" sm />}
                </div>
                {g.key ? (
                  <button type="button" class="dleaf ins whole" onClick={() => onPick(g.key)}>
                    <span class="dk mono">{g.key}</span>
                    <span class="dv mono">the whole output</span>
                    <span class="dins"><Icon name="plus" /></span>
                  </button>
                ) : null}
                {leaf(g.data, g.key, 0)}
              </div>
            ))
          ) : (
            <div class="empty small">Nothing earlier to read yet: choose a trigger, or add a step above this one.</div>
          )}
        </div>
      </div>
    </ModalShell>
  )
}

function FilterAdd({ keys, ro, allowed, onAdd }: { keys: string[]; ro: string; allowed: boolean; onAdd: (k: string, v: string) => void }): JSX.Element {
  const [k, setK] = useState(keys[0] || '')
  const [val, setVal] = useState('')
  const key = keys.includes(k) ? k : keys[0] || ''
  return (
    <div class="flt-row">
      <select class="select mono" aria-label="New filter key" disabled={!!ro} onChange={(e) => setK(e.currentTarget.value)}>
        {keys.map((x) => <option selected={x === key}>{x}</option>)}
      </select>
      <input class="input mono" placeholder={/expr$/.test(key) ? "!is_draft && !contains(title, 'Release')" : 'value, value'} aria-label="New filter value" readOnly={!!ro} value={val} onInput={(e) => setVal(e.currentTarget.value)} />
      <button class="btn sm" type="button" {...off(ro || (allowed ? '' : 'This event declares no filters'))} onClick={() => {
        if (ro || !allowed || !key || !val.trim()) return
        onAdd(key, val.trim())
        setVal('')
      }}>
        <Icon name="plus" />Add
      </button>
    </div>
  )
}

function InputAdd({ ro, onAdd }: { ro: string; onAdd: (n: string, type: string, req: boolean) => void }): JSX.Element {
  const [n, setN] = useState('')
  const [t, setT] = useState('string')
  const [req, setReq] = useState(false)
  return (
    <div class="flt-row">
      <input class="input mono" placeholder="name" aria-label="Input name" value={n} onInput={(e) => setN(e.currentTarget.value)} />
      <select class="select mono" aria-label="Input type" onChange={(e) => setT(e.currentTarget.value)}>
        {['string', 'integer', 'boolean'].map((x) => <option selected={x === t}>{x}</option>)}
      </select>
      <label class="row tiny"><input type="checkbox" class="cbx" checked={req} onChange={(e) => setReq(e.currentTarget.checked)} />required</label>
      <button class="btn sm" type="button" {...off(ro)} onClick={() => {
        if (ro || !/^[a-z_][\w]*$/.test(n.trim())) return
        onAdd(n.trim(), t, req)
        setN('')
      }}>
        <Icon name="plus" />Add input
      </button>
    </div>
  )
}

function Missing({ name }: { name: string }): JSX.Element {
  return (
    <div class="wf-hero-page">
      <EmptyState hero icon="workflows" testid="workflow-missing" title="No such workflow" action={{ label: 'All workflows', href: '#/workflows', testid: 'workflow-missing-all' }}>
        <p>
          There’s no workflow named <b class="wf-hero-name">{name}</b> in this config: it may have been renamed or removed.
        </p>
      </EmptyState>
    </div>
  )
}

// go is re-exported for the list's "Open in the builder" (kept here so the
// builder owns its own route shape).
export const openBuilder = (name: string) => go('workflows/' + encodeURIComponent(name))
