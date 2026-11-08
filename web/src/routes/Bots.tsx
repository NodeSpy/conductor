// The Bots screen (design §4.4, "teams are the page"): each team is a section
// whose header states its shared rules in words, with its bots below; solo bots
// come last. Group by Team or None. New bot opens a sheet with the identity
// picker. Managing a bot's settings and faces is on its page; this is the roster.
// With no bots yet it is a welcome instead: what a bot is for and one way to
// make the first, staged and applied right here.
import type { JSX } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { botsApi, displayName, type BotSummary, type TeamSummary } from '../api/bots'
import { humanError } from '../api/errors'
import { Avatar, AvatarPreview } from '../components/Avatar'
import { EmptyState } from '../components/EmptyState'
import { FilterBar } from '../components/FilterBar'
import { Icon } from '../components/Icon'
import { ReviewPanel } from '../components/ReviewPanel'
import { StatusMark } from '../components/StatusMark'
import { apply, applyResult, busy, doReview, loadConfig, review, stage, staged } from '../configStore'
import { api } from '../api/client'
import { NewBotSheet } from './NewBotSheet'
import { plural } from '../lib/format'
import { useHashFlag } from '../lib/hashFlag'

export function Bots(): JSX.Element {
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [editable, setEditable] = useState(true)
  const [err, setErr] = useState('')
  const [groupBy, setGroupBy] = useState<'team' | 'none'>('team')
  const [q, setQ] = useState('')
  const [mode, setMode] = useState<'all' | 'live' | 'practice' | 'archived'>('all')
  const [newOpen, setNewOpen] = useState(false)
  // #/bots?new=1 (the ⌘K palette's New bot) opens the sheet.
  useHashFlag('new', () => setNewOpen(true))
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

  const all = bots || []
  const filtered = useMemo(() => {
    return all.filter((b) => {
      if (q && !(`${b.bot} ${displayName(b.identity, b.bot)} ${b.description}`.toLowerCase().includes(q.toLowerCase()))) return false
      if (mode === 'live' && (b.shadow || b.disabled)) return false
      if (mode === 'practice' && !b.shadow) return false
      if (mode === 'archived' && !b.disabled) return false
      return true
    })
  }, [all, q, mode])

  const soloCount = all.filter((b) => !b.team).length
  const pendingBots = staged.value.filter((c) => c.path[0] === 'bots').length
  const noWorkers = workers !== null && workers.length === 0
  const sheet = newOpen ? <NewBotSheet teams={teams} workers={workers} onClose={() => setNewOpen(false)} onStaged={() => setNewOpen(false)} /> : null

  if (bots === null) {
    return (
      <div class="m-page col bots-screen">
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

  if (all.length === 0) {
    return (
      <div class="m-page col bots-screen">
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
                  : { label: 'Create your first bot', onClick: () => setNewOpen(true), testid: 'create-first-bot' }
            }
          >
            <p>A bot is a teammate you hand work to. It picks up what comes in, does the jobs you allow on its own, and asks you before anything that matters.</p>
            <p>Every new bot starts in practice mode: it only suggests, until you let it act.</p>
            {!editable ? (
              <p>This dashboard was started without a config file, so it can't add one. Add a bot under <code>bots:</code> in conductor's config instead.</p>
            ) : noWorkers ? (
              <p data-testid="bots-need-runtime">
                First, a bot needs somewhere to do its work: an agent command-line runtime on this machine. Add one under <code>runtimes:</code> in the config (for example <code>claude: {'{'} use: cli, tool: claude-code {'}'}</code>), then come back here.
              </p>
            ) : null}
          </EmptyState>
        )}
        {sheet}
      </div>
    )
  }

  const [sel, setSel] = useState<Set<string>>(new Set())
  const toggleSel = (name: string) =>
    setSel((s) => {
      const next = new Set(s)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  const [archiving, setArchiving] = useState(false)
  const bulkArchive = async () => {
    const names = [...sel]
    if (!names.length) return
    if (!confirm(`Archive ${plural(names.length, 'bot')}? Each stops: it hears no events and starts nothing. Its memory and history stay.`)) return
    setArchiving(true)
    try {
      for (const n of names) await stage(['bots', n, 'disabled'], true, `archive ${n}`)
    } finally {
      setSel(new Set())
      setArchiving(false)
    }
  }

  return (
    <div class="m-page col bots-screen">
      <div class="dl-phead">
        <div>
          <h1>Bots <span class="h1-count">{rosterCount(all.length, teams.length, soloCount)}</span></h1>
          {teams.length ? <p class="lede-2">Each team is a section; its header says what its bots may do, who approves and how work reaches it. Solo bots come last.</p> : null}
        </div>
        <div class="dl-actions tight">
          {editable ? <a class="btn primary" href="#/bots" onClick={(e) => { e.preventDefault(); setNewOpen(true) }} data-testid="new-bot"><Icon name="plus" />New bot</a> : null}
        </div>
      </div>

      {err ? <div class="banner err" role="alert">{err}</div> : null}
      {pendingBots || applied ? <ReviewPanel stagedCount={staged.value.length} /> : null}

      {sel.size ? (
        <div class="m-bulk">
          <b>{sel.size} selected</b>
          <button class="btn sm secondary" type="button" disabled={archiving} onClick={() => void bulkArchive()}>Archive…</button>
          <button class="link" type="button" onClick={() => setSel(new Set())}>Clear</button>
        </div>
      ) : null}

      {all.length > 1 ? (
        <FilterBar
          q={q}
          onQueryChange={setQ}
          placeholder="Find a bot"
          ariaLabel="Find a bot"
          menus={[
            {
              key: 'mode',
              label: 'Mode',
              selected: mode === 'all' ? [] : [mode],
              options: [
                { v: 'live', label: 'Live' },
                { v: 'practice', label: 'Practice mode' },
                { v: 'archived', label: 'Archived' },
              ],
              onToggle: (v) => setMode((cur) => (cur === v ? 'all' : (v as typeof mode))),
              onClear: () => setMode('all'),
            },
            ...(teams.length
              ? [
                  {
                    key: 'group',
                    label: 'Group by',
                    view: true,
                    selected: [groupBy],
                    options: [
                      { v: 'team', label: 'Team' },
                      { v: 'none', label: 'None' },
                    ],
                    onToggle: (v: string) => setGroupBy(v as 'team' | 'none'),
                  },
                ]
              : []),
          ]}
        />
      ) : null}

      {groupBy === 'none' || !teams.length ? (
        <section class="dl-card m-team">
          <ul class="m-list m-flat">
            {filtered.map((b) => <BotRow key={b.bot} b={b} showTeam={groupBy === 'none'} sel={sel} onToggleSel={toggleSel} />)}
          </ul>
          {filtered.length ? null : <li class="dl-empty-row">No bot matches.</li>}
        </section>
      ) : (
        <>
          {teams.map((t) => {
            const teamBots = filtered.filter((b) => b.team === t.team)
            return <TeamSection key={t.team} t={t} bots={teamBots} sel={sel} onToggleSel={toggleSel} />
          })}
          <SoloSection bots={filtered.filter((b) => !b.team)} sel={sel} onToggleSel={toggleSel} />
        </>
      )}

      {sheet}
    </div>
  )
}

// rosterCount is the header's count: teams and solo bots only once there are
// teams (without any, every bot is solo).
function rosterCount(bots: number, teams: number, solo: number): string {
  const parts = [plural(bots, 'bot')]
  if (teams) parts.push(plural(teams, 'team'), `${solo} solo`)
  return parts.join(' · ')
}

// botSummary says a staged new bot in plain words: its name, what it's for,
// its team, where it works and its avatar.
function botSummary(changes: { path: string[]; value?: unknown; delete?: boolean }[]): [string, string][] {
  const f: Record<string, string> = {}
  let id = ''
  for (const c of changes) {
    if (c.path[0] !== 'bots' || c.delete) continue
    id = c.path[1]
    f[c.path.slice(2).join('.')] = String(c.value ?? '')
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

interface SelProps {
  sel: Set<string>
  onToggleSel: (name: string) => void
}

function TeamSection({ t, bots, sel, onToggleSel }: { t: TeamSummary; bots: BotSummary[] } & SelProps): JSX.Element {
  // A section opens by default; its own fold state (m-tog, design §4.4) is the
  // header's first grid column — without it the rest of the header has no
  // second/third/fourth column to land in and collapses into that column's
  // own 28px width instead.
  const [open, setOpen] = useState(true)
  const lead = t.routing?.lead
  const rules: string[] = []
  rules.push(lead ? `led by ${lead}` : 'no lead yet — what you write goes to everyone')
  if (t.operators?.length) rules.push(`approved by ${t.operators.join(', ')}`)
  if (t.routing?.rules?.length) rules.push(plural(t.routing.rules.length, 'routing rule'))
  rules.push(t.peers === 'none' ? 'bots kept apart' : 'teammates may ask each other')
  const need = bots.reduce((a, b) => a + (b.pending_proposals || 0), 0)
  return (
    <section class="dl-card m-team" data-team={t.team}>
      <header class="m-team-h">
        <button type="button" class="m-tog" aria-expanded={open} aria-label={(open ? 'Fold ' : 'Show ') + t.team} onClick={() => setOpen((v) => !v)}>
          <Icon name={open ? 'chevd' : 'chev'} />
        </button>
        <div class="m-team-t">
          <div class="m-team-n">
            <Avatar id={t.identity} size={32} kind="team" />
            <h2><a class="link-plain" href={`#/teams/${t.team}`}>{displayName(t.identity, t.team)}</a></h2>
          </div>
          <span class="dl-sub m-team-sum">{plural(t.bots?.length || 0, 'bot')}{t.description ? ` · ${t.description}` : ''}</span>
          <span class="fx">{rules.map((r) => <span class="fx-i">{r}</span>)}</span>
        </div>
        <span class="m-st">
          {need ? <a class="q-st need" href="#/attention"><StatusMark s="ember" mkOnly label="Needs you" />{plural(need, 'need', 'need')} you</a> : null}
        </span>
        <div class="m-team-a">
          <a class="btn sm secondary ph-hide" href={`#/teams/${t.team}`}><Icon name="settings" />Shape the team</a>
        </div>
      </header>
      {open ? (
        <ul class="m-list">
          {bots.length ? bots.map((b) => <BotRow key={b.bot} b={b} sel={sel} onToggleSel={onToggleSel} />) : <li class="dl-empty-row">No bots in this team match.</li>}
        </ul>
      ) : null}
    </section>
  )
}

function SoloSection({ bots, sel, onToggleSel }: { bots: BotSummary[] } & SelProps): JSX.Element {
  const [open, setOpen] = useState(true)
  if (!bots.length) return <></>
  return (
    <section class="dl-card m-team m-solo" data-team="(solo)">
      <header class="m-team-h">
        <button type="button" class="m-tog" aria-expanded={open} aria-label={(open ? 'Fold' : 'Show') + ' solo bots'} onClick={() => setOpen((v) => !v)}>
          <Icon name={open ? 'chevd' : 'chev'} />
        </button>
        <div class="m-team-t">
          <div class="m-team-n"><h2>Solo bots</h2></div>
          <span class="dl-sub m-team-sum">{plural(bots.length, 'bot')} · not on a team, each with its own rules</span>
        </div>
      </header>
      {open ? (
        <ul class="m-list">
          {bots.map((b) => <BotRow key={b.bot} b={b} sel={sel} onToggleSel={onToggleSel} />)}
        </ul>
      ) : null}
    </section>
  )
}

function BotRow({ b, showTeam, sel, onToggleSel }: { b: BotSummary; showTeam?: boolean } & SelProps): JSX.Element {
  const does = b.capabilities?.does || []
  const asks = b.capabilities?.asks || []
  const facts: string[] = []
  if (showTeam) facts.push(b.team ? `team ${b.team}` : 'solo')
  if (b.disabled) facts.push('archived')
  else if (b.shadow) facts.push('practice mode')
  facts.push(does.length && asks.length ? `${does.length} on its own · asks before ${asks.length}` : asks.length ? `asks before everything (${asks.length})` : does.length ? `${does.length} on its own` : 'only reads')
  if (b.home_runtime) facts.push(b.home_runtime)
  const needs = b.pending_proposals > 0
  return (
    <li class={'m-row' + (b.disabled ? ' off' : '')} data-bot={b.bot}>
      <span class="m-sel">
        <input type="checkbox" class="cbx" checked={sel.has(b.bot)} onChange={() => onToggleSel(b.bot)} aria-label={`Select ${b.bot}`} />
      </span>
      <Avatar id={b.identity} size={32} />
      <a class="m-main" href={`#/bots/${b.bot}`}>
        <b>{displayName(b.identity, b.bot)}{displayName(b.identity, b.bot) !== b.bot ? <span class="bav-id mono">{b.bot}</span> : null}</b>
        <span class="dl-why">{b.description || 'A bot.'}</span>
        <span class="fx">{facts.map((f) => <span class="fx-i">{f}</span>)}</span>
      </a>
      <span class="m-st">
        {needs ? (
          <a class="q-st need" href={`#/bots/${b.bot}/approvals`}><StatusMark s="ember" mkOnly label="Needs you" />{plural(b.pending_proposals, 'need', 'need')} you</a>
        ) : b.live_workers > 0 ? (
          <span class="q-st"><StatusMark s="run" mkOnly label="Working" />Working</span>
        ) : null}
      </span>
    </li>
  )
}
