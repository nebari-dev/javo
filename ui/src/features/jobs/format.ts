import cronstrue from 'cronstrue'
import type { Engine, RunView, Template } from '../../api'
import type { Status } from '../../components/Pill'

/** Whether the locale (the reader's when not given) tells time on a 12 hour clock. */
function uses12Hour(locale?: string): boolean {
  const cycle = new Intl.DateTimeFormat(locale, { hour: 'numeric' }).resolvedOptions().hourCycle
  return cycle === 'h11' || cycle === 'h12'
}

/** The schedule in words on the reader's clock ("At 06:00, only on Monday"); the expression itself when it cannot be read. */
export function cronText(cron: string, locale?: string): string {
  try {
    return cronstrue.toString(cron, { throwExceptionOnParseError: true, use24HourTimeFormat: !uses12Hour(locale) })
  } catch {
    return cron
  }
}

function inZone(iso: string, timeZone: string, withDate: boolean): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  try {
    const opts: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }
    if (withDate) Object.assign(opts, { weekday: 'short', day: 'numeric', month: 'short' })
    return `${new Intl.DateTimeFormat('en-GB', opts).format(d)} ${timeZone}`
  } catch {
    return ''
  }
}

/** A time always with its zone: "Mon 2 Nov, 06:00 Europe/Berlin" (UTC when the job has no schedule). */
export function when(iso: string | undefined, timeZone = 'UTC'): string {
  return iso ? inZone(iso, timeZone, true) : ''
}

/** "05:00 UTC" style text for the same instant. */
export function whenUtc(iso: string): string {
  return inZone(iso, 'UTC', true)
}

/** "10 succeeded, 2 failed": the strip's text alternative, counted in the order outcomes first appear. */
export function outcomeSummary(recent: string[]): string {
  const counts = new Map<string, number>()
  for (const o of recent) counts.set(o, (counts.get(o) ?? 0) + 1)
  return [...counts].map(([o, n]) => `${n} ${o.toLowerCase()}`).join(', ')
}

const FAILED = new Set(['Failed', 'TimedOut', 'Lost', 'Missed'])

export const isFailedOutcome = (outcome: string): boolean => FAILED.has(outcome)

/** The pill colour for a run outcome. */
export function outcomeStatus(outcome: string): Status {
  if (outcome === 'Succeeded') return 'running'
  if (outcome === 'Running') return 'starting'
  if (FAILED.has(outcome)) return 'failed'
  if (outcome === 'Skipped' || outcome === 'Cancelled') return 'stopped'
  return 'pending'
}

/** "TimedOut" as "Timed out". */
export function outcomeLabel(outcome: string): string {
  return outcome.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/ [A-Z]/g, (m) => m.toLowerCase())
}

/** "1m 30s" between a run's start and finish; empty until both are known. */
export function duration(run: Pick<RunView, 'startTime' | 'finishTime'>): string {
  if (!run.startTime || !run.finishTime) return ''
  const s = Math.round((Date.parse(run.finishTime) - Date.parse(run.startTime)) / 1000)
  if (!(s >= 0)) return ''
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`
}

/** Engines a template offers, in the template's order. */
export function templateEngines(t: Template | undefined, engines: Engine[]): Engine[] {
  const ids = t?.jobs?.engines ?? []
  return ids.map((id) => engines.find((e) => e.id === id)).filter((e): e is Engine => !!e)
}

/** The engine that runs a file, chosen by its extension from the template's engines. */
export function engineForFile(path: string, offered: Engine[]): Engine | undefined {
  const lower = path.toLowerCase()
  return offered.find((e) => (e.extensions ?? []).some((x) => lower.endsWith(x.toLowerCase())))
}

/** The kind whose logo stands for an engine; unknown engines get the neutral terminal mark. */
export function engineKind(engine: string): string {
  return engine === 'papermill' ? 'jupyterlab' : engine
}
