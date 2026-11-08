// A memory or knowledge repo browser — a bot's Memory tab ("bot:<name>") and a
// team's Knowledge tab ("team:<name>") — ported line for line from the
// prototype's UI.repoView (js/screens/repo.js): for a team, "Suggested by
// bots" (bulk select, Approve / Deny selected, each proposal's diff, Approve /
// Approve with changes / Deny, schema-violation callouts); then the two panes:
// the rail (search, By kind / Files, the kind tree with counts, History with
// diff and revert) and the entry (its kind's fields, the markdown body, who
// reads it, what cites it, Who wrote this, the commit bar). On a phone it shows
// the list or the entry, never both.
//
// Everything is the daemon's: kinds and their schemas, files, history, blame,
// diffs (internal/bot/httpapi_repo.go). Every save is one commit in the
// signed-in operator's name against the head it read; a commit that landed in
// between comes back as a conflict to resolve, never an overwrite. A team's
// proposals are decided behind the approval step-up, an edited approval bound
// to the exact text (internal/dashboard/approve_edit.go).
//
// Left out on purpose: the prototype's "Simulate a bot commit" (a demo
// control) and file attachments in the markdown body (memory entries are
// text). Pinning has no engine: conductor lists every entry in a bot's index.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { authState, stepupMethods } from '../api/auth'

// usesPasskey: the passkey glyph only when a passkey is what confirms.
const usesPasskey = () => stepupMethods(authState.value)[0] === 'passkey'
import { botsApi } from '../api/bots'
import { humanError } from '../api/errors'
import {
  approveKnowledgeWithChanges,
  decideKnowledge,
  repoApi,
  type BlameGroup,
  type CommitView,
  type KnowledgeProposal,
  type RepoCommit,
  type RepoData,
  type RepoKind,
} from '../api/repo'
import { stage } from '../configStore'
import { ago } from '../lib/format'
import { FactsLine } from './Facts'
import { FilterBar } from './FilterBar'
import { ConfirmModal, FormModal, type FormField } from './FormModal'
import { Icon } from './Icon'
import { Menu, type MenuItem } from './Menu'
import { DiffView, emitDoc, Markdown, MdEditor, parseDoc, slug, validate, type FM } from './RepoKit'
import { StatusMark } from './StatusMark'
import '../styles/bots-repo.css'

// ---------- kinds: what goes where (the prototype's KINDS, over the daemon's) ----------

interface FieldUI {
  k: string
  type: string // string | text | list | steps | enum | number | bool | evidence
  req: boolean
  label?: string
  help?: string
  ph?: string
  rows?: number
  options?: string[]
  def?: string
}

interface KindUI {
  id: string
  label: string
  one: string
  icon: string
  dir: string
  titleKey: string
  body: string
  who: { team?: string; bot?: string }
  readonly: string // why an operator can't write it ('' = writable)
  annotate: boolean
  free: boolean
  coll: boolean
  template: string
  fields: FieldUI[]
  raw: RepoKind
}

type Meta = {
  label: string
  one: string
  icon: string
  body?: string
  titleKey?: string
  who: { team?: string; bot?: string }
  readonly?: string
  fields?: Record<string, Partial<FieldUI>>
  order?: string[]
}
const META: Record<string, Meta> = {
  knowledge: {
    label: 'Knowledge',
    one: 'knowledge entry',
    icon: 'sparkle',
    body: 'What is true, and why you believe it',
    who: {
      team: 'You write directly; bots promote from their memory, with your approval.',
      bot: 'You and the bot write directly.',
    },
    order: ['title', 'applies_to', 'evidence', 'confidence'],
    fields: {
      applies_to: {
        label: 'Applies to',
        ph: 'service or resource',
        help: 'Services and resources this is about. Used to filter and to find it.',
      },
      evidence: {
        type: 'evidence',
        label: 'Evidence',
        help: 'Why you believe it: a ticket, a run, a pull request, a link or a note.',
      },
      confidence: { def: 'medium' },
    },
  },
  runbook: {
    label: 'Runbooks',
    one: 'runbook',
    icon: 'workflows',
    body: 'Notes on the procedure',
    who: {
      team: 'You write directly; bots propose.',
      bot: 'You and the bot write directly.',
    },
    order: ['title', 'when_to_use', 'prerequisites', 'steps', 'verification', 'rollback', 'owner'],
    fields: {
      when_to_use: {
        rows: 2,
        label: 'When to use',
        help: 'The trigger or the symptoms.',
      },
      prerequisites: { ph: 'access, a change window, a tool' },
      verification: { rows: 2, help: 'How you know it worked.' },
      rollback: { rows: 2, help: 'How to undo it, or "nothing to roll back".' },
      owner: { ph: 'a bot, a team or a person' },
    },
  },
  watch: {
    label: 'Watch',
    one: 'watch item',
    icon: 'eye',
    titleKey: 'what',
    who: {
      team: 'You write directly; bots propose.',
      bot: 'You and the bot write directly.',
    },
    order: ['what', 'signal', 'threshold', 'why'],
    fields: {
      what: { label: 'What to watch' },
      signal: { rows: 2, label: 'Signal / query' },
      threshold: { rows: 1 },
      why: { rows: 2 },
    },
  },
  log: {
    label: 'Log',
    one: 'log entry',
    icon: 'clock',
    readonly:
      "The bot writes its log, one entry per case or sweep. You can add a note or flag it as wrong (both appended, with your name), but history isn't rewritten.",
    who: { bot: 'The bot only. You annotate or flag.' },
  },
  handoff: {
    label: 'Handoff',
    one: 'handoff note',
    icon: 'send',
    readonly: 'Written by conductor when a home session rotates. Read-only.',
    who: { bot: 'System-generated, read-only.' },
  },
  digest: {
    label: 'Digests',
    one: 'digest',
    icon: 'list',
    body: 'What happened, per case',
    who: { bot: 'Written when its log is consolidated; you can correct one.' },
    order: ['title', 'summary', 'period', 'cases'],
  },
  charter: {
    label: 'Charter',
    one: 'charter',
    icon: 'lock',
    readonly: "Its template's charter, written by conductor. Change it through the bot's template.",
    who: { bot: 'Conductor only.' },
  },
}
const HIDE_WHEN_EMPTY = new Set(['digest', 'charter'])

function kindUI(k: RepoKind): KindUI {
  const m = META[k.kind]
  const coll = !k.builtin
  const writers = k.writers.split(/,\s*/).filter(Boolean)
  let fields: FieldUI[] = []
  if (k.kind !== 'log' && k.kind !== 'handoff' && k.kind !== 'charter') {
    const order = m?.order || k.fields.map((f) => f.name)
    const pick = order.map((n) => k.fields.find((f) => f.name === n)).filter(Boolean) as RepoKind['fields']
    if (coll) for (const f of k.fields) if (!pick.includes(f)) pick.push(f)
    fields = pick.map((f) => ({
      k: f.name,
      type: f.type === 'enum' ? 'enum' : ['string', 'text', 'list', 'steps', 'number', 'bool'].includes(f.type) ? f.type : 'string',
      req: f.required,
      options: f.values,
      rows: f.type === 'text' ? 2 : undefined,
      ...(m?.fields?.[f.name] || {}),
    }))
    if (coll && !fields.some((f) => f.k === 'title')) fields.unshift({ k: 'title', type: 'string', req: true })
  }
  const readonly =
    m?.readonly ||
    (!k.operator
      ? `Only ${writers.join(' and ') || 'conductor'} write${writers.length === 1 ? 's' : ''} ${(m?.label || k.kind).toLowerCase()}, as its kind declares`
      : '')
  return {
    id: k.kind,
    label: m?.label || k.kind,
    one: m?.one || k.kind.replace(/s$/, '') + ' entry',
    icon: m?.icon || 'folder',
    dir: k.dir.replace(/\/$/, ''),
    titleKey: m?.titleKey || 'title',
    body: k.kind === 'log' || k.kind === 'handoff' ? '' : m?.body || 'Body',
    who: m?.who || {
      team: `Writers: ${writers.join(', ')}.`,
      bot: `Writers: ${writers.join(', ')}.`,
    },
    readonly,
    annotate: k.annotate,
    free: k.free_paths,
    coll,
    template: k.template || '',
    fields,
    raw: k,
  }
}

interface Entry {
  kind: KindUI
  path: string
  title: string
  fm: FM
  body: string
  text: string
  err?: string
  status: string
  problem?: string
}

const visible = (p: string) => !p.split('/').some((x) => x.startsWith('.'))

// ---------- evidence: a list of things (conductor keeps it as text, one per line) ----------

type EvKind = 'ticket' | 'run' | 'ref' | 'url' | 'note'
interface Ev {
  k: EvKind
  v: string
}
const EVK: Record<EvKind, [string, string]> = {
  ticket: ['ticket', 'Ticket'],
  run: ['runs', 'Run'],
  ref: ['branch', 'Pull request or issue'],
  url: ['globe', 'Link'],
  note: ['edit', 'Note'],
}
const evFrom = (v: unknown): Ev[] => {
  if (v == null || v === '') return []
  const lines = (Array.isArray(v) ? v.map(String) : String(v).split('\n')).map((l) => l.trim()).filter(Boolean)
  return lines.map((l) => {
    let m = l.match(/^ticket (\S+)$/i)
    if (m) return { k: 'ticket', v: m[1] }
    m = l.match(/^run (\S+)$/i)
    if (m) return { k: 'run', v: m[1] }
    if (/^[\w.-]+\/[\w.-]+#\d+$/.test(l)) return { k: 'ref', v: l }
    if (/^https?:\/\/\S+$/.test(l)) return { k: 'url', v: l }
    return { k: 'note', v: l }
  })
}
const evTo = (xs: Ev[]): string => xs.map((x) => (x.k === 'ticket' ? `ticket ${x.v}` : x.k === 'run' ? `run ${x.v}` : x.v)).join('\n')
const refHref = (r: string) => {
  const m = /^([\w.-]+\/[\w.-]+)#(\d+)$/.exec(r || '')
  return m ? `https://github.com/${m[1]}/issues/${m[2]}` : ''
}
export const evidenceWords = (v: unknown) =>
  evFrom(v)
    .map((x) => `${EVK[x.k][1]}: ${x.k === 'url' ? x.v.replace(/^https?:\/\//, '') : x.v}`)
    .join(' · ')

// ---------- runbook steps: one line each, a command or verb riding along ----------

interface Step {
  do: string
  type: '' | 'run' | 'verb'
  arg: string
}
const stepsFrom = (v: unknown): Step[] =>
  (Array.isArray(v) ? v : []).map((st) => {
    const m = String(st || '').match(/^(.*?)\s*\((run|verb): `?([^`)]*)`?\)\s*$/)
    return m ? { do: m[1], type: m[2] as Step['type'], arg: m[3] } : { do: String(st || ''), type: '', arg: '' }
  })
const stepsTo = (st: Step[]) =>
  st
    .filter((x) => x.do || x.arg)
    .map((x) => `${x.do}${x.type === 'run' && x.arg ? ` (run: \`${x.arg}\`)` : x.type === 'verb' && x.arg ? ` (verb: ${x.arg})` : ''}`)

// ---------- small pieces ----------

const Count = ({ n }: { n: ComponentChildren }) => <span class="count">{n}</span>
const Tag = ({ children, s, cls, icon }: { children: ComponentChildren; s?: string; cls?: string; icon?: string }) => (
  <span class={'tag' + (cls ? ' ' + cls : '')} data-s={s}>
    {icon ? <Icon name={icon} /> : null}
    {children}
  </span>
)
function Seg({ items, value, onPick }: { items: [string, string][]; value: string; onPick: (v: string) => void }): JSX.Element {
  return (
    <div class="seg" role="group">
      {items.map(([v, l]) => (
        <button type="button" data-v={v} aria-pressed={v === value} class={v === value ? 'on' : ''} onClick={() => onPick(v)}>
          {l}
        </button>
      ))}
    </div>
  )
}
// Run renders fn as its own component: the hooks fn calls belong to this
// (stable) component, so a field's or a modal's state survives the view's
// re-renders while fn still closes over the view's current state.
function Run({ fn }: { fn: () => JSX.Element }): JSX.Element {
  return fn()
}
const off = (reason: string) => ({
  'aria-disabled': true as const,
  'data-reason': reason,
  'data-tip': reason,
})

interface Toast {
  title: string
  sub: string
  s: string
}

// ---------- the view ----------

interface Draft {
  fm: FM
  body: string
  free: string
}

interface Conflict {
  theirs: string
  head: string
  base: string
  mine: string
  who: string
  sha: string
}

type Modal =
  | { t: 'new' }
  | { t: 'diff'; c: CommitView }
  | {
      t: 'confirm'
      title: string
      body: ComponentChildren
      ok: string
      danger?: boolean
      run: () => Promise<void> | void
    }
  | {
      t: 'form'
      title: string
      caps?: string
      intro?: string
      fields: FormField[]
      submit: string
      run: (v: Record<string, string>) => Promise<string | void> | string | void
    }
  | { t: 'import'; docs?: { name: string; text: string }[] }
  | { t: 'map'; docs: { name: string; text: string }[] }

export function RepoView({ target, title }: { target: string; title?: string }): JSX.Element {
  const [kindOf, name] = target.split(':')
  const isTeam = kindOf === 'team'
  const me = authState.value.operator || ''
  const [data, setData] = useState<RepoData | null>(null)
  const [err, setErr] = useState('')
  const [props, setProps] = useState<KnowledgeProposal[]>([])
  const [related, setRelated] = useState<Record<string, RepoData>>({})
  const [tickets, setTickets] = useState<Record<string, { ticket: string; title: string }>>({})
  const [hist, setHist] = useState<RepoCommit[] | null>(null)
  const [histMode, setHistMode] = useState<'file' | 'repo'>('file')
  const [view, setView] = useState<'kinds' | 'files'>('kinds')
  const [kindId, setKindId] = useState('')
  const [key, setKey] = useState<string | null>(null)
  const [isNew, setIsNew] = useState(false)
  const [file, setFile] = useState<string | null>(null)
  const [q, setQ] = useState('')
  const [tags, setTags] = useState<string[]>([])
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [phList, setPhList] = useState(false)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [base, setBase] = useState('') // the draft as opened, serialized
  const [opened, setOpened] = useState('') // the file's text as opened
  const [proposal, setProposal] = useState<KnowledgeProposal | null>(null)
  const [conflict, setConflict] = useState<Conflict | null>(null)
  const [blame, setBlame] = useState<BlameGroup[] | null>(null)
  const [schemaErrs, setSchemaErrs] = useState<string[]>([])
  const [saveErr, setSaveErr] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const [modal, setModal] = useState<Modal | null>(null)
  const [menu, setMenu] = useState<{
    el: HTMLElement
    items: MenuItem[]
  } | null>(null)
  const [toast, setToast] = useState<Toast | null>(null)
  const blameBtn = useRef<HTMLButtonElement>(null)

  const say = (t: string, sub = '', s = 'ok') => setToast({ title: t, sub, s })
  useEffect(() => {
    if (!toast) return
    const h = setTimeout(() => setToast(null), 6000)
    return () => clearTimeout(h)
  }, [toast])

  async function load(keepOpen = true): Promise<RepoData | null> {
    try {
      const d = await repoApi.load(target)
      setData(d)
      setErr('')
      if (isTeam)
        repoApi
          .proposals(name)
          .then((r) => setProps(r.proposals || []))
          .catch(() => {})
      return d
    } catch (e) {
      setErr(humanError(e))
      if (!keepOpen) setData(null)
      return null
    }
  }

  // first load: open what another page asked for (a search hit), else the first entry of the first kind
  useEffect(() => {
    setData(null)
    setKey(null)
    setKindId('')
    setIsNew(false)
    setView('kinds')
    setQ('')
    setTags([])
    setPhList(false)
    setProposal(null)
    setConflict(null)
    load(false).then((d) => {
      if (!d) return
      const want = sessionStorage.getItem('repo-open:' + target)
      sessionStorage.removeItem('repo-open:' + target)
      const ks = kindsOf(d)
      const es = (k: KindUI) => entriesOf(d, k)
      const wantK = want ? ks.find((k) => es(k).some((e) => e.path === want)) : null
      const k = wantK || ks[0]
      if (!k) return
      setKindId(k.id)
      const e = es(k).find((x) => x.path === want) || es(k)[0]
      if (e) openEntry(e)
    })
    // related repos: a team's bots' memory, a bot's team knowledge (search and "cited by")
    ;(async () => {
      const out: Record<string, RepoData> = {}
      const d = await repoApi.load(target).catch(() => null)
      if (!d) return
      const ids = isTeam ? (d.readers || []).map((b) => 'bot:' + b) : d.team && d.team_knowledge ? ['team:' + d.team] : []
      for (const id of ids) {
        const r = await repoApi.load(id).catch(() => null)
        if (r) out[id] = r
      }
      setRelated(out)
    })()
    botsApi
      .tickets()
      .then((r) => {
        const m: Record<string, { ticket: string; title: string }> = {}
        for (const t of r.tickets || []) m[t.id] = { ticket: t.ticket, title: t.title }
        setTickets(m)
      })
      .catch(() => {})
  }, [target])

  const kinds = useMemo(() => (data ? kindsOf(data) : []), [data])
  function kindsOf(d: RepoData): KindUI[] {
    const all = d.kinds.map((k) => kindUI(k))
    return all.filter((k) => !HIDE_WHEN_EMPTY.has(k.id) || d.files.some((f) => f.kind === k.id))
  }
  function entriesOf(d: RepoData, k: KindUI): Entry[] {
    return d.files
      .filter((f) => f.kind === k.id && visible(f.path))
      .map((f) => {
        const x = parseDoc(f.text || '')
        const t = (x.fm[k.titleKey] as string) || f.title || f.path.split('/').pop()!.replace(/\.md$/, '')
        return {
          kind: k,
          path: f.path,
          title: String(t),
          fm: x.fm,
          body: x.body,
          text: f.text || '',
          err: x.err,
          status: f.status,
          problem: f.problem,
        }
      })
      .sort((a, b) => a.path.localeCompare(b.path))
  }
  const kind = kinds.find((k) => k.id === kindId) || kinds[0]
  const entries = (k: KindUI) => (data ? entriesOf(data, k) : [])
  const current = kind && key ? entries(kind).find((e) => e.path === key) || null : null

  const serialize = (k: KindUI, d: Draft) => emitDoc(k.raw, d.fm, d.body) + '|' + (d.free || '')
  const dirty = !!(kind && draft && !kind.readonly && (serialize(kind, draft) !== base || !!proposal))

  function openEntry(e: Entry, d?: Draft) {
    const dd = d || {
      fm: JSON.parse(JSON.stringify(e.fm)),
      body: e.body || '',
      free: e.kind.free ? e.path.slice(e.kind.dir.length + 1).replace(/\.md$/, '') : '',
    }
    setKindId(e.kind.id)
    setKey(e.path)
    setIsNew(false)
    setDraft(dd)
    setBase(serialize(e.kind, { fm: e.fm, body: e.body || '', free: dd.free }))
    setOpened(e.text)
    setConflict(null)
    setBlame(null)
    setSchemaErrs([])
    setSaveErr('')
    setMsg('')
    setView('kinds')
    setFile(null)
  }
  function newEntry(k: KindUI, d?: Draft) {
    const dd = d || {
      fm: Object.fromEntries(k.fields.filter((f) => f.def).map((f) => [f.k, f.def])),
      body: k.template || '',
      free: '',
    }
    setKindId(k.id)
    setKey(null)
    setIsNew(true)
    setDraft(dd)
    setBase(serialize(k, { fm: {}, body: k.template || '', free: '' }))
    setOpened('')
    setConflict(null)
    setBlame(null)
    setSchemaErrs([])
    setSaveErr('')
    setMsg('')
    setView('kinds')
    setPhList(false)
    setTimeout(() => (document.getElementById('ke-title') || document.getElementById('ke-what'))?.focus(), 40)
  }
  // guardThen: leaving unsaved edits asks first
  function guardThen(fn: () => void) {
    if (!dirty && !proposal) return fn()
    setModal({
      t: 'confirm',
      title: 'Discard unsaved changes?',
      body: proposal ? (
        'You are approving a proposal with changes.'
      ) : (
        <>
          Your edits to <span class="mono">{key || 'the new entry'}</span> aren't saved. Leave and lose them?
        </>
      ),
      ok: 'Discard',
      danger: true,
      run: () => {
        setProposal(null)
        setBase('')
        fn()
      },
    })
  }

  // history: this entry's, or the repo's
  const histPath = histMode === 'file' ? key : null
  useEffect(() => {
    if (!data) return
    if (histMode === 'file' && !key) {
      setHist([])
      return
    }
    repoApi
      .history(target, histPath || undefined, 30)
      .then((r) => setHist(r.commits || []))
      .catch(() => setHist([]))
  }, [target, histPath, histMode, data?.head])

  // ---------- writes ----------

  const derive = (k: KindUI, t: string, free: string) => `${k.dir}/${k.free && free ? free.replace(/^\/+/, '').replace(/\.md$/, '') : slug(t)}.md`
  const titleOf = (k: KindUI, fm: FM) => String(fm[k.titleKey] || '')
  // where the draft lands: a new entry follows its title; an existing one keeps
  // its path until its title (or, in a free-path collection, its path) changes
  const pathFor = (k: KindUI, d: Draft) => {
    if (isNew || !key) return derive(k, titleOf(k, d.fm), d.free)
    if (k.free) return d.free ? derive(k, '', d.free) : key
    const t = titleOf(k, d.fm)
    return !t || t === titleOf(k, parseDoc(opened).fm) ? key : derive(k, t, '')
  }

  async function writeConflict(head: string, path: string, mine: string) {
    const d = await load()
    const theirs = d?.files.find((f) => f.path === path)?.text || ''
    let who = 'someone'
    let sha = d?.head || head
    try {
      const h = await repoApi.history(target, path, 1)
      const c = (h.commits || [])[0]
      if (c) {
        who = c.kind === 'operator' ? (c.who === me ? 'you' : c.who) : c.who
        sha = c.short
      }
    } catch {
      /* the conflict stands without a name */
    }
    setConflict({
      theirs,
      head: d?.head || head,
      base: opened,
      mine,
      who,
      sha,
    })
  }

  async function save(force = false) {
    if (!kind || !draft || !data) return
    const errs = validate(kind.raw, draft.fm)
    if (kind.free && draft.free && (!/^[a-z0-9][\w./-]*$/i.test(draft.free) || /\.\./.test(draft.free)))
      errs.push('free: a relative path such as vendors/acme (no .. or leading /)')
    setSchemaErrs(errs)
    setSaveErr('')
    if (errs.length) return
    const text = emitDoc(kind.raw, draft.fm, draft.body)
    const path = pathFor(kind, draft)
    const from = isNew ? null : key
    if (path !== from && data.files.some((f) => f.path === path)) {
      setSchemaErrs([`${kind.titleKey}: an entry with this ${kind.titleKey === 'what' ? 'name' : 'title'} already exists (${path})`])
      return
    }
    setBusy(true)
    try {
      if (proposal) {
        await approveKnowledgeWithChanges(proposal.bot, proposal.proposal, text)
        setProposal(null)
        const d = await load()
        const e = d && entriesOf(d, kind).find((x) => x.path === proposal.to)
        if (e) openEntry(e)
        say(`Approved ${proposal.proposal} with changes`, 'one commit in your name, the bot named as its source')
        return
      }
      let head = force && conflict ? conflict.head : data.head
      const message = msg.trim() || undefined
      let commit = ''
      if (isNew) {
        const r = kind.free
          ? await repoApi.edit(target, {
              op: 'put',
              kind: kind.id,
              path,
              text,
              create: true,
              base: head,
              message: message || `Add “${titleOf(kind, draft.fm)}”`,
            })
          : await repoApi.create(target, {
              kind: kind.id,
              text,
              base: head,
              message: message || `Add “${titleOf(kind, draft.fm)}”`,
              slug: slug(titleOf(kind, draft.fm)),
            })
        commit = r.commit
      } else {
        if (from && from !== path) {
          const r = await repoApi.edit(target, {
            op: 'mv',
            path: from,
            to: path,
            base: head,
            message: `Rename “${titleOf(kind, parseDoc(opened).fm) || from}” to “${titleOf(kind, draft.fm)}”`,
          })
          head = r.commit
        }
        const r = await repoApi.edit(target, {
          op: 'put',
          kind: kind.id,
          path,
          text,
          base: head,
          message: message || `Edit “${titleOf(kind, draft.fm)}”${force ? ' (over a newer commit)' : ''}`,
        })
        commit = r.commit
      }
      const d = await load()
      const e = d && entriesOf(d, kind).find((x) => x.path === path)
      if (e) openEntry(e)
      say(`Committed ${commit.slice(0, 7)}`, `one commit by ${me || 'you'} · ${path}`)
    } catch (e) {
      const st = (e as { status?: number }).status
      if (st === 409 && !isNew && key) await writeConflict(data.head, key, text)
      else setSaveErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  async function edit(req: Parameters<typeof repoApi.edit>[1], done: string, sub = '') {
    try {
      const r = await repoApi.edit(target, req)
      await load()
      say(done, sub || `commit ${r.commit.slice(0, 7)}`)
      return r
    } catch (e) {
      say('Not saved', humanError(e), 'fail')
      if ((e as { status?: number }).status === 409) await load()
      return null
    }
  }

  // ---------- the rail ----------

  const allEntries = useMemo(() => kinds.flatMap((k) => entries(k)), [kinds, data])
  const tagList = useMemo(
    () => [...new Set(allEntries.flatMap((e) => (Array.isArray(e.fm.applies_to) ? (e.fm.applies_to as string[]) : [])))].sort(),
    [allEntries],
  )
  const match = (e: Entry) =>
    (!tags.length || (Array.isArray(e.fm.applies_to) && (e.fm.applies_to as string[]).some((t) => tags.includes(t)))) &&
    (!q || JSON.stringify(e.fm).toLowerCase().includes(q.toLowerCase()) || (e.body || '').toLowerCase().includes(q.toLowerCase()))
  const nEntries = allEntries.length

  function KindRow({ k }: { k: KindUI }): JSX.Element {
    const es = entries(k)
    const shown = es.filter(match)
    const on = !!kind && k.id === kind.id
    return (
      <div class={'kind-grp' + (on ? ' on' : '')}>
        <a
          href="#"
          class="kind-row"
          data-k={k.id}
          aria-expanded={on}
          onClick={(e) => {
            e.preventDefault()
            guardThen(() => {
              setKindId(k.id)
              setView('kinds')
              const first = es[0]
              if (first) openEntry(first)
              else {
                setKey(null)
                setIsNew(false)
                setDraft(null)
              }
            })
          }}
        >
          <Icon name={k.icon} />
          <span class="grow">{k.label}</span>
          <Count n={`${q || tags.length ? `${shown.length}/` : ''}${es.length}`} />
        </a>
        {on ? (
          <div class="kind-entries">
            {shown.length ? (
              shown.map((e) => (
                <a
                  href="#"
                  class={'f entry' + (e.path === key ? ' on' : '')}
                  data-key={e.path}
                  onClick={(ev) => {
                    ev.preventDefault()
                    guardThen(() => {
                      setPhList(false)
                      openEntry(e)
                    })
                  }}
                >
                  <span class="ellipsis grow" title={e.title}>
                    {e.title}
                  </span>
                  {e.status === 'invalid' || e.err ? <Tag s="fail">invalid</Tag> : null}
                </a>
              ))
            ) : (
              <div class="muted tiny" style="padding:6px 12px">
                {es.length ? 'Nothing matches.' : 'None yet.'}
              </div>
            )}
          </div>
        ) : null}
      </div>
    )
  }

  function SearchHits(): JSX.Element {
    const ql = q.toLowerCase()
    const out: { repo: string; kind: KindUI; e: Entry; snip: string }[] = []
    const repos: [string, RepoData][] = [[target, data!], ...Object.entries(related)]
    for (const [id, d] of repos) {
      for (const k of d.kinds.map((x) => kindUI(x))) {
        for (const e of entriesOf(d, k)) {
          const hay = JSON.stringify(e.fm) + '\n' + (e.body || '')
          if (!hay.toLowerCase().includes(ql)) continue
          const line =
            (e.body || '').split('\n').find((l) => l.toLowerCase().includes(ql)) ||
            Object.values(e.fm)
              .map(String)
              .find((v) => v.toLowerCase().includes(ql)) ||
            ''
          out.push({ repo: id, kind: k, e, snip: line.trim() })
        }
      }
    }
    return (
      <div class="list search-res">
        {out.length ? (
          out.map((x) => (
            <a
              href="#"
              class="item click small"
              onClick={(ev) => {
                ev.preventDefault()
                guardThen(() => {
                  setQ('')
                  if (x.repo === target) {
                    openEntry(x.e)
                    setPhList(false)
                  } else {
                    sessionStorage.setItem('repo-open:' + x.repo, x.e.path)
                    const [kk, n] = x.repo.split(':')
                    location.hash = kk === 'team' ? `#/teams/${n}/knowledge` : `#/bots/${n}/memory`
                  }
                })
              }}
            >
              <div class="grow">
                <div class="mono tiny muted">
                  {x.repo} · {x.kind.label}
                </div>
                <b>{x.e.title}</b>
                <div class="muted tiny ellipsis" title={x.snip}>
                  {x.snip}
                </div>
              </div>
            </a>
          ))
        ) : (
          <div class="item small muted">No match.</div>
        )}
        <button class="btn sm ghost" style="margin:6px" onClick={() => setQ('')}>
          <Icon name="x" />
          Clear search
        </button>
      </div>
    )
  }

  // ---------- history ----------

  async function showDiff(c: RepoCommit) {
    try {
      setModal({ t: 'diff', c: await repoApi.commit(target, c.commit) })
    } catch (e) {
      say("Couldn't load that commit", humanError(e), 'fail')
    }
  }
  function revert(c: RepoCommit) {
    setModal({
      t: 'confirm',
      title: `Revert ${c.short}?`,
      body: `A new commit undoes “${humanSubject(c.subject)}”. History is kept.`,
      ok: 'Revert',
      run: async () => {
        await edit(
          {
            op: 'revert',
            commit: c.commit,
            base: data!.head,
            message: `Revert "${c.subject}"`,
          },
          `Reverted ${c.short}`,
        )
      },
    })
  }
  // a commit's subject as people read it: conductor's own subjects name files by
  // path ("add knowledge knowledge/deploy-freeze.md"); each path becomes that
  // entry's title (a removed one keeps its file name), the kind word before a
  // path goes, and it starts with a capital
  const titles = useMemo(() => {
    const m = new Map<string, string>()
    for (const k of kinds) for (const e of entries(k)) m.set(e.path, e.title)
    return m
  }, [kinds, data])
  const humanSubject = (subj: string) => {
    let t = subj.replace(/(\b(?:knowledge|runbook|watch|watch item|knowledge entry|log entry|digest|charter|handoff note)\s+)?((?:[\w.-]+\/)+[\w.-]+\.md)\b/g, (_m, _kw, p: string) => {
      const name = titles.get(p)
      return `“${name || p.split('/').pop()!.replace(/\.md$/, '')}”`
    })
    return t.charAt(0).toUpperCase() + t.slice(1)
  }
  const whoLabel = (c: { kind: string; who: string }) => (c.kind === 'operator' ? (c.who === me ? 'you' : c.who) : c.kind === 'team' ? 'conductor' : c.who)
  const caseLink = (id?: string) => (id && tickets[id] ? `#/tickets/${tickets[id].ticket}` : '')
  const WhoChip = ({ c }: { c: { kind: string; who: string; case?: string } }) => (
    <>
      <Tag cls={'who ' + c.kind} icon={c.kind === 'operator' ? 'users' : 'bots'}>
        {whoLabel(c)}
      </Tag>
      {c.case && caseLink(c.case) ? (
        <>
          {' '}
          <a class="link tiny" href={caseLink(c.case)} title={tickets[c.case]?.title || 'Its ticket'}>
            ticket
          </a>
        </>
      ) : null}
    </>
  )

  // ---------- the entry ----------

  const readersLine = () => {
    const rs = data?.readers || []
    return (
      <div class="readers small muted">
        <Icon name="eye" /> read by{' '}
        {rs.length
          ? rs.map((x, i) => (
              <>
                {i ? ', ' : ''}
                <a class="link mono" href={`#/bots/${x}`}>
                  {x}
                </a>
              </>
            ))
          : 'no bot yet'}
        {isTeam ? ` · team ${name}` : ' · its own memory'} · {kind?.who[isTeam ? 'team' : 'bot'] || ''}
      </div>
    )
  }

  // who cites an entry: runbooks and log entries that link it (here or in a related repo), and their cases
  function cites(path: string) {
    const stem = path.replace(/\.md$/, '')
    const out = {
      runbooks: [] as { repo: string; path: string; title: string }[],
      logs: [] as { repo: string; path: string; title: string }[],
      cases: new Set<string>(),
    }
    const repos: [string, RepoData][] = [[target, data!], ...Object.entries(related)]
    for (const [id, d] of repos) {
      for (const f of d.files) {
        if ((id === target && f.path === path) || !f.text || !visible(f.path)) continue
        if (!(f.text.includes('[[' + stem + ']]') || f.text.includes(path))) continue
        const fm = parseDoc(f.text).fm
        if (f.kind === 'runbook')
          out.runbooks.push({
            repo: id,
            path: f.path,
            title: String(fm.title || f.path),
          })
        if (f.kind === 'log') {
          out.logs.push({
            repo: id,
            path: f.path,
            title: String(fm.title || f.path),
          })
          if (fm.case) out.cases.add(String(fm.case))
        }
      }
    }
    return { ...out, cases: [...out.cases] }
  }
  const goHit = (repo: string, path: string) =>
    guardThen(() => {
      if (repo === target) {
        const e = allEntries.find((x) => x.path === path)
        if (e) openEntry(e)
        return
      }
      sessionStorage.setItem('repo-open:' + repo, path)
      const [kk, n] = repo.split(':')
      location.hash = kk === 'team' ? `#/teams/${n}/knowledge` : `#/bots/${n}/memory`
    })

  const setFm = (k: string, v: unknown) => setDraft((d) => (d ? { ...d, fm: { ...d.fm, [k]: v } } : d))

  function FieldRow({ f }: { f: FieldUI }): JSX.Element {
    const v = draft?.fm[f.k]
    const id = `ke-${f.k}`
    const lbl = f.label || f.k.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())
    const bad = schemaErrs.filter((e) => e.split(/[.:[]/)[0] === f.k).map((e) => e.replace(/^[\w]+:\s*/, ''))
    const req = f.req ? (
      <span class="req" aria-label="required">
        *
      </span>
    ) : (
      <span class="opt">optional</span>
    )
    let input: JSX.Element
    if (f.type === 'evidence') return <Run key={kindId + ':evidence'} fn={() => EvidenceField({ f, bad })} />
    if (f.type === 'steps') return <Run key={kindId + ':steps'} fn={() => StepsField({ bad })} />
    switch (f.type) {
      case 'text':
        input = (
          <textarea
            class="textarea"
            id={id}
            data-k={f.k}
            rows={f.rows || 3}
            placeholder={f.ph || ''}
            value={String(v ?? '')}
            onInput={(e) => setFm(f.k, e.currentTarget.value)}
          />
        )
        break
      case 'enum':
        input = (
          <select
            class="select mono"
            id={id}
            data-k={f.k}
            value={String(v ?? (isNew ? (f.def ?? '') : ''))}
            onChange={(e) => setFm(f.k, e.currentTarget.value)}
          >
            {f.req ? null : <option value="">— none —</option>}
            {!f.req || v ? null : (
              <option value="" disabled>
                choose…
              </option>
            )}
            {(f.options || []).map((o) => (
              <option value={o}>{o}</option>
            ))}
          </select>
        )
        break
      case 'bool':
        input = (
          <label class="row small" style="min-height:30px">
            <input type="checkbox" class="cbx" id={id} data-k={f.k} checked={!!v} onChange={(e) => setFm(f.k, e.currentTarget.checked)} /> on
          </label>
        )
        break
      case 'number':
        input = (
          <input
            class="input mono"
            id={id}
            data-k={f.k}
            inputMode="decimal"
            value={v == null ? '' : String(v)}
            placeholder={f.ph || ''}
            onInput={(e) => {
              const t = e.currentTarget.value.trim()
              setFm(f.k, t === '' ? '' : Number.isNaN(Number(t)) ? t : Number(t))
            }}
          />
        )
        break
      case 'list':
        return <Run key={kindId + ':' + f.k} fn={() => ListField({ f, lbl, bad })} />
      default:
        input = (
          <input class="input mono" id={id} data-k={f.k} value={String(v ?? '')} placeholder={f.ph || ''} onInput={(e) => setFm(f.k, e.currentTarget.value)} />
        )
    }
    return (
      <div class={'field' + (f.type === 'bool' ? ' boolf' : '') + (bad.length ? ' bad' : '')} data-field={f.k}>
        <label for={id}>
          {lbl} {req}
        </label>
        {input}
        {f.help ? <div class="hint">{f.help}</div> : null}
        <div class="ferr" role="alert">
          {bad.join(' · ')}
        </div>
      </div>
    )
  }

  function ListField({ f, lbl, bad }: { f: FieldUI; lbl: string; bad: string[] }): JSX.Element {
    const items = Array.isArray(draft?.fm[f.k]) ? (draft!.fm[f.k] as string[]) : draft?.fm[f.k] ? String(draft.fm[f.k]).split(/,\s*/) : []
    const [t, setT] = useState('')
    const add = () => {
      const x = t.trim()
      if (x && !items.includes(x)) setFm(f.k, [...items, x])
      setT('')
    }
    return (
      <div class={'field' + (bad.length ? ' bad' : '')} data-field={f.k}>
        <label for={`ke-${f.k}`}>
          {lbl}{' '}
          {f.req ? (
            <span class="req" aria-label="required">
              *
            </span>
          ) : (
            <span class="opt">optional</span>
          )}
        </label>
        <div class="listed" data-listk={f.k} id={`ke-${f.k}`}>
          {items.map((x) => (
            <span class="chip-x">
              <span class="mono" data-v={x}>
                {x}
              </span>
              <button
                type="button"
                aria-label={`Remove ${x}`}
                onClick={() =>
                  setFm(
                    f.k,
                    items.filter((y) => y !== x),
                  )
                }
              >
                <Icon name="x" />
              </button>
            </span>
          ))}
          <input
            class="input mono list-add"
            placeholder={f.ph || 'add, then Enter'}
            value={t}
            onInput={(e) => setT(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                add()
              }
            }}
          />
          <button type="button" class="btn sm" onClick={add}>
            <Icon name="plus" />
            Add
          </button>
        </div>
        {f.help ? <div class="hint">{f.help}</div> : null}
        <div class="ferr" role="alert">
          {bad.join(' · ')}
        </div>
      </div>
    )
  }

  function EvidenceField({ f, bad }: { f: FieldUI; bad: string[] }): JSX.Element {
    const xs = evFrom(draft?.fm.evidence)
    const put = (n: Ev[]) => setFm('evidence', evTo(n))
    const addBtn = useRef<HTMLButtonElement>(null)
    const label = (x: Ev) =>
      x.k === 'ticket'
        ? tickets[x.v]?.title || Object.values(tickets).find((t) => t.ticket === x.v)?.title || x.v
        : x.k === 'url'
          ? x.v.replace(/^https?:\/\//, '').replace(/\/$/, '')
          : x.v
    const href = (x: Ev) =>
      x.k === 'ticket' ? `#/tickets/${tickets[x.v]?.ticket || x.v}` : x.k === 'run' ? `#/runs/${x.v}` : x.k === 'ref' ? refHref(x.v) : x.k === 'url' ? x.v : ''
    const ask = (k: EvKind) => {
      if (k === 'ticket') {
        const have = xs.filter((x) => x.k === 'ticket').map((x) => x.v)
        const ts = Object.values(tickets)
          .filter((t) => !have.includes(t.ticket))
          .slice(0, 8)
        setMenu({
          el: addBtn.current!,
          items: ts.length
            ? ts.map((t) => ({
                label: t.title,
                icon: 'ticket',
                onClick: () => put([...xs, { k: 'ticket', v: t.ticket }]),
              }))
            : [
                {
                  label: 'No other tickets',
                  icon: 'ticket',
                  disabled: 'Every ticket is already here',
                },
              ],
        })
        return
      }
      const forms: Record<string, [string, FormField]> = {
        run: [
          'Add a run',
          {
            k: 'v',
            type: 'string',
            req: true,
            label: 'Run',
            ph: 'the run id',
            help: 'From the run page',
          },
        ],
        ref: [
          'Add a pull request or issue',
          {
            k: 'v',
            type: 'string',
            req: true,
            label: 'Pull request or issue',
            ph: 'acme/web#1288',
            help: 'owner/repo#number',
          },
        ],
        url: [
          'Add a link',
          {
            k: 'v',
            type: 'string',
            req: true,
            label: 'Link',
            ph: 'https://…',
            help: 'A dashboard, a doc, a status page',
          },
        ],
        note: [
          'Add a note',
          {
            k: 'v',
            type: 'text',
            req: true,
            label: 'Note',
            ph: 'What you saw, and where',
          },
        ],
      }
      const [title, field] = forms[k]
      setModal({
        t: 'form',
        title,
        intro: k === 'ref' ? 'Read through your GitHub connection.' : undefined,
        fields: [field],
        submit: 'Add',
        run: (v) => {
          const x = v.v.trim()
          if (k === 'ref' && !refHref(x)) return 'Write it as owner/repo#number, like acme/web#1288.'
          if (k === 'url' && !/^https?:\/\/\S+\.\S+/.test(x)) return 'Write a full link, starting https://'
          put([...xs, { k, v: k === 'note' ? x.replace(/\s*\n\s*/g, ' ') : x }])
        },
      })
    }
    return (
      <div class={'field' + (bad.length ? ' bad' : '')} data-field="evidence">
        <label>
          {f.label || 'Evidence'}
          {f.req ? (
            <>
              {' '}
              <span class="req">*</span>
            </>
          ) : null}
        </label>
        <div class="ev-wrap">
          <div class="ev-list" id="ke-evid">
            {xs.map((x, i) => {
              const [ic, word] = EVK[x.k]
              const inner = (
                <>
                  <Icon name={ic} />
                  <span class="ev-t">{label(x)}</span>
                </>
              )
              const h = href(x)
              return (
                <span class={`ev-chip ev-${x.k}`} data-i={i} title={`${word}: ${label(x)}`}>
                  {h ? (
                    <a href={h} {...(x.k === 'ref' || x.k === 'url' ? { target: '_blank', rel: 'noopener noreferrer' } : {})}>
                      {inner}
                    </a>
                  ) : x.k === 'note' ? (
                    <button
                      type="button"
                      aria-label={`Edit the note: ${label(x)}`}
                      onClick={() =>
                        setModal({
                          t: 'form',
                          title: 'Edit the note',
                          fields: [
                            {
                              k: 'v',
                              type: 'text',
                              req: true,
                              label: 'Note',
                              def: x.v,
                            },
                          ],
                          submit: 'Save note',
                          run: (v) =>
                            put(
                              xs.map((y, j) =>
                                j === i
                                  ? {
                                      k: 'note',
                                      v: v.v.trim().replace(/\s*\n\s*/g, ' '),
                                    }
                                  : y,
                              ),
                            ),
                        })
                      }
                    >
                      {inner}
                    </button>
                  ) : (
                    <span>{inner}</span>
                  )}
                  <button type="button" class="ev-x" aria-label={`Remove ${word.toLowerCase()}: ${label(x)}`} onClick={() => put(xs.filter((_, j) => j !== i))}>
                    <Icon name="x" />
                  </button>
                </span>
              )
            })}
            <button
              type="button"
              class="btn sm ev-add"
              aria-haspopup="menu"
              ref={addBtn}
              onClick={() =>
                setMenu({
                  el: addBtn.current!,
                  items: (Object.keys(EVK) as EvKind[]).map((k) => ({
                    label: `${EVK[k][1]}…`,
                    icon: EVK[k][0],
                    onClick: () => setTimeout(() => ask(k), 0),
                  })),
                })
              }
            >
              <Icon name="plus" />
              Add evidence
            </button>
          </div>
        </div>
        <div class="hint">{f.help}</div>
        <div class="ferr" role="alert">
          {bad.join(' · ')}
        </div>
      </div>
    )
  }

  function StepsField({ bad }: { bad: string[] }): JSX.Element {
    const steps = stepsFrom(draft?.fm.steps)
    const put = (n: Step[]) => setFm('steps', stepsTo(n).length || n.length === 0 ? (n.some((x) => x.do || x.arg) ? stepsTo(n) : n.map(() => '')) : [])
    // keep blank rows while they're being written: store the raw rows on the draft
    const [rows, setRows] = useState<Step[]>(steps.length ? steps : [])
    useEffect(() => setRows(stepsFrom(draft?.fm.steps)), [key, isNew, proposal])
    const commit = (n: Step[]) => {
      setRows(n)
      setFm('steps', stepsTo(n))
    }
    void put
    return (
      <div class={'field' + (bad.length ? ' bad' : '')} data-field="steps">
        <label>
          Steps <span class="req">*</span>
        </label>
        <div class="rb-wrap">
          <div class="rb-steps" id="rb-steps">
            {rows.length ? (
              rows.map((s, i) => (
                <div class="rb-step" data-i={i}>
                  <span class="rb-n">{i + 1}</span>
                  <div class="rb-main">
                    <input
                      class="input"
                      data-sk="do"
                      value={s.do}
                      placeholder="What to do"
                      aria-label={`Step ${i + 1}: what to do`}
                      onInput={(e) => commit(rows.map((x, j) => (j === i ? { ...x, do: e.currentTarget.value } : x)))}
                    />
                    <div class="rb-act">
                      <select
                        class="select sm"
                        data-sk="type"
                        aria-label={`Step ${i + 1}: command or verb`}
                        value={s.type}
                        onChange={(e) =>
                          commit(
                            rows.map((x, j) =>
                              j === i
                                ? {
                                    ...x,
                                    type: e.currentTarget.value as Step['type'],
                                  }
                                : x,
                            ),
                          )
                        }
                      >
                        <option value="">no command</option>
                        <option value="run">run a command</option>
                        <option value="verb">call a verb</option>
                      </select>
                      {s.type ? (
                        <input
                          class="input mono"
                          data-sk="arg"
                          value={s.arg}
                          placeholder={s.type === 'verb' ? 'connector.verb' : 'the command'}
                          aria-label={`Step ${i + 1}: ${s.type === 'verb' ? 'verb' : 'command'}`}
                          onInput={(e) => commit(rows.map((x, j) => (j === i ? { ...x, arg: e.currentTarget.value } : x)))}
                        />
                      ) : null}
                    </div>
                  </div>
                  <div class="rb-ctl">
                    <button
                      type="button"
                      class="icon-btn sm"
                      {...(i ? {} : off('Already the first step'))}
                      aria-label={`Move step ${i + 1} up`}
                      onClick={() => i && commit(rows.map((x, j) => (j === i - 1 ? rows[i] : j === i ? rows[i - 1] : x)))}
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      class="icon-btn sm"
                      {...(i < rows.length - 1 ? {} : off('Already the last step'))}
                      aria-label={`Move step ${i + 1} down`}
                      onClick={() => i < rows.length - 1 && commit(rows.map((x, j) => (j === i + 1 ? rows[i] : j === i ? rows[i + 1] : x)))}
                    >
                      ↓
                    </button>
                    <button type="button" class="icon-btn sm" aria-label={`Remove step ${i + 1}`} onClick={() => commit(rows.filter((_, j) => j !== i))}>
                      <Icon name="trash" />
                    </button>
                  </div>
                </div>
              ))
            ) : (
              <div class="muted small">No steps yet.</div>
            )}
          </div>
          <button type="button" class="btn sm" onClick={() => setRows([...rows, { do: '', type: '', arg: '' }])}>
            <Icon name="plus" />
            Add a step
          </button>
        </div>
        <div class="ferr" role="alert">
          {bad.join(' · ')}
        </div>
      </div>
    )
  }

  const backBtn = () => (
    <button class="icon-btn sm rp-back" aria-label="Back to the list" data-tip="The list" onClick={() => setPhList(true)}>
      <Icon name="chev" />
    </button>
  )

  async function toggleBlame() {
    if (blame) return setBlame(null)
    if (!key) return
    try {
      const r = await repoApi.blame(target, key)
      setBlame(r.groups || [])
    } catch (e) {
      say("Couldn't read who wrote it", humanError(e), 'fail')
    }
  }
  useEffect(() => {
    if (!blame) return
    const k = (e: KeyboardEvent) => e.key === 'Escape' && setBlame(null)
    document.addEventListener('keydown', k)
    return () => document.removeEventListener('keydown', k)
  }, [blame])

  const promoteTeam = !isTeam && data?.team && data.team_knowledge && kind && (kind.id === 'knowledge' || kind.id === 'runbook') ? data.team : ''
  function fileMenu(el: HTMLElement) {
    if (!kind || !key) return
    const items: MenuItem[] = []
    if (promoteTeam)
      items.push({
        label: `Promote to team ${promoteTeam}`,
        icon: 'upload',
        onClick: () => promote(),
      })
    if (!isTeam)
      items.push({
        label: 'Consolidate its log now',
        icon: 'commit',
        onClick: () => consolidate(),
      })
    if (items.length) items.push('-')
    if (!isTeam) {
      items.push({
        label: 'Pin (always in context)',
        icon: 'pin',
        disabled: 'Conductor has no pins: every entry is listed in the index the bot reads',
      })
      items.push('-')
    }
    items.push({
      label: 'Delete…',
      icon: 'trash',
      onClick: () =>
        setModal({
          t: 'confirm',
          title: `Delete this ${kind.one}?`,
          body: (
            <>
              One commit removes <span class="mono">{key}</span>. Revert that commit to bring it back.
            </>
          ),
          ok: 'Delete',
          danger: true,
          run: async () => {
            const p = key
            const r = await edit(
              {
                op: 'rm',
                path: p,
                base: data!.head,
                message: `Delete “${current?.title || p}”`,
              },
              `Deleted ${p}`,
              'revert from history to restore it',
            )
            if (r) {
              setKey(null)
              setDraft(null)
              const d = await load()
              const e = d && entriesOf(d, kind)[0]
              if (e) openEntry(e)
            }
          },
        }),
    })
    setMenu({ el, items })
  }
  async function promote() {
    if (!kind || !key || !promoteTeam) return
    const text = data!.files.find((f) => f.path === key)?.text || ''
    try {
      const t = await repoApi.load('team:' + promoteTeam)
      const r = await repoApi.edit('team:' + promoteTeam, {
        op: 'put',
        kind: kind.id,
        path: key,
        text,
        create: true,
        base: t.head,
        message: `Promote “${current?.title || key}” from ${name}`,
      })
      sessionStorage.setItem('repo-open:team:' + promoteTeam, key)
      say(`Promoted to team ${promoteTeam}`, `commit ${r.commit.slice(0, 7)} · ${key}`)
      location.hash = `#/teams/${promoteTeam}/knowledge`
    } catch (e) {
      say('Not promoted', humanError(e), 'fail')
    }
  }
  async function consolidate() {
    try {
      const r = await repoApi.consolidate(name)
      say(
        r.skipped ? 'Nothing to consolidate' : 'Consolidation started',
        r.skipped ? 'no new log entries since the last time' : `a worker is writing its digests${r.case ? ` · ${r.case}` : ''}`,
        r.skipped ? 'idle' : 'run',
      )
    } catch (e) {
      say('Not started', humanError(e), 'fail')
    }
  }

  function annotate(flag: boolean) {
    if (!key) return
    setModal({
      t: 'form',
      caps: key,
      title: flag ? 'Flag as wrong' : 'Annotate this log entry',
      intro: flag ? 'The entry stays as the bot wrote it; your flag and reason sit beside it, and the bot reads them.' : undefined,
      fields: [
        {
          k: 'text',
          type: 'text',
          req: true,
          label: flag ? 'What is wrong' : 'Note',
        },
      ],
      submit: flag ? 'Flag' : 'Add note',
      run: async (v) => {
        try {
          await repoApi.edit(target, {
            op: flag ? 'flag' : 'annotate',
            path: key,
            text: v.text,
            base: data!.head,
          })
          await load()
          say(flag ? 'Flagged as wrong' : 'Note added', 'appended with your name; the bot is told')
        } catch (e) {
          return humanError(e)
        }
      },
    })
  }

  function ReadEntry({ e }: { e: Entry }): JSX.Element {
    const k = e.kind
    const fl = e.fm.flagged_wrong ? String(e.fm.flagged_wrong) : ''
    const cs = e.fm.case ? String(e.fm.case) : ''
    return (
      <div class="panel repo-ed ke" data-kind={k.id}>
        <div class="panel-head ed-head">
          {backBtn()}
          <Tag cls="kind-tag" icon={k.icon}>
            {k.label}
          </Tag>
          <b class="grow ellipsis">{e.title}</b>
          {fl ? <Tag s="fail">flagged as wrong</Tag> : null}
          {cs && caseLink(cs) ? (
            <a class="btn sm ghost" href={caseLink(cs)}>
              <Icon name="runs" />
              case {cs}
            </a>
          ) : null}
        </div>
        <div class="ke-path">
          <span class="caps">path</span>
          <span class="mono">{e.path}</span>
          <span class="muted tiny">
            {k.id === 'log' ? `written by the bot, ${cs ? 'task ' + cs : e.fm.sweep ? 'sweep ' + String(e.fm.sweep) : 'per case'}` : 'system-generated'}
          </span>
        </div>
        <div class="callout small">
          <Icon name="lock" /> {k.readonly}
        </div>
        <div class="panel-body prose">
          <Markdown src={e.body} />
        </div>
        {k.annotate ? (
          <>
            {fl ? (
              <div class="callout danger small" style="margin:0 16px">
                <Icon name="warn" /> Flagged wrong: {fl}
              </div>
            ) : null}
            <div class="panel-body">
              <div class="row">
                <button class="btn sm" onClick={() => annotate(false)}>
                  <Icon name="edit" />
                  Add a note
                </button>
                <button class="btn sm secondary" onClick={() => annotate(true)}>
                  <Icon name="warn" />
                  Flag as wrong
                </button>
                <span class="muted tiny">appended below the bot's text with your name; nothing is rewritten</span>
              </div>
            </div>
          </>
        ) : null}
      </div>
    )
  }

  function Editor({ k }: { k: KindUI }): JSX.Element {
    const d = draft || { fm: {}, body: '', free: '' }
    const path = pathFor(k, d)
    const onDisk = key ? data?.files.find((f) => f.path === key)?.text : undefined
    const behind = !isNew && !dirty && onDisk != null && opened && onDisk !== opened
    const errs = isNew ? [] : validate(k.raw, current?.fm || {})
    const c = (k.id === 'knowledge' || k.id === 'runbook') && !isNew && key ? cites(key) : null
    const links = allEntries
      .filter((e) => e.path !== key && e.kind.dir)
      .map((e) => ({
        v: e.path.replace(/\.md$/, ''),
        label: e.title,
        kind: e.kind.one,
      }))
    const titleText = String(d.fm.title || d.fm.what || '')
    return (
      <div class="panel repo-ed ke" data-kind={k.id}>
        <div class="panel-head ed-head">
          {backBtn()}
          <Tag cls="kind-tag" icon={k.icon}>
            {k.label}
          </Tag>
          <b class="grow ellipsis" title={titleText}>
            {isNew ? `New ${k.one}` : titleText || path}
          </b>
          {dirty && !proposal ? (
            <span class="tag" data-s="wait" id="ke-dirty">
              unsaved
            </span>
          ) : null}
          {proposal ? <Tag s="wait">{`approving ${proposal.proposal} with changes`}</Tag> : null}
          <div class="tools">
            {isNew ? null : (
              <>
                <button
                  ref={blameBtn}
                  class={'btn sm ghost' + (blame ? ' on' : '')}
                  data-act="rp-blame"
                  aria-pressed={!!blame}
                  aria-label="Who wrote this"
                  onClick={toggleBlame}
                >
                  <Icon name="bots" />
                  <span class="rp-bl-t">Who wrote this</span>
                </button>
                {proposal ? null : (
                  <button class="btn sm ghost" aria-label="Entry actions" onClick={(e) => fileMenu(e.currentTarget)}>
                    <Icon name="more" />
                  </button>
                )}
              </>
            )}
          </div>
        </div>
        {blame && !isNew ? (
          <div class="rp-blame-pop" id="rp-blame-pop" role="dialog" aria-label="Who wrote this" tabIndex={-1} style="top:52px">
            <div class="rp-bp-head">
              <div class="grow">
                <b>Who wrote this</b>
                <div class="muted tiny">The saved file, each part beside the commit that last changed it</div>
              </div>
              <button type="button" class="icon-btn sm" aria-label="Close who wrote this" onClick={() => setBlame(null)}>
                <Icon name="x" />
              </button>
            </div>
            <div class="blame rp-bp-body">
              {blame.map((g) => (
                <div class={`bl-group ${g.kind}`}>
                  <div class="bl-who">
                    <WhoChip c={g} />
                    <span class="mono tiny muted">
                      {g.short} · {ago(g.date)}
                    </span>
                  </div>
                  <pre>{g.lines.join('\n')}</pre>
                </div>
              ))}
            </div>
          </div>
        ) : null}
        <div class="ed-body">
          <div class="ke-path">
            <span class="caps">path</span>
            <span class="mono" id="ke-path">
              {path}
            </span>
            <span class="muted tiny">
              {k.free
                ? `you choose the path inside ${k.dir}/`
                : `derived from the kind and the ${k.titleKey === 'what' ? '“what to watch”' : 'title'}; rename by changing it`}
            </span>
          </div>
          {readersLine()}
          {conflict ? (
            <div class="conflict">
              <div class="callout warn small">
                <b>Conflict.</b> {conflict.who.replace(/^./, (c) => c.toUpperCase())} committed <span class="mono">{conflict.sha}</span> to this entry after you
                opened it. Choose explicitly:
              </div>
              <div class="grid g2">
                <div>
                  <div class="caps">their change</div>
                  <DiffView before={conflict.base} after={conflict.theirs} file={key || ''} />
                </div>
                <div>
                  <div class="caps">your change</div>
                  <DiffView before={conflict.base} after={conflict.mine} file={key || ''} />
                </div>
              </div>
              <div class="row wrap" style="margin-top:8px">
                <button class="btn primary" onClick={rebase}>
                  <Icon name="branch" />
                  Rebase my edit on theirs
                </button>
                <button class="btn secondary" onClick={() => save(true)}>
                  Overwrite theirs with mine
                </button>
                <button
                  class="btn ghost"
                  onClick={() => {
                    const e = current
                    if (e) openEntry(e)
                  }}
                >
                  Discard mine, keep theirs
                </button>
              </div>
            </div>
          ) : behind ? (
            <div class="callout info small">
              A newer commit changed this entry.{' '}
              <button class="link" onClick={() => current && openEntry(current)}>
                Load it
              </button>
            </div>
          ) : null}
          {errs.length && !proposal ? (
            <div class="callout danger small">
              This entry fails the {k.one} schema: {errs.join('; ')}. Fix it and save.
            </div>
          ) : null}
          <form class="ke-form stack" id="ke-form" data-pre="ke-" noValidate onSubmit={(e) => e.preventDefault()}>
            {k.free ? (
              <div class="field" data-field="free">
                <label for="ke-free">
                  Path in {k.dir}/ <span class="opt">this collection allows free paths</span>
                </label>
                <div class="ke-free">
                  <span class="mono muted">{k.dir}/</span>
                  <input
                    class="input mono"
                    id="ke-free"
                    value={d.free}
                    placeholder={slug(titleText || 'my-entry')}
                    aria-label={`Path inside ${k.dir}`}
                    onInput={(e) => setDraft({ ...d, free: e.currentTarget.value.trim() })}
                  />
                  <span class="mono muted">.md</span>
                </div>
                <div class="ferr" role="alert"></div>
              </div>
            ) : null}
            {k.fields.map((f) => FieldRow({ f }))}
            {k.body ? (
              <div class="field">
                <label>{k.body}</label>
                <MdEditor
                  id="rp-md"
                  text={d.body}
                  label={k.body}
                  placeholder="What is true, and why you believe it"
                  links={links}
                  onChange={(t) => setDraft((x) => (x ? { ...x, body: t } : x))}
                />
              </div>
            ) : null}
          </form>
          {c ? (
            <div class="cites small">
              <div class="caps">{k.id === 'runbook' ? 'used in' : 'cited by'}</div>
              {c.runbooks.map((x) => (
                <a
                  class="cite-link"
                  href="#"
                  onClick={(e) => {
                    e.preventDefault()
                    goHit(x.repo, x.path)
                  }}
                >
                  <Icon name="workflows" />
                  {x.title}
                </a>
              ))}
              {c.cases.map((x) =>
                caseLink(x) ? (
                  <a class="cite-link" href={caseLink(x)}>
                    <Icon name="runs" />
                    {tickets[x]?.title || 'a ticket'}
                  </a>
                ) : (
                  <span class="cite-link">
                    <Icon name="runs" />
                    {x}
                  </span>
                ),
              )}
              {c.logs.map((x) => (
                <a
                  class="cite-link"
                  href="#"
                  onClick={(e) => {
                    e.preventDefault()
                    goHit(x.repo, x.path)
                  }}
                >
                  <Icon name="clock" />
                  {x.title}
                </a>
              ))}
              {!c.runbooks.length && !c.cases.length && !c.logs.length ? <span class="muted">nothing yet</span> : null}
            </div>
          ) : null}
          <div class={'form-err callout danger small' + (schemaErrs.length || saveErr ? '' : ' hide')} id="ke-err" role="alert">
            {saveErr || (schemaErrs.length ? `Fails the ${k.one} schema: ${schemaErrs.join('; ')}` : '')}
          </div>
        </div>
        <div class="ed-bar">
          <div class="panel-foot ed-foot">
            <input
              class="input grow"
              id="rp-msg"
              placeholder="Commit message (optional)"
              aria-label="Commit message"
              value={msg}
              onInput={(e) => setMsg(e.currentTarget.value)}
            />
            <button
              class="btn sm ghost"
              {...((dirty || isNew) && !proposal ? {} : off('No unsaved changes'))}
              onClick={() => (dirty || isNew) && !proposal && discard()}
            >
              {isNew ? 'Cancel' : 'Discard'}
            </button>
            <button
              class="btn primary"
              {...(dirty && !busy ? {} : off(isNew ? 'Fill in the fields first' : 'Nothing to save yet: change a field first'))}
              onClick={() => dirty && !busy && save()}
            >
              <Icon name="commit" />
              {proposal ? 'Approve with changes' : busy ? 'Saving…' : 'Save · one commit'}
            </button>
          </div>
          <div class="muted tiny ed-sign">
            {proposal
              ? "Saving approves the bot's proposal with your changes, as one commit in your name."
              : 'Saving makes one commit in your name, marked as edited here. Your own edits need no approval.'}
          </div>
        </div>
      </div>
    )
  }

  function discard() {
    if (isNew && !dirty) {
      setIsNew(false)
      setDraft(null)
      const e = kind && entries(kind)[0]
      if (e) openEntry(e)
      return
    }
    setModal({
      t: 'confirm',
      title: isNew ? 'Discard this new entry?' : 'Discard your edits?',
      body: isNew ? (
        'Nothing was saved.'
      ) : (
        <>
          Puts <span class="mono">{key}</span> back to the last commit.
        </>
      ),
      ok: 'Discard',
      danger: true,
      run: () => {
        if (isNew) {
          setIsNew(false)
          setDraft(null)
          const e = kind && entries(kind)[0]
          if (e) openEntry(e)
        } else if (current) openEntry(current)
      },
    })
  }

  // rebase: every field you changed (against what you opened) wins; the rest is theirs; the body merges line by line
  function rebase() {
    if (!conflict || !kind || !draft || !current) return
    const pb = parseDoc(conflict.base)
    const pt = parseDoc(conflict.theirs)
    const fm: FM = { ...pt.fm }
    for (const f of kind.fields) if (JSON.stringify(draft.fm[f.k]) !== JSON.stringify(pb.fm[f.k])) fm[f.k] = draft.fm[f.k]
    const out = (pt.body || '').split('\n')
    let anchor: string | null = null
    const a = (pb.body || '').split('\n')
    const b = (draft.body || '').split('\n')
    // the lines you removed go; the lines you added follow the line they followed
    const removed = a.filter((l) => !b.includes(l))
    for (const l of removed) {
      const i = out.indexOf(l)
      if (i >= 0) out.splice(i, 1)
    }
    for (const l of b) {
      if (a.includes(l)) {
        anchor = l
        continue
      }
      const i = anchor == null ? -1 : out.lastIndexOf(anchor)
      out.splice(i + 1, 0, l)
      anchor = l
    }
    openEntry(current, { fm, body: out.join('\n'), free: draft.free })
    say('Rebased on their commit', 'review the result, then save', 'run')
  }

  function RawFile({ p }: { p: string }): JSX.Element {
    const f = data!.files.find((x) => x.path === p)
    const k = kinds.find((x) => x.id === f?.kind)
    return (
      <div class="panel repo-ed">
        <div class="panel-head ed-head">
          {backBtn()}
          <span class="mono small grow ellipsis" title={p}>
            {p}
          </span>
          {k ? <Tag>{k.label}</Tag> : <Tag s="wait">untyped</Tag>}
          <div class="tools">
            {k && f?.status !== 'unmapped' ? (
              <button
                class="btn sm"
                onClick={() => {
                  const e = entries(k).find((x) => x.path === p)
                  if (e) openEntry(e)
                }}
              >
                Open as {k.one}
              </button>
            ) : (
              <button
                class="btn sm primary"
                onClick={() =>
                  setModal({
                    t: 'map',
                    docs: [{ name: p, text: f?.text || '' }],
                  })
                }
              >
                Map into a kind…
              </button>
            )}
          </div>
        </div>
        <div class="muted small" style="padding:8px 16px 0">
          The files view is read-mostly: entries are edited through their kind, so they keep their fields and their place.
          {f?.problem ? ` ${f.problem}.` : ''}
        </div>
        <pre class="raw mono">{f?.text ?? 'Too large to show here.'}</pre>
      </div>
    )
  }

  // ---------- Suggested by bots (a team) ----------

  const pkind = (p: KnowledgeProposal) => kinds.find((k) => k.id === p.kind) || kinds.find((k) => k.id === 'knowledge')
  const perrs = (p: KnowledgeProposal) => {
    const k = pkind(p)
    const x = parseDoc(p.text || '')
    return x.err ? [x.err] : k ? validate(k.raw, x.fm) : []
  }
  async function decide(ps: KnowledgeProposal[], d: 'approve' | 'deny', reason = '') {
    let n = 0
    for (const p of ps) {
      try {
        await decideKnowledge(p.bot, p.proposal, d, reason)
        n++
      } catch (e) {
        const t = humanError(e)
        if (!/cancel|superseded/i.test(t)) say(`${p.proposal} wasn't ${d === 'approve' ? 'approved' : 'denied'}`, t, 'fail')
        break
      }
    }
    setSel(new Set())
    const dd = await load()
    if (n && d === 'approve' && dd) {
      const first = ps[0]
      const k = pkind(first)
      const e = k && entriesOf(dd, k).find((x) => x.path === first.to)
      if (e) openEntry(e)
    }
    if (n)
      say(
        d === 'approve' ? (n === 1 ? `Approved ${ps[0].proposal}` : `Approved ${n}`) : n === 1 ? 'Denied' : `Denied ${n}`,
        d === 'approve' ? (n === 1 ? `written to ${ps[0].to}` : 'one commit each, the bot as author') : 'the bot is told why',
        d === 'approve' ? 'ok' : 'fail',
      )
  }
  const denyForm = (ps: KnowledgeProposal[]) =>
    setModal({
      t: 'form',
      caps: `team:${name} · ${ps.map((p) => p.proposal).join(', ')}`,
      title: ps.length > 1 ? `Deny ${ps.length} proposals` : `Deny ${ps[0].proposal}`,
      fields: [
        {
          k: 'reason',
          type: 'text',
          req: true,
          label: 'Reason (goes back to the proposing bot)',
          ph: 'e.g. Already covered in knowledge/alb.md; link to it instead.',
        },
      ],
      submit: 'Deny',
      run: (v) => {
        setTimeout(() => decide(ps, 'deny', v.reason), 0)
      },
    })
  function approveWithChanges(p: KnowledgeProposal) {
    guardThen(() => {
      const k = pkind(p)
      if (!k) return
      const x = parseDoc(p.text || '')
      const existing = entries(k).find((e) => e.path === p.to)
      const d = { fm: x.fm, body: x.body, free: '' }
      if (existing) openEntry(existing, d)
      else newEntry(k, d)
      setProposal(p)
      setPhList(false)
      const errs = validate(k.raw, x.fm)
      if (errs.length) setSchemaErrs(errs)
      setTimeout(() => document.querySelector('.repo-ed')?.scrollIntoView({ block: 'start' }), 40)
    })
  }
  const pending = props.filter((p) => p.status === 'pending')
  function Proposals(): JSX.Element {
    const selP = pending.filter((p) => sel.has(p.bot + '/' + p.proposal))
    return (
      <div class="panel" style="margin-bottom:14px" data-testid="kn-proposals">
        <div class="panel-head">
          <h3>Suggested by bots</h3>
          <span class="muted small">each is checked against its kind's schema</span>
          <div class="tools">
            <button
              class="btn sm"
              {...(selP.length ? {} : off('Tick proposals first'))}
              onClick={() => {
                if (!selP.length) return
                const bad = selP.filter((p) => perrs(p).length)
                if (bad.length)
                  return say(
                    `${bad.map((p) => p.proposal).join(', ')} fail${bad.length > 1 ? '' : 's'} the schema`,
                    'approve with changes or deny; untick to approve the rest',
                    'fail',
                  )
                decide(selP, 'approve')
              }}
            >
              {usesPasskey() ? <Icon name="passkey" /> : null}
              Approve selected{selP.length ? ` (${selP.length})` : ''}
            </button>
            <button class="btn sm secondary" {...(selP.length ? {} : off('Tick proposals first'))} onClick={() => selP.length && denyForm(selP)}>
              Deny selected
            </button>
          </div>
        </div>
        <div class="list">
          {props.length ? (
            props.map((p) => {
              const k = pkind(p)
              const x = parseDoc(p.text || '')
              const errs = p.status === 'pending' ? perrs(p) : []
              const id = p.bot + '/' + p.proposal
              const approved = /^(approved|executed|landed)/.test(p.status)
              return (
                <div class="item kprop" style="align-items:flex-start" data-prop={p.proposal}>
                  {p.status === 'pending' ? (
                    <input
                      type="checkbox"
                      class="cbx"
                      checked={sel.has(id)}
                      aria-label={`Select ${p.proposal}`}
                      onChange={(e) => {
                        const n = new Set(sel)
                        if (e.currentTarget.checked) n.add(id)
                        else n.delete(id)
                        setSel(n)
                      }}
                    />
                  ) : (
                    <StatusMark s={approved ? 'ok' : 'fail'} word="" />
                  )}
                  <div class="grow small">
                    <div class="row wrap" style="gap:6px">
                      <Tag cls="kind-tag" icon={k?.icon || 'sparkle'}>
                        {k?.label || p.kind || 'Knowledge'}
                      </Tag>
                      <b>{p.note || p.summary}</b>
                    </div>
                    <FactsLine
                      facts={[
                        {
                          icon: 'obj-bot',
                          value: p.bot,
                          href: `#/bots/${p.bot}`,
                          pri: 1,
                          label: 'Proposed by',
                        },
                        { icon: 'file', value: p.to, pri: 2, label: 'Goes to' },
                        p.case && p.case_title
                          ? {
                              icon: 'ticket',
                              value: p.case_title,
                              href: caseLink(p.case) || undefined,
                              pri: 3,
                              label: 'For the ticket',
                            }
                          : null,
                        {
                          icon: 'clock',
                          value: ago(p.created),
                          pri: 2,
                          label: 'Proposed',
                        },
                        p.approver
                          ? {
                              icon: 'obj-approval',
                              value: `${p.approver.replace(/^operator:/, '')}${p.edited_by ? ', with changes' : ''}`,
                              pri: 3,
                              label: 'Decided by',
                            }
                          : null,
                      ]}
                    />
                    {p.reason && p.status !== 'pending' ? (
                      <div class="tiny">
                        <span class="st sm" data-s="fail">
                          <StatusMark s="fail" word={`denied: ${p.reason}`} />
                        </span>
                      </div>
                    ) : null}
                    {errs.length ? (
                      <div class="schema-errs">
                        <b>Fails the {k?.one || 'knowledge entry'} schema</b>
                        <ul>
                          {errs.map((e) => (
                            <li class="mono">{e}</li>
                          ))}
                        </ul>
                      </div>
                    ) : null}
                    {p.status === 'pending' && !errs.length ? (
                      p.current != null ? (
                        <DiffView before={p.current} after={p.text || ''} file={p.to} />
                      ) : (
                        <div class="prop-fields">
                          {(k?.fields || [])
                            .filter((f) => x.fm[f.k] != null && f.type !== 'steps')
                            .map((f) => (
                              <div>
                                <span class="caps">{f.label || f.k.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())}</span>{' '}
                                {f.type === 'evidence'
                                  ? evidenceWords(x.fm[f.k])
                                  : Array.isArray(x.fm[f.k])
                                    ? (x.fm[f.k] as string[]).join(', ')
                                    : String(x.fm[f.k])}
                              </div>
                            ))}
                        </div>
                      )
                    ) : null}
                  </div>
                  {p.status === 'pending' ? (
                    <div class="row wrap kp-acts" style="gap:4px">
                      <button
                        class="btn sm primary"
                        {...(errs.length
                          ? off(
                              `Fails the ${k?.one || 'knowledge entry'} schema: ${errs[0]}${errs.length > 1 ? ` (+${errs.length - 1} more)` : ''}. Approve it with changes, or deny it.`,
                            )
                          : {})}
                        onClick={() => !errs.length && decide([p], 'approve')}
                      >
                        {usesPasskey() ? <Icon name="passkey" /> : null}
                        Approve
                      </button>
                      <button class="btn sm" onClick={() => approveWithChanges(p)}>
                        <Icon name="edit" />
                        Approve with changes
                      </button>
                      <button class="btn sm secondary" onClick={() => denyForm([p])}>
                        Deny
                      </button>
                    </div>
                  ) : (
                    <Tag s={approved ? 'ok' : 'fail'}>{p.status}</Tag>
                  )}
                </div>
              )
            })
          ) : (
            <div class="item small muted">No proposals.</div>
          )}
        </div>
      </div>
    )
  }

  // ---------- New…, import, collections ----------

  function NewModal(): JSX.Element {
    return (
      <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && setModal(null)}>
        <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1}>
          <span class="sheet-grip only-phone" aria-hidden="true"></span>
          <div class="modal-head">
            <div>
              <div class="caps">{target}</div>
              <h2>New…</h2>
            </div>
            <button class="icon-btn" aria-label="Close" onClick={() => setModal(null)}>
              <Icon name="x" />
            </button>
          </div>
          <div class="modal-body stack">
            <p class="small dim" style="margin:0">
              Pick what you're writing. Its kind decides where it lives and what it needs: knowledge goes in knowledge/, runbooks in runbooks/.
            </p>
            <div class="choice-grid">
              {kinds.map((k) => (
                <button
                  type="button"
                  class="choice"
                  aria-pressed="false"
                  {...(k.readonly ? off(k.readonly) : {})}
                  onClick={() => {
                    if (k.readonly) return
                    setModal(null)
                    newEntry(k)
                  }}
                >
                  <span class="choice-ic">
                    <Icon name={k.icon} />
                  </span>
                  <span class="choice-body">
                    <span class="choice-title">{k.coll ? k.label : k.one.replace(/^./, (c) => c.toUpperCase())}</span>
                    <span class="choice-desc">{`${k.dir}/ · ${k.fields.map((f) => f.k.replace(/_/g, ' ')).join(', ')}${k.free ? ' · free paths' : ''}`}</span>
                    {k.readonly ? <span class="choice-note">{k.readonly}</span> : null}
                  </span>
                  <span class="choice-check" aria-hidden="true">
                    <Icon name="check" />
                  </span>
                </button>
              ))}
            </div>
          </div>
          <div class="modal-foot">
            <button class="btn" onClick={() => setModal({ t: 'import' })}>
              <Icon name="upload" />
              Import markdown…
            </button>
            <button class="btn ghost" onClick={() => collectionForm()}>
              Declare a collection…
            </button>
            <button class="btn ghost" onClick={() => setModal(null)}>
              Cancel
            </button>
          </div>
        </div>
      </div>
    )
  }

  // a collection is config: staged on the team or bot, reviewed and applied like any change
  function collectionForm() {
    setModal({
      t: 'form',
      caps: `${isTeam ? 'teams' : 'bots'}.${name}.collections`,
      title: 'New collection',
      intro: `A kind of entry of your own for ${isTeam ? `team ${name}'s knowledge` : `${name}'s memory`}: its own directory, fields and template. It's config, so it goes through the review.`,
      fields: [
        {
          k: 'name',
          type: 'string',
          req: true,
          pattern: /^[a-z][a-z0-9_-]{0,39}$/,
          patternMsg: 'lowercase letters, digits, _ and -, up to 40',
          validate: (v) => (kinds.some((k) => k.id === v) ? 'There is already a kind by that name.' : ''),
        },
        {
          k: 'dir',
          type: 'string',
          label: 'Directory',
          ph: 'defaults to the name',
          validate: (v) =>
            v && !/^[a-z][a-z0-9_-]*(\/[a-z0-9_-]+)*\/?$/.test(v)
              ? 'A relative folder such as notes/vendors'
              : v && kinds.some((k) => k.dir === v.replace(/\/$/, '').split('/')[0])
                ? `${v.split('/')[0]}/ belongs to another kind`
                : '',
        },
        {
          k: 'fields',
          type: 'text',
          label: 'Fields (besides title)',
          ph: 'severity!:enum(sev1|sev2|sev3)',
          help: 'name:type, one per line; type is string, text, list, steps, number, bool or enum(a|b|c). Add ! after the name for a required field.',
          validate: (v) => {
            const bad = (v || '')
              .split('\n')
              .map((l) => l.trim())
              .filter(Boolean)
              .find((l) => !/^([a-z][a-z0-9_]*)(!)?:(string|text|list|steps|number|bool|enum\(([\w-]+(\|[\w-]+)*)\))$/.test(l))
            return bad ? `"${bad}" is not name:type (or name!:type)` : ''
          },
        },
        {
          k: 'template',
          type: 'text',
          label: 'Body template',
          ph: '## Timeline\n\n## Root cause\n',
        },
        {
          k: 'free_paths',
          type: 'seg',
          label: 'Paths',
          options: [
            ['', 'follow the title'],
            ['yes', 'writers choose'],
          ],
          def: '',
        },
        {
          k: 'writers',
          type: 'enum',
          req: true,
          label: 'Who writes',
          options: isTeam
            ? [
                ['operator', 'operators (you, from the dashboard)'],
                ['operator,proposal', 'operators, and bots propose'],
              ]
            : [
                ['operator', 'operators (you, from the dashboard)'],
                ['operator,bots', 'operators and the bot'],
              ],
        },
      ],
      submit: 'Review change',
      run: async (v) => {
        const n: Record<string, unknown> = {}
        const dir = (v.dir || '').replace(/\/$/, '')
        if (dir && dir !== v.name) n.dir = dir
        const fields: Record<string, unknown> = {}
        for (const l of (v.fields || '')
          .split('\n')
          .map((x) => x.trim())
          .filter(Boolean)) {
          const m = l.match(/^([a-z][a-z0-9_]*)(!)?:(string|text|list|steps|number|bool|enum\(([\w-]+(\|[\w-]+)*)\))$/)!
          const fd: Record<string, unknown> = {
            type: m[3].startsWith('enum') ? 'enum' : m[3],
          }
          if (m[2]) fd.required = true
          if (fd.type === 'enum') fd.values = m[4].split('|')
          fields[m[1]] = fd
        }
        if (Object.keys(fields).length) n.fields = fields
        if (v.template) n.template = v.template
        if (v.free_paths === 'yes') n.free_paths = true
        n.writers = (v.writers || 'operator').split(',')
        await stage([isTeam ? 'teams' : 'bots', name, 'collections', v.name], n, `${isTeam ? 'team' : 'bot'} ${name}: collection ${v.name}`)
        say(`Staged collection ${v.name}`, 'review and apply it on the Config screen', 'run')
      },
    })
  }

  // import: markdown mapped into a kind (the prototype's R.detect / R.convert)
  function ImportModal({ docs: pre }: { docs?: { name: string; text: string }[] }): JSX.Element {
    const [mode, setMode] = useState<'drop' | 'paste' | 'repo'>('drop')
    const [picked, setPicked] = useState<{ name: string; text: string }[]>(pre || [])
    const [paste, setPaste] = useState('')
    const [note, setNote] = useState('')
    const [over, setOver] = useState(false)
    const take = (fs: FileList | null) => {
      if (!fs) return
      Promise.all([...fs].map((f) => f.text().then((text) => ({ name: f.name, text })))).then((xs) => setPicked((p) => p.concat(xs)))
    }
    const go = () => {
      if (mode === 'drop') {
        if (!picked.length) return setNote('Choose or drop a file first.')
        setModal({ t: 'map', docs: picked })
      } else if (mode === 'paste') {
        if (!paste.trim()) return setNote('Paste some markdown first.')
        setModal({ t: 'map', docs: [{ name: 'pasted.md', text: paste }] })
      } else setNote('Copy the file from the repo and paste it, or download it and drop it here.')
    }
    return (
      <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && setModal(null)}>
        <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1}>
          <span class="sheet-grip only-phone" aria-hidden="true"></span>
          <div class="modal-head">
            <div>
              <div class="caps">{target}</div>
              <h2>Import markdown</h2>
            </div>
            <button class="icon-btn" aria-label="Close" onClick={() => setModal(null)}>
              <Icon name="x" />
            </button>
          </div>
          <div class="modal-body stack">
            <Seg
              items={[
                ['drop', 'Drop files'],
                ['paste', 'Paste'],
                ['repo', 'From a repo'],
              ]}
              value={mode}
              onPick={(v) => {
                setMode(v as typeof mode)
                setNote('')
              }}
            />
            {mode === 'drop' ? (
              <div class="up-pane">
                <label
                  class={'dropzone' + (over ? ' over' : '')}
                  id="up-drop"
                  onDragOver={(e) => {
                    e.preventDefault()
                    setOver(true)
                  }}
                  onDragLeave={() => setOver(false)}
                  onDrop={(e) => {
                    e.preventDefault()
                    setOver(false)
                    take(e.dataTransfer?.files || null)
                  }}
                >
                  <Icon name="upload" />
                  <span>
                    <b>Drop markdown files here</b> or choose them
                  </span>
                  <input
                    type="file"
                    id="up-file"
                    accept=".md,.markdown,.txt,text/markdown,text/plain"
                    multiple
                    aria-label="Choose markdown files"
                    onChange={(e) => take(e.currentTarget.files)}
                  />
                </label>
                <div id="up-list" class="small muted">
                  {picked.map((f) => (
                    <div class="mono">
                      {f.name} · {f.text.length} chars
                    </div>
                  ))}
                  {note}
                </div>
              </div>
            ) : mode === 'paste' ? (
              <div class="up-pane">
                <div class="field">
                  <label>Paste markdown</label>
                  <MdEditor id="up-text" text={paste} label="Paste markdown" placeholder="Paste a markdown file here" onChange={setPaste} />
                </div>
                {note ? <div class="small muted">{note}</div> : null}
              </div>
            ) : (
              <div class="up-pane">
                <div class="hint">Conductor doesn't fetch a file from a repo here. Copy it from the repo and paste it, or download it and drop it in.</div>
                {note ? <div class="small muted">{note}</div> : null}
              </div>
            )}
            <div class="callout small">Next you confirm which kind each file becomes. Nothing is placed by path.</div>
          </div>
          <div class="modal-foot">
            <button class="btn ghost" onClick={() => setModal(null)}>
              Cancel
            </button>
            <button class="btn primary" onClick={go}>
              Next: map into kinds <Icon name="chev" />
            </button>
          </div>
        </div>
      </div>
    )
  }

  function MapModal({ docs }: { docs: { name: string; text: string }[] }): JSX.Element {
    const choices = kinds.filter((k) => !k.readonly && k.id !== 'watch')
    const [st, setSt] = useState(() => docs.map((d) => ({ ...d, kind: detect(d.text) })))
    const [busyI, setBusyI] = useState(false)
    const go = async () => {
      setBusyI(true)
      const todo: { k: KindUI; d: Draft }[] = []
      let head = data!.head
      let n = 0
      try {
        for (const x of st) {
          const k = choices.find((c) => c.id === x.kind) || choices[0]
          const d = convert(k, x.text, x.name)
          const path = derive(k, titleOf(k, d.fm), '')
          if (validate(k.raw, d.fm).length || data!.files.some((f) => f.path === path)) {
            todo.push({ k, d })
            continue
          }
          const r = await repoApi.create(target, {
            kind: k.id,
            text: emitDoc(k.raw, d.fm, d.body),
            base: head,
            message: `Import ${x.name}`,
            slug: slug(titleOf(k, d.fm)),
          })
          head = r.commit
          n++
        }
      } catch (e) {
        setBusyI(false)
        say('Import stopped', humanError(e), 'fail')
        return
      }
      setModal(null)
      const dd = await load()
      if (todo.length) {
        newEntry(todo[0].k, todo[0].d)
        setSchemaErrs(validate(todo[0].k.raw, todo[0].d.fm))
        say(
          n ? `Imported ${n}; ${todo.length} need${todo.length > 1 ? '' : 's'} fields` : "Fill in what the import couldn't find",
          'the first one is open: complete it and save',
          'run',
        )
      } else {
        const k = choices.find((c) => c.id === st[0].kind) || choices[0]
        const e = dd && entriesOf(dd, k).find((x) => x.path === derive(k, titleOf(k, convert(k, st[0].text, st[0].name).fm), ''))
        if (e) openEntry(e)
        say(`Imported ${n}`, 'one commit each, by you')
      }
    }
    return (
      <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && setModal(null)}>
        <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1}>
          <span class="sheet-grip only-phone" aria-hidden="true"></span>
          <div class="modal-head">
            <div>
              <div class="caps">{target} · import</div>
              <h2>Map into a kind</h2>
            </div>
            <button class="icon-btn" aria-label="Close" onClick={() => setModal(null)}>
              <Icon name="x" />
            </button>
          </div>
          <div class="modal-body stack" id="im-body">
            <p class="small dim" style="margin:0">
              Each file becomes one entry of the kind you confirm; its path follows from the kind and its title.
            </p>
            {st.map((x, i) => {
              const k = choices.find((c) => c.id === x.kind) || choices[0]
              if (!k) return null
              const d = convert(k, x.text, x.name)
              const errs = validate(k.raw, d.fm)
              return (
                <div class="im-row">
                  <div class="row wrap">
                    <b class="mono small">{x.name}</b>
                    <span class="muted tiny">detected: {META[detect(x.text)]?.one}</span>
                    <label class="row small" style="margin-left:auto">
                      as{' '}
                      <select
                        class="select sm"
                        aria-label={`Kind for ${x.name}`}
                        value={x.kind}
                        onChange={(e) => setSt(st.map((y, j) => (j === i ? { ...y, kind: e.currentTarget.value } : y)))}
                      >
                        {choices.map((c) => (
                          <option value={c.id}>{c.coll ? c.label : c.one}</option>
                        ))}
                      </select>
                    </label>
                  </div>
                  <div class="mono tiny">→ {derive(k, titleOf(k, d.fm), '')}</div>
                  <div class="tiny muted">
                    {summaryOf(k, d)}
                    {errs.length ? (
                      <>
                        {' · '}
                        <span class="danger-text">
                          {errs.length} field{errs.length > 1 ? 's' : ''} to fill
                        </span>
                      </>
                    ) : (
                      <>
                        {' · '}
                        <span class="ok-text">ready</span>
                      </>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
          <div class="modal-foot">
            <button class="btn ghost" onClick={() => setModal(null)}>
              Cancel
            </button>
            <button class="btn primary" {...(busyI ? off('Importing…') : {})} onClick={() => !busyI && go()}>
              <Icon name="commit" />
              Import
            </button>
          </div>
        </div>
      </div>
    )
  }

  // ---------- render ----------

  if (err && !data) {
    return (
      <div class="repo" data-repo={target}>
        <div class="panel">
          <div class="empty">
            <span class="serif">Couldn't open {isTeam ? 'its knowledge' : 'its memory'}</span>
            {err}
            <button class="btn" onClick={() => load(false)}>
              Try again
            </button>
          </div>
        </div>
      </div>
    )
  }
  if (!data || !kind) {
    return (
      <div class="repo" data-repo={target}>
        <p class="muted small">Loading…</p>
      </div>
    )
  }

  const fit = !isTeam
  const ph = phList || !(key || isNew || (view === 'files' && file))
  const files = data.files
    .filter((f) => visible(f.path))
    .map((f) => f.path)
    .sort()
  const curEntry = isNew ? null : current
  const mainPane =
    view === 'files' && file ? (
      RawFile({ p: file })
    ) : (key && curEntry) || isNew ? (
      kind.readonly && !isNew && curEntry ? (
        ReadEntry({ e: curEntry })
      ) : (
        Editor({ k: kind })
      )
    ) : (
      <div class="panel">
        <div class="empty">
          <span class="serif">No {kind.label.toLowerCase()} yet</span>
          <span>{kind.who[isTeam ? 'team' : 'bot'] || ''}</span>
          {kind.readonly ? null : (
            <button class="btn primary" onClick={() => newEntry(kind)}>
              <Icon name="plus" />
              New {kind.one}
            </button>
          )}
        </div>
      </div>
    )
  const sideTitle = isTeam ? 'Team knowledge' : 'What it knows'
  const histRows = hist || []

  return (
    <div class="repo" data-repo={target} data-testid="repo-view">
      {isTeam ? Proposals() : null}
      <div class={'grid repo-grid' + (fit ? ` fit ${ph ? 'ph-list' : 'ph-entry'}` : '')} data-repo={target}>
        <div class="rp-rail">
          {fit && (key || isNew) ? (
            <button type="button" class="rp-resume" onClick={() => setPhList(false)}>
              <Icon name="edit" />
              <span class="grow ellipsis">{isNew ? `Back to the new ${kind.one}` : `Back to ${curEntry?.title || 'the entry'}`}</span>
              {dirty ? <Tag s="wait">unsaved</Tag> : null}
              <Icon name="chev" />
            </button>
          ) : null}
          <div class="panel repo-side">
            <div class="panel-head rp-side-head">
              <h3 class="ellipsis">{sideTitle}</h3>
              <button class="btn sm" title="Write a new entry, or import markdown" onClick={() => guardThen(() => setModal({ t: 'new' }))}>
                <Icon name="plus" />
                New…
              </button>
              <div class="tiny muted ellipsis rp-side-repo" title={title || target}>
                {nEntries} entr{nEntries === 1 ? 'y' : 'ies'} · {isTeam && data.repo ? <span class="mono">{data.repo}</span> : 'its own repo'}
              </div>
            </div>
            <div class="panel-body tight stack tight rp-find" style="padding:8px">
              <FilterBar
                q={q}
                onQueryChange={setQ}
                placeholder={isTeam ? 'Find in team knowledge' : 'Find in its memory'}
                ariaLabel="Find and filter knowledge"
                menus={[
                  {
                    key: 'about',
                    label: 'About',
                    multi: true,
                    title: 'What it applies to',
                    selected: tags,
                    options: tagList.map((t) => ({
                      v: t,
                      label: t,
                      n: allEntries.filter((e) => Array.isArray(e.fm.applies_to) && (e.fm.applies_to as string[]).includes(t)).length,
                    })),
                    onToggle: (v) => setTags(tags.includes(v) ? tags.filter((x) => x !== v) : [...tags, v]),
                    onClear: () => setTags([]),
                  },
                ]}
              />
              <Seg
                items={[
                  ['kinds', 'By kind'],
                  ['files', 'Files'],
                ]}
                value={view}
                onPick={(v) => guardThen(() => setView(v as 'kinds' | 'files'))}
              />
            </div>
            {q && view === 'kinds' ? SearchHits() : null}
            {view === 'files' ? (
              <div class="tree" style="padding:6px">
                {files.length ? (
                  files.map((p) => {
                    const f = data.files.find((x) => x.path === p)
                    return (
                      <a
                        href="#"
                        class={'f' + (p === file ? ' on' : '')}
                        data-path={p}
                        onClick={(e) => {
                          e.preventDefault()
                          setFile(p)
                          setPhList(false)
                        }}
                      >
                        <Icon name="file" />
                        <span class="mono small ellipsis" title={p}>
                          {p}
                        </span>
                        {f?.kind && f.status !== 'unmapped' ? null : <Tag>untyped</Tag>}
                      </a>
                    )
                  })
                ) : (
                  <div class="muted small" style="padding:8px">
                    Empty.
                  </div>
                )}
                <div class="muted tiny" style="padding:8px">
                  Read-mostly: edit entries by kind.
                </div>
              </div>
            ) : (
              <>
                <div class="kinds">{kinds.map((k) => KindRow({ k }))}</div>
                <div class="dl-sub rp-coll">
                  <a
                    class="dl-link"
                    href="#"
                    onClick={(e) => {
                      e.preventDefault()
                      collectionForm()
                    }}
                  >
                    Your own kinds of entry…
                  </a>
                </div>
              </>
            )}
          </div>
          <div class="panel repo-hist">
            <div class="panel-head">
              <h3>History</h3>
              <div class="tools">
                <Seg
                  items={[
                    ['file', 'this entry'],
                    ['repo', 'repo'],
                  ]}
                  value={histMode}
                  onPick={(v) => setHistMode(v as 'file' | 'repo')}
                />
              </div>
            </div>
            <div class="list">
              {histRows.length ? (
                histRows.map((c) => (
                  <div class="item rp-commit" style="align-items:flex-start">
                    <div class="grow small">
                      <div class="rp-subj" title={c.subject}>
                        {humanSubject(c.subject)}
                      </div>
                      <div class="tiny rp-commit-by">
                        <WhoChip c={c} /> <span class="muted">· {ago(c.date)}</span>{' '}
                        <span class="mono muted" title="Commit">
                          {c.short}
                        </span>
                      </div>
                      {histMode === 'repo' ? <div class="mono tiny muted">{(c.files || []).filter(visible).join(', ')}</div> : null}
                    </div>
                    <button class="icon-btn sm" aria-label={`Diff ${c.short}`} data-tip="Diff" onClick={() => showDiff(c)}>
                      <Icon name="eye" />
                    </button>
                    <button class="icon-btn sm" aria-label={`Revert ${c.short}`} data-tip="Revert as a new commit" onClick={() => revert(c)}>
                      <Icon name="undo" />
                    </button>
                  </div>
                ))
              ) : (
                <div class="item small muted">No commits yet.</div>
              )}
            </div>
          </div>
        </div>
        {mainPane}
      </div>

      {modal?.t === 'new' ? NewModal() : null}
      {modal?.t === 'import' ? <Run key="import" fn={() => ImportModal({ docs: modal.docs })} /> : null}
      {modal?.t === 'map' ? <Run key="map" fn={() => MapModal({ docs: modal.docs })} /> : null}
      {modal?.t === 'diff' ? (
        <div class="scrim" id="scrim" onClick={(e) => e.target === e.currentTarget && setModal(null)}>
          <div class="modal wide" role="dialog" aria-modal="true" tabIndex={-1}>
            <span class="sheet-grip only-phone" aria-hidden="true"></span>
            <div class="modal-head">
              <div>
                <div class="caps">
                  {modal.c.short} · {whoLabel(modal.c)} · {ago(modal.c.date)}
                </div>
                <h2>{humanSubject(modal.c.subject)}</h2>
              </div>
              <button class="icon-btn" aria-label="Close" onClick={() => setModal(null)}>
                <Icon name="x" />
              </button>
            </div>
            <div class="modal-body">
              {modal.c.trailers ? (
                <div class="mono tiny muted" style="margin-bottom:8px">
                  {modal.c.trailers.split('\n').join(' · ')}
                </div>
              ) : null}
              {modal.c.files.map((f) => (
                <DiffView before={f.before} after={f.after} file={f.path} />
              ))}
            </div>
            <div class="modal-foot">
              <button class="btn" onClick={() => setModal(null)}>
                Close
              </button>
            </div>
          </div>
        </div>
      ) : null}
      {modal?.t === 'confirm' ? (
        <ConfirmModal
          title={modal.title}
          body={modal.body}
          ok={modal.ok}
          cancel={modal.danger ? (modal.ok === 'Discard' ? 'Keep editing' : 'Keep it') : 'Cancel'}
          danger={modal.danger}
          onOk={async () => {
            const run = modal.run
            setModal(null)
            await run()
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
      {modal?.t === 'form' ? (
        <FormModal
          caps={modal.caps}
          title={modal.title}
          intro={modal.intro}
          fields={modal.fields}
          submit={modal.submit}
          onSubmit={async (v) => {
            const r = await modal.run(v)
            if (typeof r === 'string' && r) return r
          }}
          onClose={() => setModal((m) => (m && m.t === 'form' ? null : m))}
        />
      ) : null}
      {menu ? <Menu anchor={menu.el} items={menu.items} onClose={() => setMenu(null)} label="Entry actions" /> : null}
      {toast ? (
        <div class="toasts" aria-live="polite">
          <div class="toast" data-s={toast.s} role="status">
            <StatusMark s={toast.s} />
            <div>
              <b>{toast.title}</b>
              <p>{toast.sub}</p>
            </div>
            <button class="icon-btn toast-x" aria-label="Dismiss" onClick={() => setToast(null)}>
              <Icon name="x" />
            </button>
          </div>
        </div>
      ) : null}
    </div>
  )
}

// ---------- import: markdown into a kind ----------

function sections(md: string) {
  let title = ''
  const secs: Record<string, string[]> = { _intro: [] }
  let cur = '_intro'
  for (const l of String(md).split('\n')) {
    const h1 = l.match(/^#\s+(.*)/)
    const h = l.match(/^#{2,3}\s+(.*)/)
    if (h1 && !title) {
      title = h1[1].trim()
      continue
    }
    if (h) {
      cur = h[1].trim().toLowerCase()
      secs[cur] = []
      continue
    }
    secs[cur].push(l)
  }
  return { title, secs }
}
const itemsOf = (ls: string[] | null) =>
  (ls || [])
    .map((l) => l.match(/^\s*(?:\d+[.)]|[-*+])\s+(.*)$/))
    .filter(Boolean)
    .map((m) => m![1].trim())
const secOf = (secs: Record<string, string[]>, re: RegExp) => {
  const k = Object.keys(secs).find((x) => x !== '_intro' && re.test(x))
  return k ? secs[k] : null
}
const txt = (ls: string[] | null) => (ls || []).join('\n').trim()
function detect(md: string): string {
  const { secs } = sections(md)
  const numbered = String(md)
    .split('\n')
    .filter((l) => /^\s*\d+[.)]\s/.test(l)).length
  return numbered >= 2 && (secOf(secs, /step|procedure/) || secOf(secs, /verif|rollback|when|symptom/)) ? 'runbook' : 'knowledge'
}
function convert(kind: KindUI, md: string, name: string): Draft {
  const { title, secs } = sections(md)
  const t =
    title ||
    String(name || 'imported')
      .replace(/\.\w+$/, '')
      .replace(/[-_]/g, ' ')
  if (kind.id === 'runbook') {
    const stepLines =
      secOf(secs, /step|procedure/) ||
      String(md)
        .split('\n')
        .filter((l) => /^\s*\d+[.)]\s/.test(l))
    const steps = itemsOf(stepLines).map((x) => {
      const m = x.match(/`([^`]+)`/)
      return m
        ? `${
            x
              .replace(/`[^`]+`/, '')
              .replace(/[\s:—-]+$/, '')
              .trim() || 'Run it'
          } (run: \`${m[1]}\`)`
        : x
    })
    const pre = itemsOf(secOf(secs, /prereq|before you/))
    const fm: FM = {
      title: t,
      when_to_use: txt(secOf(secs, /when|symptom|trigger/)) || txt(secs._intro),
      steps,
      verification: txt(secOf(secs, /verif|check it worked|done when/)),
      rollback: txt(secOf(secs, /rollback|undo|back out/)),
      owner: 'ops',
    }
    if (pre.length) fm.prerequisites = pre
    return { fm, body: '', free: '' }
  }
  if (kind.id === 'knowledge') {
    const paras = txt(secs._intro).split(/\n\s*\n/)
    return {
      fm: {
        title: t,
        evidence: `imported from ${name || 'a paste'}`,
        confidence: 'medium',
      },
      body:
        paras.join('\n\n') +
        Object.entries(secs)
          .filter(([k]) => k !== '_intro')
          .map(([k, v]) => `\n\n## ${k}\n${txt(v)}`)
          .join(''),
      free: '',
    }
  }
  return {
    fm: { title: t },
    body:
      String(md)
        .replace(/^#\s+.*\n/, '')
        .trim() + '\n',
    free: '',
  }
}
function summaryOf(kind: KindUI, d: Draft): string {
  const n = Array.isArray(d.fm.steps) ? (d.fm.steps as unknown[]).length : 0
  const bits =
    kind.id === 'runbook'
      ? [
          `${n} step${n === 1 ? '' : 's'}`,
          `when to use ${d.fm.when_to_use ? '✓' : '✗'}`,
          `verification ${d.fm.verification ? '✓' : '✗'}`,
          `rollback ${d.fm.rollback ? '✓' : '✗ (you add it)'}`,
        ]
      : kind.id === 'knowledge'
        ? ['evidence ✓', `applies to ${Array.isArray(d.fm.applies_to) && (d.fm.applies_to as unknown[]).length ? '✓' : '– (optional)'}`]
        : ['body ✓']
  return bits.join(' · ')
}
