// A proposal's or a purchase's approval content, ported from the prototype's
// approvalPage / bots.js proposalDetail: a needs-you kicker, the plain title
// (the approve description), what it asks (the summary), Approve / Deny with
// an optional reason, and a folded "for audit" block with the digest. The
// in-app button is the confirmation; the digest always comes from the
// approval view (never computed here) — see client.rs's `decide`, which
// does the SSH step-up and sends it as `X-Conductor-Stepup`.
import { useState } from 'preact/hooks'
import type { JSX } from 'preact'
import { api, ApiError } from '../api/client'
import type { ApprovalView, Decision } from '../api/types'
import { Icon, Mark } from './icons'

type Kind = 'proposal' | 'purchase'

export function Approval(props: { kind: Kind; bot: string | null; id: string; onDecided: () => void }): JSX.Element | null {
  const [view, setView] = useState<ApprovalView | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(true)
  const [denying, setDenying] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState<Decision | null>(null)
  const [decided, setDecided] = useState<Decision | null>(null)

  if (loading && !view && !err) {
    api
      .approvalView(props.kind, props.bot, props.id)
      .then((v) => {
        setView(v)
        setLoading(false)
      })
      .catch((e) => {
        setErr(e instanceof ApiError ? e.message : String(e))
        setLoading(false)
      })
  }

  const decide = async (decision: Decision) => {
    setBusy(decision)
    setErr('')
    try {
      await api.decide(props.kind, props.bot, props.id, decision, decision === 'deny' ? reason.trim() || undefined : undefined)
      setDecided(decision)
      props.onDecided()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  if (loading) return <p class="dl-sub">Loading…</p>
  if (err && !view) return <p class="gate-err" role="alert">{err}</p>
  if (!view) return null

  const who = view.bot || 'conductor'
  const summaryText = typeof view.summary === 'string' ? view.summary : view.summary ? JSON.stringify(view.summary) : ''

  return (
    <article class="dl-card dl-proposal" data-testid="approval">
      <div class="dl-kick">
        <Mark s="ember" lg />
        Needs you · {view.purpose === 'browser.purchase' ? 'a purchase' : `from ${who}`}
      </div>
      <h2 class="dl-ptitle">{decided ? (decided === 'approve' ? view.descriptions.approve : view.descriptions.deny) : view.descriptions.approve}</h2>
      {summaryText ? (
        <div class="dl-pwhy">
          <span class="dl-lbl">Why</span>
          <p>{summaryText}</p>
        </div>
      ) : null}
      {decided ? (
        <p class="dl-decided">
          <Mark s={decided === 'approve' ? 'ok' : 'fail'} />
          {decided === 'approve' ? 'Approved.' : 'Denied.'} {decided === 'approve' ? 'It runs exactly as approved.' : 'The bot was told.'}
        </p>
      ) : (
        <>
          <p class="dl-sub">{view.operators.length ? `Only ${view.operators.join(' or ')} can decide this.` : 'Any operator can decide this.'}</p>
          {err ? <p class="gate-err" role="alert">{err}</p> : null}
          {denying ? (
            <div class="field">
              <label for="deny-reason">Tell {who} why (optional)</label>
              <textarea id="deny-reason" class="textarea" rows={2} value={reason} onInput={(e) => setReason((e.target as HTMLTextAreaElement).value)} />
            </div>
          ) : null}
          <div class="dl-decide prop-decide">
            {denying ? (
              <>
                <button class="btn lg danger" disabled={busy !== null} onClick={() => void decide('deny')} data-testid="approval-deny-confirm">
                  {busy === 'deny' ? 'Denying…' : 'Confirm deny'}
                </button>
                <button class="btn lg ghost" disabled={busy !== null} onClick={() => setDenying(false)}>
                  Cancel
                </button>
              </>
            ) : (
              <>
                <button class="btn lg primary" disabled={busy !== null} onClick={() => void decide('approve')} data-testid="approval-approve">
                  <Icon name="check" />
                  {busy === 'approve' ? 'Approving…' : 'Approve'}
                </button>
                <button class="btn lg secondary" disabled={busy !== null} onClick={() => setDenying(true)} data-testid="approval-deny">
                  Deny…
                </button>
              </>
            )}
          </div>
        </>
      )}
      <details class="dl-audit-d">
        <summary>
          <Icon name="chev" />
          For audit: the digest your key signs
        </summary>
        <dl class="dl-facts prop-tech-facts">
          <dt>{props.kind === 'proposal' ? 'Proposal' : 'Purchase'}</dt>
          <dd class="mono">{props.id}</dd>
          <dt>Action</dt>
          <dd class="mono">{view.action_sha256}</dd>
        </dl>
      </details>
    </article>
  )
}
