// Payment cards (design §4.5.1): every purchase waits for your approval, and the
// card never reaches a bot. A card shows its brand and last four only. Freeze is
// one click (it narrows); unfreeze, a new card, a shop or a higher limit widen,
// so they ask for your passkey. Spend limits stack and the lowest wins.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg, go } from '../router'
import {
  browser, walletOp, freezeCard, unfreezeCard, money, who,
  type WalletCard, type Purchase, type SpendSummary,
} from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs } from './Browsers'
import { srcWord } from './Details'
import { PurchaseRow } from './Purchase'

function kindWord(k: string): string {
  return k === 'virtual' ? 'Virtual card' : k === 'single-use' ? 'Single-use card' : k === 'physical' ? 'Physical card' : 'Card'
}

export function Cards(): JSX.Element {
  const s2 = seg(2)
  if (s2 === 'new') return <NewCard />
  if (s2) return <CardPage name={s2} />
  return <CardsList />
}

function CardsList(): JSX.Element {
  const [cards, setCards] = useState<WalletCard[] | null>(null)
  const [spend, setSpend] = useState<SpendSummary[]>([])
  const [pending, setPending] = useState<Purchase[]>([])
  const [done, setDone] = useState<Purchase[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  function load() {
    browser.wallet().then((r) => { setCards(r.cards || []); setSpend(r.spend || []) }).catch((e) => setErr(String(e)))
    browser.purchases().then((r) => {
      const all = r.purchases || []
      setPending(all.filter((p) => p.status === 'pending'))
      setDone(all.filter((p) => p.status !== 'pending'))
    }).catch(() => {})
  }
  useEffect(load, [])

  async function toggleFreeze(c: WalletCard) {
    setBusy(c.name)
    setErr('')
    try {
      if (c.frozen) await unfreezeCard(c.name)
      else await freezeCard(c.name)
      load()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy('')
    }
  }

  function spend24(c: WalletCard): { day: number; month: number } {
    const s = spend.find((x) => x.card === c.name && !x.identity && !x.bot && !x.team)
    return { day: s?.day || 0, month: s?.month || 0 }
  }

  return (
    <div class="col browsers">
      <div class="page-head">
        <h1 class="screen-title serif">Browsers</h1>
        <a class="btn primary sm" href="#/browser/cards/new">Add a card</a>
      </div>
      <p class="muted">Every purchase waits for your approval, and the card never reaches a bot.</p>
      <BrowserTabs active="cards" />
      {err ? <div class="banner err">{err}</div> : null}

      {pending.length > 0 ? (
        <section class="panel">
          <div class="panel-head"><span class="panel-title">Waiting for you</span><span class="count hot">{pending.length}</span></div>
          <ul class="dl-list">{pending.map((p) => <PurchaseRow key={p.id} p={p} />)}</ul>
        </section>
      ) : null}

      <section class="panel">
        <span class="panel-title">Cards</span>
        {cards === null ? (
          <p class="muted">Loading cards…</p>
        ) : cards.length === 0 ? (
          <div class="empty">
            <h3>No cards yet</h3>
            <p class="muted">Add a virtual or single-use card with its own limits; bots can then propose purchases with it for you to approve.</p>
          </div>
        ) : (
          <ul class="dl-list">
            {cards.map((c) => {
              const sp = spend24(c)
              const noShop = !c.merchants || c.merchants.length === 0
              return (
                <li class={'dl-conn wl-crow' + (c.frozen ? ' frozen' : '')} data-card={c.name}>
                  <a class="dl-conn-main" href={`#/browser/cards/${encodeURIComponent(c.name)}`}>
                    <b>{c.name}</b> <span class="dl-sub">{c.brand} •••• {c.last4}</span>
                    <p class="dl-why muted" data-testid="card-status">
                      {c.frozen
                        ? 'Frozen: nothing can be paid with it'
                        : noShop
                        ? 'No shop is allowed yet, so nothing can be bought with it'
                        : `${money(sp.day, c.limits.Currency)} of ${money(c.limits.PerDay, c.limits.Currency)} in 24 hours · ${money(sp.month, c.limits.Currency)} of ${money(c.limits.PerMonth, c.limits.Currency)} in 30 days`}
                    </p>
                    <p class="facts muted">
                      {kindWord(c.kind)} · Who: {(c.identities || []).map(who).join(', ') || 'Nobody yet'} · {(c.merchants || []).length} shop(s)
                      {c.method === 'config' ? ' · In the config' : ''}
                    </p>
                  </a>
                  <div class="row-actions">
                    <button class="btn ghost sm" type="button" disabled={busy === c.name} data-testid={c.frozen ? 'unfreeze' : 'freeze'} onClick={() => void toggleFreeze(c)}>
                      {busy === c.name ? '…' : c.frozen ? 'Unfreeze' : 'Freeze'}
                    </button>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      {done.length > 0 ? (
        <section class="panel">
          <div class="panel-head"><span class="panel-title">Purchases</span><span class="muted small">receipts and decisions</span></div>
          <ul class="dl-list">{done.map((p) => <PurchaseRow key={p.id} p={p} />)}</ul>
        </section>
      ) : null}
    </div>
  )
}

function CardPage({ name }: { name: string }): JSX.Element {
  const [c, setC] = useState<WalletCard | null>(null)
  const [spend, setSpend] = useState<SpendSummary[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  function load() {
    browser.wallet().then((r) => {
      setC((r.cards || []).find((x) => x.name === name) || null)
      setSpend(r.spend || [])
    }).catch((e) => setErr(String(e)))
  }
  useEffect(load, [name])

  if (err) return <div class="col"><a class="back" href="#/browser/cards">← Payment cards</a><div class="banner err">{err}</div></div>
  if (!c) return <div class="col"><p class="muted">Loading {name}…</p></div>
  const sp = spend.find((x) => x.card === c.name && !x.identity && !x.bot && !x.team)
  const cur = c.limits.Currency || 'USD'
  const editable = c.method !== 'config'

  async function toggleFreeze() {
    setBusy(true)
    try {
      if (c!.frozen) await unfreezeCard(name)
      else await freezeCard(name)
      load()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col browsers wl-card" data-card={name}>
      <a class="back" href="#/browser/cards">← Payment cards</a>
      <div class="page-head">
        <h1 class="screen-title serif">{name}</h1>
        <div class="row-actions">
          <button class="btn ghost sm" type="button" disabled={busy} data-testid={c.frozen ? 'unfreeze' : 'freeze'} onClick={() => void toggleFreeze()}>
            {c.frozen ? 'Unfreeze' : 'Freeze'}
          </button>
        </div>
      </div>
      <p class="muted">{c.brand} •••• {c.last4} · {kindWord(c.kind).toLowerCase()}{c.frozen ? ' · frozen' : ''}</p>
      {c.frozen ? <div class="callout warn" data-testid="frozen-banner">Frozen. Nothing is filled with it: no purchase can be proposed, approved or paid until you unfreeze it.</div> : null}
      {!editable ? <p class="callout info">Defined in the config (browser.cards.{name}). Its shops, limits and who may use it change there. Freezing works here too.</p> : null}

      <div class="dl-cols wl-cols">
        <section class="panel">
          <span class="panel-title">Spending</span>
          <div class="wl-limits">
            <p>A purchase — up to {money(c.limits.PerPurchase, cur)}</p>
            <Meter label="Last 24 hours" used={sp?.day || 0} cap={c.limits.PerDay} cur={cur} />
            <Meter label="Last 30 days" used={sp?.month || 0} cap={c.limits.PerMonth} cur={cur} />
          </div>
          <p class="muted small">Checked when a purchase is proposed, when you approve it, and again when it's paid.</p>
        </section>
        <section class="panel">
          <span class="panel-title">Shops it may pay</span>
          {(c.merchants || []).length === 0 ? (
            <p class="callout warn">None yet, so nothing can be bought with this card. Add the exact address of a shop you trust.</p>
          ) : (
            <ul class="dl-list">{(c.merchants || []).map((m) => (<li class="wl-shop">{m.replace(/^https?:\/\//, '')}</li>))}</ul>
          )}
        </section>
      </div>

      <section class="panel">
        <span class="panel-title">Who may use it</span>
        {(c.identities || []).length === 0 ? <p class="muted">Nobody yet.</p> : (
          <ul class="dl-list">{(c.identities || []).map((id) => (<li class="wl-who">{who(id)} <span class="dl-sub mono">{id}</span></li>))}</ul>
        )}
      </section>

      <section class="panel">
        <div class="panel-head"><span class="panel-title">The card's details</span><span class="muted small">never shown, never sent to a bot</span></div>
        <table class="wl-fields">
          <tbody>
            {['number', 'expiry', 'cvc', 'name'].map((f) => {
              const fv = c.fields && c.fields[f]
              const label = f === 'number' ? 'Number' : f === 'expiry' ? 'Expiry' : f === 'cvc' ? 'Security code' : 'Name on the card'
              return (
                <tr data-field={f}>
                  <td>{label}</td>
                  <td>{fv ? (<><span class="wl-masked">{f === 'number' ? `•••• ${c.last4}` : '••••'}</span> <span class="dl-sub">set</span></>) : <span class="dl-sub">Not set</span>}</td>
                  <td class="wl-src">{fv ? srcWord(fv) : ''}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </section>
    </div>
  )
}

function Meter({ label, used, cap, cur }: { label: string; used: number; cap: number; cur: string }): JSX.Element {
  const pct = cap > 0 ? Math.min(100, Math.round((used / cap) * 100)) : 0
  return (
    <div class="wl-lrow">
      <span class="wl-ll">{label}</span>
      <span class="wl-lv">{money(used, cur)} of {money(cap, cur)}</span>
      <div class="wl-meter"><span style={`width:${pct}%`} data-hi={pct >= 80 ? '1' : undefined} /></div>
    </div>
  )
}

function NewCard(): JSX.Element {
  const [name, setName] = useState('')
  const [kind, setKind] = useState('virtual')
  const [number, setNumber] = useState<VSValue>({ ...emptyVS() })
  const [expiry, setExpiry] = useState<VSValue>({ ...emptyVS() })
  const [cvc, setCvc] = useState<VSValue>({ ...emptyVS() })
  const [pp, setPP] = useState('50')
  const [pd, setPD] = useState('100')
  const [pm, setPM] = useState('300')
  const [shop, setShop] = useState('')
  const [bind, setBind] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  async function save() {
    setBusy(true)
    setErr('')
    try {
      const values: Record<string, string> = {}
      const refs: Record<string, string> = {}
      const put = (f: string, v: VSValue) => {
        const w = vsToWire(v)
        if (w.value) values[f] = w.value
        if (w.ref) refs[f] = w.ref
      }
      put('number', number)
      put('expiry', expiry)
      if (cvc.value || cvc.ref) put('cvc', cvc)
      await walletOp('browser-card-add', {
        name,
        kind,
        merchants: shop ? [shop] : [],
        identities: bind.split(',').map((s) => s.trim()).filter(Boolean),
        limits: { per_purchase: Number(pp), per_day: Number(pd), per_month: Number(pm), currency: 'USD' },
        values,
        refs,
      })
      go(`browser/cards/${encodeURIComponent(name)}`)
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="col browsers">
      <a class="back" href="#/browser/cards">← Payment cards</a>
      <h1 class="screen-title serif">Add a card</h1>
      <p class="callout info">Use a limited card. A virtual or single-use card with its own limits is the second layer behind conductor's checks.</p>
      {err ? <div class="banner err">{err}</div> : null}
      <section class="panel">
        <label class="form-field"><span>Name for this card</span>
          <input value={name} placeholder="household" onInput={(e) => setName((e.target as HTMLInputElement).value)} /></label>
        <div class="form-field"><span>Kind</span>
          <div class="seg">
            {['virtual', 'single-use', 'physical'].map((k) => (
              <button type="button" class={'seg-btn' + (kind === k ? ' on' : '')} onClick={() => setKind(k)}>{k}</button>
            ))}
          </div>
          <span class="muted small">{kind === 'physical' ? 'A physical card works, but conductor will remind you to prefer a virtual or single-use one.' : kind === 'single-use' ? 'A single-use card: good for one purchase.' : 'A virtual card with its own limits: good.'}</span>
        </div>
        <h3 class="subhead">The card</h3>
        <p class="muted">Each typed once (stored write-only) or from a vault item. The page will only ever show the last four digits.</p>
        <ValueSource label="Card number" sensitive credential value={number} onChange={setNumber} testid="card-number" />
        <ValueSource label="Expiry (MM/YY)" sensitive credential value={expiry} onChange={setExpiry} testid="card-expiry" />
        <ValueSource label="Security code" sensitive credential value={cvc} onChange={setCvc} testid="card-cvc" />
        <h3 class="subhead">Limits</h3>
        <p class="muted">In US dollars. A purchase fits inside a day, and a day inside 30 days.</p>
        <div class="wl-grid3">
          <label class="form-field"><span>A purchase</span><input type="number" value={pp} onInput={(e) => setPP((e.target as HTMLInputElement).value)} /></label>
          <label class="form-field"><span>A day (24 hours)</span><input type="number" value={pd} onInput={(e) => setPD((e.target as HTMLInputElement).value)} /></label>
          <label class="form-field"><span>30 days</span><input type="number" value={pm} onInput={(e) => setPM((e.target as HTMLInputElement).value)} /></label>
        </div>
        <h3 class="subhead">Where, and who</h3>
        <label class="form-field"><span>A shop it may pay</span>
          <input value={shop} placeholder="https://shop.example.com" onInput={(e) => setShop((e.target as HTMLInputElement).value)} />
          <span class="muted small">None by default: until you add one, nothing can be bought with it.</span></label>
        <label class="form-field"><span>Who may use it</span>
          <input value={bind} placeholder="bot:shopper, team:platform" onInput={(e) => setBind((e.target as HTMLInputElement).value)} /></label>
        <div class="row-actions">
          <button class="btn primary" type="button" disabled={busy || !name} onClick={() => void save()}>
            {busy ? 'Confirming…' : 'Save…'}
          </button>
        </div>
      </section>
    </div>
  )
}
