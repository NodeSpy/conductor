// A browser's live view (js/screens/browser.js live()): a CDP screencast relayed
// over a WebSocket, who it acts for, and an operator takeover — a passkey lease
// that pauses the agent and drives the same sandboxed page with CDP input
// (keystrokes audited as a length only, like fills). Esc releases, and the agent
// resumes with a fresh snapshot. Screenshot saves a masked still; Close browser
// ends the session (the agent's next browser call opens a fresh one).
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { go } from '../router'
import { browser, takeover, releaseTakeover, sendInput, walletOp, type BrowserSession } from '../api/browser'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FactsLine } from '../components/Facts'
import { Menu } from '../components/Menu'
import { ConfirmModal } from '../components/FormModal'
import { whoWord, loadRoster, roster } from '../lib/browserwords'
import { humanError } from '../api/errors'

const NAV_OFF = 'The browser’s own back, forward and reload aren’t driven from here: take over and use the page'

export function BrowserSessionView({ id }: { id: string }): JSX.Element {
  const [sess, setSess] = useState<BrowserSession | null>(null)
  const [gone, setGone] = useState(false)
  const [err, setErr] = useState('')
  const [frame, setFrame] = useState('') // data: URL of the latest screencast frame
  const [still, setStill] = useState({ src: '', url: '' })
  const [live, setLive] = useState(false)
  const [leased, setLeased] = useState(false)
  const [busy, setBusy] = useState(false)
  const [closing, setClosing] = useState(false)
  // On a phone Close browser folds into the ⋯ menu (UI.head's phone actions).
  const [more, setMore] = useState<HTMLElement | null>(null)
  const [log, setLog] = useState<[string, string, string][]>([])
  const imgRef = useRef<HTMLImageElement>(null)
  const screenRef = useRef<HTMLDivElement>(null)
  const naturalRef = useRef<{ w: number; h: number }>({ w: 1280, h: 800 })

  useEffect(loadRoster, [])
  void roster.value

  function load() {
    browser
      .session(id)
      .then((r) => {
        setSess(r.session)
        setLeased(!!r.session.leased_by)
      })
      .catch((e) => {
        if (/no session/.test(String(e?.message || e))) setGone(true)
        else setErr(humanError(e))
      })
  }
  useEffect(load, [id])

  // A first still (and the page's address) while the screencast connects.
  useEffect(() => {
    let made = ''
    fetch(browser.peekURL(id), { credentials: 'same-origin' })
      .then(async (r) => {
        if (!r.ok) return
        const url = r.headers.get('X-Browser-URL') || ''
        made = URL.createObjectURL(await r.blob())
        setStill({ src: made, url })
      })
      .catch(() => {})
    return () => {
      if (made) URL.revokeObjectURL(made)
    }
  }, [id])

  // Open the screencast WebSocket; each text frame carries a base64 JPEG.
  useEffect(() => {
    let ws: WebSocket | null = null
    try {
      ws = new WebSocket(browser.screencastURL(id))
      ws.onmessage = (ev) => {
        try {
          const f = JSON.parse(ev.data as string) as { data: string; format?: string }
          setFrame(`data:image/${f.format || 'jpeg'};base64,${f.data}`)
          setLive(true)
        } catch {
          /* ignore a malformed frame */
        }
      }
      ws.onclose = () => setLive(false)
    } catch {
      /* no screencast: the still stays */
    }
    return () => ws?.close()
  }, [id])

  // Esc releases control.
  useEffect(() => {
    if (!leased) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.getElementById('scrim')) void doRelease()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [leased])

  const note = (a: string, d: string, s: string) => setLog((l) => [[a, d, s] as [string, string, string], ...l].slice(0, 12))

  async function doTakeover() {
    setBusy(true)
    setErr('')
    try {
      await takeover(id)
      setLeased(true)
      note('takeover', 'operator lease taken · agent actions wait', 'wait')
      setTimeout(() => screenRef.current?.focus(), 30)
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  async function doRelease() {
    setBusy(true)
    try {
      await releaseTakeover(id)
      setLeased(false)
      note('release', 'lease returned · agent resumes with a fresh snapshot', 'ok')
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  async function screenshot() {
    try {
      const r = await fetch(browser.peekURL(id), { credentials: 'same-origin' })
      if (!r.ok) throw new Error('the browser didn’t answer the screenshot')
      const href = URL.createObjectURL(await r.blob())
      const a = document.createElement('a')
      a.href = href
      a.download = `browser-${id}.png`
      a.click()
      setTimeout(() => URL.revokeObjectURL(href), 1000)
      note('peek (operator)', 'masked screenshot · credential fields hidden', 'ok')
    } catch (e) {
      setErr(humanError(e))
    }
  }

  function onScreenClick(e: MouseEvent) {
    if (!leased || !imgRef.current) return
    const r = imgRef.current.getBoundingClientRect()
    const nat = naturalRef.current
    const x = ((e.clientX - r.left) / r.width) * nat.w
    const y = ((e.clientY - r.top) / r.height) * nat.h
    void sendInput(id, { type: 'mouse', kind: 'mousePressed', x, y, button: 'left', clicks: 1 })
      .then(() => sendInput(id, { type: 'mouse', kind: 'mouseReleased', x, y, button: 'left', clicks: 1 }))
      .then(() => note('click (operator)', `at ${Math.round(x)},${Math.round(y)} · Input.dispatchMouseEvent`, 'ok'))
      .catch((er) => setErr(humanError(er)))
  }
  function onScreenKey(e: KeyboardEvent) {
    if (!leased || e.key === 'Escape') return
    e.preventDefault()
    if (e.key.length === 1) void sendInput(id, { type: 'key', kind: 'char', text: e.key })
    else void sendInput(id, { type: 'key', kind: 'keyDown', key: e.key })
  }

  if (gone)
    return (
      <div class="empty">
        <span class="serif">That browser has closed</span>
        <a class="btn" href="#/browser">Browsers</a>
      </div>
    )
  if (err && !sess) return <div class="callout danger small" role="alert">{err}</div>
  if (!sess) return <p class="muted">Loading the session…</p>

  const url = still.url
  const title = url ? url.replace(/^https?:\/\//, '').split(/[/?#]/)[0] : `Browser ${id}`
  const signedIn = /^the logged-in browser/.test(sess.profile)
  const [mark, word] = leased ? ['ember', 'You have control'] : ['run', 'Running']

  return (
    <div class="br-live" data-session={id}>
      <div class="page-head">
        <div>
          <nav class="dl-crumbs" aria-label="Where you are"><a href="#/browser">Browsers</a><span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">{title}</span></nav>
          <h1 class="gen dl-h1-long">{title}</h1>
        </div>
        <div class="actions">
          <StatusMark s={mark} word={word} lg />
          <button class="btn" type="button" data-testid="peek" onClick={() => void screenshot()}><Icon name="download" />Screenshot</button>
          {leased ? (
            <button class="btn" type="button" id="take" disabled={busy} data-testid="release" onClick={() => void doRelease()}><Icon name="undo" />Release</button>
          ) : (
            <button class="btn primary" type="button" id="take" disabled={busy} data-testid="takeover" onClick={() => void doTakeover()}><Icon name="hand" />{busy ? 'Confirming…' : 'Take over'}</button>
          )}
          <button class="btn secondary hide-phone" type="button" data-testid="close-browser" onClick={() => setClosing(true)}><Icon name="x" />Close browser</button>
          <button class="btn ph-more only-phone" type="button" aria-label="More actions" onClick={(e) => setMore(e.currentTarget as HTMLElement)}><Icon name="more" /></button>
        </div>
      </div>
      <FactsLine facts={[
        sess.agent ? { icon: 'obj-run', value: `Used by ${sess.agent.replace(/^sandbox:/, 'an agent in the sandbox: ')}`, pri: 1, label: 'Used by' } : null,
        { icon: 'obj-login', value: `Signs in as ${whoWord(sess.identity)}`, pri: 1, label: 'Signs in as' },
        { icon: 'users', value: `Asked for by ${sess.requester ? (sess.requester === 'operator' ? 'you' : whoWord(sess.requester)) : 'its own work'}`, pri: 2, label: 'Asked for by' },
      ]} />
      <details class="dl-audit-d b-audit">
        <summary><Icon name="chev" />For audit: the browser, run and identity as recorded</summary>
        <dl class="dl-facts">
          <dt>Browser</dt><dd class="mono">{sess.id}</dd>
          <dt>Run</dt><dd class="mono">{sess.agent || '—'}</dd>
          <dt>Identity</dt><dd class="mono">{sess.identity}</dd>
          <dt>Asked for by</dt><dd class="mono">{sess.requester || '—'}</dd>
        </dl>
      </details>
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}

      <div class="grid" style="grid-template-columns:minmax(0,1fr) 340px;align-items:start">
        <div class="panel">
          <div class="bchrome">
            {(['back', 'fwd', 'reload'] as const).map((k) => (
              <button class={'icon-btn' + (k === 'back' ? ' flip' : '')} type="button" aria-disabled="true" data-reason={leased ? NAV_OFF : 'The agent is driving: take over to use the browser yourself'} title={leased ? NAV_OFF : 'The agent is driving: take over to use the browser yourself'} aria-label={k} key={k}>
                <Icon name={k === 'reload' ? 'updates' : 'chev'} />
              </button>
            ))}
            <div class="url"><Icon name="lock" class="lockic" /><span class="ellipsis">{url || 'no page open yet'}</span></div>
            <span class="tag">1 tab</span>
            <span class="row tiny mono muted"><span class="rec" />{live ? 'CDP screencast · live' : 'CDP screencast · connecting'}</span>
          </div>
          <div
            ref={screenRef}
            class={'bscreen' + (leased ? ' takeover' : '')}
            id="screen"
            style={frame || still.src ? 'aspect-ratio:auto' : undefined}
            tabIndex={leased ? 0 : -1}
            onClick={onScreenClick}
            onKeyDown={onScreenKey}
          >
            {leased ? (
              <div class="ctrl-banner" id="banner" data-testid="control-banner">
                <Icon name="hand" />You have control · agent paused<span class="kbd on-accent">Esc</span> to release
              </div>
            ) : null}
            {frame || still.src ? (
              <img
                ref={imgRef}
                style="width:100%;display:block"
                src={frame || still.src}
                alt="The live page"
                onLoad={(e) => {
                  const el = e.target as HTMLImageElement
                  if (el.naturalWidth) naturalRef.current = { w: el.naturalWidth, h: el.naturalHeight }
                }}
              />
            ) : null}
          </div>
          <div class="panel-foot small">
            <span class="muted"><Icon name="shield" /> jailed Chromium · CDP over a pipe (no debugging port) · egress via the allowlist proxy only</span>
            <span style="margin-left:auto" class="mono tiny muted">{signedIn ? 'logged-in' : 'anonymous'}</span>
          </div>
        </div>
        <div class="stack">
          <div class="panel">
            <div class="panel-head"><h3>Session</h3></div>
            <div class="panel-body">
              <dl class="kv small">
                <dt>profile</dt>
                <dd>{signedIn ? <span class="tag" data-s="wait">logged-in browser · narrowed to its sites</span> : <span class="tag" data-s="ok">anonymous · ephemeral</span>}</dd>
                <dt>evaluate</dt>
                <dd><span class="tag" data-s="ok">{signedIn ? 'off (a credential was filled)' : 'off'}</span></dd>
              </dl>
            </div>
          </div>
          <div class="panel">
            <div class="panel-head"><h3>Actions</h3><span class="muted small">audited · values never recorded</span></div>
            <div class="list">
              {log.length === 0 ? (
                <div class="item small muted"><span class="grow">Nothing from you on this page yet. The agent's own actions are in <a class="dl-link" href="#/system/logs">Logs &amp; audit</a>.</span></div>
              ) : null}
              {log.map(([a, d, s], i) => (
                <div class="item small" style="align-items:flex-start" key={i}>
                  <StatusMark s={s} />
                  <div class="grow"><span class="mono" style="font-weight:500">{a}</span><div class="muted tiny mono">{d}</div></div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
      {more ? <Menu anchor={more} label={`More for ${id}`} onClose={() => setMore(null)} items={[{ label: 'Close browser…', icon: 'x', onClick: () => setClosing(true) }]} /> : null}
      {closing ? (
        <ConfirmModal
          title={`Close browser ${id}?`}
          body="Ends the browser session; the agent's next browser call opens a fresh one. Its ephemeral profile is discarded."
          ok="Close browser"
          danger
          onOk={async () => {
            try {
              await walletOp('browser-session-close', { session: id })
              go('browser')
            } catch (e) {
              setErr(humanError(e))
            }
          }}
          onClose={() => setClosing(false)}
        />
      ) : null}
    </div>
  )
}
