// "Buying, and your details" on a bot's What it can do (design §4.5.1): per card
// that names the bot or its team, what it may buy — asks you first, up to the
// limits, at the shops, with the card (brand + last four) — and the profiles it
// may fill. Money always asks; "Without asking" is never unless an operator opts
// a bot in. Nothing here is a value.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { browser, money, who, hostOf, type WalletCard, type WalletProfile } from '../api/browser'

function names(ids: string[] | null, bot: string, team: string): boolean {
  if (!ids) return false
  return ids.some((id) => id === 'bot:' + bot || (team && id === 'team:' + team))
}

export function BotWallet({ bot, team }: { bot: string; team: string }): JSX.Element | null {
  const [cards, setCards] = useState<WalletCard[]>([])
  const [profiles, setProfiles] = useState<WalletProfile[]>([])
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    browser
      .wallet()
      .then((r) => {
        setCards((r.cards || []).filter((c) => names(c.identities, bot, team)))
        setProfiles((r.profiles || []).filter((p) => names(p.identities, bot, team)))
      })
      .catch(() => {})
      .finally(() => setLoaded(true))
  }, [bot, team])

  if (!loaded || (cards.length === 0 && profiles.length === 0)) return null

  return (
    <section class="dl-card wl-bot" id="wl-bot">
      <h3>Buying, and your details</h3>
      {cards.length > 0 ? (
        <ul class="wl-cans">
          {cards.map((c) => (
            <li class="wl-can" data-card={c.name}>
              <p>
                <b>Can buy:</b>{' '}
                {c.frozen
                  ? `nothing with ${c.name} while it's frozen.`
                  : c.merchants && c.merchants.length
                  ? `asks you first, up to ${money(c.limits.PerPurchase, c.limits.Currency)} a purchase, ${money(c.limits.PerDay, c.limits.Currency)} a day and ${money(c.limits.PerMonth, c.limits.Currency)} in 30 days, at ${c.merchants.map(hostOf).join(', ')}, with ${c.name} (${c.brand} •••• ${c.last4}).`
                  : `nothing yet: ${c.name} has no shop.`}
              </p>
            </li>
          ))}
        </ul>
      ) : null}
      <p class="wl-un"><b>Without asking:</b> never. Every purchase waits for your approval.</p>
      {profiles.length > 0 ? (
        <p class="wl-fills">
          <b>Fills your details:</b>{' '}
          {profiles
            .map((p) => `${p.name} on ${(p.sites || []).map((s) => hostOf(s.origin)).join(', ') || 'no site yet'} (${p.fields ? Object.keys(p.fields).join(', ') : ''})`)
            .join('; ')}
          . It never sees them.
        </p>
      ) : null}
      <p class="muted small">The card never reaches it: conductor fills it, only on a shop's page, only for a purchase you approved. {who('bot:' + bot)} proposes; you approve.</p>
    </section>
  )
}
