// A browser's live view (design §4.5): a CDP screencast relayed over a WebSocket,
// the session facts, and an operator takeover — a passkey lease that pauses the
// agent and drives the same sandboxed page with CDP input (keystrokes audited as
// a length only, like fills). Esc releases, and the agent resumes with a fresh
// snapshot.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { browser, who, takeover, releaseTakeover, sendInput, type BrowserSession } from '../api/browser'

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
      .catch((e) => setErr(String(e)))
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
      setErr(String(e))
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
      setErr(String(e))
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
    <div class="col browser-live" data-session={id}>
      <a class="back" href="#/browser">← Browsers</a>
      <div class="page-head">
        <h1 class="screen-title serif sans-gen">{sess.identity}</h1>
        <div class="row-actions">
          {leased ? (
            <button class="btn" type="button" disabled={busy} data-testid="release" onClick={() => void doRelease()}>
              Release
            </button>
          ) : (
            <button class="btn primary" type="button" disabled={busy} data-testid="takeover" onClick={() => void doTakeover()}>
              {busy ? 'Touch your passkey…' : 'Take over'}
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

      <div class="live-grid">
        <section class="panel live-screen-panel">
          {leased ? (
            <div class="ctrl-banner" data-testid="control-banner">
              You have control · the agent is paused <span class="kbd">Esc</span> to release
            </div>
          ) : null}
          <div
            class={'bscreen' + (leased ? ' takeover' : '')}
            tabIndex={leased ? 0 : -1}
            onClick={onScreenClick}
            onKeyDown={onScreenKey}
          >
            <img
              ref={imgRef}
              class="live-frame"
              src={frame || browser.peekURL(id)}
              alt="live view"
              onLoad={(e) => {
                const el = e.target as HTMLImageElement
                if (el.naturalWidth) naturalRef.current = { w: el.naturalWidth, h: el.naturalHeight }
              }}
            />
          </div>
          <p class="panel-foot facts muted">jailed Chromium · CDP over a pipe (no debugging port) · egress via the allowlist proxy only</p>
        </section>

        <aside class="live-side">
          <section class="panel">
            <span class="panel-title">Session</span>
            <dl class="detail-list">
              <dt>Profile</dt>
              <dd>{sess.profile}</dd>
              <dt>Identity</dt>
              <dd class="mono">{sess.identity}</dd>
              {sess.agent ? (<><dt>Agent</dt><dd class="mono">{sess.agent}</dd></>) : null}
              {sess.requester ? (<><dt>Asked for by</dt><dd>{sess.requester}</dd></>) : null}
            </dl>
          </section>
          <section class="panel">
            <div class="panel-head">
              <span class="panel-title">Actions</span>
              <span class="muted small">audited · values never recorded</span>
            </div>
            <p class="muted">Each browser action the agent takes is audited (never a value). Keystrokes you type while in control are audited as a length only.</p>
          </section>
        </aside>
      </div>
    </div>
  )
}
