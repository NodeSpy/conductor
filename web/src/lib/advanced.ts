// Show advanced (Preferences): technical details and the YAML of a form start
// open everywhere, instead of one click away. A display preference of this
// browser only. Every such fold in the app is a <details class="dl-tech-d">
// (the prototype's technical-details fold), so one observer covers them all.
const KEY = 'pref-advanced'

export function advancedOn(): boolean {
  return localStorage.getItem(KEY) === '1'
}

export function setAdvanced(v: boolean): void {
  localStorage.setItem(KEY, v ? '1' : '0')
  if (v) openAll(document)
}

function openAll(root: ParentNode): void {
  root.querySelectorAll<HTMLDetailsElement>('details.dl-tech-d:not([data-adv])').forEach((d) => {
    d.dataset.adv = '1'
    d.open = true
  })
}

/** Starts watching the page: a new fold opens as it appears while the preference is on. */
export function watchAdvanced(): void {
  if (typeof MutationObserver === 'undefined') return
  new MutationObserver(() => {
    if (advancedOn()) openAll(document)
  }).observe(document.body, { childList: true, subtree: true })
  if (advancedOn()) openAll(document)
}
