/**
 * The friendly schedules the job form offers, and the cron expression each
 * one writes. A cron that matches none of them stays a custom expression.
 * Weekdays are numbered as cron numbers them: 0 is Sunday.
 */
export type Schedule =
  | { kind: 'minute' }
  | { kind: 'minutes'; every: number }
  | { kind: 'hourly'; minute: number }
  | { kind: 'daily'; hour: number; minute: number }
  | { kind: 'weekly'; days: number[]; hour: number; minute: number }
  | { kind: 'monthly'; day: number; hour: number; minute: number }
  | { kind: 'custom'; cron: string }

export type ScheduleKind = Schedule['kind']

/** The "every N minutes" choices: each divides an hour, so runs are evenly spaced. */
export const MINUTE_STEPS = [2, 5, 10, 15, 20, 30]

/** Ranges for runs of three or more days in a row ("1-5"), single days otherwise. */
function dayList(days: number[]): string {
  const sorted = [...new Set(days)].sort((a, b) => a - b)
  const parts: string[] = []
  for (let i = 0; i < sorted.length; ) {
    let j = i
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++
    if (j - i >= 2) parts.push(`${sorted[i]}-${sorted[j]}`)
    else for (let k = i; k <= j; k++) parts.push(String(sorted[k]))
    i = j + 1
  }
  return parts.join(',')
}

/** The cron expression of a schedule; '' for a weekly one with no day chosen. */
export function toCron(s: Schedule): string {
  switch (s.kind) {
    case 'minute':
      return '* * * * *'
    case 'minutes':
      return `*/${s.every} * * * *`
    case 'hourly':
      return `${s.minute} * * * *`
    case 'daily':
      return `${s.minute} ${s.hour} * * *`
    case 'weekly':
      return s.days.length ? `${s.minute} ${s.hour} * * ${dayList(s.days)}` : ''
    case 'monthly':
      return `${s.minute} ${s.hour} ${s.day} * *`
    case 'custom':
      return s.cron.trim().split(/\s+/).join(' ')
  }
}

const DAY_NAMES = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

function int(field: string, min: number, max: number): number | undefined {
  if (!/^\d{1,2}$/.test(field)) return undefined
  const n = Number(field)
  return n >= min && n <= max ? n : undefined
}

function day(field: string): number | undefined {
  const named = DAY_NAMES.indexOf(field.toLowerCase())
  if (named >= 0) return named
  const n = int(field, 0, 7)
  return n === 7 ? 0 : n
}

/** The weekdays of a weekday field ("1-5", "MON,FRI", "7"), or undefined when it is not a plain list. */
function days(field: string): number[] | undefined {
  const out = new Set<number>()
  for (const part of field.split(',')) {
    const [a, b, extra] = part.split('-')
    if (extra !== undefined) return undefined
    const from = day(a)
    const to = b === undefined ? from : day(b)
    if (from === undefined || to === undefined || (b !== undefined && to < from)) return undefined
    for (let d = from; d <= to; d++) out.add(d)
  }
  return [...out].sort((x, y) => x - y)
}

/** The preset a cron expression is, or custom when it is none of them. */
export function fromCron(cron: string): Schedule {
  const custom: Schedule = { kind: 'custom', cron: cron.trim() }
  const f = cron.trim().split(/\s+/)
  if (f.length !== 5) return custom
  const [mi, h, dom, mon, dow] = f
  if (mon !== '*') return custom
  if (h === '*' && dom === '*' && dow === '*') {
    if (mi === '*') return { kind: 'minute' }
    const step = mi.match(/^\*\/(\d+)$/)
    if (step) return MINUTE_STEPS.includes(Number(step[1])) ? { kind: 'minutes', every: Number(step[1]) } : custom
    const minute = int(mi, 0, 59)
    return minute === undefined ? custom : { kind: 'hourly', minute }
  }
  const minute = int(mi, 0, 59)
  const hour = int(h, 0, 23)
  if (minute === undefined || hour === undefined) return custom
  if (dom === '*' && dow === '*') return { kind: 'daily', hour, minute }
  if (dom === '*') {
    const ds = days(dow)
    return ds ? { kind: 'weekly', days: ds, hour, minute } : custom
  }
  if (dow === '*') {
    const d = int(dom, 1, 31)
    return d === undefined ? custom : { kind: 'monthly', day: d, hour, minute }
  }
  return custom
}
