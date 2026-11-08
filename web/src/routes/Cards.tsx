// Payment cards (design §4.5.1): every purchase waits for your approval, and the
// card never reaches a bot. A card shows its brand and last four only. Freeze is
// one click (it narrows); unfreeze, a new card, a shop or a higher limit widen,
// so they ask for your passkey. Spend limits stack and the lowest wins.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg, go } from '../router'
import {
  browser, walletOp, freezeCard, unfreezeCard, money, who, kindWord,
  type WalletCard, type Purchase, type SpendSummary,
} from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs, BrowserCrumbs } from './Browsers'
import { srcWord } from './Details'
import { PurchaseRow } from './Purchase'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { humanError } from '../api/errors'

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
  const [approvers, setApprovers] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  function load() {
    browser.wallet().then((r) => { setCards(r.cards || []); setSpend(r.spend || []) }).catch((e) => setErr(humanError(e)))
    browser.purchases().then((r) => {
      const all = r.purchases || []
      setPending(all.filter((p) => p.status === 'pending'))
      setDone(all.filter((p) => p.status !== 'pending'))
      setApprovers(r.approvers || [])
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
      setErr(humanError(e))
    } finally {
      setBusy('')
    }
  }

  function spend24(c: WalletCard): { day: number; month: number } {
    const s = spend.find((x) => x.card === c.name && !x.identity && !x.bot && !x.team)
    return { day: s?.day || 0, month: s?.month || 0 }
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrowserCrumbs here="Payment cards" />
          <h1>Payment cards</h1>
          <p class="lede-2">Every purchase waits for your approval, and the card never reaches a bot.</p>
        </div>
        {/* With no card yet the empty state below carries the one Add a card. */}
        {cards === null || cards.length ? (
          <div class="actions">
            <a class="btn primary" href="#/browser/cards/new"><Icon name="plus" />Add a card</a>
          </div>
        ) : null}
      </div>
      <BrowserTabs active="cards" />
      {err ? <div class="banner err">{err}</div> : null}

      {pending.length > 0 ? (
        <section class="dl-card wl-gapb">
          <header class="dl-ch"><h2>Waiting for you</h2><span class="count hot">{pending.length}</span></header>
          <ul class="dl-list dl-needs">{pending.map((p) => <PurchaseRow key={p.id} p={p} />)}</ul>
        </section>
      ) : null}

      <section class="dl-card">
        <header class="dl-ch"><h2>Cards</h2></header>
        {cards === null ? (
          <p class="dl-sub" style="padding:0 var(--pad-card) var(--pad-card)">{err ? 'Nothing to show until conductor answers.' : 'Loading cards…'}</p>
        ) : cards.length === 0 ? (
          <EmptyState hero icon="obj-card" title="No cards yet" testid="cards-empty" action={{ label: 'Add a card', href: '#/browser/cards/new', testid: 'cards-empty-add' }}>
            <p>Add a virtual or single-use card with its own limits; bots can then propose purchases with it for you to approve.</p>
          </EmptyState>
        ) : (
          <ul class="dl-list dl-conns wl-flushl">
            {cards.map((c) => {
              const sp = spend24(c)
              const noShop = !c.merchants || c.merchants.length === 0
              const attn = !c.frozen && noShop
              return (
                <li class={'dl-conn wl-crow' + (attn ? ' attn' : '')} data-s={c.frozen ? 'idle' : 'wait'} data-card={c.name} key={c.name}>
                  <span class="dl-ng">{c.frozen ? <span class="wl-ice" role="img" aria-label="Frozen"><Icon name="freeze" /></span> : <Icon name="obj-card" />}</span>
                  <span class="dl-nt">
                    <b><a class="dl-link-q" href={`#/browser/cards/${encodeURIComponent(c.name)}`}>{c.name}</a> <span class="tgt">{c.brand} •••• {c.last4}</span></b>
                    <span class={attn || c.frozen ? 'reason' : 'dl-why'} data-testid="card-status">
                      {c.frozen
                        ? 'Frozen: nothing can be paid with it'
                        : noShop
                        ? 'No shop is allowed yet, so nothing can be bought with it'
                        : `${money(sp.day, c.limits.Currency)} of ${money(c.limits.PerDay, c.limits.Currency)} in 24 hours · ${money(sp.month, c.limits.Currency)} of ${money(c.limits.PerMonth, c.limits.Currency)} in 30 days`}
                    </span>
                    <span class="facts muted">
                      {kindWord(c.kind)} · {(c.identities || []).map(who).join(', ') || 'Nobody yet'} · {(c.merchants || []).length} shop{(c.merchants || []).length === 1 ? '' : 's'}
                      {c.method === 'config' ? ' · In the config' : ''}
                    </span>
                  </span>
                  <span class="dl-na">
                    <button class="btn sm secondary" type="button" disabled={busy === c.name} data-testid={c.frozen ? 'unfreeze' : 'freeze'} onClick={() => void toggleFreeze(c)}>
                      <Icon name={c.frozen ? 'undo' : 'freeze'} />
                      {busy === c.name ? '…' : c.frozen ? 'Unfreeze' : 'Freeze'}
                    </button>
                    <a class="btn sm secondary" href={`#/browser/cards/${encodeURIComponent(c.name)}`}>Open</a>
                  </span>
                </li>
              )
            })}
          </ul>
        )}
        <p class="dl-sub wl-foot">
          <Icon name="shield" />{' '}
          {approvers.length ? (
            <>{approvers.join(', ')} approve{approvers.length === 1 ? 's' : ''} purchases, each with a passkey. An approval is used once, within an hour.</>
          ) : (
            <><b>Nobody can approve a purchase yet</b> (no operator has a key), so every purchase will wait.</>
          )}{' '}
          Pause all automation also stops every payment.
        </p>
      </section>

      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>Purchases</h2><span class="dl-sub">receipts and decisions</span></header>
        {done.length > 0 ? (
          <ul class="dl-list dl-needs">{done.map((p) => <PurchaseRow key={p.id} p={p} />)}</ul>
        ) : (
          <p class="dl-sub" style="padding:0 var(--pad-card) var(--pad-card)">Nothing bought yet.</p>
        )}
      </section>
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
    }).catch((e) => setErr(humanError(e)))
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
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="wl-page" data-card={name}>
      <div class="page-head">
        <div>
          <a class="back" href="#/browser/cards">← Payment cards</a>
          <h1>{name}</h1>
          <p class="sub">{c.brand} •••• {c.last4} · {kindWord(c.kind).toLowerCase()}{c.frozen ? ' · frozen' : ''}</p>
        </div>
        <div class="actions">
          <button class="btn secondary" type="button" disabled={busy} data-testid={c.frozen ? 'unfreeze' : 'freeze'} onClick={() => void toggleFreeze()}>
            <Icon name={c.frozen ? 'undo' : 'freeze'} />{c.frozen ? 'Unfreeze' : 'Freeze'}
          </button>
        </div>
      </div>
      {c.frozen ? (
        <section class="dl-card wl-frozen" data-testid="frozen-banner">
          <p><span class="wl-ice" aria-hidden="true"><Icon name="freeze" /></span><span><b>Frozen.</b> Nothing is filled with it: no purchase can be proposed, approved or paid until you unfreeze it.</span></p>
        </section>
      ) : null}
      {!editable ? (
        <div class="wl-cfg callout small">
          <Icon name="config" />
          <span><b>Defined in the config</b> (browser.cards.{name}), so its shops, limits and who may use it change there. Freezing works here too.</span>
        </div>
      ) : null}

      <div class="dl-cols wl-cols">
        <section class="dl-card">
          <header class="dl-ch"><h2>Spending</h2></header>
          <div class="wl-limits">
            <div class="wl-lrow"><span class="wl-ll">A purchase</span><span class="wl-lv">up to <b>{money(c.limits.PerPurchase, cur)}</b></span></div>
            <Meter label="Last 24 hours" used={sp?.day || 0} cap={c.limits.PerDay} cur={cur} />
            <Meter label="Last 30 days" used={sp?.month || 0} cap={c.limits.PerMonth} cur={cur} />
          </div>
          <p class="dl-sub">Checked when a purchase is proposed, when you approve it, and again when it's paid.</p>
        </section>
        <section class="dl-card">
          <header class="dl-ch"><h2>Shops it may pay</h2></header>
          {(c.merchants || []).length === 0 ? (
            <p class="wl-empty"><StatusMark s="wait" mkOnly /><span>None yet, so nothing can be bought with this card. Add the exact address of a shop you trust.</span></p>
          ) : (
            <ul class="dl-list dl-conns wl-flushl">
              {(c.merchants || []).map((m) => (
                <li class="dl-conn" key={m}>
                  <span class="dl-ng"><Icon name="store" /></span>
                  <span class="dl-nt"><b>{m.replace(/^https?:\/\//, '')}</b></span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <section class="dl-card">
        <header class="dl-ch"><h2>Who may use it</h2></header>
        {(c.identities || []).length === 0 ? <p class="dl-sub" style="padding:0 var(--pad-card) var(--pad-card)">Nobody yet.</p> : (
          <ul class="dl-list dl-conns wl-flushl">
            {(c.identities || []).map((id) => (
              <li class="dl-conn" key={id}>
                <span class="dl-ng"><Icon name={id.startsWith('team:') ? 'users' : 'bots'} /></span>
                <span class="dl-nt"><b>{who(id)}</b><span class="dl-why mono">{id}</span></span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>The card's details</h2><span class="dl-sub">never shown, never sent to a bot</span></header>
        <ul class="dl-list dl-conns wl-flushl">
          {['number', 'expiry', 'cvc', 'name'].map((f) => {
            const fv = c.fields && c.fields[f]
            const label = f === 'number' ? 'Number' : f === 'expiry' ? 'Expiry' : f === 'cvc' ? 'Security code' : 'Name on the card'
            return (
              <li class="dl-conn" key={f}>
                <span class="dl-ng"><Icon name="lock" /></span>
                <span class="dl-nt">
                  <b>{label}</b>
                  <span class="dl-why">{fv ? (<><span class="wl-masked">{f === 'number' ? `•••• ${c.last4}` : '••••'}</span> set · {srcWord(fv)}</>) : 'Not set'}</span>
                </span>
              </li>
            )
          })}
        </ul>
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
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <a class="back" href="#/browser/cards">← Payment cards</a>
          <h1>Add a card</h1>
        </div>
      </div>
      <p class="callout small"><Icon name="shield" />Use a limited card. A virtual or single-use card with its own limits is the second layer behind conductor's checks.</p>
      {err ? <div class="banner err">{err}</div> : null}
      <div class="dl-card wl-form">
        <label class="form-field"><span>Name for this card</span>
          <input class="input" value={name} placeholder="household" onInput={(e) => setName((e.target as HTMLInputElement).value)} /></label>
        <div class="form-field"><span class="flabel">Kind</span>
          <div class="seg">
            {['virtual', 'single-use', 'physical'].map((k) => (
              <button type="button" class={kind === k ? 'on' : ''} aria-pressed={kind === k} onClick={() => setKind(k)} key={k}>{k}</button>
            ))}
          </div>
          <span class="dl-sub">{kind === 'physical' ? 'A physical card works, but conductor will remind you to prefer a virtual or single-use one.' : kind === 'single-use' ? 'A single-use card: good for one purchase.' : 'A virtual card with its own limits: good.'}</span>
        </div>
        <h3 class="wl-h">The card</h3>
        <p class="dl-sub">Each typed once (stored write-only) or from a vault item. The page will only ever show the last four digits.</p>
        <ValueSource label="Card number" sensitive credential value={number} onChange={setNumber} testid="card-number" />
        <ValueSource label="Expiry (MM/YY)" sensitive credential value={expiry} onChange={setExpiry} testid="card-expiry" />
        <ValueSource label="Security code" sensitive credential value={cvc} onChange={setCvc} testid="card-cvc" />
        <h3 class="wl-h">Limits</h3>
        <p class="dl-sub">In US dollars. A purchase fits inside a day, and a day inside 30 days.</p>
        <div class="wl-grid3">
          <label class="form-field"><span>A purchase</span><input class="input" type="number" value={pp} onInput={(e) => setPP((e.target as HTMLInputElement).value)} /></label>
          <label class="form-field"><span>A day (24 hours)</span><input class="input" type="number" value={pd} onInput={(e) => setPD((e.target as HTMLInputElement).value)} /></label>
          <label class="form-field"><span>30 days</span><input class="input" type="number" value={pm} onInput={(e) => setPM((e.target as HTMLInputElement).value)} /></label>
        </div>
        <h3 class="wl-h">Where, and who</h3>
        <label class="form-field"><span>A shop it may pay</span>
          <input class="input" value={shop} placeholder="https://shop.example.com" onInput={(e) => setShop((e.target as HTMLInputElement).value)} />
          <span class="dl-sub">None by default: until you add one, nothing can be bought with it.</span></label>
        <label class="form-field"><span>Who may use it</span>
          <input class="input" value={bind} placeholder="bot:shopper, team:platform" onInput={(e) => setBind((e.target as HTMLInputElement).value)} /></label>
        <div class="row-actions">
          <button class="btn primary" type="button" disabled={busy || !name} onClick={() => void save()}>
            {busy ? 'Confirming…' : 'Save…'}
          </button>
        </div>
      </div>
    </div>
  )
}
