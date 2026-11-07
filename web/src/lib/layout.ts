// usePhoneLayout mirrors app.css's own phone breakpoint (max-width: 720px),
// so a screen that renders two parallel layouts for the same data (Shell's
// rail vs. the tab bar + More; Activity's desktop table vs. its phone groups)
// can choose one instead of relying on CSS alone to hide the other. CSS-only
// hiding (display: none) is invisible to a person, but a plain-text or
// role-based locator still matches the hidden copy — a duplicate that broke
// phone.spec.ts's "More carries Settings on a phone" (Shell's rail nav and
// More.tsx both had a "System" link) and would do the same to Activity's
// "Finished" group and its "Show N more" button (present in both the table
// and the phone groups, with the phone disclosure's own "Show"/"Hide" button
// a second, invisible match for the table's "Show N more").
import { useEffect, useState } from 'preact/hooks'

export function usePhoneLayout(): boolean {
  const [phone, setPhone] = useState(() => (typeof matchMedia === 'undefined' ? false : matchMedia('(max-width: 720px)').matches))
  useEffect(() => {
    if (typeof matchMedia === 'undefined') return
    const mq = matchMedia('(max-width: 720px)')
    const onChange = () => setPhone(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return phone
}
