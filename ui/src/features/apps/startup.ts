import type { Step } from '../../api'

function at(s: Step | undefined): number | undefined {
  const t = s?.since ? Date.parse(s.since) : NaN
  return Number.isNaN(t) ? undefined : t
}

/**
 * Seconds from getting a machine (Scheduled) to answering (Ready), from the
 * API's step times. Undefined unless both steps are done and have times.
 */
export function startupSeconds(steps: Step[]): number | undefined {
  const first = steps.find((s) => s.type === 'Scheduled')
  const ready = steps.find((s) => s.type === 'Ready')
  if (first?.status !== 'True' || ready?.status !== 'True') return undefined
  const a = at(first)
  const b = at(ready)
  return a === undefined || b === undefined ? undefined : Math.max(0, Math.round((b - a) / 1000))
}

/** When the app last became ready (its Ready step turned True); undefined unless it is ready and the API sent the time. */
export function readySince(steps: Step[]): number | undefined {
  const ready = steps.find((s) => s.type === 'Ready')
  return ready?.status === 'True' ? at(ready) : undefined
}

/**
 * How long each done step took after the step before it, as "n s". The
 * first step has no step before it, so it has no time.
 */
export function stepTimes(steps: Step[]): (string | undefined)[] {
  return steps.map((s, i) => {
    if (i === 0 || s.status !== 'True' || steps[i - 1].status !== 'True') return undefined
    const a = at(steps[i - 1])
    const b = at(s)
    return a === undefined || b === undefined ? undefined : `${Math.max(0, Math.round((b - a) / 1000))} s`
  })
}
