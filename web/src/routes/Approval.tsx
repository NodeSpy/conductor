// The approval screen (design §6.6): a proposal's canonical payload and digest,
// rendered from the daemon (never agent text), with a colourised Terraform plan
// when it's a plan. Approve or Deny steps up over the per-decision digest (a
// passkey, SSH key or authenticator code); the decision itself is a no-tabs
// choice in the view.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { approvalView, decideProposal, type ApprovalView } from '../api/auth'
import { botsApi, type ProposalRow } from '../api/bots'

export function Approval({ bot, id }: { bot: string; id: string }): JSX.Element {
  const [view, setView] = useState<ApprovalView | null>(null)
  const [row, setRow] = useState<ProposalRow | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState<'approve' | 'deny' | ''>('')
  const [done, setDone] = useState('')

  useEffect(() => {
    approvalView(bot, id).then(setView).catch((e) => setErr(String(e)))
    botsApi.botProposals(bot).then((r) => setRow(r.proposals.find((p) => p.proposal === id) || null)).catch(() => {})
  }, [bot, id])

  async function decide(decision: 'approve' | 'deny') {
    setBusy(decision)
    setErr('')
    try {
      await decideProposal(bot, id, decision)
      setDone(decision)
    } catch (e) {
      const t = e instanceof Error ? e.message : String(e)
      if (!/cancelled/.test(t)) setErr(t)
    } finally {
      setBusy('')
    }
  }

  if (err && !view) return <div class="col"><div class="banner err">{err}</div></div>
  if (!view) return <div class="col"><p class="muted">Loading the proposal…</p></div>

  // The view's summary carries the canonical action under `action` (what the
  // digest pins and what runs) and the requester's own words beside it.
  const summary = (view.summary as Record<string, unknown>) || {}
  const action = (summary.action as Record<string, unknown>) || {}
  const requesterWords = String(summary.requester_summary || '')
  const tf = row?.terraform

  if (done) {
    return (
      <div class="col">
        <div class="banner ok" role="status" data-testid="approval-done">{done === 'approve' ? 'Approved' : 'Denied'} — the decision is signed and recorded.</div>
        <a class="btn ghost sm" href={`#/bots/${bot}/approvals`}>Back to {bot}'s approvals</a>
      </div>
    )
  }

  return (
    <div class="col approval-screen" data-proposal={id}>
      <a class="back" href={`#/bots/${bot}/approvals`}>← {bot}'s approvals</a>
      <h1 class="screen-title serif">Approve this?</h1>
      <p class="muted">Review exactly what {bot} would do. You confirm your decision (passkey, SSH key or authenticator code), and the confirmation is bound to this plan, so it can never be replayed onto a different action.</p>

      {err ? <div class="banner err" role="alert">{err}</div> : null}

      <section class="panel approval-payload">
        <span class="panel-title">The action, exactly as it will run</span>
        <dl class="detail-list">
          {String(action.uses || '') ? <><dt>Verb</dt><dd class="mono">{String(action.uses)}</dd></> : null}
          <dt>Kind</dt><dd>{String(action.kind || '')}</dd>
          <dt>Tier</dt><dd>{String(summary.tier || row?.tier || '')}</dd>
          <dt>Requested by</dt><dd>{String(summary.requester_class || row?.requester_class || 'a worker')}</dd>
          {row?.case ? <><dt>Ticket</dt><dd class="mono">{row.case}</dd></> : null}
        </dl>
        <pre class="payload-json" data-testid="approval-canonical">{JSON.stringify(action, null, 2)}</pre>
        {requesterWords ? (
          <p class="facts muted" data-testid="approval-requester-words">
            The requester's own words (not checked): {requesterWords}
          </p>
        ) : null}
      </section>

      {tf ? (
        <section class="panel">
          <span class="panel-title">Terraform plan</span>
          <p class="facts muted">{tf.repo} · {tf.dir} · workspace {tf.workspace} · <strong class="tf-add">+{tf.add}</strong> <strong class="tf-change">~{tf.change}</strong> <strong class="tf-destroy">-{tf.destroy}</strong></p>
          {tf.destroyed?.length ? <p class="banner attn">Destroys: {tf.destroyed.join(', ')}</p> : null}
          {tf.plan ? <pre class="tf-plan" data-testid="tf-plan">{tf.plan.split('\n').map((l) => <div class={tfLineClass(l)}>{l}</div>)}</pre> : null}
          {tf.plan_sha256 ? <p class="facts muted">plan sha256 <span class="mono">{tf.plan_sha256.slice(0, 16)}…</span></p> : null}
        </section>
      ) : null}

      <section class="panel approval-sign">
        <div class="digest-row">
          <span class="muted">Approval digest</span>
          <span class="mono digest" data-testid="approval-digest">{view.action_sha256.slice(0, 24)}…</span>
        </div>
        <p class="muted">May be decided by: {view.operators.join(', ') || 'a configured operator'}.</p>
        <div class="row-actions approval-actions">
          <button class="btn primary" type="button" disabled={!!busy} data-testid="approve" onClick={() => void decide('approve')}>
            {busy === 'approve' ? 'Confirming…' : 'Approve…'}
          </button>
          <button class="btn ghost danger" type="button" disabled={!!busy} data-testid="deny" onClick={() => void decide('deny')}>
            {busy === 'deny' ? 'Confirming…' : 'Deny…'}
          </button>
        </div>
      </section>
    </div>
  )
}

function tfLineClass(l: string): string {
  const t = l.trimStart()
  if (t.startsWith('+')) return 'tfl add'
  if (t.startsWith('-')) return 'tfl del'
  if (t.startsWith('~') || t.startsWith('-/+')) return 'tfl change'
  return 'tfl'
}
