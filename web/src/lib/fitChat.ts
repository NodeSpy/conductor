// useFitChat ports the prototype's UI.fitChat (js/chat.js): a chat panel that
// is a page's main part fits what's left of the visible viewport below the
// page's head and tabs, so only its thread scrolls and the composer stays on
// screen (above the phone's tab bar). It refits on resize. A fixed CSS offset
// can't know how tall the head above it is.
import { useEffect } from 'preact/hooks'

export function fitChats(): void {
  const main = document.getElementById('main')
  if (!main) return
  const bar = document.querySelector<HTMLElement>('nav.tabbar')
  const barTop = bar && bar.offsetParent ? bar.getBoundingClientRect().top : Infinity
  const vv = window.visualViewport
  const phone = innerWidth <= 720
  main.querySelectorAll<HTMLElement>('.chat.fit').forEach((c) => {
    const box = c.querySelector<HTMLElement>('#msgs')
    const atEnd = !box || !c.dataset.fitted || box.scrollHeight - box.scrollTop - box.clientHeight < 40
    c.dataset.fitted = '1'
    const bottom = Math.min(vv ? vv.height + vv.offsetTop : innerHeight, barTop, innerHeight) - (phone ? 12 : 24)
    const top = c.getBoundingClientRect().top + main.scrollTop
    c.style.height = `${Math.max(phone ? 240 : 320, Math.floor(bottom - top))}px`
    if (box && atEnd) box.scrollTop = box.scrollHeight
    const side = c.parentElement?.querySelector<HTMLElement>(':scope > .cm-side')
    if (side) {
      side.classList.add('fit-side')
      side.style.maxHeight = phone ? '' : c.style.height
    }
  })
}

export function useFitChat(deps: unknown[] = []): void {
  useEffect(() => {
    const run = () => requestAnimationFrame(fitChats)
    run()
    const t = setTimeout(run, 300)
    addEventListener('resize', run)
    window.visualViewport?.addEventListener('resize', run)
    return () => {
      clearTimeout(t)
      removeEventListener('resize', run)
      window.visualViewport?.removeEventListener('resize', run)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}
