// New bot, guided (design botwizard.js): Purpose → Watches → Tells you where →
// What it may do → Review. A large sheet over the Bots page (#/bots/new): you
// can see where you came from; a ✕ where people look for it, Esc, or the scrim
// closes it (asking first only once something's been entered); a thin
// progress line instead of a big stepper; full screen on a phone.
//
// Every answer becomes real config (newbotModel.ts), staged through the one
// review → apply pipeline: bots.<name>, one trigger per watched event that
// hands it to the bot, and — for a community bot from a pack you don't have
// yet — that pack. What a connection delivers and can do comes from its
// plugin at runtime (GET /api/v1/newbot/connections); community bots are the
// bot templates packs ship (GET /api/v1/newbot/templates).
import type { JSX } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { botsApi, type BotSummary, type TeamSummary } from '../api/bots'
import { api } from '../api/client'
import { humanError } from '../api/errors'
import { AvatarPreview } from '../components/Avatar'
import { ConfirmModal } from '../components/FormModal'
import { Icon } from '../components/Icon'
import { IdentityPicker, toAvatarSpec, type IdentityChoice } from '../components/IdentityPicker'
import { StatusMark } from '../components/StatusMark'
import { Yaml } from '../components/Yaml'
import '../styles/bots-wizard.css'
import { cfgError, config, doReview, loadConfig, stage, staged } from '../configStore'
import { COLORS, EYES, SHAPES } from '../lib/avatar'
import { go, query } from '../router'
import {
  CHANNEL, PRESETS, PURPOSES, STEPS, botCfg, chatConns, chosenRows, eventRows, fresh, grants, onOf, packInstall, previewYAML, triggers,
  type Answers, type Ctx, type NbConnection, type NbTemplate,
} from './newbotModel'

function getJSON<T>(path: string): Promise<T> {
  return fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' }).then(async (r) => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    return (await r.json()) as T
  })
}

// A new bot starts with a face no other bot has; you can change it on the first step.
function freeMascot(bots: BotSummary[]): IdentityChoice {
  const used = new Set(bots.map((b) => (b.identity?.avatar?.shape ? `${b.identity.avatar.shape}/${b.identity.avatar.eyes}` : '')))
  for (const sh of SHAPES) for (const ey of EYES) if (!used.has(`${sh}/${ey}`)) return { mode: 'face', shape: sh, eyes: ey, icon: 'radar', color: COLORS[used.size % COLORS.length] }
  return { mode: 'face', shape: 'circle', eyes: 'round', icon: 'radar', color: 'teal' }
}

// A button that can't act yet: the prototype's UI.off (aria-disabled with the reason as its tip).
const off = (reason: string) => ({ 'aria-disabled': true as const, 'data-reason': reason, 'data-tip': reason })

function Choice({ on, icon, title, desc, offWhy, onPick, testid }: { on: boolean; icon: string; title: string; desc: string; offWhy?: string; onPick: () => void; testid?: string }): JSX.Element {
  return (
    <button type="button" class={'choice' + (on ? ' on' : '')} aria-pressed={on} data-testid={testid} {...(offWhy ? off(offWhy) : {})} onClick={() => !offWhy && onPick()}>
      <span class="choice-ic"><Icon name={icon} /></span>
      <span class="choice-body">
        <span class="choice-title">{title}</span>
        <span class="choice-desc">{desc}</span>
        {offWhy ? <span class="choice-note">{offWhy}</span> : null}
      </span>
      <span class="choice-check" aria-hidden="true"><Icon name="check" /></span>
    </button>
  )
}

function Switch({ on, label, disabled, onToggle }: { on: boolean; label: string; disabled?: string; onToggle: () => void }): JSX.Element {
  return <button type="button" role="switch" class={'switch' + (on ? ' on' : '')} aria-checked={on} aria-label={label} {...(disabled ? off(disabled) : {})} onClick={() => !disabled && onToggle()} />
}

function MascotPicker({ value, title, onDone, onClose }: { value: IdentityChoice; title: string; onDone: (c: IdentityChoice) => void; onClose: () => void }): JSX.Element {
  const [v, setV] = useState(value)
  return (
    <div class="scrim" id="scrim" onClick={onClose}>
      <div class="modal" role="dialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div><h2>{title}</h2></div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div class="modal-body"><IdentityPicker value={v} onChange={setV} /></div>
        <div class="modal-foot">
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" data-testid="mascot-done" onClick={() => { onDone(v); onClose() }}>Use this mascot</button>
        </div>
      </div>
    </div>
  )
}

// The prototype's verb-pattern words: a pattern reads as "read <connection>".
function verbWords(v: string, conns: NbConnection[], tellVerb: string | null, channel: string): string {
  if (v === tellVerb) return `Tell you in ${channel}`
  const [inst, rest = ''] = v.split('.')
  if (rest.endsWith('*')) return `Read ${inst}`
  const d = conns.find((c) => c.name === inst)?.verbs.find((x) => x.name === rest)?.desc
  return d ? d.replace(/^./, (c) => c.toUpperCase()) : v
}

export function NewBotWizard(): JSX.Element {
  const [a, setA] = useState<Answers>(fresh)
  const set = (patch: Partial<Answers>) => setA((p) => ({ ...p, ...patch }))
  const [bots, setBots] = useState<BotSummary[] | null>(null)
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [conns, setConns] = useState<NbConnection[] | null>(null)
  const [templates, setTemplates] = useState<NbTemplate[] | null>(null)
  const [runtimes, setRuntimes] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [busyCreate, setBusyCreate] = useState(false)
  const [confirm, setConfirm] = useState<null | 'close' | 'noevents'>(null)
  const [picking, setPicking] = useState(false)
  const sheet = useRef<HTMLElement>(null)

  useEffect(() => {
    void loadConfig()
    botsApi.bots().then((r) => {
      setBots(r.bots || [])
      setTeams(r.teams || [])
    }).catch((e) => { setBots([]); setErr(humanError(e)) })
    getJSON<{ connections: NbConnection[] }>('/api/v1/newbot/connections').then((r) => setConns(r.connections || [])).catch(() => setConns([]))
    getJSON<{ templates: NbTemplate[] }>('/api/v1/newbot/templates').then((r) => setTemplates(r.templates || [])).catch(() => setTemplates([]))
    api.connections().then((c) => {
      const cli = (c.runtimes ?? []).filter((r) => r.type === 'cli')
      setRuntimes(cli.map((r) => r.name))
      const def = cli.find((r) => r.default) || cli[0]
      if (def) setA((p) => (p.runtime ? p : { ...p, runtime: def.name }))
    }).catch(() => setRuntimes([]))
  }, [])

  // New bot from a team's page starts on that team.
  const qTeam = query('team')
  const teamTouched = useRef(false)
  useEffect(() => {
    if (qTeam && teams.some((t) => t.team === qTeam)) setA((p) => (p.at === 0 && !p.team && !teamTouched.current ? { ...p, team: qTeam } : p))
  }, [qTeam, teams])

  useEffect(() => {
    if (sheet.current && !sheet.current.contains(document.activeElement)) sheet.current.focus({ preventScroll: true })
  }, [a.at])

  const tree = (config.value?.tree || {}) as Record<string, any>
  const teamCfg = (a.team && tree.teams?.[a.team]) || {}
  const ceiling: string = teamCfg.autonomy?.ceiling || ''
  const teamShadow = teamCfg.autonomy?.shadow === true
  const rows = useMemo(() => eventRows(conns || []), [conns])
  const ctx: Ctx = { conns: conns || [], rows, ceiling }
  const chats = chatConns(conns || [])
  const all = bots || []
  const existingTriggers: Record<string, unknown>[] = (() => {
    const st = [...staged.value].reverse().find((c) => c.path.length === 1 && c.path[0] === 'triggers' && !c.delete)
    return ((st ? st.value : tree.triggers) as Record<string, unknown>[] | undefined) || []
  })()
  const takenTriggers = existingTriggers.map((t) => String((t as { name?: string })?.name || ''))
  const operators = (() => {
    const t = teams.find((x) => x.team === a.team)
    return t && t.operators?.length ? t.operators : Object.keys(tree.operators || {})
  })()

  const nameErr = !/^[a-z][a-z0-9-]*$/.test(a.name) ? 'lowercase letters, digits and dashes' : a.name.length > 40 ? 'at most 40 characters' : all.some((b) => b.bot === a.name) ? 'a bot by that name exists' : ''
  const canNext =
    a.at === 0 ? (a.purpose && !nameErr ? '' : !a.purpose ? 'Pick what it should do first' : 'Fix the name first')
    : a.at === 1 && rows.some((r) => a.events.has(r.key) && !onOf(a, r)) ? 'Name the event you ticked'
    : a.at === 2 && a.tell === 'chat' && !a.chat ? 'Pick a chat connection'
    : a.at === 2 && a.tell === 'chat' && !CHANNEL.test(a.channel || '') ? 'A channel is # and up to 80 lowercase letters, digits, - or _'
    : ''
  const dirty = a.at > 0 || !!a.purpose || !!a.name || !!a.description || (!!a.team && a.team !== qTeam)
  const pct = Math.round(((a.at + 1) / STEPS.length) * 100)

  const leave = () => go('bots')
  const close = () => (dirty ? setConfirm('close') : leave())
  const advance = () => setA((p) => ({ ...p, at: Math.min(STEPS.length - 1, p.at + 1) }))
  const next = () => {
    if (canNext) return
    if (a.at === 1 && !chosenRows(a, rows).length && !a.sweep) return setConfirm('noevents')
    advance()
  }

  // Esc closes (asking first once something's been entered) — unless a dialog of its own is open.
  const closeRef = useRef(close)
  closeRef.current = close
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || [...document.querySelectorAll<HTMLElement>('.scrim, .modal, .menu-pop, .fb-menu, .palette')].some((el) => !el.closest('.nb-under') && el.offsetParent !== null)) return
      e.preventDefault()
      closeRef.current()
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [])

  const pickPurpose = (k: string) =>
    setA((p) => {
      const pp = PURPOSES[k]
      const prev = p.purpose ? PURPOSES[p.purpose] : null
      const fromTpl = p.template
      return {
        ...p,
        template: null,
        purpose: k,
        name: !p.name || (prev && p.name === prev.name) || fromTpl ? pp.name : p.name,
        description: !p.description || (prev && p.description === prev.description) || fromTpl ? pp.description : p.description,
        avatar: p.avatar || freeMascot(all),
      }
    })
  const pickTemplate = (t: NbTemplate) =>
    setA((p) => ({
      ...p,
      template: t,
      purpose: 'custom',
      name: !p.name || Object.values(PURPOSES).some((x) => x.name === p.name) || p.template ? (all.some((b) => b.bot === t.name) ? `${t.name}-2` : t.name) : p.name,
      description: (t.description || '').split('.')[0],
      avatar: p.avatar || freeMascot(all),
    }))

  async function create() {
    if (a.at !== STEPS.length - 1 || nameErr || busyCreate) return
    setBusyCreate(true)
    setErr('')
    const name = a.name
    const firstBot = all.length === 0
    try {
      const pk = packInstall(a)
      if (pk) {
        await stage(['packs', pk.name], pk.value, `install ${pk.name} for its bot template ${a.template!.name}`)
        if (cfgError.value) throw new Error(cfgError.value)
      }
      await stage(['bots', name], botCfg(a, ctx), `new bot ${name}${a.template ? ` from ${a.template.pack}${a.template.version ? ' ' + a.template.version : ''}` : ''}`)
      if (cfgError.value) throw new Error(cfgError.value)
      const ts = triggers(a, ctx, takenTriggers)
      if (ts.length) {
        await stage(['triggers'], [...existingTriggers, ...ts], `${ts.length} trigger${ts.length > 1 ? 's' : ''} that hand events to ${name}`)
        if (cfgError.value) throw new Error(cfgError.value)
      }
      // The first bot is reviewed and applied on the Bots welcome (its own one
      // button); any other goes to the review, already checked.
      if (firstBot) go('bots')
      else {
        await doReview()
        go('system/config')
      }
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusyCreate(false)
    }
  }

  const fromPack = query('from')
  const tpls = (templates || []).filter((t) => !fromPack || t.pack === fromPack)
  const tplSection = (
    <>
      <h3 class="bw-sub">{fromPack ? `From ${fromPack}` : 'Or start from a community bot'}</h3>
      {templates === null ? (
        <p class="muted small">Looking for community bots…</p>
      ) : tpls.length ? (
        <div class="choice-grid">
          {tpls.map((t) => (
            <Choice
              key={`${t.pack}/${t.name}`}
              on={!!(a.template && a.template.pack === t.pack && a.template.name === t.name)}
              icon="bots"
              title={t.name}
              desc={`${t.description ? t.description.replace(/\.?$/, '.') + ' ' : ''}From ${t.pack}${t.version ? ' ' + t.version : ''} (${t.official ? 'official' : 'community'}).`}
              onPick={() => pickTemplate(t)}
              testid="nb-template"
            />
          ))}
        </div>
      ) : (
        <p class="muted small" data-testid="nb-no-templates">No community bots to start from yet. When a pack ships one, it shows here.</p>
      )}
    </>
  )

  function step0(): JSX.Element {
    const av = a.avatar || freeMascot(all)
    return (
      <>
        {fromPack ? tplSection : null}
        <div class="choice-grid">
          {Object.entries(PURPOSES).map(([k, p]) => (
            <Choice key={k} on={a.purpose === k && !a.template} icon={p.icon} title={p.title} desc={p.desc} onPick={() => pickPurpose(k)} testid={`nb-purpose-${k}`} />
          ))}
        </div>
        {fromPack ? null : tplSection}
        {a.purpose ? (
          <div class="stack" style={{ gap: '12px', marginTop: '6px' }}>
            <div class={'field' + (nameErr ? ' bad' : '')} data-field="name">
              <label for="bw-name">Name <span class="req">*</span></label>
              <input class="input mono" id="bw-name" value={a.name} autocomplete="off" onInput={(e) => set({ name: (e.target as HTMLInputElement).value })} />
              <div class="hint">How you'll refer to it, in chat and in config.</div>
              <div class="ferr" role="alert">{nameErr}</div>
            </div>
            <div class="field">
              <label>Its mascot</label>
              <div class="mk-field">
                <AvatarPreview spec={toAvatarSpec(av)} size={48} />
                <span class="dl-sub">{av.mode === 'icon' ? `${av.icon} icon` : `${av.shape} · ${av.eyes} eyes`} · {av.color}</span>
                <button type="button" class="btn sm secondary" data-testid="nb-mascot" onClick={() => setPicking(true)}>Change…</button>
              </div>
            </div>
            <div class="field">
              <label for="bw-desc">What it's for, in a line</label>
              <input class="input" id="bw-desc" value={a.description} placeholder="Triages incidents for the API" onInput={(e) => set({ description: (e.target as HTMLInputElement).value })} />
            </div>
            <div class="row wrap" style={{ gap: '12px' }}>
              <div class="field grow">
                <label for="bw-team">Team</label>
                <select class="select" id="bw-team" value={a.team} onChange={(e) => { teamTouched.current = true; set({ team: (e.target as HTMLSelectElement).value, operators: [] }) }}>
                  <option value="">none (a solo bot)</option>
                  {teams.map((t) => <option value={t.team} selected={a.team === t.team}>{t.team}</option>)}
                </select>
                <div class="hint">{teams.length ? 'A team shares approvers, limits and knowledge; its bots can only be stricter.' : 'No teams yet: it starts as a solo bot, and you can put it on a team later.'}</div>
              </div>
            </div>
          </div>
        ) : null}
      </>
    )
  }

  function step1(): JSX.Element {
    const chosen = chosenRows(a, rows)
    const repoOK = chosen.some((r) => (r.ev.filters || []).includes('repo'))
    return (
      <>
        <p class="muted small" style={{ margin: 0 }}>Pick the events it should hear about. Each becomes a trigger that hands the event to <code>{a.name}</code>.</p>
        <div class="panel">
          <div class="list">
            {conns === null ? (
              <div class="empty small">Loading what your connections deliver…</div>
            ) : rows.length ? (
              rows.map((r) => {
                const checked = a.events.has(r.key)
                const toggle = (on: boolean) => setA((p) => { const ev = new Set(p.events); if (on) ev.add(r.key); else ev.delete(r.key); return { ...p, events: ev } })
                return (
                  <label class="item click bw-ev" key={r.key} data-testid="nb-event">
                    <input type="checkbox" class="cbx" checked={checked} onChange={(e) => toggle((e.target as HTMLInputElement).checked)} />
                    <span class="grow"><b>{r.label}</b> <span class="muted small">from {r.conn.name}</span></span>
                    {r.ev.dynamic ? (
                      <>
                        <input
                          class="input mono sm"
                          style={{ width: '160px', minHeight: '28px' }}
                          aria-label={`Which ${r.conn.name} event`}
                          placeholder="which one"
                          list={`nb-seen-${r.key}`}
                          value={a.dyn[r.key] || ''}
                          onInput={(e) => { const v = (e.target as HTMLInputElement).value; setA((p) => { const ev = new Set(p.events); if (v.trim()) ev.add(r.key); return { ...p, dyn: { ...p.dyn, [r.key]: v }, events: ev } }) }}
                        />
                        <datalist id={`nb-seen-${r.key}`}>{(r.ev.seen || []).map((s) => <option value={s} />)}</datalist>
                      </>
                    ) : (
                      <span class="mono tiny muted">{onOf(a, r)}</span>
                    )}
                  </label>
                )
              })
            ) : (
              <div class="empty small" data-testid="nb-no-events">No connected service delivers events yet. <a class="link" href="#/connections">Connect one</a>.</div>
            )}
          </div>
        </div>
        {repoOK ? (
          <div class="field">
            <label for="bw-repos">Which repos</label>
            <input class="input mono" id="bw-repos" value={a.repos} placeholder="acme/api, acme/web" onInput={(e) => set({ repos: (e.target as HTMLInputElement).value })} />
            <div class="hint">Events from these repos only; leave it empty for every repo.</div>
          </div>
        ) : null}
        <label class="row" style={{ gap: '10px' }}>
          <Switch on={a.sweep} label="Also check in on its own" onToggle={() => set({ sweep: !a.sweep })} />
          <span>
            <b>Also check in on its own</b> <span class="muted small">every</span>{' '}
            <select class="select sm" style={{ width: 'auto', display: 'inline-block', minHeight: '28px', padding: '2px 26px 2px 8px' }} aria-label="How often" value={a.every} onChange={(e) => set({ every: (e.target as HTMLSelectElement).value })}>
              {['1h', '4h', '12h', '24h'].map((x) => <option selected={a.every === x}>{x}</option>)}
            </select>{' '}
            <span class="muted small">(quiet 22:00–07:00, at most 6 a day)</span>
          </span>
        </label>
        {!chosen.length && !a.sweep ? (
          <div class="callout wait small"><Icon name="warn" /> <span>It won't do anything until it watches something. You can create it anyway and add events later from its page.</span></div>
        ) : null}
      </>
    )
  }

  function step2(): JSX.Element {
    const first = chats[0]
    return (
      <>
        <div class="choice-grid two">
          <Choice on={a.tell === 'dashboard'} icon="grid" title="Here, in the dashboard" desc="Its chat, and Needs you for anything you have to decide." onPick={() => set({ tell: 'dashboard' })} testid="nb-tell-dashboard" />
          <Choice
            on={a.tell === 'chat'}
            icon="send"
            title={first ? `In a ${first.plugin.replace(/^./, (c) => c.toUpperCase())} channel` : 'In a chat channel'}
            desc="Posts updates to a channel you pick (decisions still come to Needs you)."
            offWhy={chats.length ? '' : 'No chat connection yet; add one under Plugins.'}
            onPick={() => set({ tell: 'chat', chat: a.chat || first?.name || '' })}
            testid="nb-tell-chat"
          />
        </div>
        {a.tell === 'chat' ? (
          <div class="row wrap" style={{ gap: '12px' }}>
            <div class="field">
              <label for="bw-slack">Connection</label>
              <select class="select mono" id="bw-slack" value={a.chat} onChange={(e) => set({ chat: (e.target as HTMLSelectElement).value })}>
                {chats.map((c) => <option selected={a.chat === c.name}>{c.name}</option>)}
              </select>
            </div>
            <div class="field grow">
              <label for="bw-channel">Channel</label>
              <input class="input mono" id="bw-channel" value={a.channel} onInput={(e) => set({ channel: (e.target as HTMLInputElement).value })} />
            </div>
          </div>
        ) : null}
      </>
    )
  }

  function step3(): JSX.Element {
    const g = grants(a, ctx)
    const mode = (v: string) => {
      let t = 'propose'
      for (const k of ['never', 'propose', 'reversible', 'observe']) if ((g.tiers[k] || []).includes(v)) { t = k; break }
      return t === 'never' ? 'never' : t === 'observe' ? 'own' : t === 'reversible' && g.promote.includes(v) && ceiling !== 'observe' ? 'own' : 'asks'
    }
    const grp = (label: string, s: string, items: string[], empty: string) => (
      <div class="cang" data-s={s}>
        <h3><StatusMark s={s} mkOnly label={label} />{label}</h3>
        <ul>{items.length ? items.map((v) => <li key={v} title={v}>{verbWords(v, ctx.conns, g.tellVerb, a.channel)}</li>) : <li class="dl-sub">{empty}</li>}</ul>
      </div>
    )
    const shadowLock = teamShadow ? `Team ${a.team} keeps its bots in practice mode` : ''
    return (
      <>
        <div class="choice-grid three">
          {Object.entries(PRESETS).map(([k, p]) => (
            <Choice key={k} on={a.preset === k} icon={k === 'read' ? 'eye' : k === 'post' ? 'send' : 'lock'} title={p.title} desc={p.desc} onPick={() => set({ preset: k as Answers['preset'] })} testid={`nb-preset-${k}`} />
          ))}
        </div>
        {!g.promoteOK ? <div class="callout small"><Icon name="lock" /> On team {a.team} a bot does on its own only things that read, so comments and posts will ask you first.</div> : null}
        <div class="panel">
          <div class="panel-head"><h3>What {a.name} can do</h3><span class="muted small">from the services it watches{a.tell === 'chat' ? ' and the channel it tells you in' : ''}</span></div>
          <div class="panel-body">
            {g.verbs.length ? (
              <div class="dl-can compact">
                {a.shadow || teamShadow ? <p class="dl-sub">In practice mode: even what it does on its own is only recorded, never done.</p> : null}
                <div class="dl-can-grid">
                  {grp('Does on its own', 'ok', g.verbs.filter((v) => mode(v) === 'own'), 'Nothing: it asks first')}
                  {grp('Asks you first', 'ember', g.verbs.filter((v) => mode(v) === 'asks'), 'Nothing needs your approval')}
                  {grp('Never', 'fail', g.verbs.filter((v) => mode(v) === 'never'), 'Nothing is refused outright')}
                </div>
              </div>
            ) : (
              <div class="muted small">Nothing yet: it watches no service it could act on. It can still chat with you.</div>
            )}
          </div>
        </div>
        <label class="row" style={{ gap: '10px' }}>
          <Switch on={a.shadow || teamShadow} label="Start in practice mode" disabled={shadowLock} onToggle={() => set({ shadow: !a.shadow })} />
          <span><b>Start in practice mode</b> <span class="muted small">{shadowLock ? `${shadowLock}: it stays in practice mode until the team leaves it` : 'it says what it would do, and nothing it does is real until you turn practice mode off on its page'}</span></span>
        </label>
        <details class="panel adv">
          <summary class="panel-head"><h3>Advanced</h3><span class="muted small">where its workers run, who approves, how long an approval lasts</span></summary>
          <div class="panel-body stack" style={{ gap: '12px' }}>
            <div class="row wrap" style={{ gap: '12px' }}>
              <div class="field">
                <label for="bw-rt">Worker runtime</label>
                {runtimes.length ? (
                  <select class="select mono" id="bw-rt" value={a.runtime} onChange={(e) => set({ runtime: (e.target as HTMLSelectElement).value })}>
                    {runtimes.map((r) => <option selected={a.runtime === r}>{r}</option>)}
                  </select>
                ) : (
                  <div class="hint">No agent runtime yet: add one under Plugins before it can work.</div>
                )}
              </div>
              <div class="field">
                <label for="bw-ttl">An approval lasts</label>
                <select class="select mono" id="bw-ttl" value={a.ttl} onChange={(e) => set({ ttl: (e.target as HTMLSelectElement).value })}>
                  {['1h', '8h', '24h', '72h'].map((x) => <option selected={a.ttl === x}>{x}</option>)}
                </select>
              </div>
            </div>
            <div class="field">
              <label>Who may approve <span class="opt">none ticked = {a.team ? `team ${a.team}'s approvers` : 'every operator'}</span></label>
              <div class="row wrap" style={{ gap: '12px' }}>
                {operators.length ? operators.map((o) => (
                  <label class="row small" key={o}>
                    <input type="checkbox" class="cbx" checked={a.operators.includes(o)} onChange={(e) => set({ operators: (e.target as HTMLInputElement).checked ? a.operators.concat(o) : a.operators.filter((x) => x !== o) })} />
                    {o}
                  </label>
                )) : <span class="muted small">No operators are set up yet: anyone at this dashboard approves.</span>}
              </div>
            </div>
          </div>
        </details>
      </>
    )
  }

  function step4(): JSX.Element {
    const c = botCfg(a, ctx) as { autonomy: { shadow: boolean } }
    const evs = chosenRows(a, rows)
    const ts = triggers(a, ctx, takenTriggers)
    return (
      <>
        <div class="panel">
          <div class="list">
            <div class="item"><span class="caps" style={{ width: '120px' }}>bot</span><span class="grow"><b class="mono">{a.name}</b> {a.description ? <span class="muted">· {a.description}</span> : null}{a.team ? <> <span class="tag">team {a.team}</span></> : null}{c.autonomy.shadow || teamShadow ? <> <span class="tag" data-s="wait">shadow</span></> : null}{a.template ? <> <span class="tag">from {a.template.pack}</span></> : null}</span></div>
            <div class="item"><span class="caps" style={{ width: '120px' }}>watches</span><span class="grow">{evs.length ? evs.map((r) => (r.ev.dynamic ? `${r.label} (${a.dyn[r.key]})` : r.label)).join(', ') : !a.sweep ? <span class="muted">nothing yet</span> : null}{a.sweep ? `${evs.length ? '; ' : ''}checks in every ${a.every}` : ''}</span></div>
            <div class="item"><span class="caps" style={{ width: '120px' }}>tells you</span><span class="grow">{a.tell === 'chat' ? `in ${a.channel} via ${a.chat}` : 'in the dashboard'}</span></div>
            <div class="item"><span class="caps" style={{ width: '120px' }}>may</span><span class="grow">{PRESETS[a.preset].title}</span></div>
          </div>
        </div>
        <details class="panel dl-tech-d bw-yaml" data-testid="nb-yaml">
          <summary class="panel-head"><h3>The config this writes</h3><span class="muted small">bots.{a.name}{ts.length ? ` and ${ts.length} trigger${ts.length > 1 ? 's' : ''}` : ''}{packInstall(a) ? ` and the ${a.template!.pack} pack` : ''} · applied through the diff</span></summary>
          <div class="panel-body"><Yaml text={previewYAML(a, ctx, takenTriggers)} /></div>
        </details>
        {err ? <div class="banner err" role="alert">{err}</div> : null}
      </>
    )
  }

  const body = [step0, step1, step2, step3, step4][a.at]()
  return (
    <>
      <div class="nb-scrim" aria-hidden="true" onClick={close}></div>
      <section class="nb-sheet" role="dialog" aria-modal="true" aria-labelledby="nb-title" tabIndex={-1} ref={sheet} data-testid="new-bot-wizard">
        <header class="nb-head">
          <button type="button" class="icon-btn nb-x bw-close" aria-label="Close the new bot (Esc)" data-testid="nb-close" onClick={close}>
            <Icon name="x" />
            <span class="sr-only">Close</span>
          </button>
          <div>
            <h1 id="nb-title">New bot</h1>
            <span class="dl-sub" data-testid="nb-step">Step {a.at + 1} of {STEPS.length} · {STEPS[a.at]}</span>
          </div>
        </header>
        <div class="nb-prog" role="progressbar" aria-valuemin={1} aria-valuemax={STEPS.length} aria-valuenow={a.at + 1} aria-label={`Step ${a.at + 1} of ${STEPS.length}`}>
          <i style={{ width: `${pct}%` }}></i>
        </div>
        <div class="nb-body">
          <div class="stack bw-body" style={{ gap: '14px' }}>{body}</div>
        </div>
        <footer class="wizard-foot nb-foot">
          {a.at ? (
            <button class="btn" type="button" data-testid="nb-back" onClick={() => set({ at: Math.max(0, a.at - 1) })}>
              <Icon name="chev" class="flip" />Back
            </button>
          ) : null}
          <span class="grow dl-sub nb-esc">Esc closes; you’re asked first once you’ve entered something.</span>
          {a.at < STEPS.length - 1 ? (
            <button class="btn primary" type="button" data-testid="nb-next" {...(canNext ? off(canNext) : {})} onClick={next}>Next</button>
          ) : (
            <button class="btn primary" type="button" data-testid="nb-create" disabled={busyCreate} onClick={() => void create()}>
              <Icon name="check" />Review and apply
            </button>
          )}
        </footer>
      </section>
      {picking ? <MascotPicker value={a.avatar || freeMascot(all)} title={`${a.name || 'Its'} mascot`} onDone={(v) => set({ avatar: v })} onClose={() => setPicking(false)} /> : null}
      {confirm === 'close' ? (
        <ConfirmModal title="Discard this new bot?" body="What you’ve entered so far is thrown away. Nothing has been created yet." ok="Discard" cancel="Keep editing" danger onOk={leave} onClose={() => setConfirm(null)} />
      ) : null}
      {confirm === 'noevents' ? (
        <ConfirmModal
          title="Continue without anything to watch?"
          body={<><code>{a.name}</code> won't hear about any event and won't check in on its own, so it only answers when you chat with it. You can add what it watches later.</>}
          ok="Continue without events"
          cancel="Pick events"
          onOk={advance}
          onClose={() => setConfirm(null)}
        />
      ) : null}
    </>
  )
}
