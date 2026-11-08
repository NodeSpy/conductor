// The Config tab (prototype js/screens/config.js): applied changes with their
// diff and a restore, the files conductor loaded, the sections (each opening
// its own editor, or the YAML), how a change is kept safe — and the change
// pipeline every editor ends in, #/system/config/apply (the prototype's
// #/config/apply): staged node edits → validate on a copy → diff → confirm →
// apply with a backup → reload. The raw editor (#/system/config/raw) turns a
// hand edit into the same node-level staged edits.
import type { JSX } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { parse as parseYAML } from 'yaml'
import {
  config, staged, cfgError, busy, loadConfig, stage, stageDelete, unstage, clearStaged, history, loadHistory,
  restoreToReview, restoreId, cancelRestore, review, applyResult, doReview, apply, runValidate, validateMsg,
} from '../configStore'
import { api } from '../api/client'
import { confirmLabel, stepupMethods, authState } from '../api/auth'
import type { ConfigLeaf, ConfigResponse, HistoryItem, DiffLineDTO, ConfigChange } from '../api/types'
import { Icon } from '../components/Icon'
import { EmptyState } from '../components/EmptyState'
import { Chipped } from '../components/Chips'
import { StatusMark } from '../components/StatusMark'
import { CodeEditor } from '../components/CodeEditor'
import { Menu, type MenuItem } from '../components/Menu'
import { Stepper, BrCrumbs, off } from '../components/BrowserFields'
import { useConfirm, agoText, dateTime } from '../components/SysBits'
import { status } from '../state'
import { seg, query, go } from '../router'

// Sections whose scalars are edited on their own page here (#/system/config/
// section/<name>): the core blocks that have no screen of their own.
const EDITABLE = new Set(['settings', 'dashboard', 'update', 'sandbox', 'store', 'usage', 'callable', 'memory'])

// Where each section is edited (the prototype's SECTIONS table): its own
// screen when the app has one, else its section page here.
const SECTION_HREF: Record<string, string> = {
  vaults: '#/secrets', secrets: '#/secrets', connectors: '#/connections', hosts: '#/connections', runtimes: '#/connections',
  sandbox: '#/system/safety', operators: '#/system/access', bots: '#/bots', teams: '#/teams', browser: '#/browser',
  packs: '#/packs', update: '#/system/updates', triggers: '#/workflows', workflows: '#/workflows',
}
const sectionHref = (s: string) => SECTION_HREF[s] ?? `#/system/config/section/${encodeURIComponent(s)}`

const sizeOf = (v: unknown) => (Array.isArray(v) ? v.length : v && typeof v === 'object' ? Object.keys(v).length : 1)

export function Config(): JSX.Element {
  useEffect(() => {
    void loadConfig()
    void loadHistory()
  }, [])
  const sub = seg(2)
  if (sub === 'raw') return <RawConfig c={config.value} />
  if (sub === 'apply') return <ConfigApply />
  if (sub === 'section') return <SectionPage name={seg(3)} />
  return <ConfigHome />
}

function ConfigHome(): JSX.Element {
  const c = config.value
  const n = staged.value.length
  const tree = (c?.tree ?? {}) as Record<string, unknown>
  return (
    <>
      <div class="dl-phead">
        <div>
          <h1>Config</h1>
          <p class="dl-lead">Edits to a section, a bot or a workflow wait here; nothing applies until you review it.</p>
        </div>
        <div class="dl-actions tight">
          <a class="btn secondary" href="#/system/config/raw"><Icon name="code" />Edit the file</a>
          {n ? <a class="btn primary" href="#/system/config/apply">Review {n} change{n > 1 ? 's' : ''}</a> : null}
        </div>
      </div>
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <div class="cfg-stack">
        <HistoryCard />
        <FilesCard c={c} />
        <div class="dl-cols">
          <div class="dl-col-main">
            <details class="panel dl-files-d cfg-sections" data-testid="cfg-edit-sections">
              <summary class="panel-head"><h3>Edit the config by section</h3><span class="muted small">each section in its editor, or in the YAML</span></summary>
              <div class="panel-body">
                <div class="sec-grid">
                  {Object.keys(tree).map((s) => (
                    <div class="sec-chip" key={s}>
                      <a href={sectionHref(s)} class="grow"><b>{s}</b> <span class="mono tiny muted">{sizeOf(tree[s])}</span></a>
                      <a class="icon-btn" href={`#/system/config/raw?at=${encodeURIComponent(s)}`} data-tip={`${s}: in the YAML`} aria-label={`${s} in the YAML`}><Icon name="code" /></a>
                    </div>
                  ))}
                </div>
              </div>
            </details>
          </div>
          <div class="dl-col-side">
            <SafeCard />
          </div>
        </div>
      </div>
    </>
  )
}

// ---- applied changes ----

const NOUN: Record<string, [string, string]> = {
  bots: ['bot', 'bots'], connectors: ['connection', 'connections'], triggers: ['workflow', 'workflows'], workflows: ['workflow', 'workflows'],
  packs: ['pack', 'packs'], runtimes: ['runtime', 'runtimes'], hosts: ['host', 'hosts'], vaults: ['vault', 'vaults'], teams: ['team', 'teams'],
  operators: ['operator', 'operators'], secrets: ['secret', 'secrets'],
}
const listWords = (xs: string[]) => (xs.length <= 1 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`)
const idTime = (id?: string) => {
  const m = /^(\d{4})(\d\d)(\d\d)T(\d\d)(\d\d)(\d\d)Z/.exec(id ?? '')
  return m ? `${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}Z` : ''
}

// plainChange: what an applied change did, in words (the prototype's
// plainChange), from the leaf paths the history read model lists.
function plainChange(h: HistoryItem): string {
  if (h.event === 'restore') {
    const t = idTime(h.restored_from)
    return t ? `Went back to the config from before ${dateTime(t)}` : 'Went back to an earlier config'
  }
  const paths = h.changed ?? []
  if (!paths.length) return h.added || h.removed ? `Edited the file: ${h.added} line${h.added === 1 ? '' : 's'} added, ${h.removed} removed` : 'Saved the file with no change'
  const bySec = new Map<string, Set<string>>()
  for (const p of paths) {
    const [sec, a, b] = p.split('.')
    const k = NOUN[sec] ? a : b ?? a ?? ''
    if (!bySec.has(sec)) bySec.set(sec, new Set())
    if (k) bySec.get(sec)!.add(k)
  }
  const parts = [...bySec].map(([sec, names]) => {
    const xs = [...names]
    if (NOUN[sec]) return `the ${NOUN[sec][xs.length === 1 ? 0 : 1]} ${listWords(xs)}`
    return xs.length ? `${sectionTitle(sec).toLowerCase()} (${listWords(xs.map((x) => humanKey(x).toLowerCase()))})` : sectionTitle(sec).toLowerCase()
  })
  return 'Changed ' + listWords(parts)
}

// whoWords: who made it and how (the prototype's whoWords). An apply or
// restore the dashboard recorded names its operator; a backup with no audit
// record behind it was an edit of the file itself.
function whoWords(h: HistoryItem): string {
  if (!h.event) return 'Someone edited the file'
  return `${h.operator || 'Someone'}, in the dashboard`
}

function HistoryCard(): JSX.Element {
  const items = history.value
  const [ask, askNode] = useConfirmPair()
  return (
    <section class="dl-card cfg-hist" aria-label="Applied changes">
      <header class="dl-ch">
        <h2>Applied changes</h2>
        {items.length ? <span class="dl-sub">newest first · each keeps a backup you can go back to</span> : null}
      </header>
      {items.length ? (
        <>
          <ul class="dl-list">
            {items.map((h, i) => <HistoryRow key={h.id} h={h} i={i} all={items} ask={ask} />)}
          </ul>
          <p class="dl-sub cfg-note">Revert undoes the newest change. Restore goes back to before a change, undoing everything since. Both are checked on a copy and backed up before they apply.</p>
        </>
      ) : (
        <ul class="dl-list"><li class="dl-empty-row" data-testid="history-empty">No changes applied yet: each one you apply shows here.</li></ul>
      )}
      {askNode}
    </section>
  )
}

function useConfirmPair(): [ReturnType<typeof useConfirm>[1], JSX.Element | null] {
  const [node, ask] = useConfirm()
  return [ask, node]
}

function HistoryRow({ h, i, all, ask }: { h: HistoryItem; i: number; all: HistoryItem[]; ask: ReturnType<typeof useConfirm>[1] }): JSX.Element {
  const [open, setOpen] = useState(false)
  const [menu, setMenu] = useState<HTMLElement | null>(null)
  const live = i === 0
  const goReview = async () => {
    await restoreToReview(h.id)
    if (restoreId.value) go('system/config/apply')
  }
  const restore = async () => {
    const later = all.slice(0, i)
    if (later.length) {
      const ok = await ask({
        title: `Restore to before ${dateTime(h.ts)}?`,
        body: (
          <>
            This also undoes <b>{later.length} later change{later.length > 1 ? 's' : ''}</b>:
            {later.map((x) => <div class="mono tiny" style="margin-top:4px">{dateTime(x.ts)} · {plainChange(x)}</div>)}
          </>
        ),
        ok: `Undo all ${later.length + 1}`,
        cancel: 'Keep them',
        danger: true,
      })
      if (!ok) return
    }
    await goReview()
  }
  const items: MenuItem[] = [
    { label: 'Revert this change…', icon: 'undo', disabled: live ? false : 'A later change came after it: restore to before it instead', onClick: () => void goReview() },
    { label: 'Restore to before this…', icon: 'clock', onClick: () => void restore() },
  ]
  return (
    <li class="cfg-h">
      <div class="cfg-h-row">
        <span class="dl-ng"></span>
        <span class="dl-nt">
          <b>{plainChange(h)}</b>
          {open && h.changed?.length ? <span class="dl-tech mono">{h.changed.join(', ')}</span> : null}
        </span>
        <span class="cfg-who">
          <span>{whoWords(h)}</span>
          <span class="dl-sub">{agoText(h.ts)} · {dateTime(h.ts)}</span>
        </span>
        <span class="dl-na">
          {live ? <span class="dl-sub">in use now</span> : null}
          <button class="btn sm ghost" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? 'Hide the diff' : 'Diff'}
          </button>
          <button class="icon-btn" type="button" aria-label="More for this change" disabled={!!restoreId.value || busy.value} onClick={(e) => setMenu(e.currentTarget as HTMLElement)}>
            <Icon name="more" />
          </button>
        </span>
      </div>
      {open ? <HistoryDiff id={h.id} /> : null}
      {menu ? <Menu anchor={menu} items={items} onClose={() => setMenu(null)} label="More for this change" /> : null}
    </li>
  )
}

function HistoryDiff({ id }: { id: string }): JSX.Element {
  const [lines, setLines] = useState<DiffLineDTO[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let live = true
    api.historyDiff(id).then((r) => { if (live) setLines(r.lines ?? []) }).catch((e) => { if (live) setErr(String(e)) })
    return () => { live = false }
  }, [id])
  if (err) return <div class="hist-diff"><p class="dl-sub">Couldn't load the diff: {err}</p></div>
  if (!lines) return <div class="hist-diff"><p class="dl-sub">Loading…</p></div>
  if (!lines.length) return <div class="hist-diff"><p class="dl-sub">This change was made outside the dashboard; its diff is in the backup <span class="mono">{id}</span>.</p></div>
  return <div class="hist-diff"><DiffLines lines={lines} mode="unified" /></div>
}

// DiffLines is the prototype's UI.diffView: unified rows (old line, new
// line, sign, text) or a split view (old left, new right), with the changed
// part of a paired line marked.
function DiffLines({ lines, mode, src, file = 'config.yaml' }: { lines: DiffLineDTO[]; mode: 'unified' | 'split'; src?: string; file?: string }): JSX.Element {
  const marks = new Map<DiffLineDTO, [string, string, string]>()
  for (let i = 0; i < lines.length; ) {
    if (lines[i].op !== 'del') { i++; continue }
    let j = i
    while (j < lines.length && lines[j].op === 'del') j++
    let k = j
    while (k < lines.length && lines[k].op === 'add') k++
    for (let n = 0; n < Math.min(j - i, k - j); n++) {
      const x = lines[i + n].text, y = lines[j + n].text
      let p = 0
      while (p < x.length && p < y.length && x[p] === y[p]) p++
      let q = 0
      while (q < x.length - p && q < y.length - p && x[x.length - 1 - q] === y[y.length - 1 - q]) q++
      if ((p + q) / Math.max(x.length, y.length, 1) >= 0.4) {
        marks.set(lines[i + n], [x.slice(0, p), x.slice(p, x.length - q), x.slice(x.length - q)])
        marks.set(lines[j + n], [y.slice(0, p), y.slice(p, y.length - q), y.slice(y.length - q)])
      }
    }
    i = k
  }
  const text = (l: DiffLineDTO) => {
    const w = marks.get(l)
    return w ? <>{w[0]}{w[1] ? <mark class="wd">{w[1]}</mark> : null}{w[2]}</> : l.text || ' '
  }
  if (mode === 'split') {
    const L: JSX.Element[] = [], R: JSX.Element[] = []
    let dl: DiffLineDTO[] = [], al: DiffLineDTO[] = []
    const gap = <div class="sl gap"><span></span><span> </span></div>
    const flush = () => {
      for (let k = 0; k < Math.max(dl.length, al.length); k++) {
        L.push(dl[k] ? <div class="sl del"><span>{dl[k].old_line ?? ''}</span><span>{text(dl[k])}</span></div> : gap)
        R.push(al[k] ? <div class="sl add"><span>{al[k].new_line ?? ''}</span><span>{text(al[k])}</span></div> : gap)
      }
      dl = []
      al = []
    }
    for (const l of lines) {
      if (l.op === 'del') dl.push(l)
      else if (l.op === 'add') al.push(l)
      else {
        flush()
        L.push(<div class="sl"><span>{l.old_line ?? ''}</span><span>{l.text || ' '}</span></div>)
        R.push(<div class="sl"><span>{l.new_line ?? ''}</span><span>{l.text || ' '}</span></div>)
      }
    }
    flush()
    return <div class="diff split" aria-label="Side-by-side diff"><div class="split"><div>{L}</div><div>{R}</div></div></div>
  }
  // a hunk starts where the line numbers jump; its head names the file, the
  // key path of its first changed line and that line's number (UI.diffView)
  const heads = new Map<DiffLineDTO, string>()
  if (src != null) {
    const L = src.split('\n')
    let prev: DiffLineDTO | null = null
    let start = 0
    const flushHead = (from: number, to: number) => {
      const hunk = lines.slice(from, to)
      const r = hunk.find((x) => x.op !== 'context') ?? hunk[0]
      if (!r) return
      const n = r.op === 'add' ? (r.new_line ?? 0) : (r.old_line ?? 0)
      const path = r.op === 'add' ? '' : keyAt(L, n - 1)
      heads.set(hunk[0], `${file} ${path ? '· ' + path + ' ' : ''}· line ${n}`)
    }
    lines.forEach((l, i) => {
      if (prev && l.old_line != null && prev.old_line != null && l.old_line > prev.old_line + 1) { flushHead(start, i); start = i }
      if (l.old_line != null) prev = l
    })
    flushHead(start, lines.length)
  }
  return (
    <div class="diff unified" aria-label="Unified diff">
      {lines.map((l) => (<>
        {heads.has(l) ? <div class="h">{heads.get(l)}</div> : null}
        <div class={'dl ' + (l.op === 'add' ? 'add' : l.op === 'del' ? 'del' : '')}>
          <span>{l.op === 'add' ? '' : l.old_line ?? ''}</span>
          <span>{l.op === 'del' ? '' : l.new_line ?? ''}</span>
          <span>{l.op === 'add' ? '+' : l.op === 'del' ? '-' : ' '}</span>
          <span>{text(l)}</span>
        </div>
      </>))}
    </div>
  )
}

// keyAt is the dotted key path of line i of a YAML text (UI.diffView's keyAt).
function keyAt(lines: string[], i: number): string {
  const path: string[] = []
  let ind = Infinity
  for (let k = i; k >= 0; k--) {
    const m = (lines[k] || '').match(/^(\s*)(- )?("[^"]+"|[\w.@/${}-]+):(\s|$)/)
    if (!m) continue
    const n = m[1].length + (m[2] ? 2 : 0)
    if (n < ind) {
      path.unshift(m[3].replace(/^"|"$/g, ''))
      ind = m[2] ? m[1].length : n
      if (!ind) break
    }
  }
  return path.join('.')
}

// FilesCard: what conductor loaded at boot, with a real check that the file
// on disk is still what it loaded (ConfigResponse.disk_sha vs sha).
function FilesCard({ c }: { c: ConfigResponse | null }): JSX.Element {
  const disk = (c as ConfigResponse | null)?.disk_sha
  const same = !c || !disk || disk === c.sha
  const started = status.value?.started_at
  return (
    <details class="panel cfg-files dl-files-d">
      <summary class="panel-head">
        <h3>Files</h3>
        <span class="muted small">what conductor loaded at boot · {same ? "on disk matches what's loaded" : 'the file on disk has changed since it was loaded'}</span>
      </summary>
      <table class="tbl compact responsive">
        <thead><tr><th>file</th><th>owner</th><th>sha</th><th>loaded</th></tr></thead>
        <tbody>
          {c ? (
            <tr>
              <td data-l="file" class="mono"><a class="link" href="#/system/config/raw">{c.path}</a></td>
              <td data-l="owner"><span class="tag owner op">you</span></td>
              <td data-l="sha" class="mono">{c.sha}{!same ? <span class="muted"> (on disk {disk})</span> : null}</td>
              <td data-l="loaded" class="muted">{started ? agoText(started) : ''}</td>
            </tr>
          ) : (
            <tr><td colSpan={4} class="muted">Loading…</td></tr>
          )}
        </tbody>
      </table>
    </details>
  )
}

function SafeCard(): JSX.Element {
  return (
    <details class="panel dl-files-d">
      <summary class="panel-head"><h3>How a change is kept safe</h3></summary>
      <div class="panel-body small stack tight">
        <div class="row guard"><span class="grow">Edits are node-level: comments, anchors and unknown keys are kept.</span></div>
        <div class="row guard"><span class="grow">Validated on a copy, with copied plugin state; <code>conductor validate</code> is not read-only.</span></div>
        <div class="row guard"><span class="grow">A config this binary can't load is never written.</span></div>
        <div class="row guard"><span class="grow">Boot guard: a config that fails at boot is restored automatically.</span></div>
      </div>
    </details>
  )
}

// ---- review and apply (#/system/config/apply) ----

const STEPS = ['Edit', 'Validate on a copy', 'Review diff', 'Apply', 'Reload & verify']

function ConfigApply(): JSX.Element {
  const P = staged.value
  const rev = review.value
  const applied = applyResult.value
  const restoring = !!restoreId.value
  const [mode, setMode] = useState<'unified' | 'split'>('unified')
  const [ack, setAck] = useState(false)
  const [confirmNode, ask] = useConfirm()
  const key = P.map((c) => c.id).join(',')

  // Review (and validate on a copy) whenever the staged set changes.
  // vKey: which staged set (or restore) the validate result in validateMsg is
  // for, so a result left over from an earlier review never counts here.
  const runKey = restoring ? 'restore:' + restoreId.value : key
  const [vKey, setVKey] = useState('')
  useEffect(() => {
    if (!restoring && !P.length) return
    if (!restoring) void doReview()
    const stop = runValidate()
    setVKey(runKey)
    setAck(false)
    return stop
  }, [runKey])

  if (applied && !P.length && !restoring) return <AppliedView />

  if (!P.length && !restoring) {
    return (
      <div class="cfg-apply">
        <div class="page-head"><div><BrCrumbs trail={[['Config', '#/system/config'], ['Review and apply']]} /><h1>Review and apply</h1></div></div>
        <div class="panel">
          <EmptyState hero icon="edit" title="Nothing staged" testid="apply-empty" action={{ label: 'Config', href: '#/system/config' }}>
            <p>Edit a connection, a bot, a secret reference or a workflow and its change waits here: validated on a copy, shown as a diff, applied when you confirm.</p>
          </EmptyState>
        </div>
      </div>
    )
  }

  const lines = rev?.lines ?? []
  const adds = lines.filter((l) => l.op === 'add').length
  const dels = lines.filter((l) => l.op === 'del').length
  const v = validateMsg.value
  const checking = !rev || !v || v.type === 'start' || vKey !== runKey
  const vBad = v?.type === 'error'
  const wid = rev?.widening ?? []
  const disk = (config.value as ConfigResponse | null)?.disk_sha
  const baseOk = !disk || !config.value || disk === config.value.sha
  const blocked = vBad ? 'The check on a copy failed: fix it or discard the change' : checking ? 'Checks are still running on the copy' : wid.length && !ack ? 'This change widens what agents can do: tick the acknowledgement first' : !rev?.changed ? 'Nothing would change' : ''
  const cur = checking || vBad ? 1 : 2
  const states = STEPS.map((_, i) => (i < cur ? 'done' : i === cur ? (vBad && i === 1 ? 'bad' : 'cur') : ''))
  const touched = (rev?.effects ?? []).map((e) => e.path).filter((p) => p && p !== '(restore)')
  const sha = (x?: string) => <code>{x ?? ''}</code>
  const CHECKS: [string, JSX.Element | string, string][] = [
    ['Node-level edit', restoring ? 'the whole file is replaced by the backup' : <>{touched.length} node{touched.length === 1 ? '' : 's'} changed{touched.length ? <> ({touched.slice(0, 4).map((t, k) => <>{k ? ', ' : ''}<code>{t}</code></>)}{touched.length > 4 ? ` and ${touched.length - 4} more` : ''})</> : null}</>, 'ok'],
    ['Base unchanged', baseOk ? <>config.yaml is still {sha(config.value?.sha)}: no one edited it since conductor loaded it</> : <>config.yaml changed on disk ({sha(disk)}) since conductor loaded {sha(config.value?.sha)}</>, baseOk ? 'ok' : 'wait'],
    ['conductor validate on a copy', !v || v.type === 'start' ? 'running on a scratch copy; the live config is untouched' : v.message || (vBad ? 'failed' : 'no errors'), !v || v.type === 'start' ? 'run' : vBad ? 'fail' : 'ok'],
    ['Widening', wid.length ? `${wid.length} change${wid.length > 1 ? 's' : ''} widen what agents can do: acknowledge them below` : 'your change widens nothing', wid.length ? 'wait' : 'ok'],
  ]
  const failed = CHECKS.filter((c) => c[2] === 'fail').length
  const needs = !!rev?.stepup
  // the fingerprint only when a passkey is what confirms (as confirmLabel words it)
  const byPasskey = stepupMethods(authState.value)[0] === 'passkey'
  const okLabel = restoring ? 'Restore' : 'Apply'
  const discardAll = async () => {
    if (restoring) { cancelRestore(); go('system/config'); return }
    const ok = await ask({ title: 'Discard all staged changes?', body: `${P.length} change${P.length > 1 ? 's' : ''} will be thrown away. The live config is untouched.`, ok: 'Discard', danger: true })
    if (ok) await clearStaged()
  }
  return (
    <div class="cfg-apply">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Config', '#/system/config'], [restoring ? 'Restore a backup' : 'Review and apply']]} />
          <h1>{restoring ? 'Restore a backup' : 'Review and apply'}</h1>
          <p class="sub">{restoring ? 'The whole config goes back to the backup' : `${P.length} staged change${P.length > 1 ? 's' : ''}`} · +{adds} −{dels}</p>
        </div>
        <div class="actions">
          <button class="btn ghost" type="button" onClick={() => void discardAll()}><Icon name="trash" />{restoring ? 'Cancel the restore' : 'Discard all'}</button>
        </div>
      </div>
      <Stepper steps={STEPS} at={cur} states={states} />
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <div class="grid g-side">
        <div class="stack">
          <div class="panel">
            <div class="panel-head"><h3>Staged</h3><span class="muted small">{restoring ? 'a restore rewrites the whole file' : 'each one is a node edit in config.yaml'}</span></div>
            <div class="list staged-list">
              {restoring ? (
                <div class="item" style="align-items:flex-start">
                  <StatusMark s="run" mkOnly />
                  <div class="grow"><div class="small"><b>Go back to the config from before {dateTime(history.value.find((h) => h.id === restoreId.value)?.ts)}</b></div><div class="mono tiny muted">backup {restoreId.value}</div></div>
                </div>
              ) : (
                P.map((c) => <StagedItem key={c.id} c={c} widens={wid.filter((w) => w.path.startsWith(c.path.join('.'))).map((w) => w.text)} />)
              )}
            </div>
          </div>
          <div class="panel">
            <div class="panel-head">
              <h3>Diff</h3><span class="muted small">what will be written · +{adds} −{dels}</span>
              <div class="tools">
                <div class="seg" role="group">
                  {(['unified', 'split'] as const).map((m) => (
                    <button type="button" aria-pressed={mode === m} class={mode === m ? 'on' : ''} onClick={() => setMode(m)}>{m}</button>
                  ))}
                </div>
              </div>
            </div>
            <div class="panel-body tight">{rev ? (lines.length ? <DiffLines lines={lines} mode={mode} src={config.value?.text} /> : <div class="empty small"><span class="serif">No difference</span>The candidate is byte-identical to the live file.</div>) : <p class="dl-sub">Loading…</p>}</div>
          </div>
        </div>
        <div class="stack">
          <div class="panel">
            <div class="panel-head">
              <h3>Checks</h3>
              <div class="tools" id="chk-state">
                {checking ? <StatusMark s="run" word="running" /> : failed ? <StatusMark s="fail" word={`${failed} failed`} /> : <StatusMark s="ok" word={`${CHECKS.length} passed`} />}
              </div>
            </div>
            <div class="panel-body" id="checks">
              {CHECKS.map(([k, d, s]) => (
                <div class="check-row"><span class="ci"><StatusMark s={s} mkOnly /></span><div><b>{k}</b><div class="muted tiny">{d}</div></div></div>
              ))}
            </div>
            {v && v.type !== 'start' ? (
              <details class="validate-out" open={vBad}>
                <summary class="small">conductor validate output <span class="muted">(on the copy)</span></summary>
                <pre class="code-out mono" id="validate-out">{v.message || (vBad ? 'failed' : 'ok')}</pre>
              </details>
            ) : null}
          </div>
          <div class="panel" id="apply-box">
            <div class="panel-body stack">
              {wid.length ? (
                <>
                  <label class="row small ack"><input type="checkbox" class="cbx" checked={ack} onChange={(e) => setAck((e.target as HTMLInputElement).checked)} /> <span>I understand this <b>widens</b> what agents can do:</span></label>
                  <ul class="widen-list">{wid.map((x) => <li><span class="mono tiny">{x.path}</span> {x.text}</li>)}</ul>
                </>
              ) : null}
              <div class={'pk-why ' + (needs ? 'needs' : 'none')} id="pk-why">
                {needs ? (
                  <><Icon name={byPasskey ? 'passkey' : 'shield'} /><span><b>{byPasskey ? 'Needs your passkey' : "Needs you to confirm it's you"}:</b> <Chipped text={(wid[0] ?? rev?.guarded?.[0])?.text ?? (restoring ? 'a restore rewrites the whole file' : 'this config asks for it on every change')} keys={P.map((x) => x.path.join('.'))} />.</span></>
                ) : (
                  <><Icon name="check" /><span><b>No confirmation needed:</b> nothing here lets bots or agents do more. It's still checked on a copy, backed up first, and can be rolled back.</span></>
                )}
              </div>
              <div class="small muted"><Icon name="download" /> A backup is written before anything changes. Applying reloads conductor with the new config.</div>
              <button class="btn lg primary" type="button" data-testid="apply-go" {...off(blocked || (busy.value ? 'Working…' : ''))} onClick={() => { if (!blocked && !busy.value) void apply() }}>
                {needs ? <>{byPasskey ? <Icon name="passkey" /> : null}{confirmLabel(okLabel)}</> : <><Icon name="check" />{okLabel}</>}
              </button>
            </div>
          </div>
        </div>
      </div>
      {confirmNode}
    </div>
  )
}

function StagedItem({ c, widens }: { c: ConfigChange; widens: string[] }): JSX.Element {
  const path = c.path.join('.')
  const structured = typeof c.value === 'object' && c.value !== null
  const said = c.summary || (c.delete ? `Remove ${path}` : structured ? `Set ${path}` : `Set ${path} to ${String(c.value)}`)
  return (
    <div class="item staged-item" style="align-items:flex-start">
      <StatusMark s={c.delete ? 'fail' : 'run'} mkOnly />
      <div class="grow">
        <div class="small"><b><Chipped text={said} keys={[path]} /></b></div>
        {/* the key once: only when the summary above doesn't already name it */}
        {said.includes(path) ? null : <div class="tiny muted"><code>{path}</code></div>}
        {structured && !c.delete ? <pre class="staged-val staged-tree mono tiny" data-testid="staged-tree">{JSON.stringify(c.value, null, 2)}</pre> : null}
        {widens.map((w) => <span class="tag" data-s="wait"><Icon name="warn" /> widens: {w}</span>)}
      </div>
      <div class="staged-acts">
        <a class="btn sm ghost" href={sectionHref(c.path[0])} aria-label={`Edit ${path}`}><Icon name="edit" /><span class="staged-lbl">Edit</span></a>
        <button class="btn sm ghost" type="button" aria-label={`Discard ${c.summary || path}`} onClick={() => void unstage(c.id)}><Icon name="x" /><span class="staged-lbl">Discard</span></button>
      </div>
    </div>
  )
}

function AppliedView(): JSX.Element {
  const a = applyResult.value!
  return (
    <div class="cfg-apply">
      <div class="page-head"><div><BrCrumbs trail={[['Config', '#/system/config'], ['Applied']]} /><h1>Applied</h1></div></div>
      <Stepper steps={STEPS} at={5} states={STEPS.map(() => 'done')} />
      <div class="grid g-side">
        <div class="panel">
          <div class="panel-head"><h3>Reload &amp; verify</h3>{a.reload_error ? <StatusMark s="wait" word="reload pending" /> : <StatusMark s="ok" word="done" />}</div>
          <div class="panel-body" id="stages">
            {a.backup_id ? <div class="check-row"><StatusMark s="ok" mkOnly /><div><b>A backup of the old config</b><div class="muted tiny mono">{a.backup_id}</div></div></div> : null}
            <div class="check-row"><StatusMark s="ok" mkOnly /><div><b>The new config is in place</b><div class="muted tiny mono">config.yaml swapped in one step · sha {a.sha}</div></div></div>
            <div class="check-row"><StatusMark s={a.reload_error ? 'wait' : 'ok'} mkOnly /><div><b>{a.reload_error ? 'conductor picks it up when it next restarts' : 'conductor was told to reload with it'}</b><div class="muted tiny mono">{a.reload_error || 'SIGHUP: it re-reads the config; the dashboard reconnects by itself'}</div></div></div>
          </div>
        </div>
        <div class="stack">
          <div class="banner ok applied-note" role="status">
            <StatusMark s="ok" word="Applied" /> Live config is <span class="mono">{a.sha}</span>.{a.backup_id ? <> Backup <span class="mono">{a.backup_id}</span> holds what it replaced.</> : null}
          </div>
          <div class="row">
            {a.backup_id ? (
              <button class="btn" type="button" onClick={() => { applyResult.value = null; void restoreToReview(a.backup_id!) }}><Icon name="undo" />Roll back</button>
            ) : null}
            <a class="btn ghost" href="#/system/config" onClick={() => { applyResult.value = null; void loadHistory() }}>Done</a>
          </div>
        </div>
      </div>
    </div>
  )
}

// ---- the raw editor (#/system/config/raw) ----

type RawChange = { path: string[]; value?: unknown; del?: boolean }

// rawChanges compares the edited YAML with the live config, node by node, and
// returns the smallest set of path edits that turns one into the other (the
// prototype's rawToChanges): a changed scalar is one edit, so its comment and
// layout are kept; an added or reshaped map or list is staged whole.
function rawChanges(before: unknown, after: unknown, path: string[] = []): RawChange[] {
  const isMap = (x: unknown) => !!x && typeof x === 'object' && !Array.isArray(x)
  if (isMap(before) && isMap(after)) {
    const a = before as Record<string, unknown>, b = after as Record<string, unknown>
    const out: RawChange[] = []
    for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) {
      if (!(k in b)) out.push({ path: [...path, k], del: true })
      else if (!(k in a)) out.push({ path: [...path, k], value: b[k] })
      else out.push(...rawChanges(a[k], b[k], [...path, k]))
    }
    return out
  }
  if (canon(before) === canon(after)) return []
  return [{ path, value: after }]
}

// canon is a value's JSON with map keys sorted, so key order never counts as a change.
function canon(v: unknown): string {
  return JSON.stringify(v, (_k, x) => (x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, (x as Record<string, unknown>)[k]])) : x))
}

function RawConfig({ c }: { c: ConfigResponse | null }): JSX.Element {
  const [text, setText] = useState<string | null>(null)
  const [err, setErr] = useState<{ message: string; line?: number } | null>(null)
  const [stagingBusy, setStagingBusy] = useState(false)
  const base = c?.text ?? ''
  useEffect(() => { if (c && text === null) setText(c.text) }, [c])
  const cur = text ?? base
  const at = query('at')
  const changed = useMemo(() => {
    const a = base.split('\n'), b = cur.split('\n')
    let n = 0
    for (let i = 0; i < Math.max(a.length, b.length); i++) if (a[i] !== b[i]) n++
    return n
  }, [base, cur])
  const outline = cur.split('\n').map((l, i) => [l, i + 1] as [string, number]).filter(([l]) => /^[a-z][\w-]*:/.test(l) || /^ {2}[\w"@:./-]+:/.test(l) || /^ {2}- name: /.test(l))
  const [hl, setHl] = useState<number | undefined>(undefined)
  useEffect(() => {
    if (!at) return
    const n = cur.split('\n').findIndex((l) => l.startsWith(at + ':'))
    if (n >= 0) setHl(n + 1)
  }, [at, c])
  const onInput = (t: string) => {
    setText(t)
    try { parseYAML(t); setErr(null) } catch (e) { const ee = e as { message: string; linePos?: { line: number }[] }; setErr({ message: ee.message, line: ee.linePos?.[0]?.line }) }
  }
  const reviewIt = async () => {
    if (!c) return
    let after: unknown
    try { after = parseYAML(cur) ?? {} } catch (e) { setErr({ message: (e as Error).message }); return }
    const changes = rawChanges(c.tree ?? {}, after)
    if (!changes.length) { setErr({ message: 'Nothing changed: only spacing or comments differ, and those are kept as they are.' }); return }
    if (changes.some((x) => !x.path.length)) { setErr({ message: 'The file has to stay a map of sections. Nothing was staged.' }); return }
    setStagingBusy(true)
    for (const ch of changes) {
      if (ch.del) await stageDelete(ch.path, `Remove ${ch.path.join('.')} (from the file)`)
      else await stage(ch.path, ch.value, `${ch.path.join('.')} (from the file)`)
      if (cfgError.value) { setErr({ message: cfgError.value }); setStagingBusy(false); return }
    }
    setStagingBusy(false)
    setText(null)
    go('system/config/apply')
  }
  return (
    <div class="cfg-raw">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Config', '#/system/config'], ['The config file']]} />
          <h1>config.yaml</h1>
          <p class="sub">{c ? (
            // each part stays whole, so a narrow screen breaks between them, never inside one
            <>
              <span class="nowrap">{c.path.replace(/^\/home\/[^/]+|^\/Users\/[^/]+/, '~')}</span>{' '}
              <span class="nowrap">· sha {String(c.sha).slice(0, 12)}</span>
              {staged.value.length ? <>{' '}<span class="nowrap">· {staged.value.length} staged change{staged.value.length > 1 ? 's wait' : ' waits'} in the review</span></> : null}
            </>
          ) : 'Loading…'}</p>
        </div>
        <div class="actions">
          <span class="mono tiny muted" id="raw-state">{changed ? `${changed} lines edited` : 'no edits'}</span>
          <button class="btn" type="button" {...off(changed ? '' : 'No edits to throw away')} onClick={() => { if (changed) { setText(base); setErr(null) } }}><Icon name="undo" />Revert edits</button>
          <button class="btn primary" type="button" data-testid="raw-review" {...off(changed ? (stagingBusy ? 'Staging…' : '') : 'Edit the YAML first; the review shows the node-level diff')} onClick={() => { if (changed && !stagingBusy) void reviewIt() }}><Icon name="check" />Validate &amp; review</button>
        </div>
      </div>
      <div class="grid raw-grid">
        <div class="panel">
          <div class="panel-head"><span class="mono small">editing</span></div>
          {err ? <div id="raw-err" class="callout danger small" style="margin:10px" role="alert">{err.message}</div> : null}
          <div class="ced-scroll">
            {c ? <CodeEditor id="raw-ed" text={cur} onChange={onInput} minRows={30} label="config.yaml" errLines={err?.line ? [err.line] : []} hlLine={hl} /> : <p class="dl-sub" style="padding:14px">Loading…</p>}
          </div>
        </div>
        <div class="stack raw-side">
          <div class="panel">
            <div class="panel-head"><h3>Outline</h3></div>
            <div class="tree" style="padding:6px;max-height:60vh;overflow:auto">
              {outline.map(([l, n]) => {
                const d = l.search(/\S/)
                const name = l.trim().replace(/^- name: /, '').replace(/:.*$/, '')
                return (
                  <a href={`#/system/config/raw?at=${encodeURIComponent(name)}`} class={hl === n ? 'on' : ''} style={`padding-left:${10 + d * 6}px`} onClick={(e) => { e.preventDefault(); setHl(n); document.querySelector(`#raw-ed .ln:nth-child(${n})`)?.scrollIntoView({ block: 'center' }) }}>
                    <Icon name={d ? 'chev' : 'file'} />{name}
                  </a>
                )
              })}
            </div>
          </div>
          <div class="callout info small">Raw edits are compared with the live config node by node, so a change here is still a node-level change and the same pipeline applies.</div>
        </div>
      </div>
    </div>
  )
}

// ---- one section's values (#/system/config/section/<name>) ----

function SectionPage({ name }: { name: string }): JSX.Element {
  const c = config.value
  const stagedPaths = new Set(staged.value.map((s) => s.path.join('.')))
  const leaves = (c?.leaves ?? []).filter((l) => l.section === name)
  const editable = EDITABLE.has(name)
  const n = staged.value.length
  return (
    <>
      <div class="dl-phead">
        <div>
          <BrCrumbs trail={[['Config', '#/system/config'], [sectionTitle(name)]]} />
          <h1>{sectionTitle(name)}</h1>
          <p class="dl-lead">{editable ? 'Each value stages as its own edit; nothing applies until you review it.' : 'Shown as it is in the file; change it in the YAML.'}</p>
        </div>
        <div class="dl-actions tight">
          <a class="btn secondary" href={`#/system/config/raw?at=${encodeURIComponent(name)}`}><Icon name="code" />In the YAML</a>
          {n ? <a class="btn primary" href="#/system/config/apply">Review {n} change{n > 1 ? 's' : ''}</a> : null}
        </div>
      </div>
      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <section class="dl-card" aria-label={name} style="margin-top:20px">
        {!c ? (
          <p class="dl-sub">Loading…</p>
        ) : leaves.length ? (
          <div class="fields">
            {leaves.map((l) => <Field key={l.path.join('.')} leaf={l} editable={editable} staged={stagedPaths.has(l.path.join('.'))} />)}
          </div>
        ) : (
          <p class="dl-sub">This section has no values of its own in the file.</p>
        )}
      </section>
    </>
  )
}

function Field({ leaf, editable, staged: isStaged }: { leaf: ConfigLeaf; editable: boolean; staged: boolean }): JSX.Element {
  const path = leaf.path.join('.')
  const sub = leaf.path.slice(1).join('.') || path
  const value = fmt(leaf.value)
  const long = leaf.kind === 'string' && value.length > 28
  const commit = (raw: string | boolean) => {
    let v: unknown = raw
    if (leaf.kind === 'int' || leaf.kind === 'float') v = Number(raw)
    else if (leaf.kind === 'bool') v = raw === true || raw === 'true'
    void stage(leaf.path, v)
  }
  return (
    <label class={'field' + (isStaged ? ' is-staged' : '') + (long ? ' wide' : '')} data-path={path}>
      <span class="cfg-label">
        <span class="cfg-name">{leafLabel(leaf.path)}</span>
        {sub !== leaf.path[leaf.path.length - 1] ? <span class="field-path mono">{sub}</span> : null}
      </span>
      {!editable ? (
        <span class="field-ro mono">{value}</span>
      ) : leaf.kind === 'bool' ? (
        <input type="checkbox" checked={leaf.value === true} data-path={path} onChange={(e) => commit((e.target as HTMLInputElement).checked)} />
      ) : long ? (
        <textarea
          class="input field-long mono" rows={1} ref={fit} value={value} data-path={path}
          onInput={(e) => fit(e.target as HTMLTextAreaElement)}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); (e.target as HTMLTextAreaElement).blur() } }}
          onChange={(e) => commit((e.target as HTMLTextAreaElement).value)}
        />
      ) : (
        <input type={leaf.kind === 'int' || leaf.kind === 'float' ? 'number' : 'text'} class="input mono" value={value} data-path={path} onChange={(e) => commit((e.target as HTMLInputElement).value)} />
      )}
      {isStaged ? <span class="field-badge">staged</span> : null}
    </label>
  )
}

const SECTION_TITLES: Record<string, string> = {
  settings: 'Settings', dashboard: 'Dashboard', store: 'Where conductor keeps its state', update: 'Updates', sandbox: 'Sandbox',
  usage: 'Usage limits', callable: 'Workflows other tools can call', memory: 'Memory', connectors: 'Connections', runtimes: 'Runtimes',
  hosts: 'Hosts', vaults: 'Vaults', secrets: 'Secrets', packs: 'Packs', bots: 'Bots', teams: 'Teams', operators: 'Operators',
  triggers: 'Workflows', browser: 'Browser', policy: 'Policy',
}
function sectionTitle(s: string): string {
  return SECTION_TITLES[s] ?? humanKey(s)
}
const KEY_LABELS: Record<string, string> = { state_file: 'State file', allow_open: 'Open without sign-in', listen: 'Listens on', use: 'Plugin' }
function leafLabel(path: string[]): string {
  const last = path[path.length - 1]
  if (last === 'enabled' && path.length > 1) return humanKey(path[path.length - 2]) + ' on'
  if (/^\d+$/.test(last) && path.length > 1) {
    const n = Number(last)
    return humanKey(path[path.length - 2]) + (n > 0 ? ` (${n + 1})` : '')
  }
  return humanKey(last)
}
function fit(el: HTMLTextAreaElement | null): void {
  if (!el) return
  el.style.height = 'auto'
  el.style.height = el.scrollHeight + 2 + 'px'
}
function humanKey(k: string): string {
  if (KEY_LABELS[k]) return KEY_LABELS[k]
  const words = k.replace(/[_-]+/g, ' ').trim()
  return words ? words[0].toUpperCase() + words.slice(1) : k
}
function fmt(v: unknown): string {
  if (v === null || v === undefined) return ''
  return String(v)
}
