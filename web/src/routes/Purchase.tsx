// A purchase approval (design §4.5.1): what it buys, the card (last four only),
// the shop, the limits counting this purchase (the lowest layer wins, the
// tightest marked), what happens next, who asked. Approve or Deny is a passkey
// step-up bound to the decision — the dashboard's passkey IS the signature, as a
// bot proposal's is. After you decide, the card says so.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { browser, decidePurchase, money, who, hostOf, kindWord, type PurchaseDetail, type Purchase as P, type LayerState, type WalletCard } from '../api/browser'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { FactsLine, type Fact } from '../components/Facts'
import { humanError } from '../api/errors'

const STATUS_MARK: Record<string, string> = {
  pending: 'ember', approved: 'ok', used: 'run', completed: 'ok', denied: 'fail', refused: 'fail', expired: 'idle',
}

// PurchaseRow is a purchase in a list (Payment cards, Needs you) — the shared
// dl-need row, as the prototype's puRow (js/screens/wallet.js).
export function PurchaseRow({ p }: { p: P }): JSX.Element {
  const pending = p.status === 'pending'
  const r = p.receipt
  const mark = r && r.note ? 'wait' : STATUS_MARK[p.status] || 'idle'
  const why = pending
    ? `${who(p.bot ? 'bot:' + p.bot : p.identity)} asks before it buys`
    : r
    ? `${r.note ? 'Charged more than you approved' : 'Paid'} · order ${r.order_number || '—'}, ${money(r.total || p.total, r.currency || p.currency)}`
    : statusWord(p)
  const facts: Fact[] = [
    { icon: 'banknote', tone: 'danger', pri: 1, label: p.recurring ? 'A subscription' : 'Total', value: `${money(p.total, p.currency)}${p.recurring && p.interval ? ` / ${p.interval}` : ''}` },
    { icon: 'store', pri: 1, label: 'Shop', value: p.merchant ? hostOf(p.merchant) : '' },
    { icon: 'obj-card', pri: 2, label: 'Card', value: p.card },
  ]
  return (
    <li class="dl-need" data-purchase={p.id} data-status={p.status}>
      <a class="dl-need-a" href={`#/browser/purchases/${encodeURIComponent(p.id)}`}>
        <span class="dl-ng"><StatusMark s={mark} mkOnly label={pending ? 'Needs you' : statusWord(p)} /></span>
        <span class="dl-nt">
          <b>{title(p)}</b>
          <span class="dl-why">{why}</span>
          <FactsLine facts={facts} />
        </span>
        <span class="dl-age">{ago(p.created)}</span>
        <span class="dl-na"><span class="btn sm secondary">{pending ? 'Review' : 'Open'}</span></span>
      </a>
    </li>
  )
}

function ago(iso: string): string {
  const s = Math.max(1, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

function title(p: P): string {
  const item = p.items && p.items.length ? p.items[0].name + (p.items.length > 1 ? ` and ${p.items.length - 1} more` : '') : 'something'
  return `Buy ${item} for ${money(p.total, p.currency)}${p.recurring && p.interval ? ` a ${p.interval}` : ''}`
}

function statusWord(p: P): string {
  switch (p.status) {
    case 'approved': return 'Approved'
    case 'denied': return 'Denied'
    case 'completed': return p.receipt?.note ? 'Paid, more than you approved' : 'Paid'
    case 'used': return 'Paying now'
    case 'refused': return 'Not paid'
    case 'expired': return 'Expired'
    default: return p.status
  }
}

export function Purchase({ id }: { id: string }): JSX.Element {
  const [d, setD] = useState<PurchaseDetail | null>(null)
  const [card, setCard] = useState<WalletCard | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState<'approve' | 'deny' | ''>('')
  const [done, setDone] = useState('')
  const [reason, setReason] = useState('')
  const [denying, setDenying] = useState(false)

  function load() {
    browser.purchase(id).then((r) => {
      setD(r)
      browser.wallet().then((w) => setCard((w.cards || []).find((c) => c.name === r.purchase.card) || null)).catch(() => {})
    }).catch((e) => setErr(humanError(e)))
  }
  useEffect(load, [id])

  async function decide(decision: 'approve' | 'deny') {
    setBusy(decision)
    setErr('')
    try {
      await decidePurchase(id, decision, decision === 'deny' ? reason : '')
      setDone(decision)
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy('')
    }
  }

  if (err && !d) return <div class="col"><a class="back" href="#/browser/cards">← Payment cards</a><div class="banner err">{err}</div></div>
  if (!d) return <div class="col"><p class="muted">Loading the purchase…</p></div>
  const p = d.purchase
  const cur = p.currency || 'USD'
  const layers = d.remaining_by_layer || p.layers || []
  const pending = p.status === 'pending'
  const approvers = d.approvers || []

  if (done) {
    return (
      <div class="wl-page">
        <a class="back" href="#/browser/cards">← Payment cards</a>
        <div class="banner ok" role="status" data-testid="purchase-done">
          {done === 'approve' ? 'Approved' : 'Denied'} — the decision is confirmed and recorded.
        </div>
      </div>
    )
  }

  const who1 = who(p.bot ? 'bot:' + p.bot : p.identity)

  return (
    <div class="wl-page" data-purchase={id}>
      <nav class="dl-crumbs" aria-label="Where you are"><a href="#/browser">Browsers</a><span class="sep" aria-hidden="true">›</span><a href="#/browser/cards">Payment cards</a><span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">{title(p)}</span></nav>
      <div class="prop-single">
        <article class={'dl-card dl-proposal wl-pu' + (pending ? '' : ' decided')}>
          {pending ? (
            <div class="dl-kick" data-testid="purchase-kicker"><StatusMark s="ember" mkOnly />Needs you · <span class="nowrap">asked {ago(p.created)}</span>{p.expires ? <span class="nowrap"> · {lapse(p)}</span> : null}</div>
          ) : (
            <div class="dl-kick dl-sub"><StatusMark s={p.receipt?.note ? 'wait' : STATUS_MARK[p.status] || 'idle'} word={statusWord(p)} /> · {ago(p.created)}</div>
          )}
          <h2 class="dl-ptitle">{title(p)}</h2>
          {pending ? <p class="dl-ask">{who1} wants to buy this with the {p.card} card. You approve exactly these terms; it can then pay once, on {hostOf(p.merchant)} only.</p> : null}
          {err ? <div class="banner err">{err}</div> : null}

          <div class="prop-body">
            <div class="dl-pwhy wl-what">
              <span class="dl-lbl">What it buys</span>
              <table class="wl-items">
                <thead><tr><th>Item</th><th class="num">Qty</th><th class="num">Each</th></tr></thead>
                <tbody>
                  {(p.items || []).map((it, i) => (
                    <tr key={i}><td>{it.name}</td><td class="num">{it.qty || 1}</td><td class="num">{money(it.amount || 0, cur)}</td></tr>
                  ))}
                </tbody>
                <tfoot><tr><td colSpan={2}>Total{p.recurring && p.interval ? `, every ${p.interval}` : ''}</td><td class="num"><b>{money(p.total, cur)}</b></td></tr></tfoot>
              </table>
              {p.recurring ? (
                <p class="wl-flag"><StatusMark s="wait" mkOnly /><span><b>A subscription: it charges {money(p.total, cur)} every {p.interval}.</b> You're approving that it repeats, not only the first charge. A subscription is never bought without asking you.</span></p>
              ) : null}
              {p.supersedes ? (
                <p class="wl-flag"><StatusMark s="wait" mkOnly /><span><b>Asks again for a higher price.</b> You approved a lower total; at checkout the page showed more, so nothing was paid and it asks you for what the page shows.</span></p>
              ) : null}
            </div>

            {p.why ? <div class="dl-pwhy"><span class="dl-lbl">Why</span><p>{p.why}</p></div> : null}
            <p class="dl-origin">{p.requester ? `Asked for by ${p.requester}` : 'Asked for by you'}.</p>

            {p.receipt ? (
              <div class={'wl-receipt' + (p.receipt.note ? ' flag' : '')}>
                <span class="dl-lbl">Receipt</span>
                <p><StatusMark s={p.receipt.note ? 'wait' : 'ok'} mkOnly /><span>Order <b class="mono">{p.receipt.order_number || '—'}</b> · {money(p.receipt.total || 0, p.receipt.currency || cur)} charged{p.receipt.note ? <><br /><b>{p.receipt.note}</b> You were notified; the card's own provider limits are what stop this.</> : null}</span></p>
              </div>
            ) : null}
            {p.case_ref ? <p class="dl-sub">The ticket: {p.case_ref}</p> : null}

            <dl class="dl-where wl-where">
              <dt>Card</dt>
              <dd>
                <a class="dl-link" href={`#/browser/cards/${encodeURIComponent(p.card)}`}>{p.card}</a>
                {card ? <span class="dl-sub"> · {kindWord(card.kind).toLowerCase()}{card.frozen ? ' · frozen' : ''}</span> : null}
              </dd>
              <dt>Shop</dt>
              <dd>
                {hostOf(p.merchant)}
                {card && (card.merchants || []).includes(p.merchant) ? <span class="dl-sub"> · one of the shops this card may pay</span> : null}
              </dd>
              <dt>Limits</dt>
              <dd>
                <span class="dl-sub">What each limit leaves after this purchase; the lowest wins:</span>
                <ul class="wl-layers">
                  {layers.map((l) => <LayerRow key={l.layer} l={l} cur={cur} />)}
                </ul>
                {d.would_refuse ? <p class="wl-flag"><StatusMark s="fail" mkOnly /><span><b>If you approve it now, conductor refuses it:</b> {d.would_refuse}.</span></p> : null}
              </dd>
              <dt>Then</dt>
              <dd>{who1} may pay once, within {approvers.length ? 'an hour' : 'the approval window'}, on {hostOf(p.merchant)}'s checkout page. First conductor checks the page shows this total, fills the card itself and reads the receipt; if the page asks for more, nothing is paid and you're asked again. {p.bot ? who1 : 'The agent'} never sees the card.</dd>
              <dt>Undo</dt><dd>There's no undo in conductor; a refund is the shop's.</dd>
            </dl>
          </div>

          {pending ? (
            <div class="dl-decide prop-decide">
              {denying ? (
                <label class="form-field"><span>Tell {who1} why</span>
                  <textarea class="input" value={reason} placeholder="e.g. Not this month." onInput={(e) => setReason((e.target as HTMLTextAreaElement).value)} /></label>
              ) : null}
              <button class="btn lg primary" type="button" disabled={!!busy} data-testid="approve" onClick={() => void decide('approve')}>
                <Icon name="passkey" />{busy === 'approve' ? 'Confirming…' : 'Approve with passkey'}
              </button>
              {denying ? (
                <button class="btn lg secondary" type="button" disabled={!!busy} data-testid="deny-confirm" onClick={() => void decide('deny')}>
                  {busy === 'deny' ? 'Confirming…' : 'Confirm deny'}
                </button>
              ) : (
                <button class="btn lg secondary" type="button" onClick={() => setDenying(true)} data-testid="deny">Deny…</button>
              )}
              <span class="hint">{approvers.length ? `Only ${approvers.join(' or ')} can approve.` : 'No operator can approve yet.'}</span>
            </div>
          ) : null}
        </article>
      </div>
    </div>
  )
}

function LayerRow({ l, cur }: { l: LayerState; cur: string }): JSX.Element {
  const over = l.remaining < 0
  return (
    <li class={(over ? 'over' : l.binding ? 'bind' : '')}>
      {over ? <StatusMark s="fail" mkOnly /> : l.binding ? <StatusMark s="wait" mkOnly /> : <i />}
      <span>{l.layer}</span>
      <b>{over ? `over by ${money(-l.remaining, l.currency || cur)}` : `${money(l.remaining, l.currency || cur)} left of ${money(l.limit, l.currency || cur)}`}</b>
      {l.binding && !over ? <span class="wl-tight">the tightest</span> : null}
    </li>
  )
}

function lapse(p: P): string {
  if (!p.expires) return 'asked'
  const mins = Math.round((new Date(p.expires).getTime() - Date.now()) / 60000)
  if (mins <= 0) return 'expiring'
  if (mins < 60) return `expires in ${mins} min`
  return `expires in ${Math.round(mins / 60)} h`
}
