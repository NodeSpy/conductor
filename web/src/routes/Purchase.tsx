// A purchase (js/screens/wallet.js purchasePage/puRow/purchaseWhat): what it
// buys and on what terms (the items and total, a subscription flagged), the
// card and shop, what each spending limit leaves (the lowest wins, the tightest
// marked), what happens next, who asked; or, once decided, its record and
// receipt. Approve or Deny opens the signing sheet (what you're agreeing to in
// words), then your passkey signs the decision — the dashboard's step-up IS the
// signature, bound to that exact decision.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { browser, decidePurchase, type PurchaseDetail, type Purchase as P, type LayerState, type WalletCard } from '../api/browser'
import { StatusMark } from '../components/StatusMark'
import { Icon } from '../components/Icon'
import { FactsLine, type Fact } from '../components/Facts'
import { agoText, botName, host, listWords, loadRoster, roster, whoWord } from '../lib/browserwords'
import { humanError } from '../api/errors'
import { authState, confirmLabel } from '../api/auth'
import { cardName, kindOf, usd } from '../lib/walletwords'

/** A purchase's status: a pending or approved one past its expiry has lapsed. */
export function puStatus(p: P): string {
  let s = p.status
  if ((s === 'pending' || s === 'approved') && p.expires && Date.parse(p.expires) < Date.now()) s = 'expired'
  return s
}
const PST: Record<string, [string, string]> = {
  pending: ['ember', 'Waiting for you'], approved: ['ok', 'Approved'], used: ['run', 'Paying'], completed: ['ok', 'Paid'],
  denied: ['fail', 'Denied'], refused: ['fail', 'Not paid'], expired: ['idle', 'Expired'],
}
const pst = (p: P): [string, string] => PST[puStatus(p)] || ['idle', puStatus(p)]

const itemsWord = (p: P): string => {
  const it = p.items || []
  if (!it.length) return 'a purchase'
  const a = it[0]
  return `${(a.qty || 1) > 1 ? `${a.qty} × ` : ''}${a.name}${it.length > 1 ? ` and ${it.length - 1} more` : ''}`
}
const perWord = (p: P): string => (p.recurring ? ` a ${p.interval || 'period'}` : '')
export const purchaseTitle = (p: P): string => `Buy ${itemsWord(p)} for ${usd(p.total, p.currency)}${perWord(p)}`
const byWord = (p: P): string =>
  p.bot ? botName(p.bot) : /^dir:/.test(p.identity || '') ? `An agent you started in ${p.identity.slice(4)}` : whoWord(p.identity).replace(/^./, (c) => c.toUpperCase())

/** A purchase's facts: the money and the shop first, then who, then the card. */
export function purchaseFacts(p: P, card?: WalletCard | null, short = false): (Fact | null)[] {
  return [
    { icon: 'banknote', tone: 'danger', value: `${usd(p.total, p.currency)}${perWord(p)}`, pri: 1, label: p.recurring ? 'A subscription' : 'Total' },
    { icon: 'store', value: host(p.merchant), pri: 1, label: 'Shop' },
    p.bot ? { icon: 'obj-bot', value: botName(p.bot), href: `#/bots/${p.bot}`, pri: 1, label: 'Bot' } : { icon: 'obj-sandbox', value: whoWord(p.identity), pri: 2, label: 'Asked for by' },
    short ? null : { icon: 'obj-card', value: `${p.card} •••• ${card?.last4 || ''}`.trim(), href: `#/browser/cards/${p.card}`, pri: 2, label: 'Card' },
  ]
}

/** Where a purchase is decided: a bot's on its proposal (its Approvals), anyone else's here. */
export const puHref = (p: P): string =>
  p.bot && p.bot_proposal && puStatus(p) === 'pending' ? `#/bots/${encodeURIComponent(p.bot)}/proposals/${encodeURIComponent(p.bot_proposal)}` : `#/browser/purchases/${encodeURIComponent(p.id)}`

// PurchaseRow is a purchase in a list (Payment cards, a card's page): puRow.
export function PurchaseRow({ p, card }: { p: P; card?: WalletCard | null }): JSX.Element {
  void roster.value
  const [s, word] = pst(p)
  const r = p.receipt
  const why =
    s === 'ember'
      ? `${byWord(p)} asks before it buys`
      : r
      ? `${r.note ? 'Charged more than you approved · ' : 'Paid · '}order ${r.order_number || '—'}, ${usd(r.total ?? p.total, r.currency || p.currency)}`
      : word + (puStatus(p) === 'refused' && p.reason ? `: ${p.reason.split(/\.\s/)[0].replace(/\.$/, '')}` : '')
  return (
    <li class="dl-need" data-s={s} data-purchase={p.id} data-status={p.status}>
      <a class="dl-need-a" href={puHref(p)}>
        <span class="dl-ng"><StatusMark s={r && r.note ? 'wait' : s} mkOnly lg label={word} /></span>
        <span class="dl-nt">
          <b>{purchaseTitle(p)}</b>
          <span class="dl-why">{why}</span>
          <FactsLine facts={purchaseFacts(p, card)} />
        </span>
        <span class="dl-age">{agoText(p.created)}</span>
        <span class="dl-na"><span class="btn sm secondary">{s === 'ember' ? 'Review' : 'Open'}</span></span>
      </a>
    </li>
  )
}

// --- the spending limits, as the daemon computes them (each layer and window) ---

interface Group { key: string; name: string; left: number; limit: number; dim: string; over: boolean; bind: boolean }
const DIM: Record<string, string> = { purchase: 'per_purchase', day: 'per_day', '30 days': 'per_month', ticket: 'total' }

function layerName(layer: string): string {
  let m
  if ((m = /^card (\S+) for (.+)$/.exec(layer))) return `The card’s limits for ${whoWord(m[2])}`
  if (/^card \S+$/.test(layer)) return 'The card’s limits'
  if ((m = /^bot (\S+) on card (\S+)$/.exec(layer))) return `${botName(m[1])}’s caps on ${m[2]}`
  if ((m = /^bot (\S+)$/.exec(layer))) return `${botName(m[1])}’s own caps`
  if ((m = /^team (\S+) on card (\S+)$/.exec(layer))) return `Team ${m[1]}’s caps on ${m[2]}`
  if ((m = /^team (\S+)$/.exec(layer))) return `Team ${m[1]}’s caps`
  if (/^ticket /.test(layer)) return 'This ticket’s budget'
  return layer
}

/** Each layer once, at the window this purchase leaves the least room in. */
function groups(ls: LayerState[], total: number): Group[] {
  const out: Group[] = []
  for (const l of ls) {
    if (l.window === 'currency') continue
    const left = Math.round((l.remaining - total) * 100) / 100
    const g = out.find((x) => x.key === l.layer)
    const cand: Group = { key: l.layer, name: layerName(l.layer), left, limit: l.limit, dim: DIM[l.window] || l.window, over: left < -0.004, bind: !!l.binding }
    if (!g) out.push(cand)
    else if (left < g.left) Object.assign(g, cand, { bind: g.bind || cand.bind })
    else if (l.binding) g.bind = true
  }
  return out
}

function roomWords(g: Group, cur: string): string {
  if (g.over) return `over by ${usd(-g.left, cur)}`
  if (g.dim === 'total') return `${usd(g.left, cur)} left of ${usd(g.limit, cur)}`
  if (g.dim === 'per_purchase') return `${usd(g.left, cur)} under the ${usd(g.limit, cur)} per-purchase cap`
  return `${usd(g.left, cur)} left ${g.dim === 'per_day' ? 'today' : 'in 30 days'}, of ${usd(g.limit, cur)}`
}

function Layers({ ls, total, cur, refuse }: { ls: LayerState[]; total: number; cur: string; refuse?: string }): JSX.Element {
  const gs = groups(ls, total)
  return (
    <>
      <ul class="wl-layers">
        {gs.map((g) => (
          <li class={(g.bind ? 'bind' : '') + (g.over ? ' over' : '')} key={g.key}>
            {g.over ? <StatusMark s="fail" mkOnly /> : g.bind ? <StatusMark s="wait" mkOnly /> : <i />}
            <span>{g.name}</span>
            <b>{roomWords(g, cur)}</b>
            {g.bind && !g.over ? <span class="wl-tight">the tightest</span> : null}
          </li>
        ))}
      </ul>
      {refuse ? (
        <p class="wl-flag"><StatusMark s="fail" mkOnly /><span><b>If you approve it now, conductor refuses it:</b> {refuse}.</span></p>
      ) : null}
    </>
  )
}

// What the decision did, once made (UI.purchaseDecided).
function Decided({ p, me, fallback }: { p: P; me: string; fallback: JSX.Element }): JSX.Element {
  const st = puStatus(p)
  const r = p.receipt
  const by = byWord(p)
  const when = p.decided ? agoText(p.decided) : ''
  const appr = (p.approver || '').replace(/^operator:/, '')
  const whoA = appr && appr === me ? 'you' : appr
  const hm = (iso: string) => new Date(iso).toTimeString().slice(0, 5)
  const v: [string, string, string] | null =
    st === 'approved' ? ['ok', `Approved${whoA ? ` by ${whoA}` : ''}`, `${by} may pay once${p.expires ? `, before ${hm(p.expires)}` : ''}, on ${host(p.merchant)}’s checkout page. The receipt will show here.`]
    : st === 'denied' ? ['fail', `Denied${whoA ? ` by ${whoA}` : ''}`, `Nothing was paid. ${by} was told${p.reason ? `: “${p.reason}”` : '.'}`]
    : st === 'completed' ? [r && r.note ? 'wait' : 'ok', r && r.note ? 'Paid, more than you approved' : 'Paid', r ? `Order ${r.order_number}, ${usd(r.total ?? p.total, r.currency || p.currency)} charged${r.note ? `. ${r.note}` : ''}.` : '']
    : st === 'used' ? ['run', 'Paying now', `${by} is at the checkout; the receipt will show here.`]
    : st === 'refused' ? ['fail', 'Not paid', p.reason || '']
    : st === 'expired' ? ['idle', 'Expired', 'The approval lapsed unused. Nothing was paid.']
    : null
  if (!v) return fallback
  return (
    <div class="wl-done" data-s={v[0]} data-testid="purchase-done">
      <StatusMark s={v[0]} mkOnly lg />
      <div>
        <b>{v[1]}</b>
        {when ? <span class="dl-sub"> · {when}</span> : null}
        {v[2] ? <p>{v[2]}</p> : null}
      </div>
    </div>
  )
}

function expiresIn(p: P): string {
  const m = Math.max(1, Math.round((Date.parse(p.expires) - Date.now()) / 60000))
  return m < 60 ? `${m} min` : `${Math.round(m / 60)} h`
}

export function Purchase({ id }: { id: string }): JSX.Element {
  const [d, setD] = useState<PurchaseDetail | null>(null)
  const [card, setCard] = useState<WalletCard | null>(null)
  const [err, setErr] = useState('')
  const [seal, setSeal] = useState<'approve' | 'deny' | null>(null)
  const me = authState.value.operator || ''
  useEffect(loadRoster, [])
  void roster.value

  function load() {
    browser.purchase(id).then((r) => {
      setD(r)
      browser.wallet().then((w) => setCard((w.cards || []).find((c) => c.name === r.purchase.card) || null)).catch(() => {})
    }).catch((e) => setErr(humanError(e)))
  }
  useEffect(load, [id])

  if (err && !d) return <div class="callout danger small" role="alert">{err}</div>
  if (!d) return <p class="muted">Loading the purchase…</p>
  const p = d.purchase
  const cur = p.currency || 'USD'
  const pend = puStatus(p) === 'pending'
  const [s, word] = pst(p)
  const ops = d.approvers || []
  const by = byWord(p)
  const req = p.requester || ''
  const layers = d.remaining_by_layer || p.layers || []

  return (
    <div class="wl-pu-page" data-purchase={id}>
      <nav class="dl-crumbs" aria-label="Where you are"><a href="#/browser">Browsers</a><span class="sep" aria-hidden="true">›</span><a href="#/browser/cards">Payment cards</a><span class="sep" aria-hidden="true">›</span><span class="cur" aria-current="page">{purchaseTitle(p)}</span></nav>
      <div class="prop-single">
        <article class={'dl-card dl-proposal wl-pu' + (pend ? '' : ' decided')}>
          {pend ? (
            <div class="dl-kick" data-testid="purchase-kicker">
              <StatusMark s="ember" mkOnly />Needs you · <span class="nowrap">asked {agoText(p.created)}</span>
              {p.expires ? <> · <span class="nowrap">expires in {expiresIn(p)}</span></> : null}
            </div>
          ) : (
            <Decided p={p} me={me} fallback={<div class="dl-kick dl-sub"><StatusMark s={s} word={word} sm /> · {agoText(p.created)}</div>} />
          )}
          <h2 class="dl-ptitle">{purchaseTitle(p)}</h2>
          {pend ? <p class="dl-ask">{by} wants to buy this with the {p.card} card. You approve exactly these terms; {p.bot ? 'it' : 'the agent'} can then pay once, on {host(p.merchant)} only.</p> : null}
          {err ? <div class="callout danger small" role="alert">{err}</div> : null}
          <div class="prop-body">
            <div class="dl-pwhy wl-what">
              <span class="dl-lbl">What it buys</span>
              <table class="wl-items">
                <thead><tr><th>Item</th><th class="num">Qty</th><th class="num">Each</th></tr></thead>
                <tbody>
                  {(p.items || []).map((it, i) => (
                    <tr key={i}><td>{it.name}</td><td class="num">{it.qty || 1}</td><td class="num">{usd(it.amount || 0, cur)}</td></tr>
                  ))}
                </tbody>
                <tfoot><tr><td colSpan={2}>Total{p.recurring ? `, every ${p.interval || 'period'}` : ''}</td><td class="num"><b>{usd(p.total, cur)}</b> <span class="dl-sub">{cur}</span></td></tr></tfoot>
              </table>
              {p.recurring ? (
                <p class="wl-flag"><StatusMark s="wait" mkOnly /><span><b>A subscription: it charges {usd(p.total, cur)} every {p.interval || 'period'}.</b> You’re approving that it repeats, not only the first charge. A subscription is never bought without asking you.</span></p>
              ) : null}
              {p.supersedes ? (
                <p class="wl-flag"><StatusMark s="wait" mkOnly /><span><b>Asks again for a higher price.</b> At checkout the page showed {usd(p.total, cur)}, more than you approved, so nothing was paid and it asks you for what the page shows.</span></p>
              ) : null}
            </div>
            <dl class="dl-where wl-where">
              <dt>Card</dt>
              <dd>
                <a class="dl-link" href={`#/browser/cards/${encodeURIComponent(p.card)}`}>{p.card}</a> · {cardName(card)} · {kindOf(card?.kind).toLowerCase()}
                {card?.frozen ? <> · <StatusMark s="idle" word="frozen" sm /></> : null}
              </dd>
              <dt>Shop</dt>
              <dd>{host(p.merchant)} <span class="dl-sub">· {(card?.merchants || []).includes(p.merchant) ? 'one of the shops this card may pay' : 'not on this card’s list'}</span></dd>
              <dt>Limits</dt>
              <dd><span class="dl-sub">What each limit leaves after this purchase; the lowest wins:</span><Layers ls={layers} total={p.total} cur={cur} refuse={d.would_refuse} /></dd>
              <dt>Then</dt>
              <dd>{by} may pay once, within an hour, on {host(p.merchant)}’s checkout page. First conductor checks the page shows this total, fills the card itself and reads the receipt; if the page asks for more, nothing is paid and you’re asked again. {p.bot ? botName(p.bot) : 'The agent'} never sees the card.</dd>
              <dt>Undo</dt>
              <dd>There’s no undo in conductor; a refund is the shop’s.</dd>
            </dl>
            {p.why ? <div class="dl-pwhy"><span class="dl-lbl">Why</span><p>{p.why}</p></div> : null}
            <p class="dl-origin">
              {/^operator:/.test(req) ? (
                <><Icon name="users" />Asked for by {req.slice(9) === me ? 'you' : req.slice(9)}{/^dir:/.test(p.identity) ? <>, in an agent you started in <span class="mono">{p.identity.slice(4)}</span></> : null}.</>
              ) : (
                <><Icon name="warn" />Asked for by <span class="mono">{req || 'someone else'}</span>.</>
              )}
            </p>
            {p.receipt ? (
              <div class={'wl-receipt' + (p.receipt.note ? ' flag' : '')}>
                <span class="dl-lbl">Receipt</span>
                <p>
                  <StatusMark s={p.receipt.note ? 'wait' : 'ok'} mkOnly />
                  <span>
                    Order <b class="mono">{p.receipt.order_number || '—'}</b> · {usd(p.receipt.total ?? p.total, p.receipt.currency || cur)} charged · {agoText(p.receipt.at)}
                    {p.receipt.note ? <><br /><b>{p.receipt.note}</b> You were notified; the card’s own provider limits are what stop this.</> : null}
                  </span>
                </p>
              </div>
            ) : null}
            {p.case_ref ? <p class="dl-sub prop-can-link">The ticket: {p.case_ref}</p> : null}
            <details class="dl-audit-d">
              <summary><Icon name="chev" />For audit: the purchase, its identity and the digest your passkey signs</summary>
              <dl class="dl-facts">
                <dt>Purchase</dt><dd class="mono">{p.id}</dd>
                <dt>Card</dt><dd class="mono">{p.card}</dd>
                <dt>Merchant</dt><dd class="mono">{p.merchant}</dd>
                <dt>Identity</dt><dd class="mono">{p.identity}</dd>
                {p.bot_proposal ? <><dt>Bot proposal</dt><dd class="mono">{p.bot} {p.bot_proposal}</dd></> : null}
                {p.supersedes ? <><dt>Replaces</dt><dd class="mono">{p.supersedes}</dd></> : null}
                <dt>Digest</dt><dd class="mono">{p.digest}</dd>
                <dt>Signed by</dt><dd>{p.approver || 'not yet: your passkey signs this digest'}</dd>
              </dl>
            </details>
          </div>
          {pend && p.bot ? (
            <div class="dl-decide prop-decide">
              {p.bot_proposal ? (
                <a class="btn lg primary" href={puHref(p)} data-testid="review-proposal"><Icon name="passkey" />Review {botName(p.bot)}’s approval</a>
              ) : null}
              <span class="hint">A bot’s purchase is decided on its approval, under {botName(p.bot)} › Approvals.</span>
            </div>
          ) : pend ? (
            <div class="dl-decide prop-decide">
              <button class="btn lg primary" type="button" data-testid="approve" onClick={() => (card?.frozen ? setErr(`${p.card} is frozen: unfreeze it before approving a purchase with it.`) : setSeal('approve'))}>
                <Icon name="passkey" />{confirmLabel('Approve')}
              </button>
              <button class="btn lg secondary" type="button" data-testid="deny" onClick={() => setSeal('deny')}>Deny…</button>
              <span class="hint">{ops.length ? `Only ${ops.join(' or ')} can approve.` : 'No operator can approve yet.'}{card?.frozen ? ' The card is frozen: unfreeze it first.' : ''}</span>
            </div>
          ) : null}
        </article>
      </div>
      {seal ? <Seal p={p} card={card} deny={seal === 'deny'} layers={layers} onClose={() => setSeal(null)} onDone={() => { setSeal(null); load() }} /> : null}
    </div>
  )
}

// The signing sheet: what you're agreeing to in words first; ids and the
// digest only under For audit. Its button asks for your passkey.
function Seal({ p, card, deny, layers, onClose, onDone }: { p: P; card: WalletCard | null; deny: boolean; layers: LayerState[]; onClose: () => void; onDone: () => void }): JSX.Element {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const by = byWord(p)
  const cur = p.currency || 'USD'
  const items = listWords((p.items || []).map((it) => `${(it.qty || 1) > 1 ? `${it.qty} × ` : ''}${it.name}`)) || 'a purchase'
  const bind = deny ? null : groups(layers, p.total).find((g) => g.bind)
  const req = p.requester || ''
  async function go() {
    setBusy(true)
    setErr('')
    try {
      await decidePurchase(p.id, deny ? 'deny' : 'approve', reason.trim())
      onDone()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div class="scrim" id="scrim">
      <div class="modal wl-seal" role="dialog" aria-modal="true" tabIndex={-1} data-testid="purchase-seal">
        <div class="modal-head">
          <div>
            <div class="caps">{deny ? 'Deny' : 'Approve'} a purchase · signed with your passkey</div>
            <h2>{deny ? 'Deny' : 'Approve'}: {purchaseTitle(p)}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div class="modal-body wl-seal-body">
          <p class="seal-words">
            {deny ? (
              <>You’re refusing to let <b>{by}</b> buy {items} for {usd(p.total, cur)}{perWord(p)}. Nothing is paid, and {by} is told.</>
            ) : (
              <>You’re letting <b>{by}</b> buy <b>{items}</b> for <b>{usd(p.total, cur)}</b>{p.recurring ? `, charged every ${p.interval || 'period'} until it’s cancelled at the shop` : ', once'}, at {host(p.merchant)}, with the {p.card} card.</>
            )}
          </p>
          <dl class="dl-where wl-seal-where">
            <dt>Total</dt><dd><b>{usd(p.total, cur)}</b> {cur}{p.recurring ? ` every ${p.interval || 'period'}: a subscription` : ', one payment'}</dd>
            <dt>Card</dt><dd>{p.card} · {cardName(card)}</dd>
            <dt>Shop</dt><dd>{host(p.merchant)}</dd>
            <dt>For</dt><dd>asked for by {/^operator:/.test(req) ? 'you' : req ? `someone else (${req})` : 'you'}</dd>
            {bind ? <><dt>Tightest limit</dt><dd>{bind.name}: {roomWords(bind, cur)}</dd></> : null}
            {deny ? null : <><dt>Then</dt><dd>{by} may pay once, within an hour, on {host(p.merchant)}’s checkout page. {by} never sees the card: conductor fills it itself.</dd></>}
          </dl>
          {deny ? (
            <div class="field">
              <label for="seal-reason">Tell {by} why <span class="opt">optional</span></label>
              <textarea class="textarea" id="seal-reason" rows={2} maxLength={500} placeholder="e.g. Not this month." value={reason} onInput={(e) => setReason((e.target as HTMLTextAreaElement).value)} />
            </div>
          ) : null}
          <details class="dl-audit-d wl-seal-audit">
            <summary><Icon name="chev" />For audit: what your passkey signs</summary>
            <dl class="dl-facts">
              <dt>Purchase</dt><dd class="mono">{p.id}</dd>
              {p.bot_proposal ? <><dt>The bot’s approval</dt><dd class="mono">{p.bot} · {p.bot_proposal}</dd></> : null}
              <dt>Merchant</dt><dd class="mono">{p.merchant}</dd>
              <dt>Identity</dt><dd class="mono">{p.identity}</dd>
              <dt>Decision</dt><dd class="mono">{deny ? 'deny' : 'approve'}</dd>
              <dt>Digest</dt><dd class="mono">{p.digest}</dd>
            </dl>
          </details>
          <div class={'seal' + (busy ? ' signing' : '')} id="seal">
            <div id="seal-msg"><b>{busy ? 'Confirm it’s you…' : 'Confirm with your passkey'}</b><div class="muted small">This signature covers only this purchase, once.</div></div>
          </div>
          {err ? <div class="callout danger small" role="alert">{err}</div> : null}
        </div>
        <div class="modal-foot">
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class={'btn ' + (deny ? 'danger' : 'primary')} type="button" id="seal-go" disabled={busy} data-testid={deny ? 'deny-confirm' : 'approve-confirm'} onClick={() => void go()}>
            <Icon name="passkey" />{confirmLabel(deny ? 'Deny' : 'Approve')}
          </button>
        </div>
      </div>
    </div>
  )
}
