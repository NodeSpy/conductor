// The approval screen (design §4.4, prototype js/screens/bots.js proposalDetail,
// the approved mock's approvalPage): a needs-you kicker with age and expiry, the
// plain title, what it asks, why, the four change counts (a plan's Adds /
// Changes / Replaces / Destroys, or exactly what the action sends), where /
// effect / undo, who started it, Approve with passkey / Deny, who can approve,
// and the technical detail (ids, hashes, the digest a passkey signs) folded at
// the end. It stands alone at every width: no bot head or tabs above it, one
// Back.
//
// Deliberate departures the brief carries over from CONFORMANCE.md: the
// status is a mark and a word, not a capsule; the decision bar is pinned
// (`.prop-decide`, sticky via CSS); the Rotate session / Sweep now / Deliver
// an event / Workers actions the mock has no place for are left for the
// bot's own page, not drawn here.
//
// Data gaps (the daemon's ProposalRow carries no env, impact or reversible-undo
// text yet): Where/Effect/Undo only renders what's there (a Terraform plan's
// workspace), and the risk fact under the title is inferred from the plan's
// workspace name containing "prod" — a real `env` field would read better.
// Deny has no reason field in the decision API yet, so "Deny…" decides
// straight away rather than opening a reason sheet.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { approvalView, decideProposal, type ApprovalView } from '../api/auth'
import { botsApi, type ProposalRow } from '../api/bots'
import { humanError } from '../api/errors'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { ago, humanDuration } from '../lib/format'

function expiresIn(iso?: string): string {
  if (!iso) return ''
  const ms = new Date(iso).getTime() - Date.now()
  if (Number.isNaN(ms) || ms <= 0) return ''
  return `expires in ${humanDuration(ms)}`
}

export function Approval({ bot, id }: { bot: string; id: string }): JSX.Element {
  const [view, setView] = useState<ApprovalView | null>(null)
  const [row, setRow] = useState<ProposalRow | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState<'approve' | 'deny' | ''>('')
  const [done, setDone] = useState('')

  useEffect(() => {
    approvalView(bot, id).then(setView).catch((e) => setErr(humanError(e)))
    botsApi.botProposals(bot).then((r) => setRow(r.proposals.find((p) => p.proposal === id) || null)).catch(() => {})
  }, [bot, id])

  async function decide(decision: 'approve' | 'deny') {
    setBusy(decision)
    setErr('')
    try {
      await decideProposal(bot, id, decision)
      setDone(decision)
    } catch (e) {
      const t = humanError(e)
      if (!/cancelled/.test(t)) setErr(t)
    } finally {
      setBusy('')
    }
  }

  if (err && !view) return <div class="col"><a class="back" href={`#/bots/${bot}/approvals`}>← {bot}'s approvals</a><div class="banner err">{err}</div></div>
  if (!view) return <div class="col"><p class="muted">Loading the proposal…</p></div>

  const summary = (view.summary as Record<string, unknown>) || {}
  const action = (summary.action as Record<string, unknown>) || {}
  const requesterWords = String(summary.requester_summary || '')
  const tf = row?.terraform
  const title = row?.summary || String(action.uses || action.kind || 'this proposal')
  const ask = row?.action || (tf ? `${bot} wants to run terraform apply on ${tf.workspace}.` : `${bot} wants to ${title}.`)
  const prod = !!(tf && /prod/i.test(tf.workspace))
  const operators = view.operators || []

  if (done) {
    return (
      <div class="col">
        <div class="banner ok" role="status" data-testid="approval-done">{done === 'approve' ? 'Approved' : 'Denied'} — the decision is signed and recorded.</div>
        <a class="btn ghost sm" href={`#/bots/${bot}/approvals`}>Back to {bot}'s approvals</a>
      </div>
    )
  }

  return (
    <div class="col" data-proposal={id}>
      <a class="back" href={`#/bots/${bot}/approvals`}>← {bot}'s approvals</a>
      {err ? <div class="banner err" role="alert">{err}</div> : null}

      <div class="prop-single">
        <article class="dl-card dl-proposal">
          <div class="dl-kick">
            <StatusMark s="ember" mkOnly label="Needs you" />
            Needs you{row?.created ? ` · asked ${ago(row.created)}` : ''}{row?.expires && expiresIn(row.expires) ? ` · ${expiresIn(row.expires)}` : ''}
          </div>
          <h1 class="dl-ptitle">{title}</h1>
          <p class="dl-ask">{ask}</p>
          {tf || prod ? (
            <span class="fx">
              {prod ? <span class="fx-i prod">production</span> : null}
              {tf ? <span class="fx-i">changes infrastructure</span> : null}
            </span>
          ) : null}

          <div class="prop-body">
            {tf ? (
              <div class="dl-pwhy prop-what">
                <span class="dl-lbl">What will change</span>
                <div class="dl-changes">
                  <div class={tf.add ? '' : 'zero'}><b>{tf.add}</b><span>Adds</span></div>
                  <div class={tf.change ? '' : 'zero'}><b>{tf.change}</b><span>Changes</span></div>
                  <div class={tf.replaced?.length ? '' : 'zero'} data-s={tf.replaced?.length ? 'wait' : undefined}><b>{tf.replaced?.length || 0}</b><span>Replaces</span></div>
                  <div class={tf.destroy ? '' : 'zero'} data-s={tf.destroy ? 'fail' : undefined}><b>{tf.destroy}</b><span>Destroys</span></div>
                </div>
                {tf.replaced?.length ? (
                  <p class="dl-chg-note">
                    <StatusMark s="wait" mkOnly label="Replaces" />
                    <span><b>Replaces {tf.replaced.length} {tf.replaced.length === 1 ? 'resource' : 'resources'}</b> ({tf.replaced.join(', ')}). Stateful things such as databases are never replaced without a separate rule.</span>
                  </p>
                ) : null}
              </div>
            ) : (
              <div class="dl-pwhy">
                <span class="dl-lbl">Exactly what it sends</span>
                <pre class="dl-code" data-testid="approval-canonical">{JSON.stringify(action, null, 2)}</pre>
              </div>
            )}

            {requesterWords ? (
              <div class="dl-pwhy">
                <span class="dl-lbl">Why</span>
                <p data-testid="approval-requester-words">The requester's own words (not checked): {requesterWords}</p>
              </div>
            ) : null}

            {tf?.workspace ? (
              <dl class="dl-where">
                <dt>Where</dt>
                <dd>{tf.workspace[0].toUpperCase()}{tf.workspace.slice(1)} · workspace {tf.workspace}</dd>
              </dl>
            ) : null}

            <p class="dl-origin">
              {originIcon(row?.requester_class)}
              {originText(bot, row?.requester, row?.requester_class)}
            </p>

            <details class="dl-audit-d">
              <summary><Icon name="chev" />For audit: ids, hashes and the digest your passkey signs</summary>
              <dl class="dl-facts">
                <dt>Proposal</dt><dd><span class="mono">{row?.proposal || id}</span> · tier <span class="mono">{row?.tier || String(summary.tier || '')}</span></dd>
                <dt>Verb</dt><dd class="mono">{String(action.uses || row?.uses || '')}</dd>
                {tf ? <><dt>Exact command</dt><dd class="mono">terraform apply · repo {tf.repo} · dir {tf.dir} · workspace {tf.workspace}</dd></> : null}
                {tf?.plan_sha256 ? <><dt>Plan sha256</dt><dd class="mono">{tf.plan_sha256.slice(0, 16)}…</dd></> : null}
                {row?.case ? <><dt>Ticket</dt><dd><a class="dl-link mono" href={`#/tickets/${row.case}`}>{row.case}</a></dd></> : null}
                <dt>Asked for by</dt><dd class="mono">{row?.requester || '—'}{row?.requester_class ? ` (${row.requester_class})` : ''}</dd>
                <dt>Digest</dt><dd class="mono" data-testid="approval-digest">{view.action_sha256.slice(0, 24)}…</dd>
              </dl>
            </details>
          </div>

          <div class="dl-decide prop-decide">
            <button class="btn lg primary" type="button" disabled={!!busy} data-testid="approve" onClick={() => void decide('approve')}>
              <Icon name="passkey" />
              {busy === 'approve' ? 'Confirming…' : 'Approve with passkey'}
            </button>
            <button class="btn lg secondary" type="button" disabled={!!busy} data-testid="deny" onClick={() => void decide('deny')}>
              {busy === 'deny' ? 'Confirming…' : 'Deny…'}
            </button>
            <span class="hint">{operators.length ? `Only ${operators.join(' or ')} can approve.` : 'Any operator can approve.'}</span>
          </div>
        </article>
      </div>
    </div>
  )
}

function originIcon(cls?: string): JSX.Element {
  if (cls === 'event') return <Icon name="zap" />
  if (cls === 'user') return <Icon name="warn" />
  if (cls === 'proactive') return <Icon name="clock" />
  if (cls === 'peer') return <Icon name="bots" />
  return <Icon name="users" />
}

function originText(bot: string, requester?: string, cls?: string): string {
  if (cls === 'event') return `Started by an event (${requester || 'unknown'}), not a person.`
  if (cls === 'user') return `Asked for in chat by someone who isn't one of your operators (${requester || 'unknown'}); it runs with no more trust than they have.`
  if (cls === 'proactive') return `Found on ${bot}'s own routine check; nobody asked.`
  if (cls === 'peer') return `Asked for by another bot (${requester || 'unknown'}), carrying the original requester's trust.`
  return `Asked for by an operator (${requester || 'unknown'}).`
}
