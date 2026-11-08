// The wallet screens' words (js/screens/wallet.js "words"): a profile's
// details by what they mean, where a value comes from, what a bot may learn,
// a card's kind and name, money in one format. Never a value.
import type { FieldView, WalletCard } from '../api/browser'

export const usd = (n: number, cur = 'USD'): string => {
  try {
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: cur, currencyDisplay: cur === 'USD' ? 'narrowSymbol' : 'code' }).format(n || 0)
  } catch {
    return `${(+n || 0).toFixed(2)} ${cur}`
  }
}

export const KIND: Record<string, string> = { virtual: 'Virtual card', 'single-use': 'Single-use card', physical: 'Physical card', '': 'Card' }
export const kindOf = (k?: string): string => KIND[k || ''] || 'Card'
export const cardName = (c?: Pick<WalletCard, 'brand' | 'last4'> | null): string => `${c?.brand || 'Card'} •••• ${c?.last4 || '····'}`

const FW: Record<string, string> = {
  name: 'Name', given_name: 'First name', family_name: 'Last name', email: 'Email', phone: 'Phone', company: 'Company', birthday: 'Birthday',
  line1: 'Street', line2: 'Street, line 2', city: 'City', region: 'Region', postal_code: 'Postcode', country: 'Country',
}
export function fieldWord(f: string): string {
  const [a, b] = String(f).split('.')
  if (a === 'address') return FW[b] || b
  if (a === 'billing') return `Billing ${String(FW[b] || b).toLowerCase()}`
  if (a === 'custom') return (b || '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())
  return FW[a] || a
}
export const siteFieldWord = (f: string): string => (f === 'address' ? 'the address' : f === 'billing' ? 'the billing address' : fieldWord(f).toLowerCase())

// What a bot may learn about a detail (hidden by default).
export const FACT: Record<string, string> = { email: 'its domain', phone: 'its country code', birthday: 'whether 18 or older', 'address.country': 'the country', 'billing.country': 'the country' }
export const VIS: [string, string][] = [['hidden', 'Nothing'], ['exists', 'Whether it’s set'], ['fact', 'A fact'], ['value', 'The value']]
export const RANK: Record<string, number> = { hidden: 0, exists: 1, fact: 2, value: 3 }
export const visWord = (f: string, v: string): string => (v === 'exists' ? 'whether it’s set' : v === 'fact' ? FACT[f] || 'a fact about it' : v === 'value' ? 'the value' : 'nothing')
export const visOpts = (f: string): [string, string][] => VIS.filter(([k]) => k !== 'fact' || FACT[f])
const LEARN: Record<string, string> = { email: 'the email’s domain', phone: 'the phone’s country code', birthday: 'whether you’re 18 or older', 'address.country': 'the country', 'billing.country': 'the billing country' }
export const learnWord = (f: string, v: string): string =>
  v === 'exists' ? `whether the ${fieldWord(f).toLowerCase()} is set` : v === 'fact' ? LEARN[f] || `a fact about the ${fieldWord(f).toLowerCase()}` : v === 'value' ? `the ${fieldWord(f).toLowerCase()} itself` : ''

/** Where a value comes from, in words; never the value. */
export function srcWord(s?: FieldView | null): string {
  if (!s || s.kind === 'stored' || s.kind === 'literal') return 'typed in, stored write-only'
  const v = String(s.source || '')
  let x
  if ((x = v.match(/vault "(\w+)" "([^"]+)"/))) return `${({ op: '1Password', house: 'the conductor vault', store: 'pass' } as Record<string, string>)[x[1]] || x[1]} · ${x[2]}`
  if ((x = v.match(/env\.(\w+)/))) return `environment variable ${x[1]}`
  if ((x = v.match(/secret\.(\w+)/))) return `secret ${x[1]}`
  return v
}

export const CARDF: Record<string, string> = { number: 'Number', expiry: 'Expiry', cvc: 'Security code', name: 'Name on the card' }
