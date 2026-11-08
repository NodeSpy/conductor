// Browsers (design §4.5): the live agent sessions, and the shared tab bar across
// Sessions · Logins · Personal details · Payment cards (js/screens/browser.js
// UI.browserTabs). This component is the entry router for every #/browser/*
// route, so app.tsx keeps one case.
//
// The live view's back/forward/reload controls are left out: the daemon's
// input only forwards mouse and keyboard events, not navigation commands.
// "Register a passkey" (conductor browser passkey register) drives a live
// WebAuthn ceremony in a real browser window on the conductor machine, so the
// button names the command rather than faking a flow over HTTP.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg } from '../router'
import { browser, type BrowserSession, type LoginRecord } from '../api/browser'
import { StatusMark } from '../components/StatusMark'
import { FactsLine } from '../components/Facts'
import { FormModal } from '../components/FormModal'
import { config, staged, cfgError, loadConfig, stage, stageDelete } from '../configStore'
import { agoText, host, whoWord, loadRoster, roster } from '../lib/browserwords'
import { BrowserSessionView } from './BrowserSession'
import { Logins, AddLogin } from './Logins'
import { Details } from './Details'
import { Cards } from './Cards'
import { Purchase } from './Purchase'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { TabsBar, type TabItem } from '../components/TabsBar'
import { BrModal } from '../components/BrowserFields'
import { humanError } from '../api/errors'

export type BrowserTab = 'sessions' | 'logins' | 'details' | 'cards'

// counts is a tiny, best-effort fetch of each tab's size (design: the tab bar
// shows a count beside each, UI.browserTabs). A failure on one just leaves
// that tab's count off; it never blocks the page it's drawn on.
function useBrowserCounts(): Partial<Record<BrowserTab, number>> {
  const [counts, setCounts] = useState<Partial<Record<BrowserTab, number>>>({})
  useEffect(() => {
    browser.sessions().then((r) => setCounts((c) => ({ ...c, sessions: (r.sessions || []).length }))).catch(() => {})
    browser.logins().then((r) => setCounts((c) => ({ ...c, logins: (r.logins || []).length }))).catch(() => {})
    browser.wallet().then((r) => setCounts((c) => ({ ...c, details: (r.profiles || []).length, cards: (r.cards || []).length }))).catch(() => {})
  }, [])
  return counts
}

export function BrowserTabs({ active }: { active: BrowserTab }): JSX.Element {
  const counts = useBrowserCounts()
  // a count only when there is something to count: no "0" beside a tab
  const n = (v?: number) => (v ? v : undefined)
  const tabs: TabItem[] = [
    { id: 'sessions', label: 'Sessions', href: '#/browser', count: n(counts.sessions) },
    { id: 'logins', label: 'Logins', href: '#/browser/logins', count: n(counts.logins) },
    { id: 'details', label: 'Personal details', href: '#/browser/details', count: n(counts.details) },
    { id: 'cards', label: 'Payment cards', href: '#/browser/cards', count: n(counts.cards) },
  ]
  return <TabsBar items={tabs} active={active} ariaLabel="Browsers" />
}

// BrowserCrumbs: "Browsers › <tab>" above every tab page but the root
// (Sessions, which is Browsers' own landing page and carries no crumb, same
// as the prototype's browser.js UI.head call).
export function BrowserCrumbs({ here }: { here: string }): JSX.Element {
  return (
    <nav class="dl-crumbs" aria-label="Where you are">
      <a href="#/browser">Browsers</a>
      <span class="sep" aria-hidden="true">›</span>
      <span class="cur" aria-current="page">{here}</span>
    </nav>
  )
}

// RegisterPasskey: a real entry point — honest about what it can do. There is
// no dashboard endpoint for `conductor browser passkey register` (it drives a
// live WebAuthn ceremony in a real browser window); this names the command
// instead of faking a flow that can't work over HTTP.
export function RegisterPasskey(): JSX.Element {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button class="btn" type="button" onClick={() => setOpen(true)}>
        <Icon name="plus" />Register a passkey
      </button>
      {open ? (
        <BrModal caps="passkey" title="Register a passkey" onClose={() => setOpen(false)} testid="passkey-modal" foot={<button class="btn primary" type="button" onClick={() => setOpen(false)}>Done</button>}>
          <p class="small">A dedicated passkey for one login, registered live on its own site: conductor opens the site's security page in a real browser window and the site itself creates the passkey. That window runs on the machine conductor is on, so start it there:</p>
          <p><code>conductor browser passkey register CREDENTIAL</code></p>
          <p class="dl-sub">It's revocable on the site without touching any passkey of your own, and its private key goes straight to the vault.</p>
        </BrModal>
      ) : null}
    </>
  )
}

// Browsers routes #/browser/* to its screen.
export function Browsers(): JSX.Element {
  const s1 = seg(1)
  switch (s1) {
    case '':
      return <Sessions />
    case 'logins':
      return <Logins />
    case 'add':
      return <AddLogin />
    case 'details':
      return <Details />
    case 'cards':
      return <Cards />
    case 'purchases':
      return seg(2) ? <Purchase id={seg(2)} /> : <Cards />
    default:
      // a session id
      return <BrowserSessionView id={s1} />
  }
}

// A session's state in words: an operator driving it, else running.
function sessState(s: BrowserSession): [string, string] {
  return s.leased_by ? ['ember', 'You have control'] : ['run', 'Running']
}

// usePeek reads a session's current page once: a masked still (credential
// fields hidden) and its address. Each peek is audited as one.
function usePeek(id: string): { src: string; url: string } {
  const [p, setP] = useState({ src: '', url: '' })
  useEffect(() => {
    let alive = true
    let made = ''
    fetch(browser.peekURL(id), { credentials: 'same-origin' })
      .then(async (r) => {
        if (!r.ok) return
        const url = r.headers.get('X-Browser-URL') || ''
        made = URL.createObjectURL(await r.blob())
        if (alive) setP({ src: made, url })
      })
      .catch(() => {})
    return () => {
      alive = false
      if (made) URL.revokeObjectURL(made)
    }
  }, [id])
  return p
}

function SessionTile({ s }: { s: BrowserSession }): JSX.Element {
  const peek = usePeek(s.id)
  const [mark, word] = sessState(s)
  const signedIn = /^the logged-in browser/.test(s.profile)
  return (
    <a class="panel btile" href={`#/browser/${encodeURIComponent(s.id)}`} data-session={s.id}>
      <div class="thumb">
        {peek.src ? <img src={peek.src} alt="" style="display:block;width:100%;height:100%;object-fit:cover;object-position:top left" /> : null}
        {s.leased_by ? <span class="ctrl-banner"><Icon name="hand" />You have control</span> : null}
      </div>
      <div class="panel-body stack tight">
        <div class="row"><StatusMark s={mark} word={word} /><span class="dl-sub" style="margin-left:auto">used {agoText(s.last_used)}</span></div>
        <div class="ellipsis" style="font-weight:600">{peek.url ? host(peek.url.split('?')[0]).split('/')[0] : `Browser ${s.id}`}</div>
        <div class="dl-sub ellipsis">{peek.url ? peek.url.replace(/^https?:\/\//, '') : 'no page open yet'}</div>
        <FactsLine facts={[
          { icon: 'obj-bot', value: whoWord(s.identity), pri: 1, label: 'Signs in as' },
          { icon: signedIn ? 'cookie' : 'eyeoff', value: signedIn ? 'Signed in' : 'Not signed in', pri: 2, label: 'Session' },
        ]} />
      </div>
    </a>
  )
}

function Sessions(): JSX.Element {
  const [sessions, setSessions] = useState<BrowserSession[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    browser.sessions().then((r) => setSessions(r.sessions || [])).catch((e) => setErr(humanError(e)))
  }, [])
  useEffect(loadRoster, [])
  void roster.value

  return (
    <div class="br-page">
      <div class="page-head">
        <div>
          <h1>Browsers</h1>
          <p class="lede-2">conductor's Chromium: one per agent session, sandboxed.</p>
        </div>
        <div class="actions">
          {/* With no browser open the hero below carries the one Add a login. */}
          {sessions !== null && sessions.length === 0 ? null : <a class="btn" href="#/browser/add"><Icon name="key" />Add a login</a>}
          <RegisterPasskey />
        </div>
      </div>
      <BrowserTabs active="sessions" />

      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      {sessions === null ? (
        <p class="muted">{err ? 'Nothing to show until conductor answers.' : 'Loading live sessions…'}</p>
      ) : sessions.length === 0 ? (
        <EmptyState hero icon="globe" title="No browser open right now" testid="sessions-empty" action={{ label: 'Add a login', href: '#/browser/add', testid: 'sessions-empty-add' }}>
          <p>When an agent opens conductor's browser, its live session shows here: watch it, or take over. Add a login so a bot can sign in to a site you choose.</p>
        </EmptyState>
      ) : (
        <div class="grid g3">
          {sessions.map((s) => <SessionTile s={s} key={s.id} />)}
        </div>
      )}
      <IdentitySettings />
    </div>
  )
}

// --- Settings per identity: which sites each may reach, its logins and
// credentials (browser.* in config). The allowlist is one list for every
// identity (browser.allow); logged-in sites are browser.logins entries;
// evaluate is browser.evaluate. Edits are staged, reviewed and applied through
// the config pipeline; anything that widens asks for your passkey there.

interface BrowserCfg {
  allow?: string[]
  logins?: { site: string; identities?: string[]; also?: string[] }[]
  evaluate?: string[] | string
  credentials?: { name: string; identities?: string[] }[]
}

function IdentitySettings(): JSX.Element {
  const [logins, setLogins] = useState<LoginRecord[]>([])
  const [edit, setEdit] = useState<string | null>(null)
  const [note, setNote] = useState('')
  useEffect(() => {
    if (!config.value) void loadConfig()
    browser.logins().then((r) => setLogins(r.logins || [])).catch(() => {})
  }, [])
  const b = ((config.value?.tree as Record<string, unknown> | undefined)?.browser || {}) as BrowserCfg
  const evals = Array.isArray(b.evaluate) ? b.evaluate : []
  const cfgLogins = b.logins || []
  const ids = new Set<string>()
  for (const l of cfgLogins) for (const i of l.identities || []) ids.add(i)
  for (const e of evals) ids.add(e)
  for (const c of b.credentials || []) for (const i of c.identities || []) ids.add(i)
  for (const l of logins) for (const i of l.bindings || []) ids.add(i)
  for (const x of roster.value?.bots || []) ids.add('bot:' + x.bot)
  const list = [...ids].sort()
  const isStaged = (id: string) => staged.value.some((c) => (c.path || []).join('.').startsWith('browser.') && (c.summary || '').includes(id))

  return (
    <details class="panel dl-files-d ident-d">
      <summary class="panel-head"><h3>Settings per identity</h3><span class="muted small">which sites each may reach, its logins and credentials (browser.* in config)</span></summary>
      {note ? <div class="callout small" role="status" style="margin:0 var(--space-4) var(--space-3)">{note} <a class="dl-link" href="#/system/config/apply">Review and apply</a></div> : null}
      <table class="tbl responsive">
        <thead><tr><th>identity</th><th>allowlist</th><th>logged-in sites</th><th>credentials</th><th>evaluate</th><th></th></tr></thead>
        <tbody>
          {list.length === 0 ? (
            <tr><td colSpan={6} class="small muted">No identity uses the browser yet.</td></tr>
          ) : (
            list.map((id) => {
              const sites = cfgLogins.filter((l) => (l.identities || []).includes(id)).map((l) => l.site)
              const creds = logins.filter((l) => (l.bindings || []).includes(id)).map((l) => `${l.name} (${Object.keys(l.fields || {}).join(', ') || l.method})`).join(' · ') || '—'
              const ev = evals.includes(id) ? 'read-only' : 'off'
              return (
                <tr key={id} data-identity={id}>
                  <td data-l="identity" class="mono">{id} {isStaged(id) ? <span class="tag" data-s="run">staged</span> : null}</td>
                  <td data-l="allow" class="mono small">{(b.allow || []).join(', ') || '—'}</td>
                  <td data-l="logins" class="mono small">{sites.join(', ') || '—'}</td>
                  <td data-l="credentials" class="small">{creds}</td>
                  <td data-l="evaluate"><span class="tag" data-s={ev === 'off' ? 'ok' : 'wait'}>{ev}</span></td>
                  <td class="num"><button class="btn sm" type="button" data-testid="ident-edit" onClick={() => setEdit(id)}><Icon name="edit" />Edit</button></td>
                </tr>
              )
            })
          )}
        </tbody>
      </table>
      <div class="panel-foot small muted">
        <button class="btn sm" type="button" data-testid="ident-add" onClick={() => setEdit('')}><Icon name="plus" />Add identity</button>
        <span style="margin-left:8px">A <span class="mono">"*"</span> allowlist is refused. Edits go through the config pipeline; anything that widens (a site, a login, a credential, evaluate) also asks for your passkey.</span>
      </div>
      {edit !== null ? (
        <IdentityForm
          id={edit}
          b={b}
          onClose={() => setEdit(null)}
          onStaged={(m) => setNote(m)}
        />
      ) : null}
    </details>
  )
}

function IdentityForm({ id, b, onClose, onStaged }: { id: string; b: BrowserCfg; onClose: () => void; onStaged: (m: string) => void }): JSX.Element {
  const evals = Array.isArray(b.evaluate) ? b.evaluate : []
  const cfgLogins = b.logins || []
  const curSites = cfgLogins.filter((l) => (l.identities || []).includes(id)).map((l) => l.site)
  return (
    <FormModal
      caps={id ? `browser · ${id}` : 'browser'}
      title={id ? `Browser settings · ${id}` : 'Add a browser identity'}
      testid="ident-form"
      values={{ id, allow: (b.allow || []).join(', '), logins: curSites.join(', '), evaluate: evals.includes(id) ? 'read-only' : 'off' }}
      fields={[
        { k: 'id', label: 'Identity', req: true, readonly: !!id, ph: 'agent:docs@acme/api', pattern: /^(bot:|agent:|team:|dir:)[\w@./*-]+$/, patternMsg: 'bot:<name>, agent:<name>@<repo> or dir:<path>' },
        { k: 'allow', label: 'Allowlist (sites any browser may reach)', ph: 'docs.example.com, shop.example.com', help: 'One list for every identity. A "*" allowlist is refused. Adding a site widens.', validate: (v) => (v.split(/[,\s]+/).includes('*') ? 'A "*" allowlist is refused: list the sites.' : '') },
        { k: 'logins', label: 'Logged-in sites', ph: 'console.example.com', help: 'Must also be on the allowlist. Sign in to it after applying.' },
        { k: 'evaluate', type: 'seg', label: 'Evaluate', options: [['off', 'off'], ['read-only', 'read-only (widens)']], def: 'off', help: 'Page JavaScript the agent may run, read-only. Never once a credential is filled.' },
      ]}
      submit="Review change"
      submitIcon="check"
      onSubmit={async (v) => {
        const ident = id || v.id
        const split = (x?: string) => (x || '').split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)
        const allow = split(v.allow)
        const want = split(v.logins)
        const nextLogins = cfgLogins
          .map((l) => {
            const has = (l.identities || []).includes(ident)
            const keep = want.includes(l.site)
            const idsOut = has && !keep ? (l.identities || []).filter((x) => x !== ident) : !has && keep ? [...(l.identities || []), ident] : l.identities || []
            return { ...l, identities: idsOut }
          })
          .filter((l) => (l.identities || []).length)
        for (const site of want) if (!cfgLogins.some((l) => l.site === site)) nextLogins.push({ site, identities: [ident] })
        const nextEval = v.evaluate === 'read-only' ? [...new Set([...evals, ident])] : evals.filter((x) => x !== ident)
        const sum = `browser settings for ${ident}`
        if (allow.join(',') !== (b.allow || []).join(',')) await stage(['browser', 'allow'], allow, sum)
        if (JSON.stringify(nextLogins) !== JSON.stringify(cfgLogins)) {
          if (nextLogins.length) await stage(['browser', 'logins'], nextLogins, sum)
          else await stageDelete(['browser', 'logins'], sum)
        }
        if (nextEval.join(',') !== evals.join(',')) {
          if (nextEval.length) await stage(['browser', 'evaluate'], nextEval, sum)
          else await stageDelete(['browser', 'evaluate'], sum)
        }
        if (cfgError.value) return cfgError.value
        onStaged(`Staged: ${sum}, not applied yet.`)
      }}
      onClose={onClose}
    />
  )
}
