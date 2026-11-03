const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['day', 86400],
  ['hour', 3600],
  ['minute', 60],
]

/** How long before now an ISO time was: "5 minutes ago", "just now" under a minute; empty for no or a broken time. */
export function ago(iso: string | undefined, now = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN
  if (Number.isNaN(t)) return ''
  const seconds = Math.round((now - t) / 1000)
  const unit = UNITS.find(([, size]) => seconds >= size)
  if (!unit) return 'just now'
  return new Intl.RelativeTimeFormat('en', { numeric: 'always' }).format(-Math.floor(seconds / unit[1]), unit[0])
}
