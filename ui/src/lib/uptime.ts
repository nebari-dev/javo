const S = 1000
const MIN = 60 * S
const H = 60 * MIN
const D = 24 * H

/** How long an app has been up, from milliseconds: "45 s", "12 min", "2 h 14 min", "3 d 4 h". */
export function uptime(ms: number): string {
  const t = Math.max(0, ms)
  if (t < MIN) return `${Math.floor(t / S)} s`
  if (t < H) return `${Math.floor(t / MIN)} min`
  const pair = (big: number, bigUnit: string, small: number, smallUnit: string) => (small ? `${big} ${bigUnit} ${small} ${smallUnit}` : `${big} ${bigUnit}`)
  if (t < D) return pair(Math.floor(t / H), 'h', Math.floor((t % H) / MIN), 'min')
  return pair(Math.floor(t / D), 'd', Math.floor((t % D) / H), 'h')
}

/** Milliseconds until uptime(ms) can next change: the next second under a minute, the next minute after. */
export function untilNextChange(ms: number): number {
  if (ms < 0) return -ms
  const step = ms < MIN ? S : MIN
  return step - (ms % step)
}
