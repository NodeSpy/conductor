// A purchase approval (design §4.5.1): what it buys, the card (last four only),
// the shop, the limits counting this purchase (the lowest layer wins, the
// tightest marked), what happens next, who asked. Approve or Deny is a passkey
// step-up bound to the decision — the dashboard's passkey IS the signature, as a
// bot proposal's is. After you decide, the card says so.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { browser, decidePurchase, money, who, hostOf, type PurchaseDetail, type Purchase as P, type LayerState } from '../api/browser'

// PurchaseRow is a purchase in a list (Payment cards, Needs you).
export function PurchaseRow({ p }: { p: P }): JSX.Element {
  const pending = p.status === 'pending'
  return (
    <li class={'dl-conn wl-purow' + (pending ? ' needs' : '')} data-purchase={p.id} data-status={p.status}>
      <a class="dl-conn-main" href={`#/browser/purchases/${encodeURIComponent(p.id)}`}>
        <b>{title(p)}</b> <span class="dl-sub">{p.card}</span>
        <p class="dl-why muted">
          {pending ? 'Needs you' : statusWord(p)} · {who(p.bot ? 'bot:' + p.bot : p.identity)}
          {p.merchant ? ` · ${hostOf(p.merchant)}` : ''}
        </p>
      </a>
      {pending ? <a class="btn primary sm" href={`#/browser/purchases/${encodeURIComponent(p.id)}`} data-testid="review-purchase">Review</a> : null}
    </li>
  )
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
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState<'approve' | 'deny' | ''>('')
  const [done, setDone] = useState('')
  const [reason, setReason] = useState('')
  const [denying, setDenying] = useState(false)

  function load() {
    browser.purchase(id).then(setD).catch((e) => setErr(String(e)))
  }
  useEffect(load, [id])

  async function decide(decision: 'approve' | 'deny') {
    setBusy(decision)
    setErr('')
    try {
      await decidePurchase(id, decision, decision === 'deny' ? reason : '')
      setDone(decision)
    } catch (e) {
      setErr(String(e))
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
      <div class="col browsers">
        <a class="back" href="#/browser/cards">← Payment cards</a>
        <div class="banner ok" role="status" data-testid="purchase-done">
          {done === 'approve' ? 'Approved' : 'Denied'} — the decision is confirmed and recorded.
        </div>
      </div>
    )
  }

  return (
    <div class="col browsers wl-pu" data-purchase={id}>
      <a class="back" href="#/browser/cards">← Payment cards</a>
      {pending ? <p class="kicker needs" data-testid="purchase-kicker">Needs you · {lapse(p)}</p> : <p class="kicker">{statusWord(p)}</p>}
      <h1 class="screen-title serif">{title(p)}</h1>
      {pending ? (
        <p class="dl-ask muted">{who(p.bot ? 'bot:' + p.bot : p.identity)} wants to buy this with the {p.card} card. You approve exactly these terms; it can then pay once, on {hostOf(p.merchant)} only.</p>
      ) : null}
      {err ? <div class="banner err">{err}</div> : null}

      <section class="panel">
        <span class="panel-title">What it buys</span>
        <table class="wl-items">
          <thead><tr><th>Item</th><th>Qty</th><th>Each</th></tr></thead>
          <tbody>
            {(p.items || []).map((it) => (
              <tr><td>{it.name}</td><td>{it.qty || 1}</td><td>{money(it.amount || 0, cur)}</td></tr>
            ))}
          </tbody>
          <tfoot><tr><td>Total{p.recurring && p.interval ? `, every ${p.interval}` : ''}</td><td></td><td><b>{money(p.total, cur)}</b></td></tr></tfoot>
        </table>
        {p.recurring ? <p class="callout warn">A subscription: it charges {money(p.total, cur)} every {p.interval}. You're approving that it repeats, not only the first charge.</p> : null}
        {p.supersedes ? <p class="callout warn">Asks again for a higher price. You approved a lower total; at checkout the page showed more, so nothing was paid and it asks you for what the page shows.</p> : null}
      </section>

      <section class="panel">
        <span class="panel-title">Where it goes</span>
        <dl class="detail-list">
          <dt>Card</dt><dd>{p.card}</dd>
          <dt>Shop</dt><dd>{hostOf(p.merchant)}</dd>
        </dl>
        <h3 class="subhead">The limits counting this purchase</h3>
        <p class="muted small">What each limit leaves after this purchase; the lowest wins.</p>
        <ul class="wl-layers">
          {layers.map((l) => <LayerRow key={l.layer} l={l} cur={cur} />)}
        </ul>
        {d.would_refuse ? <p class="callout err" data-testid="would-refuse">If you approve it now, conductor refuses it: {d.would_refuse}.</p> : null}
        <p class="muted small">
          {who(p.bot ? 'bot:' + p.bot : p.identity)} may pay once, on {hostOf(p.merchant)}'s checkout page. First conductor checks the page shows this total, fills the card itself and reads the receipt; if the page asks for more, nothing is paid and you're asked again. The agent never sees the card.
        </p>
      </section>

      {p.why ? <section class="panel"><span class="panel-title">Why</span><p>{p.why}</p>{p.case_ref ? <p class="facts muted">The ticket: {p.case_ref}</p> : null}</section> : null}

      {p.receipt ? (
        <section class="panel">
          <span class="panel-title">Receipt</span>
          <p>Order {p.receipt.order_number} · {money(p.receipt.total || 0, p.receipt.currency || cur)} charged</p>
          {p.receipt.note ? <p class="callout warn">{p.receipt.note}</p> : null}
        </section>
      ) : null}

      {pending ? (
        <section class="panel wl-decide">
          {denying ? (
            <label class="form-field"><span>Tell {who(p.bot ? 'bot:' + p.bot : p.identity)} why</span>
              <textarea value={reason} placeholder="e.g. Not this month." onInput={(e) => setReason((e.target as HTMLTextAreaElement).value)} /></label>
          ) : null}
          <div class="row-actions">
            <button class="btn lg primary" type="button" disabled={!!busy} data-testid="approve" onClick={() => void decide('approve')}>
              {busy === 'approve' ? 'Confirming…' : 'Approve…'}
            </button>
            {denying ? (
              <button class="btn lg ghost danger" type="button" disabled={!!busy} data-testid="deny-confirm" onClick={() => void decide('deny')}>
                {busy === 'deny' ? 'Confirming…' : 'Deny…'}
              </button>
            ) : (
              <button class="btn lg ghost" type="button" onClick={() => setDenying(true)} data-testid="deny">Deny…</button>
            )}
          </div>
          <p class="muted small">{approvers.length ? `Only ${approvers.join(', ')} can approve.` : 'No operator can approve yet.'}</p>
        </section>
      ) : null}
    </div>
  )
}

function LayerRow({ l, cur }: { l: LayerState; cur: string }): JSX.Element {
  const over = l.remaining < 0
  return (
    <li class={'wl-layer' + (over ? ' over' : l.binding ? ' bind' : '')}>
      <span class="wl-layer-name">{l.layer}</span>
      <span class="wl-layer-room">
        {over ? `over by ${money(-l.remaining, l.currency || cur)}` : `${money(l.remaining, l.currency || cur)} left of ${money(l.limit, l.currency || cur)}`}
      </span>
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
