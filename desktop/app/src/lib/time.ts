// A plain-words age, matching the prototype's UI.ageText/UI.agoText style
// (short, no seconds precision beyond a minute).
export function ageText(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const min = Math.max(0, Math.round(ms / 60000))
  if (min < 1) return 'just now'
  if (min < 60) return `${min}m`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr}h`
  const day = Math.round(hr / 24)
  return `${day}d`
}
