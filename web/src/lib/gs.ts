// Get started's Hide (Home) and Show Get started (the account menu): one
// display preference of this browser, as a signal so both update at once.
import { signal } from '@preact/signals'

export const gsHidden = signal(typeof localStorage !== 'undefined' && localStorage.getItem('gs-hidden') === '1')

export function setGsHidden(v: boolean): void {
  if (v) localStorage.setItem('gs-hidden', '1')
  else localStorage.removeItem('gs-hidden')
  gsHidden.value = v
}
