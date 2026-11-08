// A ticket's spending limit (prototype js/screens/wallet.js UI.ticketBudget,
// budgetFields, w-tkb): the most a ticket's bot may spend on it, optionally
// only with one card and only at some shops. Set on a new ticket or changed on
// a ticket's page through the ticket API's spend_limit; a new, larger or
// looser limit (or clearing one) widens what a bot may spend, so the daemon
// asks for a passkey first (the step-up dialog, handled in the API client).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, type Ticket, type TicketSpendLimit } from '../api/bots'
import { browser, money, type WalletCard } from '../api/browser'
import { Icon } from './Icon'
import { toast } from './ChatBits'
import { humanError } from '../api/errors'

export interface SpendDraft {
  amount: string
  card: string
  merchants: string[]
}
export const emptySpend = (sl?: TicketSpendLimit): SpendDraft => ({ amount: sl ? Number(sl.amount).toFixed(2) : '', card: sl?.card || '', merchants: sl?.merchants ? [...sl.merchants] : [] })

const originErr = (x: string) => {
  try {
    const u = new URL(x)
    return u.protocol === 'https:' && u.origin === x ? '' : `${x} isn’t a shop’s exact address (like https://shop.example.com)`
  } catch {
    return `${x} isn’t a shop’s exact address (like https://shop.example.com)`
  }
}

// readSpend: undefined when left empty (and not required), a string error, or
// the limit.
export function readSpend(d: SpendDraft, required: boolean): TicketSpendLimit | string | undefined {
  const ms = d.merchants.map((m) => m.trim().replace(/\/$/, '')).filter(Boolean)
  if (!d.amount.trim() && !ms.length && !d.card && !required) return undefined
  const v = d.amount.replace(/[$,\s]/g, '')
  if (!/^\d+(\.\d{1,2})?$/.test(v) || Number(v) <= 0) return 'An amount in dollars, above 0 (up to two decimals).'
  const bad = ms.map(originErr).find(Boolean)
  if (bad) return bad
  const sl: TicketSpendLimit = { amount: Number(v).toFixed(2), currency: 'USD' }
  if (d.card) sl.card = d.card
  if (ms.length) sl.merchants = ms
  return sl
}

export function useCards(): WalletCard[] {
  const [cards, setCards] = useState<WalletCard[]>([])
  useEffect(() => {
    browser
      .wallet()
      .then((w) => setCards(w.cards || []))
      .catch(() => setCards([]))
  }, [])
  return cards
}

export function SpendFields({ d, set, err, cards }: { d: SpendDraft; set: (d: SpendDraft) => void; err?: string; cards: WalletCard[] }): JSX.Element {
  return (
    <>
      <div class={'field' + (err ? ' bad' : '')} data-field="budget">
        <label for="w-m-budget">Up to, for this ticket <span class="req" aria-label="required">*</span></label>
        <div class="wl-money">
          <span aria-hidden="true">$</span>
          <input class="input mono" id="w-m-budget" data-money="budget" inputMode="decimal" autoComplete="off" placeholder="Amount" value={d.amount} onInput={(e) => set({ ...d, amount: (e.target as HTMLInputElement).value })} />
        </div>
        <div class="hint">Everything proposed under it counts until it’s denied, refused or expires.</div>
        <div class="ferr" role="alert">{err || ''}</div>
      </div>
      <div class="field" data-field="bcard">
        <label for="w-bcard">Only with this card <span class="opt">optional</span></label>
        <select class="select" id="w-bcard" value={d.card} onChange={(e) => set({ ...d, card: (e.target as HTMLSelectElement).value })}>
          <option value="">Any card it may use</option>
          {cards.map((c) => (
            <option value={c.name} key={c.name}>{c.name} · {c.brand || c.kind}</option>
          ))}
        </select>
        <div class="ferr" role="alert"></div>
      </div>
      <div class="field" data-field="bmerch">
        <span class="flabel">Only at these shops <span class="opt">optional</span></span>
        <div class="wl-rows" data-rows="bmerch">
          {d.merchants.map((m, i) => (
            <div class="wl-row" key={i}>
              <input class="input mono" data-row="bmerch" value={m} placeholder="https://shop.example.com" aria-label="A shop’s exact address" autoComplete="off" onInput={(e) => set({ ...d, merchants: d.merchants.map((x, j) => (j === i ? (e.target as HTMLInputElement).value : x)) })} />
              <button type="button" class="icon-btn" aria-label="Remove this shop" onClick={() => set({ ...d, merchants: d.merchants.filter((_, j) => j !== i) })}><Icon name="trash" /></button>
            </div>
          ))}
        </div>
        <button type="button" class="btn sm secondary wl-row-add" onClick={() => set({ ...d, merchants: [...d.merchants, ''] })}><Icon name="plus" />Add a shop</button>
        <div class="hint">Optional: none means any shop the card may pay.</div>
        <div class="ferr" role="alert"></div>
      </div>
    </>
  )
}

const host = (m: string) => m.replace(/^https?:\/\//, '')
const list = (xs: string[]) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`)

// TicketBudget is the Details rail's Spending limit panel, and its change form.
export function TicketBudget({ t, nm, onChanged }: { t: Ticket; nm: string; onChanged: () => void }): JSX.Element | null {
  const [open, setOpen] = useState(false)
  const [d, setD] = useState<SpendDraft>(emptySpend(t.spend_limit))
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const cards = useCards()
  const sl = t.spend_limit
  const bot = t.assignee?.startsWith('bot:')
  if (!sl && !t.assignee) return null

  async function apply(next: TicketSpendLimit | null) {
    setBusy(true)
    setErr('')
    try {
      await botsApi.patchTicket(t.ticket, { spend_limit: next })
      setOpen(false)
      toast(next ? 'Spending limit saved' : 'Spending limit removed', next ? `${money(Number(next.amount))} for this ticket` : 'purchases fall back to its own caps and the card’s', 'ok')
      onChanged()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  const modal = open ? (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true" data-testid="tk-budget-form">
        <div class="modal-head">
          <div>
            <div class="caps">{t.title}</div>
            <h2>{sl ? 'Change the spending limit' : 'Set a spending limit'}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={() => setOpen(false)}><Icon name="x" /></button>
        </div>
        <div class="modal-body stack" id="w-tkb">
          <SpendFields d={d} set={setD} cards={cards} err={err && /amount|address/.test(err) ? err : ''} />
          {err && !/amount|address/.test(err) ? <div class="callout err" role="alert">{err}</div> : null}
        </div>
        <div class="modal-foot">
          {sl ? <button class="btn ghost danger-text" type="button" style={{ marginRight: 'auto' }} disabled={busy} onClick={() => void apply(null)}>Remove the limit</button> : null}
          <button class="btn ghost" type="button" onClick={() => setOpen(false)}>Cancel</button>
          <button
            class="btn primary"
            type="button"
            disabled={busy}
            data-testid="tk-budget-save"
            onClick={() => {
              const r = readSpend(d, true)
              if (typeof r === 'string') return setErr(r)
              if (r) void apply(r)
            }}
          >
            Save
          </button>
        </div>
      </div>
    </div>
  ) : null

  if (!sl) {
    return (
      <section class="panel wl-tkb">
        <div class="panel-head"><h3>Spending limit</h3></div>
        <div class="panel-body">
          <p class="dl-sub">None: what {nm} buys for this ticket is held to {bot ? 'its' : 'their'} own caps and the card’s limits.</p>
          <button class="btn sm secondary" type="button" data-testid="tk-budget" onClick={() => { setD(emptySpend()); setErr(''); setOpen(true) }}><Icon name="plus" />Set a spending limit</button>
        </div>
        {modal}
      </section>
    )
  }
  const amt = Number(sl.amount)
  const spent = t.spent?.known ? Number(t.spent.spent || 0) : 0
  const left = Math.max(0, Math.round((amt - spent) * 100) / 100)
  const pct = amt > 0 ? Math.min(100, Math.round((spent / amt) * 100)) : 0
  return (
    <section class="panel wl-tkb">
      <div class="panel-head"><h3>Spending limit</h3><span class="dl-sp"></span><button class="btn sm secondary" type="button" data-testid="tk-budget" onClick={() => { setD(emptySpend(sl)); setErr(''); setOpen(true) }}>Change</button></div>
      <div class="panel-body">
        <p class="wl-tkb-big"><b>{money(left, sl.currency)}</b> left of {money(amt, sl.currency)}</p>
        <span class="wl-meter" role="img" aria-label={`${money(spent, sl.currency)} of ${money(amt, sl.currency)}`}><i style={{ width: `${pct}%` }} data-hi={pct >= 80 ? 1 : 0}></i></span>
        <p class="dl-sub">{t.spent?.known ? `${money(spent, sl.currency)} paid` : 'What’s been paid shows here once a purchase goes through'}. A denied or refused purchase frees its share; what’s left never moves to another ticket.</p>
        {sl.card || (sl.merchants || []).length ? (
          <p class="dl-sub">{sl.card ? `Only with ${sl.card}` : 'With any card it may use'}{(sl.merchants || []).length ? `, only at ${list((sl.merchants || []).map(host))}` : ''}.</p>
        ) : null}
        {bot ? null : <p class="dl-sub">It applies to {nm}.</p>}
        {t.spend_held ? <p class="wl-flag"><span>Held: {t.spend_held}</span></p> : null}
      </div>
      {modal}
    </section>
  )
}
