// The Bots screen (prototype js/screens/botlist.js, "teams are the page"): each
// team is a section whose header says its shared rules in words (the most its
// bots may do, who approves, how work is routed to it, its budget) and builds
// the team in place (Add a bot, Shape the team, ⋯); its bots are below. Bots
// not on a team are cards of their own, sorted with the teams by name. A row
// says what the bot is for, then its facts (team lead, practice mode or
// archived, what it may do, what it's connected to, its model); its status is
// one quiet mark. Select rows for bulk changes, each staged into the one diff
// and reviewed on System · Config. With no bots yet it is a welcome instead.
import type { JSX } from 'preact'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { botsApi, displayName, type BotSummary, type TeamSummary } from '../api/bots'
import { humanError } from '../api/errors'
import { Avatar, AvatarPreview } from '../components/Avatar'
import { EmptyState } from '../components/EmptyState'
import { FactsLine, type Fact } from '../components/Facts'
import { FilterBar, type FilterMenu } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { Menu, type MenuItem } from '../components/Menu'
import { ReviewPanel } from '../components/ReviewPanel'
import { StatusMark } from '../components/StatusMark'
import { apply, applyResult, busy, cfgError, config, doReview, loadConfig, review, staged } from '../configStore'
import { api } from '../api/client'
import { plural } from '../lib/format'
import { AddToTeamForm, ArchiveConfirm, CloneForm, ModelForm, MoveForm, botPractice, setArchived, setPractice } from './botListActions'
import '../styles/bots-list.css'
import { TeamDelete, TeamForm, TeamPicture, cfgTree, toReview } from './TeamForm'

/* eslint-disable @typescript-eslint/no-explicit-any */
type Obj = Record<string, any>
const getPath = (o: Obj | undefined, p: string): any => p.split('.').reduce<any>((a, k) => (a == null ? undefined : a[k]), o)

const GLOSSARY = {
  bot: 'An AI teammate with memory you can chat with. It watches what you pick, tells you what it finds, and proposes actions; risky ones wait for you.',
  team: 'A group of bots that share approvers, limits and knowledge. A bot on a team can only be stricter than its team.',
}
function Term({ k, children }: { k: keyof typeof GLOSSARY; children: string }): JSX.Element {
  return (
    <span class="term" tabIndex={0} title={GLOSSARY[k]} data-term={k}>
      {children}
    </span>
  )
}

/* ---------- what a bot is, in facts ---------- */
const modelStr = (m: unknown): string => (typeof m === 'string' ? m : Array.isArray(m) ? String(m[0] ?? '') : m && typeof m === 'object' ? String((m as Obj).any?.[0] ?? '') : '')
function modelOf(b: BotSummary): string {
  const c = cfgTree().bots?.[b.bot] || {}
  return modelStr(getPath(c, 'worker.model')) || getPath(c, 'worker.runtime') || getPath(c, 'runtime') || b.home_runtime || 'paseo'
}
function connsOf(b: BotSummary): string[] {
  const t = cfgTree()
  const sv = getPath(t.bots?.[b.bot], 'skill.verbs')
  const vs: string[] = Array.isArray(sv) ? sv : sv ? Object.keys(sv) : []
  // The connection's own name (connectors.<name>), as the operator called it:
  // its plugin (`use:`) can be a generic one like "command" shared by many.
  const out = vs.map((v) => String(v).split('.')[0]).filter((inst) => inst && t.connectors?.[inst])
  return [...new Set(out)]
}
const mayWords = (b: BotSummary): string => {
  const own = b.capabilities?.does?.length || 0
  const asks = b.capabilities?.asks?.length || 0
  return own && asks ? `${own} on its own · asks before ${asks}` : asks ? `asks before everything (${asks})` : own ? `${own} on its own` : 'only reads'
}
const modeOf = (b: BotSummary) => (b.disabled ? 'archived' : botPractice(b) ? 'practice' : 'live')
function leadOf(t: string, teams: TeamSummary[]): string {
  return getPath(cfgTree().teams?.[t], 'routing.lead') || teams.find((x) => x.team === t)?.routing?.lead || ''
}

/* ---------- a team, in words ---------- */
const CEIL: Record<string, string> = {
  observe: 'Its bots only read',
  reversible: 'On their own, its bots do only things that can be undone; they ask before the rest',
  propose: 'Its bots ask before anything that changes something',
  commit_money: 'Its bots may spend within the budget',
}
function teamFacts(t: string, teams: TeamSummary[], name: (b: string) => string): Fact[] {
  const tree = cfgTree()
  const c: Obj = tree.teams?.[t] || {}
  const sum = teams.find((x) => x.team === t)
  const d: Obj = c.defaults || {}
  const ceil = getPath(c, 'autonomy.ceiling') || getPath(d, 'autonomy.max_tier')
  const ops: string[] = c.operators || d.operators || sum?.operators || []
  const rules: unknown[] = getPath(c, 'routing.rules') || sum?.routing?.rules || []
  const fb: string = getPath(c, 'routing.fallback') || sum?.routing?.fallback || ''
  const dollars = tree.usage?.mode === 'api'
  const budget =
    dollars && getPath(c, 'budget.spend_cap.per_period')
      ? `spend capped at $${Number(getPath(c, 'budget.spend_cap.per_period')).toLocaleString()}`
      : getPath(c, 'budget.proactive.max_runs_per_day')
        ? `${getPath(c, 'budget.proactive.max_runs_per_day')} self-started runs a day`
        : dollars && getPath(d, 'budget.max_cost_per_day')
          ? `$${getPath(d, 'budget.max_cost_per_day')} a day`
          : ''
  const enc = encodeURIComponent(t)
  return [
    ceil ? { icon: 'shield', value: CEIL[ceil] || ceil, href: `#/teams/${enc}/defaults`, pri: 1, label: 'The most its bots may do' } : null,
    ops.length ? { icon: 'members', value: `approved by ${ops.join(', ')}`, pri: 1, label: 'Who approves' } : null,
    rules.length || fb
      ? { icon: 'zap', value: `${rules.length ? plural(rules.length, 'routing rule') : ''}${rules.length && fb ? ' · ' : ''}${fb ? `the rest to ${name(fb)}` : ''}`, href: `#/teams/${enc}/routing`, pri: 2, label: 'How work is routed to it' }
      : { icon: 'zap', value: 'no routing yet', href: `#/teams/${enc}/routing`, pri: 2 },
    getPath(c, 'isolation.sandbox') === 'required' || getPath(d, 'isolation') ? { icon: 'obj-sandbox', value: 'sandboxed', pri: 3, label: 'Sandbox' } : null,
    budget ? { icon: 'dollar', value: budget, pri: 3, label: 'Budget' } : null,
  ].filter(Boolean) as Fact[]
}

// OneLineFacts keeps a team's facts on one line (ui.js UI.fitFacts): what
// doesn't fit hides from the end and a quiet "+N" says how many; it opens them
// in place.
function OneLineFacts({ facts }: { facts: Fact[] }): JSX.Element {
  const ref = useRef<HTMLSpanElement>(null)
  const [all, setAll] = useState(false)
  const [hide, setHide] = useState(0)
  const [w, setW] = useState(0)
  useEffect(() => {
    const on = () => setW(window.innerWidth)
    window.addEventListener('resize', on)
    return () => window.removeEventListener('resize', on)
  }, [])
  // The items are hidden on the DOM (FactsLine's own nodes); the +N is ours.
  useLayoutEffect(() => {
    const fx = ref.current
    if (!fx) return
    const items = [...fx.children].filter((x) => x.classList.contains('fx-i') && !x.classList.contains('fx-more')) as HTMLElement[]
    items.forEach((x) => (x.hidden = false))
    if (all) return
    const vis = items.filter((x) => getComputedStyle(x).display !== 'none')
    if (!vis.length || fx.offsetParent === null) return setHide(0)
    const real = fx.querySelector<HTMLElement>('.fx-more')
    if (real) real.style.display = 'none'
    const top = vis[0].offsetTop
    const wrapped = () => vis.filter((x) => !x.hidden).some((x) => x.offsetTop > top + 2)
    let n = 0
    if (wrapped()) {
      const more = document.createElement('span')
      more.className = 'fx-i fx-more'
      more.textContent = '+9'
      fx.appendChild(more)
      for (let i = vis.length - 1; i > 0 && (wrapped() || more.offsetTop > top + 2); i--) {
        vis[i].hidden = true
        n++
      }
      more.remove()
    }
    if (real) real.style.display = ''
    setHide(n)
  }, [facts.map((f) => f.value).join('|'), w, all])
  const rest = all ? [] : facts.slice(facts.length - hide)
  const line = FactsLine({ facts }) as any
  return (
    <span ref={ref} class={'fx' + (all ? ' fx-all' : '')}>
      {line?.props?.children}
      {hide && !all ? (
        <button
          type="button"
          class="fx-i fx-more"
          title={rest.map((f) => f.value).join(' · ')}
          aria-label={`${rest.length} more: ${rest.map((f) => f.value).join(' · ')}`}
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            setAll(true)
          }}
        >
          +{rest.length}
        </button>
      ) : null}
    </span>
  )
}

type Modal =
  | { k: 'team-new'; withBots?: string[] }
  | { k: 'team-edit'; team: string }
  | { k: 'team-pic'; team: string }
  | { k: 'team-del'; team: string }
  | { k: 'team-add'; team: string }
  | { k: 'move'; names: string[] }
  | { k: 'model'; names: string[] }
  | { k: 'archive'; names: string[] }
  | { k: 'clone'; name: string }
  | null

export function Bots(): JSX.Element {
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [editable, setEditable] = useState(true)
  const [err, setErr] = useState('')
  // The filter (botlist.js bv): search, Team / Mode / Model / Connected to, Group by.
  const [q, setQ] = useState('')
  const [fTeam, setFTeam] = useState<string[]>([])
  const [fMode, setFMode] = useState<string[]>([])
  const [fModel, setFModel] = useState<string[]>([])
  const [fConn, setFConn] = useState<string[]>([])
  const [groupBy, setGroupBy] = useState<'team' | 'none'>('team')
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [moreRows, setMoreRows] = useState<Record<string, boolean>>({})
  const [menu, setMenu] = useState<{ anchor: HTMLElement; items: MenuItem[]; label: string } | null>(null)
  const [modal, setModal] = useState<Modal>(null)
  const [note, setNote] = useState('')
  // The cli runtimes a bot's workers can run on (null: not known yet).
  const [workers, setWorkers] = useState<string[] | null>(null)

  const load = () =>
    botsApi
      .bots()
      .then((r) => {
        setBots(r.bots || [])
        setTeams(r.teams || [])
        setEditable(r.editable !== false)
        setErr('')
        return (r.bots || []).length
      })
      .catch((e) => {
        setErr(humanError(e))
        return -1
      })
  useEffect(() => {
    void load()
    void loadConfig()
    api
      .connections()
      .then((c) => setWorkers((c.runtimes ?? []).filter((r) => r.type === 'cli').map((r) => r.name)))
      .catch(() => setWorkers(null))
  }, [])

  // An apply made here reloads the daemon; a new bot shows once it is back.
  // (applyResult is shared, so one left from another screen doesn't count.)
  const appliedAtMount = useRef(applyResult.value)
  const applied = applyResult.value && applyResult.value !== appliedAtMount.current ? applyResult.value : null
  const before = useRef(0)
  const [gaveUp, setGaveUp] = useState(false)
  useEffect(() => {
    if (!applied) return
    before.current = bots?.length ?? 0
    setGaveUp(false)
    let tries = 0
    let stop = false
    const poll = () => {
      if (stop) return
      void load().then((n) => {
        if (stop || n > before.current) return
        if (++tries < 20) setTimeout(poll, 1000)
        else setGaveUp(true)
      })
    }
    const t = setTimeout(poll, 800)
    return () => {
      stop = true
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applied])

  void config.value // re-render when the config tree arrives (facts read it)
  const all = bots || []
  const byName = (n: string) => all.find((b) => b.bot === n)
  const nm = (n: string) => {
    const b = byName(n)
    return b ? displayName(b.identity, b.bot) : n
  }

  // a `key:value` in the search is a filter (team:platform, mode:practice, model:…, conn:…)
  const quals = useMemo(() => {
    const out: Record<string, string[]> = {}
    const words: string[] = []
    for (const w of q.trim().split(/\s+/).filter(Boolean)) {
      const m = w.match(/^(team|mode|model|conn|connected):(.+)$/i)
      if (m) (out[m[1].toLowerCase() === 'connected' ? 'conn' : m[1].toLowerCase()] ||= []).push(m[2].toLowerCase())
      else words.push(w)
    }
    return { out, text: words.join(' ').toLowerCase() }
  }, [q])
  const keep = (b: BotSummary) => {
    const team = b.team || 'solo'
    const model = modelOf(b)
    const conns = connsOf(b)
    const mode = modeOf(b)
    if (fTeam.length && !fTeam.includes(team)) return false
    if (fMode.length && !fMode.includes(mode)) return false
    if (fModel.length && !fModel.includes(model)) return false
    if (fConn.length && !conns.some((x) => fConn.includes(x))) return false
    const qo = quals.out
    if (qo.team && !qo.team.includes(team.toLowerCase())) return false
    if (qo.mode && !qo.mode.includes(mode)) return false
    if (qo.model && !qo.model.includes(model.toLowerCase())) return false
    if (qo.conn && !conns.some((x) => qo.conn.includes(x.toLowerCase()))) return false
    const t = quals.text
    return !t || `${b.bot} ${displayName(b.identity, b.bot)} ${b.description || ''} ${b.team || ''}`.toLowerCase().includes(t)
  }

  const go = (href: string) => {
    location.hash = href.replace(/^#/, '')
  }
  const staged1 = (s: string) => {
    if (!cfgError.value) setNote(s)
  }

  if (bots === null) {
    return (
      <div class="m-page bot-list">
        <div class="dl-phead"><div><h1>Bots</h1></div></div>
        {err ? (
          <div class="banner err" role="alert">
            Couldn't load your bots: {err}{' '}
            <button class="btn sm" type="button" onClick={() => void load()}>Try again</button>
          </div>
        ) : (
          <p class="muted">Loading…</p>
        )}
      </div>
    )
  }

  const pendingBots = staged.value.filter((c) => c.path[0] === 'bots').length
  const noWorkers = workers !== null && workers.length === 0

  if (all.length === 0) {
    return (
      <div class="m-page bot-list">
        <div class="dl-phead"><div><h1>Bots</h1></div></div>
        {err ? <div class="banner err" role="alert">{err}</div> : null}
        {applied ? (
          <>
            <ReviewPanel stagedCount={staged.value.length} />
            {gaveUp ? (
              <div class="banner attn" role="status" data-testid="bots-waiting">
                Your bot is applied but hasn't shown up yet. conductor may still be reloading: reload this page in a moment. If it is still missing, the daemon log says why.
              </div>
            ) : (
              <p class="muted" data-testid="bots-waiting">Starting your new bot…</p>
            )}
          </>
        ) : pendingBots ? (
          <>
            {/* The next step is this card's one button: review, then apply. */}
            <EmptyState
              testid="bots-staged"
              title="Your first bot is ready to apply"
              action={
                review.value?.token
                  ? { label: 'Apply and start your bot', onClick: () => void apply(), testid: 'bots-apply', disabled: busy.value }
                  : { label: 'Review and apply', onClick: () => void doReview(), testid: 'bots-review', disabled: busy.value }
              }
            >
              <dl class="bot-summary" data-testid="bot-summary">
                {botSummary(staged.value).map(([k, v]) => (
                  <>
                    <dt>{k}</dt>
                    <dd>{v}</dd>
                  </>
                ))}
              </dl>
              <p>Apply it and conductor reloads with your new bot. It starts in practice mode, so it only suggests until you let it act.</p>
            </EmptyState>
            <ReviewPanel stagedCount={staged.value.length} guided />
          </>
        ) : (
          <EmptyState
            hero
            testid="bots-empty"
            art={<WelcomeFaces />}
            title="Meet your first bot"
            action={
              !editable
                ? undefined
                : noWorkers
                  ? { label: 'Open the config', href: '#/config', testid: 'bots-empty-runtime' }
                  : { label: 'Create your first bot', href: '#/bots/new', testid: 'create-first-bot' }
            }
          >
            <p>
              A bot is a teammate you hand work to: it does the jobs you allow on its own, asks before anything that matters, and starts in practice mode.
            </p>
            {!editable ? (
              <p>This dashboard was started without a config file, so it can't add one. Add a bot under <code>bots:</code> in conductor's config instead.</p>
            ) : noWorkers ? (
              <p data-testid="bots-need-runtime">
                First, a bot needs somewhere to do its work: an agent command-line runtime on this machine. Add one under <code>runtimes:</code> in the config (for example <code>claude: {'{'} use: cli, tool: claude-code {'}'}</code>), then come back here.
              </p>
            ) : null}
          </EmptyState>
        )}
      </div>
    )
  }

  /* ---------- the page ---------- */
  const teamNames = teams.map((t) => t.team)
  const solo = all.filter((b) => !b.team)
  const shown = all.filter(keep)
  const total = all.length
  const tally = (xs: string[]) => {
    const m = new Map<string, number>()
    xs.filter(Boolean).forEach((x) => m.set(x, (m.get(x) || 0) + 1))
    return [...m].map(([v, n]) => ({ v, label: v, n }))
  }
  const toggle = (set: (f: (x: string[]) => string[]) => void) => (v: string) => set((xs) => (xs.includes(v) ? xs.filter((y) => y !== v) : [...xs, v]))
  const menus: FilterMenu[] = [
    { key: 'team', label: 'Team', multi: true, selected: fTeam, options: tally(all.map((b) => b.team || 'solo')), onToggle: toggle(setFTeam), onClear: () => setFTeam([]) },
    {
      key: 'mode', label: 'Mode', multi: true, selected: fMode,
      options: ([['live', 'Live'], ['practice', 'Practice mode'], ['archived', 'Archived']] as const).map(([v, label]) => ({ v, label, n: all.filter((b) => modeOf(b) === v).length })).filter((o) => o.n),
      onToggle: toggle(setFMode), onClear: () => setFMode([]),
    },
    { key: 'model', label: 'Model', multi: true, selected: fModel, options: tally(all.map(modelOf)), onToggle: toggle(setFModel), onClear: () => setFModel([]) },
    { key: 'conn', label: 'Connected to', multi: true, selected: fConn, options: tally(all.flatMap(connsOf)), onToggle: toggle(setFConn), onClear: () => setFConn([]) },
    { key: 'group', label: 'Group by', view: true, title: 'Group by', selected: [groupBy], options: [{ v: 'team', label: 'Team' }, { v: 'none', label: 'None' }], onToggle: (v) => setGroupBy(v as 'team' | 'none') },
  ]

  const toggleSel = (name: string) =>
    setSel((s) => {
      const next = new Set(s)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  const names = [...sel]

  const rowMenu = (b: BotSummary, anchor: HTMLElement) => {
    const n = b.bot
    const prac = botPractice(b)
    setMenu({
      anchor,
      label: `More for ${n}`,
      items: [
        { label: 'Open', icon: 'bots', onClick: () => go(`#/bots/${encodeURIComponent(n)}`) },
        { label: 'Edit its settings', icon: 'edit', onClick: () => go(`#/bots/${encodeURIComponent(n)}/settings`) },
        { label: 'Clone…', icon: 'copy', onClick: () => setModal({ k: 'clone', name: n }) },
        { label: 'Move to a team…', icon: 'members', onClick: () => setModal({ k: 'move', names: [n] }) },
        { label: prac ? 'Switch to live…' : 'Practice mode', icon: 'ghost', onClick: () => void setPractice([n], !prac).then(toReview) },
        '-',
        b.disabled
          ? { label: 'Switch back on', icon: 'box', onClick: () => void setArchived([n], false).then(toReview) }
          : { label: 'Archive…', icon: 'box', onClick: () => setModal({ k: 'archive', names: [n] }) },
      ],
    })
  }
  const teamMenu = (t: string, anchor: HTMLElement) => {
    const e = encodeURIComponent(t)
    setMenu({
      anchor,
      label: `More for team ${t}`,
      items: [
        { label: 'Open the team', icon: 'members', onClick: () => go(`#/teams/${e}`) },
        { label: 'Change its picture…', icon: 'image', onClick: () => setModal({ k: 'team-pic', team: t }) },
        { label: 'Shape the team', icon: 'settings', onClick: () => go(`#/teams/${e}/defaults`) },
        { label: 'How work is routed…', icon: 'zap', onClick: () => go(`#/teams/${e}/routing`) },
        { label: 'Shared knowledge', icon: 'file', onClick: () => go(`#/teams/${e}/knowledge`) },
        '-',
        { label: 'Edit the team…', icon: 'edit', onClick: () => setModal({ k: 'team-edit', team: t }) },
      ],
    })
  }

  const row = (b: BotSummary, o: { team?: boolean } = {}) => (
    <BotRow key={b.bot} b={b} showTeam={o.team} lead={!!b.team && leadOf(b.team, teams) === b.bot} sel={sel.has(b.bot)} onToggleSel={() => toggleSel(b.bot)} onMore={(el) => rowMenu(b, el)} />
  )
  const isOpen = (key: string, i: number) => (key in open ? open[key] : total <= 40 || i === 0)
  const teamSection = (t: string, ms: BotSummary[], i: number) => {
    const sum = teams.find((x) => x.team === t)
    const o = isOpen(t, i)
    const lead = leadOf(t, teams)
    const prac = ms.filter(botPractice).length
    const need = ms.reduce((a, b) => a + (b.pending_proposals || 0), 0)
    const vis = ms.filter(keep)
    const e = encodeURIComponent(t)
    return (
      <section class="dl-card m-team" data-team={t} key={'t:' + t}>
        <header class="m-team-h">
          <button type="button" class="m-tog" aria-expanded={o} aria-label={(o ? 'Fold ' : 'Show ') + t} onClick={() => setOpen((x) => ({ ...x, [t]: !o }))}>
            <Icon name={o ? 'chevd' : 'chev'} />
          </button>
          <div class="m-team-t">
            <div class="m-team-n">
              <Avatar id={sum?.identity} size={32} kind="team" />
              <h2><a class="link-plain" href={`#/teams/${e}`}>{t}</a></h2>
            </div>
            <span class="dl-sub m-team-sum">
              {plural(ms.length, 'bot')}
              {lead ? ` · led by ${nm(lead)}` : ' · no lead yet'}
              {prac ? ` · ${prac} in practice mode` : ''}
            </span>
            <OneLineFacts facts={teamFacts(t, teams, nm)} />
          </div>
          <span class="m-st">
            {need ? (
              <a class="q-st need" href="#/attention"><StatusMark s="ember" mkOnly label="Needs you" />{need} need{need > 1 ? '' : 's'} you</a>
            ) : null}
          </span>
          <div class="m-team-a">
            {editable ? (
              <button type="button" class="btn sm secondary" data-testid="team-add-bot" onClick={() => setModal({ k: 'team-add', team: t })}><Icon name="plus" />Add a bot</button>
            ) : null}
            <a class="btn sm secondary ph-hide" href={`#/teams/${e}/defaults`}><Icon name="settings" />Shape the team</a>
            <button type="button" class="icon-btn" aria-label={`More for team ${t}`} onClick={(ev) => teamMenu(t, ev.currentTarget as HTMLElement)}><Icon name="more" /></button>
          </div>
        </header>
        {o ? (
          <ul class="m-list">
            {vis.length ? vis.slice(0, moreRows[t] ? 1e9 : 10).map((b) => row(b)) : <li class="dl-empty-row">No bot here matches the filter.</li>}
            {vis.length > 10 && !moreRows[t] ? (
              <li class="dl-more-row">
                <button class="link" type="button" onClick={() => setMoreRows((x) => ({ ...x, [t]: true }))}>Show {vis.length - 10} more on {t}</button>
              </li>
            ) : null}
          </ul>
        ) : null}
      </section>
    )
  }

  const groups =
    groupBy === 'none' ? (
      <section class="dl-card m-team">
        <ul class="m-list m-flat">
          {shown.length ? [...shown].sort((a, b) => a.bot.localeCompare(b.bot)).map((b) => row(b, { team: true })) : <li class="dl-empty-row">No bot matches.</li>}
        </ul>
      </section>
    ) : (
      // teams and the bots not on a team are peers: each its own card, one list sorted by name
      [
        ...teamNames.filter((t) => !fTeam.length || fTeam.includes(t)).map((t) => [t, () => teamSection(t, all.filter((b) => b.team === t), teamNames.indexOf(t))] as const),
        ...(!fTeam.length || fTeam.includes('solo')
          ? solo.filter(keep).map((b) => [nm(b.bot), () => (
              <section class="dl-card m-team m-solo" data-team="solo" data-bot={b.bot} aria-label={`${nm(b.bot)}, not on a team`} key={'b:' + b.bot}>
                <ul class="m-list">{row(b)}</ul>
              </section>
            )] as const)
          : []),
      ]
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([, f]) => f())
    )

  return (
    <div class="m-page bot-list">
      <div class="dl-phead">
        <div>
          <h1>
            Bots{' '}
            <span class="h1-count">
              {plural(total, 'bot')} · {plural(teamNames.length, 'team')} · {solo.length} solo{shown.length < total ? ` · showing ${shown.length}` : ''}
            </span>
          </h1>
          <p class="lede-2">
            A <Term k="bot">bot</Term> is an AI teammate with memory; on a <Term k="team">team</Term>, bots share who approves, the most they may do, and the work routed to them.
          </p>
        </div>
        {editable ? (
          <div class="dl-actions tight">
            <button class="btn secondary" type="button" data-testid="new-team" onClick={() => setModal({ k: 'team-new' })}><Icon name="members" />New team</button>
            <a class="btn primary" href="#/bots/new" data-testid="new-bot"><Icon name="plus" />New bot</a>
          </div>
        ) : null}
      </div>

      {err ? <div class="banner err" role="alert">{err}</div> : null}
      {cfgError.value ? <div class="banner err" role="alert">Couldn't stage that: {cfgError.value}</div> : null}
      {note && !cfgError.value ? (
        <div class="banner" role="status" data-testid="bots-staged-note">
          Staged: {note}. Not applied yet. <a class="link" href="#/system/config">Review and apply</a>
        </div>
      ) : null}
      {applied ? <ReviewPanel stagedCount={staged.value.length} /> : null}

      <FilterBar q={q} onQueryChange={setQ} placeholder="Find a bot, or filter: team:platform" ariaLabel="Find and filter bots" filterByHint menus={menus} />

      {sel.size ? (
        <div class="m-bulk">
          <b>{sel.size} selected</b>
          <button class="btn sm secondary" type="button" onClick={() => setModal({ k: 'move', names })}>Move to a team…</button>
          <button class="btn sm secondary" type="button" onClick={() => { setSel(new Set()); setModal({ k: 'team-new', withBots: names }) }}><Icon name="members" />Make a team…</button>
          <button class="btn sm secondary" type="button" onClick={() => setModal({ k: 'model', names })}>Change model…</button>
          <button class="btn sm secondary" type="button" onClick={() => void setPractice(names, true).then(() => { setSel(new Set()); toReview() })}>Practice mode</button>
          <button class="btn sm secondary" type="button" onClick={() => setModal({ k: 'archive', names })}>Archive…</button>
          <button class="link" type="button" onClick={() => setSel(new Set())}>Clear</button>
        </div>
      ) : null}

      {groups}

      {menu ? <Menu anchor={menu.anchor} items={menu.items} label={menu.label} onClose={() => setMenu(null)} /> : null}
      {modal ? (
        modal.k === 'team-new' ? <TeamForm withBots={modal.withBots} onClose={() => setModal(null)} />
        : modal.k === 'team-edit' ? <TeamForm team={modal.team} onClose={() => setModal(null)} />
        : modal.k === 'team-pic' ? <TeamPicture team={modal.team} onClose={() => setModal(null)} onStaged={staged1} />
        : modal.k === 'team-del' ? <TeamDelete team={modal.team} onClose={() => setModal(null)} />
        : modal.k === 'team-add' ? <AddToTeamForm bots={all} team={modal.team} onClose={() => setModal(null)} />
        : modal.k === 'move' ? <MoveForm bots={all} teams={teams} names={modal.names} onClose={() => { setModal(null); setSel(new Set()) }} />
        : modal.k === 'model' ? <ModelForm names={modal.names} onClose={() => { setModal(null); setSel(new Set()) }} />
        : modal.k === 'archive' ? <ArchiveConfirm names={modal.names} onClose={() => { setModal(null); setSel(new Set()) }} />
        : modal.k === 'clone' ? <CloneForm bots={all} name={modal.name} onClose={() => setModal(null)} />
        : null
      ) : null}
    </div>
  )
}

// botSummary says a staged new bot in plain words: its name, what it's for,
// its team, where it works and its avatar.
function botSummary(changes: { path: string[]; value?: unknown; delete?: boolean }[]): [string, string][] {
  const f: Record<string, string> = {}
  let id = ''
  for (const c of changes) {
    if (c.path[0] !== 'bots' || c.delete) continue
    id = c.path[1]
    if (c.path.length === 2 && c.value && typeof c.value === 'object') {
      const flat = (o: Obj, pre: string) => Object.entries(o).forEach(([k, v]) => (v && typeof v === 'object' && !Array.isArray(v) ? flat(v, pre + k + '.') : (f[pre + k] = String(v ?? ''))))
      flat(c.value as Obj, '')
    } else f[c.path.slice(2).join('.')] = String(c.value ?? '')
  }
  const out: [string, string][] = [['Name', f['display_name'] || id]]
  if (f['description']) out.push(['Description', f['description']])
  if (f['team']) out.push(['Team', f['team']])
  if (f['worker.runtime']) out.push(['Works on', f['worker.runtime']])
  const face = f['avatar.icon']
    ? [[f['avatar.color'], f['avatar.icon'], 'icon'].filter(Boolean).join(' ')]
    : [[f['avatar.color'], f['avatar.shape']].filter(Boolean).join(' '), f['avatar.eyes'] ? `${f['avatar.eyes']} eyes` : '']
  const faceText = face.filter(Boolean).join(', ')
  if (faceText) out.push(['Avatar', faceText])
  return out
}

// WelcomeFaces is three sample faces from the identity picker: what a bot
// looks like here, before there is one.
function WelcomeFaces(): JSX.Element {
  return (
    <>
      <AvatarPreview spec={{ shape: 'blob', eyes: 'happy' }} color="violet" size={52} />
      <AvatarPreview spec={{ shape: 'hexagon', eyes: 'round' }} color="teal" size={68} />
      <AvatarPreview spec={{ shape: 'squircle', eyes: 'focused' }} color="ocean" size={52} />
    </>
  )
}

function BotRow({ b, showTeam, lead, sel, onToggleSel, onMore }: { b: BotSummary; showTeam?: boolean; lead: boolean; sel: boolean; onToggleSel: () => void; onMore: (el: HTMLElement) => void }): JSX.Element {
  const name = displayName(b.identity, b.bot)
  const conns = connsOf(b)
  const e = encodeURIComponent(b.bot)
  const facts: (Fact | null)[] = [
    showTeam ? (b.team ? { icon: 'obj-team', value: b.team, href: `#/teams/${encodeURIComponent(b.team)}`, pri: 1, label: 'Team' } : { icon: 'obj-team', value: 'solo', pri: 1, label: 'Not on a team' }) : null,
    lead ? { icon: 'sparkle', value: 'lead', pri: 1, label: 'Leads its team' } : null,
    b.disabled ? { icon: 'box', value: 'archived', pri: 1, label: 'Switched off' } : botPractice(b) ? { icon: 'ghost', value: 'practice mode', pri: 1, label: 'Says what it would do, never acts' } : null,
    { icon: 'shield', value: mayWords(b), href: `#/bots/${e}/access`, pri: 2, label: 'What it may do' },
    conns.length ? { icon: 'link', value: conns.join(', '), pri: 2, label: 'Connected to' } : null,
    { icon: 'code', value: modelOf(b), pri: 3, label: 'Model or runtime' },
  ]
  const need = b.pending_proposals || 0
  return (
    <li class={'m-row' + (b.disabled ? ' off' : '')} data-bot={b.bot}>
      <span class="m-sel">
        <input type="checkbox" class="cbx m-cbx" checked={sel} onChange={onToggleSel} aria-label={`Select ${b.bot}`} />
      </span>
      <Avatar id={b.identity} size={32} />
      <a class="m-main" href={`#/bots/${e}`}>
        <b>{name}{name !== b.bot ? <> <span class="bav-id">{b.bot}</span></> : null}</b>
        <span class="dl-why">{b.description || ''}</span>
        <FactsLine facts={facts} />
      </a>
      <span class="m-st">
        {need ? (
          <a class="q-st need" href="#/attention"><StatusMark s="ember" mkOnly label="Needs you" />{need} need{need > 1 ? '' : 's'} you</a>
        ) : b.live_workers > 0 ? (
          <a class="q-st" href={`#/activity?bot=${e}`}><StatusMark s="run" mkOnly label="Working" />Working</a>
        ) : null}
      </span>
      <button type="button" class="icon-btn" aria-label={`More for ${b.bot}`} onClick={(ev) => onMore(ev.currentTarget as HTMLElement)}><Icon name="more" /></button>
    </li>
  )
}
