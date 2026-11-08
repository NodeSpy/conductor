// Payment cards (js/screens/wallet.js cardsPage/cardPage/newCardPage): every
// purchase waits for your approval, and the card never reaches a bot. A card
// shows its brand and last four only. Freeze is one click (it narrows);
// unfreeze, a new card, a shop, a bot or a higher limit widen, so the daemon
// asks for your passkey. Spend limits stack and the lowest wins.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { seg, go } from '../router'
import { browser, walletOp, freezeCard, unfreezeCard, type WalletCard, type Purchase, type SpendSummary, type CardLimits } from '../api/browser'
import { ValueSource, vsToWire, emptyVS, type VSValue } from '../components/ValueSource'
import { BrowserTabs } from './Browsers'
import { PurchaseRow } from './Purchase'
import { EmptyState } from '../components/EmptyState'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FactsLine } from '../components/Facts'
import { Menu } from '../components/Menu'
import { ConfirmModal, FormModal } from '../components/FormModal'
import { BrModal, BrCrumbs, Field, MoneyInput, WhoPicker, moneyOf, off } from '../components/BrowserFields'
import { agoText, host, idOptions, listWords, loadRoster, originErr, roster, whoWord } from '../lib/browserwords'
import { CARDF, cardName, kindOf, srcWord, usd } from '../lib/walletwords'
import { status } from '../state'
import { humanError } from '../api/errors'

export function Cards(): JSX.Element {
  const s2 = seg(2)
  if (s2 === 'new') return <NewCard />
  if (s2) return <CardPage name={s2} />
  return <CardsList />
}

const CFG_ONLY = (n: string) => `${n} is defined in the config: change it there`
/** validate's reminders about a card (WalletNotes), in words. */
const notes = (c: WalletCard): string[] =>
  [
    c.kind === 'physical' || !c.kind ? 'A physical card: prefer a virtual or single-use card with its own provider limits, the second layer behind conductor’s own checks.' : '',
    !(c.merchants || []).length ? 'No shop is allowed yet, so nothing can be bought with it.' : '',
  ].filter(Boolean)
const frozenWord = (c: WalletCard): string => (c.frozen ? `Frozen${c.frozen_at ? ` ${agoText(c.frozen_at)}` : ' in the config'}` : '')
const spentOf = (spend: SpendSummary[], card: string, identity = ''): { day: number; month: number } => {
  const s = spend.find((x) => x.card === card && (identity ? x.identity === identity || (identity.startsWith('bot:') && x.bot === identity.slice(4)) || (identity.startsWith('team:') && x.team === identity.slice(5)) : !x.identity && !x.bot && !x.team))
  return { day: s?.day || 0, month: s?.month || 0 }
}
const cur = (c: WalletCard) => c.limits.Currency || 'USD'

/** Freeze is one click; unfreeze asks for your passkey, and a card frozen in the config unfreezes there. */
function FreezeButton({ c, sm, onDone, onErr }: { c: WalletCard; sm?: boolean; onDone: () => void; onErr: (e: string) => void }): JSX.Element {
  const [busy, setBusy] = useState(false)
  const cfgFrozen = c.frozen && !c.frozen_at
  async function run() {
    if (cfgFrozen || busy) return
    setBusy(true)
    try {
      if (c.frozen) await unfreezeCard(c.name)
      else await freezeCard(c.name)
      onDone()
    } catch (e) {
      onErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }
  return c.frozen ? (
    <button class={`btn ${sm ? 'sm ' : ''}secondary`} type="button" data-testid="unfreeze" disabled={busy} {...off(cfgFrozen && 'Frozen in the config (frozen: true): unfreeze it there')} onClick={() => void run()}>
      <Icon name="passkey" />Unfreeze
    </button>
  ) : (
    <button class={`btn ${sm ? 'sm ' : ''}secondary`} type="button" data-testid="freeze" disabled={busy} onClick={() => void run()}>
      <Icon name="freeze" />Freeze
    </button>
  )
}

function CardsList(): JSX.Element {
  const [cards, setCards] = useState<WalletCard[] | null>(null)
  const [spend, setSpend] = useState<SpendSummary[]>([])
  const [all, setAll] = useState<Purchase[]>([])
  const [approvers, setApprovers] = useState<string[]>([])
  const [err, setErr] = useState('')
  useEffect(loadRoster, [])
  void roster.value

  function load() {
    browser.wallet().then((r) => { setCards(r.cards || []); setSpend(r.spend || []) }).catch((e) => setErr(humanError(e)))
    browser.purchases().then((r) => { setAll((r.purchases || []).slice().sort((a, b) => Date.parse(b.created) - Date.parse(a.created))); setApprovers(r.approvers || []) }).catch(() => {})
  }
  useEffect(load, [])
  const cardOf = (n: string) => (cards || []).find((c) => c.name === n) || null
  const wait = all.filter((p) => p.status === 'pending' && !(p.expires && Date.parse(p.expires) < Date.now()))
  const past = all.filter((p) => !wait.includes(p))
  const none = cards !== null && cards.length === 0

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Browsers', '#/browser'], ['Payment cards']]} />
          <h1>Payment cards</h1>
          <p class="lede-2">Every purchase waits for your approval, and the card never reaches a bot.</p>
        </div>
        {/* With no card yet the hero below carries the one Add a card. */}
        {none ? null : (
          <div class="actions">
            <a class="btn primary" href="#/browser/cards/new"><Icon name="plus" />Add a card</a>
          </div>
        )}
      </div>
      <BrowserTabs active="cards" />
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      {status.value?.paused ? (
        <div class="callout warn small wl-gapb"><Icon name="pause" /><span><b>All automation is paused,</b> so nothing is paid until you resume it. <a class="dl-link" href="#/system">Safety</a></span></div>
      ) : null}

      {wait.length ? (
        <section class="dl-card wl-gapb">
          <header class="dl-ch"><h2>Waiting for you</h2><span class="count hot">{wait.length}</span></header>
          <ul class="dl-list dl-needs">{wait.map((p) => <PurchaseRow key={p.id} p={p} card={cardOf(p.card)} />)}</ul>
        </section>
      ) : null}

      <section class="dl-card">
        <header class="dl-ch"><h2>Cards</h2></header>
        {cards === null ? (
          <p class="dl-sub">{err ? 'Nothing to show until conductor answers.' : 'Loading cards…'}</p>
        ) : cards.length === 0 ? (
          <EmptyState hero icon="obj-card" title="No cards yet" testid="cards-empty" action={{ label: 'Add a card', href: '#/browser/cards/new', testid: 'cards-empty-add' }}>
            <p>Add a virtual or single-use card with its own limits; bots can then propose purchases with it for you to approve.</p>
          </EmptyState>
        ) : (
          <ul class="dl-list dl-conns wl-flushl">
            {cards.map((c) => {
              const L = c.limits
              const s = spentOf(spend, c.name)
              const ns = notes(c)
              const att = !c.frozen && ns.length > 0
              return (
                <li class={'dl-conn wl-crow' + (att ? ' attn' : '')} data-s={c.frozen ? 'idle' : 'wait'} data-card={c.name} key={c.name}>
                  <span class="dl-ng">
                    {c.frozen ? <span class="wl-ice" role="img" aria-label="Frozen"><Icon name="freeze" /></span> : ns.length ? <StatusMark s="wait" mkOnly lg label="Needs a look" /> : <Icon name="obj-card" />}
                  </span>
                  <span class="dl-nt">
                    <b><a class="dl-link-q" href={`#/browser/cards/${encodeURIComponent(c.name)}`}>{c.name}</a> <span class="tgt">{cardName(c)}</span></b>
                    <span class={att || c.frozen ? 'reason' : 'dl-why'} data-testid="card-status">
                      {c.frozen
                        ? `${frozenWord(c)}: nothing can be paid with it`
                        : ns.length
                        ? ns[ns.length - 1]
                        : `${usd(s.day, cur(c))} of ${usd(L.PerDay, cur(c))} in 24 hours · ${usd(s.month, cur(c))} of ${usd(L.PerMonth, cur(c))} in 30 days`}
                    </span>
                    <FactsLine
                      facts={[
                        { icon: 'obj-card', value: kindOf(c.kind), pri: 2, label: 'Kind' },
                        { icon: 'users', value: listWords((c.identities || []).map(whoWord)) || 'Nobody yet', pri: 1, label: 'Who may use it' },
                        { icon: 'store', value: `${(c.merchants || []).length} shop${(c.merchants || []).length === 1 ? '' : 's'}`, pri: 2, label: 'Shops it may pay' },
                        c.method === 'config' ? { icon: 'obj-config', value: 'In the config', pri: 3, label: 'Where it’s defined' } : null,
                      ]}
                    />
                  </span>
                  <span class="dl-na">
                    <FreezeButton c={c} sm onDone={load} onErr={setErr} />
                    <a class="btn sm secondary" href={`#/browser/cards/${encodeURIComponent(c.name)}`}>Open</a>
                  </span>
                </li>
              )
            })}
          </ul>
        )}
        <p class="dl-sub wl-foot">
          <Icon name="shield" />{' '}
          {/* one text box beside the icon, so the bold lead never becomes its own narrow flex column on a phone */}
          <span>
          {approvers.length ? (
            <>{listWords(approvers)} approve{approvers.length === 1 ? 's' : ''} purchases, each with a passkey. An approval is used once, within an hour.</>
          ) : (
            <><b>Nobody can approve a purchase yet</b> (no operator has a key), so every purchase will wait.</>
          )}{' '}
          Pause all automation also stops every payment.
          </span>
        </p>
      </section>

      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>Purchases</h2><span class="dl-sub">receipts and decisions</span></header>
        {past.length ? <ul class="dl-list dl-needs">{past.map((p) => <PurchaseRow key={p.id} p={p} card={cardOf(p.card)} />)}</ul> : <p class="dl-sub">Nothing bought yet.</p>}
      </section>
    </div>
  )
}

const LIMF: [keyof CardLimits, string][] = [['PerPurchase', 'A purchase'], ['PerDay', 'A day (24 hours)'], ['PerMonth', '30 days']]

type CardModal =
  | { k: 'limits' }
  | { k: 'shop' }
  | { k: 'shop-rm'; o: string }
  | { k: 'who' }
  | { k: 'who-rm'; id: string }
  | { k: 'set'; f: string }
  | { k: 'remove' }
  | null

function CardPage({ name }: { name: string }): JSX.Element {
  const [c, setC] = useState<WalletCard | null | undefined>(undefined)
  const [spend, setSpend] = useState<SpendSummary[]>([])
  const [ps, setPs] = useState<Purchase[]>([])
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const [modal, setModal] = useState<CardModal>(null)
  const [menu, setMenu] = useState<HTMLElement | null>(null)
  useEffect(loadRoster, [])
  void roster.value

  function load() {
    browser.wallet().then((r) => { setC((r.cards || []).find((x) => x.name === name) || null); setSpend(r.spend || []) }).catch((e) => setErr(humanError(e)))
    browser.purchases().then((r) => setPs((r.purchases || []).filter((p) => p.card === name).sort((a, b) => Date.parse(b.created) - Date.parse(a.created)))).catch(() => {})
  }
  useEffect(load, [name])
  const done = (msg: string) => { setModal(null); setOk(msg); load() }
  async function op(cmd: string, req: Record<string, unknown>, msg: string) {
    setErr('')
    try {
      await walletOp(cmd, { name, ...req })
      done(msg)
    } catch (e) {
      setModal(null)
      setErr(humanError(e))
    }
  }

  const crumbs = <BrCrumbs trail={[['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], [name]]} />
  if (c === undefined) return <div>{crumbs}<p class="muted">{err || `Loading ${name}…`}</p></div>
  if (c === null) return <div>{crumbs}<EmptyState icon="obj-card" title="No such card" action={{ label: 'Payment cards', href: '#/browser/cards' }}><p>{name} isn’t in the wallet any more.</p></EmptyState></div>
  const ed = c.editable !== false && c.method !== 'config'
  const L = c.limits
  const cr = cur(c)
  const s = spentOf(spend, c.name)
  const ns = notes(c)
  const idl = Object.entries(c.identity_limits || {})
  const cfgOff = ed ? {} : off(CFG_ONLY(c.name))
  const act = (m: CardModal) => () => (ed ? setModal(m) : undefined)
  const lim = (lbl: string, used: number, cap: number) => {
    const pct = cap ? Math.min(100, Math.round((used / cap) * 100)) : 0
    return (
      <div class="wl-lrow">
        <span class="wl-ll">{lbl}</span>
        <span class="wl-lv"><b>{usd(used, cr)}</b> of {usd(cap, cr)}</span>
        <span class="wl-meter" role="img" aria-label={`${usd(used, cr)} of ${usd(cap, cr)}`}><i style={`width:${pct}%`} data-hi={pct >= 80 ? 1 : 0} /></span>
      </div>
    )
  }

  return (
    <div class="wl-page" data-card={name}>
      <div class="page-head">
        <div>
          {crumbs}
          <h1>{c.name}</h1>
          <p class="sub">{cardName(c)} · {kindOf(c.kind).toLowerCase()}{c.frozen ? ' · frozen' : ''}</p>
        </div>
        <div class="actions">
          <FreezeButton c={c} onDone={load} onErr={setErr} />
          <button class="icon-btn ph-more" type="button" aria-label={`More for ${c.name}`} data-testid="card-more" onClick={(e) => setMenu(e.currentTarget as HTMLElement)}><Icon name="more" /></button>
        </div>
      </div>
      {err ? <div class="callout danger small" role="alert">{err}</div> : null}
      {ok ? <div class="callout ok small" role="status">{ok}</div> : null}
      {c.frozen ? (
        <section class="dl-card wl-frozen" data-testid="frozen-banner">
          <p><span class="wl-ice" aria-hidden="true"><Icon name="freeze" /></span><span><b>{frozenWord(c)}.</b> Nothing is filled with it: no purchase can be proposed, approved or paid until you unfreeze it.{c.frozen_at ? '' : ' It’s frozen in the config (frozen: true), so it unfreezes there.'}</span></p>
        </section>
      ) : null}
      {!ed ? (
        <div class="callout small wl-cfg"><Icon name="obj-config" /><span><b>Defined in the config</b> (<code>browser.cards.{c.name}</code>), so its shops, limits and who may use it change there. Freezing works here too. <a class="dl-link" href="#/system/config">Open Config</a></span></div>
      ) : null}
      {ns.map((n) => <div class="callout warn small wl-note" key={n}><StatusMark s="wait" mkOnly /><span>{n}</span></div>)}

      <div class="dl-cols wl-cols">
        <section class="dl-card">
          <header class="dl-ch"><h2>Spending</h2><span class="dl-sp" /><button class="btn sm secondary" type="button" data-testid="card-limits" {...cfgOff} onClick={act({ k: 'limits' })}><Icon name="edit" />Change limits</button></header>
          <div class="wl-limits">
            <div class="wl-lrow"><span class="wl-ll">A purchase</span><span class="wl-lv">up to <b>{usd(L.PerPurchase, cr)}</b></span></div>
            {lim('Last 24 hours', s.day, L.PerDay)}
            {lim('Last 30 days', s.month, L.PerMonth)}
          </div>
          {idl.map(([id, l]) => {
            const si = spentOf(spend, c.name, id)
            return (
              <div class="wl-limits wl-idl" key={id}>
                <h3>{whoWord(id)}</h3>
                <div class="wl-lrow"><span class="wl-ll">A purchase</span><span class="wl-lv">up to <b>{usd(l.PerPurchase, cr)}</b></span></div>
                {lim('Last 24 hours', si.day, l.PerDay)}
                {lim('Last 30 days', si.month, l.PerMonth)}
              </div>
            )
          })}
          <p class="dl-sub">Checked when a purchase is proposed, when you approve it, and again when it’s paid.</p>
        </section>
        <div class="stack wl-side">
          <section class="dl-card">
            <header class="dl-ch"><h2>Shops it may pay</h2><span class="dl-sp" /><button class="btn sm secondary" type="button" data-testid="card-shop-add" {...cfgOff} onClick={act({ k: 'shop' })}><Icon name="plus" />Add a shop</button></header>
            {(c.merchants || []).length ? (
              <ul class="dl-list dl-conns wl-flushl">
                {(c.merchants || []).map((o) => (
                  <li class="dl-conn" key={o}>
                    <span class="dl-ng"><Icon name="store" /></span>
                    <span class="dl-nt"><b>{host(o)}</b>{/^https:\/\/[^:]+$/.test(o) ? null : <span class="dl-why mono">{o}</span>}</span>
                    <span class="dl-na"><button class="btn sm secondary" type="button" {...cfgOff} onClick={act({ k: 'shop-rm', o })}>Remove</button></span>
                  </li>
                ))}
              </ul>
            ) : (
              <p class="wl-empty"><StatusMark s="wait" mkOnly /><span>None yet, so nothing can be bought with this card. Add the exact address of a shop you trust.</span></p>
            )}
          </section>
          <section class="dl-card">
            <header class="dl-ch"><h2>Who may use it</h2><span class="dl-sp" /><button class="btn sm secondary" type="button" data-testid="card-who-add" {...cfgOff} onClick={act({ k: 'who' })}><Icon name="plus" />Add</button></header>
            {(c.identities || []).length ? (
              <ul class="dl-list dl-conns wl-flushl">
                {(c.identities || []).map((id) => {
                  const l = (c.identity_limits || {})[id]
                  return (
                    <li class="dl-conn" key={id}>
                      <span class="dl-ng"><Icon name={/^team:/.test(id) ? 'obj-team' : /^bot:/.test(id) ? 'obj-bot' : 'obj-sandbox'} /></span>
                      <span class="dl-nt"><b>{whoWord(id)}</b><span class="dl-why">{l ? `Its own limit: ${usd(l.PerPurchase, cr)} a purchase, ${usd(l.PerDay, cr)} a day, ${usd(l.PerMonth, cr)} in 30 days` : 'The card’s limits'}</span></span>
                      <span class="dl-na"><button class="btn sm secondary" type="button" {...cfgOff} onClick={act({ k: 'who-rm', id })}>Remove</button></span>
                    </li>
                  )
                })}
              </ul>
            ) : (
              <p class="dl-sub">Nobody yet.</p>
            )}
          </section>
        </div>
      </div>

      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>Purchases with this card</h2></header>
        {ps.length ? <ul class="dl-list dl-needs">{ps.map((p) => <PurchaseRow key={p.id} p={p} card={c} />)}</ul> : <p class="dl-sub">Nothing bought with it yet.</p>}
      </section>
      <section class="dl-card wl-gap">
        <header class="dl-ch"><h2>The card’s details</h2><span class="dl-sub">never shown, never sent to a bot</span></header>
        <ul class="dl-list dl-conns wl-flushl">
          {Object.keys(CARDF).map((k) => {
            const f = (c.fields || {})[k]
            return (
              <li class="dl-conn" key={k}>
                <span class="dl-ng"><Icon name="lock" /></span>
                <span class="dl-nt"><b>{CARDF[k]}</b><span class="dl-why">{f ? <><span class="wl-masked" data-testid="masked-value">{k === 'number' ? `•••• ${c.last4 || ''}` : '••••'}</span> set · {srcWord(f)}</> : 'Not set'}</span></span>
                <span class="dl-na"><button class="btn sm secondary" type="button" data-testid={`card-set-${k}`} {...cfgOff} onClick={act({ k: 'set', f: k })}>{f ? 'Replace' : 'Set'}</button></span>
              </li>
            )
          })}
        </ul>
      </section>

      {menu ? (
        <Menu
          anchor={menu}
          label={`More for ${c.name}`}
          onClose={() => setMenu(null)}
          items={[
            { label: 'See it in the audit log', icon: 'logs', onClick: () => go('logs/audit') },
            '-',
            { label: 'Remove this card…', icon: 'trash', disabled: ed ? false : 'Defined in the config: remove it there', onClick: () => setModal({ k: 'remove' }) },
          ]}
        />
      ) : null}
      {modal?.k === 'limits' ? <LimitsModal c={c} onClose={() => setModal(null)} onSave={(l) => op('browser-card-bind', { limits: l }, 'Saved: the new limits apply to the next purchase')} /> : null}
      {modal?.k === 'shop' ? (
        <FormModal
          caps={c.name}
          title="Add a shop"
          intro="The card is filled only on this exact address, and only for a purchase you approve."
          fields={[{ k: 'origin', type: 'string', req: true, mono: true, label: 'The shop’s address', ph: 'https://shop.example.com', validate: (v) => originErr(v.replace(/\/$/, '')) || ((c.merchants || []).includes(v.replace(/\/$/, '')) ? 'Already on the list.' : '') }]}
          note="The browser must already be allowed to reach it. A checkout inside another site’s frame (hosted payment fields) isn’t filled yet."
          submit="Add with passkey"
          submitIcon="passkey"
          testid="card-shop-form"
          onSubmit={async (v) => {
            const o = v.origin.trim().replace(/\/$/, '')
            try {
              await walletOp('browser-card-bind', { name, add_merchants: [o] })
              done(`Added: ${c.name} may pay ${host(o)}`)
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
      {modal?.k === 'shop-rm' ? (
        <ConfirmModal title={`Stop ${c.name} paying ${host(modal.o)}?`} body="Nothing more is bought there with this card. Takes effect at once." ok="Remove" danger onOk={() => op('browser-card-bind', { remove_merchants: [modal.o] }, `Removed: ${c.name} won’t pay ${host(modal.o)} any more`)} onClose={() => setModal(null)} />
      ) : null}
      {modal?.k === 'who' ? (
        <FormModal
          caps={c.name}
          title="Let a bot or team use this card"
          fields={[{ k: 'id', type: 'enum', req: true, label: 'Who', options: idOptions(c.identities || []) }]}
          note="It can propose purchases with the card; every one still waits for your approval."
          submit="Allow with passkey"
          submitIcon="passkey"
          testid="card-who-form"
          onSubmit={async (v) => {
            if (!v.id) return 'Pick a bot or team.'
            try {
              await walletOp('browser-card-bind', { name, bind: [v.id] })
              done(`Allowed: ${whoWord(v.id)} may use ${c.name}`)
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
      {modal?.k === 'who-rm' ? (
        <ConfirmModal title={`Stop ${whoWord(modal.id)} using ${c.name}?`} body="It can no longer propose purchases with this card. Purchases already approved for it lapse unused." ok="Remove" danger onOk={() => op('browser-card-bind', { unbind: [modal.id] }, `Removed: ${whoWord(modal.id)} can’t use ${c.name} any more`)} onClose={() => setModal(null)} />
      ) : null}
      {modal?.k === 'set' ? (
        <ReplaceModal
          title={`${(c.fields || {})[modal.f] ? 'Replace' : 'Set'} the ${CARDF[modal.f].toLowerCase()}`}
          label={CARDF[modal.f]}
          onClose={() => setModal(null)}
          onSave={(w) => op('browser-card-set', { values: w.value ? { [modal.f]: w.value } : undefined, refs: w.ref ? { [modal.f]: w.ref } : undefined }, `Saved: the ${CARDF[modal.f].toLowerCase()} is set`)}
        />
      ) : null}
      {modal?.k === 'remove' ? (
        <ConfirmModal
          title={`Remove ${c.name}?`}
          body="Its stored values are deleted and nothing more can be bought with it. Approved purchases lapse unused. Receipts stay."
          ok="Remove"
          danger
          onOk={async () => {
            try {
              await walletOp('browser-card-rm', { name })
              go('browser/cards')
            } catch (e) {
              setModal(null)
              setErr(humanError(e))
            }
          }}
          onClose={() => setModal(null)}
        />
      ) : null}
    </div>
  )
}

/** Replace one value (a card's or a profile's detail): neutral, so no passkey; write-only either way. */
export function ReplaceModal({ title, label, caps = 'write-only', onClose, onSave }: { title: string; label: string; caps?: string; onClose: () => void; onSave: (w: { value?: string; ref?: string }) => Promise<void> | void }): JSX.Element {
  const [v, setV] = useState<VSValue>({ ...emptyVS() })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <BrModal
      caps={caps}
      title={title}
      onClose={onClose}
      testid="replace-modal"
      foot={
        <>
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button
            class="btn primary"
            type="button"
            disabled={busy}
            data-testid="replace-save"
            onClick={async () => {
              const w = vsToWire(v)
              if (!w.value && !w.ref) return setErr('Required: type it, or pick where it comes from.')
              setBusy(true)
              await onSave(w)
              setBusy(false)
            }}
          >
            Save
          </button>
        </>
      }
    >
      <Field k="v" label={label} req err={err} hint="The page will say set, never the value.">
        <ValueSource sensitive value={v} onChange={(x) => { setV(x); setErr('') }} placeholder="Type it once: stored write-only" testid="replace-value" />
      </Field>
    </BrModal>
  )
}

function LimitsModal({ c, onClose, onSave }: { c: WalletCard; onClose: () => void; onSave: (l: CardLimits) => void }): JSX.Element {
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(LIMF.map(([k]) => [k, String(c.limits[k] ?? '')])))
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const cr = cur(c)
  function save() {
    const n: Record<string, number> = {}
    const e: Record<string, string> = {}
    for (const [k] of LIMF) {
      n[k] = moneyOf(vals[k])
      if (!(n[k] > 0)) e[k] = 'An amount above 0.'
    }
    setErrs(e)
    if (Object.keys(e).length) return
    if (n.PerPurchase > n.PerDay || n.PerDay > n.PerMonth) return setFormErr('A purchase’s limit fits inside a day’s, and a day’s inside 30 days’.')
    onSave({ PerPurchase: n.PerPurchase, PerDay: n.PerDay, PerMonth: n.PerMonth, Currency: c.limits.Currency || 'USD' })
  }
  const up = LIMF.some(([k]) => moneyOf(vals[k]) > (c.limits[k] as number))
  return (
    <BrModal
      caps={c.name}
      title="Change the limits"
      onClose={onClose}
      testid="limits-modal"
      foot={
        <>
          <button class="btn ghost" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" data-testid="limits-save" onClick={save}><Icon name={up ? 'passkey' : 'check'} />{up ? 'Save with passkey' : 'Save'}</button>
        </>
      }
    >
      <p class="small">In {cr}. Raising one asks for your passkey; lowering one doesn’t.</p>
      {LIMF.map(([k, l]) => (
        <Field k={k} label={l} req forId={`w-m-${k}`} err={errs[k]} key={k}>
          <MoneyInput id={`w-m-${k}`} value={vals[k]} onInput={(v) => setVals({ ...vals, [k]: v })} />
        </Field>
      ))}
      {formErr ? <div class="callout danger small" role="alert">{formErr}</div> : null}
    </BrModal>
  )
}

// --- Add a card ---

const KH: Record<string, string> = {
  virtual: 'A virtual card with its own limits: good.',
  'single-use': 'A single-use card: good for one purchase.',
  physical: 'A physical card works, but conductor will remind you to prefer a virtual or single-use one.',
}

function NewCard(): JSX.Element {
  const [name, setName] = useState('')
  const [kind, setKind] = useState('virtual')
  const [vs, setVs] = useState<Record<string, VSValue>>({ number: emptyVS(), expiry: emptyVS(), cvc: emptyVS(), cardname: emptyVS() })
  const [lim, setLim] = useState<Record<string, string>>({ PerPurchase: '', PerDay: '', PerMonth: '' })
  const [shops, setShops] = useState<string[]>([])
  const [ids, setIds] = useState<string[]>([])
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [existing, setExisting] = useState<string[]>([])
  useEffect(() => { browser.wallet().then((r) => setExisting((r.cards || []).map((c) => c.name))).catch(() => {}) }, [])

  const vf = (k: string, label: string, req: boolean) => (
    <Field k={k} label={label} req={req} err={errs[k]}>
      <ValueSource sensitive value={vs[k]} onChange={(v) => setVs({ ...vs, [k]: v })} placeholder="Type it once: stored write-only" testid={`card-${k}`} />
    </Field>
  )

  async function save() {
    const e: Record<string, string> = {}
    if (!/^[A-Za-z0-9_-]{1,40}$/.test(name)) e.name = 'Letters, digits, - and _, up to 40.'
    else if (existing.includes(name)) e.name = 'A card by that name exists.'
    const n: Record<string, number> = {}
    for (const [k] of LIMF) {
      n[k] = moneyOf(lim[k])
      if (!(n[k] > 0)) e[k] = 'An amount in dollars, above 0 (up to two decimals).'
    }
    if (!e.PerPurchase && !e.PerDay && !e.PerMonth && (n.PerPurchase > n.PerDay || n.PerDay > n.PerMonth)) e.PerPurchase = 'A purchase fits inside a day, and a day inside 30 days.'
    const xs = shops.map((x) => x.trim().replace(/\/$/, '')).filter(Boolean)
    const badOne = xs.find((x) => originErr(x))
    const dup = xs.find((x, i) => xs.indexOf(x) !== i)
    if (badOne) e.merchants = `“${badOne}”: ${originErr(badOne)}`
    else if (dup) e.merchants = `${host(dup)} is listed twice.`
    if (!ids.length) e.who = 'Add at least one bot or team.'
    const w = Object.fromEntries(Object.entries(vs).map(([k, v]) => [k, vsToWire(v)]))
    if (!w.number.value && !w.number.ref) e.number = 'Required: type it, or pick where it comes from.'
    if (!w.expiry.value && !w.expiry.ref) e.expiry = 'Required: type it, or pick where it comes from.'
    setErrs(e)
    if (Object.keys(e).length) return
    const values: Record<string, string> = {}
    const refs: Record<string, string> = {}
    for (const [a, k] of [['number', 'number'], ['expiry', 'expiry'], ['cvc', 'cvc'], ['cardname', 'name']]) {
      if (w[a].value) values[k] = w[a].value!
      if (w[a].ref) refs[k] = w[a].ref!
    }
    setBusy(true)
    setFormErr('')
    try {
      await walletOp('browser-card-add', {
        name, kind, merchants: xs, identities: ids,
        limits: { PerPurchase: n.PerPurchase, PerDay: n.PerDay, PerMonth: n.PerMonth, Currency: 'USD' },
        values, refs,
      })
      go(`browser/cards/${encodeURIComponent(name)}`)
    } catch (err) {
      setFormErr(humanError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="wl-page">
      <div class="page-head">
        <div>
          <BrCrumbs trail={[['Browsers', '#/browser'], ['Payment cards', '#/browser/cards'], ['Add a card']]} />
          <h1>Add a card</h1>
        </div>
      </div>
      <form class="dl-card wl-form" id="w-nc" noValidate onSubmit={(e) => e.preventDefault()}>
        <div class="callout small wl-tip"><Icon name="shield" /><span><b>Use a limited card.</b> A virtual or single-use card with its own limits (a masked card from 1Password, Privacy.com, Stripe Issuing) is the second layer behind conductor’s checks.</span></div>
        <Field k="name" label="Name for this card" req forId="w-nc-name" err={errs.name} hint="How you and your bots refer to this card, on purchases and receipts. Letters, digits, - and _.">
          <input class="input mono" id="w-nc-name" placeholder="household" autocomplete="off" value={name} onInput={(e) => setName((e.target as HTMLInputElement).value.trim())} />
        </Field>
        <div class="field" data-field="kind">
          <span class="flabel">Kind</span>
          <div class="seg" role="group" aria-label="Kind" id="w-nc-kind">
            {[['virtual', 'Virtual'], ['single-use', 'Single-use'], ['physical', 'Physical']].map(([k, l]) => (
              <button type="button" class={kind === k ? 'on' : ''} aria-pressed={kind === k} onClick={() => setKind(k)} key={k}>{l}</button>
            ))}
          </div>
          <div class="hint" id="w-nc-kh">{KH[kind]}</div>
        </div>
        <h3 class="wl-h">The card</h3>
        <p class="dl-sub">Each typed once (stored write-only) or from a vault item or an environment variable. The page will only ever show the last four digits.</p>
        <div class="wl-grid2">
          {vf('number', 'Card number', true)}
          {vf('expiry', 'Expiry (MM/YY)', true)}
          {vf('cvc', 'Security code', false)}
          {vf('cardname', 'Name on the card', false)}
        </div>
        <h3 class="wl-h">Limits</h3>
        <p class="dl-sub">In US dollars; checked when a purchase is proposed, when you approve it and when it’s paid. A purchase fits inside a day, and a day inside 30 days.</p>
        <div class="wl-grid3">
          {LIMF.map(([k, l]) => (
            <Field k={k} label={l} req forId={`w-m-${k}`} err={errs[k]} key={k}>
              <MoneyInput id={`w-m-${k}`} value={lim[k]} onInput={(v) => setLim({ ...lim, [k]: v })} />
            </Field>
          ))}
        </div>
        <h3 class="wl-h">Where, and who</h3>
        <Field k="merchants" label="Shops it may pay" err={errs.merchants} hint="Each shop’s exact address (https:// and the host). None by default: until you add one, nothing can be bought with it.">
          <div class="wl-rows" data-rows="merchants">
            {shops.map((x, i) => {
              const v = x.trim().replace(/\/$/, '')
              const wrong = !!v && (!!originErr(v) || shops.filter((y) => y.trim().replace(/\/$/, '') === v).length > 1)
              return (
                <div class="wl-row" key={i}>
                  <input class={'input mono' + (wrong && errs.merchants ? ' wl-bad' : '')} aria-invalid={wrong && errs.merchants ? 'true' : 'false'} value={x} placeholder="https://shop.example.com" aria-label="A shop’s exact address" autocomplete="off" onInput={(e) => setShops(shops.map((y, j) => (j === i ? (e.target as HTMLInputElement).value : y)))} />
                  <button type="button" class="icon-btn" aria-label="Remove this shop" onClick={() => { setShops(shops.filter((_, j) => j !== i)); setErrs({ ...errs, merchants: '' }) }}><Icon name="trash" /></button>
                </div>
              )
            })}
          </div>
          <button type="button" class="btn sm secondary wl-row-add" onClick={() => setShops([...shops, ''])}><Icon name="plus" />Add a shop</button>
        </Field>
        <Field k="who" label="Who may use it" req err={errs.who} hint="Bots and teams. Each may propose purchases; every one waits for your approval.">
          <WhoPicker id="w-who" value={ids} onChange={setIds} />
        </Field>
        {formErr ? <div class="form-err callout danger small" role="alert" id="w-nc-err">{formErr}</div> : null}
        <div class="wizard-foot">
          <a class="btn ghost" href="#/browser/cards">Cancel</a>
          <span class="spacer" />
          <button type="button" class="btn primary" disabled={busy} data-testid="card-save" onClick={() => void save()}><Icon name="passkey" />Save with passkey</button>
        </div>
      </form>
    </div>
  )
}
