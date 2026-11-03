const DATE = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })

/** "1 Nov, 09:00" on the reader's clock; "Never" when absent. */
export function when(iso?: string | null): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : DATE.format(d)
}

/** Bytes in the largest unit that keeps a number of at least 1. */
export function bytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

/** "1 app", "3 apps". */
export function count(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

/** How long since a time, in plain words: "under a minute", "14 min", "2 h 14 min", "3 days". */
export function duration(since: number, now = Date.now()): string {
  const min = Math.max(0, Math.floor((now - since) / 60000))
  let words: string
  if (min < 1) words = 'under a minute'
  else if (min < 60) words = `${min} min`
  else if (min < 1440) words = `${Math.floor(min / 60)} h${min % 60 ? ` ${min % 60} min` : ''}`
  else words = count(Math.floor(min / 1440), 'day')
  // No-break spaces keep the duration on one line when a narrow column wraps.
  return words.replaceAll(' ', '\u00a0')
}
