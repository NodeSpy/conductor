// Small formatting helpers. Durations read in words ("2 h 30 min"); times read
// as a compact "time since" (design §3.2: ids and times never bare).

export function humanDuration(ms: number): string {
  if (ms <= 0) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return s % 60 ? `${m} min ${s % 60} s` : `${m} min`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d} d ${h % 24} h` : `${d} d`
}

// goDuration reads a Go duration string ("30m0s", "12h0m0s", "1h30m",
// "1.5s", "250ms") as words via humanDuration; anything else passes through.
export function goDuration(s: string): string {
  if (!s) return '—'
  const unit: Record<string, number> = { h: 3_600_000, m: 60_000, s: 1000, ms: 1, us: 0.001, 'µs': 0.001, ns: 0.000001 }
  const re = /(\d+(?:\.\d+)?)(ms|us|µs|ns|h|m|s)/gy
  let ms = 0
  let m: RegExpExecArray | null
  let end = 0
  while ((m = re.exec(s)) !== null) {
    ms += parseFloat(m[1]) * unit[m[2]]
    end = re.lastIndex
  }
  if (end !== s.length || end === 0) return s
  return ms < 1000 && ms > 0 ? `${Math.round(ms)} ms` : humanDuration(ms)
}

// ago is a compact "time since", already ending in "ago" ("5 min ago"), or
// "just now" — callers must not add their own "ago".
export function ago(iso: string): string {
  if (!iso) return '—'
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return '—'
  const d = Date.now() - then
  if (d < 60_000) return 'just now'
  if (d < 3_600_000) return `${Math.floor(d / 60_000)} min ago`
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)} h ago`
  return `${Math.floor(d / 86_400_000)} d ago`
}

export function uptime(sec: number): string {
  return humanDuration(sec * 1000)
}
