// useHashFlag lets a deep link open a page's own dialog: when the hash carries
// ?<key>=1 (#/bots?new=1, #/connections?add=1) the page runs `open` once and
// drops the query, so Back and a reload don't reopen it. It also fires when
// the flag arrives while the page is already showing (the router's route
// signal ignores the query, so the page doesn't remount for it).
import { useEffect } from 'preact/hooks'
import { query } from '../router'

export function useHashFlag(key: string, open: () => void): void {
  useEffect(() => {
    const check = () => {
      if (query(key) !== '1') return
      history.replaceState(null, '', location.hash.split('?')[0] || '#/')
      open()
    }
    check()
    window.addEventListener('hashchange', check)
    return () => window.removeEventListener('hashchange', check)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
}
