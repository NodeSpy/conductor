// A browser's live view (design §4.5): a CDP screencast relayed over a WebSocket,
// the session facts, and an operator takeover — a passkey lease that pauses the
// agent and drives the same sandboxed page with CDP input (keystrokes audited as
// a length only, like fills). Esc releases, and the agent resumes with a fresh
// snapshot.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { browser, who, takeover, releaseTakeover, sendInput, type BrowserSession } from '../api/browser'
import { Icon } from '../components/Icon'
import { humanError } from '../api/errors'

export function BrowserSessionView({ id }: { id: string }): JSX.Element {
  const [sess, setSess] = useState<BrowserSession | null>(null)
  const [err, setErr] = useState('')
  const [frame, setFrame] = useState('') // data: URL of the latest screencast frame
  const [leased, setLeased] = useState(false)
  const [busy, setBusy] = useState(false)
  const imgRef = useRef<HTMLImageElement>(null)
  const naturalRef = useRef<{ w: number; h: number }>({ w: 1280, h: 800 })

  useEffect(() => {
    browser
      .session(id)
      .then((r) => {
        setSess(r.session)
        setLeased(!!r.session.leased_by)
      })
      .catch((e) => setErr(humanError(e)))
  }, [id])

  // Open the screencast WebSocket; each text frame is a base64 JPEG.
  useEffect(() => {
    let ws: WebSocket | null = null
    try {
      ws = new WebSocket(browser.screencastURL(id))
      ws.onmessage = (ev) => {
        try {
          const f = JSON.parse(ev.data as string) as { data: string; format?: string }
          setFrame(`data:image/${f.format || 'jpeg'};base64,${f.data}`)
        } catch {
          /* ignore malformed frame */
        }
      }
      ws.onerror = () => {
        /* the live view falls back to the peek still below */
      }
    } catch {
      /* no screencast: the peek still is shown */
    }
    return () => ws?.close()
  }, [id])

  // Esc releases control.
  useEffect(() => {
    if (!leased) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') void doRelease()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [leased])

  async function doTakeover() {
    setBusy(true)
    setErr('')
    try {
      await takeover(id)
      setLeased(true)
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
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  function onScreenClick(e: MouseEvent) {
    if (!leased || !imgRef.current) return
    const r = imgRef.current.getBoundingClientRect()
    const nat = naturalRef.current
    const x = ((e.clientX - r.left) / r.width) * nat.w
    const y = ((e.clientY - r.top) / r.height) * nat.h
    void sendInput(id, { type: 'mouse', kind: 'mousePressed', x, y, button: 'left', clicks: 1 }).then(() =>
      sendInput(id, { type: 'mouse', kind: 'mouseReleased', x, y, button: 'left', clicks: 1 }),
    )
  }
  function onScreenKey(e: KeyboardEvent) {
    if (!leased) return
    if (e.key === 'Escape') return
    if (e.key.length === 1) void sendInput(id, { type: 'key', kind: 'char', text: e.key })
    else void sendInput(id, { type: 'key', kind: 'keyDown', key: e.key })
  }

  if (err && !sess) return <div class="col"><a class="back" href="#/browser">← Browsers</a><div class="banner err">{err}</div></div>
  if (!sess) return <div class="col"><p class="muted">Loading the session…</p></div>

  return (
    <div class="wl-page" data-session={id}>
      <div class="page-head">
        <div>
          <a class="back" href="#/browser">← Browsers</a>
          <h1 class="gen">{sess.identity}</h1>
        </div>
        <div class="actions">
          {leased ? (
            <button class="btn" type="button" disabled={busy} data-testid="release" onClick={() => void doRelease()}>
              <Icon name="undo" />Release
            </button>
          ) : (
            <button class="btn primary" type="button" disabled={busy} data-testid="takeover" onClick={() => void doTakeover()}>
              <Icon name="hand" />{busy ? 'Confirming…' : 'Take over'}
            </button>
          )}
        </div>
      </div>
      <p class="facts muted">
        Signs in as {who(sess.identity)}
        {sess.agent ? ` · ${sess.agent}` : ''}
        {sess.requester ? ` · asked by ${sess.requester}` : ''}
      </p>
      {err ? <div class="banner err">{err}</div> : null}

      <div class="grid" style="grid-template-columns:minmax(0,1fr) 340px;align-items:start">
        <div class="panel">
          <div class="bchrome">
            <div class="url"><Icon name="lock" class="lockic" /><span class="ellipsis">{sess.profile}</span></div>
            <span class="row tiny mono muted"><span class="rec" />CDP screencast</span>
          </div>
          <div
            class={'bscreen' + (leased ? ' takeover' : '')}
            tabIndex={leased ? 0 : -1}
            onClick={onScreenClick}
            onKeyDown={onScreenKey}
          >
            {leased ? (
              <div class="ctrl-banner" id="banner" data-testid="control-banner">
                <Icon name="hand" />You have control · agent paused<span class="kbd on-accent">Esc</span> to release
              </div>
            ) : null}
            <img
              ref={imgRef}
              style="width:100%;display:block"
              src={frame || browser.peekURL(id)}
              alt="live view"
              onLoad={(e) => {
                const el = e.target as HTMLImageElement
                if (el.naturalWidth) naturalRef.current = { w: el.naturalWidth, h: el.naturalHeight }
              }}
            />
          </div>
          <div class="panel-foot small"><span class="muted"><Icon name="shield" /> jailed Chromium · CDP over a pipe (no debugging port) · egress via the allowlist proxy only</span></div>
        </div>
        <div class="stack">
          <div class="panel">
            <div class="panel-head"><h3>Session</h3></div>
            <div class="panel-body"><dl class="kv small">
              <dt>profile</dt><dd>{sess.profile}</dd>
              <dt>identity</dt><dd class="mono">{sess.identity}</dd>
              {sess.agent ? (<><dt>agent</dt><dd class="mono">{sess.agent}</dd></>) : null}
              {sess.requester ? (<><dt>asked for by</dt><dd>{sess.requester}</dd></>) : null}
            </dl></div>
          </div>
          <div class="panel">
            <div class="panel-head"><h3>Actions</h3><span class="muted small">audited · values never recorded</span></div>
            <div class="panel-body"><p class="muted">Each browser action the agent takes is audited (never a value). Keystrokes you type while in control are audited as a length only.</p></div>
          </div>
        </div>
      </div>
    </div>
  )
}
