// A ticket's page (design §4.4.1): where it stands in words, its next action
// (Mark ready on a draft), the description, its comments (the chat component),
// and the details rail (assignee, priority, history). Comments live on the ledger
// case, so they load from the ticket itself, not the chat stream.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, who, type Ticket, type BotSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { ago } from '../lib/format'
import { STATUS_LABEL } from './Tickets'

export function TicketDetail({ num }: { num: string }): JSX.Element {
  const [t, setT] = useState<Ticket | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [err, setErr] = useState('')
  const [comment, setComment] = useState('')
  const [busy, setBusy] = useState(false)

  const load = () => botsApi.ticket(num).then(setT).catch((e) => setErr(String(e)))
  useEffect(() => {
    load()
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setBots(m)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [num])

  if (err) return <div class="col"><div class="banner err">{err}</div></div>
  if (!t) return <div class="col"><p class="muted">Loading {num}…</p></div>

  const aw = t.assignee ? who(t.assignee) : null
  const aId = aw?.kind === 'bot' ? bots[aw.name]?.identity : undefined

  async function act(body: Record<string, unknown>) {
    setBusy(true)
    try {
      await botsApi.patchTicket(num, body)
      await load()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  async function postComment() {
    if (!comment.trim()) return
    setBusy(true)
    try {
      await botsApi.comment(num, comment.trim())
      setComment('')
      await load()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col ticket-detail" data-ticket={num}>
      <a class="back" href="#/tickets">← Tickets</a>
      <div class="ticket-detail-head">
        <div class="ticket-detail-head-main">
          <h1 class="screen-title serif">{t.title} <span class="mono id">{t.ticket}</span></h1>
          <p class="facts muted" data-testid="ticket-status">
            {STATUS_LABEL[t.status] || t.status}
            {t.created_by ? ` · opened by ${who(t.created_by).name}` : ''}
            {` · ${ago(t.updated)}`}
          </p>
        </div>
        <div class="row-actions">
          {t.status === 'draft' ? (
            <button class="btn primary" type="button" disabled={busy} data-testid="mark-ready" onClick={() => void act({ status: 'ready' })}>Mark ready…</button>
          ) : null}
          {t.status !== 'done' && t.status !== 'declined' ? (
            <>
              <button class="btn ghost sm" type="button" disabled={busy} onClick={() => void act({ status: 'done' })}>Mark done</button>
              <button class="btn ghost sm danger" type="button" disabled={busy} onClick={() => void act({ status: 'declined' })}>Decline</button>
            </>
          ) : (
            <button class="btn ghost sm" type="button" disabled={busy} onClick={() => void act({ status: 'ready' })}>Reopen</button>
          )}
        </div>
      </div>

      {t.status === 'draft' ? <div class="banner attn">A draft is an idea — no bot starts on it. Mark it ready (it needs an assignee) to let work begin.</div> : null}

      <div class="ticket-detail-body">
        <div class="ticket-main">
          <section class="panel">
            <span class="panel-title">Description</span>
            <p class="ticket-desc">{t.description || <span class="muted">No description.</span>}</p>
          </section>

          <section class="panel">
            <span class="panel-title">Comments</span>
            <div class="comments">
              {(t.comments || []).length === 0 ? <p class="muted">No comments yet.</p> : null}
              {(t.comments || []).map((c) => {
                const cw = who(c.by)
                const cId = cw.kind === 'bot' ? bots[cw.name]?.identity : undefined
                return (
                  <div class="comment" data-from={c.by}>
                    {cw.kind === 'bot' ? <Avatar id={cId} size={30} /> : null}
                    <div class="comment-body">
                      <div class="chat-meta"><span class="chat-who">{cw.kind === 'operator' ? 'You' : displayName(cId, cw.name)}</span><span class="chat-at">{ago(c.at)}</span></div>
                      <div class="chat-text">{c.text}</div>
                    </div>
                  </div>
                )
              })}
            </div>
            <div class="chat-input-wrap">
              <textarea class="chat-input" data-testid="comment-input" rows={2} value={comment} placeholder="Comment…" onInput={(e) => setComment((e.target as HTMLTextAreaElement).value)} />
              <button class="btn primary chat-send" type="button" disabled={busy || !comment.trim()} onClick={() => void postComment()}>Comment</button>
            </div>
          </section>
        </div>

        <aside class="ticket-rail">
          <section class="panel">
            <span class="panel-title">Details</span>
            <dl class="detail-list">
              <dt>Assigned to</dt>
              <dd>{aId ? <span class="inline-id"><Avatar id={aId} size={20} /> {displayName(aId, aw!.name)}</span> : t.assignee ? who(t.assignee).name : 'nobody yet'}</dd>
              <dt>Priority</dt>
              <dd>{t.priority || 'normal'}</dd>
              {t.due ? <><dt>Due</dt><dd>{t.due}</dd></> : null}
              {t.labels?.length ? <><dt>Labels</dt><dd>{t.labels.join(', ')}</dd></> : null}
            </dl>
          </section>
          <section class="panel">
            <span class="panel-title">History</span>
            <ul class="history-list">
              {(t.history || []).map((h) => (
                <li><span class="muted">{ago(h.at)}</span> {h.what} <span class="muted">· {who(h.by).name}</span></li>
              ))}
            </ul>
          </section>
        </aside>
      </div>
    </div>
  )
}
