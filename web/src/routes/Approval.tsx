// The approval screen (design §4.4, prototype js/screens/bots.js proposalDetail,
// the approved mock's approvalPage): a needs-you kicker with age and expiry, the
// plain title, what it asks, why, the four change counts (a plan's Adds /
// Changes / Replaces / Destroys, or exactly what the action sends), where /
// effect / undo, who started it, Approve / Deny, who can approve,
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
import { approvalView, authState, confirmLabel, decideProposal, stepupMethods, type ApprovalView } from '../api/auth'

// usesPasskey: the passkey glyph only when a passkey is what confirms.
const usesPasskey = () => stepupMethods(authState.value)[0] === 'passkey'
import { botsApi, displayName } from '../api/bots'
import { humanError } from '../api/errors'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FactsLine } from '../components/Facts'
import { actionFact, agoText, envFact, envOf, expiresIn, originText, proposalTitle, proposalWhy, useVerbs, type ProposalRowD } from './BotDetail'

// The detail page spells a span out (prototype UI.humanSpan): "23 h 52 min".
const humanSpan = (t: string) => t.replace(/(\d+)d\b/g, '$1 d').replace(/(\d+)h\b/g, '$1 h').replace(/(\d+)m\b/g, '$1 min')

export function Approval({ bot, id }: { bot: string; id: string }): JSX.Element {
  const [view, setView] = useState<ApprovalView | null>(null)
  const [row, setRow] = useState<ProposalRowD | null>(null)
  const [who, setWho] = useState<{ name: string; team: string }>({ name: bot, team: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState<'approve' | 'deny' | ''>('')
  const [done, setDone] = useState('')
  const verbs = useVerbs()

  useEffect(() => {
    approvalView(bot, id).then(setView).catch((e) => setErr(humanError(e)))
    botsApi.botProposals(bot).then((r) => setRow(r.proposals.find((p) => p.proposal === id) || null)).catch(() => {})
    botsApi.bot(bot).then((b) => setWho({ name: displayName(b.identity, bot), team: b.team || '' })).catch(() => {})
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
  const title = row ? proposalTitle(row) : String(action.uses || action.kind || 'this proposal')
  // The ask in words (never the raw verb and payload: those are "Exactly what it sends" below).
  const nm = who.name
  const pend = !row || row.status === 'pending'
  const ok = row?.status === 'executed' || row?.status === 'approved'
  const ds = ok ? 'ok' : row?.status === 'denied' || row?.status === 'refused' || row?.status === 'failed' ? 'fail' : 'idle'
  const word = row ? (ok ? 'Approved' : row.status[0].toUpperCase() + row.status.slice(1)) : ''
  const next = row?.status === 'executed' ? 'Done: conductor applied exactly what was signed.'
    : ok ? (tf ? 'Next: conductor checks the plan is unchanged, applies it, and posts the result on the ticket.' : 'Next: it runs once, exactly as approved; the result lands on the ticket.')
    : row?.status === 'denied' ? `${nm} was told${row.reason ? ` why: “${row.reason.replace(/[.!?]\s*$/, '')}”` : ''}. Nothing runs.`
    : row?.status === 'refused' ? 'conductor refused it before it reached you; nothing ran.'
    : row?.status === 'expired' ? 'Nobody decided in time; nothing ran.' : ''
  const ask = row?.kind === 'knowledge' ? proposalWhy(row, nm) : tf ? `${nm} wants to run terraform apply on ${(row && envOf(row)) || tf.workspace}.` : `${nm} wants to ${row?.summary || `run ${String(action.uses || row?.uses || 'an action')}`}.`
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
      <nav class="dl-crumbs prop-crumbs" aria-label="Where you are">
        <a href="#/bots">Bots</a>
        {who.team ? <><span class="sep" aria-hidden="true">›</span><a href={`#/teams/${who.team}`}>{who.team}</a></> : null}
        <span class="sep" aria-hidden="true">›</span><a href={`#/bots/${bot}`}>{nm}</a>
        <span class="sep" aria-hidden="true">›</span><a href={`#/bots/${bot}/proposals`}>Approvals</a>
        <span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">{title}</span>
      </nav>
      {err ? <div class="banner err" role="alert">{err}</div> : null}

      <div class="prop-single">
        <article class={'dl-card dl-proposal' + (pend ? '' : ' decided')}>
          {pend ? (
            <div class="dl-kick">
              <StatusMark s="ember" mkOnly label="Needs you" />
              Needs you{row?.created ? <> · <span class="nowrap">asked {agoText(row.created)}</span></> : null}{row?.expires && expiresIn(row.expires) ? <> · <span class="nowrap">expires in {humanSpan(expiresIn(row.expires))}</span></> : null}
            </div>
          ) : (
            <div class="prop-decided" data-s={ds} data-testid="approval-decided">
              <p class="prop-decided-h">
                <StatusMark s={ds} mkOnly lg label={word} />
                <b>{word}</b>
                {row?.approver ? ` by ${row.approver.replace(/^operator:/, '') === authState.value.operator ? 'you' : row.approver.replace(/^operator:/, '')}` : ''}
                {row?.decided ? ` · ${agoText(row.decided)}` : ''}
              </p>
              {next ? <p class="prop-decided-n">{next}</p> : null}
            </div>
          )}
          <h1 class="dl-ptitle">{title}</h1>
          <p class="dl-ask">{ask}</p>
          {row ? <FactsLine facts={[envFact(row), actionFact(row, verbs)]} /> : null}

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
                <Payload action={action} />
              </div>
            )}

            {requesterWords ? (
              <div class="dl-pwhy">
                <span class="dl-lbl">Why</span>
                <p data-testid="approval-requester-words">{requesterWords}</p>
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
              {originText(nm, row?.requester, row?.requester_class)}
            </p>

            <p class="dl-sub prop-can-link"><a class="dl-link" href={`#/bots/${bot}/access`}>What {nm} may do on its own, and what it asks first →</a></p>

            <details class="dl-audit-d">
              <summary><Icon name="chev" />For audit: ids, hashes and the digest your approval signs</summary>
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

          {pend ? <div class="dl-decide prop-decide">
            <button class="btn lg primary" type="button" disabled={!!busy} data-testid="approve" onClick={() => void decide('approve')}>
              {usesPasskey() ? <Icon name="passkey" /> : null}
              {busy === 'approve' ? 'Confirming…' : confirmLabel('Approve')}
            </button>
            <button class="btn lg secondary" type="button" disabled={!!busy} data-testid="deny" onClick={() => void decide('deny')}>
              {busy === 'deny' ? 'Confirming…' : 'Deny…'}
            </button>
            <span class="hint">{operators.length ? `Only ${operators.join(' or ')} can approve.` : 'Any operator can approve.'}</span>
          </div> : null}
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

// Payload is exactly what the action sends, field by field (prototype UI.payloadHTML): the verb, then each option; a
// long or multi-line value gets a block of its own.
function Payload({ action }: { action: Record<string, unknown> }): JSX.Element {
  const opts = (action.options as Record<string, unknown>) || Object.fromEntries(Object.entries(action).filter(([k]) => k !== 'uses' && k !== 'kind'))
  const entries = Object.entries(opts)
  return (
    <div class="payload" data-testid="approval-canonical">
      <div class="pl-row"><span class="pl-k">verb</span><span class="pl-v mono">{String(action.uses || action.kind || '')}</span></div>
      {entries.length ? (
        entries.map(([k, v]) => {
          const t = typeof v === 'object' ? JSON.stringify(v) : String(v)
          return (
            <div class="pl-row">
              <span class="pl-k mono">{k}</span>
              {t.length > 80 || /\n/.test(t) ? <div class="pl-long">{t}</div> : <span class="pl-v mono">{t}</span>}
            </div>
          )
        })
      ) : (
        <div class="pl-row muted small">no options</div>
      )}
    </div>
  )
}
