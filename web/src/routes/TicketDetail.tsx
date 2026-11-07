// A ticket's page (design §4.4.1): where it stands in words, its next action
// (Mark ready on a draft), the description, its comments (the chat component),
// and the details rail (assignee, priority, history). Comments live on the ledger
// case, so they load from the ticket itself, not the chat stream.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, displayName, who, type Ticket, type BotSummary } from '../api/bots'
import { Avatar } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { ago } from '../lib/format'
import { STATUS_LABEL } from './Tickets'
import { humanError } from '../api/errors'

const STATUS_MARK: Record<string, string> = {
  draft: 'draft',
  ready: 'ready',
  in_progress: 'progress',
  waiting: 'ember',
  done: 'ok',
  declined: 'fail',
}

export function TicketDetail({ num }: { num: string }): JSX.Element {
  const [t, setT] = useState<Ticket | null>(null)
  const [bots, setBots] = useState<Record<string, BotSummary>>({})
  const [err, setErr] = useState('')
  const [comment, setComment] = useState('')
  const [busy, setBusy] = useState(false)

  const load = () => botsApi.ticket(num).then(setT).catch((e) => setErr(humanError(e)))
  useEffect(() => {
    load()
    botsApi.bots().then((r) => {
      const m: Record<string, BotSummary> = {}
      for (const b of r.bots) m[b.bot] = b
      setBots(m)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [num])

  if (err) return <div class="tk-page"><nav class="dl-crumbs"><a href="#/tickets">Tickets</a></nav><div class="banner err" role="alert">{err}</div></div>
  if (!t) return <div class="tk-page"><p class="muted">Loading {num}…</p></div>

  const aw = t.assignee ? who(t.assignee) : null
  const aId = aw?.kind === 'bot' ? bots[aw.name]?.identity : undefined
  const open = t.status !== 'done' && t.status !== 'declined'
  const draft = t.status === 'draft'

  async function act(body: Record<string, unknown>) {
    setBusy(true)
    try {
      await botsApi.patchTicket(num, body)
      await load()
    } catch (e) {
      setErr(humanError(e))
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
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="tk-page" data-ticket={num}>
      <nav class="dl-crumbs"><a href="#/tickets">Tickets</a><span>{t.title}</span></nav>

      <section class="dl-card case-head tk-head">
        <div class="tk-ht">
          <StatusMark s={STATUS_MARK[t.status]} lg label={STATUS_LABEL[t.status] || t.status} />
          <h1 class="case-title dl-h1-long">{t.title} <span class="mono id">{t.ticket}</span></h1>
        </div>
        <p class="dl-sub case-state" data-testid="ticket-status">
          {STATUS_LABEL[t.status] || t.status}
          {t.created_by ? ` · opened by ${who(t.created_by).name}` : ''}
          {` · ${ago(t.updated)}`}
        </p>

        {draft ? (
          <div class="tk-ready">
            <p><b>A draft.</b> Nobody works on it until you mark it ready.</p>
            <button class="btn primary" type="button" disabled={busy} data-testid="mark-ready" onClick={() => void act({ status: 'ready' })}>
              <Icon name="check" />Mark ready
            </button>
          </div>
        ) : null}

        <div class="dl-actions">
          {open ? (
            <>
              <button class="btn secondary" type="button" disabled={busy} onClick={() => void act({ status: 'done' })}><Icon name="check" />Mark done</button>
              <button class="btn secondary" type="button" disabled={busy} onClick={() => void act({ status: 'declined' })}><Icon name="x" />Decline</button>
            </>
          ) : (
            <button class="btn secondary" type="button" disabled={busy} onClick={() => void act({ status: 'ready' })}><Icon name="undo" />Reopen</button>
          )}
        </div>
      </section>

      <div class="grid g-side tk-grid">
        <div class="stack">
          <section class="dl-card tk-desc">
            <header class="dl-ch"><h2>Description</h2></header>
            <p class="tk-body">{t.description || <span class="dl-sub">No description.</span>}</p>
          </section>

          <section class="dl-card tk-chat">
            <header class="dl-ch"><h2>Comments</h2></header>
            <div class="cm-thread" id="msgs" data-testid="chat-scroll">
              {(t.comments || []).length === 0 ? <p class="muted cm-empty">No comments yet.</p> : null}
              {(t.comments || []).map((c) => {
                const cw = who(c.by)
                const mine = cw.kind === 'operator'
                const cId = cw.kind === 'bot' ? bots[cw.name]?.identity : undefined
                const meta = (
                  <div class="cm-meta">
                    {mine ? <span>You</span> : <b>{displayName(cId, cw.name)}</b>}
                    <span class="dl-sub">{ago(c.at)}</span>
                  </div>
                )
                const bubble = <div class="cm-bubble">{c.text}</div>
                return mine ? (
                  <div class="cm-row cm-op" data-from={c.by}>{meta}{bubble}</div>
                ) : (
                  <div class="cm-row cm-bot" data-from={c.by}>
                    <Avatar id={cId} size={32} />
                    <div>{meta}{bubble}</div>
                  </div>
                )
              })}
            </div>
            <div class="cm-comp">
              <div class="cm-in">
                <div class="cm-field">
                  <textarea class="cm-ta" data-testid="comment-input" rows={2} placeholder="Comment…" value={comment} onInput={(e) => setComment((e.target as HTMLTextAreaElement).value)} />
                </div>
                <button class="btn primary cm-send" type="button" disabled={busy || !comment.trim()} onClick={() => void postComment()} aria-label="Comment">
                  <Icon name="send" />
                </button>
              </div>
              {err ? <p class="cm-err">{err}</p> : null}
            </div>
          </section>
        </div>

        <div class="stack tk-side">
          <section class="panel">
            <div class="panel-head"><h3>Details</h3></div>
            <div class="tk-fields">
              <div class="tk-f"><span class="dl-lbl">Status</span><span class="tk-st"><StatusMark s={STATUS_MARK[t.status]} mkOnly /> {STATUS_LABEL[t.status] || t.status}</span></div>
              <div class="tk-f">
                <span class="dl-lbl">Assigned to</span>
                <span>{aId ? <span class="inline-id"><Avatar id={aId} size={20} /> {displayName(aId, aw!.name)}</span> : t.assignee ? who(t.assignee).name : 'nobody yet'}</span>
              </div>
              <div class="tk-f"><span class="dl-lbl">Priority</span><span>{t.priority || 'normal'}</span></div>
              {t.due ? <div class="tk-f"><span class="dl-lbl">Due</span><span>{t.due}</span></div> : null}
              {t.labels?.length ? <div class="tk-f"><span class="dl-lbl">Labels</span><span>{t.labels.join(', ')}</span></div> : null}
            </div>
          </section>
          <section class="panel">
            <div class="panel-head"><h3>History</h3></div>
            <ol class="tk-hist">
              {(t.history || []).map((h) => (
                <li><span>{h.what} <span class="dl-sub">· {who(h.by).name}</span></span><span class="dl-sub">{ago(h.at)}</span></li>
              ))}
            </ol>
          </section>
        </div>
      </div>
    </div>
  )
}
